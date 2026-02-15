package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/capture"
	"github.com/jmo/terminal-redeemer/internal/collector"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/diff"
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
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}

	switch args[0] {
	case "-h", "--help", "help":
		printHelp(stdout)
		return 0
	case "capture":
		return runCapture(args[1:], stdout, stderr)
	case "history":
		return runHistory(args[1:], stdout, stderr)
	case "restore":
		return runRestore(args[1:], stdout, stderr)
	case "prune":
		return runPrune(args[1:], stdout, stderr)
	case "doctor":
		fmt.Fprintf(stdout, "stateDir=%s\n", config.DefaultStateDir())
		fmt.Fprintln(stdout, "status=ok")
		return 0
	case "bottle":
		fmt.Fprintf(stderr, "subcommand '%s' scaffolded but not implemented yet\n", args[0])
		return 2
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return 2
	}
}

func runRestore(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redeem restore <apply|tui> [flags]")
		return 2
	}
	if args[0] == "tui" {
		return runRestoreTUI(args[1:], stdout, stderr)
	}
	if args[0] != "apply" {
		fmt.Fprintf(stderr, "unknown restore subcommand: %s\n", args[0])
		return 2
	}

	fs := flag.NewFlagSet("restore apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	atRaw := fs.String("at", "", "timestamp (RFC3339)")
	yes := fs.Bool("yes", false, "apply plan without prompt")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if strings.TrimSpace(*atRaw) == "" {
		fmt.Fprintln(stderr, "restore apply requires --at")
		return 2
	}
	at, err := time.Parse(time.RFC3339Nano, *atRaw)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --at: %v\n", err)
		return 2
	}

	engine, err := replay.NewEngine(*stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "restore init failed: %v\n", err)
		return 1
	}
	state, err := engine.At(at)
	if err != nil {
		fmt.Fprintf(stderr, "restore replay failed: %v\n", err)
		return 1
	}

	planner := restore.NewPlanner(restore.PlannerConfig{Terminal: restore.TerminalConfig{Command: "kitty", ZellijAttachOrCreate: true}, AppAllowlist: map[string]string{}})
	plan := planner.Build(state)

	if !*yes {
		summary := summarizePlan(plan)
		fmt.Fprintf(stdout, "restore_plan ready=%d skipped=%d degraded=%d\n", summary.ready, summary.skipped, summary.degraded)
		fmt.Fprintln(stdout, "pass --yes to execute")
		return 0
	}

	executor := restore.NewExecutor(restore.ShellRunner{})
	result := executor.Execute(context.Background(), plan)
	fmt.Fprintf(stdout, "restore_summary restored=%d skipped=%d failed=%d\n", result.Restored, result.Skipped, result.Failed)
	return 0
}

