package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/bootid"
	"github.com/jmo/terminal-redeemer/internal/capture"
	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/collector"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/doctor"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/niri"
	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/prune"
	"github.com/jmo/terminal-redeemer/internal/replay"
	"github.com/jmo/terminal-redeemer/internal/restore"
	"github.com/jmo/terminal-redeemer/internal/resume"
	"github.com/jmo/terminal-redeemer/internal/sliceattach"
	"github.com/jmo/terminal-redeemer/internal/slicecontroller"
	"github.com/jmo/terminal-redeemer/internal/sliceenv"
	"github.com/jmo/terminal-redeemer/internal/slicelaunch"
	"github.com/jmo/terminal-redeemer/internal/slicelayout"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/slicetransport"
	"github.com/jmo/terminal-redeemer/internal/slicetui"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
	"github.com/jmo/terminal-redeemer/internal/tui"
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
	case "mirror":
		return runMirror(args[1:], resolvedConfig, stdout, stderr)
	case "restore":
		return runRestore(args[1:], resolvedConfig, stdout, stderr)
	case "resume":
		return runResume(args[1:], resolvedConfig, stdout, stderr)
	case "slice":
		return runSlice(args[1:], resolvedConfig, stdout, stderr)
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
		doctor.ConfigLoadCheck{Path: flags.configPath, Explicit: flags.explicitConfig},
		doctor.StatePathsCheck{StateDir: resolvedConfig.StateDir},
		doctor.BootIDCheck{Current: bootid.Current},
		doctor.NiriReadinessCheck{
			FixturePath: strings.TrimSpace(os.Getenv("REDEEM_NIRI_FIXTURE")),
			Command:     captureNiriCommandDefault(resolvedConfig),
			Socket:      os.Getenv("NIRI_SOCKET"),
		},
		doctor.ResumeLauncherCheck{Command: resolvedConfig.Restore.Terminal.Command},
		doctor.ZellijListingCheck{},
		doctor.ResumePolicyCheck{
			MaxCheckpointAge:    resolvedConfig.Restore.MaxCheckpointAge,
			UnresolvedWorkspace: resolvedConfig.Restore.UnresolvedWorkspace,
			OnStartup:           resolvedConfig.Restore.OnStartup,
			CaptureInterval:     resolvedConfig.Capture.Interval,
		},
		doctor.StartupServiceCheck{Enabled: resolvedConfig.Restore.OnStartup},
		doctor.LocalInstallCheck{Path: localInstallPath()},
		doctor.EventsIntegrityCheck{StateDir: resolvedConfig.StateDir},
		doctor.CheckpointsIntegrityCheck{StateDir: resolvedConfig.StateDir},
		doctor.SnapshotsIntegrityCheck{StateDir: resolvedConfig.StateDir},
	}
	if strings.TrimSpace(resolvedConfig.Mirror.SourceHost) != "" {
		checks = append(checks,
			doctor.CommandAvailableCheck{CheckName: "mirror_ssh_available", Command: resolvedConfig.Mirror.SSHCommand},
			doctor.CommandAvailableCheck{CheckName: "mirror_launcher_available", Command: resolvedConfig.Mirror.LauncherCommand},
			doctor.CommandAvailableCheck{CheckName: "mirror_niri_available", Command: resolvedConfig.Mirror.NiriCommand},
		)
		if resolvedConfig.Mirror.Clipboard.Enabled {
			checks = append(checks,
				doctor.CommandAvailableCheck{CheckName: "mirror_clipboard_available", Command: resolvedConfig.Mirror.Clipboard.Command},
				doctor.CommandAvailableCheck{CheckName: "mirror_scp_available", Command: resolvedConfig.Mirror.Clipboard.SCPCommand},
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

func runSlice(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem slice <inventory|rpc|attach|host-attach|controller|mode|launch|manage|projection-run|close-focused> [flags]")
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "rpc":
		return runSliceRPC(args[1:], resolvedConfig, stdout, stderr)
	case "attach":
		return runSliceAttach(args[1:], resolvedConfig, stdout, stderr)
	case "host-attach":
		return runSliceHostAttach(args[1:], resolvedConfig, stdout, stderr)
	case "controller":
		return runSliceController(args[1:], resolvedConfig, stdout, stderr)
	case "mode":
		return runSliceMode(args[1:], resolvedConfig, stdout, stderr)
	case "launch":
		return runSliceLaunch(args[1:], resolvedConfig, stdout, stderr)
	case "manage":
		return runSliceManage(args[1:], resolvedConfig, stdout, stderr)
	case "projection-run":
		return runSliceProjection(args[1:], resolvedConfig, stdout, stderr)
	case "close-focused":
		return runSliceCloseFocused(args[1:], resolvedConfig, stdout, stderr)
	case "inventory":
		if len(args) < 2 || isHelpToken(args[1]) {
			_, _ = fmt.Fprintln(stdout, "usage: redeem slice inventory <init|snapshot> [flags]")
			if len(args) < 2 {
				return 2
			}
			return 0
		}
		switch args[1] {
		case "init":
			return runSliceInventoryInit(args[2:], resolvedConfig, stdout, stderr)
		case "snapshot":
			return runSliceInventorySnapshot(args[2:], resolvedConfig, stdout, stderr)
		default:
			_, _ = fmt.Fprintf(stderr, "unknown slice inventory subcommand: %s\n", args[1])
			return 2
		}
	default:
		_, _ = fmt.Fprintf(stderr, "unknown slice subcommand: %s\n", args[0])
		return 2
	}
}

var sliceRPCInput io.Reader = os.Stdin

func runSliceRPC(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice rpc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "Terminal Redeemer state directory")
	timeout := fs.Duration("timeout", resolvedConfig.Slice.RequestTimeout, "bounded RPC request timeout")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "slice rpc --timeout must be positive")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	request, err := slicerpc.DecodeRequestContext(ctx, sliceRPCInput)
	if err != nil {
		_ = slicerpc.EncodeResponse(stdout, slicerpc.Response{SchemaVersion: slicerpc.SchemaVersion, Outcome: slicerpc.Outcome{Status: slicerpc.StatusInvalid, Code: "invalid_request"}, SupportedSchemaVersions: []uint32{slicerpc.SchemaVersion}})
		return 2
	}
	resolver := sliceenv.Resolver{Keys: resolvedConfig.Slice.GraphicalContextKeys, Systemctl: resolvedConfig.Slice.SystemctlCommand}
	resolveContext := func(ctx context.Context) (map[string]string, error) { return resolver.Resolve(ctx) }
	store, storeErr := sourceinventory.NewStore(*stateDir)
	sourceHostID, sourceEpoch, sourceFingerprint := "", "", ""
	if storeErr == nil {
		if state, readErr := store.Read(); readErr == nil {
			sourceHostID = state.SourceHostID
			if state.Authority != nil {
				sourceEpoch = state.Authority.SourceEpoch
				sourceFingerprint = state.PrivateFingerprint
			}
		}
	}
	tokens, tokensErr := slicerpc.NewTokenStore(*stateDir)
	if tokensErr != nil {
		tokens = nil
	}
	mutator := &rpcNiriMutator{resolve: resolveContext}
	server := slicerpc.Server{
		SourceHostID: sourceHostID, Tokens: tokens, TokenStateUnavailable: tokensErr != nil, Niri: mutator, PollInterval: 100 * time.Millisecond,
		CheckNiri: func(ctx context.Context) error {
			if _, err := resolveContext(ctx); err != nil {
				return err
			}
			return niriipc.VerifyVersion(ctx, resolvedConfig.Slice.NiriCommand, resolvedConfig.Slice.ExpectedNiriVersion)
		},
		Snapshot: func(ctx context.Context) (sliceprotocol.Envelope, error) {
			env, err := resolveContext(ctx)
			if err != nil {
				return sliceprotocol.Envelope{}, err
			}
			if err := niriipc.VerifyVersion(ctx, resolvedConfig.Slice.NiriCommand, resolvedConfig.Slice.ExpectedNiriVersion); err != nil {
				return sliceprotocol.Envelope{}, err
			}
			return collectSliceInventorySnapshot(ctx, sliceInventorySnapshotOptions{stateDir: *stateDir, niriSocket: env["NIRI_SOCKET"], niriCommand: resolvedConfig.Slice.NiriCommand, zellijCommand: resolvedConfig.Slice.ZellijCommand, zellijSocketDir: os.Getenv("ZELLIJ_SOCKET_DIR"), zellijCacheHome: os.Getenv("XDG_CACHE_HOME")})
		},
		Launcher: slicerpc.LauncherFunc(func(ctx context.Context, id string) slicerpc.LaunchResult {
			env, err := resolveContext(ctx)
			if err != nil {
				return slicerpc.LaunchResult{Err: err}
			}
			return (slicerpc.DirectKittyLauncher{Command: resolvedConfig.Slice.KittyCommand, Environment: env}).Launch(ctx, id)
		}),
	}
	if env, envErr := resolveContext(ctx); envErr == nil && sourceEpoch != "" {
		boot, _ := bootid.Current()
		currentFingerprint, fingerprintErr := sourceinventory.NiriFingerprint(boot, env["NIRI_SOCKET"])
		if fingerprintErr == nil && currentFingerprint == sourceFingerprint {
			server.SourceEpoch = sourceEpoch
			server.SourceFingerprint = sourceFingerprint
			directTransaction := slicerpc.DirectHostTransaction{SelfCommand: resolvedConfig.Slice.SelfCommand, ZellijCommand: resolvedConfig.Slice.ZellijCommand, KittyCommand: resolvedConfig.Slice.KittyCommand, Environment: env, Niri: mutator, SourceEpoch: sourceEpoch, ZellijSocketDir: os.Getenv("ZELLIJ_SOCKET_DIR"), CreationCacheRoot: filepath.Join(*stateDir, "slice", "host-zellij-create"), ShimCache: filepath.Join(*stateDir, "slice", "host-zellij-shim"), PollInterval: server.PollInterval}
			server.HostTransaction = directTransaction
			server.ProveCommit = func(proofCtx context.Context, record slicerpc.TokenRecord) (string, string, error) {
				return slicerpc.ProveRoutedCommit(proofCtx, record, sourceEpoch, sourceFingerprint, resolveContext, server.Snapshot, directTransaction)
			}
		}
	}
	response := server.Handle(ctx, request)
	if err := slicerpc.EncodeResponse(stdout, response); err != nil {
		_, _ = fmt.Fprintf(stderr, "slice rpc encode failed: %v\n", err)
		return 1
	}
	return 0
}

