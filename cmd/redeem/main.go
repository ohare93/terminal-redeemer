package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/capture"
	"github.com/jmo/terminal-redeemer/internal/collector"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/diff"
	"github.com/jmo/terminal-redeemer/internal/doctor"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/niri"
	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/prune"
	"github.com/jmo/terminal-redeemer/internal/replay"
	"github.com/jmo/terminal-redeemer/internal/restore"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
	"github.com/jmo/terminal-redeemer/internal/tui"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	globalFlags, remainingArgs, err := parseGlobalFlags(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid global flags: %v\n", err)
		return 2
	}

	args = remainingArgs
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}

	if args[0] == "doctor" {
		return runDoctor(globalFlags, stdout)
	}

	resolvedConfig, err := config.Load(globalFlags.configPath, globalFlags.explicitConfig)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "config load failed: %v\n", err)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		printHelp(stdout)
		return 0
	case "capture":
		return runCapture(args[1:], resolvedConfig, stdout, stderr)
	case "history":
		return runHistory(args[1:], resolvedConfig, stdout, stderr)
	case "restore":
		return runRestore(args[1:], resolvedConfig, stdout, stderr)
	case "prune":
		return runPrune(args[1:], resolvedConfig, stdout, stderr)
	case "bottle":
		_, _ = fmt.Fprintf(stderr, "subcommand '%s' scaffolded but not implemented yet\n", args[0])
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return 2
	}
}

func runDoctor(flags globalFlags, stdout io.Writer) int {
	resolvedConfig, err := config.Load(flags.configPath, flags.explicitConfig)
	if err != nil {
		resolvedConfig = config.Defaults()
	}

	checks := []doctor.Check{
		doctor.StateDirWritableCheck{StateDir: resolvedConfig.StateDir},
		doctor.ConfigLoadCheck{Path: flags.configPath, Explicit: flags.explicitConfig},
		doctor.NiriSourceCheck{
			FixturePath: strings.TrimSpace(os.Getenv("REDEEM_NIRI_FIXTURE")),
			Command:     captureNiriCommandDefault(resolvedConfig),
		},
		doctor.CommandAvailableCheck{CheckName: "kitty_available", Command: resolvedConfig.Restore.Terminal.Command},
		doctor.CommandAvailableCheck{CheckName: "zellij_available", Command: "zellij"},
		doctor.EventsIntegrityCheck{StateDir: resolvedConfig.StateDir},
		doctor.SnapshotsIntegrityCheck{StateDir: resolvedConfig.StateDir},
	}

	results := doctor.Run(context.Background(), checks)
	for _, result := range results {
		_, _ = fmt.Fprintf(stdout, "doctor_check name=%s status=%s detail=%s\n", result.Name, result.Status, result.Detail)
	}

	summary := doctor.Summarize(results)
	_, _ = fmt.Fprintf(stdout, "doctor_summary total=%d passed=%d failed=%d\n", summary.Total, summary.Passed, summary.Failed)

	if doctor.HasFailures(results) {
		return 1
	}
	return 0
}

