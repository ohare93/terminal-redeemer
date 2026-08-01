package sliceattach

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type Status string

const (
	StatusInvalid      Status = "invalid"
	StatusUnavailable  Status = "unavailable"
	StatusSetupFailed  Status = "setup_failed"
	StatusAttachFailed Status = "attach_failed"
	StatusDetached     Status = "detached"
	StatusCancelled    Status = "cancelled"
)

type Outcome struct {
	Status Status `json:"status"`
	Code   string `json:"code,omitempty"`
}

// ExactSocketIdentity is the crash-safe identity of one private exact-session
// namespace. Marker identity proves ownership; socket identity prevents replay
// from adopting an ordinary same-name socket that was replaced.
type ExactSocketIdentity struct {
	Path         string
	MarkerDevice uint64
	MarkerInode  uint64
	SocketDevice uint64
	SocketInode  uint64
}

const rootMarkerContents = "terminal-redeemer-attach-root-v1\n"
const attachmentMarkerContents = "terminal-redeemer-attachment-v1\n"
const readyMarkerPrefix = "\x1eTERMINAL_REDEEMER_ATTACH_READY_V1:"

func ReadyMarker(token string) string { return readyMarkerPrefix + token + "\x1f" }

func exactSocketPaths(realSocketBase, privateRoot, session, token string) (string, string, string, Outcome) {
	for _, value := range []string{realSocketBase, privateRoot, session, token} {
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return "", "", "", Outcome{Status: StatusInvalid, Code: "invalid_arguments"}
		}
	}
	if !zellijlive.SafeSessionName(session) || !zellijlive.SafeSessionName(token) || !filepath.IsAbs(realSocketBase) || !filepath.IsAbs(privateRoot) {
		return "", "", "", Outcome{Status: StatusInvalid, Code: "invalid_arguments"}
	}
	attachmentRoot := filepath.Join(privateRoot, "att-"+token)
	privateSocket := filepath.Join(attachmentRoot, zellijlive.SocketContractDir, session)
	if len([]byte(privateSocket)) > zellijlive.MaxSocketPathBytes {
		return "", "", "", Outcome{Status: StatusInvalid, Code: "socket_path_too_long"}
	}
	return filepath.Join(realSocketBase, zellijlive.SocketContractDir, session), attachmentRoot, privateSocket, Outcome{}
}

// PlanExactSocket records the exact live socket inode and deterministic private
// path before the per-launch namespace is created.
func PlanExactSocket(realSocketBase, privateRoot, session, token string, uid int) (ExactSocketIdentity, Outcome) {
	real, attachmentRoot, _, outcome := exactSocketPaths(realSocketBase, privateRoot, session, token)
	if outcome.Status != "" {
		return ExactSocketIdentity{}, outcome
	}
	if err := verifySocketDirectory(realSocketBase, uid); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "unsafe_socket_directory"}
	}
	if err := verifySocketDirectory(filepath.Dir(real), uid); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "unsafe_socket_directory"}
	}
	info, err := os.Lstat(real)
	if errors.Is(err, os.ErrNotExist) {
		return ExactSocketIdentity{}, Outcome{Status: StatusUnavailable, Code: "session_unavailable"}
	}
	if err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "socket_inspection_failed"}
	}
	realStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || int(realStat.Uid) != uid {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "unsafe_session_socket"}
	}
	if err := secureAttachmentRoot(privateRoot, uid); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "private_root_failed"}
	}
	rootInfo, err := os.Lstat(privateRoot)
	rootStat, ok := infoSys(rootInfo)
	if err != nil || !ok || uint64(rootStat.Dev) != uint64(realStat.Dev) {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "cross_filesystem_private_root"}
	}
	return ExactSocketIdentity{Path: attachmentRoot, SocketDevice: uint64(realStat.Dev), SocketInode: uint64(realStat.Ino)}, Outcome{}
}

