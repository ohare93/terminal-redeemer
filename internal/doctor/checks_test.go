package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/config"
)

type staticCheck struct {
	name   string
	result Result
}

func (c staticCheck) Name() string { return c.name }
func (c staticCheck) Run(_ context.Context) Result {
	return c.result
}

func TestRunSummaryAndFailureDetection(t *testing.T) {
	t.Parallel()

	results := Run(context.Background(), []Check{
		staticCheck{name: "a", result: Result{Name: "a", Status: StatusPass, Detail: "ok"}},
		staticCheck{name: "b", result: Result{Name: "b", Status: StatusFail, Detail: "nope"}},
	})

	summary := Summarize(results)
	if summary.Total != 2 || summary.Passed != 1 || summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if !HasFailures(results) {
		t.Fatal("expected failures")
	}
}

func TestStatePathsCheckIsReadOnly(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "missing-state")
	result := StatePathsCheck{StateDir: stateDir}.Run(context.Background())
	if result.Status != StatusPass || !strings.Contains(result.Detail, "checkpoints") {
		t.Fatalf("expected absent checkpoint path to pass with guidance, got %+v", result)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("doctor state path check mutated the filesystem: %v", err)
	}
}

func TestBootIDCheck(t *testing.T) {
	t.Parallel()

	pass := BootIDCheck{Current: func() (string, error) { return "boot-a", nil }}.Run(context.Background())
	if pass.Status != StatusPass {
		t.Fatalf("expected pass, got %+v", pass)
	}
	fail := BootIDCheck{Current: func() (string, error) { return "", errors.New("no proc") }}.Run(context.Background())
	if fail.Status != StatusFail || !strings.Contains(fail.Detail, "resume cannot select") {
		t.Fatalf("expected actionable failure, got %+v", fail)
	}
}

