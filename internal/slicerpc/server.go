package slicerpc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jmo/terminal-redeemer/internal/bootid"
	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceattach"
	"github.com/jmo/terminal-redeemer/internal/sliceenv"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type Snapshotter func(context.Context) (sliceprotocol.Envelope, error)

// ProveRoutedCommit performs the production routed-launch commit proof. Keeping
// the proof here lets the packaged RPC construction and the fsync-gated
// subprocess harness use the exact same tuple, authority, and compositor
// rotation checks without giving production code a fault-injection surface.
func ProveRoutedCommit(ctx context.Context, record TokenRecord, sourceEpoch, sourceFingerprint string, resolveContext func(context.Context) (map[string]string, error), snapshot Snapshotter, directTransaction DirectHostTransaction) (string, string, error) {
	if record.TransactionEpoch != sourceEpoch || record.TransactionFingerprint != sourceFingerprint {
		return "", "", errors.New("routed transaction epoch no longer current")
	}
	finalEnv, err := resolveContext(ctx)
	if err != nil {
		return "", "", err
	}
	boot, err := bootid.Current()
	if err != nil {
		return "", "", err
	}
	fingerprint, err := sourceinventory.NiriFingerprint(boot, finalEnv["NIRI_SOCKET"])
	if err != nil || fingerprint != sourceFingerprint {
		return "", "", errors.New("source compositor fingerprint changed before launch commit")
	}
	envelope, err := snapshot(ctx)
	if err != nil || envelope.Observation.Quality != sliceprotocol.QualityComplete || envelope.Authoritative == nil || envelope.Authoritative.SourceEpoch != sourceEpoch {
		return "", "", errors.New("complete matching source authority required before launch commit")
	}
	expectedID, err := sourceinventory.SourceID(sourceEpoch, record.NiriWindowID)
	if err != nil {
		return "", "", err
	}
	matched := false
	for _, source := range envelope.Authoritative.Sources {
		if source.SourceID == expectedID && source.RuntimeWindowID == record.NiriWindowID && source.Session.Name == record.SessionName && source.Workspace.Name == record.WorkspaceName {
			matched = true
		}
	}
	if !matched {
		return "", "", errors.New("exact routed source tuple absent from complete authority")
	}
	proofTransaction := directTransaction
	proofTransaction.Environment = finalEnv
	if err := proofTransaction.ProveWindow(ctx, record, record.WorkspaceName); err != nil {
		return "", "", err
	}
	lastEnv, err := resolveContext(ctx)
	if err != nil {
		return "", "", err
	}
	lastFingerprint, err := sourceinventory.NiriFingerprint(boot, lastEnv["NIRI_SOCKET"])
	if err != nil || lastFingerprint != sourceFingerprint {
		return "", "", errors.New("source compositor rotated during launch commit proof")
	}
	return expectedID, sourceEpoch, nil
}

type NiriMutator interface {
	Snapshot(context.Context) (niriipc.State, error)
	Action(context.Context, any) error
}
type LaunchResult struct {
	Started bool
	Err     error
}
type Launcher interface {
	Launch(context.Context, string) LaunchResult
}
type LauncherFunc func(context.Context, string) LaunchResult

func (fn LauncherFunc) Launch(ctx context.Context, id string) LaunchResult { return fn(ctx, id) }

type HostTransaction interface {
	EnsureSession(context.Context, TokenRecord) (bool, error)
	PlanKitty(context.Context, TokenRecord) (sliceattach.ExactSocketIdentity, error)
	PrepareKitty(context.Context, TokenRecord) (sliceattach.ExactSocketIdentity, error)
	EnsureKitty(context.Context, TokenRecord) (int, uint64, bool, error)
	Place(context.Context, TokenRecord, uint64) error
	CleanupKitty(context.Context, TokenRecord) error
}

type DirectHostTransaction struct {
	SelfCommand       string
	ZellijCommand     string
	KittyCommand      string
	Environment       map[string]string
	Niri              NiriMutator
	SourceEpoch       string
	ZellijSocketDir   string
	CreationCacheRoot string
	ShimCache         string
	PollInterval      time.Duration
	Run               func(context.Context, string, []string, []string) ([]byte, bool, error)
	StartKitty        func(context.Context, string, []string, []string) (int, bool, error)
	ReadProcess       func(int) (string, []string, error)
}