// ValidatePreparedExactSocket accepts only the journaled path, marker inode,
// and exact linked socket inode. Zero marker identity is permitted only while
// adopting a fully prepared namespace after a crash between preparation and
// its second journal write.
func ValidatePreparedExactSocket(identity ExactSocketIdentity, session string, uid int) (ExactSocketIdentity, Outcome) {
	if identity.Path == "" || !filepath.IsAbs(identity.Path) || identity.SocketDevice == 0 || identity.SocketInode == 0 || !zellijlive.SafeSessionName(session) {
		return ExactSocketIdentity{}, Outcome{Status: StatusInvalid, Code: "invalid_arguments"}
	}
	info, err := os.Lstat(identity.Path)
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || int(stat.Uid) != uid {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_directory_failed"}
	}
	entries, err := os.ReadDir(identity.Path)
	if err != nil || len(entries) != 2 || entries[0].Name() != ".owned-v1" || entries[1].Name() != zellijlive.SocketContractDir {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_directory_failed"}
	}
	markerPath := filepath.Join(identity.Path, ".owned-v1")
	if err := validateMarker(markerPath, attachmentMarkerContents, uid); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_marker_failed"}
	}
	markerInfo, err := os.Lstat(markerPath)
	markerStat, markerOK := infoSys(markerInfo)
	if err != nil || !markerOK || (identity.MarkerDevice != 0 && (identity.MarkerDevice != uint64(markerStat.Dev) || identity.MarkerInode != uint64(markerStat.Ino))) {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_marker_failed"}
	}
	contractPath := filepath.Join(identity.Path, zellijlive.SocketContractDir)
	if err := verifyPrivateDirectory(contractPath, uid); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_directory_failed"}
	}
	contractEntries, err := os.ReadDir(contractPath)
	if err != nil || len(contractEntries) != 1 || contractEntries[0].Name() != session {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "session_link_verify_failed"}
	}
	socketInfo, err := os.Lstat(filepath.Join(contractPath, session))
	socketStat, socketOK := infoSys(socketInfo)
	if err != nil || !socketOK || socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode()&os.ModeSymlink != 0 || int(socketStat.Uid) != uid || uint64(socketStat.Dev) != identity.SocketDevice || uint64(socketStat.Ino) != identity.SocketInode {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "session_link_verify_failed"}
	}
	identity.MarkerDevice = uint64(markerStat.Dev)
	identity.MarkerInode = uint64(markerStat.Ino)
	return identity, Outcome{}
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func verifyPrivateDirectory(path string, uid int) error {
	info, err := os.Lstat(path)
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || int(stat.Uid) != uid {
		return errors.New("unsafe private directory")
	}
	return nil
}

// PreparePlannedExactSocket creates or crash-adopts only the planned inode.
func PreparePlannedExactSocket(realSocketBase, privateRoot, session, token string, uid int, planned ExactSocketIdentity) (ExactSocketIdentity, Outcome) {
	real, attachmentRoot, privateSocket, outcome := exactSocketPaths(realSocketBase, privateRoot, session, token)
	if outcome.Status != "" || planned.Path != attachmentRoot || planned.SocketDevice == 0 || planned.SocketInode == 0 {
		if outcome.Status != "" {
			return ExactSocketIdentity{}, outcome
		}
		return ExactSocketIdentity{}, Outcome{Status: StatusInvalid, Code: "invalid_arguments"}
	}
	if _, err := os.Lstat(attachmentRoot); err == nil {
		return ValidatePreparedExactSocket(planned, session, uid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_directory_failed"}
	}
	current, planOutcome := PlanExactSocket(realSocketBase, privateRoot, session, token, uid)
	if planOutcome.Status != "" {
		return ExactSocketIdentity{}, planOutcome
	}
	if current.Path != planned.Path || current.SocketDevice != planned.SocketDevice || current.SocketInode != planned.SocketInode {
		return ExactSocketIdentity{}, Outcome{Status: StatusUnavailable, Code: "session_identity_changed"}
	}
	if err := os.Mkdir(attachmentRoot, 0o700); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_directory_failed"}
	}
	okSetup := false
	defer func() {
		if !okSetup {
			_ = os.RemoveAll(attachmentRoot)
		}
	}()
	if err := createMarker(filepath.Join(attachmentRoot, ".owned-v1"), attachmentMarkerContents, attachmentRoot, uid); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_marker_failed"}
	}
	if err := os.Mkdir(filepath.Dir(privateSocket), 0o700); err != nil {
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "attachment_directory_failed"}
	}
	if err := os.Link(real, privateSocket); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ExactSocketIdentity{}, Outcome{Status: StatusUnavailable, Code: "session_link_failed"}
		}
		return ExactSocketIdentity{}, Outcome{Status: StatusSetupFailed, Code: "session_link_failed"}
	}
	prepared, verifyOutcome := ValidatePreparedExactSocket(planned, session, uid)
	if verifyOutcome.Status != "" {
		return ExactSocketIdentity{}, verifyOutcome
	}
	okSetup = true
	return prepared, Outcome{}
}