func TestNiriReadinessCheckOfflineAndLive(t *testing.T) {
	t.Parallel()

	fixture := filepath.Join(t.TempDir(), "niri.json")
	if err := os.WriteFile(fixture, []byte(`{"workspaces":[],"windows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	offline := NiriReadinessCheck{FixturePath: fixture}.Run(context.Background())
	if offline.Status != StatusPass || !strings.Contains(offline.Detail, "live Niri IPC bypassed") {
		t.Fatalf("unexpected offline result: %+v", offline)
	}

	unset := NiriReadinessCheck{Command: "niri msg -j windows"}.Run(context.Background())
	if unset.Status != StatusFail || !strings.Contains(unset.Detail, "NIRI_SOCKET") {
		t.Fatalf("expected socket guidance, got %+v", unset)
	}

	live := NiriReadinessCheck{
		Command: "niri msg -j windows", Socket: "/run/user/1000/niri.sock",
		LookPath: func(string) (string, error) { return "/bin/niri", nil },
		Snapshot: func(context.Context) ([]byte, error) { return []byte(`{"workspaces":[],"windows":[]}`), nil },
	}.Run(context.Background())
	if live.Status != StatusPass {
		t.Fatalf("expected live query pass, got %+v", live)
	}
}

func TestResumeLauncherAndZellijListingChecks(t *testing.T) {
	t.Parallel()

	launcher := ResumeLauncherCheck{Command: "kitty", LookPath: func(file string) (string, error) {
		if file != "kitty" {
			t.Fatalf("unexpected launcher lookup %q", file)
		}
		return "/bin/kitty", nil
	}}.Run(context.Background())
	if launcher.Status != StatusPass || !strings.Contains(launcher.Detail, "client_pid") {
		t.Fatalf("unexpected launcher result: %+v", launcher)
	}

	listing := ZellijListingCheck{
		LookPath: func(string) (string, error) { return "/bin/zellij", nil },
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "zellij" || strings.Join(args, " ") != "list-sessions --short" {
				t.Fatalf("unexpected command")
			}
			return []byte("one\ntwo\n"), nil
		},
	}.Run(context.Background())
	if listing.Status != StatusPass || !strings.Contains(listing.Detail, "sessions=2") {
		t.Fatalf("unexpected listing result: %+v", listing)
	}
}

func TestStartupServiceCheckOptionality(t *testing.T) {
	t.Parallel()

	called := false
	disabled := StartupServiceCheck{Enabled: false, RunCommand: func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}.Run(context.Background())
	if disabled.Status != StatusPass || called {
		t.Fatalf("disabled startup must be an optional pass without systemctl: %+v called=%t", disabled, called)
	}

	enabled := StartupServiceCheck{Enabled: true, RunCommand: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("disabled"), errors.New("exit 1")
	}}.Run(context.Background())
	if enabled.Status != StatusFail || !strings.Contains(enabled.Detail, "journalctl") {
		t.Fatalf("expected enabled service guidance, got %+v", enabled)
	}
}

func TestConfigLoadCheck(t *testing.T) {
	t.Parallel()

	pass := ConfigLoadCheck{Load: func(string, bool) (config.Config, error) { return config.Defaults(), nil }}.Run(context.Background())
	if pass.Status != StatusPass {
		t.Fatalf("expected pass, got %+v", pass)
	}

	fail := ConfigLoadCheck{Load: func(string, bool) (config.Config, error) { return config.Config{}, errors.New("bad config") }}.Run(context.Background())
	if fail.Status != StatusFail {
		t.Fatalf("expected fail, got %+v", fail)
	}
}

func TestNiriSourceCheckCommandAndFixture(t *testing.T) {
	t.Parallel()

	commandPass := NiriSourceCheck{Command: "niri msg -j workspaces windows", LookPath: func(file string) (string, error) {
		if file != "niri" {
			t.Fatalf("unexpected binary lookup: %s", file)
		}
		return "/tmp/niri", nil
	}}.Run(context.Background())
	if commandPass.Status != StatusPass {
		t.Fatalf("expected pass, got %+v", commandPass)
	}

	commandFail := NiriSourceCheck{Command: "", LookPath: func(string) (string, error) { return "", nil }}.Run(context.Background())
	if commandFail.Status != StatusFail {
		t.Fatalf("expected fail, got %+v", commandFail)
	}

	fixturePath := filepath.Join(t.TempDir(), "niri.json")
	if err := os.WriteFile(fixturePath, []byte(`{"workspaces":[],"windows":[]}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fixturePass := NiriSourceCheck{FixturePath: fixturePath}.Run(context.Background())
	if fixturePass.Status != StatusPass {
		t.Fatalf("expected fixture pass, got %+v", fixturePass)
	}
}

func TestNiriSourceCheckFixtureTakesPrecedenceOverCommand(t *testing.T) {
	t.Parallel()

	fixturePath := filepath.Join(t.TempDir(), "niri.json")
	if err := os.WriteFile(fixturePath, []byte(`{"workspaces":[],"windows":[]}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	calledLookPath := false
	result := NiriSourceCheck{
		FixturePath: fixturePath,
		Command:     "",
		LookPath: func(string) (string, error) {
			calledLookPath = true
			return "", errors.New("should not be called when fixture is present")
		},
	}.Run(context.Background())

	if result.Status != StatusPass {
		t.Fatalf("expected pass, got %+v", result)
	}
	if calledLookPath {
		t.Fatal("expected fixture path to bypass command lookup")
	}
}

func TestNiriSourceCheckFixtureInvalidFails(t *testing.T) {
	t.Parallel()

	fixturePath := filepath.Join(t.TempDir(), "niri.json")
	if err := os.WriteFile(fixturePath, []byte(`not-json`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result := NiriSourceCheck{FixturePath: fixturePath}.Run(context.Background())
	if result.Status != StatusFail {
		t.Fatalf("expected fail, got %+v", result)
	}
}

func TestCommandAvailableCheck(t *testing.T) {
	t.Parallel()

	pass := CommandAvailableCheck{CheckName: "kitty_available", Command: "kitty", LookPath: func(string) (string, error) { return "/tmp/kitty", nil }}.Run(context.Background())
	if pass.Status != StatusPass {
		t.Fatalf("expected pass, got %+v", pass)
	}

	fail := CommandAvailableCheck{CheckName: "kitty_available", Command: "kitty", LookPath: func(string) (string, error) { return "", errors.New("missing") }}.Run(context.Background())
	if fail.Status != StatusFail {
		t.Fatalf("expected fail, got %+v", fail)
	}

	empty := CommandAvailableCheck{CheckName: "kitty_available", Command: "   "}.Run(context.Background())
	if empty.Status != StatusFail {
		t.Fatalf("expected fail for empty command, got %+v", empty)
	}
}

func TestLocalInstallCheckPassesWhenAbsent(t *testing.T) {
	t.Parallel()

	result := LocalInstallCheck{
		Path: "/nonexistent/path/redeem",
		Stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
	}.Run(context.Background())
	if result.Status != StatusPass {
		t.Fatalf("expected pass, got %+v", result)
	}
}

func TestLocalInstallCheckFailsWhenPresent(t *testing.T) {
	t.Parallel()

	tmp := filepath.Join(t.TempDir(), "redeem")
	if err := os.WriteFile(tmp, []byte("fake"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	result := LocalInstallCheck{Path: tmp}.Run(context.Background())
	if result.Status != StatusFail {
		t.Fatalf("expected fail, got %+v", result)
	}
}

func TestLocalInstallCheckPassesWithEmptyPath(t *testing.T) {
	t.Parallel()

	result := LocalInstallCheck{Path: ""}.Run(context.Background())
	if result.Status != StatusPass {
		t.Fatalf("expected pass for empty path, got %+v", result)
	}
}
