package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/bootid"
	"github.com/jmo/terminal-redeemer/internal/capture"
	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/collector"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/doctor"
	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/mirrortui"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/niri"
	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/prune"
	"github.com/jmo/terminal-redeemer/internal/resume"
	"github.com/jmo/terminal-redeemer/internal/storelock"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	warnLocalInstall(stderr)

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

	if isHelpToken(args[0]) {
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
	case "capture":
		return runCapture(args[1:], resolvedConfig, stdout, stderr)
	case "mirror":
		return runMirror(args[1:], resolvedConfig, stdout, stderr)
	case "resume":
		return runResume(args[1:], resolvedConfig, stdout, stderr)
	case "prune":
		return runPrune(args[1:], resolvedConfig, stdout, stderr)
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
		doctor.ConfigLoadCheck{Path: flags.configPath, Explicit: flags.explicitConfig},
		doctor.StatePathsCheck{StateDir: resolvedConfig.StateDir},
		doctor.BootIDCheck{Current: bootid.Current},
		doctor.NiriReadinessCheck{
			FixturePath: strings.TrimSpace(os.Getenv("REDEEM_NIRI_FIXTURE")),
			Command:     captureNiriCommandDefault(resolvedConfig),
			Socket:      os.Getenv("NIRI_SOCKET"),
		},
		doctor.ResumeLauncherCheck{Command: resolvedConfig.Resume.TerminalCommand},
		doctor.ZellijListingCheck{},
		doctor.ResumePolicyCheck{
			MaxCheckpointAge:    resolvedConfig.Resume.MaxCheckpointAge,
			UnresolvedWorkspace: resolvedConfig.Resume.UnresolvedWorkspace,
			OnStartup:           resolvedConfig.Resume.OnStartup,
			CaptureInterval:     resolvedConfig.Capture.Interval,
		},
		doctor.StartupServiceCheck{Enabled: resolvedConfig.Resume.OnStartup},
		doctor.LocalInstallCheck{Path: localInstallPath()},
		doctor.CheckpointsIntegrityCheck{StateDir: resolvedConfig.StateDir},
		doctor.RecoveryInventoryCheck{
			StateDir: resolvedConfig.StateDir, Host: strings.TrimSpace(resolvedConfig.Host), Profile: strings.TrimSpace(resolvedConfig.Profile),
			CurrentBootID: bootid.Current,
		},
	}
	if strings.TrimSpace(resolvedConfig.Mirror.SourceHost) != "" {
		checks = append(checks,
			doctor.CommandAvailableCheck{CheckName: "mirror_ssh_available", Command: resolvedConfig.Mirror.SSHCommand},
			doctor.CommandAvailableCheck{CheckName: "mirror_launcher_available", Command: resolvedConfig.Mirror.LauncherCommand},
			doctor.CommandAvailableCheck{CheckName: "mirror_niri_available", Command: resolvedConfig.Mirror.NiriCommand},
		)
		if resolvedConfig.Mirror.Clipboard.Enabled {
			checks = append(checks,
				doctor.CommandAvailableCheck{CheckName: "mirror_self_available", Command: resolvedConfig.Mirror.SelfCommand},
				doctor.CommandAvailableCheck{CheckName: "mirror_clipboard_available", Command: resolvedConfig.Mirror.Clipboard.Command},
				doctor.CommandAvailableCheck{CheckName: "mirror_scp_available", Command: resolvedConfig.Mirror.Clipboard.SCPCommand},
				doctor.CommandAvailableCheck{CheckName: "mirror_kitty_available", Command: resolvedConfig.Mirror.Clipboard.KittyCommand},
			)
		}
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

func runMirror(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printMirrorHelp(stderr)
		return 2
	}
	if isHelpToken(args[0]) {
		printMirrorHelp(stdout)
		return 0
	}
	switch args[0] {
	case "snapshot":
		return runMirrorSnapshot(args[1:], resolvedConfig, stdout, stderr)
	case "list":
		return runMirrorList(args[1:], resolvedConfig, stdout, stderr)
	case "open":
		return runMirrorOpen(args[1:], resolvedConfig, stdout, stderr)
	case "new":
		return runMirrorNew(args[1:], resolvedConfig, stdout, stderr)
	case "attach-local":
		return runMirrorAttachLocal(args[1:], resolvedConfig, stdout, stderr)
	case "save":
		return runMirrorSave(args[1:], resolvedConfig, stdout, stderr)
	case "apply":
		return runMirrorApply(args[1:], resolvedConfig, stdout, stderr)
	case "follow":
		return runMirrorFollow(args[1:], resolvedConfig, stdout, stderr)
	case "status":
		return runMirrorStatus(args[1:], resolvedConfig, stdout, stderr)
	case "close":
		return runMirrorClose(args[1:], resolvedConfig, stdout, stderr)
	case "paste-image":
		return runMirrorPaste(args[1:], resolvedConfig, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown mirror subcommand: %s\n", args[0])
		return 2
	}
}

func runMirrorSnapshot(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", resolvedConfig.Host, "host identifier")
	profile := fs.String("profile", resolvedConfig.Profile, "profile name")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", captureNiriCommandDefault(resolvedConfig), "niri snapshot command")
	processWhitelist := fs.String("process-whitelist", strings.Join(resolvedConfig.ProcessMetadata.Whitelist, ","), "comma-separated process tags")
	processWhitelistExtra := fs.String("process-whitelist-extra", strings.Join(resolvedConfig.ProcessMetadata.WhitelistExtra, ","), "comma-separated extra process tags")
	includeSessionTag := fs.Bool("include-session-tag", resolvedConfig.ProcessMetadata.IncludeSessionTag, "include terminal session tags")
	outputPath := fs.String("output", "", "optional output file path")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if strings.TrimSpace(*fixture) == "" && strings.TrimSpace(*niriCmd) == "" {
		_, _ = fmt.Fprintln(stderr, "mirror snapshot requires --fixture or --niri-cmd")
		return 2
	}
	snapshot, err := mirror.Capture(context.Background(), mirror.Options{
		Host: *host, Profile: *profile, NiriCommand: *niriCmd, FixturePath: *fixture,
		ProcessMetadata: procmeta.Config{Whitelist: splitCSV(*processWhitelist), WhitelistExtra: splitCSV(*processWhitelistExtra), IncludeSessionTag: *includeSessionTag},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror snapshot failed: %v\n", err)
		return 1
	}
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror snapshot encode failed: %v\n", err)
		return 1
	}
	payload = append(payload, '\n')
	if strings.TrimSpace(*outputPath) != "" {
		if err := os.WriteFile(*outputPath, payload, 0o600); err != nil {
			_, _ = fmt.Fprintf(stderr, "mirror snapshot write failed: %v\n", err)
			return 1
		}
		return 0
	}
	_, _ = stdout.Write(payload)
	return 0
}

type repeatFlag struct {
	values []string
	set    bool
}

func (value *repeatFlag) String() string { return strings.Join(value.values, ",") }
func (value *repeatFlag) Set(item string) error {
	if !value.set {
		value.values = nil
		value.set = true
	}
	value.values = append(value.values, item)
	return nil
}

type mirrorSourceFlags struct {
	host            *string
	sshCommand      *string
	sshOptions      repeatFlag
	snapshotCommand repeatFlag
}

func addMirrorSourceFlags(fs *flag.FlagSet, cfg config.MirrorConfig) *mirrorSourceFlags {
	flags := &mirrorSourceFlags{
		host:            fs.String("host", cfg.SourceHost, "SSH source host"),
		sshCommand:      fs.String("ssh-command", cfg.SSHCommand, "SSH executable"),
		sshOptions:      repeatFlag{values: append([]string(nil), cfg.SSHOptions...)},
		snapshotCommand: repeatFlag{values: append([]string(nil), cfg.SnapshotCommand...)},
	}
	fs.Var(&flags.sshOptions, "ssh-option", "SSH option (repeatable; first occurrence replaces config)")
	fs.Var(&flags.snapshotCommand, "snapshot-arg", "remote snapshot argv item (repeatable; first occurrence replaces config)")
	return flags
}

func acquireMirrorSnapshot(ctx context.Context, flags *mirrorSourceFlags, snapshotFile string) (mirror.Snapshot, string, error) {
	host := strings.TrimSpace(*flags.host)
	if strings.TrimSpace(snapshotFile) != "" {
		snapshot, err := mirror.ReadSnapshot(snapshotFile)
		if host == "" {
			host = snapshot.Host
		}
		return snapshot, host, err
	}
	if host == "" {
		return mirror.Snapshot{}, "", fmt.Errorf("source host is required (--host or mirror.sourceHost)")
	}
	snapshot, err := mirror.AcquireRemote(ctx, mirror.ExecRunner{}, mirror.RemoteConfig{
		Host: host, SSHCommand: *flags.sshCommand, SSHOptions: flags.sshOptions.values, SnapshotCommand: flags.snapshotCommand.values,
	})
	return snapshot, host, err
}

func runMirrorList(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	snapshotFile := fs.String("snapshot-file", "", "read snapshot JSON locally instead of SSH")
	asJSON := fs.Bool("json", false, "emit discovered windows as JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	snapshot, host, err := acquireMirrorSnapshot(context.Background(), source, *snapshotFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror list failed: %v\n", err)
		return 1
	}
	windows := mirror.Discover(snapshot)
	if *asJSON {
		payload, _ := json.MarshalIndent(windows, "", "  ")
		_, _ = fmt.Fprintf(stdout, "%s\n", payload)
		return 0
	}
	for _, window := range windows {
		cwd := ""
		if window.Terminal != nil {
			cwd = window.Terminal.CWD
		}
		_, _ = fmt.Fprintf(stdout, "order=%d host=%s session=%q workspace=%q cwd=%q title=%q\n", window.Order, host, mirror.SessionName(window), window.WorkspaceID, cwd, window.Title)
	}
	return 0
}