type rpcNiriMutator struct {
	resolve func(context.Context) (map[string]string, error)
}

func (m *rpcNiriMutator) Snapshot(ctx context.Context) (niriipc.State, error) {
	env, err := m.resolve(ctx)
	if err != nil {
		return niriipc.State{}, err
	}
	return (niriipc.Client{SocketPath: env["NIRI_SOCKET"]}).Snapshot(ctx)
}
func (m *rpcNiriMutator) Action(ctx context.Context, action any) error {
	env, err := m.resolve(ctx)
	if err != nil {
		return err
	}
	return (niriipc.Client{SocketPath: env["NIRI_SOCKET"]}).Action(ctx, action)
}

func runSliceAttach(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "exact verified live Zellij session")
	token := fs.String("token", "", "bounded attachment token")
	readyToken := fs.String("ready-token", "", "optional exact attachment readiness nonce")
	realBaseDefault := os.Getenv("ZELLIJ_SOCKET_DIR")
	if realBaseDefault == "" {
		runtime := os.Getenv("XDG_RUNTIME_DIR")
		if runtime != "" {
			realBaseDefault = filepath.Join(runtime, "zellij")
		}
	}
	realBase := fs.String("real-socket-dir", realBaseDefault, "real Zellij socket base")
	privateDefault := resolvedConfig.Slice.AttachPrivateRoot
	if privateDefault == "" && realBaseDefault != "" {
		privateDefault = filepath.Join(realBaseDefault, ".redeem-attach")
	}
	privateRoot := fs.String("private-root", privateDefault, "same-filesystem private attachment root")
	cacheDefault := resolvedConfig.Slice.AttachShimCache
	if cacheDefault == "" && privateDefault != "" {
		cacheDefault = filepath.Join(privateDefault, "shim-cache")
	}
	shimCache := fs.String("shim-cache", cacheDefault, "empty resurrection-isolation cache")
	zellijCommand := fs.String("zellij-command", resolvedConfig.Slice.ZellijCommand, "pinned Zellij executable")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	outcome := (sliceattach.Wrapper{Command: *zellijCommand, Session: *session, Token: *token, RealSocketBase: *realBase, PrivateRoot: *privateRoot, ShimCache: *shimCache, ReadyToken: *readyToken, ReadyWriter: stdout, Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}).Attach(ctx)
	_ = json.NewEncoder(stdout).Encode(outcome)
	return sliceattach.ExitCode(outcome)
}

func runSliceHostAttach(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice host-attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "journaled exact Zellij session")
	preparedPath := fs.String("prepared-path", "", "journaled prepared namespace path")
	markerDevice := fs.Uint64("marker-device", 0, "journaled marker device")
	markerInode := fs.Uint64("marker-inode", 0, "journaled marker inode")
	socketDevice := fs.Uint64("socket-device", 0, "journaled socket device")
	socketInode := fs.Uint64("socket-inode", 0, "journaled socket inode")
	shimCache := fs.String("shim-cache", "", "empty resurrection-isolation cache")
	zellijCommand := fs.String("zellij-command", resolvedConfig.Slice.ZellijCommand, "pinned Zellij executable")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "slice host-attach accepts flags only")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	outcome := (sliceattach.PreparedWrapper{
		Command: *zellijCommand, Session: *session, ShimCache: *shimCache,
		Identity: sliceattach.ExactSocketIdentity{Path: *preparedPath, MarkerDevice: *markerDevice, MarkerInode: *markerInode, SocketDevice: *socketDevice, SocketInode: *socketInode},
		Stdin:    os.Stdin, Stdout: stdout, Stderr: stderr,
	}).Attach(ctx)
	_ = json.NewEncoder(stdout).Encode(outcome)
	return sliceattach.ExitCode(outcome)
}

