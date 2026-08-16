package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/model"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "terminal-redeemer-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", root)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func TestHelpByDefault(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	code := run(nil, &out, &errBuf)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	for _, want := range []string{
		"Refresh this boot's rolling terminal checkpoint",
		"Restore exact prior-boot terminal placement",
		"Create, pick, pin, apply, or temporarily follow remote terminals",
		"Read-only capture/resume/mirror diagnostics",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help output missing %q: %q", want, out.String())
		}
	}
	if stderrWithoutWarning(errBuf.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", errBuf.String())
	}
}

func TestHelpDoesNotRequireValidRuntimeConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("unknownField: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"--config", path, "--help"}, &out, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "Create, pick, pin, apply, or temporarily follow remote terminals") {
		t.Fatalf("unexpected help: %q", out.String())
	}
}

func TestMirrorHelpDistinguishesBoundedWorkflows(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer
	if code := run([]string{"mirror", "--help"}, &out, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{
		"Create one persistent session and best-effort open its source view",
		"Manually pick and attach visible or headless live sessions",
		"Replace one pinned projection set from fresh exact evidence",
		"Attach the available sessions from that pin without creating them",
		"Temporarily follow one selected source workspace in the foreground",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("mirror help missing %q: %q", want, out.String())
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var err bytes.Buffer
	code := run([]string{"nope"}, &out, &err)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(err.String(), "unknown command") {
		t.Fatalf("expected unknown command message, got %q", err.String())
	}
}

func TestSubcommandHelpExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "capture once", args: []string{"capture", "once", "--help"}},
		{name: "mirror snapshot", args: []string{"mirror", "snapshot", "--help"}},
		{name: "mirror list", args: []string{"mirror", "list", "--help"}},
		{name: "mirror open", args: []string{"mirror", "open", "--help"}},
		{name: "mirror new", args: []string{"mirror", "new", "--help"}},
		{name: "mirror save", args: []string{"mirror", "save", "--help"}},
		{name: "mirror apply", args: []string{"mirror", "apply", "--help"}},
		{name: "mirror follow", args: []string{"mirror", "follow", "--help"}},
		{name: "mirror status", args: []string{"mirror", "status", "--help"}},
		{name: "mirror close", args: []string{"mirror", "close", "--help"}},
		{name: "mirror paste-image", args: []string{"mirror", "paste-image", "--help"}},
		{name: "resume", args: []string{"resume", "--help"}},
		{name: "prune run", args: []string{"prune", "run", "--help"}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			var stderr bytes.Buffer
			code := run(tc.args, &out, &stderr)

			if code != 0 {
				t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "Usage of") {
				t.Fatalf("expected help usage output on stderr, got %q", stderr.String())
			}
		})
	}
}

func TestInvalidUsageExitCodesRemainTwo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "capture once unknown flag", args: []string{"capture", "once", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "mirror snapshot unknown flag", args: []string{"mirror", "snapshot", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "mirror save rejects local fixture", args: []string{"mirror", "save", "--snapshot-file", "fixture.json"}, want: "flag provided but not defined"},
		{name: "mirror apply rejects local fixture", args: []string{"mirror", "apply", "--snapshot-file", "fixture.json"}, want: "flag provided but not defined"},
		{name: "mirror follow rejects unsafe interval", args: []string{"mirror", "follow", "--interval", "1s"}, want: "--interval must be at least 2s"},
		{name: "resume invalid timeout", args: []string{"resume", "--timeout", "0s"}, want: "--timeout and --poll-interval must be positive"},
		{name: "prune run unknown flag", args: []string{"prune", "run", "--no-such-flag"}, want: "flag provided but not defined"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			var stderr bytes.Buffer
			code := run(tc.args, &out, &stderr)

			if code != 2 {
				t.Fatalf("expected code 2, got %d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("expected stderr containing %q, got %q", tc.want, stderr.String())
			}
		})
	}
}