var chooseMirrorSessions = mirrortui.Run
var newMirrorSessionName = mirror.NewSessionName

func runMirrorNew(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", resolvedConfig.Mirror.SourceHost, "SSH source host")
	sshCommand := fs.String("ssh-command", resolvedConfig.Mirror.SSHCommand, "SSH executable")
	launcher := fs.String("launcher-command", resolvedConfig.Mirror.LauncherCommand, "Kitty-compatible launcher executable")
	appID := fs.String("app-id", resolvedConfig.Mirror.AppID, "owned Kitty app ID/class")
	selfCommand := fs.String("self-command", resolvedConfig.Mirror.SelfCommand, "redeem executable used by Kitty clipboard mapping")
	dryRun := fs.Bool("dry-run", false, "print launch command without executing")
	noClipboard := fs.Bool("no-clipboard", false, "disable image clipboard bridge mapping")
	sourceWorkspace := fs.String("source-workspace", "", "optional Lattice Niri workspace name or number for the source Kitty")
	sshOptions := repeatFlag{values: append([]string(nil), resolvedConfig.Mirror.SSHOptions...)}
	fs.Var(&sshOptions, "ssh-option", "SSH option (repeatable; first occurrence replaces config)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if err := mirror.ValidateWorkspaceReference(*sourceWorkspace); err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror new failed: %v\n", err)
		return 2
	}

	session, err := newMirrorSessionName()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror new failed: %v\n", err)
		return 1
	}
	unique, err := mirror.RandomID()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror new failed: create socket name: %v\n", err)
		return 1
	}
	socket := fmt.Sprintf("unix:/tmp/%s-%s.sock", safeSocketPart(*appID), unique)
	plan, err := mirror.PlanNew(session, mirror.LaunchConfig{
		SourceHost: strings.TrimSpace(*host), SSHCommand: *sshCommand, SSHOptions: sshOptions.values,
		LauncherCommand: *launcher, SelfCommand: *selfCommand, AppID: *appID,
		Socket: socket, Clipboard: resolvedConfig.Mirror.Clipboard.Enabled && !*noClipboard,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror new failed: %v\n", err)
		return 1
	}
	helper, helperPlanErr := mirror.PlanSourceAttach(mirror.SourceAttachConfig{
		SourceHost: strings.TrimSpace(*host), SSHCommand: *sshCommand, SSHOptions: sshOptions.values,
		SnapshotCommand: resolvedConfig.Mirror.SnapshotCommand, Session: session, Workspace: *sourceWorkspace,
	})
	if *dryRun {
		_, _ = fmt.Fprintln(stdout, mirror.RenderCommand(plan.Command))
		if helperPlanErr != nil {
			_, _ = fmt.Fprintf(stdout, "# source Kitty unavailable (best effort): %v\n", helperPlanErr)
		} else {
			_, _ = fmt.Fprintln(stdout, "# source-local helper waits for exact ACTIVE session (best effort)")
			_, _ = fmt.Fprintln(stdout, mirror.RenderCommand(helper))
		}
		return 0
	}
	sourceErr, err := executeMirrorNew(context.Background(), mirror.ExecRunner{}, plan, helper, helperPlanErr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror new failed for %s: %v\n", plan.Session, err)
		return 1
	}
	if sourceErr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: persistent-session creator %s was launched for %s, but source Kitty setup did not complete (the detached view may still be open): %v\n", plan.Session, strings.TrimSpace(*host), sourceErr)
	}
	return 0
}

