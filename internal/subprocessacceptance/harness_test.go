//go:build linux

// Package subprocessacceptance exercises the packaged command at process, argv,
// Unix-socket, JSON-line and durable-state boundaries.  The helper executable
// aliases in this file emulate only Niri/Kitty/OpenSSH process boundaries.
package subprocessacceptance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceattach"
	"github.com/jmo/terminal-redeemer/internal/slicecontroller"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
	"golang.org/x/sys/unix"
)

const topologyName = "topology.json"

type topology struct {
	Redeem, Zellij, HostConfig, HostState string
	HostEnv                               []string
	Ledger                                string
	FaultDir                              string
	OwnerID                               string
	SourceHostID                          string
	SourceEpoch                           string
	SourceFingerprint                     string
}

type invocation struct {
	Kind      string   `json:"kind"`
	PID       int      `json:"pid"`
	StartTime uint64   `json:"start_time,omitempty"`
	Argv      []string `json:"argv"`
	Verb      string   `json:"verb,omitempty"`
	Token     string   `json:"token,omitempty"`
	InputSHA  string   `json:"input_sha256,omitempty"`
	OutputSHA string   `json:"output_sha256,omitempty"`
	Dropped   bool     `json:"dropped,omitempty"`
	EnvKeys   []string `json:"env_keys,omitempty"`
}

type windowRegistration struct {
	PID, PPID int
	StartTime uint64
	AppID     string
	Argv      []string
	At        int64
}

func TestMain(m *testing.M) {
	if os.Getenv("TERMINAL_REDEEMER_CRASH_RPC_HELPER") == "1" {
		os.Exit(runCrashRPCHelper())
	}
	switch filepath.Base(os.Args[0]) {
	case "niri":
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println(niriipc.SupportedVersionOutput)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "controlled niri accepts only --version")
		os.Exit(64)
	case "ssh":
		os.Exit(runSSHShim())
	case "kitty":
		os.Exit(runKittyShim())
	case "systemctl":
		fmt.Fprintln(os.Stderr, "unexpected systemctl use")
		os.Exit(70)
	}
	os.Exit(m.Run())
}

func helperTopology() (topology, error) {
	var out topology
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), topologyName))
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(payload, &out)
	return out, err
}

func appendJSON(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(payload)
	return err
}

func envKeyLedger() []string {
	keys := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		if i := strings.IndexByte(item, '='); i >= 0 {
			keys = append(keys, item[:i])
		}
	}
	return keys
}