func cleanGraphicalEnv(environment map[string]string) ([]string, error) {
	if err := sliceenv.ValidateContext(environment); err != nil {
		return nil, err
	}
	keys := []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"}
	env := make([]string, 0, 3)
	for _, key := range keys {
		value := environment[key]
		if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid graphical environment")
		}
		env = append(env, key+"="+value)
	}
	return env, nil
}
func directOutput(ctx context.Context, command string, args, env []string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	err := cmd.Wait()
	if output.Len() > 1<<20 {
		return nil, true, errors.New("command output exceeds bound")
	}
	return output.Bytes(), true, err
}
func directStartKitty(ctx context.Context, command string, args, env []string) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	cmd := exec.Command(command, args...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return 0, false, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, true, nil
}
func (t DirectHostTransaction) interval() time.Duration {
	if t.PollInterval > 0 {
		return t.PollInterval
	}
	return 100 * time.Millisecond
}
func (t DirectHostTransaction) runner() func(context.Context, string, []string, []string) ([]byte, bool, error) {
	if t.Run != nil {
		return t.Run
	}
	return directOutput
}
func privateCache(path string, requireEmpty bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("Zellij cache path must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("unsafe Zellij cache")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || int(stat.Uid) != os.Getuid() {
		return "", errors.New("unsafe Zellij cache")
	}
	if requireEmpty {
		entries, err := os.ReadDir(path)
		if err != nil {
			return "", err
		}
		if len(entries) != 0 {
			return "", errors.New("Zellij isolation cache is not empty")
		}
	}
	return path, nil
}
func (t DirectHostTransaction) cacheContext(r TokenRecord) (base []string, catalog, create, attach string, err error) {
	if _, err = privateCache(t.ShimCache, false); err != nil {
		return
	}
	if _, err = privateCache(t.CreationCacheRoot, false); err != nil {
		return
	}
	catalog, err = privateCache(filepath.Join(t.ShimCache, "catalog"), false)
	if err != nil {
		return
	}
	attach, err = privateCache(filepath.Join(t.ShimCache, "attach-"+r.SessionName), true)
	if err != nil {
		return
	}
	create, err = privateCache(filepath.Join(t.CreationCacheRoot, r.SessionName), false)
	if err != nil {
		return
	}
	base, err = cleanGraphicalEnv(t.Environment)
	if err != nil {
		return
	}
	if t.ZellijSocketDir != "" {
		if !filepath.IsAbs(t.ZellijSocketDir) || len(t.ZellijSocketDir) > 4096 || strings.ContainsAny(t.ZellijSocketDir, "\x00\r\n") {
			err = errors.New("invalid Zellij socket directory")
			return
		}
		base = append(base, "ZELLIJ_SOCKET_DIR="+t.ZellijSocketDir)
	}
	return
}
func (t DirectHostTransaction) EnsureSession(ctx context.Context, r TokenRecord) (bool, error) {
	if t.ZellijCommand == "" || r.SessionName == "" {
		return false, errors.New("Zellij transaction unavailable")
	}
	socketRoot := t.ZellijSocketDir
	if socketRoot == "" {
		socketRoot = filepath.Join(t.Environment["XDG_RUNTIME_DIR"], "zellij")
	}
	effective := filepath.Join(socketRoot, zellijlive.SocketContractDir, r.SessionName)
	if !filepath.IsAbs(socketRoot) || len([]byte(effective)) > zellijlive.MaxSocketPathBytes {
		return false, errors.New("effective Zellij socket path exceeds pinned limit")
	}
	base, catalog, createCache, _, err := t.cacheContext(r)
	if err != nil {
		return false, err
	}
	env := append(append([]string(nil), base...), "XDG_CACHE_HOME="+catalog)
	run := t.runner()
	version, started, err := run(ctx, t.ZellijCommand, []string{"--version"}, env)
	if err != nil || strings.TrimSpace(string(version)) != "zellij "+zellijlive.PinnedVersion {
		return started, errors.New("pinned Zellij unavailable")
	}
	listed, listStarted, listErr := run(ctx, t.ZellijCommand, []string{"list-sessions", "--short"}, env)
	if listErr != nil && (!listStarted || !strings.Contains(string(listed), "No active zellij sessions found")) {
		return listStarted, listErr
	}
	scanner := bufio.NewScanner(bytes.NewReader(listed))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == r.SessionName {
			if r.Stage == "pending" {
				return false, errors.New("fresh routed session name collision")
			}
			return false, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	if r.Stage == "session_starting" {
		return true, errors.New("session creation outcome remains ambiguous")
	}
	entries, err := os.ReadDir(createCache)
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, errors.New("refusing possible dead-session resurrection")
	}
	createEnv := append(append([]string(nil), base...), "XDG_CACHE_HOME="+createCache)
	_, created, err := run(ctx, t.ZellijCommand, []string{"attach", "--create-background", r.SessionName}, createEnv)
	return created, err
}
func (t DirectHostTransaction) socketRoot() string {
	if t.ZellijSocketDir != "" {
		return t.ZellijSocketDir
	}
	return filepath.Join(t.Environment["XDG_RUNTIME_DIR"], "zellij")
}
func hostAttachToken(session string) string {
	sum := sha256.Sum256([]byte("terminal-redeemer/host-exact-attach/v1\x00" + session))
	return "h" + base64.RawURLEncoding.EncodeToString(sum[:10])
}
func socketIdentityFromRecord(r TokenRecord) sliceattach.ExactSocketIdentity {
	return sliceattach.ExactSocketIdentity{
		Path: r.PreparedSocketPath, MarkerDevice: r.PreparedMarkerDevice, MarkerInode: r.PreparedMarkerInode,
		SocketDevice: r.PreparedSocketDevice, SocketInode: r.PreparedSocketInode,
	}
}
func (t DirectHostTransaction) PlanKitty(ctx context.Context, r TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	base, catalog, _, _, err := t.cacheContext(r)
	if err != nil {
		return sliceattach.ExactSocketIdentity{}, err
	}
	catalogEnv := append(append([]string(nil), base...), "XDG_CACHE_HOME="+catalog)
	// `attach --create-background` can return after publishing the socket but
	// before the new server answers `list-sessions`. That observed fresh-Nix
	// window is not definitive absence: wait boundedly for the exact name before
	// planning the immutable attachment namespace.
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var lastErr error
	for {
		listed, _, listErr := t.runner()(readyCtx, t.ZellijCommand, []string{"list-sessions", "--short"}, catalogEnv)
		found := false
		if listErr == nil {
			scanner := bufio.NewScanner(bytes.NewReader(listed))
			for scanner.Scan() {
				if strings.TrimSpace(scanner.Text()) == r.SessionName {
					found = true
				}
			}
			lastErr = scanner.Err()
		} else {
			lastErr = listErr
		}
		if found && lastErr == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			if ctx.Err() != nil {
				return sliceattach.ExactSocketIdentity{}, ctx.Err()
			}
			if lastErr != nil {
				return sliceattach.ExactSocketIdentity{}, fmt.Errorf("exact live Zellij session readiness: %w", lastErr)
			}
			return sliceattach.ExactSocketIdentity{}, errors.New("exact live Zellij session unavailable after readiness window")
		case <-time.After(t.interval()):
		}
	}
	root := t.socketRoot()
	planned, outcome := sliceattach.PlanExactSocket(root, filepath.Join(root, ".trh"), r.SessionName, hostAttachToken(r.SessionName), os.Getuid())
	if outcome.Status != "" {
		return sliceattach.ExactSocketIdentity{}, fmt.Errorf("exact host Zellij socket plan %s/%s", outcome.Status, outcome.Code)
	}
	return planned, nil
}
func (t DirectHostTransaction) validPreparedPath(r TokenRecord) bool {
	return r.PreparedSocketPath == filepath.Join(t.socketRoot(), ".trh", "att-"+hostAttachToken(r.SessionName))
}
func (t DirectHostTransaction) PrepareKitty(_ context.Context, r TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	root := t.socketRoot()
	prepared, outcome := sliceattach.PreparePlannedExactSocket(root, filepath.Join(root, ".trh"), r.SessionName, hostAttachToken(r.SessionName), os.Getuid(), socketIdentityFromRecord(r))
	if outcome.Status != "" {
		return sliceattach.ExactSocketIdentity{}, fmt.Errorf("exact host Zellij socket setup %s/%s", outcome.Status, outcome.Code)
	}
	return prepared, nil
}
func (t DirectHostTransaction) CleanupKitty(_ context.Context, r TokenRecord) error {
	if !t.validPreparedPath(r) {
		return errors.New("prepared exact Zellij namespace path mismatch")
	}
	if outcome := sliceattach.RemovePreparedExactSocket(socketIdentityFromRecord(r), r.SessionName, os.Getuid()); outcome.Status != "" {
		return fmt.Errorf("exact host Zellij socket cleanup %s/%s", outcome.Status, outcome.Code)
	}
	return nil
}
func withoutEnvKey(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, value := range env {
		if !strings.HasPrefix(value, prefix) {
			out = append(out, value)
		}
	}
	return out
}
func hostKittyArgs(t DirectHostTransaction, r TokenRecord) []string {
	app := hostAppID(r.HostTerminalID)
	return []string{
		"--config", "NONE", "--class", app, "--title", "terminal-redeemer-host:" + r.HostTerminalID,
		t.SelfCommand, "slice", "host-attach",
		"--session", r.SessionName,
		"--prepared-path", r.PreparedSocketPath,
		"--marker-device", strconv.FormatUint(r.PreparedMarkerDevice, 10),
		"--marker-inode", strconv.FormatUint(r.PreparedMarkerInode, 10),
		"--socket-device", strconv.FormatUint(r.PreparedSocketDevice, 10),
		"--socket-inode", strconv.FormatUint(r.PreparedSocketInode, 10),
		"--shim-cache", filepath.Join(t.ShimCache, "attach-"+r.SessionName),
		"--zellij-command", t.ZellijCommand,
	}
}
func readProcess(pid int) (string, []string, error) {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", nil, err
	}
	parts := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	argv := make([]string, len(parts))
	for i, p := range parts {
		argv[i] = string(p)
	}
	return exe, argv, nil
}
func (t DirectHostTransaction) verifyProcess(pid int, r TokenRecord) error {
	read := t.ReadProcess
	if read == nil {
		read = readProcess
	}
	exe, argv, err := read(pid)
	if err != nil {
		return err
	}
	expected := append([]string{t.KittyCommand}, hostKittyArgs(t, r)...)
	if exe != t.KittyCommand || !reflect.DeepEqual(argv, expected) {
		return errors.New("host Kitty process evidence mismatch")
	}
	return nil
}
func hostAppID(id string) string {
	sum := sha256.Sum256([]byte("terminal-redeemer/host-app/v1\x00" + id))
	return "terminal-redeemer-host-" + hex.EncodeToString(sum[:8])
}
func (t DirectHostTransaction) findWindow(ctx context.Context, r TokenRecord) (int, uint64, error) {
	state, err := t.Niri.Snapshot(ctx)
	if err != nil {
		return 0, 0, err
	}
	app := hostAppID(r.HostTerminalID)
	matches := []niriipc.Window{}
	for _, w := range state.Windows {
		if w.AppID == app && (r.KittyPID == 0 || w.PID == r.KittyPID) {
			matches = append(matches, w)
		}
	}
	if len(matches) > 1 {
		return 0, 0, errors.New("ambiguous host Kitty windows")
	}
	if len(matches) == 1 {
		if err := t.verifyProcess(matches[0].PID, r); err != nil {
			return 0, 0, err
		}
		return matches[0].PID, matches[0].ID, nil
	}
	return 0, 0, nil
}
func (t DirectHostTransaction) EnsureKitty(ctx context.Context, r TokenRecord) (int, uint64, bool, error) {
	if t.Niri == nil || t.SelfCommand == "" || t.KittyCommand == "" || t.ZellijCommand == "" {
		return 0, 0, false, errors.New("host Kitty transaction unavailable")
	}
	if !t.validPreparedPath(r) {
		return 0, 0, true, errors.New("prepared exact Zellij namespace path mismatch")
	}
	if _, outcome := sliceattach.ValidatePreparedExactSocket(socketIdentityFromRecord(r), r.SessionName, os.Getuid()); outcome.Status != "" {
		return 0, 0, true, fmt.Errorf("prepared exact Zellij namespace unavailable %s/%s", outcome.Status, outcome.Code)
	}
	if pid, wid, err := t.findWindow(ctx, r); err != nil {
		return pid, wid, false, err
	} else if wid != 0 {
		return pid, wid, false, nil
	}
	if r.Stage == "kitty_starting" {
		return 0, 0, true, errors.New("Kitty creation outcome remains ambiguous")
	}
	base, _, _, _, err := t.cacheContext(r)
	if err != nil {
		return 0, 0, false, err
	}
	// The packaged child helper receives the journaled identity explicitly and
	// owns the isolated socket/cache environment for the attached client.
	env := append(withoutEnvKey(base, "ZELLIJ_SOCKET_DIR"), "TERMINAL_REDEEMER_HOST_TERMINAL_ID="+r.HostTerminalID)
	args := hostKittyArgs(t, r)
	start := t.StartKitty
	if start == nil {
		start = directStartKitty
	}
	pid, started, err := start(ctx, t.KittyCommand, args, env)
	if err != nil {
		return pid, 0, started, err
	}
	if !started {
		return pid, 0, false, errors.New("Kitty did not start")
	}
	ticker := time.NewTicker(t.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return pid, 0, true, ctx.Err()
		case <-ticker.C:
			probe := r
			probe.KittyPID = pid
			p, w, findErr := t.findWindow(ctx, probe)
			if findErr != nil {
				return pid, 0, true, findErr
			}
			if w != 0 {
				return p, w, true, nil
			}
		}
	}
}
func (t DirectHostTransaction) ProveWindow(ctx context.Context, r TokenRecord, workspaceName string) error {
	state, err := t.Niri.Snapshot(ctx)
	if err != nil {
		return err
	}
	workspaceIDs := map[uint64]bool{}
	for _, workspace := range state.Workspaces {
		if workspace.Name != nil && *workspace.Name == workspaceName {
			workspaceIDs[workspace.ID] = true
		}
	}
	matches := 0
	for _, window := range state.Windows {
		if window.ID == r.NiriWindowID && window.PID == r.KittyPID && window.AppID == hostAppID(r.HostTerminalID) && window.WorkspaceID != nil && workspaceIDs[*window.WorkspaceID] {
			if err := t.verifyProcess(window.PID, r); err != nil {
				return err
			}
			matches++
		}
	}
	if matches != 1 {
		return errors.New("exact committed host window tuple unavailable")
	}
	return nil
}
func (t DirectHostTransaction) Place(ctx context.Context, r TokenRecord, workspaceID uint64) error {
	if t.Niri == nil || r.NiriWindowID == 0 {
		return errors.New("host placement unavailable")
	}
	action := map[string]any{"MoveWindowToWorkspace": niriipc.MoveWindowToWorkspaceAction{WindowID: r.NiriWindowID, Reference: niriipc.WorkspaceReference{ID: workspaceID}, Focus: false}}
	if err := t.Niri.Action(ctx, action); err != nil {
		return err
	}
	ticker := time.NewTicker(t.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			state, err := t.Niri.Snapshot(ctx)
			if err != nil {
				continue
			}
			for _, w := range state.Windows {
				if w.ID == r.NiriWindowID && w.PID == r.KittyPID && w.AppID == hostAppID(r.HostTerminalID) && w.WorkspaceID != nil && *w.WorkspaceID == workspaceID {
					return nil
				}
			}
		}
	}
}