func executeMirrorNew(ctx context.Context, runner mirror.Runner, creator mirror.LaunchPlan, helper mirror.Command, helperPlanErr error) (error, error) {
	if err := runner.Run(ctx, creator.Command); err != nil {
		return nil, err
	}
	if helperPlanErr != nil {
		return helperPlanErr, nil
	}
	helperCtx, cancel := context.WithTimeout(ctx, mirror.DefaultSourceHelperTimeout)
	defer cancel()
	if err := runner.Run(helperCtx, helper); err != nil {
		return fmt.Errorf("launch source Kitty helper: %w", err), nil
	}
	return nil, nil
}

func runMirrorAttachLocal(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror attach-local", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "exact generated mirror session")
	workspace := fs.String("workspace", "", "optional local Niri workspace name or number")
	launcher := fs.String("launcher-command", resolvedConfig.Mirror.LauncherCommand, "Kitty-compatible launcher executable")
	niriCommand := fs.String("niri-command", resolvedConfig.Mirror.NiriCommand, "Niri executable")
	timeout := fs.Duration("timeout", mirror.DefaultLocalAttachTimeout, "maximum source readiness and launch time")
	dryRun := fs.Bool("dry-run", false, "print the local source launch without executing")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	plan, err := mirror.PlanLocalAttach(*session, *workspace, *launcher)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror attach-local failed: %v\n", err)
		return 2
	}
	if *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "mirror attach-local failed: --timeout must be positive")
		return 2
	}
	if *dryRun {
		_, _ = fmt.Fprintln(stdout, mirror.RenderCommand(plan.Command))
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := mirror.AttachLocal(ctx, mirror.AttachLocalConfig{
		Session: *session, Workspace: *workspace, LauncherCommand: *launcher,
		NiriCommand: *niriCommand, StateDir: resolvedConfig.StateDir, Environment: os.Environ(),
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror attach-local failed: %v\n", err)
		return 1
	}
	if result.PlacementError != nil {
		_, _ = fmt.Fprintf(stderr, "source Kitty %d is attached, but workspace placement failed: %v\n", result.WindowID, result.PlacementError)
		return 1
	}
	return 0
}

func runMirrorOpen(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	snapshotFile := fs.String("snapshot-file", "", "read snapshot JSON locally instead of SSH")
	launcher := fs.String("launcher-command", resolvedConfig.Mirror.LauncherCommand, "Kitty-compatible launcher executable")
	appID := fs.String("app-id", resolvedConfig.Mirror.AppID, "owned Kitty app ID/class")
	selfCommand := fs.String("self-command", resolvedConfig.Mirror.SelfCommand, "redeem executable used by Kitty clipboard mapping")
	openDelay := fs.Duration("open-delay", resolvedConfig.Mirror.OpenDelay, "delay between launches")
	all := fs.Bool("all", false, "open all discovered source windows")
	selectIndex := fs.Int("select", 0, "open one 1-based result index without prompting")
	dryRun := fs.Bool("dry-run", false, "print launch commands without executing")
	noClipboard := fs.Bool("no-clipboard", false, "disable image clipboard bridge mapping")
	sessions := repeatFlag{}
	fs.Var(&sessions, "session", "session name to open (repeatable)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *openDelay < 0 {
		_, _ = fmt.Fprintln(stderr, "--open-delay must not be negative")
		return 2
	}
	if *all && (len(sessions.values) > 0 || *selectIndex > 0) {
		_, _ = fmt.Fprintln(stderr, "--all cannot be combined with --session or --select")
		return 2
	}
	snapshot, host, err := acquireMirrorSnapshot(context.Background(), source, *snapshotFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror open failed: %v\n", err)
		return 1
	}
	windows := mirror.Discover(snapshot)
	if len(windows) == 0 {
		_, _ = fmt.Fprintf(stderr, "no live Zellij sessions found on %s\n", host)
		return 1
	}
	selected := windows
	switch {
	case *all:
	case len(sessions.values) > 0:
		selected, err = mirror.FilterSessions(windows, sessions.values)
	case *selectIndex > 0:
		if *selectIndex > len(windows) {
			err = fmt.Errorf("--select %d exceeds %d results", *selectIndex, len(windows))
		} else {
			selected = windows[*selectIndex-1 : *selectIndex]
		}
	default:
		var cancelled bool
		selected, cancelled, err = chooseMirrorSessions(windows)
		if cancelled {
			return 0
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror open failed: %v\n", err)
		return 1
	}
	for i, window := range selected {
		unique, idErr := mirror.RandomID()
		if idErr != nil {
			_, _ = fmt.Fprintf(stderr, "mirror open failed: create socket name: %v\n", idErr)
			return 1
		}
		socket := fmt.Sprintf("unix:/tmp/%s-%s.sock", safeSocketPart(*appID), unique)
		plan, planErr := mirror.PlanLaunch(window, mirror.LaunchConfig{
			SourceHost: host, SSHCommand: *source.sshCommand, SSHOptions: source.sshOptions.values,
			LauncherCommand: *launcher, SelfCommand: *selfCommand, AppID: *appID,
			Socket: socket, Clipboard: resolvedConfig.Mirror.Clipboard.Enabled && !*noClipboard,
		})
		if planErr != nil {
			_, _ = fmt.Fprintf(stderr, "mirror open failed: %v\n", planErr)
			return 1
		}
		if *dryRun {
			_, _ = fmt.Fprintln(stdout, mirror.RenderCommand(plan.Command))
			continue
		}
		if runErr := (mirror.ExecRunner{}).Run(context.Background(), plan.Command); runErr != nil {
			_, _ = fmt.Fprintf(stderr, "mirror open failed for %s: %v\n", plan.Session, runErr)
			return 1
		}
		if i+1 < len(selected) && *openDelay > 0 {
			time.Sleep(*openDelay)
		}
	}
	return 0
}

func runMirrorSave(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror save", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	appID := fs.String("app-id", resolvedConfig.Mirror.AppID, "owned Kitty app ID/class")
	niriCommand := fs.String("niri-command", resolvedConfig.Mirror.NiriCommand, "Niri executable")
	timeout := fs.Duration("timeout", resolvedConfig.Resume.Timeout, "maximum live inventory time")
	dryRun := fs.Bool("dry-run", false, "inspect and print without replacing the pin")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "mirror save failed: --timeout must be positive")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	snapshot, host, err := acquireMirrorSnapshot(ctx, source, "")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	manager := mirror.WindowManager{Runner: mirror.ExecRunner{}, NiriCommand: *niriCommand}
	windows, err := manager.List(ctx, *appID, "")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	workspaces, err := manager.Workspaces(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	inventory, err := mirror.InspectProjections(ctx, windows, mirror.ProjectionEvidenceConfig{SSHCommand: *source.sshCommand, SSHOptions: source.sshOptions.values})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	result, err := mirror.BuildPin(snapshot, host, windows, workspaces, inventory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	if *dryRun {
		_, _ = fmt.Fprintf(stdout, "would_save=%d host=%s profile=%s untracked=%d ambiguous=%d\n", len(result.Pin.Projections), host, result.Pin.SourceProfile, result.Untracked, result.Ambiguous)
		return 0
	}
	store, err := mirror.OpenPinStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	path, err := store.Write(result.Pin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror save failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "saved=%d pin=%s untracked=%d ambiguous=%d\n", len(result.Pin.Projections), path, result.Untracked, result.Ambiguous)
	return 0
}

func runMirrorApply(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	launcher := fs.String("launcher-command", resolvedConfig.Mirror.LauncherCommand, "Kitty-compatible launcher executable")
	appID := fs.String("app-id", resolvedConfig.Mirror.AppID, "owned Kitty app ID/class")
	niriCommand := fs.String("niri-command", resolvedConfig.Mirror.NiriCommand, "Niri executable")
	timeout := fs.Duration("timeout", resolvedConfig.Resume.Timeout, "per-launch correlation/action timeout")
	pollInterval := fs.Duration("poll-interval", resolvedConfig.Resume.PollInterval, "projection evidence poll interval")
	dryRun := fs.Bool("dry-run", false, "preflight and print without launching or mutating")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *timeout <= 0 || *pollInterval <= 0 || *pollInterval > *timeout {
		_, _ = fmt.Fprintln(stderr, "mirror apply failed: timeout and poll interval must be positive, and poll interval must not exceed timeout")
		return 2
	}
	preflightCtx, cancel := context.WithTimeout(context.Background(), *timeout)
	snapshot, host, err := acquireMirrorSnapshot(preflightCtx, source, "")
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror apply failed: %v\n", err)
		return 1
	}
	overall := 5 * time.Minute
	if *timeout <= overall/258 {
		overall = 258 * *timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), overall)
	defer cancel()
	runner := mirror.ExecRunner{}
	result, err := mirror.ApplyPinned(ctx, mirror.ApplyConfig{
		Snapshot: snapshot, SourceHost: host, SSHCommand: *source.sshCommand,
		SSHOptions: source.sshOptions.values, LauncherCommand: *launcher, AppID: *appID,
		NiriCommand: *niriCommand, StateDir: *stateDir, Timeout: *timeout, PollInterval: *pollInterval, DryRun: *dryRun,
	}, mirror.ApplyDeps{Runner: runner})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror apply failed: %v\n", err)
		return 1
	}
	if writeMirrorApplyResult(stdout, result) {
		return 1
	}
	return 0
}