func runRestore(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: redeem restore <apply|tui> [flags]")
		return 2
	}
	if isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem restore <apply|tui> [flags]")
		return 0
	}
	if args[0] == "tui" {
		return runRestoreTUI(args[1:], resolvedConfig, stdout, stderr)
	}
	if args[0] != "apply" {
		_, _ = fmt.Fprintf(stderr, "unknown restore subcommand: %s\n", args[0])
		return 2
	}

	fs := flag.NewFlagSet("restore apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	atRaw := fs.String("at", "", "timestamp (RFC3339)")
	yes := fs.Bool("yes", false, "apply plan without prompt")
	dryRun := fs.Bool("dry-run", false, "print restore actions without executing")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if strings.TrimSpace(*atRaw) == "" {
		_, _ = fmt.Fprintln(stderr, "restore apply requires --at")
		return 2
	}
	at, err := parseAtSpec(*atRaw, time.Now().UTC())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid --at: %v\n", err)
		return 2
	}

	engine, err := replay.NewEngine(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "restore init failed: %v\n", err)
		return 1
	}
	state, err := engine.At(at)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "restore replay failed: %v\n", err)
		return 1
	}

	planner := restore.NewPlanner(restore.PlannerConfig{Terminal: restore.TerminalConfig{Command: resolvedConfig.Restore.Terminal.Command, ZellijAttachOrCreate: resolvedConfig.Restore.Terminal.ZellijAttachOrCreate}, AppAllowlist: resolvedConfig.Restore.AppAllowlist})
	plan := planner.Build(state)
	if *dryRun {
		printRestoreDryRun(stdout, plan)
		return 0
	}

	if !*yes {
		summary := summarizePlan(plan)
		_, _ = fmt.Fprintf(stdout, "restore_plan ready=%d skipped=%d degraded=%d\n", summary.ready, summary.skipped, summary.degraded)
		_, _ = fmt.Fprintln(stdout, "pass --yes to execute")
		return 0
	}

	executor := restore.NewExecutor(restore.ShellRunner{})
	result := executor.Execute(context.Background(), plan)
	printRestoreExecution(stdout, result)
	return 0
}

func printRestoreDryRun(stdout io.Writer, plan restore.Plan) {
	readyItems := make([]restore.Item, 0)
	degradedItems := make([]restore.Item, 0)
	skippedItems := make([]restore.Item, 0)

	for _, item := range plan.Items {
		switch item.Status {
		case restore.StatusReady:
			readyItems = append(readyItems, item)
		case restore.StatusDegraded:
			degradedItems = append(degradedItems, item)
		default:
			skippedItems = append(skippedItems, item)
		}
	}

	_, _ = fmt.Fprintln(stdout, "Restore Dry Run")
	_, _ = fmt.Fprintln(stdout, "")

	if len(readyItems) > 0 {
		_, _ = fmt.Fprintln(stdout, "Would Restore:")
		for _, item := range readyItems {
			writef(stdout, "- %s\n", item.WindowKey)
			writef(stdout, "  command: %s\n", item.Command)
		}
		_, _ = fmt.Fprintln(stdout, "")
	}

	if len(degradedItems) > 0 {
		_, _ = fmt.Fprintln(stdout, "Degraded:")
		for _, item := range degradedItems {
			writef(stdout, "- %s\n", item.WindowKey)
			writef(stdout, "  reason: %s\n", item.Reason)
		}
		_, _ = fmt.Fprintln(stdout, "")
	}

	if len(skippedItems) > 0 {
		_, _ = fmt.Fprintln(stdout, "Skipped:")
		for _, item := range skippedItems {
			writef(stdout, "- %s\n", item.WindowKey)
			writef(stdout, "  reason: %s\n", item.Reason)
		}
		_, _ = fmt.Fprintln(stdout, "")
	}

	writef(stdout, "Summary: would_restore=%d skipped=%d degraded=%d\n", len(readyItems), len(skippedItems), len(degradedItems))
	_, _ = fmt.Fprintln(stdout, "Run with --yes to execute.")
}