type DirectKittyLauncher struct {
	Command     string
	Environment map[string]string
	Run         func(context.Context, string, []string, []string) LaunchResult
}

func (l DirectKittyLauncher) Launch(ctx context.Context, terminalID string) LaunchResult {
	if !utf8.ValidString(l.Command) || strings.TrimSpace(l.Command) == "" {
		return LaunchResult{Err: errors.New("Kitty executable is required")}
	}
	if !safeID.MatchString(terminalID) || len(terminalID) > 128 {
		return LaunchResult{Err: errors.New("host terminal identity is invalid")}
	}
	if err := sliceenv.ValidateContext(l.Environment); err != nil {
		return LaunchResult{Err: err}
	}
	args := []string{"--config", "NONE", "--detach", "--class", "terminal-redeemer-host", "--title", "terminal-redeemer-host:" + terminalID}
	env := make([]string, 0, len(l.Environment)+1)
	keys := make([]string, 0, len(l.Environment))
	for key := range l.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := l.Environment[key]
		if !utf8.ValidString(key) || !utf8.ValidString(value) || strings.ContainsAny(key+value, "\x00\r\n") || len(value) > 4096 {
			return LaunchResult{Err: errors.New("invalid graphical launch context")}
		}
		env = append(env, key+"="+value)
	}
	env = append(env, "TERMINAL_REDEEMER_HOST_TERMINAL_ID="+terminalID)
	run := l.Run
	if run == nil {
		run = runCommand
	}
	return run(ctx, l.Command, args, env)
}
func runCommand(ctx context.Context, command string, args, env []string) LaunchResult {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return LaunchResult{Err: err}
	}
	return LaunchResult{Started: true, Err: cmd.Wait()}
}