// RemovePreparedExactSocket removes only a marker/inode identity that still
// exactly matches the journal.
func RemovePreparedExactSocket(identity ExactSocketIdentity, session string, uid int) Outcome {
	if identity.Path == "" || !filepath.IsAbs(identity.Path) || identity.MarkerDevice == 0 || identity.MarkerInode == 0 || identity.SocketDevice == 0 || identity.SocketInode == 0 || !zellijlive.SafeSessionName(session) {
		return Outcome{Status: StatusInvalid, Code: "invalid_arguments"}
	}
	if _, err := os.Lstat(identity.Path); errors.Is(err, os.ErrNotExist) {
		// Checked lifecycle cleanup is idempotent if a caller observes completion
		// after removal. Absence never authorizes discovery of a replacement.
		return Outcome{}
	}
	if _, outcome := ValidatePreparedExactSocket(identity, session, uid); outcome.Status != "" {
		return outcome
	}
	if err := os.RemoveAll(identity.Path); err != nil {
		return Outcome{Status: StatusSetupFailed, Code: "attachment_cleanup_failed"}
	}
	return Outcome{}
}

// PrepareExactSocket creates an owner-only socket namespace containing only a
// hard link to the requested live Zellij session. The caller removes it only
// after positive client attachment or a definite pre-start failure.
func PrepareExactSocket(realSocketBase, privateRoot, session, token string, uid int) (string, Outcome) {
	planned, outcome := PlanExactSocket(realSocketBase, privateRoot, session, token, uid)
	if outcome.Status != "" {
		return "", outcome
	}
	prepared, outcome := PreparePlannedExactSocket(realSocketBase, privateRoot, session, token, uid, planned)
	return prepared.Path, outcome
}

// PreparedWrapper owns an already journaled exact namespace for the complete
// lifetime of one host-side Zellij client. It never discovers or prepares a
// same-name socket: the marker and socket inode supplied by the journal must
// still match before the pinned attach command is started.
type PreparedWrapper struct {
	Command   string
	Session   string
	Identity  ExactSocketIdentity
	ShimCache string
	UID       int
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Run       func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error
	Version   func(context.Context, string) error
}

