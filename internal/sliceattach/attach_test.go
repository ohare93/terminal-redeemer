package sliceattach

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

func shortLiveSocket(t *testing.T, session string) (string, net.Listener) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "tr-a-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	base := filepath.Join(root, "z")
	version := filepath.Join(base, zellijlive.SocketContractDir)
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(version, session))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return base, listener
}

func liveSocket(t *testing.T, session string) (string, net.Listener) {
	t.Helper()
	return shortLiveSocket(t, session)
}
func TestExactAttachHardLinksScrubsAndDetaches(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	private := filepath.Join(base, "private")
	cache := filepath.Join(base, "cache")
	t.Setenv("ZELLIJ", "1")
	t.Setenv("ZELLIJ_SESSION_NAME", "other")
	var gotArgs, gotEnv []string
	input := strings.NewReader("input")
	var output, errorsOutput bytes.Buffer
	wrapper := Wrapper{Command: "/nix/store/zellij", Session: "exact", Token: "token-1", RealSocketBase: base, PrivateRoot: private, ShimCache: cache, MinInteractiveLifetime: -1, Stdin: input, Stdout: &output, Stderr: &errorsOutput, Version: func(context.Context, string) error { return nil }, Run: func(_ context.Context, command string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if stdin != input || stdout != &output || stderr != &errorsOutput {
			t.Fatal("attachment I/O seam did not preserve caller streams")
		}
		if command != "/nix/store/zellij" {
			t.Fatal(command)
		}
		gotArgs = append([]string(nil), args...)
		gotEnv = append([]string(nil), env...)
		linked := filepath.Join(private, "att-token-1", zellijlive.SocketContractDir, "exact")
		real := filepath.Join(base, zellijlive.SocketContractDir, "exact")
		a, _ := os.Stat(linked)
		b, _ := os.Stat(real)
		if !os.SameFile(a, b) {
			t.Fatal("private entry is not exact hard link")
		}
		return nil
	}}
	outcome := wrapper.Attach(context.Background())
	if outcome.Status != StatusDetached {
		t.Fatalf("outcome=%#v", outcome)
	}
	want := []string{"attach", "exact", "options", "--on-force-close", "detach"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args=%q", gotArgs)
	}
	joined := strings.Join(gotEnv, "\n")
	if strings.Contains(joined, "ZELLIJ=1") || strings.Contains(joined, "ZELLIJ_SESSION_NAME=") {
		t.Fatalf("nested variables leaked: %q", gotEnv)
	}
	if !strings.Contains(joined, "ZELLIJ_SOCKET_DIR="+filepath.Join(private, "att-token-1")) || !strings.Contains(joined, "XDG_CACHE_HOME="+cache) {
		t.Fatalf("isolated env missing: %q", gotEnv)
	}
	if _, err := os.Stat(filepath.Join(private, "att-token-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attachment directory not cleaned: %v", err)
	}
}
func TestPreparedExactSocketReplayRetainsJournaledInodeAcrossSameNameReplacement(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "tr-att-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	base := filepath.Join(root, "z")
	if err := os.MkdirAll(filepath.Join(base, zellijlive.SocketContractDir), 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := net.Listen("unix", filepath.Join(base, zellijlive.SocketContractDir, "exact"))
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(base, "private")
	planned, outcome := PlanExactSocket(base, private, "exact", "token", os.Getuid())
	if outcome.Status != "" {
		t.Fatalf("plan=%#v", outcome)
	}
	prepared, outcome := PreparePlannedExactSocket(base, private, "exact", "token", os.Getuid(), planned)
	if outcome.Status != "" || prepared.MarkerInode == 0 {
		t.Fatalf("prepared=%+v outcome=%#v", prepared, outcome)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(base, zellijlive.SocketContractDir, "exact")
	if err := os.Remove(realPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", realPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	replacementInfo, err := os.Lstat(filepath.Join(base, zellijlive.SocketContractDir, "exact"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(replacementInfo, mustStat(t, filepath.Join(prepared.Path, zellijlive.SocketContractDir, "exact"))) {
		t.Fatal("test replacement unexpectedly reused the prepared inode")
	}
	adopted, outcome := PreparePlannedExactSocket(base, private, "exact", "token", os.Getuid(), prepared)
	if outcome.Status != "" || adopted != prepared {
		t.Fatalf("replay adopted=%+v want=%+v outcome=%#v", adopted, prepared, outcome)
	}
	if outcome := RemovePreparedExactSocket(prepared, "exact", os.Getuid()); outcome.Status != "" {
		t.Fatalf("cleanup=%#v", outcome)
	}
	conn, err := net.DialTimeout("unix", filepath.Join(base, zellijlive.SocketContractDir, "exact"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("cleanup destroyed replacement host socket: %v", err)
	}
	_ = conn.Close()
}

func TestPreparedWrapperOwnsNamespaceUntilPinnedClientExit(t *testing.T) {
	base, _ := shortLiveSocket(t, "exact")
	private := filepath.Join(base, "p")
	planned, outcome := PlanExactSocket(base, private, "exact", "h", os.Getuid())
	if outcome.Status != "" {
		t.Fatal(outcome)
	}
	prepared, outcome := PreparePlannedExactSocket(base, private, "exact", "h", os.Getuid(), planned)
	if outcome.Status != "" {
		t.Fatal(outcome)
	}
	started := make(chan struct{})
	exit := make(chan struct{})
	done := make(chan Outcome, 1)
	var gotArgs, gotEnv []string
	go func() {
		done <- (PreparedWrapper{
			Command: "/store/zellij", Session: "exact", Identity: prepared, ShimCache: filepath.Join(base, "host-cache"),
			Version: func(context.Context, string) error { return nil },
			Run: func(_ context.Context, _ string, args, env []string, _ io.Reader, _, _ io.Writer) error {
				gotArgs = append([]string(nil), args...)
				gotEnv = append([]string(nil), env...)
				close(started)
				<-exit
				return nil
			},
		}).Attach(context.Background())
	}()
	<-started
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("namespace disappeared while client runs: %v", err)
	}
	close(exit)
	if out := <-done; out.Status != StatusDetached {
		t.Fatalf("outcome=%+v", out)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("namespace survived client exit: %v", err)
	}
	want := []string{"attach", "exact", "options", "--on-force-close", "detach"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("argv=%q", gotArgs)
	}
	joined := strings.Join(gotEnv, "\n")
	if !strings.Contains(joined, "ZELLIJ_SOCKET_DIR="+prepared.Path) || !strings.Contains(joined, "XDG_CACHE_HOME="+filepath.Join(base, "host-cache")) {
		t.Fatalf("isolated env=%q", gotEnv)
	}
	conn, err := net.DialTimeout("unix", filepath.Join(base, zellijlive.SocketContractDir, "exact"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("helper cleanup destroyed host session: %v", err)
	}
	_ = conn.Close()
}

func TestPreparedWrapperRefusesJournalIdentityMismatchWithoutStarting(t *testing.T) {
	base, _ := shortLiveSocket(t, "exact")
	private := filepath.Join(base, "p")
	planned, outcome := PlanExactSocket(base, private, "exact", "h", os.Getuid())
	if outcome.Status != "" {
		t.Fatal(outcome)
	}
	prepared, outcome := PreparePlannedExactSocket(base, private, "exact", "h", os.Getuid(), planned)
	if outcome.Status != "" {
		t.Fatal(outcome)
	}
	prepared.SocketInode++
	started := false
	out := (PreparedWrapper{Command: "/store/zellij", Session: "exact", Identity: prepared, ShimCache: filepath.Join(base, "cache"), Version: func(context.Context, string) error { return nil }, Run: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		started = true
		return nil
	}}).Attach(context.Background())
	if out.Status != StatusSetupFailed || started {
		t.Fatalf("outcome=%+v started=%v", out, started)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("mismatched identity was cleaned: %v", err)
	}
}

func TestPreparedExactSocketCleanupRefusesReplacedMarker(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	private := filepath.Join(base, "private")
	planned, outcome := PlanExactSocket(base, private, "exact", "token", os.Getuid())
	if outcome.Status != "" {
		t.Fatal(outcome)
	}
	prepared, outcome := PreparePlannedExactSocket(base, private, "exact", "token", os.Getuid(), planned)
	if outcome.Status != "" {
		t.Fatal(outcome)
	}
	marker := filepath.Join(prepared.Path, ".owned-v1")
	oldMarker := filepath.Join(prepared.Path, ".owned-old")
	if err := os.Rename(marker, oldMarker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(attachmentMarkerContents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldMarker); err != nil {
		t.Fatal(err)
	}
	if outcome := RemovePreparedExactSocket(prepared, "exact", os.Getuid()); outcome.Status == "" {
		t.Fatal("cleanup accepted a replacement ownership marker")
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("refused namespace was removed: %v", err)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestReadinessMarkerRequiresBoundedInteractiveSurvival(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	var output bytes.Buffer
	wrapper := Wrapper{Command: "zellij", Session: "exact", Token: "token", ReadyToken: "ready_nonce", ReadyWriter: &output, RealSocketBase: base, PrivateRoot: filepath.Join(base, "private"), ShimCache: filepath.Join(base, "cache"), MinInteractiveLifetime: 20 * time.Millisecond, Version: func(context.Context, string) error { return nil }, Run: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		time.Sleep(40 * time.Millisecond)
		return nil
	}}
	out := wrapper.Attach(context.Background())
	if out.Status != StatusDetached || output.String() != ReadyMarker("ready_nonce") {
		t.Fatalf("out=%#v marker=%q", out, output.String())
	}
	for name, run := range map[string]func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error{
		"failed_start": func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			return errors.New("start failed")
		},
		"immediate_session_death": func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			output.Reset()
			failed := wrapper
			failed.Token = name
			failed.Run = run
			result := failed.Attach(context.Background())
			if output.Len() != 0 || result.Status == StatusDetached {
				t.Fatalf("immediate exit emitted readiness: outcome=%#v marker=%q", result, output.String())
			}
		})
	}
}

func TestAttachTypedFailureExits(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	common := Wrapper{Command: "zellij", Session: "exact", Token: "token", RealSocketBase: base, PrivateRoot: filepath.Join(base, "private"), ShimCache: filepath.Join(base, "cache"), Version: func(context.Context, string) error { return nil }}
	missing := common
	missing.Session = "missing"
	if out := missing.Attach(context.Background()); out.Status != StatusUnavailable || ExitCode(out) != 4 {
		t.Fatalf("missing=%#v", out)
	}
	early := common
	early.Token = "early"
	early.MinInteractiveLifetime = time.Second
	early.Run = func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil }
	if out := early.Attach(context.Background()); out.Status != StatusAttachFailed || out.Code != "interactive_lifetime_unconfirmed" {
		t.Fatalf("early=%#v", out)
	}
	failed := common
	failed.Run = func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("exit")
	}
	if out := failed.Attach(context.Background()); out.Status != StatusAttachFailed || ExitCode(out) != 6 {
		t.Fatalf("failed=%#v", out)
	}
	cancelled := common
	cancelled.Token = "cancel"
	cancelled.Run = func(ctx context.Context, _ string, _ []string, _ []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := cancelled.Attach(ctx); out.Status != StatusCancelled || ExitCode(out) != 130 {
		t.Fatalf("cancelled=%#v", out)
	}
}
func TestAttachRejectsPathBudgetAndNonemptyCache(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	longRoot := filepath.Join(base, strings.Repeat("p", 80))
	wrapper := Wrapper{Command: "zellij", Session: "exact", Token: "token", RealSocketBase: base, PrivateRoot: longRoot, ShimCache: filepath.Join(base, "cache"), Version: func(context.Context, string) error { return nil }}
	if out := wrapper.Attach(context.Background()); out.Status != StatusInvalid || out.Code != "socket_path_too_long" {
		t.Fatalf("outcome=%#v", out)
	}
	base2, _ := liveSocket(t, "exact")
	cache := filepath.Join(base2, "cache")
	dead := filepath.Join(cache, "zellij", zellijlive.SocketContractDir, "session_info")
	if err := os.MkdirAll(dead, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dead, "resurrection"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper = Wrapper{Command: "zellij", Session: "exact", Token: "token", RealSocketBase: base2, PrivateRoot: filepath.Join(base2, "private"), ShimCache: cache, Version: func(context.Context, string) error { return nil }}
	if out := wrapper.Attach(context.Background()); out.Status != StatusSetupFailed || out.Code != "shim_cache_failed" {
		t.Fatalf("outcome=%#v", out)
	}
}
func TestProductionRunnerWiresCallerStreams(t *testing.T) {
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.NewBufferString("through-stdin")
	var stdout, stderr bytes.Buffer
	env := append(os.Environ(), "GO_WANT_ATTACH_IO_HELPER=1")
	if err := runCommand(context.Background(), command, []string{"-test.run=TestAttachIOHelperProcess"}, env, input, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "through-stdin" || stderr.String() != "helper-stderr" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
func TestProductionReadyRunnerRejectsImmediateExitAndConfirmsSurvival(t *testing.T) {
	command, _ := exec.LookPath("true")
	immediate := false
	if err := runCommandReady(context.Background(), command, nil, os.Environ(), nil, io.Discard, io.Discard, 250*time.Millisecond, func() error { immediate = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if immediate {
		t.Fatal("immediate post-start exit emitted readiness")
	}
	confirmed := false
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	command, _ = os.Executable()
	env := append(os.Environ(), "GO_WANT_ATTACH_IO_HELPER=block")
	_ = runCommandReady(ctx, command, []string{"-test.run=TestAttachIOHelperProcess"}, env, nil, io.Discard, io.Discard, 10*time.Millisecond, func() error { confirmed = true; return nil })
	if !confirmed {
		t.Fatal("surviving exact client did not emit readiness after confirmation interval")
	}
}

func TestProductionRunnerCancellationReachesChild(t *testing.T) {
	command, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	env := append(os.Environ(), "GO_WANT_ATTACH_IO_HELPER=block")
	if err := runCommand(ctx, command, []string{"-test.run=TestAttachIOHelperProcess"}, env, nil, io.Discard, io.Discard); err == nil || time.Since(started) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
}
func TestAttachIOHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_ATTACH_IO_HELPER")
	if mode == "" {
		return
	}
	if mode == "block" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	_, _ = io.Copy(os.Stdout, os.Stdin)
	_, _ = fmt.Fprint(os.Stderr, "helper-stderr")
	os.Exit(0)
}

func TestAttachMapsProvablyStaleSocketToUnavailable(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	base := filepath.Join(root, "z")
	version := filepath.Join(base, zellijlive.SocketContractDir)
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(version, "stale")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	wrapper := Wrapper{Command: "zellij", Session: "stale", Token: "token", RealSocketBase: base, PrivateRoot: filepath.Join(base, "private"), ShimCache: filepath.Join(base, "cache"), Version: func(context.Context, string) error { return nil }, Run: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("client handshake failed")
	}}
	out := wrapper.Attach(context.Background())
	if out.Status != StatusUnavailable || out.Code != "session_unavailable" {
		t.Fatalf("outcome=%#v", out)
	}
}

func TestAttachRejectsSymlinkedOrUnmarkedPrivateRootWithoutGC(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	short, err := os.MkdirTemp("/tmp", "tr-att-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	target := filepath.Join(short, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(target, "att-project")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(unrelated, past, past)
	link := filepath.Join(short, "private")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	wrapper := Wrapper{Command: "zellij", Session: "exact", Token: "token", RealSocketBase: base, PrivateRoot: link, ShimCache: filepath.Join(base, "cache"), Version: func(context.Context, string) error { return nil }}
	out := wrapper.Attach(context.Background())
	if out.Status != StatusSetupFailed || out.Code != "private_root_failed" {
		t.Fatalf("outcome=%#v", out)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated directory deleted: %v", err)
	}
}

func TestAttachGarbageCollectsOnlyOldOwnedPrefixDirectories(t *testing.T) {
	base, _ := liveSocket(t, "exact")
	private := filepath.Join(base, "private")
	if err := secureAttachmentRoot(private, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(private, "att-old")
	unmarked := filepath.Join(private, "att-project")
	keep := filepath.Join(private, "unrelated")
	_ = os.Mkdir(old, 0o700)
	_ = os.Mkdir(unmarked, 0o700)
	_ = os.Mkdir(keep, 0o700)
	if err := createMarker(filepath.Join(old, ".owned-v1"), attachmentMarkerContents, old, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(unmarked, past, past)
	wrapper := Wrapper{Command: "zellij", Session: "exact", Token: "new", RealSocketBase: base, PrivateRoot: private, ShimCache: filepath.Join(base, "cache"), GCMaxAge: time.Hour, MinInteractiveLifetime: -1, Version: func(context.Context, string) error { return nil }, Run: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil }}
	if out := wrapper.Attach(context.Background()); out.Status != StatusDetached {
		t.Fatalf("outcome=%#v", out)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old not collected: %v", err)
	}
	if _, err := os.Stat(unmarked); err != nil {
		t.Fatalf("unmarked att-* removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated removed: %v", err)
	}
}