func runRestoreTUI(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	atRaw := fs.String("at", "", "timestamp (RFC3339, optional)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	eventsList, err := replay.ListEvents(*stateDir, nil, nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "restore tui failed to list history: %v\n", err)
		return 1
	}
	timestamps := uniqueEventTimestamps(eventsList)

	at := time.Now().UTC()
	if len(timestamps) > 0 {
		at = timestamps[len(timestamps)-1]
	}
	if strings.TrimSpace(*atRaw) != "" {
		parsed, err := parseAtSpec(*atRaw, time.Now().UTC())
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "invalid --at: %v\n", err)
			return 2
		}
		at = parsed
	}
	timestamps = ensureTimestampOption(timestamps, at)

	engine, err := replay.NewEngine(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "restore tui init failed: %v\n", err)
		return 1
	}
	planner := restore.NewPlanner(restore.PlannerConfig{Terminal: restore.TerminalConfig{Command: resolvedConfig.Restore.Terminal.Command, ZellijAttachOrCreate: resolvedConfig.Restore.Terminal.ZellijAttachOrCreate}, AppAllowlist: resolvedConfig.Restore.AppAllowlist})
	planAt := func(ts time.Time) (restore.Plan, error) {
		state, err := engine.At(ts)
		if err != nil {
			return restore.Plan{}, err
		}
		return planner.Build(state), nil
	}

	initialPlan, err := planAt(at)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "restore tui replay failed: %v\n", err)
		return 1
	}

	filteredPlan, confirmed, err := tui.RunWithPlanLoader(initialPlan, timestamps, at, planAt)
	if err != nil {
		writef(stderr, "restore tui failed: %v\n", err)
		return 1
	}
	if !confirmed {
		_, _ = fmt.Fprintln(stdout, "restore cancelled")
		return 0
	}

	executor := restore.NewExecutor(restore.ShellRunner{})
	result := executor.Execute(context.Background(), filteredPlan)
	printRestoreExecution(stdout, result)
	return 0
}

func printRestoreExecution(stdout io.Writer, result restore.Result) {
	for _, item := range result.Items {
		switch item.Status {
		case restore.StatusFailed:
			writef(stdout, "restore_item window_key=%s status=%s error=%q\n", item.WindowKey, item.Status, item.Error)
		case restore.StatusDegraded, restore.StatusSkipped:
			writef(stdout, "restore_item window_key=%s status=%s reason=%q\n", item.WindowKey, item.Status, item.Reason)
		}
	}
	writef(stdout, "restore_summary restored=%d skipped=%d failed=%d\n", result.Summary.Restored, result.Summary.Skipped, result.Summary.Failed)
}

func uniqueEventTimestamps(eventsList []events.Event) []time.Time {
	if len(eventsList) == 0 {
		return nil
	}
	seen := make(map[int64]struct{})
	out := make([]time.Time, 0, len(eventsList))
	for _, event := range eventsList {
		k := event.TS.UnixNano()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, event.TS)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func ensureTimestampOption(timestamps []time.Time, ts time.Time) []time.Time {
	if ts.IsZero() {
		return timestamps
	}
	for _, existing := range timestamps {
		if existing.Equal(ts) {
			return timestamps
		}
	}
	out := append(append([]time.Time(nil), timestamps...), ts)
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func runPrune(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 && isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem prune run [--state-dir <path>] [--days <n>]")
		return 0
	}
	if len(args) == 0 || args[0] != "run" {
		_, _ = fmt.Fprintln(stderr, "usage: redeem prune run [--state-dir <path>] [--days <n>]")
		return 2
	}
	fs := flag.NewFlagSet("prune run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	days := fs.Int("days", resolvedConfig.Retention.Days, "retention days")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	runner := prune.NewRunner(*stateDir, *days, time.Now)
	summary, err := runner.Run()
	if err != nil {
		writef(stderr, "prune run failed: %v\n", err)
		return 1
	}
	writef(stdout, "prune_summary events_pruned=%d snapshots_pruned=%d\n", summary.EventsPruned, summary.SnapshotsPruned)
	return 0
}

type planSummary struct {
	ready    int
	skipped  int
	degraded int
}

func summarizePlan(plan restore.Plan) planSummary {
	s := planSummary{}
	for _, item := range plan.Items {
		switch item.Status {
		case restore.StatusReady:
			s.ready++
		case restore.StatusDegraded:
			s.degraded++
		default:
			s.skipped++
		}
	}
	return s
}

func runHistory(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: redeem history <list|inspect> [flags]")
		return 2
	}
	if isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem history <list|inspect> [flags]")
		return 0
	}

	switch args[0] {
	case "list":
		return runHistoryList(args[1:], resolvedConfig, stdout, stderr)
	case "inspect":
		return runHistoryInspect(args[1:], resolvedConfig, stdout, stderr)
	default:
		writef(stderr, "unknown history subcommand: %s\n", args[0])
		return 2
	}
}