func TestResumeDryRunSelectsPriorBootAndOnlyListsSessions(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Add(-time.Minute)
	state := model.State{
		Workspaces: []model.Workspace{{ID: "old-id", Index: 2, Name: "dev", Output: "DP-1"}},
		Windows: []model.Window{{
			Key: "w-terminal", AppID: "kitty", WorkspaceID: "old-id",
			WorkspaceRef: &model.WorkspaceRef{Name: "dev", Output: "DP-1", Index: 2},
			Terminal:     &model.Terminal{CWD: "/tmp/project", SessionTag: "session-a"},
		}},
	}
	hash, err := state.Hash()
	if err != nil {
		t.Fatal(err)
	}
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(checkpoints.Checkpoint{
		V: checkpoints.SchemaVersion, BootID: "prior-boot", Host: "local", Profile: "default",
		ObservedAt: now, State: state, StateHash: hash,
	}); err != nil {
		t.Fatal(err)
	}

	fixture := filepath.Join(t.TempDir(), "niri.json")
	if err := os.WriteFile(fixture, []byte(`{"workspaces":[{"id":"current-id","idx":5,"name":"dev","output":"DP-2"}],"windows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "unexpected")
	zellij := filepath.Join(bin, "zellij")
	script := "#!/bin/sh\nif [ \"$1 $2\" != \"list-sessions --short\" ]; then echo x > " + marker + "; exit 9; fi\nprintf 'session-a\\n'\n"
	if err := os.WriteFile(zellij, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("stateDir: "+root+"\nhost: local\nprofile: default\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, stderr bytes.Buffer
	code := run([]string{"--config", configPath, "resume", "--dry-run", "--fixture", fixture}, &out, &stderr)
	if code != 0 {
		t.Fatalf("resume code = %d, stderr=%q", code, stderr.String())
	}
	for _, want := range []string{
		`resume_candidate status=ready boot_id="prior-boot"`,
		`resume_item window_key="w-terminal" session="session-a" status=ready workspace_method=name`,
		"resume_summary ready=1 already_open=0 unavailable=0 degraded=0 stale=0 failed=0 skipped=0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dry run attempted an unexpected Zellij command")
	}
}

func TestResumeWaitsForNiriBeforeCheckpointSelection(t *testing.T) {
	stateDir := t.TempDir()
	missingFixture := filepath.Join(t.TempDir(), "not-ready.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("stateDir: "+stateDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, stderr bytes.Buffer
	code := run([]string{"--config", configPath, "resume", "--dry-run", "--fixture", missingFixture, "--timeout", "15ms", "--poll-interval", "2ms"}, &out, &stderr)
	if code != 1 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "resume Niri readiness failed") || !strings.Contains(stderr.String(), "verify NIRI_SOCKET") {
		t.Fatalf("expected actionable readiness error, got %q", stderr.String())
	}
	if strings.Contains(out.String(), "resume_candidate") {
		t.Fatalf("checkpoint selection ran before Niri readiness: %q", out.String())
	}
}

func TestCaptureOnceEndToEndWithFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	err := os.WriteFile(fixturePath, []byte(`{
		"workspaces": [{"id": "ws-1", "idx": 1, "name": "main"}],
		"windows": [{"id": 101, "app_id": "kitty", "title": "shell", "workspace_id": "ws-1", "pid": 4242}]
	}`), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stateDir := filepath.Join(root, "state")

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"capture", "once", "--state-dir", stateDir, "--fixture", fixturePath, "--host", "host-a", "--profile", "default"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	got, issues, err := checkpoints.List(stateDir)
	if err != nil || len(issues) != 0 || len(got) != 1 {
		t.Fatalf("captured checkpoints=%#v issues=%#v err=%v", got, issues, err)
	}
	if got[0].Host != "host-a" || got[0].Profile != "default" || len(got[0].State.Windows) != 1 {
		t.Fatalf("unexpected captured checkpoint: %#v", got[0])
	}
}

func TestCaptureOnceEndToEndWithCommandSnapshotter(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")

	t.Setenv("REDEEM_NIRI_CMD", "printf '{\"workspaces\":[{\"id\":\"ws-1\",\"idx\":1}],\"windows\":[{\"id\":101,\"app_id\":\"kitty\",\"workspace_id\":\"ws-1\",\"pid\":4242}]}'")

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"capture", "once", "--state-dir", stateDir, "--host", "host-a", "--profile", "default"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	got, issues, err := checkpoints.List(stateDir)
	if err != nil || len(issues) != 0 || len(got) != 1 {
		t.Fatalf("captured checkpoints=%#v issues=%#v err=%v", got, issues, err)
	}
	if got[0].Host != "host-a" || got[0].Profile != "default" || len(got[0].State.Windows) != 1 {
		t.Fatalf("unexpected captured checkpoint: %#v", got[0])
	}
}

func TestMirrorSnapshotEndToEndWithFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	err := os.WriteFile(fixturePath, []byte(`{
		"workspaces": [
			{"id": "ws-2", "idx": 2},
			{"id": "ws-1", "idx": 1}
		],
		"windows": [
			{"id": 20, "app_id": "kitty", "title": "second", "workspace_id": "ws-2", "pid": 0, "layout": {"pos_in_scrolling_layout": [1, 1]}},
			{"id": 10, "app_id": "kitty", "title": "first", "workspace_id": "ws-1", "pid": 0, "layout": {"pos_in_scrolling_layout": [1, 1]}}
		]
	}`), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"mirror", "snapshot", "--fixture", fixturePath, "--host", "lattice", "--profile", "default"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	got := out.String()
	if !strings.Contains(got, `"host": "lattice"`) {
		t.Fatalf("expected host in mirror output, got %q", got)
	}
	firstIndex := strings.Index(got, `"title": "first"`)
	secondIndex := strings.Index(got, `"title": "second"`)
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("expected ordered mirror output first before second, got %q", got)
	}
}

func TestPruneRunCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := model.State{}
	hash, err := state.Hash()
	if err != nil {
		t.Fatal(err)
	}
	for boot, ageDays := range map[string]int{"older": -60, "newest-prior": -40} {
		if _, err := store.Write(checkpoints.Checkpoint{
			V: checkpoints.SchemaVersion, BootID: boot, Host: "host-a", Profile: "default",
			ObservedAt: time.Now().UTC().AddDate(0, 0, ageDays), State: state, StateHash: hash,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var out, stderr bytes.Buffer
	code := run([]string{"prune", "run", "--state-dir", root, "--days", "30"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "prune_summary checkpoints_pruned=1") {
		t.Fatalf("expected prune summary output, got %q", out.String())
	}
}

func TestGlobalConfigAppliesCaptureDefaultsAndCLIOverrides(t *testing.T) {
	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	err := os.WriteFile(fixturePath, []byte(`{
		"workspaces": [{"id": "ws-1", "idx": 1, "name": "main"}],
		"windows": [{"id": 101, "app_id": "kitty", "title": "shell", "workspace_id": "ws-1", "pid": 4242}]
	}`), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stateFromConfig := filepath.Join(root, "state-config")
	overrideStateDir := filepath.Join(root, "state-override")
	configPath := filepath.Join(root, "redeem.yaml")
	err = os.WriteFile(configPath, []byte("stateDir: "+stateFromConfig+"\nhost: cfg-host\nprofile: cfg-profile\n"), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "capture", "once", "--fixture", fixturePath}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	got, issues, err := checkpoints.List(stateFromConfig)
	if err != nil || len(issues) != 0 || len(got) != 1 {
		t.Fatalf("checkpoints=%#v issues=%#v err=%v", got, issues, err)
	}
	if got[0].Host != "cfg-host" || got[0].Profile != "cfg-profile" {
		t.Fatalf("expected config host/profile, got host=%q profile=%q", got[0].Host, got[0].Profile)
	}

	out.Reset()
	stderr.Reset()
	code = run([]string{"--config", configPath, "capture", "once", "--fixture", fixturePath, "--state-dir", overrideStateDir, "--host", "cli-host", "--profile", "cli-profile"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected override code 0, got %d stderr=%q", code, stderr.String())
	}

	overrideCheckpoints, issues, err := checkpoints.List(overrideStateDir)
	if err != nil || len(issues) != 0 || len(overrideCheckpoints) != 1 {
		t.Fatalf("override checkpoints=%#v issues=%#v err=%v", overrideCheckpoints, issues, err)
	}
	if overrideCheckpoints[0].Host != "cli-host" || overrideCheckpoints[0].Profile != "cli-profile" {
		t.Fatalf("expected CLI host/profile, got host=%q profile=%q", overrideCheckpoints[0].Host, overrideCheckpoints[0].Profile)
	}
}

func TestGlobalConfigExplicitMissingFileErrors(t *testing.T) {
	pathDir := t.TempDir()
	for _, cmd := range []string{"kitty", "zellij", "niri"} {
		cmdPath := filepath.Join(pathDir, cmd)
		err := os.WriteFile(cmdPath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
		if err != nil {
			t.Fatalf("write fake command %s: %v", cmd, err)
		}
	}
	t.Setenv("PATH", pathDir)

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml"), "doctor"}, &out, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(out.String(), "doctor_check name=config_load status=fail") {
		t.Fatalf("expected config check failure, got %q", out.String())
	}
	if !strings.Contains(out.String(), "doctor_summary total=") {
		t.Fatalf("expected doctor summary, got %q", out.String())
	}
	if stderrWithoutWarning(stderr.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", stderr.String())
	}
}

func TestDoctorPassExitCode(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + stateDir + "\nresume:\n  terminalCommand: kitty\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	pathDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatalf("mkdir path dir: %v", err)
	}
	for _, cmd := range []string{"kitty", "zellij", "niri"} {
		cmdPath := filepath.Join(pathDir, cmd)
		err := os.WriteFile(cmdPath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
		if err != nil {
			t.Fatalf("write fake command %s: %v", cmd, err)
		}
	}
	t.Setenv("PATH", pathDir)
	fixturePath := filepath.Join(root, "niri.json")
	if err := os.WriteFile(fixturePath, []byte(`{"workspaces":[],"windows":[]}`), 0o600); err != nil {
		t.Fatalf("write doctor fixture: %v", err)
	}
	t.Setenv("REDEEM_NIRI_FIXTURE", fixturePath)
	// Set HOME to temp dir so localInstallPath() doesn't find a real ~/.local/bin/redeem.
	t.Setenv("HOME", root)

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "doctor"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d output=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "doctor_summary total=10 passed=10 failed=0") {
		t.Fatalf("unexpected doctor summary: %q", out.String())
	}
	for _, name := range []string{"boot_id", "state_paths", "niri_readiness", "resume_launcher", "zellij_listing", "resume_policy", "startup_service", "checkpoints_integrity"} {
		if !strings.Contains(out.String(), "doctor_check name="+name+" status=pass") {
			t.Fatalf("doctor output missing passing %s check: %q", name, out.String())
		}
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("doctor must not create the configured state directory: %v", err)
	}
	if stderrWithoutWarning(stderr.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", stderr.String())
	}
}

func TestMirrorSaveDryRunDoesNotCreateState(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf '%s\\n' '{\"host\":\"remote-self-label\",\"profile\":\"default\",\"generated_at\":\"2026-01-01T00:00:00Z\",\"windows\":[]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	niri := filepath.Join(dir, "niri")
	if err := os.WriteFile(niri, []byte("#!/bin/sh\nprintf '[]'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "absent-state")
	var stdout, stderr bytes.Buffer
	code := run([]string{"mirror", "save", "--host", "lattice", "--ssh-command", ssh, "--niri-command", niri, "--state-dir", stateDir, "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created state: %v", err)
	}
	if !strings.Contains(stdout.String(), "would_save=0") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestMirrorApplyDryRunPlansWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf '%s\\n' '{\"host\":\"remote-self-label\",\"profile\":\"default\",\"generated_at\":\"2026-01-01T00:00:00Z\",\"active_zellij_sessions\":[\"A\"],\"windows\":[{\"order\":0,\"app_id\":\"zellij\",\"title\":\"A\",\"headless\":true,\"zellij_session\":\"A\"}]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	niri := filepath.Join(dir, "niri")
	if err := os.WriteFile(niri, []byte("#!/bin/sh\nprintf '[]'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	store, err := mirror.OpenPinStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	floating := false
	pin := mirror.Pin{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []mirror.PinnedProjection{{Session: "A", Workspace: model.WorkspaceRef{Index: 1}, Order: 0, Placement: model.Placement{IsFloating: &floating}}}}
	path, err := store.Write(pin)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"mirror", "apply", "--host", "lattice", "--ssh-command", ssh, "--niri-command", niri, "--state-dir", stateDir, "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dry-run changed pin")
	}
	if !strings.Contains(stdout.String(), `session="A" order=0 status=ready`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestMirrorOpenDryRunFromSnapshotFile(t *testing.T) {
	root := t.TempDir()
	snapshotPath := filepath.Join(root, "snapshot.json")
	payload := `{"host":"source","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":0,"source_window_id":1,"app_id":"kitty","title":"work","zellij_session":"session-a","terminal":{"cwd":"/tmp/project","zellij_session":"session-a"}}]}`
	if err := os.WriteFile(snapshotPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"mirror", "open", "--snapshot-file", snapshotPath, "--host", "source", "--all", "--dry-run", "--no-clipboard"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, part := range []string{"'kitty'", "'source[0|session-a]: work'", "'ssh'", "'attach'", "'session-a'", "'/tmp/project'"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("dry-run missing %q: %s", part, out.String())
		}
	}
}

func TestMirrorNewDryRunShowsCreatorAndBestEffortSourceHelper(t *testing.T) {
	original := newMirrorSessionName
	defer func() { newMirrorSessionName = original }()
	newMirrorSessionName = func() (string, error) {
		return "redeem-0123456789abcdef0123456789abcdef", nil
	}

	var out, stderr bytes.Buffer
	code := run([]string{"mirror", "new", "--host", "user@lattice", "--ssh-command", "ssh", "--launcher-command", "kitty", "--app-id", "owned-mirror", "--source-workspace", "agentleman", "--dry-run", "--no-clipboard"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"kitty", "owned-mirror", "user@lattice", "--create", "redeem-0123456789abcdef0123456789abcdef", "--on-force-close", "detach", "attach-local", "--workspace", "agentleman", "waits for exact ACTIVE session"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run missing %q: %s", want, out.String())
		}
	}
	for _, forbidden := range []string{"${SHELL", "exec sh", "exec bash"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("dry-run contains fallback %q: %s", forbidden, out.String())
		}
	}
	if strings.Count(out.String(), "'--create'") != 1 {
		t.Fatalf("only the Overton creator may receive --create: %s", out.String())
	}
}

type newCLIRunner struct {
	runs        []mirror.Command
	helperError error
	blockHelper bool
}

func (runner *newCLIRunner) Output(context.Context, mirror.Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}

func (runner *newCLIRunner) Run(ctx context.Context, command mirror.Command) error {
	runner.runs = append(runner.runs, command)
	if len(runner.runs) != 2 {
		return nil
	}
	if runner.blockHelper {
		<-ctx.Done()
		return ctx.Err()
	}
	return runner.helperError
}

func TestExecuteMirrorNewRunsCreatorThenOneBoundedBestEffortHelper(t *testing.T) {
	const session = "redeem-0123456789abcdef0123456789abcdef"
	creator, err := mirror.PlanNew(session, mirror.LaunchConfig{
		SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned",
	})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := mirror.PlanSourceAttach(mirror.SourceAttachConfig{
		SourceHost: "lattice", SSHCommand: "ssh", SnapshotCommand: []string{"redeem", "mirror", "snapshot"}, Session: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &newCLIRunner{helperError: errors.New("source helper unavailable")}
	sourceErr, creatorErr := executeMirrorNew(context.Background(), runner, creator, helper, nil)
	if creatorErr != nil || sourceErr == nil || len(runner.runs) != 2 || !strings.Contains(sourceErr.Error(), "source helper unavailable") {
		t.Fatalf("creatorErr=%v sourceErr=%v calls=%#v", creatorErr, sourceErr, runner.runs)
	}
	if !strings.Contains(strings.Join(runner.runs[0].Args, " "), "--create") || strings.Contains(strings.Join(runner.runs[1].Args, " "), "--create") {
		t.Fatalf("creation boundary violated: %#v", runner.runs)
	}

	blocking := &newCLIRunner{blockHelper: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	sourceErr, creatorErr = executeMirrorNew(ctx, blocking, creator, helper, nil)
	if creatorErr != nil || sourceErr == nil || !errors.Is(sourceErr, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("bounded helper creatorErr=%v sourceErr=%v elapsed=%s", creatorErr, sourceErr, time.Since(started))
	}

	unsupported := &newCLIRunner{}
	planErr := errors.New("unsupported snapshot wrapper")
	sourceErr, creatorErr = executeMirrorNew(context.Background(), unsupported, creator, mirror.Command{}, planErr)
	if creatorErr != nil || !errors.Is(sourceErr, planErr) || len(unsupported.runs) != 1 {
		t.Fatalf("post-creator planning warning creatorErr=%v sourceErr=%v calls=%#v", creatorErr, sourceErr, unsupported.runs)
	}
}

func TestMirrorAttachLocalDryRunIsAttachOnlyAndValidatesWorkspace(t *testing.T) {
	const session = "redeem-0123456789abcdef0123456789abcdef"
	var out, stderr bytes.Buffer
	code := run([]string{"mirror", "attach-local", "--session", session, "--workspace", "agentleman", "--dry-run"}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "'zellij' 'attach'") || strings.Contains(out.String(), "--create") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"mirror", "attach-local", "--session", session, "--workspace", "--bad", "--dry-run"}, &out, &stderr); code != 2 {
		t.Fatalf("unsafe workspace code=%d stderr=%q", code, stderr.String())
	}
}

func TestMirrorOpenInteractivePickerIntegrationAndCancellation(t *testing.T) {
	original := chooseMirrorSessions
	defer func() { chooseMirrorSessions = original }()

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	payload := `{"host":"source","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":0,"source_window_id":1,"app_id":"kitty","title":"first","workspace_name":"Dev","zellij_session":"alpha","terminal":{"cwd":"/tmp/a","zellij_session":"alpha"}},{"order":1,"source_window_id":2,"app_id":"kitty","title":"second","workspace_name":"Chat","zellij_session":"beta","terminal":{"cwd":"/tmp/b","zellij_session":"beta"}}]}`
	if err := os.WriteFile(snapshotPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	chooseMirrorSessions = func(windows []mirror.Window) ([]mirror.Window, bool, error) {
		called = true
		if len(windows) != 2 || mirror.SessionName(windows[0]) != "alpha" || windows[0].WorkspaceName != "Dev" {
			t.Fatalf("picker received unexpected discovery: %#v", windows)
		}
		return []mirror.Window{windows[1]}, false, nil
	}
	var out, stderr bytes.Buffer
	code := run([]string{"mirror", "open", "--snapshot-file", snapshotPath, "--dry-run", "--no-clipboard"}, &out, &stderr)
	if code != 0 || !called || !strings.Contains(out.String(), "second") || strings.Contains(out.String(), "first") {
		t.Fatalf("code=%d called=%t out=%q stderr=%q", code, called, out.String(), stderr.String())
	}

	chooseMirrorSessions = func([]mirror.Window) ([]mirror.Window, bool, error) {
		return nil, true, nil
	}
	out.Reset()
	stderr.Reset()
	code = run([]string{"mirror", "open", "--snapshot-file", snapshotPath}, &out, &stderr)
	if code != 0 || out.Len() != 0 || stderrWithoutWarning(stderr.String()) != "" {
		t.Fatalf("cancel code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestMirrorOpenNoninteractiveFlagsBypassPickerAndPreserveOrdering(t *testing.T) {
	original := chooseMirrorSessions
	defer func() { chooseMirrorSessions = original }()
	chooseMirrorSessions = func([]mirror.Window) ([]mirror.Window, bool, error) {
		t.Fatal("noninteractive selection invoked picker")
		return nil, false, nil
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	payload := `{"host":"source","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":0,"source_window_id":1,"app_id":"kitty","title":"first","zellij_session":"alpha","terminal":{"cwd":"/tmp/a","zellij_session":"alpha"}},{"order":1,"source_window_id":2,"app_id":"kitty","title":"second","zellij_session":"beta","terminal":{"cwd":"/tmp/b","zellij_session":"beta"}}]}`
	if err := os.WriteFile(snapshotPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	base := []string{"mirror", "open", "--snapshot-file", snapshotPath, "--dry-run", "--no-clipboard"}
	tests := []struct {
		name      string
		flags     []string
		want      []string
		dontWant  []string
		wantOrder bool
	}{
		{name: "all", flags: []string{"--all"}, want: []string{"first", "second"}, wantOrder: true},
		{name: "repeatable session", flags: []string{"--session", "beta", "--session", "alpha"}, want: []string{"first", "second"}, wantOrder: true},
		{name: "select", flags: []string{"--select", "2"}, want: []string{"second"}, dontWant: []string{"first"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := run(append(append([]string(nil), base...), tc.flags...), &out, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q: %s", want, out.String())
				}
			}
			for _, unwanted := range tc.dontWant {
				if strings.Contains(out.String(), unwanted) {
					t.Fatalf("output unexpectedly contains %q: %s", unwanted, out.String())
				}
			}
			if tc.wantOrder && strings.Index(out.String(), "first") > strings.Index(out.String(), "second") {
				t.Fatalf("launch ordering changed: %s", out.String())
			}
		})
	}
}

func TestMirrorMalformedSnapshotError(t *testing.T) {
	var out bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"windows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"mirror", "list", "--snapshot-file", path}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "malformed remote mirror snapshot") {
		t.Fatalf("malformed code=%d stderr=%q", code, stderr.String())
	}
}

func TestMirrorCloseDryRunUsesOwnedWindowFilter(t *testing.T) {
	root := t.TempDir()
	niri := filepath.Join(root, "fake-niri")
	script := `#!/bin/sh