func runMirrorFollow(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror follow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	launcher := fs.String("launcher-command", resolvedConfig.Mirror.LauncherCommand, "Kitty-compatible launcher executable")
	appID := fs.String("app-id", resolvedConfig.Mirror.AppID, "owned Kitty app ID/class")
	niriCommand := fs.String("niri-command", resolvedConfig.Mirror.NiriCommand, "Niri executable")
	interval := fs.Duration("interval", mirror.DefaultFollowInterval, "healthy source poll interval (minimum 2s)")
	maxPerPoll := fs.Int("max-per-poll", mirror.DefaultFollowMaxPerPoll, "maximum detached launch attempts in one poll")
	maxTotal := fs.Int("max-total", mirror.DefaultFollowMaxTotal, "maximum detached launch attempts in this foreground run")
	timeout := fs.Duration("timeout", resolvedConfig.Resume.Timeout, "per-command and correlation timeout")
	evidenceInterval := fs.Duration("evidence-interval", resolvedConfig.Resume.PollInterval, "new projection evidence interval")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *interval < mirror.MinimumFollowInterval || *maxPerPoll <= 0 || *maxTotal <= 0 || *timeout <= 0 || *evidenceInterval <= 0 || *evidenceInterval > *timeout {
		_, _ = fmt.Fprintln(stderr, "mirror follow failed: --interval must be at least 2s; launch bounds and timeouts must be positive")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	initialCtx, cancel := context.WithTimeout(ctx, *timeout)
	snapshot, host, err := acquireMirrorSnapshot(initialCtx, source, "")
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: %v\n", err)
		return 1
	}
	choices, err := mirror.FollowWorkspaceChoices(snapshot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: %v\n", err)
		return 1
	}
	choice, cancelled, err := mirrortui.RunWorkspaceContext(ctx, choices)
	if cancelled || errors.Is(ctx.Err(), context.Canceled) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: %v\n", err)
		return 1
	}
	manager := mirror.WindowManager{Runner: mirror.ExecRunner{}, NiriCommand: *niriCommand}
	destinationCtx, cancel := context.WithTimeout(ctx, *timeout)
	localWorkspaces, err := manager.Workspaces(destinationCtx)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: resolve local destination: %v\n", err)
		return 1
	}
	destination, err := mirror.ResolveFollowDestination(choice.Workspace, localWorkspaces)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: %v\n", err)
		return 1
	}
	lock, err := mirror.AcquireFollowLock(resolvedConfig.StateDir, host, snapshot.Profile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: another mirror operation is active or the lock is unavailable: %v\n", err)
		return 1
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(stderr, "warning: release mirror follow lock: %v\n", closeErr)
		}
	}()
	selection := mirror.SelectionForWorkspace(choice.Workspace)
	cfg := mirror.FollowConfig{
		SourceHost: host, SSHCommand: *source.sshCommand, SSHOptions: source.sshOptions.values,
		LauncherCommand: *launcher, AppID: *appID, NiriCommand: *niriCommand,
		Timeout: *timeout, EvidenceInterval: *evidenceInterval, MaxPerPoll: *maxPerPoll, MaxTotal: *maxTotal,
	}
	state := &mirror.FollowState{}
	failures := 0
	poll := func(pollParent context.Context) (mirror.FollowPollResult, time.Duration) {
		budget := time.Duration(*maxPerPoll*2+4) * *timeout
		pollCtx, pollCancel := context.WithTimeout(pollParent, budget)
		defer pollCancel()
		freshCtx, freshCancel := context.WithTimeout(pollCtx, *timeout)
		fresh, freshHost, acquireErr := acquireMirrorSnapshot(freshCtx, source, "")
		freshCancel()
		var result mirror.FollowPollResult
		if acquireErr != nil || freshHost != host || fresh.Profile != snapshot.Profile {
			result = mirror.FollowPollResult{
				TotalAttempts: state.TotalAttempts, TotalConfirmed: state.TotalConfirmed, TotalUncertain: state.TotalUncertain,
				Reason: "source disconnected: " + errorText(acquireErr, "SSH destination or profile changed"),
			}
		} else {
			result = mirror.FollowOnce(pollCtx, cfg, fresh, selection, destination, state, mirror.FollowDeps{Runner: mirror.ExecRunner{}})
		}
		if result.Healthy {
			failures = 0
		} else {
			failures++
		}
		return result, mirror.FollowRetryDelay(*interval, failures)
	}
	label := choice.Workspace.Name
	if label == "" {
		label = fmt.Sprintf("workspace %d", choice.Workspace.Index)
	}
	limits := fmt.Sprintf("interval=%s max-per-poll=%d max-total=%d", interval.String(), *maxPerPoll, *maxTotal)
	if err := mirrortui.RunFollowStatus(ctx, label, limits, poll); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "mirror follow failed: %v\n", err)
		return 1
	}
	_ = stdout
	return 0
}

