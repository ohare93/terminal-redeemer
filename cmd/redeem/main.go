package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	"github.com/jmo/terminal-redeemer/internal/replay"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
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
	case "doctor":
		fmt.Fprintf(stdout, "stateDir=%s\n", config.DefaultStateDir())
		fmt.Fprintln(stdout, "status=ok")
		return 0
	case "restore", "prune", "bottle":
		fmt.Fprintf(stderr, "subcommand '%s' scaffolded but not implemented yet\n", args[0])
		return 2
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return 2
	}
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fixture == "" {
		fmt.Fprintln(stderr, "capture once requires --fixture (or REDEEM_NIRI_FIXTURE) in this build")
		return 2
	}

	runner, err := buildCaptureRunner(*stateDir, *host, *profile, *snapshotEvery, *fixture, stderr)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fixture == "" {
		fmt.Fprintln(stderr, "capture run requires --fixture (or REDEEM_NIRI_FIXTURE) in this build")
		return 2
	}

	runner, err := buildCaptureRunner(*stateDir, *host, *profile, *snapshotEvery, *fixture, stderr)
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

func buildCaptureRunner(stateDir string, host string, profile string, snapshotEvery int, fixture string, stderr io.Writer) (*capture.Runner, error) {
	eventStore, err := events.NewStore(stateDir)
	if err != nil {
		return nil, err
	}
	snapshotStore, err := snapshots.NewStore(stateDir)
	if err != nil {
		return nil, err
	}

	snapshotter := niri.FileSnapshotter{Path: fixture}
	enricher := procmeta.NewEnricher(procmeta.NoopReader{}, procmeta.Config{IncludeSessionTag: true})
	stateCollector := collector.New(snapshotter, enricher)

	return capture.NewRunner(capture.Config{
		Collector:     stateCollector,
		DiffEngine:    diff.NewEngine(),
		EventStore:    eventStore,
		SnapshotStore: snapshotStore,
		SnapshotEvery: snapshotEvery,
		Host:          host,
		Profile:       profile,
		Source:        "capture.cli",
		Logger:        stderr,
	}), nil
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