type Server struct {
	SourceHostID          string
	Tokens                *TokenStore
	TokenStateUnavailable bool
	Snapshot              Snapshotter
	Niri                  NiriMutator
	Launcher              Launcher
	HostTransaction       HostTransaction
	SourceEpoch           string
	SourceFingerprint     string
	ProveCommit           func(context.Context, TokenRecord) (string, string, error)
	CheckNiri             func(context.Context) error
	Now                   func() time.Time
	PollInterval          time.Duration
	// AfterDurableStage is an internal process-harness boundary. Production
	// callers leave it nil; subprocess acceptance uses it to stop a disposable
	// RPC process only after the token journal fsync for an exact stage.
	AfterDurableStage func(TokenRecord)
}

func (s Server) Handle(ctx context.Context, request Request) Response {
	response := Response{SchemaVersion: SchemaVersion, RequestID: request.RequestID}
	fail := func(status OutcomeStatus, code string) Response {
		response.Outcome = Outcome{Status: status, Code: code}
		return response
	}
	if s.CheckNiri != nil && (request.Verb == VerbLiveness || request.Verb == VerbSnapshot || request.Verb == VerbWorkspaceEnsure) {
		if err := s.CheckNiri(ctx); err != nil {
			return fail(StatusUnavailable, "niri_version_unavailable")
		}
	}
	switch request.Verb {
	case VerbLiveness:
		var payload struct{}
		if err := DecodePayload(request.Payload, &payload); err != nil {
			return fail(StatusInvalid, "invalid_payload")
		}
		response.Outcome = Outcome{Status: StatusOK}
		response.Result = map[string]any{"alive": true, "schema_versions": []uint32{SchemaVersion}}
		return response
	case VerbSnapshot:
		var payload struct{}
		if err := DecodePayload(request.Payload, &payload); err != nil {
			return fail(StatusInvalid, "invalid_payload")
		}
		if s.Snapshot == nil {
			return fail(StatusUnavailable, "snapshot_unavailable")
		}
		envelope, err := s.Snapshot(ctx)
		if err != nil {
			return fail(StatusUnavailable, "snapshot_unavailable")
		}
		response.Outcome = Outcome{Status: StatusOK}
		response.Result = envelope
		return response
	case VerbWorkspaceEnsure:
		var payload WorkspaceEnsurePayload
		if err := DecodePayload(request.Payload, &payload); err != nil {
			return fail(StatusInvalid, "invalid_payload")
		}
		if err := validateWorkspaceName(payload.Name); err != nil {
			return fail(StatusInvalid, "invalid_workspace_name")
		}
		id, err := s.EnsureWorkspace(ctx, payload.Name)
		if err != nil {
			return fail(StatusUnavailable, "workspace_ensure_failed")
		}
		response.Outcome = Outcome{Status: StatusOK}
		response.Result = map[string]any{"workspace_id": id}
		return response
	case VerbLaunch:
		var payload LaunchPayload
		if err := DecodePayload(request.Payload, &payload); err != nil || !ValidToken(payload.Token) {
			return fail(StatusInvalid, "invalid_token")
		}
		if payload.SessionName != "" || payload.WorkspaceName != "" {
			if s.HostTransaction == nil {
				return fail(StatusUnavailable, "launch_unavailable")
			}
			if validateSessionName(payload.SessionName) != nil || payload.SessionName != StableSessionName(payload.Token) || validateWorkspaceName(payload.WorkspaceName) != nil {
				return fail(StatusInvalid, "invalid_launch_metadata")
			}
		}
		return s.launch(ctx, request.RequestID, payload)
	case VerbTokenQuery, VerbTokenReplay:
		var payload TokenPayload
		if err := DecodePayload(request.Payload, &payload); err != nil || !ValidToken(payload.Token) {
			return fail(StatusInvalid, "invalid_token")
		}
		if payload.SessionName != "" || payload.WorkspaceName != "" {
			if validateSessionName(payload.SessionName) != nil || payload.SessionName != StableSessionName(payload.Token) || validateWorkspaceName(payload.WorkspaceName) != nil {
				return fail(StatusInvalid, "invalid_launch_metadata")
			}
		}
		if s.Tokens == nil {
			if s.TokenStateUnavailable {
				return fail(StatusUnavailable, "token_state_unavailable")
			}
			return fail(StatusUnavailable, "token_store_unavailable")
		}
		record, err := s.Tokens.Read(payload.Token)
		if errors.Is(err, ErrTokenNotFound) {
			return fail(StatusUnavailable, "token_not_found")
		}
		if err != nil {
			return fail(StatusUnavailable, "token_state_unavailable")
		}
		if request.Verb == VerbTokenReplay && record.SessionName != "" && (payload.SessionName != record.SessionName || payload.WorkspaceName != record.WorkspaceName) {
			return fail(StatusInvalid, "invalid_launch_metadata")
		}
		if request.Verb == VerbTokenReplay && record.Status == TokenPending && record.SessionName != "" && s.HostTransaction != nil {
			return s.resumeHostLaunch(ctx, request.RequestID, record)
		}
		return responseForRecord(request.RequestID, record)
	default:
		return fail(StatusInvalid, "unsupported_verb")
	}
}