func (w PreparedWrapper) Attach(ctx context.Context) Outcome {
	if w.Command == "" || strings.ContainsAny(w.Command, "\x00\r\n") || !zellijlive.SafeSessionName(w.Session) || !filepath.IsAbs(w.ShimCache) || len(w.ShimCache) > 4096 || strings.ContainsAny(w.ShimCache, "\x00\r\n") {
		return Outcome{Status: StatusInvalid, Code: "invalid_arguments"}
	}
	uid := w.UID
	if uid == 0 {
		uid = os.Getuid()
	}
	identity, outcome := ValidatePreparedExactSocket(w.Identity, w.Session, uid)
	if outcome.Status != "" {
		return outcome
	}
	verify := w.Version
	if verify == nil {
		verify = verifyVersion
	}
	if err := verify(ctx, w.Command); err != nil {
		if cleanup := RemovePreparedExactSocket(identity, w.Session, uid); cleanup.Status != "" {
			return cleanup
		}
		return Outcome{Status: StatusInvalid, Code: "unsupported_zellij_version"}
	}
	if err := ensureEmptyCache(w.ShimCache, uid); err != nil {
		if cleanup := RemovePreparedExactSocket(identity, w.Session, uid); cleanup.Status != "" {
			return cleanup
		}
		return Outcome{Status: StatusSetupFailed, Code: "shim_cache_failed"}
	}
	env := scrub(os.Environ())
	env = append(env, "ZELLIJ_SOCKET_DIR="+identity.Path, "XDG_CACHE_HOME="+w.ShimCache)
	args := []string{"attach", w.Session, "options", "--on-force-close", "detach"}
	stdin, stdout, stderr := w.Stdin, w.Stdout, w.Stderr
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	var err error
	if w.Run != nil {
		err = w.Run(ctx, w.Command, args, env, stdin, stdout, stderr)
	} else {
		err = runCommand(ctx, w.Command, args, env, stdin, stdout, stderr)
	}
	// The helper, rather than source-shaped /proc evidence in the RPC server,
	// owns cleanup. This boundary cannot precede the client's first lookup: it
	// is reached only after the attached client process has exited.
	if cleanup := RemovePreparedExactSocket(identity, w.Session, uid); cleanup.Status != "" {
		return cleanup
	}
	if ctx.Err() != nil {
		return Outcome{Status: StatusCancelled, Code: "controller_cancelled"}
	}
	if err != nil {
		return Outcome{Status: StatusAttachFailed, Code: "isolated_attach_failed"}
	}
	return Outcome{Status: StatusDetached}
}

type Wrapper struct {
	Command                string
	Session                string
	Token                  string
	RealSocketBase         string
	PrivateRoot            string
	ShimCache              string
	UID                    int
	Now                    func() time.Time
	GCMaxAge               time.Duration
	MinInteractiveLifetime time.Duration
	Stdin                  io.Reader
	Stdout                 io.Writer
	Stderr                 io.Writer
	Run                    func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error
	ReadyToken             string
	ReadyWriter            io.Writer
	Version                func(context.Context, string) error
	Probe                  func(context.Context, string) error
}