func runSSHShim() int {
	topo, err := helperTopology()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 70
	}
	parent := os.Getppid()
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0, 0, 0); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 70
	}
	if os.Getppid() != parent {
		return 75
	}
	// The packaged caller is already an explicit owned process group. The shim
	// remains in that group while its exact owner/start identity is also logged,
	// so cancellation cannot escape either ownership mechanism.
	shimCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	start, _ := processStartTime(os.Getpid())
	_ = appendJSON(topo.Ledger, invocation{Kind: "owned-ssh", PID: os.Getpid(), StartTime: start})
	args := os.Args[1:]
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+3 >= len(args) {
		fmt.Fprintln(os.Stderr, "invalid shell-inert ssh argv")
		return 64
	}
	interactive := len(args) > 0 && args[0] == "-tt"
	if !interactive {
		if len(args) < 7 || args[0] != "-T" || args[1] != "-o" || !strings.HasPrefix(args[2], "ServerAliveInterval=") || args[3] != "-o" || !strings.HasPrefix(args[4], "ServerAliveCountMax=") {
			fmt.Fprintln(os.Stderr, "invalid RPC ssh argv")
			return 64
		}
	}
	remote := args[sep+1:]
	if remote[0] != "host.test" || remote[1] != topo.Redeem || remote[2] != "slice" || (remote[3] != "rpc" && remote[3] != "attach") {
		fmt.Fprintln(os.Stderr, "unexpected destination or remote argv")
		return 64
	}
	if interactive {
		record := invocation{Kind: "ssh", PID: os.Getpid(), StartTime: start, Argv: append([]string(nil), os.Args...), Verb: "attach", EnvKeys: envKeyLedger()}
		_ = appendJSON(topo.Ledger, record)
		if consumeMarker(filepath.Join(topo.FaultDir, "gate-next-attach")) {
			for !consumeMarker(filepath.Join(topo.FaultDir, "release-host-attach")) {
				if markerPresent(filepath.Join(topo.FaultDir, "abort-all-gates")) || shimCtx.Err() != nil {
					return 75
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		if consumeMarker(filepath.Join(topo.FaultDir, "fail-next-attach")) || markerPresent(filepath.Join(topo.FaultDir, "fail-attach-held")) {
			return 75
		}
		childArgs := append([]string{topo.Redeem, "--config", topo.HostConfig}, remote[2:]...)
		readyToken := ""
		for i, arg := range remote {
			if arg == "--ready-token" && i+1 < len(remote) {
				readyToken = remote[i+1]
			}
		}
		ptyEnv := append(append([]string(nil), topo.HostEnv...), "TERM=xterm-256color")
		return runPTYCommand(shimCtx, childArgs, ptyEnv, true, readyToken, topo.Ledger)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, (16<<20)+1))
	if err != nil || len(input) > 16<<20 {
		return 65
	}
	verb, token := "attach", ""
	if remote[3] == "rpc" {
		var request struct {
			Verb    string `json:"verb"`
			Payload struct {
				Token string `json:"token"`
			} `json:"payload"`
		}
		if json.Unmarshal(input, &request) != nil {
			return 65
		}
		verb, token = request.Verb, request.Payload.Token
	}
	sum := sha256.Sum256(input)
	record := invocation{Kind: "ssh", PID: os.Getpid(), StartTime: start, Argv: append([]string(nil), os.Args...), Verb: verb, Token: token, InputSHA: hex.EncodeToString(sum[:]), EnvKeys: envKeyLedger()}
	_ = appendJSON(topo.Ledger, record)
	if verb == "launch" && token != "" {
		_ = os.WriteFile(filepath.Join(topo.FaultDir, "request-"+token+".json"), input, 0o600)
	}
	childArgs := []string{"--config", topo.HostConfig}
	childArgs = append(childArgs, remote[2:]...)
	cmd := exec.CommandContext(shimCtx, topo.Redeem, childArgs...)
	cmd.Env = append([]string(nil), topo.HostEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if consumeMarker(filepath.Join(topo.FaultDir, "gate-host-next-"+verb)) {
		stdin, pipeErr := cmd.StdinPipe()
		if pipeErr != nil || cmd.Start() != nil {
			return 70
		}
		childStart, _ := processStartTime(cmd.Process.Pid)
		_ = appendJSON(topo.Ledger, invocation{Kind: "host-rpc-started", PID: cmd.Process.Pid, StartTime: childStart, Verb: verb, Token: token})
		for !consumeMarker(filepath.Join(topo.FaultDir, "release-host-"+verb)) {
			if consumeMarker(filepath.Join(topo.FaultDir, "abort-host-"+verb+"-"+token)) || markerPresent(filepath.Join(topo.FaultDir, "abort-all-gates")) || shimCtx.Err() != nil {
				_ = stdin.Close()
				terminateAndWait(cmd, 2*time.Second)
				return 75
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, _ = stdin.Write(input)
		_ = stdin.Close()
		err = cmd.Wait()
	} else {
		cmd.Stdin = bytes.NewReader(input)
		err = cmd.Run()
	}
	outSum := sha256.Sum256(stdout.Bytes())
	dropped := err == nil && consumeMarker(filepath.Join(topo.FaultDir, "drop-next-"+verb))
	record.OutputSHA, record.Dropped = hex.EncodeToString(outSum[:]), dropped
	_ = appendJSON(topo.Ledger, record)
	if dropped {
		return 75
	}
	_, _ = os.Stdout.Write(stdout.Bytes())
	if err != nil {
		return exitCode(err)
	}
	return 0
}

func consumeMarker(path string) bool { return os.Remove(path) == nil }
func markerPresent(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
func exitCode(err error) int {
	var e *exec.ExitError
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return 1
}

type crashHostTransaction struct {
	direct slicerpc.DirectHostTransaction
	topo   topology
}

func (t crashHostTransaction) EnsureSession(ctx context.Context, r slicerpc.TokenRecord) (bool, error) {
	started, err := t.direct.EnsureSession(ctx, r)
	if err != nil {
		_ = appendJSON(t.topo.Ledger, invocation{Kind: "transaction-error", Verb: "ensure-session", Token: r.Token, Argv: []string{err.Error()}, PID: os.Getpid()})
	}
	return started, err
}
func (t crashHostTransaction) PlanKitty(ctx context.Context, r slicerpc.TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	return t.direct.PlanKitty(ctx, r)
}
func (t crashHostTransaction) PrepareKitty(ctx context.Context, r slicerpc.TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	return t.direct.PrepareKitty(ctx, r)
}
func (t crashHostTransaction) EnsureKitty(ctx context.Context, r slicerpc.TokenRecord) (int, uint64, bool, error) {
	pid, windowID, started, err := t.direct.EnsureKitty(ctx, r)
	if err == nil && started && windowID != 0 && consumeMarker(filepath.Join(t.topo.FaultDir, "gate-kitty-after-start")) {
		start, _ := processStartTime(os.Getpid())
		_ = appendJSON(t.topo.Ledger, invocation{Kind: "kitty-start-gated", PID: os.Getpid(), StartTime: start, Token: r.Token})
		for !markerPresent(filepath.Join(t.topo.FaultDir, "abort-all-gates")) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return pid, windowID, started, err
}
func (t crashHostTransaction) Place(ctx context.Context, r slicerpc.TokenRecord, id uint64) error {
	return t.direct.Place(ctx, r, id)
}
func (t crashHostTransaction) CleanupKitty(ctx context.Context, r slicerpc.TokenRecord) error {
	err := t.direct.CleanupKitty(ctx, r)
	if err == nil && consumeMarker(filepath.Join(t.topo.FaultDir, "gate-after-checked-cleanup")) {
		start, _ := processStartTime(os.Getpid())
		_ = appendJSON(t.topo.Ledger, invocation{Kind: "cleanup-gated", PID: os.Getpid(), StartTime: start, Token: r.Token})
		for !markerPresent(filepath.Join(t.topo.FaultDir, "abort-all-gates")) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return err
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		if at := strings.IndexByte(item, '='); at > 0 {
			out[item[:at]] = item[at+1:]
		}
	}
	return out
}

// runCrashRPCHelper is a harness-only RPC process. It uses the production token
// store and DirectHostTransaction, while its sole injected boundary blocks only
// after a selected durable journal fsync. No production command reads these
// markers or exposes fault configuration.
func runCrashRPCHelper() int {
	topo, err := helperTopology()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 70
	}
	request, err := slicerpc.DecodeRequest(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 65
	}
	store, err := slicerpc.NewTokenStore(topo.HostState)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 70
	}
	allEnvironment := envMap(topo.HostEnv)
	environment := map[string]string{
		"NIRI_SOCKET": allEnvironment["NIRI_SOCKET"], "WAYLAND_DISPLAY": allEnvironment["WAYLAND_DISPLAY"], "XDG_RUNTIME_DIR": allEnvironment["XDG_RUNTIME_DIR"],
	}
	niri := niriipc.Client{SocketPath: environment["NIRI_SOCKET"], Timeout: 5 * time.Second}
	direct := slicerpc.DirectHostTransaction{
		SelfCommand: topo.Redeem, ZellijCommand: topo.Zellij, KittyCommand: filepath.Join(filepath.Dir(os.Args[0]), "kitty"),
		Environment: environment, Niri: niri, SourceEpoch: topo.SourceEpoch,
		ZellijSocketDir: allEnvironment["ZELLIJ_SOCKET_DIR"], CreationCacheRoot: filepath.Join(topo.HostState, "slice", "host-zellij-create"),
		ShimCache: filepath.Join(topo.HostState, "slice", "host-zellij-shim"), PollInterval: 5 * time.Millisecond,
	}
	if consumeMarker(filepath.Join(topo.FaultDir, "fail-kitty-before-exec")) {
		direct.StartKitty = func(context.Context, string, []string, []string) (int, bool, error) {
			return 0, false, errors.New("injected definite pre-start exec failure")
		}
	}
	tx := crashHostTransaction{direct: direct, topo: topo}
	resolveContext := func(context.Context) (map[string]string, error) {
		return map[string]string{
			"NIRI_SOCKET": environment["NIRI_SOCKET"], "WAYLAND_DISPLAY": environment["WAYLAND_DISPLAY"], "XDG_RUNTIME_DIR": environment["XDG_RUNTIME_DIR"],
		}, nil
	}
	packagedSnapshot := func(ctx context.Context) (sliceprotocol.Envelope, error) {
		cmd := exec.CommandContext(ctx, topo.Redeem, "--config", topo.HostConfig, "slice", "inventory", "snapshot", "--state-dir", topo.HostState, "--accept-schema-version", "2")
		cmd.Env = append([]string(nil), topo.HostEnv...)
		payload, err := cmd.CombinedOutput()
		if err != nil {
			return sliceprotocol.Envelope{}, fmt.Errorf("packaged inventory proof: %w: %s", err, payload)
		}
		var envelope sliceprotocol.Envelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return sliceprotocol.Envelope{}, err
		}
		return envelope, nil
	}
	server := slicerpc.Server{
		SourceHostID: topo.SourceHostID, SourceEpoch: topo.SourceEpoch, SourceFingerprint: topo.SourceFingerprint,
		Tokens: store, Niri: niri, HostTransaction: tx, PollInterval: 5 * time.Millisecond,
		ProveCommit: func(ctx context.Context, record slicerpc.TokenRecord) (string, string, error) {
			return slicerpc.ProveRoutedCommit(ctx, record, topo.SourceEpoch, topo.SourceFingerprint, resolveContext, packagedSnapshot, direct)
		},
		AfterDurableStage: func(record slicerpc.TokenRecord) {
			target, _ := os.ReadFile(filepath.Join(topo.FaultDir, "gate-stage"))
			if strings.TrimSpace(string(target)) != record.Stage {
				return
			}
			start, _ := processStartTime(os.Getpid())
			_ = appendJSON(topo.Ledger, invocation{Kind: "stage-gated", PID: os.Getpid(), StartTime: start, Verb: record.Stage, Token: record.Token})
			for !markerPresent(filepath.Join(topo.FaultDir, "abort-all-gates")) {
				time.Sleep(5 * time.Millisecond)
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := slicerpc.EncodeResponse(os.Stdout, server.Handle(ctx, request)); err != nil {
		return 70
	}
	return 0
}

func runKittyShim() int {
	topo, err := helperTopology()
	if err != nil {
		return 70
	}
	args := os.Args[1:]
	start, _ := processStartTime(os.Getpid())
	record := invocation{Kind: "kitty", PID: os.Getpid(), StartTime: start, Argv: append([]string(nil), os.Args...), EnvKeys: envKeyLedger()}
	_ = appendJSON(topo.Ledger, record)
	if len(args) == 0 {
		return 0
	}
	appID := ""
	childAt := -1
	for i := 0; i < len(args); i++ {
		if args[i] == "--class" && i+1 < len(args) {
			appID = args[i+1]
			i++
			continue
		}
		if args[i] == "-e" && i+1 < len(args) {
			childAt = i + 1
			break
		}
		if filepath.IsAbs(args[i]) && args[i] == topo.Redeem {
			childAt = i
			break
		}
	}
	if appID != "" {
		reg := windowRegistration{PID: os.Getpid(), PPID: os.Getppid(), StartTime: start, AppID: appID, Argv: append([]string(nil), os.Args...), At: time.Now().UnixNano()}
		if socket := os.Getenv("NIRI_SOCKET"); socket != "" {
			_ = appendJSON(socket+".windows", reg)
		}
	}
	if childAt < 0 {
		return 0
	}
	if consumeMarker(filepath.Join(topo.FaultDir, "gate-next-kitty-child")) {
		_ = appendJSON(topo.Ledger, invocation{Kind: "kitty-child-gated", PID: os.Getpid(), StartTime: start, Argv: append([]string(nil), os.Args...)})
		for !consumeMarker(filepath.Join(topo.FaultDir, "release-kitty-child")) {
			if markerPresent(filepath.Join(topo.FaultDir, "abort-all-gates")) {
				return 75
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return runPTYChild(args[childAt:])
}

func runPTYChild(argv []string) int {
	return runPTYCommand(context.Background(), argv, os.Environ(), false, "", "")
}

func runPTYCommand(ctx context.Context, argv, env []string, relay bool, readyToken, ledger string) int {
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 71
	}
	master := os.NewFile(uintptr(masterFD), "ptmx")
	defer master.Close()
	if err = unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		return 71
	}
	n, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		return 71
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR, 0)
	if err != nil {
		return 71
	}
	defer slave.Close()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append([]string(nil), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: syscall.SIGTERM}
	if err = cmd.Start(); err != nil {
		return 71
	}
	if ledger != "" {
		childStart, _ := processStartTime(cmd.Process.Pid)
		_ = appendJSON(ledger, invocation{Kind: "pty-child-started", PID: cmd.Process.Pid, StartTime: childStart, Token: readyToken})
	}
	if relay {
		go io.Copy(master, os.Stdin)
		// Relay the packaged attach output unchanged and ledger readiness only
		// when that packaged wrapper emits its authenticated exact marker. This
		// avoids a race-prone synthetic descendant poll under -race.
		go io.Copy(&readyRelay{dst: os.Stdout, marker: []byte(sliceattach.ReadyMarker(readyToken)), ledger: ledger, socket: os.Getenv("NIRI_SOCKET"), token: readyToken}, master)
	} else {
		go io.Copy(io.Discard, master)
	}
	waitErr := cmd.Wait()
	if ledger != "" {
		_ = appendJSON(ledger, invocation{Kind: "pty-child-exited", PID: cmd.Process.Pid, Token: readyToken, Verb: strconv.Itoa(exitCode(waitErr))})
	}
	return exitCode(waitErr)
}

type readyRelay struct {
	dst            io.Writer
	marker         []byte
	ledger, socket string
	token          string
	buffer         []byte
	recorded       bool
}

func (w *readyRelay) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if !w.recorded && len(w.marker) > 0 {
		w.buffer = append(w.buffer, p...)
		if bytes.Contains(w.buffer, w.marker) {
			w.recorded = true
			record := invocation{Kind: "attach-ready", PID: os.Getpid(), Verb: "attach", Token: w.token}
			if w.ledger != "" {
				_ = appendJSON(w.ledger, record)
			}
			if w.socket != "" {
				_ = appendJSON(w.socket+".ready", record)
			}
		} else if keep := len(w.marker) * 2; keep > 0 && len(w.buffer) > keep {
			w.buffer = append([]byte(nil), w.buffer[len(w.buffer)-keep:]...)
		}
	}
	return n, err
}

type niriServer struct {
	mu                sync.Mutex
	path              string
	ownerID           string
	listener          net.Listener
	workspaces        []niriipc.Workspace
	windows           []niriipc.Window
	requests          []string
	registrations     int
	nextID            uint64
	registrationsHeld bool
	failNextAction    string
	failedActions     int
	done              chan struct{}
}

func newNiriServer(path string, sentinelPID int, ownerID string) (*niriServer, error) {
	work, other, out := "Work", "Other", "DP-1"
	wid := uint64(1)
	s := &niriServer{path: path, ownerID: ownerID, nextID: 100, done: make(chan struct{}), workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Name: &work, Output: &out, IsActive: true, IsFocused: true}, {ID: 2, Index: 2, Name: &other, Output: &out}}, windows: []niriipc.Window{{ID: 90, Title: "sentinel", AppID: "unrelated.sentinel", PID: sentinelPID, WorkspaceID: &wid, IsFocused: false, Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{400, 300}, WindowSize: []int{400, 300}}}}}
	if err := s.start(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *niriServer) start() error {
	_ = os.Remove(s.path)
	listener, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	if err = os.Chmod(s.path, 0o600); err != nil {
		listener.Close()
		return err
	}
	s.listener = listener
	go s.serve(listener)
	return nil
}
func (s *niriServer) restart() error {
	s.mu.Lock()
	old := s.listener
	s.mu.Unlock()
	_ = old.Close()
	return s.start()
}
func (s *niriServer) close() {
	s.mu.Lock()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Unlock()
	_ = os.Remove(s.path)
	close(s.done)
}
func (s *niriServer) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}
func (s *niriServer) syncRegistrations() {
	payload, err := os.ReadFile(s.path + ".windows")
	if err != nil {
		return
	}
	lines := bytes.Split(bytes.TrimSpace(payload), []byte{'\n'})
	for !s.registrationsHeld && s.registrations < len(lines) {
		var r windowRegistration
		readyPayload, _ := os.ReadFile(s.path + ".ready")
		readyCount := 0
		if trimmed := bytes.TrimSpace(readyPayload); len(trimmed) > 0 {
			readyCount = len(bytes.Split(trimmed, []byte{'\n'}))
		}
		processReady := json.Unmarshal(lines[s.registrations], &r) == nil && r.At <= time.Now().UnixNano() && (processTreeContains(r.PID, "zellij") || readyCount > 0)
		if processReady {
			workspace := uint64(1)
			floating := false
			position, size := []int{2, 1}, []int{960, 540}
			retained := s.windows[:0]
			for _, window := range s.windows {
				if window.AppID != r.AppID {
					retained = append(retained, window)
				}
			}
			s.windows = retained
			s.nextID++
			s.windows = append(s.windows, niriipc.Window{ID: s.nextID, Title: "controlled kitty", AppID: r.AppID, PID: r.PID, WorkspaceID: &workspace, IsFloating: floating, Layout: niriipc.Layout{Position: position, TileSize: []float64{float64(size[0]), float64(size[1])}, WindowSize: size}})
			s.registrations++
		} else if r.PID > 0 && processExited(r.PID) {
			// A failed controlled launch must not head-of-line block later real
			// registrations. A pidfd provides stable OS-level exit evidence.
			s.registrations++
		} else {
			break
		}
	}
}
func processTreeContains(root int, executable string) bool {
	queue := []int{root}
	seen := map[int]bool{}
	for len(queue) > 0 && len(seen) < 256 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if pid != root {
			raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
			if err == nil {
				argv := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
				if len(argv) > 0 && filepath.Base(string(argv[0])) == executable {
					return true
				}
			}
		}
		tasks, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
		if err != nil {
			continue
		}
		children := map[int]bool{}
		for _, task := range tasks {
			payload, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", task.Name(), "children"))
			if err != nil {
				continue
			}
			for _, field := range strings.Fields(string(payload)) {
				if child, err := strconv.Atoi(field); err == nil && child > 0 {
					children[child] = true
				}
			}
		}
		for child := range children {
			queue = append(queue, child)
		}
	}
	return false
}

func (s *niriServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncRegistrations()
	s.requests = append(s.requests, line)
	switch line {
	case `"EventStream"`:
		ws, _ := json.Marshal(s.workspaces)
		wins, _ := json.Marshal(s.windows)
		fmt.Fprintf(conn, "{\"Ok\":\"Handled\"}\n{\"WorkspacesChanged\":{\"workspaces\":%s}}\n{\"WindowsChanged\":{\"windows\":%s}}\n{\"ConfigLoaded\":{\"failed\":false}}\n", ws, wins)
	case `"Outputs"`:
		io.WriteString(conn, "{\"Ok\":{\"Outputs\":{\"DP-1\":{\"name\":\"DP-1\",\"logical\":{\"x\":0,\"y\":0,\"width\":1920,\"height\":1080,\"scale\":1,\"transform\":\"Normal\"}}}}}\n")
	default:
		if !strings.Contains(line, `"Action"`) {
			io.WriteString(conn, "{\"Err\":\"unexpected\"}\n")
			return
		}
		if s.failNextAction != "" && strings.Contains(line, `"`+s.failNextAction+`"`) {
			s.failNextAction = ""
			s.failedActions++
			io.WriteString(conn, "{\"Err\":\"injected spatial action failure\"}\n")
			return
		}
		s.applyAction([]byte(line))
		io.WriteString(conn, "{\"Ok\":\"Handled\"}\n")
	}
}
func (s *niriServer) applyAction(raw []byte) {
	var env struct {
		Action map[string]json.RawMessage `json:"Action"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	for kind, payload := range env.Action {
		switch kind {
		case "MoveWindowToWorkspace":
			var p struct {
				WindowID  uint64 `json:"window_id"`
				Reference struct {
					ID uint64 `json:"Id"`
				} `json:"reference"`
			}
			_ = json.Unmarshal(payload, &p)
			for i := range s.windows {
				if s.windows[i].ID == p.WindowID {
					id := p.Reference.ID
					s.windows[i].WorkspaceID = &id
				}
			}
		case "CloseWindow":
			var p struct {
				ID uint64 `json:"id"`
			}
			_ = json.Unmarshal(payload, &p)
			out := s.windows[:0]
			for _, w := range s.windows {
				if w.ID == p.ID {
					// Pin before reading /proc, then require the registered start identity
					// and exact fixture owner again immediately before pidfd signaling.
					if reg, ok := s.registrationForPID(w.PID); ok {
						if proc, err := pinOwnedProcess(w.PID, processIdentity{PID: reg.PID, StartTime: reg.StartTime}, s.ownerID); err == nil {
							_ = terminatePinned(proc, time.Second)
							proc.close()
						}
					}
				} else {
					out = append(out, w)
				}
			}
			s.windows = out
		case "MoveWindowToFloating":
			for i := range s.windows {
				var p struct {
					ID uint64 `json:"id"`
				}
				_ = json.Unmarshal(payload, &p)
				if s.windows[i].ID == p.ID {
					s.windows[i].IsFloating = true
				}
			}
		case "MoveWindowToTiling":
			for i := range s.windows {
				var p struct {
					ID uint64 `json:"id"`
				}
				_ = json.Unmarshal(payload, &p)
				if s.windows[i].ID == p.ID {
					s.windows[i].IsFloating = false
					if len(s.windows[i].Layout.Position) == 0 {
						s.windows[i].Layout.Position = []int{7, 3}
					}
				}
			}
		case "SetWindowWidth", "SetWindowHeight":
			var p struct {
				ID     uint64 `json:"id"`
				Change struct {
					Percent float64 `json:"SetProportion"`
				} `json:"change"`
			}
			_ = json.Unmarshal(payload, &p)
			for i := range s.windows {
				if s.windows[i].ID == p.ID {
					if kind == "SetWindowWidth" {
						s.windows[i].Layout.WindowSize[0] = int(1920 * p.Change.Percent / 100)
					} else {
						s.windows[i].Layout.WindowSize[1] = int(1080 * p.Change.Percent / 100)
					}
				}
			}
		case "SetWorkspaceName":
			var p struct {
				Name      string `json:"name"`
				Workspace struct {
					ID uint64 `json:"Id"`
				} `json:"workspace"`
			}
			_ = json.Unmarshal(payload, &p)
			for i := range s.workspaces {
				if s.workspaces[i].ID == p.Workspace.ID {
					s.workspaces[i].Name = &p.Name
				}
			}
		}
	}
}
func (s *niriServer) actionLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.requests {
		if strings.Contains(r, `"Action"`) {
			out = append(out, r)
		}
	}
	return out
}
func (s *niriServer) requestLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}
func (s *niriServer) sentinelPresent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.windows {
		if w.ID == 90 && w.AppID == "unrelated.sentinel" {
			return true
		}
	}
	return false
}
func (s *niriServer) clearControlledWindows() {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.windows[:0]
	for _, w := range s.windows {
		if w.ID == 90 {
			out = append(out, w)
		}
	}
	s.windows = out
}
func (s *niriServer) holdRegistrations(held bool) {
	s.mu.Lock()
	s.registrationsHeld = held
	if !held {
		payload, _ := os.ReadFile(s.path + ".windows")
		lines := bytes.Split(bytes.TrimSpace(payload), []byte{'\n'})
		for s.registrations < len(lines) {
			var r windowRegistration
			if json.Unmarshal(lines[s.registrations], &r) != nil {
				break
			}
			workspace := uint64(1)
			floating := false
			position, size := []int{2, 1}, []int{960, 540}
			retained := s.windows[:0]
			for _, window := range s.windows {
				if window.AppID != r.AppID {
					retained = append(retained, window)
				}
			}
			s.windows = retained
			s.nextID++
			s.windows = append(s.windows, niriipc.Window{ID: s.nextID, Title: "controlled kitty", AppID: r.AppID, PID: r.PID, WorkspaceID: &workspace, IsFloating: floating, Layout: niriipc.Layout{Position: position, TileSize: []float64{float64(size[0]), float64(size[1])}, WindowSize: size}})
			s.registrations++
		}
	}
	s.mu.Unlock()
}
func (s *niriServer) registrationForPID(pid int) (windowRegistration, bool) {
	payload, _ := os.ReadFile(s.path + ".windows")
	for _, line := range bytes.Split(bytes.TrimSpace(payload), []byte{'\n'}) {
		var r windowRegistration
		if json.Unmarshal(line, &r) == nil && r.PID == pid && r.StartTime != 0 {
			return r, true
		}
	}
	return windowRegistration{}, false
}

func (s *niriServer) registrationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, _ := os.ReadFile(s.path + ".windows")
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return 0
	}
	return len(bytes.Split(payload, []byte{'\n'}))
}
func (s *niriServer) setControlledFocus(focused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.windows {
		s.windows[i].IsFocused = focused && s.windows[i].ID != 90
	}
}
func (s *niriServer) replaceControlledIdentity(pid int, appID string) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.windows {
		if s.windows[i].ID != 90 {
			priorPID, priorApp := s.windows[i].PID, s.windows[i].AppID
			s.windows[i].PID, s.windows[i].AppID = pid, appID
			return func() {
				s.mu.Lock()
				s.windows[i].PID, s.windows[i].AppID = priorPID, priorApp
				s.mu.Unlock()
			}
		}
	}
	return func() {}
}

func (s *niriServer) divergeControlledSpatial() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.windows {
		if s.windows[i].ID == 90 {
			continue
		}
		workspace := uint64(2)
		s.windows[i].WorkspaceID = &workspace
		s.windows[i].IsFloating = true
		s.windows[i].Layout.Position = nil
		s.windows[i].Layout.WindowSize = []int{480, 270}
		return s.windows[i].ID, true
	}
	return 0, false
}

func (s *niriServer) controlledSpatial(windowID uint64) (niriipc.Window, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, window := range s.windows {
		if window.ID == windowID {
			return window, true
		}
	}
	return niriipc.Window{}, false
}

func (s *niriServer) injectActionFailure(kind string) {
	s.mu.Lock()
	s.failNextAction = kind
	s.mu.Unlock()
}

func (s *niriServer) failedActionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failedActions
}

func TestPinnedOwnedProcessRefusesChangedIdentityAndOwner(t *testing.T) {
	ownerID := "/tmp/trh-pidfd-regression-" + strconv.Itoa(os.Getpid())
	cmd := exec.Command("sleep", "30")
	cmd.Env = []string{"TERMINAL_REDEEMER_HARNESS_OWNER=" + ownerID}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc, err := pinStartedOwnedProcess(cmd.Process.Pid, processIdentity{}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.close()
	defer func() {
		proc.ownerID = ownerID
		_ = terminatePinned(proc, time.Second)
		_ = cmd.Wait()
	}()

	if wrongStart, pinErr := pinOwnedProcess(cmd.Process.Pid, processIdentity{PID: cmd.Process.Pid, StartTime: proc.identity.StartTime + 1}, ownerID); !errors.Is(pinErr, errProcessIdentityChanged) {
		if wrongStart != nil {
			wrongStart.close()
		}
		t.Fatalf("changed/reused start identity was not refused: %v", pinErr)
	}
	if wrongOwner, pinErr := pinOwnedProcess(cmd.Process.Pid, proc.identity, ownerID+"-ambient"); !errors.Is(pinErr, errProcessOwnerChanged) {
		if wrongOwner != nil {
			wrongOwner.close()
		}
		t.Fatalf("changed owner was not refused: %v", pinErr)
	}

	// Revalidation, not only acquisition, protects the send boundary. Simulate
	// stale owner evidence and prove SIGTERM is refused while the child lives.
	proc.ownerID = ownerID + "-changed"
	if err := proc.signal(syscall.SIGTERM); !errors.Is(err, errProcessOwnerChanged) {
		t.Fatalf("signal did not refuse changed owner: %v", err)
	}
	if exited, err := proc.wait(0); err != nil || exited {
		t.Fatalf("refused signal harmed owned child: exited=%t err=%v", exited, err)
	}
	proc.ownerID = ownerID
}

func TestPinnedOwnedProcessRejectsEmbeddedEnvironmentMarkers(t *testing.T) {
	ownerID := "/tmp/trh-pidfd-decoy-" + strconv.Itoa(os.Getpid())
	required := "ZELLIJ_SOCKET_DIR=" + filepath.Join(ownerID, "zellij")
	cases := []struct {
		name        string
		env         []string
		requiredEnv []string
	}{
		{
			name: "owner marker embedded in another entry",
			env:  []string{"DECOY=TERMINAL_REDEEMER_HARNESS_OWNER=" + ownerID},
		},
		{
			name:        "required marker embedded in another entry",
			env:         []string{"TERMINAL_REDEEMER_HARNESS_OWNER=" + ownerID, "DECOY=" + required},
			requiredEnv: []string{required},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "0.2")
			cmd.Env = tc.env
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if proc, err := pinStartedOwnedProcess(cmd.Process.Pid, processIdentity{}, ownerID, tc.requiredEnv...); !errors.Is(err, errProcessOwnerChanged) {
				if proc != nil {
					proc.close()
				}
				t.Fatalf("embedded environment marker was accepted: %v", err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPinnedOwnedProcessAbsentExitedAndZombie(t *testing.T) {
	if proc, err := pinOwnedProcess(1<<30, processIdentity{}, "/tmp/absent-owner"); err == nil {
		proc.close()
		t.Fatal("absent PID unexpectedly pinned")
	}

	ownerID := "/tmp/trh-pidfd-zombie-" + strconv.Itoa(os.Getpid())
	cmd := exec.Command("sleep", "30")
	cmd.Env = []string{"TERMINAL_REDEEMER_HARNESS_OWNER=" + ownerID}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc, err := pinStartedOwnedProcess(cmd.Process.Pid, processIdentity{}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.close()
	if err := proc.signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if exited, err := proc.wait(2 * time.Second); err != nil || !exited {
		t.Fatalf("pidfd did not observe child exit: exited=%t err=%v", exited, err)
	}
	if state, err := processState(cmd.Process.Pid); err != nil || state != 'Z' {
		t.Fatalf("unreaped child was not observable as zombie: state=%c err=%v", state, err)
	}
	if err := proc.signal(syscall.SIGTERM); !errors.Is(err, errProcessExited) {
		t.Fatalf("zombie signal was not refused: %v", err)
	}
	_ = cmd.Wait()
	if err := proc.signal(syscall.SIGKILL); !errors.Is(err, errProcessExited) {
		t.Fatalf("reaped/exited signal was not refused: %v", err)
	}
}

// The top-level case is intentionally serial: it owns real Zellij sockets and
// long-lived packaged controller/helper process trees.
func TestHermeticTwoNodePackagedSubprocessLifecycle(t *testing.T) {
	redeem, zellij := requiredBinary(t, "REDEEM_BIN"), requiredBinary(t, "ZELLIJ_BIN")
	if out, err := exec.Command(zellij, "--version").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "zellij 0.44.3" {
		t.Fatalf("pinned Zellij unavailable: %v %q", err, out)
	}
	root, err := os.MkdirTemp("/tmp", "trh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Release every possible marker gate before bounded TERM/KILL cleanup.
		// The exact owner/start-time registry is checked before fixture deletion.
		cleanupOwnedFixture(t, root, filepath.Join(root, "ledger.jsonl"), filepath.Join(root, "faults"))
		if t.Failed() {
			t.Logf("retained subprocess fixture after owned-process cleanup: %s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	if len(root) > 35 {
		t.Fatalf("temporary root too long for Unix/Zellij sockets: %s", root)
	}
	bin := filepath.Join(root, "b")
	mustMkdir(t, bin)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A race-instrumented Go binary is not a reliable PTY/session helper after
	// setsid. Race validation may supply the exact same test helper built without
	// instrumentation while the coordinator/Niri server remains under -race.
	if helper := os.Getenv("HARNESS_HELPER_BIN"); helper != "" {
		if !filepath.IsAbs(helper) {
			t.Fatalf("HARNESS_HELPER_BIN must be absolute: %q", helper)
		}
		self, err = filepath.EvalSymlinks(helper)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"niri", "ssh", "kitty", "systemctl"} {
		copyExecutable(t, self, filepath.Join(bin, name))
	}
	host := newNode(t, root, "h")
	leech := newNode(t, root, "l")
	hostNiri, err := newNiriServer(filepath.Join(host.runtime, "n"), os.Getpid(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer hostNiri.close()
	leechNiri, err := newNiriServer(filepath.Join(leech.runtime, "n"), os.Getpid(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer leechNiri.close()
	ledger, faults := filepath.Join(root, "ledger.jsonl"), filepath.Join(root, "faults")
	mustMkdir(t, faults)
	topo := topology{Redeem: redeem, Zellij: zellij, HostConfig: host.config, HostState: host.state, Ledger: ledger, FaultDir: faults, OwnerID: root, HostEnv: nodeEnv(host, hostNiri.path, filepath.Join(host.runtime, "z"))}
	writeJSON(t, filepath.Join(bin, topologyName), topo)
	writeConfig(t, host.config, host.state, redeem, zellij, filepath.Join(bin, "kitty"), filepath.Join(bin, "niri"), filepath.Join(bin, "systemctl"), "", false)
	writeConfig(t, leech.config, leech.state, redeem, zellij, filepath.Join(bin, "kitty"), filepath.Join(bin, "niri"), filepath.Join(bin, "systemctl"), filepath.Join(bin, "ssh"), true)
	hostEnv := topo.HostEnv
	leechEnv := nodeEnv(leech, leechNiri.path, "")
	// Zellij 0.44.3 exits non-zero for an empty list. Keep one real, headless
	// seed session so the production catalog boundary can make a complete
	// observation without inventing a fake Zellij result.
	zellijCreate := exec.Command(zellij, "attach", "--create-background", "harness-seed")
	zellijCreate.Env = hostEnv
	zellijCreate.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if output, createErr := zellijCreate.CombinedOutput(); createErr != nil {
		t.Fatalf("create real seed Zellij session: %v %s", createErr, output)
	}
	defer cleanupZellijProcesses(t, root, host.runtime)
	waitZellijControlReady(t, zellij, hostEnv, "harness-seed")

	runOK(t, redeem, host.config, hostEnv, "slice", "inventory", "init", "--state-dir", host.state)
	runFailContains(t, redeem, host.config, hostEnv, "already initialized", "slice", "inventory", "init", "--state-dir", host.state)
	snap := runOK(t, redeem, host.config, hostEnv, "slice", "inventory", "snapshot", "--state-dir", host.state, "--niri-socket", hostNiri.path, "--niri-command", filepath.Join(bin, "niri"), "--zellij-command", zellij, "--zellij-socket-dir", filepath.Join(host.runtime, "z"), "--zellij-cache-home", host.cache, "--timeout", "10s", "--accept-schema-version", "2")
	var initialEnvelope struct {
		Observation struct {
			Quality string `json:"quality"`
		} `json:"observation"`
	}
	if json.Unmarshal(snap, &initialEnvelope) != nil || initialEnvelope.Observation.Quality != "complete" {
		t.Fatalf("initial inventory not complete: %s", snap)
	}

	// The fake SSH executes the real host RPC and preserves the JSON line.
	liveReq := append([]byte(`{"schema_version":1,"accept_schema_versions":[1],"request_id":"h-live","verb":"liveness","payload":{}}`), '\n')
	sshArgs := []string{"-T", "-o", "ServerAliveInterval=1", "-o", "ServerAliveCountMax=1", "--", "host.test", redeem, "slice", "rpc"}
	out := runRawOK(t, filepath.Join(bin, "ssh"), sshArgs, leechEnv, liveReq)
	if !bytes.Contains(out, []byte(`"alive":true`)) {
		t.Fatalf("RPC liveness did not traverse packaged host: %s", out)
	}

	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "init", "--state-dir", leech.state, "--host-id", "host", "--leech-id", "leech")
	runFailContains(t, redeem, leech.config, leechEnv, "already initialized", "slice", "controller", "init", "--state-dir", leech.state, "--host-id", "host", "--leech-id", "leech")
	controller := startController(t, redeem, leech.config, leechEnv, leech.state)
	defer stopProcess(controller)
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "status", "--state-dir", leech.state)
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "workspace-add", "--state-dir", leech.state, "--workspace", "Work")
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "workspace-remove", "--state-dir", leech.state, "--workspace", "Work")

	// Mode-off and unselected launches cross the real CLI/local executable seam.
	runOK(t, redeem, leech.config, leechEnv, "slice", "mode", "disable", "--state-dir", leech.state)
	runOK(t, redeem, leech.config, leechEnv, "slice", "launch", "--state-dir", leech.state)
	runOK(t, redeem, leech.config, leechEnv, "slice", "mode", "enable", "--state-dir", leech.state)
	runOK(t, redeem, leech.config, leechEnv, "slice", "launch", "--state-dir", leech.state)
	waitInvocations(t, ledger, 2, "kitty", func(v invocation) bool { return len(v.Argv) == 1 })
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "workspace-add", "--state-dir", leech.state, "--workspace", "Work")

	// Stabilize the real controller and Zellij control boundary before arming
	// ambiguity. Response loss is armed before the selected packaged launch;
	// that same invocation must replay its token through router/transport/handoff.
	leechNiri.holdRegistrations(true)
	mustWrite(t, filepath.Join(faults, "gate-next-attach"), []byte("armed"))
	waitZellijControlReady(t, zellij, hostEnv, "harness-seed")
	if _, err := tryControllerState(t, redeem, leech.config, leechEnv, leech.state); err != nil {
		t.Fatalf("controller lost control availability before selected route: %v", err)
	}
	mustWrite(t, filepath.Join(faults, "drop-next-launch"), []byte("armed"))
	routed, routedErr := runCLI(t, redeem, leech.config, leechEnv, "slice", "launch", "--state-dir", leech.state)
	var routedResult struct {
		Intent *struct {
			Token       string `json:"token"`
			SessionName string `json:"session_name"`
			Status      string `json:"status"`
			Attempt     int    `json:"attempt"`
		} `json:"intent"`
		Code string `json:"code"`
	}
	if err := decodeFirstJSONLine(routed, &routedResult); err != nil || routedResult.Intent == nil {
		dumpHarnessEvidence(t, root, ledger, hostNiri, leechNiri)
		t.Fatalf("decode selected routed launch: %v %s", err, routed)
	}
	if routedErr != nil || routedResult.Code != "launched" || routedResult.Intent.Status != "launched" || routedResult.Intent.Attempt < 2 {
		dumpHarnessEvidence(t, root, ledger, hostNiri, leechNiri)
		t.Fatalf("packaged lost-response recovery failed: %v %s", routedErr, routed)
	}
	tokenFiles, _ := filepath.Glob(filepath.Join(host.state, "slice", "rpc-tokens", "*.json"))
	if len(tokenFiles) != 1 {
		dumpHarnessEvidence(t, root, ledger, hostNiri, leechNiri)
		t.Fatalf("expected one routed host token journal, got %v", tokenFiles)
	}
	journal, err := os.ReadFile(tokenFiles[0])
	if err != nil || !bytes.Contains(journal, []byte(`"status":"launched"`)) || !bytes.Contains(journal, []byte(`"stage":"committed"`)) {
		dumpHarnessEvidence(t, root, ledger, hostNiri, leechNiri)
		t.Fatalf("routed host journal did not commit: %v %s", err, journal)
	}
	routeToken := routedResult.Intent.Token
	launchedHandoff := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		h, ok := state.LaunchHandoffs[routeToken]
		return ok && h.Status == "launched" && h.HostTerminalID != "" && h.SourceID != ""
	}, "lost-response handoff advances from launch_pending to launched")
	all := readInvocations(t, ledger)
	handoffAudit := 0
	for _, entry := range launchedHandoff.Audit {
		if entry.Kind == "launch_handoff" && entry.Detail == routeToken {
			handoffAudit++
		}
	}
	if countInv(all, "ssh", func(v invocation) bool { return v.Verb == "launch" && v.Token == routeToken && v.Dropped }) != 1 || countInv(all, "ssh", func(v invocation) bool { return v.Verb == "token_replay" && v.Token == routeToken }) < 1 || countInv(all, "kitty", func(v invocation) bool { return contains(v.Argv, "slice") && contains(v.Argv, "host-attach") }) != 1 || launchedHandoff.LaunchHandoffs[routeToken].Status != "launched" || handoffAudit < 2 {
		dumpHarnessEvidence(t, root, ledger, hostNiri, leechNiri)
		t.Fatal("lost response did not traverse packaged router/replay/handoff exactly once")
	}
	mustWrite(t, filepath.Join(faults, "release-host-attach"), []byte("release"))
	waitInvocations(t, ledger, 1, "ssh", func(v invocation) bool { return v.Verb == "attach" })
	waitInvocations(t, ledger, 1, "attach-ready", func(v invocation) bool { return v.Verb == "attach" })
	waitRegistrationCount(t, leechNiri, 1)
	launching := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		for id, projection := range state.Projections {
			return id != "" && projection.Status == slicecontroller.ProjectionLaunching && projection.ExpectedPID > 0 && projection.NiriWindowID == 0
		}
		return false
	}, "delayed projection is durably launching before Niri publication")
	var sourceID string
	for id := range launching.Projections {
		sourceID = id
	}
	leechNiri.holdRegistrations(false)
	connected := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		projection, ok := state.Projections[sourceID]
		return ok && projection.Status == slicecontroller.ProjectionOwned && projection.NiriWindowID != 0 && state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "published projection is owned and exact attachment is connected")
	hostKittyPID, leechKittyPID := 0, connected.Projections[sourceID].ExpectedPID
	for _, v := range readInvocations(t, ledger) {
		if v.Kind == "kitty" && contains(v.Argv, "host-attach") {
			hostKittyPID = v.PID
		}
	}
	if hostKittyPID == 0 || leechKittyPID == 0 || !processTreeContains(hostKittyPID, "zellij") || !processTreeContains(leechKittyPID, "zellij") {
		t.Fatalf("host and leech were not concurrently attached: host=%d leech=%d", hostKittyPID, leechKittyPID)
	}

	// Diverge the positively owned projection in every supported host-location
	// property plus diagnostics-only order, then require convergence through the
	// packaged controller and Niri socket boundaries.
	spatialWindowID, ok := leechNiri.divergeControlledSpatial()
	if !ok || spatialWindowID != connected.Projections[sourceID].NiriWindowID {
		t.Fatalf("could not diverge exact owned projection: id=%d mapping=%#v", spatialWindowID, connected.Projections[sourceID])
	}
	spatialActionsAt := len(leechNiri.actionLines())
	spatialRequestsAt := len(leechNiri.requestLines())
	hostSpatialActionsAt := len(hostNiri.actionLines())
	spatiallyConverged := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		window, present := leechNiri.controlledSpatial(spatialWindowID)
		record := state.Spatial[sourceID]
		return present && window.WorkspaceID != nil && *window.WorkspaceID == 1 && !window.IsFloating && len(window.Layout.Position) == 2 && window.Layout.Position[0] == 7 && window.Layout.Position[1] == 3 && len(window.Layout.WindowSize) == 2 && window.Layout.WindowSize[0] == 960 && window.Layout.WindowSize[1] == 540 && len(record.OrderDrift) == 1 && record.OrderDrift[0].Observed != nil && record.OrderDrift[0].Observed.Column == 7 && record.OrderDrift[0].Observed.Tile == 3 && record.LastApplied == nil && record.Recovery == nil
	}, "workspace/mode/width/height convergence with report-only order drift")
	spatialActions := leechNiri.actionLines()[spatialActionsAt:]
	assertSpatialActions(t, spatialActions, spatialWindowID)
	assertSpatialSocketSequence(t, leechNiri.requestLines()[spatialRequestsAt:], spatialWindowID)
	if drift := spatiallyConverged.Spatial[sourceID].OrderDrift; len(drift) != 1 || drift[0].SourceID != sourceID || drift[0].Expected == nil || drift[0].Expected.Column != 2 || drift[0].Expected.Tile != 1 || drift[0].Observed == nil || drift[0].Observed.Column != 7 || drift[0].Observed.Tile != 3 {
		t.Fatalf("tile order drift was not retained as report-only evidence: %#v", drift)
	}

	// One exact action failure must leave bounded degraded retry evidence, then
	// converge from host authority without any host-side spatial writeback.
	if _, ok := leechNiri.divergeControlledSpatial(); !ok {
		t.Fatal("could not prepare injected spatial failure")
	}
	failedSpatialActionsAt := len(leechNiri.actionLines())
	failedBefore := leechNiri.failedActionCount()
	leechNiri.injectActionFailure("MoveWindowToWorkspace")
	waitNiriFailure(t, leechNiri, failedBefore+1)
	degraded := waitControllerStateFast(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		record := state.Spatial[sourceID]
		if record.Recovery != nil {
			return record.Recovery.Attempt == 1 && !record.Recovery.Stable && record.Conflict == "spatial_execution_failed" && state.AuthorityMode == "host_location"
		}
		// Under -race the one-attempt degraded interval can complete between
		// packaged status calls; its durable audit plus the exact injected action
		// count still proves the monotonic failure/retry transition.
		for _, entry := range state.Audit {
			if entry.Kind == "spatial_execution_failed" && entry.Detail == sourceID && leechNiri.failedActionCount() >= failedBefore+1 {
				return state.AuthorityMode == "host_location"
			}
		}
		return false
	}, "bounded spatial retry evidence")
	if degraded.Spatial[sourceID].LastApplied != nil {
		t.Fatalf("failed spatial origin remained pending: %#v", degraded.Spatial[sourceID])
	}
	recovered := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		window, present := leechNiri.controlledSpatial(spatialWindowID)
		return present && window.WorkspaceID != nil && *window.WorkspaceID == 1 && !window.IsFloating && window.Layout.WindowSize[0] == 960 && window.Layout.WindowSize[1] == 540 && state.Spatial[sourceID].Recovery == nil && state.Spatial[sourceID].Conflict == ""
	}, "host-location convergence after one bounded retry")
	failedSpatialActions := leechNiri.actionLines()[failedSpatialActionsAt:]
	if len(failedSpatialActions) != 5 || !strings.Contains(failedSpatialActions[0], `"MoveWindowToWorkspace"`) || !strings.Contains(failedSpatialActions[0], fmt.Sprintf(`"window_id":%d`, spatialWindowID)) || !strings.Contains(failedSpatialActions[0], `"focus":false`) {
		t.Fatalf("injected spatial failure was not followed by one bounded exact retry: %v", failedSpatialActions)
	}
	assertSpatialActions(t, failedSpatialActions[1:], spatialWindowID)
	if len(hostNiri.actionLines()) != hostSpatialActionsAt || recovered.Sources[sourceID].Connection != slicecontroller.ConnectionConnected || !processTreeContains(hostKittyPID, "zellij") || !processTreeContains(leechKittyPID, "zellij") || !hostNiri.sentinelPresent() || !leechNiri.sentinelPresent() {
		t.Fatal("spatial failure/retry wrote back to host or harmed session/sentinel state")
	}
	retryAttempt := 1
	if degraded.Spatial[sourceID].Recovery != nil {
		retryAttempt = degraded.Spatial[sourceID].Recovery.Attempt
	}
	t.Logf("spatial subprocess evidence: exact_window_id=%d actions=[workspace(focus=false),tiling,width,height] verify_after_each=true order=2,1->7,3(report-only) injected_failures=%d retry_attempt=%d host_action_delta=%d sentinels=true sessions_connected=true", spatialWindowID, leechNiri.failedActionCount()-failedBefore, retryAttempt, len(hostNiri.actionLines())-hostSpatialActionsAt)

	// Cancel only after the packaged host RPC process has started. The direct
	// command and its transport are distinct explicit owned groups; the shim's
	// parent-death handler aborts and reaps the gated host child before exit.
	mustWrite(t, filepath.Join(faults, "gate-host-next-launch"), []byte("armed"))
	cmd := exec.Command(redeem, "--config", leech.config, "slice", "launch", "--state-dir", leech.state)
	cmd.Env = leechEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var cancelBuffer bytes.Buffer
	cmd.Stdout, cmd.Stderr = &cancelBuffer, &cancelBuffer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- cmd.Wait() }()
	started := waitInvocations(t, ledger, 1, "host-rpc-started", func(v invocation) bool { return v.Verb == "launch" })
	var cancelHost invocation
	for _, v := range started {
		if v.Kind == "host-rpc-started" && v.Verb == "launch" && v.Token != routeToken {
			cancelHost = v
		}
	}
	if cancelHost.Token == "" {
		t.Fatal("cancelled route did not expose an owned host child")
	}
	mustWrite(t, filepath.Join(faults, "abort-host-launch-"+cancelHost.Token), []byte("abort"))
	cancelProc, pinErr := pinOwnedProcess(cmd.Process.Pid, processIdentity{}, root)
	if pinErr != nil {
		t.Fatalf("pin cancelled route before signaling: %v", pinErr)
	}
	defer cancelProc.close()
	if err := cancelProc.signal(syscall.SIGTERM); err != nil {
		t.Fatalf("pidfd TERM cancelled route: %v", err)
	}
	select {
	case <-cancelDone:
	case <-time.After(3 * time.Second):
		_ = cancelProc.signal(syscall.SIGKILL)
		<-cancelDone
	}
	cancelOut := cancelBuffer.Bytes()
	var cancelSSH invocation
	for _, v := range readInvocations(t, ledger) {
		if v.Kind == "ssh" && v.Verb == "launch" && v.Token == cancelHost.Token {
			cancelSSH = v
		}
	}
	assertOwnedIdentitiesGone(t, root, []processIdentity{{PID: cancelHost.PID, StartTime: cancelHost.StartTime}, {PID: cancelSSH.PID, StartTime: cancelSSH.StartTime}}, "cancelled SSH/host RPC tree")
	cancelled := readInvocations(t, ledger)
	if countInv(cancelled, "kitty", func(v invocation) bool { return len(v.Argv) == 1 }) != 2 || countInv(cancelled, "kitty", func(v invocation) bool { return contains(v.Argv, "host-attach") }) != 1 {
		t.Fatalf("mid-host cancellation mutated or fell back locally: output=%s ledger=%#v", cancelOut, cancelled)
	}
	intentFiles, _ := filepath.Glob(filepath.Join(leech.state, "slice", "launch", "intents", "*.json"))
	if len(intentFiles) != 2 {
		t.Fatalf("expected committed and cancelled routed intents, got %v", intentFiles)
	}

	// Global selection crosses the packaged control boundary, composes with the
	// existing workspace reason, and survives controller restart without a
	// duplicate projection. Re-add the workspace before disabling global
	// selection so the same exact projection remains wanted throughout.
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "all-enable", "--state-dir", leech.state)
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "workspace-remove", "--state-dir", leech.state, "--workspace", "Work")
	allSelected := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		projection, projected := state.Projections[sourceID]
		return state.AllEligible && state.SelectedWorkspaces["work"] == "" && projected && projection.Status == slicecontroller.ProjectionOwned && state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "all-eligible remains the sole desired reason without duplicating the projection")
	allPID := allSelected.Projections[sourceID].ExpectedPID
	stopProcess(controller)
	controller = startController(t, redeem, leech.config, leechEnv, leech.state)
	allRestarted := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		projection, projected := state.Projections[sourceID]
		return state.AllEligible && projected && projection.ExpectedPID == allPID && projection.Status == slicecontroller.ProjectionOwned && state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "all-eligible and one exact projection survive controller restart")
	if len(allRestarted.Projections) != 1 || !processTreeContains(hostKittyPID, "zellij") {
		t.Fatalf("all-eligible restart duplicated projection or harmed host work: projections=%#v", allRestarted.Projections)
	}
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "workspace-add", "--state-dir", leech.state, "--workspace", "Work")
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "all-disable", "--state-dir", leech.state)
	allDisabled := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		projection, projected := state.Projections[sourceID]
		return !state.AllEligible && state.SelectedWorkspaces["work"] == "Work" && projected && projection.ExpectedPID == allPID && state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "workspace selection preserves one exact projection after all-disable")
	if len(allDisabled.Projections) != 1 {
		t.Fatalf("additive all-disable changed exact projection cardinality: %#v", allDisabled.Projections)
	}

	// The controller lock is a runtime singleton, not merely an init guard.
	runFailContains(t, redeem, leech.config, leechEnv, "slice controller is already running", "slice", "controller", "run", "--state-dir", leech.state)

	// Close is an owned local close, persists across process restart, and never
	// kills the host attachment. Neither launch-token reconnect nor controller
	// reconnect implicitly reopens a user-closed session.
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "close", "--state-dir", leech.state, "--source-id", sourceID)
	closed := waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		_, dropped := state.ClosedByUser[state.Sources[sourceID].SessionID]
		_, projected := state.Projections[sourceID]
		return dropped && !projected
	}, "close persisted and owned projection removed")
	if !processTreeContains(hostKittyPID, "zellij") {
		t.Fatal("controller close killed host Zellij attachment")
	}
	stopProcess(controller)
	controller = startController(t, redeem, leech.config, leechEnv, leech.state)
	closed = controllerState(t, redeem, leech.config, leechEnv, leech.state)
	if _, ok := closed.ClosedByUser[closed.Sources[sourceID].SessionID]; !ok || closed.SelectedWorkspaces["work"] != "Work" {
		t.Fatalf("close/selection did not survive controller restart: %#v", closed)
	}
	runFailContains(t, redeem, leech.config, leechEnv, `"code":"request_failed"`, "slice", "controller", "reconnect", "--state-dir", leech.state, "--source-id", sourceID)
	if state := controllerState(t, redeem, leech.config, leechEnv, leech.state); len(state.Projections) != 0 {
		t.Fatalf("rejected reconnect reopened closed projection: %#v", state.Projections)
	}

	// Explicit reopen creates a new connected projection; undo restores the
	// close. Drop uses the same durable session tombstone and is also restarted.
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "reopen", "--state-dir", leech.state, "--source-id", sourceID)
	waitRegistrationCount(t, leechNiri, 2)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		p, ok := state.Projections[sourceID]
		return ok && p.Status == slicecontroller.ProjectionOwned && state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "explicit reopen connected")
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "undo", "--state-dir", leech.state)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		_, dropped := state.ClosedByUser[state.Sources[sourceID].SessionID]
		_, projected := state.Projections[sourceID]
		return dropped && !projected
	}, "undo reopen restored close")
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "reopen", "--state-dir", leech.state, "--source-id", sourceID)
	waitRegistrationCount(t, leechNiri, 3)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		return state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "second reopen connected")
	_, _ = runCLI(t, redeem, leech.config, leechEnv, "slice", "controller", "drop", "--state-dir", leech.state, "--source-id", sourceID)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		_, dropped := state.ClosedByUser[state.Sources[sourceID].SessionID]
		_, projected := state.Projections[sourceID]
		return dropped && !projected
	}, "drop persisted")
	stopProcess(controller)
	controller = startController(t, redeem, leech.config, leechEnv, leech.state)
	if state := controllerState(t, redeem, leech.config, leechEnv, leech.state); len(state.ClosedByUser) != 1 || len(state.Projections) != 0 {
		t.Fatalf("drop did not survive restart: %#v", state)
	}
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "reopen", "--state-dir", leech.state, "--source-id", sourceID)
	waitRegistrationCount(t, leechNiri, 4)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		return state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "post-drop reopen connected")

	// A focused owned projection closes through the packaged command. Reusing
	// that runtime ID with a changed PID/app fails closed and emits no action.
	leechNiri.setControlledFocus(true)
	actionsBeforeReuse := len(leechNiri.actionLines())
	restoreIdentity := leechNiri.replaceControlledIdentity(os.Getpid(), "reused.unrelated")
	runFailContains(t, redeem, leech.config, leechEnv, "focused window is not a positively owned", "slice", "close-focused", "--state-dir", leech.state)
	if got := len(leechNiri.actionLines()); got != actionsBeforeReuse {
		t.Fatalf("reused runtime window ID caused mutation: before=%d after=%d", actionsBeforeReuse, got)
	}
	restoreIdentity()
	leechNiri.setControlledFocus(true)
	runOK(t, redeem, leech.config, leechEnv, "slice", "close-focused", "--state-dir", leech.state)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		_, dropped := state.ClosedByUser[state.Sources[sourceID].SessionID]
		return dropped
	}, "packaged close-focused")
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "reopen", "--state-dir", leech.state, "--source-id", sourceID)
	waitRegistrationCount(t, leechNiri, 5)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		return state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "reopen after close-focused")

	// Lose an already-ready attachment, fail its sole automatic retry, then
	// require the explicit reconnect command to create the next attachment.
	mustWrite(t, filepath.Join(faults, "fail-attach-held"), []byte("armed"))
	attachments := readInvocations(t, ledger)
	var lastAttach invocation
	var attachProc *pinnedProcess
	for i := len(attachments) - 1; i >= 0; i-- {
		candidate := attachments[i]
		if candidate.Kind != "ssh" || candidate.Verb != "attach" {
			continue
		}
		identity := processIdentity{PID: candidate.PID, StartTime: candidate.StartTime}
		if pinned, pinErr := pinOwnedProcess(candidate.PID, identity, root); pinErr == nil {
			lastAttach, attachProc = candidate, pinned
			break
		}
	}
	if attachProc == nil {
		t.Fatal("could not pin a live exact owned projection attachment")
	}
	if err := attachProc.signal(syscall.SIGTERM); err != nil {
		attachProc.close()
		t.Fatalf("could not pidfd-terminate exact owned projection attachment pid=%d: %v", lastAttach.PID, err)
	}
	_, _ = attachProc.wait(3 * time.Second)
	attachProc.close()
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		source := state.Sources[sourceID]
		_, projected := state.Projections[sourceID]
		return source.Connection == slicecontroller.ConnectionDisconnected && source.Recovery == nil && !projected
	}, "automatic attachment recovery exhausted")
	if err := os.Remove(filepath.Join(faults, "fail-attach-held")); err != nil {
		t.Fatal(err)
	}
	runOK(t, redeem, leech.config, leechEnv, "slice", "controller", "reconnect", "--state-dir", leech.state, "--source-id", sourceID)
	waitRegistrationCount(t, leechNiri, 7)
	waitControllerState(t, redeem, leech.config, leechEnv, leech.state, func(state slicecontroller.State) bool {
		projection, ok := state.Projections[sourceID]
		return ok && projection.Status == slicecontroller.ProjectionOwned && state.Sources[sourceID].Connection == slicecontroller.ConnectionConnected
	}, "explicit reconnect after exhausted attachment")

	// Disable launch routing and stop the controller without rolling back host
	// Zellij, durable pending/committed tokens, unrelated windows, or direct
	// packaged attach availability.
	tokenBeforeDisable := append([]byte(nil), journal...)
	runOK(t, redeem, leech.config, leechEnv, "slice", "mode", "disable", "--state-dir", leech.state)
	stopProcess(controller)
	controller = nil
	journalAfterDisable, _ := os.ReadFile(tokenFiles[0])
	pendingIntents, committedIntents := 0, 0
	for _, path := range intentFiles {
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(payload, []byte(`"status":"pending"`)) {
			pendingIntents++
		}
		if bytes.Contains(payload, []byte(`"status":"launched"`)) {
			committedIntents++
		}
	}
	if !bytes.Equal(tokenBeforeDisable, journalAfterDisable) || pendingIntents != 1 || committedIntents != 1 || !processTreeContains(hostKittyPID, "zellij") {
		t.Fatal("disablement changed pending/committed token state or host Zellij")
	}
	legacy := exec.Command(filepath.Join(bin, "kitty"), "-e", redeem, "--config", host.config, "slice", "attach", "--session", "harness-seed", "--token", "legacy_attach_token")
	legacy.Env = hostEnv
	legacy.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := legacy.Start(); err != nil {
		t.Fatal(err)
	}
	defer stopProcess(legacy)
	deadline := time.Now().Add(10 * time.Second)
	for !processTreeContains(legacy.Process.Pid, "zellij") {
		if time.Now().After(deadline) {
			t.Fatal("legacy packaged attach unavailable after disablement")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopProcess(legacy)

	// A restarted owned socket rotates the host epoch only after a new complete snapshot.
	var first struct {
		Authoritative *struct {
			SourceEpoch string `json:"source_epoch"`
		} `json:"authoritative"`
	}
	_ = json.Unmarshal(snap, &first)
	hostNiri.clearControlledWindows()
	if err := hostNiri.restart(); err != nil {
		t.Fatal(err)
	}
	snap2 := runOK(t, redeem, host.config, hostEnv, "slice", "inventory", "snapshot", "--state-dir", host.state, "--niri-socket", hostNiri.path, "--niri-command", filepath.Join(bin, "niri"), "--zellij-command", zellij, "--zellij-socket-dir", filepath.Join(host.runtime, "z"), "--zellij-cache-home", host.cache, "--timeout", "10s", "--accept-schema-version", "2")
	var second struct {
		Authoritative *struct {
			SourceEpoch string `json:"source_epoch"`
		} `json:"authoritative"`
	}
	_ = json.Unmarshal(snap2, &second)
	if first.Authoritative == nil || second.Authoritative == nil || first.Authoritative.SourceEpoch == second.Authoritative.SourceEpoch {
		t.Fatalf("Niri restart did not rotate public epoch: %s / %s", snap, snap2)
	}
	if !hostNiri.sentinelPresent() || !leechNiri.sentinelPresent() {
		t.Fatal("unrelated sentinel window was removed")
	}
	for _, line := range append(hostNiri.actionLines(), leechNiri.actionLines()...) {
		if strings.Contains(line, `"id":90`) || strings.Contains(line, `"window_id":90`) {
			t.Fatalf("unrelated sentinel was mutated: %s", line)
		}
	}

	assertStateUnder(t, root, host, leech)
	assertNoCredentialEnv(t, readInvocations(t, ledger))
}

type node struct{ root, home, config, state, runtime, cache string }

func newNode(t *testing.T, root, name string) node {
	n := node{root: filepath.Join(root, name)}
	n.home = filepath.Join(n.root, "home")
	n.config = filepath.Join(n.root, "config.yaml")
	n.state = filepath.Join(n.root, "state")
	n.runtime = filepath.Join(n.root, "run")
	n.cache = filepath.Join(n.root, "cache")
	for _, p := range []string{n.root, n.home, n.state, n.runtime, n.cache, filepath.Join(n.runtime, "z"), filepath.Join(n.runtime, "z", zellijlive.SocketContractDir)} {
		mustMkdir(t, p)
	}
	return n
}
func nodeEnv(n node, niri, zellijSockets string) []string {
	ownerID := filepath.Dir(n.root)
	out := []string{"HOME=" + n.home, "XDG_CONFIG_HOME=" + filepath.Join(n.root, "config"), "XDG_STATE_HOME=" + n.state, "XDG_RUNTIME_DIR=" + n.runtime, "XDG_CACHE_HOME=" + n.cache, "NIRI_SOCKET=" + niri, "WAYLAND_DISPLAY=wayland-harness", "PATH=/nonexistent", "LANG=C.UTF-8", "TERMINAL_REDEEMER_HARNESS_OWNER=" + ownerID}
	if zellijSockets != "" {
		out = append(out, "ZELLIJ_SOCKET_DIR="+zellijSockets)
	}
	return out
}
func writeConfig(t *testing.T, path, state, redeem, zellij, kitty, niri, systemctl, ssh string, controller bool) {
	if ssh == "" {
		ssh = filepath.Join(filepath.Dir(niri), "ssh")
	}
	content := fmt.Sprintf("stateDir: %q\nslice:\n  leechModeEnabled: false\n  sourceHost: %q\n  selfCommand: %q\n  kittyCommand: %q\n  transportCommand: %q\n  rpcCommand: [%q, slice, rpc]\n  zellijCommand: %q\n  niriCommand: %q\n  systemctlCommand: %q\n  attachPrivateRoot: %q\n  attachShimCache: %q\n  expectedNiriVersion: %q\n  requestTimeout: 90s\n  keepaliveInterval: 1s\n  keepaliveCount: 1\n  retryMaxAttempts: 2\n  retryInitialBackoff: 2s\n  retryMaxBackoff: 2s\n  graphicalContextKeys: [NIRI_SOCKET, WAYLAND_DISPLAY, XDG_RUNTIME_DIR]\n  clipboard: {enabled: false}\n  controller:\n    enabled: %t\n    hostID: host\n    leechID: leech\n    pollInterval: 500ms\n    controlTimeout: 10s\n    retryWindow: 15s\n    sourceGoneGrace: 300ms\n    sourceGoneConfirmations: 2\n    authorityMode: host_location\n    leechWriteAuthorized: false\n", state, map[bool]string{true: "host.test", false: ""}[controller], redeem, kitty, ssh, redeem, zellij, niri, systemctl, filepath.Join(filepath.Dir(state), "a"), filepath.Join(filepath.Dir(state), "c"), niriipc.SupportedVersion, controller)
	mustWrite(t, path, []byte(content))
}
func requiredBinary(t *testing.T, key string) string {
	t.Helper()
	p := os.Getenv(key)
	if p == "" {
		t.Skipf("%s must name the absolute packaged acceptance dependency", key)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("%s must be absolute: %q", key, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(resolved)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("%s not executable: %v", key, err)
	}
	return resolved
}
func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o700); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
func writeJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, p, b)
}
func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
}
func runCLI(t *testing.T, bin, cfg string, env []string, args ...string) ([]byte, error) {
	t.Helper()
	all := append([]string{"--config", cfg}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, all...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		// Go 1.24's Linux os.Process uses a pidfd-backed handle for the context
		// cancellation signal. Descendants are subsequently covered by the exact
		// owner/root pidfd cleanup rather than a numeric process-group signal.
		return out, fmt.Errorf("subprocess deadline: %w", ctx.Err())
	}
	return out, err
}
func runOK(t *testing.T, bin, cfg string, env []string, args ...string) []byte {
	t.Helper()
	out, err := runCLI(t, bin, cfg, env, args...)
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", bin, args, err, out)
	}
	return out
}
func runFailContains(t *testing.T, bin, cfg string, env []string, want string, args ...string) {
	t.Helper()
	out, err := runCLI(t, bin, cfg, env, args...)
	if err == nil || !bytes.Contains(out, []byte(want)) {
		t.Fatalf("expected failure containing %q: %v %s", want, err, out)
	}
}
func decodeFirstJSONLine(payload []byte, dst any) error {
	line := payload
	if i := bytes.IndexByte(payload, '\n'); i >= 0 {
		line = payload[:i]
	}
	return json.Unmarshal(line, dst)
}

func tryControllerState(t *testing.T, bin, cfg string, env []string, stateDir string) (slicecontroller.State, error) {
	t.Helper()
	payload, err := runCLI(t, bin, cfg, env, "slice", "controller", "status", "--state-dir", stateDir)
	if err != nil {
		return slicecontroller.State{}, fmt.Errorf("controller status: %w: %s", err, payload)
	}
	var response slicecontroller.ControlResponse
	if err := decodeFirstJSONLine(payload, &response); err != nil || response.State == nil {
		return slicecontroller.State{}, fmt.Errorf("decode controller status: %v: %s", err, payload)
	}
	return *response.State, nil
}
func controllerState(t *testing.T, bin, cfg string, env []string, stateDir string) slicecontroller.State {
	t.Helper()
	state, err := tryControllerState(t, bin, cfg, env, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func waitControllerState(t *testing.T, bin, cfg string, env []string, stateDir string, predicate func(slicecontroller.State) bool, detail string) slicecontroller.State {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last slicecontroller.State
	var lastErr error
	for {
		if current, err := tryControllerState(t, bin, cfg, env, stateDir); err == nil {
			last, lastErr = current, nil
			if predicate(last) {
				return last
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for controller state %s: %#v (last status error: %v)", detail, last, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
func waitRegistrationCount(t *testing.T, server *niriServer, want int) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for server.registrationCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d Niri registrations (got %d)", want, server.registrationCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitNiriFailure(t *testing.T, server *niriServer, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for server.failedActionCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for injected Niri action failure (got %d, want %d)", server.failedActionCount(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitControllerStateFast(t *testing.T, bin, cfg string, env []string, stateDir string, predicate func(slicecontroller.State) bool, detail string) slicecontroller.State {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last slicecontroller.State
	var lastErr error
	for {
		if current, err := tryControllerState(t, bin, cfg, env, stateDir); err == nil {
			last, lastErr = current, nil
			if predicate(last) {
				return last
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for controller state %s: %#v (last status error: %v)", detail, last, lastErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertSpatialActions(t *testing.T, lines []string, windowID uint64) {
	t.Helper()
	if len(lines) != 4 {
		t.Fatalf("spatial action count=%d, want exact workspace/mode/width/height batch: %v", len(lines), lines)
	}
	want := []string{"MoveWindowToWorkspace", "MoveWindowToTiling", "SetWindowWidth", "SetWindowHeight"}
	for i, line := range lines {
		if !strings.Contains(line, `"`+want[i]+`"`) {
			t.Fatalf("spatial action %d=%s, want %s", i, line, want[i])
		}
		if strings.Contains(line, `"focus":true`) || strings.Contains(line, "Focus") || strings.Contains(line, "Column") {
			t.Fatalf("spatial action was focus/order disruptive: %s", line)
		}
		var request struct {
			Action map[string]json.RawMessage `json:"Action"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatalf("decode spatial action: %v: %s", err, line)
		}
		payload := request.Action[want[i]]
		var ids struct {
			ID       uint64 `json:"id"`
			WindowID uint64 `json:"window_id"`
			Focus    bool   `json:"focus"`
		}
		if err := json.Unmarshal(payload, &ids); err != nil {
			t.Fatalf("decode spatial target: %v: %s", err, line)
		}
		id := ids.ID
		if id == 0 {
			id = ids.WindowID
		}
		if id != windowID || ids.Focus {
			t.Fatalf("spatial action did not preserve exact-ID/non-focus invariant: id=%d focus=%t line=%s", id, ids.Focus, line)
		}
	}
}