func (s Server) durableStage(record TokenRecord) {
	if s.AfterDurableStage != nil {
		s.AfterDurableStage(record)
	}
}

func (s Server) launch(ctx context.Context, requestID string, payload LaunchPayload) Response {
	if s.TokenStateUnavailable {
		return Response{SchemaVersion: SchemaVersion, RequestID: requestID, Outcome: Outcome{Status: StatusUnavailable, Code: "token_state_unavailable"}}
	}
	if s.Tokens == nil || (s.Launcher == nil && s.HostTransaction == nil) || s.SourceHostID == "" {
		return Response{SchemaVersion: SchemaVersion, RequestID: requestID, Outcome: Outcome{Status: StatusUnavailable, Code: "launch_unavailable"}}
	}
	token := payload.Token
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	record, created, err := s.Tokens.CreatePendingRouted(s.SourceHostID, s.SourceEpoch, s.SourceFingerprint, token, payload.SessionName, payload.WorkspaceName, now)
	if err != nil {
		return Response{SchemaVersion: SchemaVersion, RequestID: requestID, Outcome: Outcome{Status: StatusUnavailable, Code: "token_state_unavailable"}}
	}
	if !created {
		if record.SessionName != payload.SessionName || record.WorkspaceName != payload.WorkspaceName || (record.SessionName != "" && (record.TransactionEpoch != s.SourceEpoch || record.TransactionFingerprint != s.SourceFingerprint)) {
			return Response{SchemaVersion: SchemaVersion, RequestID: requestID, Outcome: Outcome{Status: StatusFailed, Code: "launch_identity_conflict"}}
		}
		return responseForRecord(requestID, record)
	}
	s.durableStage(record)
	if s.HostTransaction != nil {
		return s.resumeHostLaunch(ctx, requestID, record)
	}
	// Pending was fsynced before this side effect. Any interruption after this
	// point remains pending and is never automatically repeated.
	launch := s.Launcher.Launch(ctx, record.HostTerminalID)
	if launch.Err != nil {
		if launch.Started || ctx.Err() != nil {
			return responseForRecord(requestID, record)
		}
		record.Status = TokenFailed
		record.UpdatedAt = now
		if updateErr := s.Tokens.Update(record); updateErr != nil {
			return responseForRecord(requestID, TokenRecord{Token: record.Token, HostTerminalID: record.HostTerminalID, Status: TokenPending})
		}
		return responseForRecord(requestID, record)
	}
	if !launch.Started {
		return responseForRecord(requestID, record)
	}
	record.Status = TokenLaunched
	record.UpdatedAt = now
	if err := s.Tokens.Update(record); err != nil {
		record.Status = TokenPending
	}
	return responseForRecord(requestID, record)
}
func (s Server) resumeHostLaunch(ctx context.Context, requestID string, record TokenRecord) Response {
	lock, lockErr := s.Tokens.LockToken(record.Token)
	if lockErr != nil {
		return responseForRecord(requestID, record)
	}
	defer UnlockToken(lock)
	latest, readErr := s.Tokens.Read(record.Token)
	if readErr != nil {
		return responseForRecord(requestID, record)
	}
	record = latest
	if record.Status != TokenPending {
		return responseForRecord(requestID, record)
	}
	if record.SessionName != "" && (record.TransactionEpoch != s.SourceEpoch || record.TransactionFingerprint != s.SourceFingerprint) {
		return responseForRecord(requestID, record)
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	pending := func() Response { return responseForRecord(requestID, record) }
	failDefinite := func() Response {
		record.Status = TokenFailed
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			record.Status = TokenPending
		}
		return responseForRecord(requestID, record)
	}
	if record.Stage == "pending" {
		launchRecord := record
		record.Stage = "session_starting"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
		started, err := s.HostTransaction.EnsureSession(ctx, launchRecord)
		if err != nil {
			if started || ctx.Err() != nil {
				return pending()
			}
			return failDefinite()
		}
		record.Stage = "session_created"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
	}
	if record.Stage == "session_starting" {
		started, err := s.HostTransaction.EnsureSession(ctx, record)
		if err != nil || started {
			return pending()
		}
		record.Stage = "session_created"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
	}
	if record.Stage == "session_created" {
		planned, err := s.HostTransaction.PlanKitty(ctx, record)
		if err != nil {
			return failDefinite()
		}
		record.PreparedSocketPath = planned.Path
		record.PreparedSocketDevice = planned.SocketDevice
		record.PreparedSocketInode = planned.SocketInode
		record.Stage = "socket_planned"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
	}
	if record.Stage == "socket_planned" {
		prepared, err := s.HostTransaction.PrepareKitty(ctx, record)
		if err != nil {
			// Preparation may have completed before a crash or failed midway.
			// Retain marker-owned state for replay/checked GC; never switch to
			// an ordinary same-name socket or start Kitty without journal proof.
			return pending()
		}
		record.PreparedMarkerDevice = prepared.MarkerDevice
		record.PreparedMarkerInode = prepared.MarkerInode
		record.Stage = "kitty_prepared"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
	}
	if record.Stage == "kitty_prepared" {
		launchRecord := record
		record.Stage = "kitty_starting"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
		pid, wid, started, err := s.HostTransaction.EnsureKitty(ctx, launchRecord)
		if err != nil {
			if started || ctx.Err() != nil {
				return pending()
			}
			// Kitty and therefore the packaged lifecycle helper definitely did
			// not start. This is the server's only cleanup boundary.
			if cleanupErr := s.HostTransaction.CleanupKitty(ctx, launchRecord); cleanupErr != nil {
				return pending()
			}
			return failDefinite()
		}
		record.KittyPID = pid
		record.NiriWindowID = wid
		record.Stage = "kitty_started"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
	}
	if record.Stage == "kitty_starting" {
		pid, wid, started, err := s.HostTransaction.EnsureKitty(ctx, record)
		if err != nil || started || wid == 0 {
			return pending()
		}
		record.KittyPID = pid
		record.NiriWindowID = wid
		record.Stage = "kitty_started"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
	}
	if record.Stage == "kitty_started" {
		workspaceID, err := s.EnsureWorkspace(ctx, record.WorkspaceName)
		if err != nil {
			return pending()
		}
		if err = s.HostTransaction.Place(ctx, record, workspaceID); err != nil {
			return pending()
		}
		record.Stage = "placed"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			return pending()
		}
		s.durableStage(record)
	}
	if record.Stage == "placed" {
		if s.ProveCommit == nil {
			return pending()
		}
		sourceID, epoch, proofErr := s.ProveCommit(ctx, record)
		if proofErr != nil || sourceID == "" || epoch == "" {
			return pending()
		}
		// Source proof commits identity only. It is deliberately not attachment
		// readiness: the packaged Kitty child retains the namespace until its
		// attached Zellij client exits, including proof-before-first-lookup.
		record.SourceID = sourceID
		record.SourceEpoch = epoch
		record.Stage = "proof_committed"
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			record.SourceID = ""
			record.SourceEpoch = ""
			record.Stage = "placed"
			return pending()
		}
		s.durableStage(record)
	}
	if record.Stage == "proof_committed" {
		// A durable identity proof is necessary but not sufficient after process
		// restart: re-run the complete authority/tuple/compositor rotation proof
		// in the current packaged RPC construction immediately before commit.
		if s.ProveCommit == nil {
			return pending()
		}
		sourceID, epoch, proofErr := s.ProveCommit(ctx, record)
		if proofErr != nil || sourceID != record.SourceID || epoch != record.SourceEpoch {
			return pending()
		}
		record.Stage = "committed"
		record.Status = TokenLaunched
		record.UpdatedAt = now
		if s.Tokens.Update(record) != nil {
			record.Status = TokenPending
			record.Stage = "proof_committed"
		} else {
			s.durableStage(record)
		}
	}
	return responseForRecord(requestID, record)
}