func runHistoryList(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("history list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	fromRaw := fs.String("from", "", "start timestamp (RFC3339)")
	toRaw := fs.String("to", "", "end timestamp (RFC3339)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	from, err := parseOptionalTimestamp(*fromRaw)
	if err != nil {
		writef(stderr, "invalid --from: %v\n", err)
		return 2
	}
	to, err := parseOptionalTimestamp(*toRaw)
	if err != nil {
		writef(stderr, "invalid --to: %v\n", err)
		return 2
	}

	eventsList, err := replay.ListEvents(*stateDir, from, to)
	if err != nil {
		writef(stderr, "history list failed: %v\n", err)
		return 1
	}

	for _, event := range eventsList {
		writef(stdout, "%s %s %s\n", event.TS.Format(time.RFC3339Nano), event.EventType, event.WindowKey)
	}
	return 0
}

func runHistoryInspect(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("history inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	atRaw := fs.String("at", "", "timestamp (RFC3339)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	var at time.Time
	if strings.TrimSpace(*atRaw) == "" {
		eventsList, err := replay.ListEvents(*stateDir, nil, nil)
		if err != nil {
			writef(stderr, "history inspect failed: %v\n", err)
			return 1
		}
		if len(eventsList) == 0 {
			_, _ = fmt.Fprintln(stderr, "history inspect found no events")
			return 1
		}
		at = eventsList[len(eventsList)-1].TS
	} else {
		var err error
		at, err = parseAtSpec(*atRaw, time.Now().UTC())
		if err != nil {
			writef(stderr, "invalid --at: %v\n", err)
			return 2
		}
	}

	engine, err := replay.NewEngine(*stateDir)
	if err != nil {
		writef(stderr, "history init failed: %v\n", err)
		return 1
	}
	state, err := engine.At(at)
	if err != nil {
		writef(stderr, "history inspect failed: %v\n", err)
		return 1
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		writef(stderr, "history encode failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, string(payload))
	return 0
}

func parseOptionalTimestamp(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

func parseAtSpec(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("timestamp is empty")
	}

	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts, nil
	}

	age, err := parseRelativeAge(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected RFC3339 timestamp or relative age like 1m/2d")
	}

	return now.Add(-age), nil
}

func parseRelativeAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, fmt.Errorf("empty relative age")
	}

	total := time.Duration(0)
	i := 0
	for i < len(raw) {
		start := i
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		if start == i || i >= len(raw) {
			return 0, fmt.Errorf("invalid relative age")
		}

		value, err := strconv.Atoi(raw[start:i])
		if err != nil {
			return 0, fmt.Errorf("invalid relative age")
		}
		if value < 0 {
			return 0, fmt.Errorf("invalid relative age")
		}

		unit := raw[i]
		i++

		mult, ok := relativeUnitMultiplier(unit)
		if !ok {
			return 0, fmt.Errorf("invalid relative age")
		}

		segment := time.Duration(value) * mult
		if segment < 0 || segment > (time.Duration(math.MaxInt64)-total) {
			return 0, fmt.Errorf("relative age overflow")
		}
		total += segment
	}

	if total <= 0 {
		return 0, fmt.Errorf("relative age must be > 0")
	}
	return total, nil
}

func relativeUnitMultiplier(unit byte) (time.Duration, bool) {
	switch unit {
	case 's':
		return time.Second, true
	case 'm':
		return time.Minute, true
	case 'h':
		return time.Hour, true
	case 'd':
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

func runCapture(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: redeem capture <once|run> [flags]")
		return 2
	}
	if isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem capture <once|run> [flags]")
		return 0
	}

	switch args[0] {
	case "once":
		return runCaptureOnce(args[1:], resolvedConfig, stdout, stderr)
	case "run":
		return runCaptureRun(args[1:], resolvedConfig, stdout, stderr)
	default:
		writef(stderr, "unknown capture subcommand: %s\n", args[0])
		return 2
	}
}

func runCaptureOnce(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	host := fs.String("host", resolvedConfig.Host, "host identifier")
	profile := fs.String("profile", resolvedConfig.Profile, "profile name")
	snapshotEvery := fs.Int("snapshot-every", resolvedConfig.Capture.SnapshotEvery, "snapshot cadence")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", captureNiriCommandDefault(resolvedConfig), "niri snapshot command")
	processWhitelist := fs.String("process-whitelist", strings.Join(resolvedConfig.ProcessMetadata.Whitelist, ","), "comma-separated process tags")
	processWhitelistExtra := fs.String("process-whitelist-extra", strings.Join(resolvedConfig.ProcessMetadata.WhitelistExtra, ","), "comma-separated extra process tags")
	includeSessionTag := fs.Bool("include-session-tag", resolvedConfig.ProcessMetadata.IncludeSessionTag, "capture terminal session tags")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if strings.TrimSpace(*fixture) == "" && strings.TrimSpace(*niriCmd) == "" {
		_, _ = fmt.Fprintln(stderr, "capture once requires --fixture or --niri-cmd")
		return 2
	}

	runner, err := buildCaptureRunner(captureBuildConfig{
		stateDir:              *stateDir,
		host:                  *host,
		profile:               *profile,
		snapshotEvery:         *snapshotEvery,
		fixture:               *fixture,
		niriCmd:               *niriCmd,
		processWhitelist:      splitCSV(*processWhitelist),
		processWhitelistExtra: splitCSV(*processWhitelistExtra),
		includeSessionTag:     *includeSessionTag,
		stderr:                stderr,
	})
	if err != nil {
		writef(stderr, "capture init failed: %v\n", err)
		return 1
	}

	result, err := runner.CaptureOnce(context.Background())
	if err != nil {
		writef(stderr, "capture once failed: %v\n", err)
		return 1
	}

	writef(stdout, "events_written=%d state_hash=%s\n", result.EventsWritten, result.StateHash)
	if result.SnapshotPath != "" {
		writef(stdout, "snapshot=%s\n", result.SnapshotPath)
	}

	return 0
}

func runCaptureRun(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	host := fs.String("host", resolvedConfig.Host, "host identifier")
	profile := fs.String("profile", resolvedConfig.Profile, "profile name")
	snapshotEvery := fs.Int("snapshot-every", resolvedConfig.Capture.SnapshotEvery, "snapshot cadence")
	interval := fs.Duration("interval", resolvedConfig.Capture.Interval, "capture interval")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", captureNiriCommandDefault(resolvedConfig), "niri snapshot command")
	processWhitelist := fs.String("process-whitelist", strings.Join(resolvedConfig.ProcessMetadata.Whitelist, ","), "comma-separated process tags")
	processWhitelistExtra := fs.String("process-whitelist-extra", strings.Join(resolvedConfig.ProcessMetadata.WhitelistExtra, ","), "comma-separated extra process tags")
	includeSessionTag := fs.Bool("include-session-tag", resolvedConfig.ProcessMetadata.IncludeSessionTag, "capture terminal session tags")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if strings.TrimSpace(*fixture) == "" && strings.TrimSpace(*niriCmd) == "" {
		_, _ = fmt.Fprintln(stderr, "capture run requires --fixture or --niri-cmd")
		return 2
	}

	runner, err := buildCaptureRunner(captureBuildConfig{
		stateDir:              *stateDir,
		host:                  *host,
		profile:               *profile,
		snapshotEvery:         *snapshotEvery,
		fixture:               *fixture,
		niriCmd:               *niriCmd,
		processWhitelist:      splitCSV(*processWhitelist),
		processWhitelistExtra: splitCSV(*processWhitelistExtra),
		includeSessionTag:     *includeSessionTag,
		stderr:                stderr,
	})
	if err != nil {
		writef(stderr, "capture init failed: %v\n", err)
		return 1
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	writef(stdout, "capture_run_started interval=%s\n", interval.String())
	if err := runner.CaptureRun(ctx, ticker.C); err != nil {
		writef(stderr, "capture run failed: %v\n", err)
		return 1
	}
	return 0
}

type captureBuildConfig struct {
	stateDir              string
	host                  string
	profile               string
	snapshotEvery         int
	fixture               string
	niriCmd               string
	processWhitelist      []string
	processWhitelistExtra []string
	includeSessionTag     bool
	stderr                io.Writer
}

func buildCaptureRunner(cfg captureBuildConfig) (*capture.Runner, error) {
	eventStore, err := events.NewStore(cfg.stateDir)
	if err != nil {
		return nil, err
	}
	snapshotStore, err := snapshots.NewStore(cfg.stateDir)
	if err != nil {
		return nil, err
	}

	var snapshotter collector.Snapshotter
	if strings.TrimSpace(cfg.fixture) != "" {
		snapshotter = niri.FileSnapshotter{Path: cfg.fixture}
	} else {
		snapshotter = niri.CommandSnapshotter{Command: cfg.niriCmd}
	}

	enricher := procmeta.NewEnricher(procmeta.ProcReader{}, procmeta.Config{
		Whitelist:         cfg.processWhitelist,
		WhitelistExtra:    cfg.processWhitelistExtra,
		IncludeSessionTag: cfg.includeSessionTag,
	})
	stateCollector := collector.New(snapshotter, enricher)

	return capture.NewRunner(capture.Config{
		Collector:     stateCollector,
		DiffEngine:    diff.NewEngine(),
		EventStore:    eventStore,
		SnapshotStore: snapshotStore,
		SnapshotEvery: cfg.snapshotEvery,
		Host:          cfg.host,
		Profile:       cfg.profile,
		Source:        "capture.cli",
		Logger:        cfg.stderr,
	}), nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envOrDefault(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type globalFlags struct {
	configPath     string
	explicitConfig bool
}

func parseGlobalFlags(args []string) (globalFlags, []string, error) {
	flags := globalFlags{}
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		if arg == "--config" {
			if i+1 >= len(args) {
				return globalFlags{}, nil, fmt.Errorf("--config requires a file path")
			}
			flags.configPath = args[i+1]
			if strings.TrimSpace(flags.configPath) == "" {
				return globalFlags{}, nil, fmt.Errorf("--config requires a file path")
			}
			flags.explicitConfig = true
			i += 2
			continue
		}
		if strings.HasPrefix(arg, "--config=") {
			flags.configPath = strings.TrimPrefix(arg, "--config=")
			if strings.TrimSpace(flags.configPath) == "" {
				return globalFlags{}, nil, fmt.Errorf("--config requires a file path")
			}
			flags.explicitConfig = true
			i++
			continue
		}
		break
	}

	return flags, args[i:], nil
}

func captureNiriCommandDefault(resolvedConfig config.Config) string {
	configured := strings.TrimSpace(resolvedConfig.Capture.NiriCommand)
	defaults := strings.TrimSpace(config.Defaults().Capture.NiriCommand)
	if configured == "" {
		configured = defaults
	}
	if configured != defaults {
		return configured
	}
	return envOrDefault("REDEEM_NIRI_CMD", configured)
}

func isHelpToken(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func printHelp(w io.Writer) {
	writeln(w, "redeem - terminal session history and restore")
	writeln(w)
	writeln(w, "Usage:")
	writeln(w, "  redeem [command]")
	writeln(w)
	writeln(w, "Commands:")
	writeln(w, "  capture   Capture window/session state")
	writeln(w, "  restore   Restore from history")
	writeln(w, "  history   Inspect timeline")
	writeln(w, "  prune     Prune old events/snapshots")
	writeln(w, "  bottle    Bottle workflows (V2)")
	writeln(w, "  doctor    Basic environment checks")
	writeln(w)
	writeln(w, "Flags:")
	writeln(w, "  --config <path>  Path to YAML config file")
	writeln(w, "  -h, --help  Show help")
}

func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func writeln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
