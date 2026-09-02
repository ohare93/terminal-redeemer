package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
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

func TestCheckpointAndRecoveryInventoryDiagnostics(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC()
	column := 2
	workspace := model.WorkspaceRef{Output: "DP-1", Index: 3}
	placement := model.Placement{Column: &column}
	state := model.State{Windows: []model.Window{
		{Key: "w:kitty:1", AppID: "kitty", Terminal: &model.Terminal{SessionTag: "tracked"}},
		{Key: "w:kitty:2", AppID: "kitty"},
	}}
	recovery := model.RecoveryInventory{
		ActiveSessions: []string{"tracked"},
		Sessions:       []model.RecoverySession{{Name: "tracked", Visible: true, WorkspaceRef: &workspace, Placement: &placement, PlacementObservedAt: &observed}},
	}
	writeCheckpoint := func(boot string, at time.Time, state model.State, recovery model.RecoveryInventory) {
		hash, hashErr := state.Hash()
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		integrity, integrityErr := checkpoints.RecoveryIntegrityHash(state, recovery)
		if integrityErr != nil {
			t.Fatal(integrityErr)
		}
		if _, writeErr := store.Write(checkpoints.Checkpoint{BootID: boot, Host: "host", Profile: "profile", ObservedAt: at, State: state, StateHash: hash, Recovery: recovery, IntegrityHash: integrity}); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	priorRecovery := model.RecoveryInventory{
		ActiveSessions: []string{"tracked", "dead", "duplicate", "invalid", "missing", "prefix"},
		Sessions: []model.RecoverySession{
			{Name: "tracked", Visible: true},
			{Name: "dead", Visible: true},
			{Name: "duplicate", Visible: true},
			{Name: "invalid", Visible: true},
			{Name: "missing", Visible: true},
			{Name: "prefix", Visible: true},
		},
	}
	writeCheckpoint("prior", observed.Add(-time.Hour), model.State{}, priorRecovery)
	writeCheckpoint("current", observed, state, recovery)

	integrity := CheckpointsIntegrityCheck{StateDir: root}.Run(context.Background())
	if integrity.Status != StatusPass || integrity.Detail != "integrity=valid checkpoints=2" {
		t.Fatalf("unexpected integrity diagnostic: %+v", integrity)
	}
	diagnostic := RecoveryInventoryCheck{
		StateDir: root, Host: "host", Profile: "profile", CurrentBootID: func() (string, error) { return "current", nil },
		ObserveCatalog: func(context.Context, string) (zellijlive.Catalog, error) {
			return zellijlive.Catalog{Sessions: map[string]zellijlive.Session{
				"tracked":          {Name: "tracked", Status: zellijlive.StatusActive},
				"unrelated-active": {Name: "unrelated-active", Status: zellijlive.StatusActive},
				"dead":             {Name: "dead", Status: zellijlive.StatusDeadResurrectable},
				"cache-only":       {Name: "cache-only", Status: zellijlive.StatusDeadResurrectable},
				"duplicate":        {Name: "duplicate", Status: zellijlive.StatusDuplicate},
				"invalid":          {Name: "invalid", Status: zellijlive.StatusSocketInvalid},
				"prefix-long":      {Name: "prefix-long", Status: zellijlive.StatusActive},
			}, Names: []string{"tracked", "unrelated-active", "dead", "cache-only", "duplicate", "invalid", "prefix-long"}, ResurrectionCacheAvailable: true}, nil
		},
	}.Run(context.Background())
	for _, want := range []string{"catalog_total=7", "catalog_active_total=3", "catalog_dead_resurrectable_total=2", "current_inventory_active_total=1", "prior_inventory_active_total=6", "same_boot_eligible_active=3", "prior_active_eligible_active=1", "prior_active_eligible_dead_resurrectable=1", "prior_active_excluded_unsafe=4", "prior_active_excluded_statuses=duplicate:1,missing:1,prefix_only:1,socket_invalid:1", "resurrection_cache_available=true", "incomplete_identity_evidence=1", "unnamed_index_dependent_placements=1", "warning="} {
		if diagnostic.Status != StatusPass || !strings.Contains(diagnostic.Detail, want) {
			t.Fatalf("recovery diagnostic missing %q: %+v", want, diagnostic)
		}
	}
}

func TestRecoveryWarningsTreatIdentityEvidenceConservatively(t *testing.T) {
	t.Parallel()

	checkpoint := &checkpoints.Checkpoint{
		State: model.State{Windows: []model.Window{
			{Key: "exact-a", AppID: "kitty", Terminal: &model.Terminal{SessionTag: "duplicate"}},
			{Key: "exact-b", AppID: "kitty", Terminal: &model.Terminal{SessionTag: "duplicate"}},
			{Key: "title-only", AppID: "kitty", Terminal: &model.Terminal{}},
			{Key: "unique", AppID: "kitty", Terminal: &model.Terminal{SessionTag: "unique"}},
			{Key: "not-terminal", AppID: "browser"},
		}},
		Recovery: model.RecoveryInventory{Sessions: []model.RecoverySession{
			{Name: "duplicate", Visible: true},
			{Name: "unique", Visible: true},
		}},
	}

	incomplete, unnamed := recoveryWarnings(checkpoint)
	if incomplete != 3 || unnamed != 0 {
		t.Fatalf("duplicate tags and title-only evidence must warn, got incomplete=%d unnamed=%d", incomplete, unnamed)
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