func responseForRecord(requestID string, record TokenRecord) Response {
	status := StatusPending
	code := "launch_pending"
	if record.Status == TokenLaunched {
		status = StatusOK
		code = ""
	}
	if record.Status == TokenFailed {
		status = StatusFailed
		code = "launch_failed"
	}
	result := map[string]any{"token": record.Token, "host_terminal_id": record.HostTerminalID, "launch_status": record.Status}
	if record.SessionName != "" {
		result["session_name"] = record.SessionName
		result["workspace_name"] = record.WorkspaceName
	}
	if record.SourceID != "" {
		result["source_id"] = record.SourceID
		result["source_epoch"] = record.SourceEpoch
		result["runtime_window_id"] = record.NiriWindowID
	}
	return Response{SchemaVersion: SchemaVersion, RequestID: requestID, Outcome: Outcome{Status: status, Code: code}, Result: result}
}
func validateSessionName(name string) error {
	if len(name) != 35 || !strings.HasPrefix(name, "tr-") {
		return errors.New("invalid session name")
	}
	for _, r := range name[3:] {
		if !(r >= 'a' && r <= 'z') && !(r >= '2' && r <= '7') {
			return errors.New("invalid session name")
		}
	}
	return nil
}

func validateWorkspaceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("invalid workspace name")
	}
	_, err := sliceprotocol.NormalizeWorkspaceName(name)
	return err
}
func (s Server) EnsureWorkspace(ctx context.Context, name string) (uint64, error) {
	name = strings.TrimSpace(name)
	if err := validateWorkspaceName(name); err != nil {
		return 0, err
	}
	key, err := sliceprotocol.NormalizeWorkspaceName(name)
	if err != nil {
		return 0, err
	}
	if s.Niri == nil {
		return 0, errors.New("Niri mutator unavailable")
	}
	state, err := s.Niri.Snapshot(ctx)
	if err != nil {
		return 0, err
	}
	catalog, err := inspectWorkspaceCatalog(state, name, key)
	if err != nil {
		return 0, err
	}
	if catalog.requested != nil {
		return catalog.requested.ID, nil
	}
	candidate := catalog.maxWorkspace
	if candidate.Name != nil || catalog.occupied[candidate.ID] {
		return 0, errors.New("trailing empty workspace unavailable")
	}
	action := map[string]any{"SetWorkspaceName": niriipc.SetWorkspaceNameAction{Name: name, Workspace: niriipc.WorkspaceReference{ID: candidate.ID}}}
	if err := s.Niri.Action(ctx, action); err != nil {
		return 0, err
	}
	interval := s.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
			next, err := s.Niri.Snapshot(ctx)
			if err != nil {
				continue
			}
			nextCatalog, err := inspectWorkspaceCatalog(next, name, key)
			if err != nil {
				return 0, err
			}
			if nextCatalog.activeOutput != catalog.activeOutput {
				return 0, errors.New("active output changed during workspace ensure")
			}
			current, found := nextCatalog.byID[candidate.ID]
			if !found || current.Output == nil || *current.Output != catalog.activeOutput || current.Index != candidate.Index {
				return 0, errors.New("workspace ensure candidate identity or output changed")
			}
			if current.Name == nil {
				if nextCatalog.requested != nil && nextCatalog.requested.ID != candidate.ID {
					return 0, errors.New("requested workspace appeared on a different runtime ID")
				}
				continue
			}
			if *current.Name != name || nextCatalog.requested == nil || nextCatalog.requested.ID != candidate.ID {
				return 0, errors.New("workspace ensure candidate did not retain the exact requested identity")
			}
			replacement := nextCatalog.maxWorkspace
			if replacement.ID == candidate.ID || replacement.Index <= candidate.Index || replacement.Name != nil || nextCatalog.occupied[replacement.ID] {
				continue
			}
			return candidate.ID, nil
		}
	}
}