func runSliceMode(args []string, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem slice mode <enable|disable|status> [--state-dir DIR]")
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	fs := flag.NewFlagSet("slice mode "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	store, err := slicelaunch.NewStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice mode failed: %v\n", err)
		return 1
	}
	var mode slicelaunch.Mode
	switch args[0] {
	case "enable":
		mode, err = store.SetMode(true)
	case "disable":
		mode, err = store.SetMode(false)
	case "status":
		mode, err = store.Mode(cfg.Slice.LeechModeEnabled)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown slice mode subcommand: %s\n", args[0])
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice mode failed: %v\n", err)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(mode)
	return 0
}

type launchWorkspaceObserver struct {
	resolve                      func(context.Context) (map[string]string, error)
	niriCommand, expectedVersion string
}

func (o launchWorkspaceObserver) Current(ctx context.Context) (string, error) {
	env, err := o.resolve(ctx)
	if err != nil {
		return "", err
	}
	if err = niriipc.VerifyVersion(ctx, o.niriCommand, o.expectedVersion); err != nil {
		return "", err
	}
	state, err := (niriipc.Client{SocketPath: env["NIRI_SOCKET"]}).Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return currentStaticWorkspace(state)
}
func currentStaticWorkspace(state niriipc.State) (string, error) {
	names := map[string]string{}
	current := ""
	count := 0
	for _, w := range state.Workspaces {
		if w.Name != nil {
			key, e := sliceprotocol.NormalizeWorkspaceName(*w.Name)
			if e != nil {
				return "", e
			}
			if prior, exists := names[key]; exists {
				if prior == *w.Name {
					return "", errors.New("duplicate static workspace name")
				}
				return "", errors.New("workspace name normalization collision")
			}
			names[key] = *w.Name
		}
		if w.IsFocused {
			count++
			if w.Name == nil {
				return "", errors.New("focused workspace is not static/named")
			}
			current = *w.Name
		}
	}
	if count != 1 {
		return "", errors.New("exactly one focused workspace required")
	}
	return current, nil
}

type launchSelection struct{ stateDir string }

func (s launchSelection) Selected(name string) (bool, error) {
	store, err := slicecontroller.NewStore(s.stateDir)
	if err != nil {
		return false, err
	}
	state, err := store.Read()
	if err != nil {
		return false, err
	}
	key, err := sliceprotocol.NormalizeWorkspaceName(name)
	if err != nil {
		return false, err
	}
	return state.SelectedWorkspaces[key] != "", nil
}

type directLocalKitty struct{ command string }

func (l directLocalKitty) Launch(ctx context.Context) error {
	if strings.TrimSpace(l.command) == "" {
		return errors.New("local Kitty unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(l.command)
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

type launchRemote struct{ client slicetransport.Client }

func (r launchRemote) Call(ctx context.Context, request slicerpc.Request) (slicerpc.Response, error) {
	if strings.TrimSpace(r.client.Host) == "" {
		return slicerpc.Response{}, errors.New("slice source host is unavailable")
	}
	return r.client.Call(ctx, request)
}

type launchHandoff struct {
	stateDir string
	timeout  time.Duration
}

func (h launchHandoff) Send(ctx context.Context, intent slicelaunch.Intent) error {
	store, err := slicecontroller.NewStore(h.stateDir)
	if err != nil {
		return err
	}
	status := "launch_pending"
	if intent.Status == slicelaunch.IntentLaunched {
		status = "launched"
	} else if intent.Status == slicelaunch.IntentFailed {
		if intent.HostTerminalID == "" {
			status = "not_created"
		} else {
			status = "failed"
		}
	}
	payload := slicecontroller.LaunchHandoff{Token: intent.Token, Status: status, HostTerminalID: intent.HostTerminalID, SessionName: intent.SessionName, WorkspaceName: intent.WorkspaceName, SourceID: intent.SourceID, SourceEpoch: intent.SourceEpoch, RuntimeWindowID: intent.RuntimeWindowID}
	response, err := slicecontroller.CallControl(ctx, store.SocketPath(), h.timeout, slicecontroller.NewControlRequest(slicecontroller.VerbLaunchHandoff, payload))
	if err != nil {
		return err
	}
	if response.Outcome.Status != "ok" {
		return errors.New("controller rejected routed launch handoff")
	}
	return nil
}
func runSliceLaunch(args []string, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice launch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
	reconnect := fs.String("reconnect-token", "", "existing routed launch token")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	store, err := slicelaunch.NewStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice launch failed: %v\n", err)
		return 1
	}
	resolver := sliceenv.Resolver{Keys: cfg.Slice.GraphicalContextKeys, Systemctl: cfg.Slice.SystemctlCommand}
	router := slicelaunch.Router{Store: store, DefaultEnabled: cfg.Slice.LeechModeEnabled, Workspace: launchWorkspaceObserver{resolve: resolver.Resolve, niriCommand: cfg.Slice.NiriCommand, expectedVersion: cfg.Slice.ExpectedNiriVersion}, Selection: launchSelection{stateDir: *stateDir}, Local: directLocalKitty{command: cfg.Slice.KittyCommand}, Remote: launchRemote{client: newSliceTransport(cfg)}, Handoff: launchHandoff{stateDir: *stateDir, timeout: cfg.Slice.Controller.ControlTimeout}, RetryWindow: cfg.Slice.Controller.RetryWindow, InitialBackoff: cfg.Slice.RetryInitialBackoff, MaxBackoff: cfg.Slice.RetryMaxBackoff, MaxAttempts: cfg.Slice.RetryMaxAttempts}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result slicelaunch.Result
	if *reconnect != "" {
		result, err = router.Reconnect(ctx, *reconnect)
	} else {
		result, err = router.Route(ctx)
	}
	_ = json.NewEncoder(stdout).Encode(result)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice launch: %v\n", err)
		return 1
	}
	return 0
}

var runSliceManageUI = slicetui.Run

func runSliceManage(args []string, cfg config.Config, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice manage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
	timeout := fs.Duration("timeout", cfg.Slice.Controller.ControlTimeout, "bounded controller request timeout")
	refresh := fs.Duration("refresh-interval", cfg.Slice.Controller.PollInterval, "live status refresh interval")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *timeout <= 0 || *refresh <= 0 {
		_, _ = fmt.Fprintln(stderr, "slice manage accepts flags only and requires positive timeout and refresh interval")
		return 2
	}
	socketPath, err := slicecontroller.ControlSocketPath(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice manage failed: %v\n", err)
		return 1
	}
	client := slicetui.SocketClient{Path: socketPath, Timeout: *timeout}
	if err := runSliceManageUI(client, *refresh, *timeout); err != nil {
		_, _ = fmt.Fprintf(stderr, "slice manage failed: %v\n", err)
		return 1
	}
	return 0
}

func controllerEngineConfig(cfg config.Config) slicecontroller.ControllerConfig {
	return slicecontroller.ControllerConfig{Namespace: slicecontroller.Namespace{Host: cfg.Slice.Controller.HostID, Leech: cfg.Slice.Controller.LeechID}, RetryWindow: cfg.Slice.Controller.RetryWindow, RetryInitialBackoff: cfg.Slice.RetryInitialBackoff, RetryMaxBackoff: cfg.Slice.RetryMaxBackoff, RetryMaxAttempts: cfg.Slice.RetryMaxAttempts, SourceGoneGrace: cfg.Slice.Controller.SourceGoneGrace, SourceGoneConfirmations: cfg.Slice.Controller.SourceGoneConfirmations}
}

func runSliceController(args []string, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem slice controller <init|run|status|workspace-add|workspace-remove|all-enable|all-disable|pickup|pickup-remove|drop|close|reopen|undo|reconnect|launch-handoff> [flags]")
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "init" {
		fs := flag.NewFlagSet("slice controller init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
		hostID := fs.String("host-id", cfg.Slice.Controller.HostID, "durable host namespace")
		leechID := fs.String("leech-id", cfg.Slice.Controller.LeechID, "durable leech namespace")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		store, err := slicecontroller.NewStore(*stateDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "slice controller init failed: %v\n", err)
			return 1
		}
		lock, err := store.Acquire()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "slice controller init failed: %v\n", err)
			return 1
		}
		defer lock.Close()
		state, err := store.Initialize(slicecontroller.Namespace{Host: *hostID, Leech: *leechID})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "slice controller init failed: %v\n", err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(state)
		return 0
	}
	if args[0] == "run" {
		return runSliceControllerForeground(args[1:], cfg, stdout, stderr)
	}
	verbMap := map[string]slicecontroller.ControlVerb{"status": slicecontroller.VerbStatus, "workspace-add": slicecontroller.VerbWorkspaceAdd, "workspace-remove": slicecontroller.VerbWorkspaceRemove, "all-enable": slicecontroller.VerbAllEnable, "all-disable": slicecontroller.VerbAllDisable, "pickup": slicecontroller.VerbPickup, "pickup-remove": slicecontroller.VerbPickupRemove, "drop": slicecontroller.VerbDrop, "close": slicecontroller.VerbClose, "reopen": slicecontroller.VerbReopen, "undo": slicecontroller.VerbUndo, "reconnect": slicecontroller.VerbReconnect, "launch-handoff": slicecontroller.VerbLaunchHandoff}
	verb, ok := verbMap[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "unknown slice controller subcommand: %s\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("slice controller "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
	timeout := fs.Duration("timeout", cfg.Slice.Controller.ControlTimeout, "control timeout")
	sourceID := fs.String("source-id", "", "exact source identity")
	workspace := fs.String("workspace", "", "static workspace name")
	token := fs.String("token", "", "routed launch token")
	status := fs.String("status", "launch_pending", "routed launch status")
	terminalID := fs.String("host-terminal-id", "", "host terminal identity")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	store, err := slicecontroller.NewStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "controller control failed: %v\n", err)
		return 1
	}
	var payload any = struct{}{}
	switch verb {
	case slicecontroller.VerbWorkspaceAdd, slicecontroller.VerbWorkspaceRemove:
		payload = slicecontroller.WorkspacePayload{Name: *workspace}
	case slicecontroller.VerbPickup, slicecontroller.VerbPickupRemove, slicecontroller.VerbDrop, slicecontroller.VerbClose, slicecontroller.VerbReopen, slicecontroller.VerbReconnect:
		payload = slicecontroller.SourcePayload{SourceID: *sourceID}
	case slicecontroller.VerbLaunchHandoff:
		payload = slicecontroller.LaunchHandoff{Token: *token, Status: *status, HostTerminalID: *terminalID}
	}
	response, err := slicecontroller.CallControl(context.Background(), store.SocketPath(), *timeout, slicecontroller.NewControlRequest(verb, payload))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "controller control failed: %v\n", err)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(response)
	if response.Outcome.Status != "ok" {
		return 1
	}
	return 0
}

func newSliceTransport(cfg config.Config) slicetransport.Client {
	return slicetransport.Client{Command: cfg.Slice.TransportCommand, Options: append([]string(nil), cfg.Slice.TransportOptions...), Host: cfg.Slice.SourceHost, RPCCommand: append([]string(nil), cfg.Slice.RPCCommand...), Timeout: cfg.Slice.RequestTimeout, KeepaliveInterval: cfg.Slice.KeepaliveInterval, KeepaliveCount: cfg.Slice.KeepaliveCount, MaxAttempts: cfg.Slice.RetryMaxAttempts, InitialBackoff: cfg.Slice.RetryInitialBackoff, MaxBackoff: cfg.Slice.RetryMaxBackoff}
}