func (w Wrapper) Attach(ctx context.Context) Outcome {
	if err := w.validate(); err != nil {
		code := "invalid_arguments"
		if strings.Contains(err.Error(), "socket path") {
			code = "socket_path_too_long"
		}
		return Outcome{Status: StatusInvalid, Code: code}
	}
	verify := w.Version
	if verify == nil {
		verify = verifyVersion
	}
	if err := verify(ctx, w.Command); err != nil {
		return Outcome{Status: StatusInvalid, Code: "unsupported_zellij_version"}
	}
	uid := w.UID
	if uid == 0 {
		uid = os.Getuid()
	}
	w.gcExisting(uid)
	w.gc(uid)
	attachmentRoot, setup := PrepareExactSocket(w.RealSocketBase, w.PrivateRoot, w.Session, w.Token, uid)
	if setup.Status != "" {
		return setup
	}
	defer os.RemoveAll(attachmentRoot)
	real := filepath.Join(w.RealSocketBase, zellijlive.SocketContractDir, w.Session)
	if err := ensureEmptyCache(w.ShimCache, uid); err != nil {
		return Outcome{Status: StatusSetupFailed, Code: "shim_cache_failed"}
	}
	env := scrub(os.Environ())
	env = append(env, "ZELLIJ_SOCKET_DIR="+attachmentRoot, "XDG_CACHE_HOME="+w.ShimCache)
	args := []string{"attach", w.Session, "options", "--on-force-close", "detach"}
	run := w.Run
	stdin, stdout, stderr := w.Stdin, w.Stdout, w.Stderr
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	ready := func() error {
		if w.ReadyToken == "" {
			return nil
		}
		if !zellijlive.SafeSessionName(w.ReadyToken) {
			return errors.New("invalid readiness token")
		}
		writer := w.ReadyWriter
		if writer == nil {
			writer = stdout
		}
		_, writeErr := io.WriteString(writer, ReadyMarker(w.ReadyToken))
		return writeErr
	}
	minimum := w.MinInteractiveLifetime
	if minimum == 0 {
		minimum = 250 * time.Millisecond
	}
	started := time.Now()
	var err error
	if w.ReadyToken != "" {
		confirmation := minimum
		if confirmation <= 0 {
			confirmation = 250 * time.Millisecond
		}
		if run != nil {
			err = runWithConfirmation(ctx, confirmation, func(runCtx context.Context) error {
				return run(runCtx, w.Command, args, env, stdin, stdout, stderr)
			}, ready)
		} else {
			err = runCommandReady(ctx, w.Command, args, env, stdin, stdout, stderr, confirmation, ready)
		}
	} else if run != nil {
		err = run(ctx, w.Command, args, env, stdin, stdout, stderr)
	} else {
		err = runCommand(ctx, w.Command, args, env, stdin, stdout, stderr)
	}
	elapsed := time.Since(started)
	if ctx.Err() != nil {
		return Outcome{Status: StatusCancelled, Code: "controller_cancelled"}
	}
	if err != nil {
		probe := w.Probe
		if probe == nil {
			probe = probeSocket
		}
		if probe(ctx, real) != nil {
			return Outcome{Status: StatusUnavailable, Code: "session_unavailable"}
		}
		return Outcome{Status: StatusAttachFailed, Code: "isolated_attach_failed"}
	}
	if minimum > 0 && elapsed < minimum {
		return Outcome{Status: StatusAttachFailed, Code: "interactive_lifetime_unconfirmed"}
	}
	return Outcome{Status: StatusDetached}
}
func (w Wrapper) validate() error {
	for _, value := range []string{w.Command, w.Session, w.Token, w.RealSocketBase, w.PrivateRoot, w.ShimCache} {
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("required bounded arguments")
		}
	}
	if !zellijlive.SafeSessionName(w.Session) || !zellijlive.SafeSessionName(w.Token) || (w.ReadyToken != "" && !zellijlive.SafeSessionName(w.ReadyToken)) {
		return errors.New("unsafe session or token")
	}
	if len(filepath.Join(w.PrivateRoot, "att-"+w.Token, zellijlive.SocketContractDir, w.Session)) > zellijlive.MaxSocketPathBytes {
		return errors.New("socket path exceeds budget")
	}
	for _, path := range []string{w.RealSocketBase, w.PrivateRoot, w.ShimCache} {
		if !filepath.IsAbs(path) || len(path) > 4096 {
			return errors.New("paths must be bounded and absolute")
		}
	}
	return nil
}
func verifyVersion(ctx context.Context, command string) error {
	var output attachOutput
	cmd := exec.CommandContext(ctx, command, "--version")
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil || strings.TrimSpace(output.String()) != "zellij "+zellijlive.PinnedVersion {
		return errors.New("unsupported Zellij version")
	}
	return nil
}

type attachOutput struct{ bytes.Buffer }

func (output *attachOutput) Write(payload []byte) (int, error) {
	if output.Len()+len(payload) > 4096 {
		return 0, errors.New("version output exceeds bound")
	}
	return output.Buffer.Write(payload)
}