func errorText(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func writeMirrorApplyResult(stdout io.Writer, result mirror.ApplyResult) bool {
	failed := false
	for _, item := range result.Items {
		_, _ = fmt.Fprintf(stdout, "session=%q order=%d status=%s", item.Session, item.Order, item.Status)
		if item.WindowID > 0 {
			_, _ = fmt.Fprintf(stdout, " window_id=%d", item.WindowID)
		}
		if item.LayoutStatus != "" {
			_, _ = fmt.Fprintf(stdout, " layout_status=%s", item.LayoutStatus)
			if item.LayoutReason != "" {
				_, _ = fmt.Fprintf(stdout, " layout_reason=%q", item.LayoutReason)
			}
		}
		if item.Reason != "" {
			_, _ = fmt.Fprintf(stdout, " reason=%q", item.Reason)
		}
		_, _ = fmt.Fprintln(stdout)
		if item.Status == mirror.ApplyFailed || item.Status == mirror.ApplyMissing || item.Status == mirror.ApplyAmbiguous {
			failed = true
		}
	}
	_, _ = fmt.Fprintf(stdout, "untracked=%d ambiguous=%d\n", result.Untracked, result.Ambiguous)
	return failed
}

func safeSocketPart(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			out.WriteRune(r)
		}
	}
	if out.Len() == 0 {
		return "redeem-mirror"
	}
	return out.String()
}

func addOwnedWindowFlags(fs *flag.FlagSet, cfg config.MirrorConfig) (*string, *string, *string, *bool) {
	host := fs.String("host", cfg.SourceHost, "limit to source host")
	appID := fs.String("app-id", cfg.AppID, "owned Kitty app ID/class")
	niriCommand := fs.String("niri-command", cfg.NiriCommand, "Niri executable")
	allHosts := fs.Bool("all-hosts", false, "operate on all owned mirror windows")
	return host, appID, niriCommand, allHosts
}

func listOwnedForCLI(fs *flag.FlagSet, host *string, appID *string, niriCommand *string, allHosts *bool, stderr io.Writer) ([]mirror.OwnedWindow, mirror.WindowManager, int) {
	if *allHosts {
		*host = ""
	}
	if strings.TrimSpace(*host) == "" && !*allHosts {
		_, _ = fmt.Fprintln(stderr, "--host (or configured mirror.sourceHost) or --all-hosts is required")
		return nil, mirror.WindowManager{}, 2
	}
	manager := mirror.WindowManager{Runner: mirror.ExecRunner{}, NiriCommand: *niriCommand}
	windows, err := manager.List(context.Background(), *appID, *host)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s failed: %v\n", fs.Name(), err)
		return nil, manager, 1
	}
	return windows, manager, 0
}