printf '%s' '[{"id":11,"app_id":"owned","title":"source[0]: one","workspace_id":2},{"id":12,"app_id":"kitty","title":"other"}]'
`
	if err := os.WriteFile(niri, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"mirror", "close", "--host", "source", "--app-id", "owned", "--niri-command", niri, "--dry-run"}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "would close id=11") || strings.Contains(out.String(), "id=12") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestCaptureNiriCommandDefaultPrecedence(t *testing.T) {
	cfg := config.Defaults()

	t.Setenv("REDEEM_NIRI_CMD", "env-niri")
	if got := captureNiriCommandDefault(cfg); got != "env-niri" {
		t.Fatalf("expected env override for default command, got %q", got)
	}

	cfg.Capture.NiriCommand = "custom-niri --json"
	if got := captureNiriCommandDefault(cfg); got != "custom-niri --json" {
		t.Fatalf("expected explicit config command to win, got %q", got)
	}

	cfg.Capture.NiriCommand = ""
	if got := captureNiriCommandDefault(cfg); got != "env-niri" {
		t.Fatalf("expected env override when config command empty, got %q", got)
	}

	t.Setenv("REDEEM_NIRI_CMD", "")
	if got := captureNiriCommandDefault(config.Defaults()); got != config.Defaults().Capture.NiriCommand {
		t.Fatalf("expected builtin default when env unset, got %q", got)
	}
}

func stderrWithoutWarning(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "warning:") && strings.Contains(line, ".local/bin/redeem") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