func runSliceControllerForeground(args []string, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice controller run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
	allowDisabled := fs.Bool("allow-disabled", false, "allow a manual foreground development run while service wiring is disabled")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if !cfg.Slice.Controller.Enabled && !*allowDisabled {
		_, _ = fmt.Fprintln(stderr, "slice controller is opt-in; enable slice.controller.enabled or pass --allow-disabled for a foreground development run")
		return 2
	}
	if strings.TrimSpace(cfg.Slice.SourceHost) == "" {
		_, _ = fmt.Fprintln(stderr, "slice controller requires slice.sourceHost")
		return 2
	}
	store, err := slicecontroller.NewStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice controller failed: %v\n", err)
		return 1
	}
	lock, err := store.Acquire()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice controller failed: %v\n", err)
		return 1
	}
	defer lock.Close()
	state, err := store.Read()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice controller failed: %v\n", err)
		return 1
	}
	if state.Namespace != (slicecontroller.Namespace{Host: cfg.Slice.Controller.HostID, Leech: cfg.Slice.Controller.LeechID}) {
		_, _ = fmt.Fprintln(stderr, "slice controller namespace does not match initialized authority")
		return 1
	}
	engine := &slicecontroller.Engine{Store: store, Config: controllerEngineConfig(cfg)}
	if _, err := engine.PrepareStartup(); err != nil {
		_, _ = fmt.Fprintf(stderr, "slice controller startup transition failed: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	resolver := sliceenv.Resolver{Keys: cfg.Slice.GraphicalContextKeys, Systemctl: cfg.Slice.SystemctlCommand}
	operationMu := &sync.Mutex{}
	rawExecute := func(ctx context.Context, effects []slicecontroller.Effect) error {
		effectCtx, cancel := context.WithTimeout(ctx, cfg.Slice.RequestTimeout)
		defer cancel()
		return executeSliceControllerEffects(effectCtx, engine, cfg, resolver, effects)
	}
	execute := func(ctx context.Context, effects []slicecontroller.Effect) error {
		return engine.ExecuteEffects(ctx, effects, rawExecute)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- slicecontroller.ServeControl(ctx, store.SocketPath(), cfg.Slice.Controller.ControlTimeout, slicecontroller.ControlHandler{Engine: engine, Execute: execute, Serialize: operationMu})
	}()
	ticker := time.NewTicker(cfg.Slice.Controller.PollInterval)
	defer ticker.Stop()
	poll := func() {
		operationMu.Lock()
		defer operationMu.Unlock()
		pollSliceController(ctx, engine, cfg, resolver, execute, stderr)
	}
	poll()
	_, _ = fmt.Fprintf(stdout, "slice_controller_ready socket=%s\n", store.SocketPath())
	for {
		select {
		case <-ctx.Done():
			return 0
		case err := <-errCh:
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "slice controller control socket failed: %v\n", err)
				return 1
			}
			return 0
		case <-ticker.C:
			poll()
		}
	}
}

func pollSliceController(ctx context.Context, engine *slicecontroller.Engine, cfg config.Config, resolver sliceenv.Resolver, execute func(context.Context, []slicecontroller.Effect) error, stderr io.Writer) {
	request := slicerpc.Request{SchemaVersion: slicerpc.SchemaVersion, AcceptSchemaVersions: []uint32{slicerpc.SchemaVersion}, RequestID: fmt.Sprintf("snapshot-%d", time.Now().UnixNano()), Verb: slicerpc.VerbSnapshot, Payload: json.RawMessage("{}")}
	response, remoteErr := newSliceTransport(cfg).Call(ctx, request)
	if remoteErr == nil {
		payload, _ := json.Marshal(response.Result)
		envelope, decodeErr := sliceprotocol.Decode(bytes.NewReader(payload))
		if decodeErr != nil {
			remoteErr = decodeErr
		} else if _, effects, _, applyErr := engine.ApplyEnvelope(envelope, time.Now()); applyErr != nil {
			remoteErr = applyErr
		} else if effectErr := execute(ctx, effects); effectErr != nil {
			_, _ = fmt.Fprintf(stderr, "slice controller reconciliation effect failed: %v\n", effectErr)
		}
	}
	if remoteErr != nil {
		_, _ = engine.RecordObservationFailure("transport_disconnected")
		_, _ = fmt.Fprintf(stderr, "slice controller snapshot degraded: %v\n", remoteErr)
	}
	env, envErr := resolver.Resolve(ctx)
	if envErr == nil {
		client := niriipc.Client{SocketPath: env["NIRI_SOCKET"]}
		if local, localErr := client.Snapshot(ctx); localErr == nil {
			state, _ := engine.Status()
			owned, ownershipConflicts := slicecontroller.VerifyOwnedWindowsWithConflicts(state, local, slicecontroller.ProcFS{})
			boot, _ := bootid.Current()
			epoch, _ := sourceinventory.NiriFingerprint(boot, env["NIRI_SOCKET"])
			if _, effects, observeErr := engine.ObserveLocalWithConflicts(epoch, owned, ownershipConflicts); observeErr == nil {
				_ = execute(ctx, effects)
			}
			planControllerSpatial(ctx, engine, cfg, local, epoch, owned, execute)
		}
	}
	if _, effects, tickErr := engine.Tick(); tickErr == nil {
		_ = execute(ctx, effects)
	}
}

func planControllerSpatial(ctx context.Context, engine *slicecontroller.Engine, cfg config.Config, local niriipc.State, leechEpoch string, owned []slicecontroller.OwnedWindow, execute func(context.Context, []slicecontroller.Effect) error) {
	state, err := engine.Status()
	if err != nil || state.Inventory == nil {
		return
	}
	ownedBySource := map[string]slicecontroller.OwnedWindow{}
	for _, window := range owned {
		ownedBySource[window.SourceID] = window
	}
	localWindows := map[uint64]niriipc.Window{}
	for _, window := range local.Windows {
		localWindows[window.ID] = window
	}
	localWorkspacesByID := map[uint64]niriipc.Workspace{}
	var leechCatalog []slicelayout.Workspace
	for _, workspace := range local.Workspaces {
		localWorkspacesByID[workspace.ID] = workspace
		if workspace.Name != nil {
			if key, normalizeErr := sliceprotocol.NormalizeWorkspaceName(*workspace.Name); normalizeErr == nil {
				leechCatalog = append(leechCatalog, slicelayout.Workspace{RuntimeID: workspace.ID, Name: *workspace.Name, Key: key})
			}
		}
	}
	var hostCatalog []slicelayout.Workspace
	seenHostWorkspace := map[uint64]bool{}
	for _, source := range state.Inventory.Sources {
		if source.Workspace.Name != "" && !seenHostWorkspace[source.Workspace.RuntimeID] {
			seenHostWorkspace[source.Workspace.RuntimeID] = true
			hostCatalog = append(hostCatalog, slicelayout.Workspace{RuntimeID: source.Workspace.RuntimeID, Name: source.Workspace.Name, Key: source.Workspace.Key})
		}
	}
	for _, hostSource := range state.Inventory.Sources {
		if hostSource.Workspace.Key == "" {
			continue
		}
		ownedWindow, ok := ownedBySource[hostSource.SourceID]
		if !ok {
			continue
		}
		localWindow, ok := localWindows[ownedWindow.WindowID]
		if !ok || localWindow.WorkspaceID == nil || len(localWindow.Layout.WindowSize) != 2 {
			continue
		}
		localWorkspace, ok := localWorkspacesByID[*localWindow.WorkspaceID]
		if !ok || localWorkspace.Name == nil || localWorkspace.Output == nil {
			continue
		}
		output, ok := local.Outputs[*localWorkspace.Output]
		if !ok {
			continue
		}
		key, normalizeErr := sliceprotocol.NormalizeWorkspaceName(*localWorkspace.Name)
		if normalizeErr != nil {
			continue
		}
		mode := slicelayout.Tiled
		var order *sliceprotocol.Position
		if localWindow.IsFloating {
			mode = slicelayout.Floating
		} else if len(localWindow.Layout.Position) == 2 {
			order = &sliceprotocol.Position{Column: localWindow.Layout.Position[0], Tile: localWindow.Layout.Position[1]}
		} else {
			continue
		}
		hostMode := slicelayout.LayoutMode(hostSource.Layout.Mode)
		input := slicelayout.Input{ControllerID: state.ControllerID, Generation: state.Generation + 1, Host: slicelayout.Observation{Quality: slicelayout.Complete, SourceID: hostSource.SourceID, SourceEpoch: state.Inventory.SourceEpoch, RuntimeWindowID: hostSource.RuntimeWindowID, Output: hostSource.Output, Workspace: slicelayout.Workspace{RuntimeID: hostSource.Workspace.RuntimeID, Name: hostSource.Workspace.Name, Key: hostSource.Workspace.Key}, Mode: hostMode, WindowWidth: hostSource.Layout.WindowWidth, WindowHeight: hostSource.Layout.WindowHeight, Order: hostSource.Layout.Position}, Leech: &slicelayout.Observation{Quality: slicelayout.Complete, SourceID: hostSource.SourceID, SourceEpoch: leechEpoch, RuntimeWindowID: ownedWindow.WindowID, Output: sliceprotocol.Output{Name: output.Name, LogicalX: output.Logical.X, LogicalY: output.Logical.Y, LogicalWidth: output.Logical.Width, LogicalHeight: output.Logical.Height, Scale: output.Logical.Scale, Transform: output.Logical.Transform}, Workspace: slicelayout.Workspace{RuntimeID: localWorkspace.ID, Name: *localWorkspace.Name, Key: key}, Mode: mode, WindowWidth: localWindow.Layout.WindowSize[0], WindowHeight: localWindow.Layout.WindowSize[1], Order: order}, HostWorkspaces: hostCatalog, LeechWorkspaces: leechCatalog, Ownership: slicelayout.Ownership{SourceID: hostSource.SourceID, HostCompositorEpoch: state.Inventory.SourceEpoch, LeechCompositorEpoch: leechEpoch, HostRuntimeWindowID: hostSource.RuntimeWindowID, LeechRuntimeWindowID: ownedWindow.WindowID, ProjectionPositivelyOwned: true}}
		record := state.Spatial[hostSource.SourceID]
		if record.Recovery != nil {
			if record.Recovery.Stable {
				continue
			}
			if time.Now().Before(record.Recovery.NextAttemptAt) {
				continue
			}
		}
		input.LastApplied = record.LastApplied
		result := slicelayout.Plan(input)
		if next, effects, recordErr := engine.RecordSpatial(hostSource.SourceID, result); recordErr == nil {
			state = next
			effectErr := execute(ctx, effects)
			if effectErr != nil {
				state, _ = engine.RecordSpatialFailure(hostSource.SourceID, "spatial_execution_failed")
				continue
			}
			if len(effects) == 0 {
				state, _ = engine.CompleteSpatial(hostSource.SourceID)
			}
		}
	}
}