func runMirrorStatus(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host, appID, niriCommand, allHosts := addOwnedWindowFlags(fs, resolvedConfig.Mirror)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	windows, _, code := listOwnedForCLI(fs, host, appID, niriCommand, allHosts, stderr)
	if code != 0 {
		return code
	}
	if *asJSON {
		payload, _ := json.MarshalIndent(windows, "", "  ")
		_, _ = fmt.Fprintf(stdout, "%s\n", payload)
		return 0
	}
	for _, window := range windows {
		_, _ = fmt.Fprintf(stdout, "id=%d workspace=%v title=%q\n", window.ID, window.WorkspaceID, window.Title)
	}
	return 0
}

func runMirrorClose(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror close", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host, appID, niriCommand, allHosts := addOwnedWindowFlags(fs, resolvedConfig.Mirror)
	dryRun := fs.Bool("dry-run", false, "print owned windows without closing them")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	windows, manager, code := listOwnedForCLI(fs, host, appID, niriCommand, allHosts, stderr)
	if code != 0 {
		return code
	}
	if *dryRun {
		for _, window := range windows {
			_, _ = fmt.Fprintf(stdout, "would close id=%d workspace=%v title=%q\n", window.ID, window.WorkspaceID, window.Title)
		}
	}
	if err := manager.Close(context.Background(), windows, *dryRun); err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror close failed: %v\n", err)
		return 1
	}
	if !*dryRun {
		_, _ = fmt.Fprintf(stdout, "closed=%d\n", len(windows))
	}
	return 0
}

func runMirrorPaste(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror paste-image", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", resolvedConfig.Mirror.SourceHost, "SSH source host")
	sshCommand := fs.String("ssh-command", resolvedConfig.Mirror.SSHCommand, "SSH executable")
	scpCommand := fs.String("scp-command", resolvedConfig.Mirror.Clipboard.SCPCommand, "SCP executable")
	clipboardCommand := fs.String("clipboard-command", resolvedConfig.Mirror.Clipboard.Command, "wl-paste compatible executable")
	kittyCommand := fs.String("kitty-command", resolvedConfig.Mirror.Clipboard.KittyCommand, "Kitty executable")
	kittyTo := fs.String("kitty-to", os.Getenv("KITTY_LISTEN_ON"), "Kitty remote-control socket")
	tempDir := fs.String("temp-dir", resolvedConfig.Mirror.Clipboard.TempDir, "shared absolute image temp directory")
	sshOptions := repeatFlag{values: append([]string(nil), resolvedConfig.Mirror.SSHOptions...)}
	scpOptions := repeatFlag{values: append([]string(nil), resolvedConfig.Mirror.Clipboard.SCPOptions...)}
	mimeTypes := repeatFlag{values: append([]string(nil), resolvedConfig.Mirror.Clipboard.MIMETypes...)}
	fs.Var(&sshOptions, "ssh-option", "SSH option (repeatable; first occurrence replaces config)")
	fs.Var(&scpOptions, "scp-option", "SCP option (repeatable; first occurrence replaces config)")
	fs.Var(&mimeTypes, "mime-type", "preferred image MIME type (repeatable; first occurrence replaces config)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	result, err := (mirror.PasteBridge{Runner: mirror.ExecRunner{}}).Paste(context.Background(), mirror.PasteConfig{
		SourceHost: *host, SSHCommand: *sshCommand, SSHOptions: sshOptions.values,
		SCPCommand: *scpCommand, SCPOptions: scpOptions.values,
		ClipboardCommand: *clipboardCommand, KittyCommand: *kittyCommand, KittyTo: *kittyTo,
		TempDir: *tempDir, MIMETypes: mimeTypes.values,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror paste-image failed: %v\n", err)
		return 1
	}
	if result.Image {
		_, _ = fmt.Fprintf(stdout, "pasted_image mime=%s remote_path=%s\n", result.MIMEType, result.RemotePath)
	}
	return 0
}

type rawSnapshotter []byte

func (s rawSnapshotter) Snapshot(context.Context) ([]byte, error) {
	return append([]byte(nil), s...), nil
}

var observeResumeCatalog = func(ctx context.Context, bootID string) (zellijlive.Catalog, error) {
	return (zellijlive.CommandCataloger{BootID: bootID}).Observe(ctx)
}

type resumeCataloger struct{ bootID string }

func (cataloger resumeCataloger) Observe(ctx context.Context) (zellijlive.Catalog, error) {
	return observeResumeCatalog(ctx, cataloger.bootID)
}