func runCommand(ctx context.Context, command string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
func runWithConfirmation(ctx context.Context, interval time.Duration, run func(context.Context) error, ready func() error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(runCtx) }()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		cancel()
		_ = <-done
		return ctx.Err()
	case <-timer.C:
		select {
		case err := <-done:
			return err
		default:
		}
		if err := ready(); err != nil {
			cancel()
			_ = <-done
			return err
		}
		return <-done
	}
}
func runCommandReady(ctx context.Context, command string, args, env []string, stdin io.Reader, stdout, stderr io.Writer, interval time.Duration, ready func() error) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return <-done
	case <-timer.C:
		select {
		case err := <-done:
			return err
		default:
		}
		if err := ready(); err != nil {
			_ = cmd.Process.Kill()
			_ = <-done
			return err
		}
		return <-done
	}
}
func probeSocket(ctx context.Context, path string) error {
	conn, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	return conn.Close()
}
func validateAttachmentRoot(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || int(stat.Uid) != uid {
		return errors.New("unsafe attachment root")
	}
	return validateMarker(filepath.Join(path, ".owned-v1"), rootMarkerContents, uid)
}
func secureAttachmentRoot(path string, uid int) error {
	_, err := os.Lstat(path)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		created = true
	} else if err != nil {
		return err
	}
	marker := filepath.Join(path, ".owned-v1")
	if created {
		if err := createMarker(marker, rootMarkerContents, path, uid); err != nil {
			return err
		}
	}
	return validateAttachmentRoot(path, uid)
}
func createMarker(path, contents, dir string, uid int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := validateMarker(path, contents, uid); err != nil {
		return err
	}
	ok = true
	return nil
}
func validateMarker(path, contents string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || int(stat.Uid) != uid {
		return errors.New("unsafe ownership marker")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(payload) != contents {
		return errors.New("invalid ownership marker")
	}
	return nil
}

func secureDirectory(path string, uid int) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != uid {
		return errors.New("unsafe directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o700 {
		return errors.New("unsafe directory mode")
	}
	return nil
}
func verifySocketDirectory(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || int(stat.Uid) != uid {
		return errors.New("unsafe socket directory")
	}
	return nil
}
func secureChild(parent, name string, uid int) (string, error) {
	path := filepath.Join(parent, name)
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != uid {
		return "", errors.New("unsafe cache directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}
func ensureEmptyCache(root string, uid int) error {
	if err := secureDirectory(root, uid); err != nil {
		return err
	}
	zellij, err := secureChild(root, "zellij", uid)
	if err != nil {
		return err
	}
	contract, err := secureChild(zellij, zellijlive.SocketContractDir, uid)
	if err != nil {
		return err
	}
	path, err := secureChild(contract, "session_info", uid)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("shim resurrection cache is not empty")
	}
	return nil
}
func scrub(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.SplitN(value, "=", 2)[0]
		switch key {
		case "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_PANE_ID", "ZELLIJ_TAB_INDEX", "ZELLIJ_TAB_NAME", "ZELLIJ_SOCKET_DIR", "XDG_CACHE_HOME":
			continue
		}
		out = append(out, value)
	}
	return out
}
func (w Wrapper) gcExisting(uid int) {
	if validateAttachmentRoot(w.PrivateRoot, uid) == nil {
		w.gc(uid)
	}
}
func (w Wrapper) gc(uid int) {
	entries, err := os.ReadDir(w.PrivateRoot)
	if err != nil {
		return
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	age := w.GCMaxAge
	if age <= 0 {
		age = 24 * time.Hour
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "att-") || strings.Contains(entry.Name(), string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(w.PrivateRoot, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || now.Sub(info.ModTime()) < age {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && int(stat.Uid) == uid && validateMarker(filepath.Join(path, ".owned-v1"), attachmentMarkerContents, uid) == nil {
			_ = os.RemoveAll(path)
		}
	}
}
func ExitCode(outcome Outcome) int {
	switch outcome.Status {
	case StatusDetached:
		return 0
	case StatusInvalid:
		return 3
	case StatusUnavailable:
		return 4
	case StatusSetupFailed:
		return 5
	case StatusAttachFailed:
		return 6
	case StatusCancelled:
		return 130
	default:
		return 1
	}
}