type workspaceCatalog struct {
	activeOutput string
	byID         map[uint64]niriipc.Workspace
	occupied     map[uint64]bool
	maxWorkspace niriipc.Workspace
	requested    *niriipc.Workspace
}

func inspectWorkspaceCatalog(state niriipc.State, requestedName, requestedKey string) (workspaceCatalog, error) {
	catalog := workspaceCatalog{byID: make(map[uint64]niriipc.Workspace, len(state.Workspaces)), occupied: map[uint64]bool{}}
	if len(state.Workspaces) == 0 {
		return catalog, errors.New("workspace catalog is empty")
	}
	outputs := map[string]struct{}{}
	activeCount := 0
	indices := map[int]struct{}{}
	namedByKey := map[string]niriipc.Workspace{}
	maxIndex := -1
	for _, workspace := range state.Workspaces {
		if workspace.ID == 0 || workspace.Index <= 0 || workspace.Output == nil || *workspace.Output == "" {
			return catalog, errors.New("workspace catalog contains invalid identity or output")
		}
		if _, exists := catalog.byID[workspace.ID]; exists {
			return catalog, errors.New("workspace catalog contains a duplicate runtime ID")
		}
		if _, exists := indices[workspace.Index]; exists {
			return catalog, errors.New("workspace catalog contains a duplicate output index")
		}
		indices[workspace.Index] = struct{}{}
		catalog.byID[workspace.ID] = workspace
		outputs[*workspace.Output] = struct{}{}
		if workspace.IsActive {
			activeCount++
			catalog.activeOutput = *workspace.Output
		}
		if workspace.Index > maxIndex {
			maxIndex = workspace.Index
			catalog.maxWorkspace = workspace
		}
		if workspace.Name == nil {
			continue
		}
		key, err := sliceprotocol.NormalizeWorkspaceName(*workspace.Name)
		if err != nil {
			return catalog, errors.New("workspace catalog contains an invalid name")
		}
		if prior, exists := namedByKey[key]; exists {
			if prior.Name != nil && *prior.Name == *workspace.Name {
				return catalog, errors.New("workspace catalog contains a duplicate exact name")
			}
			return catalog, errors.New("workspace catalog contains a normalization collision")
		}
		namedByKey[key] = workspace
	}
	if len(outputs) != 1 || activeCount != 1 {
		return catalog, errors.New("MVP workspace ensure requires exactly one active output")
	}
	if _, exists := outputs[catalog.activeOutput]; !exists {
		return catalog, errors.New("active workspace output does not match the one-output topology")
	}
	for _, window := range state.Windows {
		if window.WorkspaceID == nil {
			return catalog, errors.New("window is missing its workspace during workspace ensure")
		}
		if _, exists := catalog.byID[*window.WorkspaceID]; !exists {
			return catalog, errors.New("window references an unknown workspace during workspace ensure")
		}
		catalog.occupied[*window.WorkspaceID] = true
	}
	if workspace, exists := namedByKey[requestedKey]; exists {
		if workspace.Name == nil || *workspace.Name != requestedName {
			return catalog, errors.New("workspace normalization collision")
		}
		copy := workspace
		catalog.requested = &copy
	}
	return catalog, nil
}