func runResume(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	dryRun := fs.Bool("dry-run", false, "plan terminal reconciliation without mutating")
	all := fs.Bool("all", false, "same boot: reconcile all exact ACTIVE sessions; reboot: use only the newest prior-active allow-list")
	maxAge := fs.Duration("max-age", resolvedConfig.Resume.MaxCheckpointAge, "maximum age for prior dead-session resurrection and sticky-placement warning threshold")
	unresolved := fs.String("unresolved-workspace", resolvedConfig.Resume.UnresolvedWorkspace, "unresolved workspace policy: current, skip, or fail")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "current Niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", captureNiriCommandDefault(resolvedConfig), "current Niri snapshot command")
	launcher := fs.String("launcher-command", resolvedConfig.Resume.TerminalCommand, "Kitty executable (not a shell command)")
	timeout := fs.Duration("timeout", resolvedConfig.Resume.Timeout, "per-phase correlation, attachment, and move timeout")
	pollInterval := fs.Duration("poll-interval", resolvedConfig.Resume.PollInterval, "Niri and attachment poll interval")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *maxAge <= 0 {
		_, _ = fmt.Fprintln(stderr, "resume --max-age must be positive")
		return 2
	}
	if *timeout <= 0 || *pollInterval <= 0 || *pollInterval > *timeout {
		_, _ = fmt.Fprintln(stderr, "resume --timeout and --poll-interval must be positive, and poll interval must not exceed timeout")
		return 2
	}
	policy := resume.UnresolvedWorkspacePolicy(strings.ToLower(strings.TrimSpace(*unresolved)))
	if policy != resume.UnresolvedSkip && policy != resume.UnresolvedCurrent && policy != resume.UnresolvedFail {
		_, _ = fmt.Fprintln(stderr, "resume --unresolved-workspace must be current, skip, or fail")
		return 2
	}

	if !*dryRun {
		operationLock, err := storelock.Acquire(*stateDir)
		if err != nil {
			writef(stderr, "resume operation lock failed: %v\n", err)
			return 1
		}
		defer func() { _ = operationLock.Close() }()
	}

	var snapshotter collector.Snapshotter
	if strings.TrimSpace(*fixture) != "" {
		snapshotter = niri.FileSnapshotter{Path: *fixture}
	} else {
		snapshotter = niri.CommandSnapshotter{Command: *niriCmd}
	}
	readySnapshot, err := resume.WaitForNiri(context.Background(), snapshotter, *timeout, *pollInterval)
	if err != nil {
		writef(stderr, "resume Niri readiness failed: %v; verify NIRI_SOCKET, the configured query, and graphical-session readiness\n", err)
		return 1
	}

	resumeCheckpoints, issues, err := checkpoints.List(*stateDir)
	if err != nil {
		writef(stderr, "resume checkpoint scan failed: %v\n", err)
		return 1
	}
	if len(issues) > 0 {
		writef(stderr, "resume checkpoint scan failed: invalid %s: %v\n", filepath.Base(issues[0].Path), issues[0].Err)
		return 1
	}
	currentBootID, err := bootid.Current()
	if err != nil {
		writef(stderr, "resume boot ID failed: %v\n", err)
		return 1
	}
	now := time.Now().UTC()
	selectOptions := resume.SelectOptions{
		CurrentBootID: currentBootID,
		Host:          strings.TrimSpace(resolvedConfig.Host),
		Profile:       strings.TrimSpace(resolvedConfig.Profile),
		Now:           now,
		MaxAge:        *maxAge,
	}
	selection := resume.Select(resumeCheckpoints, selectOptions)
	if *all {
		selection = resume.SelectAll(resumeCheckpoints, selectOptions)
	}

	planner := resume.NewPlanner(resume.PlannerConfig{UnresolvedWorkspace: policy})
	var current model.State
	var available []string
	var catalog zellijlive.Catalog
	needsLiveInventory := selection.Status == resume.CandidateReady || (*all && selection.Checkpoint != nil && selection.Status == resume.CandidateStale)
	if needsLiveInventory {
		enricher := procmeta.NewEnricher(procmeta.ProcReader{}, procmeta.Config{
			Whitelist:         resolvedConfig.ProcessMetadata.Whitelist,
			WhitelistExtra:    resolvedConfig.ProcessMetadata.WhitelistExtra,
			IncludeSessionTag: true,
		})
		current, err = collector.New(rawSnapshotter(readySnapshot), enricher).Collect(context.Background())
		if err != nil {
			writef(stderr, "resume current Niri state failed: %v\n", err)
			return 1
		}
		if *all {
			catalogCtx, cancel := context.WithTimeout(context.Background(), *timeout)
			catalog, err = observeResumeCatalog(catalogCtx, currentBootID)
			cancel()
			if err != nil {
				writef(stderr, "resume exact Zellij catalog failed: %v\n", err)
				return 1
			}
		} else {
			available, err = procmeta.NewZellijSessionVerifier(nil).List()
			if err != nil {
				writef(stderr, "resume Zellij session discovery failed: %v\n", err)
				return 1
			}
		}
	}

	plan := planner.Build(selection, current, available)
	if *all {
		plan = planner.BuildAll(selection, current, catalog, resume.AllOptions{Now: now, MaxAge: *maxAge})
	}
	if !*dryRun && needsLiveInventory {
		actions := resume.NiriActions{Runner: resume.ExecActionRunner{Command: "niri"}}
		executor := resume.Executor{
			Config:   resume.ExecutorConfig{LauncherCommand: *launcher, Timeout: *timeout, PollInterval: *pollInterval, RecoveryMaxAge: *maxAge},
			Launcher: resume.ExecLauncher{},
			Observer: resume.SnapshotObserver{Source: snapshotter},
			Probe:    resume.ProcAttachmentProbe{},
			Mover:    actions,
			Layout:   actions,
			Orderer:  actions,
		}
		if *all {
			executor.Cataloger = resumeCataloger{bootID: currentBootID}
		}
		plan = executor.Apply(context.Background(), plan)
	}
	printResumePlan(stdout, plan)
	if !*dryRun && plan.Summary.Failed > 0 {
		return 1
	}
	return 0
}