func executeSliceControllerEffects(ctx context.Context, engine *slicecontroller.Engine, cfg config.Config, resolver sliceenv.Resolver, effects []slicecontroller.Effect) error {
	return executeSliceControllerEffectsWithProcesses(ctx, engine, cfg, resolver, effects, slicecontroller.ProcFS{})
}

// executeSliceControllerEffectsWithProcesses is the hermetic process-evidence
// seam for the destructive local-effect boundary. Production always supplies
// ProcFS through executeSliceControllerEffects.
func executeSliceControllerEffectsWithProcesses(ctx context.Context, engine *slicecontroller.Engine, cfg config.Config, resolver sliceenv.Resolver, effects []slicecontroller.Effect, processes slicecontroller.ProcessReader) error {
	if processes == nil {
		processes = slicecontroller.ProcFS{}
	}
	for _, effect := range effects {
		state, err := engine.Status()
		if err != nil {
			return err
		}
		switch effect.Kind {
		case slicecontroller.EffectLaunchProjection:
			source, ok := state.Sources[effect.SourceID]
			current, mapped := state.Projections[effect.SourceID]
			if !ok || !mapped || current.AppID != effect.Projection.AppID || current.Status != slicecontroller.ProjectionLaunching || !state.Wanted(effect.SourceID) || len(state.PendingCleanups) > 0 {
				continue
			}
			graphical, err := resolver.Resolve(ctx)
			if err != nil {
				return err
			}
			remoteSelf := cfg.Slice.RPCCommand[0]
			plan, planErr := slicecontroller.BuildProjectionCommand(slicecontroller.ProjectionCommandConfig{KittyCommand: cfg.Slice.KittyCommand, SelfCommand: cfg.Slice.SelfCommand, TransportCommand: cfg.Slice.TransportCommand, SourceHost: cfg.Slice.SourceHost, ControlSocket: engine.Store.SocketPath(), RemoteSelfCommand: remoteSelf, TransportOptions: cfg.Slice.TransportOptions, GraphicalContext: graphical}, source, current)
			if planErr != nil {
				_, _ = engine.RecordLaunch(effect.SourceID, 0, planErr)
				return planErr
			}
			executable, argv, identityErr := slicecontroller.ResolveProjectionCommand(plan)
			if identityErr != nil {
				_, _ = engine.RecordLaunch(effect.SourceID, 0, identityErr)
				return identityErr
			}
			if _, err := engine.PrepareProjection(effect.SourceID, executable, argv); err != nil {
				return err
			}
			pid, launchErr := slicecontroller.StartProjectionCommand(ctx, plan)
			if _, err := engine.RecordLaunch(effect.SourceID, pid, launchErr); err != nil {
				return err
			}
		case slicecontroller.EffectCloseProjection:
			_, cleanupGated := state.PendingCleanups[effect.SourceID]
			recordCleanupFailure := func(code string) {
				if cleanupGated {
					if code == "" {
						code = "cleanup_ownership_unproven"
					}
					_, _ = engine.RecordCleanupFailure(effect.SourceID, code)
				}
			}
			current, mapped := state.Projections[effect.SourceID]
			if !mapped || current.AppID != effect.Projection.AppID || current.Status != slicecontroller.ProjectionClosing {
				continue
			}
			graphical, err := resolver.Resolve(ctx)
			if err != nil {
				recordCleanupFailure("cleanup_context_unavailable")
				return err
			}
			client := niriipc.Client{SocketPath: graphical["NIRI_SOCKET"]}
			local, err := client.Snapshot(ctx)
			if err != nil {
				recordCleanupFailure("cleanup_observation_unavailable")
				return err
			}
			owned, ownershipConflicts := slicecontroller.VerifyOwnedWindowsWithConflicts(state, local, processes)
			positive := false
			appPresent := false
			for _, window := range local.Windows {
				if window.AppID == current.AppID {
					appPresent = true
				}
			}
			for _, window := range owned {
				if window.SourceID == effect.SourceID && window.WindowID == effect.WindowID {
					if effect.FocusRequired && !window.Focused {
						return errors.New("focused projection changed before close mutation")
					}
					positive = true
					break
				}
			}
			if positive {
				if err := slicecontroller.CloseOwnedWindowVerified(ctx, client, effect.WindowID, 100*time.Millisecond); err != nil {
					recordCleanupFailure("cleanup_verification_failed")
					return err
				}
				if cleanupGated {
					if _, err := engine.CompleteCleanup(effect.SourceID); err != nil {
						return err
					}
				}
			} else if !appPresent {
				if cleanupGated {
					if _, err := engine.CompleteCleanup(effect.SourceID); err != nil {
						return err
					}
				}
			} else {
				recordCleanupFailure(ownershipConflicts[effect.SourceID])
				return errors.New("owned projection cleanup could not be proven")
			}
		case slicecontroller.EffectApplySpatial:
			if effect.Proposal == nil {
				return errors.New("missing spatial proposal")
			}
			proposal := *effect.Proposal
			record, ok := state.Spatial[effect.SourceID]
			if !ok || record.LastApplied == nil || record.LastApplied.Target != proposal.Target || record.LastApplied.Origin.ControllerID != proposal.Origin.ControllerID || record.LastApplied.Origin.Generation != proposal.Origin.Generation {
				continue
			}
			if proposal.Target == slicelayout.Host {
				return errors.New("v1 rejects host-target spatial effects")
			} else {
				graphical, err := resolver.Resolve(ctx)
				if err != nil {
					return err
				}
				boot, _ := bootid.Current()
				currentEpoch, epochErr := sourceinventory.NiriFingerprint(boot, graphical["NIRI_SOCKET"])
				if epochErr != nil || currentEpoch != proposal.TargetCompositorEpoch {
					return errors.New("leech compositor epoch changed before spatial mutation")
				}
				client := niriipc.Client{SocketPath: graphical["NIRI_SOCKET"]}
				if err := slicecontroller.ReproveLeechSpatial(ctx, engine.Store, client, slicecontroller.ProcFS{}, proposal, currentEpoch); err != nil {
					return err
				}
				ensureOnly := false
				for _, change := range proposal.Changes {
					if change.Kind == slicelayout.ChangeEnsureWorkspace {
						ensureOnly = true
						if _, err := (slicerpc.Server{Niri: &rpcNiriMutator{resolve: func(context.Context) (map[string]string, error) { return graphical, nil }}, PollInterval: 100 * time.Millisecond}).EnsureWorkspace(ctx, change.WorkspaceName); err != nil {
							return err
						}
					}
				}
				if !ensureOnly {
					if err := slicecontroller.ExecuteLeechSpatial(ctx, engine.Store, client, slicecontroller.ProcFS{}, proposal, currentEpoch, 100*time.Millisecond); err != nil {
						return err
					}
				}
			}
			if _, err := engine.CompleteSpatial(effect.SourceID); err != nil {
				return err
			}
		}
	}
	return nil
}