func assertSpatialSocketSequence(t *testing.T, requests []string, windowID uint64) {
	t.Helper()
	var actions []int
	for i, line := range requests {
		if strings.Contains(line, `"Action"`) {
			actions = append(actions, i)
		}
	}
	if len(actions) != 4 {
		t.Fatalf("spatial socket action count=%d in sequence: %v", len(actions), requests)
	}
	for i, actionAt := range actions {
		end := len(requests)
		if i+1 < len(actions) {
			end = actions[i+1]
		}
		sawReplay, sawOutputs := false, false
		for _, line := range requests[actionAt+1 : end] {
			sawReplay = sawReplay || line == `"EventStream"`
			sawOutputs = sawOutputs || line == `"Outputs"`
		}
		if !sawReplay || !sawOutputs {
			t.Fatalf("spatial action %d for window %d lacked complete verify-after-write socket observation: %v", i, windowID, requests[actionAt:end])
		}
	}
}

func waitFileContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for {
		payload, err := os.ReadFile(path)
		if err == nil && bytes.Contains(payload, []byte(want)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in %s: %v %s", want, path, err, payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runRawOK(t *testing.T, bin string, args, env []string, input []byte) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("raw subprocess failed: %v %s", err, out)
	}
	return out
}
func runRawFail(t *testing.T, bin string, args, env []string, input []byte) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected raw subprocess failure: %s", out)
	}
}
func startController(t *testing.T, bin, cfg string, env []string, state string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin, "--config", cfg, "slice", "controller", "run", "--state-dir", state)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if !strings.Contains(line, "slice_controller_ready") {
			stopProcess(cmd)
			t.Fatalf("controller failed readiness: %q %s", line, stderr.String())
		}
	case <-time.After(20 * time.Second):
		stopProcess(cmd)
		t.Fatalf("controller readiness timeout: %s", stderr.String())
	}
	// The readiness line precedes useful serialized control availability on a
	// sufficiently instrumented/race build. Require an actual packaged status
	// round trip before exposing the controller to the scenario.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, statusErr := tryControllerState(t, bin, cfg, env, state); statusErr == nil {
			break
		} else if time.Now().After(deadline) {
			stopProcess(cmd)
			t.Fatalf("controller control socket did not become available: %v %s", statusErr, stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cmd
}
func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	proc, err := pinOwnedProcess(cmd.Process.Pid, processIdentity{}, ownerIDFromEnv(cmd.Env))
	if err != nil {
		// It may already have exited and been reaped by another path. Never fall
		// back to a numeric-PID signal when the exact owned identity is unavailable.
		return
	}
	defer proc.close()
	_ = proc.signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = proc.signal(syscall.SIGKILL)
		<-done
	}
}
func readInvocations(t *testing.T, path string) []invocation {
	t.Helper()
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []invocation
	for _, line := range bytes.Split(bytes.TrimSpace(payload), []byte{'\n'}) {
		var v invocation
		if len(line) > 0 && json.Unmarshal(line, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}
func countInv(all []invocation, kind string, p func(invocation) bool) int {
	n := 0
	for _, v := range all {
		if v.Kind == kind && p(v) {
			n++
		}
	}
	return n
}
func waitInvocations(t *testing.T, path string, want int, kind string, p func(invocation) bool) []invocation {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		all := readInvocations(t, path)
		if countInv(all, kind, p) == want {
			return all
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d %s invocations, got %#v", want, kind, all)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func contains(all []string, want string) bool {
	for _, v := range all {
		if v == want {
			return true
		}
	}
	return false
}
func assertStateUnder(t *testing.T, root string, nodes ...node) {
	t.Helper()
	for _, n := range nodes {
		for _, p := range []string{n.home, n.config, n.state, n.runtime, n.cache} {
			if !strings.HasPrefix(p, root+string(os.PathSeparator)) {
				t.Fatalf("path escaped harness root: %s", p)
			}
		}
	}
}
func assertNoCredentialEnv(t *testing.T, all []invocation) {
	t.Helper()
	for _, v := range all {
		for _, key := range v.EnvKeys {
			switch key {
			case "SSH_AUTH_SOCK", "SSH_AGENT_PID", "AWS_ACCESS_KEY_ID", "GITHUB_TOKEN", "ZELLIJ", "ZELLIJ_SESSION_NAME":
				t.Fatalf("credential/ambient key reached %s helper: %s", v.Kind, key)
			}
		}
	}
}

type processIdentity struct {
	PID       int
	StartTime uint64
}

func processStartTime(pid int) (uint64, error) {
	payload, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := bytes.LastIndexByte(payload, ')')
	if end < 0 {
		return 0, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(payload[end+1:]))
	if len(fields) <= 19 {
		return 0, errors.New("short proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func processState(pid int) (byte, error) {
	payload, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := bytes.LastIndexByte(payload, ')')
	if end < 0 {
		return 0, errors.New("invalid proc state")
	}
	fields := strings.Fields(string(payload[end+1:]))
	if len(fields) == 0 || len(fields[0]) != 1 {
		return 0, errors.New("invalid proc state")
	}
	return fields[0][0], nil
}

func exactIdentityAlive(id processIdentity) bool {
	if id.PID <= 0 || id.StartTime == 0 {
		return false
	}
	start, err := processStartTime(id.PID)
	return err == nil && start == id.StartTime
}

var (
	errProcessIdentityChanged = errors.New("process identity changed")
	errProcessOwnerChanged    = errors.New("process owner changed")
	errProcessExited          = errors.New("process exited")
)

type procSnapshot struct {
	identity processIdentity
	state    byte
	environ  []byte
	exe      string
}

type pinnedProcess struct {
	fd          int
	identity    processIdentity
	ownerID     string
	requiredEnv []string
}

func readProcSnapshot(pid int) (procSnapshot, error) {
	var snapshot procSnapshot
	payload, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return snapshot, err
	}
	end := bytes.LastIndexByte(payload, ')')
	if end < 0 {
		return snapshot, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(payload[end+1:]))
	if len(fields) <= 19 || len(fields[0]) != 1 {
		return snapshot, errors.New("short proc stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return snapshot, fmt.Errorf("invalid proc start identity: %w", err)
	}
	snapshot.identity = processIdentity{PID: pid, StartTime: start}
	snapshot.state = fields[0][0]
	snapshot.environ, err = os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return procSnapshot{}, err
	}
	snapshot.exe, err = os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return procSnapshot{}, err
	}
	return snapshot, nil
}

func ownerIDFromEnv(env []string) string {
	const prefix = "TERMINAL_REDEEMER_HARNESS_OWNER="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func containsExactEnvironmentEntry(environ []byte, expected string) bool {
	if expected == "" || strings.IndexByte(expected, 0) >= 0 {
		return false
	}
	for len(environ) > 0 {
		end := bytes.IndexByte(environ, 0)
		if end < 0 {
			return false
		}
		if string(environ[:end]) == expected {
			return true
		}
		environ = environ[end+1:]
	}
	return false
}

func validateOwnedSnapshot(snapshot procSnapshot, expected processIdentity, ownerID string, requiredEnv []string) error {
	if expected.PID > 0 && snapshot.identity.PID != expected.PID || expected.StartTime > 0 && snapshot.identity.StartTime != expected.StartTime {
		return errProcessIdentityChanged
	}
	ownerMarker := containsExactEnvironmentEntry(snapshot.environ, "TERMINAL_REDEEMER_HARNESS_OWNER="+ownerID)
	executable := strings.TrimSuffix(snapshot.exe, " (deleted)")
	fixtureExecutable := ownerID != "" && strings.HasPrefix(executable, ownerID+string(os.PathSeparator))
	// Zellij daemonization retains the exact private socket root but deliberately
	// filters unrelated environment keys, including the harness owner marker.
	// Accept only the conjunction of the exact Zellij executable basename and a
	// required socket-root entry beneath this fixture root; requiredEnv is still
	// checked entry-for-entry below immediately before every pidfd signal.
	fixtureZellij := false
	for _, item := range requiredEnv {
		if filepath.Base(executable) == "zellij" && strings.HasPrefix(item, "ZELLIJ_SOCKET_DIR="+ownerID+string(os.PathSeparator)) && containsExactEnvironmentEntry(snapshot.environ, item) {
			fixtureZellij = true
		}
	}
	if ownerID == "" || !ownerMarker && !fixtureExecutable && !fixtureZellij {
		return errProcessOwnerChanged
	}
	for _, item := range requiredEnv {
		if !containsExactEnvironmentEntry(snapshot.environ, item) {
			return errProcessOwnerChanged
		}
	}
	return nil
}

// pinOwnedProcess opens the pidfd before any /proc read. The pidfd pins the
// kernel process identity while start time and the exact fixture marker are
// read; signal() repeats those checks immediately before pidfd_send_signal.
func pinOwnedProcess(pid int, expected processIdentity, ownerID string, requiredEnv ...string) (*pinnedProcess, error) {
	return pinOwnedProcessWithin(pid, expected, ownerID, 0, requiredEnv...)
}

// A just-forked child may briefly expose the pre-exec /proc image even though
// exec.Cmd.Start has returned. The pidfd is still acquired first; only pinned
// metadata reads are retried for a short, bounded interval.
func pinStartedOwnedProcess(pid int, expected processIdentity, ownerID string, requiredEnv ...string) (*pinnedProcess, error) {
	return pinOwnedProcessWithin(pid, expected, ownerID, 100*time.Millisecond, requiredEnv...)
}

func pinOwnedProcessWithin(pid int, expected processIdentity, ownerID string, retry time.Duration, requiredEnv ...string) (*pinnedProcess, error) {
	if pid <= 0 || pid == os.Getpid() {
		return nil, errProcessIdentityChanged
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	proc := &pinnedProcess{fd: fd, ownerID: ownerID, requiredEnv: append([]string(nil), requiredEnv...)}
	deadline := time.Now().Add(retry)
	for {
		snapshot, readErr := readProcSnapshot(pid)
		if readErr == nil {
			readErr = validateOwnedSnapshot(snapshot, expected, ownerID, requiredEnv)
		}
		if readErr == nil {
			proc.identity = snapshot.identity
			return proc, nil
		}
		err = readErr
		if time.Now().After(deadline) || (!errors.Is(err, errProcessOwnerChanged) && retry == 0) {
			proc.close()
			return nil, err
		}
		time.Sleep(time.Millisecond)
	}
}

func (proc *pinnedProcess) close() {
	if proc != nil && proc.fd >= 0 {
		_ = unix.Close(proc.fd)
		proc.fd = -1
	}
}

func (proc *pinnedProcess) revalidate() (procSnapshot, error) {
	if proc == nil || proc.fd < 0 {
		return procSnapshot{}, errProcessExited
	}
	snapshot, err := readProcSnapshot(proc.identity.PID)
	if err != nil {
		return procSnapshot{}, errProcessExited
	}
	if err := validateOwnedSnapshot(snapshot, proc.identity, proc.ownerID, proc.requiredEnv); err != nil {
		return procSnapshot{}, err
	}
	if snapshot.state == 'Z' || snapshot.state == 'X' || snapshot.state == 'x' {
		return snapshot, errProcessExited
	}
	return snapshot, nil
}

func (proc *pinnedProcess) signal(sig syscall.Signal) error {
	if _, err := proc.revalidate(); err != nil {
		return err
	}
	if err := unix.PidfdSendSignal(proc.fd, unix.Signal(sig), nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return errProcessExited
		}
		return err
	}
	return nil
}

func (proc *pinnedProcess) wait(timeout time.Duration) (bool, error) {
	if proc == nil || proc.fd < 0 {
		return true, nil
	}
	millis := int(timeout / time.Millisecond)
	if timeout > 0 && millis == 0 {
		millis = 1
	}
	fds := []unix.PollFd{{Fd: int32(proc.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, millis)
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return n > 0, err
}

func processExited(pid int) bool {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return true
	}
	defer unix.Close(fd)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err != nil || n > 0
}

func terminatePinned(proc *pinnedProcess, grace time.Duration) error {
	if err := proc.signal(syscall.SIGTERM); err != nil && !errors.Is(err, errProcessExited) {
		return err
	}
	if exited, err := proc.wait(grace); err != nil || exited {
		return err
	}
	if err := proc.signal(syscall.SIGKILL); err != nil && !errors.Is(err, errProcessExited) {
		return err
	}
	_, err := proc.wait(grace)
	return err
}

func ownedIdentityAlive(id processIdentity, ownerID string) bool {
	proc, err := pinOwnedProcess(id.PID, id, ownerID)
	if err != nil {
		return false
	}
	defer proc.close()
	exited, err := proc.wait(0)
	return err == nil && !exited
}

func terminateAndWait(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	proc, err := pinOwnedProcess(cmd.Process.Pid, processIdentity{}, ownerIDFromEnv(cmd.Env))
	if err != nil {
		return
	}
	defer proc.close()
	_ = proc.signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		_ = proc.signal(syscall.SIGKILL)
		<-done
	}
}

func waitZellijControlReady(t *testing.T, zellij string, env []string, session string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	consecutive := 0
	var last []byte
	var lastErr error
	for time.Now().Before(deadline) {
		cmd := exec.Command(zellij, "list-sessions", "--short")
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		last, lastErr = cmd.CombinedOutput()
		if lastErr == nil && contains(strings.Fields(string(last)), session) {
			consecutive++
			if consecutive == 3 {
				return
			}
		} else {
			consecutive = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("real Zellij control socket never stabilized for %q: %v %s", session, lastErr, last)
}

func collectOwnedProcesses(root, ledger string, requiredEnv ...string) []*pinnedProcess {
	owned := map[processIdentity]*pinnedProcess{}
	tryPin := func(pid int, expected processIdentity) {
		proc, err := pinOwnedProcess(pid, expected, root, requiredEnv...)
		if err != nil {
			return
		}
		if prior := owned[proc.identity]; prior != nil {
			proc.close()
			return
		}
		owned[proc.identity] = proc
	}
	// Open a pidfd before looking at any ownership metadata for every candidate.
	// Non-owned ambient processes fail exact marker validation and are closed.
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err == nil && pid != os.Getpid() {
			tryPin(pid, processIdentity{})
		}
	}
	// Ledger identities are hints only: each must independently pass the pinned
	// exact owner and start-time checks before it can enter the cleanup set.
	payload, _ := os.ReadFile(ledger)
	for _, line := range bytes.Split(bytes.TrimSpace(payload), []byte{'\n'}) {
		var v invocation
		if json.Unmarshal(line, &v) == nil && v.PID > 0 && v.StartTime > 0 {
			tryPin(v.PID, processIdentity{PID: v.PID, StartTime: v.StartTime})
		}
	}
	out := make([]*pinnedProcess, 0, len(owned))
	for _, proc := range owned {
		out = append(out, proc)
	}
	return out
}

func closePinnedProcesses(processes []*pinnedProcess) {
	for _, proc := range processes {
		proc.close()
	}
}

func collectOwnedIdentities(root, ledger string) []processIdentity {
	processes := collectOwnedProcesses(root, ledger)
	defer closePinnedProcesses(processes)
	out := make([]processIdentity, 0, len(processes))
	for _, proc := range processes {
		out = append(out, proc.identity)
	}
	return out
}

func releaseAllGates(faultDir string) {
	_ = os.MkdirAll(faultDir, 0o700)
	_ = os.WriteFile(filepath.Join(faultDir, "abort-all-gates"), []byte("abort"), 0o600)
	for _, verb := range []string{"launch", "token_replay", "attach", "liveness", "snapshot"} {
		_ = os.WriteFile(filepath.Join(faultDir, "release-host-"+verb), []byte("release"), 0o600)
	}
}

func assertOwnedIdentitiesGone(t *testing.T, ownerID string, ids []processIdentity, detail string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var remaining []string
		for _, id := range ids {
			if exactIdentityAlive(id) {
				state, _ := processState(id.PID)
				remaining = append(remaining, fmt.Sprintf("%d/%d(state=%c)", id.PID, id.StartTime, state))
			}
		}
		if len(remaining) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("owned process leak after %s (owner=%s): %v", detail, ownerID, remaining)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func cleanupOwnedFixture(t *testing.T, root, ledger, faultDir string) {
	t.Helper()
	releaseAllGates(faultDir)
	processes := collectOwnedProcesses(root, ledger)
	ids := make([]processIdentity, 0, len(processes))
	for _, proc := range processes {
		ids = append(ids, proc.identity)
		_ = proc.signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, proc := range processes {
		remaining := time.Until(deadline)
		if remaining > 0 {
			_, _ = proc.wait(remaining)
		}
	}
	for _, proc := range processes {
		if exited, _ := proc.wait(0); !exited {
			_ = proc.signal(syscall.SIGKILL)
		}
	}
	killDeadline := time.Now().Add(3 * time.Second)
	for _, proc := range processes {
		remaining := time.Until(killDeadline)
		if remaining > 0 {
			_, _ = proc.wait(remaining)
		}
	}
	closePinnedProcesses(processes)
	assertOwnedIdentitiesGone(t, root, ids, "fixture pidfd TERM/KILL cleanup")
	// Re-scan catches unledgered descendants. It acquires new pidfds first and
	// refuses processes whose exact owner/start evidence cannot be reproved.
	remaining := collectOwnedProcesses(root, ledger)
	remainingIDs := make([]processIdentity, 0, len(remaining))
	for _, proc := range remaining {
		remainingIDs = append(remainingIDs, proc.identity)
	}
	closePinnedProcesses(remaining)
	assertOwnedIdentitiesGone(t, root, remainingIDs, "final fixture-root pidfd scan")
}

// Real Zellij servers daemonize. Cleanup requires both the exact fixture owner
// and exact fixture socket root while pidfds pin each identity. Host-global
// session kill commands are intentionally never used.
func cleanupZellijProcesses(t *testing.T, ownerID, runtimeRoot string) {
	t.Helper()
	socketEnv := "ZELLIJ_SOCKET_DIR=" + filepath.Join(runtimeRoot, "z")
	candidates := collectOwnedProcesses(ownerID, "", socketEnv)
	processes := candidates[:0]
	for _, proc := range candidates {
		snapshot, err := proc.revalidate()
		if err == nil && filepath.Base(strings.TrimSuffix(snapshot.exe, " (deleted)")) == "zellij" {
			processes = append(processes, proc)
		} else {
			proc.close()
		}
	}
	defer closePinnedProcesses(processes)
	deadline := time.Now().Add(3 * time.Second)
	for _, proc := range processes {
		_ = proc.signal(syscall.SIGTERM)
	}
	for _, proc := range processes {
		remaining := time.Until(deadline)
		if remaining > 0 {
			_, _ = proc.wait(remaining)
		}
	}
	for _, proc := range processes {
		if exited, _ := proc.wait(0); !exited {
			_ = proc.signal(syscall.SIGKILL)
		}
	}
}

func dumpHarnessEvidence(t *testing.T, root, ledger string, servers ...*niriServer) {
	t.Helper()
	const max = 16 << 10
	paths := []string{ledger}
	for _, pattern := range []string{"h/state/slice/rpc-tokens/*.json", "l/state/slice/launch/intents/*.json", "l/state/slice/controller/current.json"} {
		matches, _ := filepath.Glob(filepath.Join(root, pattern))
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		payload, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(payload) > max {
			payload = payload[len(payload)-max:]
		}
		t.Logf("bounded harness failure evidence %s: %s", strings.TrimPrefix(path, root+"/"), payload)
	}
	for i, server := range servers {
		requests := server.requestLines()
		if len(requests) > 30 {
			requests = requests[len(requests)-30:]
		}
		t.Logf("bounded Niri ledger %d: %v", i, requests)
	}
	var evidence []string
	for _, id := range collectOwnedIdentities(root, ledger) {
		state, _ := processState(id.PID)
		evidence = append(evidence, fmt.Sprintf("pid=%d start=%d state=%c", id.PID, id.StartTime, state))
	}
	t.Logf("bounded owned process ledger: %v", evidence)
}

var _ = slicerpc.SchemaVersion