func runRestoreTUI(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	atRaw := fs.String("at", "", "timestamp (RFC3339, optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	eventsList, err := replay.ListEvents(*stateDir, nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "restore tui failed to list history: %v\n", err)
		return 1
	}
	timestamps := uniqueEventTimestamps(eventsList)

	at := time.Now().UTC()
	if len(timestamps) > 0 {
		at = timestamps[len(timestamps)-1]
	}
	if strings.TrimSpace(*atRaw) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, *atRaw)
		if err != nil {
			fmt.Fprintf(stderr, "invalid --at: %v\n", err)
			return 2
		}
		at = parsed
	}

	engine, err := replay.NewEngine(*stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "restore tui init failed: %v\n", err)
		return 1
	}
	state, err := engine.At(at)
	if err != nil {
		fmt.Fprintf(stderr, "restore tui replay failed: %v\n", err)
		return 1
	}

	planner := restore.NewPlanner(restore.PlannerConfig{Terminal: restore.TerminalConfig{Command: "kitty", ZellijAttachOrCreate: true}, AppAllowlist: map[string]string{}})
	plan := planner.Build(state)

	filteredPlan, confirmed, err := tui.Run(plan, timestamps)
	if err != nil {
		fmt.Fprintf(stderr, "restore tui failed: %v\n", err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(stdout, "restore cancelled")
		return 0
	}

	executor := restore.NewExecutor(restore.ShellRunner{})
	result := executor.Execute(context.Background(), filteredPlan)
	fmt.Fprintf(stdout, "restore_summary restored=%d skipped=%d failed=%d\n", result.Restored, result.Skipped, result.Failed)
	return 0
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

func runPrune(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(stderr, "usage: redeem prune run [--state-dir <path>] [--days <n>]")
		return 2
	}
	fs := flag.NewFlagSet("prune run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	days := fs.Int("days", 30, "retention days")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	runner := prune.NewRunner(*stateDir, *days, time.Now)
	summary, err := runner.Run()
	if err != nil {
		fmt.Fprintf(stderr, "prune run failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "prune_summary events_pruned=%d snapshots_pruned=%d\n", summary.EventsPruned, summary.SnapshotsPruned)
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

func runHistory(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redeem history <list|inspect> [flags]")
		return 2
	}

	switch args[0] {
	case "list":
		return runHistoryList(args[1:], stdout, stderr)
	case "inspect":
		return runHistoryInspect(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown history subcommand: %s\n", args[0])
		return 2
	}
}

func runHistoryList(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("history list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	fromRaw := fs.String("from", "", "start timestamp (RFC3339)")
	toRaw := fs.String("to", "", "end timestamp (RFC3339)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	from, err := parseOptionalTimestamp(*fromRaw)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --from: %v\n", err)
		return 2
	}
	to, err := parseOptionalTimestamp(*toRaw)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --to: %v\n", err)
		return 2
	}

	eventsList, err := replay.ListEvents(*stateDir, from, to)
	if err != nil {
		fmt.Fprintf(stderr, "history list failed: %v\n", err)
		return 1
	}

	for _, event := range eventsList {
		fmt.Fprintf(stdout, "%s %s %s\n", event.TS.Format(time.RFC3339Nano), event.EventType, event.WindowKey)
	}
	return 0
}

func runHistoryInspect(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("history inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	atRaw := fs.String("at", "", "timestamp (RFC3339)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*atRaw) == "" {
		fmt.Fprintln(stderr, "history inspect requires --at")
		return 2
	}

	at, err := time.Parse(time.RFC3339Nano, *atRaw)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --at: %v\n", err)
		return 2
	}

	engine, err := replay.NewEngine(*stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "history init failed: %v\n", err)
		return 1
	}
	state, err := engine.At(at)
	if err != nil {
		fmt.Fprintf(stderr, "history inspect failed: %v\n", err)
		return 1
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "history encode failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(payload))
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

func runCapture(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redeem capture <once|run> [flags]")
		return 2
	}

	switch args[0] {
	case "once":
		return runCaptureOnce(args[1:], stdout, stderr)
	case "run":
		return runCaptureRun(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown capture subcommand: %s\n", args[0])
		return 2
	}
}

func runCaptureOnce(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	host := fs.String("host", "local", "host identifier")
	profile := fs.String("profile", "default", "profile name")
	snapshotEvery := fs.Int("snapshot-every", 100, "snapshot cadence")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", envOrDefault("REDEEM_NIRI_CMD", "niri msg -j workspaces windows"), "niri snapshot command")
	processWhitelistExtra := fs.String("process-whitelist-extra", "", "comma-separated extra process tags")
	includeSessionTag := fs.Bool("include-session-tag", true, "capture terminal session tags")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*fixture) == "" && strings.TrimSpace(*niriCmd) == "" {
		fmt.Fprintln(stderr, "capture once requires --fixture or --niri-cmd")
		return 2
	}

	runner, err := buildCaptureRunner(captureBuildConfig{
		stateDir:              *stateDir,
		host:                  *host,
		profile:               *profile,
		snapshotEvery:         *snapshotEvery,
		fixture:               *fixture,
		niriCmd:               *niriCmd,
		processWhitelistExtra: splitCSV(*processWhitelistExtra),
		includeSessionTag:     *includeSessionTag,
		stderr:                stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "capture init failed: %v\n", err)
		return 1
	}

	result, err := runner.CaptureOnce(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "capture once failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "events_written=%d state_hash=%s\n", result.EventsWritten, result.StateHash)
	if result.SnapshotPath != "" {
		fmt.Fprintf(stdout, "snapshot=%s\n", result.SnapshotPath)
	}

	return 0
}

func runCaptureRun(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", config.DefaultStateDir(), "state directory")
	host := fs.String("host", "local", "host identifier")
	profile := fs.String("profile", "default", "profile name")
	snapshotEvery := fs.Int("snapshot-every", 100, "snapshot cadence")
	interval := fs.Duration("interval", 60*time.Second, "capture interval")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", envOrDefault("REDEEM_NIRI_CMD", "niri msg -j workspaces windows"), "niri snapshot command")
	processWhitelistExtra := fs.String("process-whitelist-extra", "", "comma-separated extra process tags")
	includeSessionTag := fs.Bool("include-session-tag", true, "capture terminal session tags")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*fixture) == "" && strings.TrimSpace(*niriCmd) == "" {
		fmt.Fprintln(stderr, "capture run requires --fixture or --niri-cmd")
		return 2
	}

	runner, err := buildCaptureRunner(captureBuildConfig{
		stateDir:              *stateDir,
		host:                  *host,
		profile:               *profile,
		snapshotEvery:         *snapshotEvery,
		fixture:               *fixture,
		niriCmd:               *niriCmd,
		processWhitelistExtra: splitCSV(*processWhitelistExtra),
		includeSessionTag:     *includeSessionTag,
		stderr:                stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "capture init failed: %v\n", err)
		return 1
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "capture_run_started interval=%s\n", interval.String())
	if err := runner.CaptureRun(ctx, ticker.C); err != nil {
		fmt.Fprintf(stderr, "capture run failed: %v\n", err)
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

	enricher := procmeta.NewEnricher(procmeta.NoopReader{}, procmeta.Config{
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

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "redeem - terminal session history and restore")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  redeem [command]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  capture   Capture window/session state")
	fmt.Fprintln(w, "  restore   Restore from history")
	fmt.Fprintln(w, "  history   Inspect timeline")
	fmt.Fprintln(w, "  prune     Prune old events/snapshots")
	fmt.Fprintln(w, "  bottle    Bottle workflows (V2)")
	fmt.Fprintln(w, "  doctor    Basic environment checks")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -h, --help  Show help")
}