func runSliceProjection(args []string, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice projection-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sourceID := fs.String("source-id", "", "exact source ID")
	session := fs.String("session", "", "exact session name")
	token := fs.String("token", "", "attachment token")
	socket := fs.String("control-socket", "", "controller socket")
	transport := fs.String("transport-command", cfg.Slice.TransportCommand, "transport executable")
	host := fs.String("host", cfg.Slice.SourceHost, "host destination")
	remoteSelf := fs.String("remote-self-command", cfg.Slice.RPCCommand[0], "remote packaged redeem executable")
	options := repeatFlag{}
	fs.Var(&options, "transport-option", "operator transport option")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	for _, v := range []string{*sourceID, *session, *token, *socket, *transport, *host, *remoteSelf} {
		if v == "" || strings.ContainsAny(v, "\x00\r\n") {
			_, _ = fmt.Fprintln(stderr, "invalid projection arguments")
			return 2
		}
	}
	if !slicerpc.ValidToken(*sourceID) || !zellijlive.SafeSessionName(*session) || !slicerpc.ValidToken(*token) || !filepath.IsAbs(*socket) || len(*socket) > 103 || mirror.ValidateDestination(*host) != nil || !safeRemoteProjectionToken(*remoteSelf) {
		_, _ = fmt.Fprintln(stderr, "invalid projection identity, destination, socket, or remote command")
		return 2
	}
	for _, option := range options.values {
		if option == "" || len(option) > 4096 || strings.ContainsAny(option, "\x00\r\n") {
			_, _ = fmt.Fprintln(stderr, "invalid projection transport option")
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return 1
	}
	readyToken := "ready_" + hex.EncodeToString(nonceBytes[:])
	sshArgs := []string{"-tt"}
	sshArgs = append(sshArgs, options.values...)
	sshArgs = append(sshArgs, "--", *host, *remoteSelf, "slice", "attach", "--session", *session, "--token", *token, "--ready-token", readyToken)
	cmd := exec.CommandContext(ctx, *transport, sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = stderr
	remoteOut, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return 1
	}
	if err := cmd.Start(); err != nil {
		request := slicecontroller.NewControlRequest(slicecontroller.VerbAttachmentLost, slicecontroller.SourcePayload{SourceID: *sourceID})
		_, _ = slicecontroller.CallControl(context.Background(), *socket, cfg.Slice.Controller.ControlTimeout, request)
		return 1
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	ready := make(chan struct{})
	relayDone := make(chan error, 1)
	go func() { relayDone <- slicecontroller.RelayAttachReadiness(remoteOut, stdout, readyToken, ready) }()
	timer := time.NewTimer(cfg.Slice.RequestTimeout)
	defer timer.Stop()
	var err error
	select {
	case <-ready:
		connected := slicecontroller.NewControlRequest(slicecontroller.VerbAttachmentConnected, slicecontroller.SourcePayload{SourceID: *sourceID})
		_, _ = slicecontroller.CallControl(context.Background(), *socket, cfg.Slice.Controller.ControlTimeout, connected)
		err = <-wait
	case relayErr := <-relayDone:
		_ = cmd.Process.Kill()
		err = <-wait
		if relayErr != nil {
			err = relayErr
		}
	case <-timer.C:
		_ = cmd.Process.Kill()
		err = <-wait
		if err == nil {
			err = errors.New("exact attachment readiness timed out")
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		err = <-wait
	}
	verb := slicecontroller.VerbAttachmentLost
	if err == nil {
		verb = slicecontroller.VerbClose
	}
	request := slicecontroller.NewControlRequest(verb, slicecontroller.SourcePayload{SourceID: *sourceID})
	_, _ = slicecontroller.CallControl(context.Background(), *socket, cfg.Slice.Controller.ControlTimeout, request)
	if ctx.Err() != nil {
		return 130
	}
	if err != nil {
		return 1
	}
	return 0
}

func safeRemoteProjectionToken(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	for _, r := range value {
		if !(r == '/' || r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func runSliceCloseFocused(args []string, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice close-focused", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", cfg.StateDir, "state directory")
	timeout := fs.Duration("timeout", cfg.Slice.Controller.ControlTimeout, "bounded close request")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	store, err := slicecontroller.NewStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice close-focused failed: %v\n", err)
		return 1
	}
	state, err := store.Read()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice close-focused failed: %v\n", err)
		return 1
	}
	resolver := sliceenv.Resolver{Keys: cfg.Slice.GraphicalContextKeys, Systemctl: cfg.Slice.SystemctlCommand}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	env, err := resolver.Resolve(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice close-focused failed: %v\n", err)
		return 1
	}
	client := niriipc.Client{SocketPath: env["NIRI_SOCKET"]}
	local, err := client.Snapshot(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice close-focused failed: %v\n", err)
		return 1
	}
	owned := slicecontroller.VerifyOwnedWindows(state, local, slicecontroller.ProcFS{})
	var focused *slicecontroller.OwnedWindow
	for i := range owned {
		if owned[i].Focused {
			focused = &owned[i]
			break
		}
	}
	if focused == nil {
		_, _ = fmt.Fprintln(stderr, "focused window is not a positively owned slice projection")
		return 1
	}
	request := slicecontroller.NewControlRequest(slicecontroller.VerbClose, slicecontroller.ClosePayload{SourceID: focused.SourceID, FocusRequired: true})
	if response, callErr := slicecontroller.CallControl(ctx, store.SocketPath(), *timeout, request); callErr == nil && response.Outcome.Status == "ok" {
		_ = json.NewEncoder(stdout).Encode(response)
		return 0
	}
	if err := slicecontroller.FocusedCloseFallback(ctx, store, controllerEngineConfig(cfg), client, slicecontroller.ProcFS{}, *focused, 100*time.Millisecond); err != nil {
		_, _ = fmt.Fprintf(stderr, "slice close-focused fallback failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "closed focused owned projection")
	return 0
}

type uint32RepeatFlag []uint32

func (flagValue *uint32RepeatFlag) String() string {
	parts := make([]string, len(*flagValue))
	for i, value := range *flagValue {
		parts[i] = strconv.FormatUint(uint64(value), 10)
	}
	return strings.Join(parts, ",")
}
func (flagValue *uint32RepeatFlag) Set(raw string) error {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || value == 0 {
		return fmt.Errorf("schema version must be a positive integer")
	}
	*flagValue = append(*flagValue, uint32(value))
	return nil
}

func runSliceInventoryInit(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice inventory init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "Terminal Redeemer state directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	store, err := sourceinventory.NewStore(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice inventory init failed: %v\n", err)
		return 1
	}
	state, err := (sourceinventory.Publisher{Store: store}).Initialize()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice inventory init failed: %v\n", err)
		return 1
	}
	payload := struct {
		SchemaVersion uint32 `json:"schema_version"`
		SourceHostID  string `json:"source_host_id"`
		Initialized   bool   `json:"initialized"`
	}{sliceprotocol.SchemaVersion, state.SourceHostID, true}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		_, _ = fmt.Fprintf(stderr, "slice inventory init encode failed: %v\n", err)
		return 1
	}
	return 0
}

type sliceInventorySnapshotOptions struct {
	stateDir        string
	niriSocket      string
	niriCommand     string
	zellijCommand   string
	zellijSocketDir string
	zellijCacheHome string
}

type versionedNiriObserver struct {
	client  niriipc.Client
	command string
}

func (observer versionedNiriObserver) Snapshot(ctx context.Context) (niriipc.State, error) {
	if observer.command != "" {
		if err := niriipc.VerifyVersion(ctx, observer.command, niriipc.SupportedVersion); err != nil {
			return niriipc.State{}, err
		}
	}
	return observer.client.Snapshot(ctx)
}

var collectSliceInventorySnapshot = func(ctx context.Context, options sliceInventorySnapshotOptions) (sliceprotocol.Envelope, error) {
	store, err := sourceinventory.NewStore(options.stateDir)
	if err != nil {
		return sliceprotocol.Envelope{}, err
	}
	boot, err := bootid.Current()
	if err != nil {
		return sliceprotocol.Envelope{}, err
	}
	fingerprint := func() (string, error) {
		currentBoot, err := bootid.Current()
		if err != nil {
			return "", err
		}
		return sourceinventory.NiriFingerprint(currentBoot, options.niriSocket)
	}
	publisher := sourceinventory.Publisher{
		Store:       store,
		Niri:        versionedNiriObserver{client: niriipc.Client{SocketPath: options.niriSocket}, command: options.niriCommand},
		Catalog:     zellijlive.CommandCataloger{Command: options.zellijCommand, SocketBase: options.zellijSocketDir, CacheHome: options.zellijCacheHome, BootID: boot},
		Builder:     sourceinventory.Builder{Processes: zellijlive.ProcObserver{}},
		Fingerprint: fingerprint,
	}
	return publisher.Snapshot(ctx)
}

func runSliceInventorySnapshot(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("slice inventory snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "Terminal Redeemer state directory")
	niriSocket := fs.String("niri-socket", os.Getenv("NIRI_SOCKET"), "direct Niri Unix socket")
	niriCommand := fs.String("niri-command", resolvedConfig.Slice.NiriCommand, "pinned Niri executable")
	zellijCommand := fs.String("zellij-command", resolvedConfig.Slice.ZellijCommand, "pinned Zellij executable")
	zellijSocketDir := fs.String("zellij-socket-dir", os.Getenv("ZELLIJ_SOCKET_DIR"), "Zellij socket base directory")
	zellijCacheHome := fs.String("zellij-cache-home", os.Getenv("XDG_CACHE_HOME"), "Zellij cache home for dead-session classification")
	timeout := fs.Duration("timeout", 15*time.Second, "bounded complete inventory attempt timeout")
	var accepted uint32RepeatFlag
	fs.Var(&accepted, "accept-schema-version", "accepted schema version (repeatable)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "slice inventory snapshot --timeout must be positive")
		return 2
	}
	if _, ok := sliceprotocol.Negotiate(accepted); !ok {
		payload := sliceprotocol.VersionError{Code: "unsupported_schema_version", SupportedSchemaVersions: sliceprotocol.SupportedVersions()}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(payload)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	envelope, err := collectSliceInventorySnapshot(ctx, sliceInventorySnapshotOptions{
		stateDir: *stateDir, niriSocket: *niriSocket, niriCommand: *niriCommand, zellijCommand: *zellijCommand,
		zellijSocketDir: *zellijSocketDir, zellijCacheHome: *zellijCacheHome,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slice inventory snapshot failed: %v\n", err)
		return 1
	}
	if err := sliceprotocol.Encode(stdout, envelope); err != nil {
		_, _ = fmt.Fprintf(stderr, "slice inventory snapshot encode failed: %v\n", err)
		return 1
	}
	return 0
}

func runMirror(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: redeem mirror <snapshot|list|open|status|close|paste-image> [flags]")
		return 2
	}
	if isHelpToken(args[0]) {
		_, _ = fmt.Fprintln(stdout, "usage: redeem mirror <snapshot|list|open|status|close|paste-image> [flags]")
		return 0
	}
	switch args[0] {
	case "snapshot":
		return runMirrorSnapshot(args[1:], resolvedConfig, stdout, stderr)
	case "list":
		return runMirrorList(args[1:], resolvedConfig, stdout, stderr)
	case "open":
		return runMirrorOpen(args[1:], resolvedConfig, stdout, stderr)
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
	snapshotFile    *string
	sshOptions      repeatFlag
	snapshotCommand repeatFlag
}

func addMirrorSourceFlags(fs *flag.FlagSet, cfg config.MirrorConfig) *mirrorSourceFlags {
	flags := &mirrorSourceFlags{
		host:            fs.String("host", cfg.SourceHost, "SSH source host"),
		sshCommand:      fs.String("ssh-command", cfg.SSHCommand, "SSH executable"),
		snapshotFile:    fs.String("snapshot-file", "", "read snapshot JSON locally instead of SSH"),
		sshOptions:      repeatFlag{values: append([]string(nil), cfg.SSHOptions...)},
		snapshotCommand: repeatFlag{values: append([]string(nil), cfg.SnapshotCommand...)},
	}
	fs.Var(&flags.sshOptions, "ssh-option", "SSH option (repeatable; first occurrence replaces config)")
	fs.Var(&flags.snapshotCommand, "snapshot-arg", "remote snapshot argv item (repeatable; first occurrence replaces config)")
	return flags
}

func acquireMirrorSnapshot(flags *mirrorSourceFlags) (mirror.Snapshot, string, error) {
	host := strings.TrimSpace(*flags.host)
	if strings.TrimSpace(*flags.snapshotFile) != "" {
		snapshot, err := mirror.ReadSnapshot(*flags.snapshotFile)
		if host == "" {
			host = snapshot.Host
		}
		return snapshot, host, err
	}
	if host == "" {
		return mirror.Snapshot{}, "", fmt.Errorf("source host is required (--host or mirror.sourceHost)")
	}
	snapshot, err := mirror.AcquireRemote(context.Background(), mirror.ExecRunner{}, mirror.RemoteConfig{
		Host: host, SSHCommand: *flags.sshCommand, SSHOptions: flags.sshOptions.values, SnapshotCommand: flags.snapshotCommand.values,
	})
	return snapshot, host, err
}

func runMirrorList(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	asJSON := fs.Bool("json", false, "emit discovered windows as JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	snapshot, host, err := acquireMirrorSnapshot(source)
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

func runMirrorOpen(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := addMirrorSourceFlags(fs, resolvedConfig.Mirror)
	mode := fs.String("mode", resolvedConfig.Mirror.DefaultMode, "attach or watch")
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
	if *mode != "attach" && *mode != "watch" {
		_, _ = fmt.Fprintf(stderr, "invalid mirror mode %q (expected attach or watch)\n", *mode)
		return 2
	}
	if *mode == "watch" {
		_, _ = fmt.Fprintf(stderr, "mirror watch is unsupported by pinned Zellij %s; no watch command was executed\n", zellijlive.PinnedVersion)
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
	snapshot, host, err := acquireMirrorSnapshot(source)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mirror open failed: %v\n", err)
		return 1
	}
	windows := mirror.Discover(snapshot)
	if len(windows) == 0 {
		_, _ = fmt.Fprintf(stderr, "no Kitty/Zellij windows found on %s\n", host)
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
		for i, window := range windows {
			_, _ = fmt.Fprintf(stdout, "%d\t%s\t%s\n", i+1, mirror.SessionName(window), window.Title)
		}
		_, _ = fmt.Fprint(stdout, "select session> ")
		line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		if readErr != nil {
			err = fmt.Errorf("interactive selection failed: %w", readErr)
		} else {
			choice, parseErr := strconv.Atoi(strings.TrimSpace(line))
			if parseErr != nil || choice < 1 || choice > len(windows) {
				err = fmt.Errorf("invalid selection %q", strings.TrimSpace(line))
			} else {
				selected = windows[choice-1 : choice]
			}
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
			LauncherCommand: *launcher, SelfCommand: *selfCommand, AppID: *appID, Mode: *mode,
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

func runResume(args []string, resolvedConfig config.Config, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", resolvedConfig.StateDir, "state directory")
	dryRun := fs.Bool("dry-run", false, "plan prior-boot terminal reconciliation without mutating")
	maxAge := fs.Duration("max-age", resolvedConfig.Restore.MaxCheckpointAge, "maximum checkpoint age")
	unresolved := fs.String("unresolved-workspace", resolvedConfig.Restore.UnresolvedWorkspace, "unresolved workspace policy: current, skip, or fail")
	fixture := fs.String("fixture", os.Getenv("REDEEM_NIRI_FIXTURE"), "current Niri JSON fixture path")
	niriCmd := fs.String("niri-cmd", captureNiriCommandDefault(resolvedConfig), "current Niri snapshot command")
	launcher := fs.String("launcher-command", resolvedConfig.Restore.Terminal.Command, "Kitty executable (not a shell command)")
	timeout := fs.Duration("timeout", resolvedConfig.Restore.ResumeTimeout, "per-phase correlation, attachment, and move timeout")
	pollInterval := fs.Duration("poll-interval", resolvedConfig.Restore.ResumePollInterval, "Niri and attachment poll interval")
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

	resumeCheckpoints, err := replay.ListResumeCheckpoints(*stateDir)
	if err != nil {
		writef(stderr, "resume checkpoint scan failed: %v\n", err)
		return 1
	}
	currentBootID, err := bootid.Current()
	if err != nil {
		writef(stderr, "resume boot ID failed: %v\n", err)
		return 1
	}
	selection := resume.Select(resumeCheckpoints, resume.SelectOptions{
		CurrentBootID: currentBootID,
		Host:          strings.TrimSpace(resolvedConfig.Host),
		Profile:       strings.TrimSpace(resolvedConfig.Profile),
		Now:           time.Now().UTC(),
		MaxAge:        *maxAge,
	})

	planner := resume.NewPlanner(resume.PlannerConfig{UnresolvedWorkspace: policy})
	var current model.State
	var available []string
	if selection.Status == resume.CandidateReady {
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
		available, err = procmeta.NewZellijSessionVerifier(nil).List()
		if err != nil {
			writef(stderr, "resume Zellij session discovery failed: %v\n", err)
			return 1
		}
	}

	plan := planner.Build(selection, current, available)
	if !*dryRun && selection.Status == resume.CandidateReady {
		actions := resume.NiriActions{Runner: resume.ExecActionRunner{Command: "niri"}}
		executor := resume.Executor{
			Config:   resume.ExecutorConfig{LauncherCommand: *launcher, Timeout: *timeout, PollInterval: *pollInterval},
			Launcher: resume.ExecLauncher{},
			Observer: resume.SnapshotObserver{Source: snapshotter},
			Probe:    resume.ProcAttachmentProbe{},
			Mover:    actions,
			Layout:   actions,
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
		writef(stdout, "resume_candidate status=%s reason=%q\n", plan.CandidateStatus, plan.Reason)
	} else {
		writef(stdout, "resume_candidate status=%s boot_id=%q captured_at=%s age=%s", plan.CandidateStatus, plan.BootID, plan.CapturedAt.UTC().Format(time.RFC3339Nano), plan.Age.Round(time.Second))
		if plan.Reason != "" {
			writef(stdout, " reason=%q", plan.Reason)
		}
		writeln(stdout)
	}
	if plan.CandidateStatus == resume.CandidateEmpty || plan.CandidateStatus == resume.CandidateStale || plan.CandidateStatus == resume.CandidateNotFound {
		writef(stdout, "resume_guidance restore_tui=%q restore_at=%q\n", "redeem restore tui", "redeem restore apply --at <RFC3339>")
	}
	for _, item := range plan.Items {
		writef(stdout, "resume_item window_key=%q session=%q status=%s", item.WindowKey, item.Session, item.Status)
		if item.Workspace != nil {
			writef(stdout, " workspace_method=%s workspace_id=%q workspace_name=%q workspace_output=%q workspace_index=%d", item.Workspace.Method, item.Workspace.ID, item.Workspace.Name, item.Workspace.Output, item.Workspace.Index)
		}
		if item.LayoutStatus != "" {
			writef(stdout, " layout_status=%s", item.LayoutStatus)
			if item.LayoutReason != "" {
				writef(stdout, " layout_reason=%q", item.LayoutReason)
			}
		}
		if item.Reason != "" {
			writef(stdout, " reason=%q", item.Reason)
		}
		writeln(stdout)
	}
	writef(stdout, "resume_summary ready=%d already_open=%d unavailable=%d degraded=%d stale=%d failed=%d skipped=%d restored=%d\n",
		plan.Summary.Ready, plan.Summary.AlreadyOpen, plan.Summary.Unavailable, plan.Summary.Degraded, plan.Summary.Stale, plan.Summary.Failed, plan.Summary.Skipped, plan.Summary.Restored)
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

	planner := restore.NewPlanner(restore.PlannerConfig{
		Terminal:     restore.TerminalConfig{Command: resolvedConfig.Restore.Terminal.Command, ZellijAttachOrCreate: resolvedConfig.Restore.Terminal.ZellijAttachOrCreate},
		AppAllowlist: resolvedConfig.Restore.AppAllowlist,
		AppMode:      parseAppModes(resolvedConfig.Restore.AppMode),
	})
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

	beforeState := tryReadNiriWindowsState(context.Background())

	executor := restore.NewExecutor(restore.ShellRunner{})
	result := executor.Execute(context.Background(), plan)
	if resolvedConfig.Restore.ReconcileWorkspaceMoves {
		time.Sleep(resolvedConfig.Restore.WorkspaceReconcileDelay)
		afterState := tryReadNiriWindowsState(context.Background())
		if beforeState != nil && afterState != nil {
			requests := restore.BuildMoveRequests(plan, *beforeState, *afterState)
			report := restore.ApplyMoveRequests(context.Background(), restore.NiriWindowMover{}, requests)
			if len(requests) > 0 {
				writef(stdout, "restore_workspace_moves moved=%d requested=%d failed=%d\n", report.Applied, len(requests), len(report.Failures))
				for _, failure := range report.Failures {
					writef(stdout, "restore_workspace_move_failed window_key=%s window_id=%d app_id=%s workspace=%s error=%q\n", failure.Request.WindowKey, failure.Request.WindowID, failure.Request.AppID, failure.Request.WorkspaceRef, failure.Err.Error())
				}
			}
		}
	}
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
	planner := restore.NewPlanner(restore.PlannerConfig{
		Terminal:     restore.TerminalConfig{Command: resolvedConfig.Restore.Terminal.Command, ZellijAttachOrCreate: resolvedConfig.Restore.Terminal.ZellijAttachOrCreate},
		AppAllowlist: resolvedConfig.Restore.AppAllowlist,
		AppMode:      parseAppModes(resolvedConfig.Restore.AppMode),
	})
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

	beforeState := tryReadNiriWindowsState(context.Background())

	executor := restore.NewExecutor(restore.ShellRunner{})
	result := executor.Execute(context.Background(), filteredPlan)
	if resolvedConfig.Restore.ReconcileWorkspaceMoves {
		time.Sleep(resolvedConfig.Restore.WorkspaceReconcileDelay)
		afterState := tryReadNiriWindowsState(context.Background())
		if beforeState != nil && afterState != nil {
			requests := restore.BuildMoveRequests(filteredPlan, *beforeState, *afterState)
			report := restore.ApplyMoveRequests(context.Background(), restore.NiriWindowMover{}, requests)
			if len(requests) > 0 {
				writef(stdout, "restore_workspace_moves moved=%d requested=%d failed=%d\n", report.Applied, len(requests), len(report.Failures))
				for _, failure := range report.Failures {
					writef(stdout, "restore_workspace_move_failed window_key=%s window_id=%d app_id=%s workspace=%s error=%q\n", failure.Request.WindowKey, failure.Request.WindowID, failure.Request.AppID, failure.Request.WorkspaceRef, failure.Err.Error())
				}
			}
		}
	}
	printRestoreExecution(stdout, result)
	return 0
}

func tryReadNiriWindowsState(ctx context.Context) *model.State {
	raw, err := niri.CommandSnapshotter{Command: "niri msg -j windows"}.Snapshot(ctx)
	if err != nil {
		return nil
	}
	state, err := niri.ParseSnapshot(raw)
	if err != nil {
		return nil
	}
	return &state
}

func parseAppModes(input map[string]string) map[string]restore.AppMode {
	out := make(map[string]restore.AppMode, len(input))
	for appID, rawMode := range input {
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		if mode == string(restore.AppModeOneShot) {
			out[appID] = restore.AppModeOneShot
			continue
		}
		out[appID] = restore.AppModePerWindow
	}
	return out
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
	writef(stdout, "prune_summary events_pruned=%d checkpoints_pruned=%d snapshots_pruned=%d\n", summary.EventsPruned, summary.CheckpointsPruned, summary.SnapshotsPruned)
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
	if result.CheckpointPath != "" {
		writef(stdout, "checkpoint=%s\n", result.CheckpointPath)
	}
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
	checkpointStore, err := checkpoints.NewStore(cfg.stateDir)
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
		Collector:       stateCollector,
		EventStore:      eventStore,
		CheckpointStore: checkpointStore,
		SnapshotStore:   snapshotStore,
		SnapshotEvery:   cfg.snapshotEvery,
		Host:            cfg.host,
		Profile:         cfg.profile,
		Source:          "capture.cli",
		Logger:          cfg.stderr,
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
	writeln(w, "  resume    Reconcile prior-boot terminal sessions")
	writeln(w, "  restore   Restore from history")
	writeln(w, "  history   Inspect timeline")
	writeln(w, "  mirror    Snapshot, discover, and mirror live terminal sessions")
	writeln(w, "  slice     Continuously project and manage live host terminals")
	writeln(w, "  prune     Prune old events/checkpoints/snapshots")
	writeln(w, "  bottle    Bottle workflows (V2)")
	writeln(w, "  doctor    Read-only capture/resume diagnostics")
	writeln(w)
	writeln(w, "Flags:")
	writeln(w, "  --config <path>  Path to YAML config file")
	writeln(w, "  -h, --help  Show help")
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