func printResumePlan(stdout io.Writer, plan resume.Plan) {
	if plan.CandidateStatus == resume.CandidateNotFound {
		writef(stdout, "resume_candidate status=%s", plan.CandidateStatus)
		if plan.CandidateSource != "" {
			writef(stdout, " source=%s", plan.CandidateSource)
		}
		writef(stdout, " reason=%q\n", plan.Reason)
	} else {
		writef(stdout, "resume_candidate status=%s", plan.CandidateStatus)
		if plan.CandidateSource != "" {
			writef(stdout, " source=%s", plan.CandidateSource)
		}
		writef(stdout, " boot_id=%q captured_at=%s age=%s", plan.BootID, plan.CapturedAt.UTC().Format(time.RFC3339Nano), plan.Age.Round(time.Second))
		if plan.Reason != "" {
			writef(stdout, " reason=%q", plan.Reason)
		}
		writeln(stdout)
	}
	for _, item := range plan.Items {
		writef(stdout, "resume_item window_key=%q session=%q status=%s", item.WindowKey, item.Session, item.Status)
		if item.CandidateSource != "" {
			writef(stdout, " candidate_source=%s", item.CandidateSource)
		}
		if item.ZellijStatus != "" {
			writef(stdout, " zellij_status=%s", item.ZellijStatus)
		}
		if item.PlacementSource != "" {
			writef(stdout, " placement_source=%s", item.PlacementSource)
		}
		if item.PlacementObservedAt != nil {
			writef(stdout, " placement_observed_at=%s placement_age=%s", item.PlacementObservedAt.UTC().Format(time.RFC3339Nano), item.PlacementAge.Round(time.Second))
		}
		if item.CapturedPlacement != nil {
			if item.CapturedPlacement.Column != nil {
				writef(stdout, " placement_column=%d", *item.CapturedPlacement.Column)
			}
			if item.CapturedPlacement.Row != nil {
				writef(stdout, " placement_row=%d", *item.CapturedPlacement.Row)
			}
		}
		if item.CurrentWindowKey != "" {
			writef(stdout, " current_window_key=%q current_window_id=%d", item.CurrentWindowKey, item.CurrentWindowID)
		}
		if item.Workspace != nil {
			writef(stdout, " workspace_method=%s workspace_id=%q workspace_name=%q workspace_output=%q workspace_index=%d", item.Workspace.Method, item.Workspace.ID, item.Workspace.Name, item.Workspace.Output, item.Workspace.Index)
		}
		if item.LayoutStatus != "" {
			writef(stdout, " layout_status=%s", item.LayoutStatus)
			if item.LayoutReason != "" {
				writef(stdout, " layout_reason=%q", item.LayoutReason)
			}
		}
		if item.PlacementWarning != "" {
			writef(stdout, " placement_warning=%q", item.PlacementWarning)
		}
		if item.Reason != "" {
			writef(stdout, " reason=%q", item.Reason)
		}
		writeln(stdout)
	}
	writef(stdout, "resume_summary ready=%d already_open=%d unavailable=%d degraded=%d stale=%d failed=%d skipped=%d restored=%d\n",
		plan.Summary.Ready, plan.Summary.AlreadyOpen, plan.Summary.Unavailable, plan.Summary.Degraded, plan.Summary.Stale, plan.Summary.Failed, plan.Summary.Skipped, plan.Summary.Restored)
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
	writef(stdout, "prune_summary checkpoints_pruned=%d\n", summary.CheckpointsPruned)
	return 0
}

func runCapture(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: redeem capture once [flags]")
		return 2
	}
	if isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem capture once [flags]")
		return 0
	}
	if args[0] != "once" {
		writef(stderr, "unknown capture subcommand: %s\n", args[0])
		return 2
	}
	return runCaptureOnce(args[1:], resolvedConfig, stdout, stderr)
}

func runCaptureOnce(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	host := fs.String("host", resolvedConfig.Host, "host identifier")
	profile := fs.String("profile", resolvedConfig.Profile, "profile name")
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
		fixture:               *fixture,
		niriCmd:               *niriCmd,
		processWhitelist:      splitCSV(*processWhitelist),
		processWhitelistExtra: splitCSV(*processWhitelistExtra),
		includeSessionTag:     *includeSessionTag,
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

	writef(stdout, "state_hash=%s\n", result.StateHash)
	if result.CheckpointPath != "" {
		writef(stdout, "checkpoint=%s\n", result.CheckpointPath)
	}
	return 0
}

type captureBuildConfig struct {
	stateDir              string
	host                  string
	profile               string
	fixture               string
	niriCmd               string
	processWhitelist      []string
	processWhitelistExtra []string
	includeSessionTag     bool
}

func buildCaptureRunner(cfg captureBuildConfig) (*capture.Runner, error) {
	checkpointStore, err := checkpoints.NewStore(cfg.stateDir)
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
		Collector:       stateCollector,
		CheckpointStore: checkpointStore,
		StateDir:        cfg.stateDir,
		Host:            cfg.host,
		Profile:         cfg.profile,
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
	writeln(w, "redeem - terminal placement resume and remote sessions")
	writeln(w)
	writeln(w, "Usage:")
	writeln(w, "  redeem [command]")
	writeln(w)
	writeln(w, "Commands:")
	writeln(w, "  capture   Refresh this boot's rolling terminal checkpoint")
	writeln(w, "  resume    Restore prior-boot placement or reconcile all recovery sessions")
	writeln(w, "  mirror    Create, pick, pin, apply, or temporarily follow remote terminals")
	writeln(w, "  prune     Prune old boot checkpoints")
	writeln(w, "  doctor    Read-only capture/resume/mirror diagnostics")
	writeln(w)
	writeln(w, "Flags:")
	writeln(w, "  --config <path>  Path to YAML config file")
	writeln(w, "  -h, --help  Show help")
}

func printMirrorHelp(w io.Writer) {
	writeln(w, "usage: redeem mirror <command> [flags]")
	writeln(w)
	writeln(w, "Commands:")
	writeln(w, "  new          Create one persistent session and best-effort open its source view")
	writeln(w, "  open         Manually pick and attach visible or headless live sessions")
	writeln(w, "  list         Print the current live source-session inventory")
	writeln(w, "  save         Replace one pinned projection set from fresh exact evidence")
	writeln(w, "  apply        Attach the available sessions from that pin without creating them")
	writeln(w, "  follow       Temporarily follow one selected source workspace in the foreground")
	writeln(w, "  snapshot     Emit a complete source workspace/window/session snapshot")
	writeln(w, "  status       Inspect one exact locally active Zellij session")
	writeln(w, "  close        Close only positively verified owned mirror windows")
	writeln(w, "  paste-image  Copy and display one clipboard image through the mirror bridge")
}

func localInstallPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "bin", "redeem")
}

func warnLocalInstall(stderr io.Writer) {
	p := localInstallPath()
	if p == "" {
		return
	}
	if _, err := os.Stat(p); err == nil {
		_, _ = fmt.Fprintf(stderr, "warning: %s exists and may shadow the Nix-managed version; run `devenv shell uninstall-local` to remove it\n", p)
	}
}

func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func writeln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
