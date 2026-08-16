package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/resume"
	"github.com/jmo/terminal-redeemer/internal/storelock"
)

func TestOpenPinStoreIsReadOnlyForDryRun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	store, err := OpenPinStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("lattice", "default"); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("read error=%v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open created state: %v", err)
	}
}

func TestPinStoreAtomicModeCorruptionFirstCreationAndPruneIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new", "state")
	store, err := OpenPinStore(root)
	if err != nil {
		t.Fatal(err)
	}
	pin := testPin("Alpha")
	path, err := store.Write(pin)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	for _, dir := range []string{root, filepath.Join(root, "mirror"), filepath.Join(root, "mirror", "pins")} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("first-creation directory %s: info=%v err=%v", dir, info, err)
		}
	}
	got, err := store.Read("lattice", "default")
	if err != nil || !reflect.DeepEqual(got, pin) {
		t.Fatalf("read=%#v err=%v", got, err)
	}
	beforeFailure, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pinsDir := filepath.Dir(path)
	if err := os.Chmod(pinsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(testPin("Replacement")); err == nil {
		t.Fatal("expected pre-publication directory failure")
	}
	if err := os.Chmod(pinsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	afterFailure, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(beforeFailure, afterFailure) {
		t.Fatalf("failed write replaced old pin: changed=%v err=%v", !reflect.DeepEqual(beforeFailure, afterFailure), err)
	}

	if err := os.MkdirAll(filepath.Join(root, "checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoints.Prune(root, time.Now().Add(time.Hour), "current"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("prune touched pin: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"v":1,"source_host":"lattice","source_profile":"default","projections":[],"payload":"sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("lattice", "default"); !errors.Is(err, ErrPinInvalid) {
		t.Fatalf("corruption error=%v", err)
	}
}

func TestPinStoreRejectsFinalAndIntermediateSymlinks(t *testing.T) {
	t.Run("intermediate", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "mirror")); err != nil {
			t.Fatal(err)
		}
		store, err := OpenPinStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(testPin("A")); err == nil {
			t.Fatal("accepted symlinked mirror directory")
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("escaped through intermediate symlink: entries=%v err=%v", entries, err)
		}
	})
	t.Run("final", func(t *testing.T) {
		root := t.TempDir()
		store, err := OpenPinStore(root)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		original := []byte("do not replace")
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		path, err := store.Write(testPin("seed"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read("lattice", "default"); !errors.Is(err, ErrPinInvalid) {
			t.Fatalf("read symlink error=%v", err)
		}
		if _, err := store.Write(testPin("A")); !errors.Is(err, ErrPinInvalid) {
			t.Fatalf("write symlink error=%v", err)
		}
		got, err := os.ReadFile(target)
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatalf("symlink target changed: %q err=%v", got, err)
		}
	})
}

func TestPinValidationRejectsUntypedOrUnboundedData(t *testing.T) {
	floating := false
	cases := []Pin{
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "bad\n", Workspace: model.WorkspaceRef{Index: 1}, Placement: model.Placement{IsFloating: &floating}}}},
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "ok", RemoteCWD: strings.Repeat("x", 4097), Workspace: model.WorkspaceRef{Index: 1}, Placement: model.Placement{IsFloating: &floating}}}},
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "ok", Workspace: model.WorkspaceRef{}, Placement: model.Placement{IsFloating: &floating}}}},
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "ok", Workspace: model.WorkspaceRef{Index: 1}, Placement: model.Placement{}}}},
	}
	for _, pin := range cases {
		if err := pin.Validate(); err == nil {
			t.Fatalf("accepted %#v", pin)
		}
	}
	leading := testPin("-hostile'; echo NO")
	if err := leading.Validate(); err != nil {
		t.Fatalf("safe typed leading-dash session rejected: %v", err)
	}
}

func TestBuildPinUsesTransportIdentityAndLocalOrderAcrossWorkspaces(t *testing.T) {
	snapshot := Snapshot{Host: "alias@lattice", Profile: "default", Windows: []Window{
		{Order: 5, SourceWindowID: 2, AppID: "kitty", ZellijSession: "B", Terminal: &Terminal{CWD: "/b", ZellijSession: "B"}},
		{Order: 1, SourceWindowID: 1, AppID: "kitty", ZellijSession: "A", Terminal: &Terminal{CWD: "/a", ZellijSession: "A"}},
	}}
	windows := []OwnedWindow{
		{ID: 10, WorkspaceID: 7, IsFloating: true, Layout: OwnedLayout{WindowSize: []int{800, 600}}},
		{ID: 11, WorkspaceID: 8, Layout: OwnedLayout{TileSize: []float64{900, 700}}},
	}
	inventory := ProjectionInventory{Exact: []Projection{
		{Window: windows[0], SourceHost: "alias@lattice", Session: "B"},
		{Window: windows[1], SourceHost: "alias@lattice", Session: "A"},
	}, Untracked: []OwnedWindow{{ID: 99}}, Ambiguous: []OwnedWindow{{ID: 98}}}
	result, err := BuildPin(snapshot, "alias@lattice", windows, []OwnedWorkspace{{ID: 7, Index: 2, Name: "two"}, {ID: 8, Index: 1, Name: "one"}}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Untracked != 1 || result.Ambiguous != 1 || len(result.Pin.Projections) != 2 {
		t.Fatalf("result=%#v", result)
	}
	if result.Pin.Projections[0].Session != "B" || result.Pin.Projections[0].Order != 0 || result.Pin.Projections[0].Workspace.Name != "two" || result.Pin.Projections[1].Session != "A" || result.Pin.Projections[1].Order != 1 {
		t.Fatalf("pin did not preserve local observation order: %#v", result.Pin)
	}
	if _, err := BuildPin(snapshot, "lattice", windows, nil, inventory); err == nil {
		t.Fatal("accepted snapshot host mismatch")
	}
}

func TestPrepareApplyMissingDuplicateCaseDistinctAndNoop(t *testing.T) {
	pin := testPin("Alpha")
	pin.Projections = append(pin.Projections, pinned("alpha", 1))
	snapshot := Snapshot{Host: "lattice", Profile: "default", ActiveSessions: []string{"Alpha"}, Windows: []Window{{Order: 0, AppID: "zellij", Headless: true, ZellijSession: "Alpha"}}}
	inventory := ProjectionInventory{Exact: []Projection{{SourceHost: "other", Session: "Alpha"}}, Ambiguous: []OwnedWindow{{ID: 9}}}
	result := prepareApply(pin, activeSet(snapshot.ActiveSessions...), inventory, []OwnedWorkspace{{ID: 1, Index: 1}, {ID: 2, Index: 2}})
	if result.Items[0].Status != ApplyReady || result.Items[1].Status != ApplyMissing || result.Ambiguous != 1 {
		t.Fatalf("items=%#v", result)
	}
	inventory.Exact = []Projection{{SourceHost: "lattice", Session: "Alpha"}, {SourceHost: "lattice", Session: "Alpha"}}
	result = prepareApply(pin, activeSet(snapshot.ActiveSessions...), inventory, nil)
	if result.Items[0].Status != ApplyAmbiguous {
		t.Fatalf("items=%#v", result.Items)
	}
	empty := testPin("unused")
	empty.Projections = []PinnedProjection{}
	if got := prepareApply(empty, activeSet(), ProjectionInventory{}, nil); len(got.Items) != 0 {
		t.Fatalf("noop=%#v", got)
	}
}

type pinRunner struct {
	commands    []Command
	failSession string
	niriErr     error
	token       string
}

func (r *pinRunner) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}
func (r *pinRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	joined := strings.Join(command.Args, " ")
	if command.Name == "kitty" {
		marker := projectionTokenEnvironment + "="
		if index := strings.Index(joined, marker); index >= 0 && len(joined) >= index+len(marker)+32 {
			r.token = joined[index+len(marker) : index+len(marker)+32]
		}
	}
	if command.Name == "kitty" && r.failSession != "" && strings.Contains(joined, r.failSession) {
		return errors.New("launch failed")
	}
	if command.Name == "niri" && r.niriErr != nil {
		return r.niriErr
	}
	return nil
}

func TestApplyPinnedPartialLaunchPlacementAndDuplicateIdempotence(t *testing.T) {
	pin := testPin("A")
	pin.Projections = append(pin.Projections, pinned("B", 1))
	snapshot := activeSnapshot("lattice", "A", "B")
	runner := &pinRunner{failSession: "'B'"}
	deps := ApplyDeps{
		Runner: runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) {
			for _, command := range runner.commands {
				if command.Name == "kitty" {
					return []OwnedWindow{{ID: 10, PID: 100}}, nil
				}
			}
			return nil, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) {
			return []OwnedWorkspace{{ID: 1, Index: 1, Name: "work"}}, nil
		},
		Inspect: func(_ context.Context, windows []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
			if len(windows) == 1 {
				return ProjectionInventory{Exact: []Projection{{Window: windows[0], SourceHost: "lattice", Session: "A", CorrelationToken: runner.token}}}, nil
			}
			return ProjectionInventory{}, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	cfg := applyTestConfig(t, pin, snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := ApplyPinned(ctx, cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != ApplyOpened || result.Items[0].WindowID != 10 || result.Items[0].LayoutStatus != resume.LayoutApplied || result.Items[1].Status != ApplyFailed {
		t.Fatalf("result=%#v", result)
	}
	if len(runner.commands) < 4 {
		t.Fatalf("commands=%#v", runner.commands)
	}

	duplicateRunner := &pinRunner{}
	deps.Runner = duplicateRunner
	deps.ListWindows = func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{{ID: 10, PID: 100}}, nil }
	one := testPin("A")
	cfg = applyTestConfig(t, one, snapshot)
	result, err = ApplyPinned(ctx, cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != ApplyAlreadyOpen || len(duplicateRunner.commands) != 0 {
		t.Fatalf("duplicate apply mutated: %#v commands=%#v", result, duplicateRunner.commands)
	}
}

func TestApplyPinnedCorrelatesOwnWindowAmidConcurrentManualOpen(t *testing.T) {
	pin := testPin("A")
	snapshot := activeSnapshot("lattice", "A")
	runner := &pinRunner{}
	launched := false
	token := ""
	deps := ApplyDeps{
		Runner: runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) {
			if launched {
				return []OwnedWindow{{ID: 20, PID: 200}, {ID: 21, PID: 201}}, nil
			}
			return nil, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) {
			return []OwnedWorkspace{{ID: 1, Index: 1, Name: "work"}}, nil
		},
		Inspect: func(_ context.Context, windows []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
			out := ProjectionInventory{}
			for _, window := range windows {
				projection := Projection{Window: window, SourceHost: "lattice", Session: "A"}
				if window.ID == 20 {
					projection.CorrelationToken = token
				}
				out.Exact = append(out.Exact, projection)
			}
			return out, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	// Observe runner launch through the command slice without mutating from the
	// inspector: ListWindows switches only after Kitty has been invoked.
	originalRun := deps.Runner
	deps.Runner = runnerFunc{output: originalRun.Output, run: func(ctx context.Context, command Command) error {
		err := originalRun.Run(ctx, command)
		if command.Name == "kitty" {
			token = runner.token
			launched = true
		}
		return err
	}}
	cfg := applyTestConfig(t, pin, snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := ApplyPinned(ctx, cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != ApplyOpened || result.Items[0].WindowID != 20 {
		t.Fatalf("result=%#v", result)
	}
	for _, command := range runner.commands {
		if command.Name == "niri" && strings.Contains(strings.Join(command.Args, " "), "21") {
			t.Fatalf("concurrent manual window received Niri mutation: %#v", runner.commands)
		}
	}
}

func TestApplyPinnedLockPreventsConcurrentDuplicateLaunch(t *testing.T) {
	pin := testPin("A")
	cfg := applyTestConfig(t, pin, activeSnapshot("lattice", "A"))
	store, err := OpenPinStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.acquire("lattice", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	runner := &pinRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = ApplyPinned(ctx, cfg, ApplyDeps{Runner: runner, ListWindows: func(context.Context) ([]OwnedWindow, error) { return nil, nil }, Workspaces: func(context.Context) ([]OwnedWorkspace, error) { return nil, nil }})
	if !errors.Is(err, storelock.ErrLocked) || len(runner.commands) != 0 {
		t.Fatalf("err=%v commands=%#v", err, runner.commands)
	}
}

type runnerFunc struct {
	output func(context.Context, Command) ([]byte, error)
	run    func(context.Context, Command) error
}

func (r runnerFunc) Output(ctx context.Context, command Command) ([]byte, error) {
	return r.output(ctx, command)
}
func (r runnerFunc) Run(ctx context.Context, command Command) error { return r.run(ctx, command) }

func applyTestConfig(t *testing.T, pin Pin, snapshot Snapshot) ApplyConfig {
	t.Helper()
	stateDir := t.TempDir()
	store, err := OpenPinStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(pin); err != nil {
		t.Fatal(err)
	}
	return ApplyConfig{Snapshot: snapshot, SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned", NiriCommand: "niri", StateDir: stateDir, Timeout: time.Second, PollInterval: time.Millisecond}
}

func activeSnapshot(host string, sessions ...string) Snapshot {
	snapshot := Snapshot{Host: host, Profile: "default", ActiveSessions: append([]string{}, sessions...), Windows: []Window{}}
	for i, session := range sessions {
		snapshot.Windows = append(snapshot.Windows, Window{Order: i, AppID: "zellij", Headless: true, ZellijSession: session})
	}
	return snapshot
}

func activeSet(sessions ...string) map[string]struct{} {
	active := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		active[session] = struct{}{}
	}
	return active
}

func pinned(session string, order int) PinnedProjection {
	floating := false
	return PinnedProjection{Session: session, Workspace: model.WorkspaceRef{Name: "work", Index: 1}, Order: order, Placement: model.Placement{IsFloating: &floating}}
}

func testPin(session string) Pin {
	pin := Pin{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{pinned(session, 0)}}
	pin.Projections[0].RemoteCWD = "/tmp"
	floating := true
	pin.Projections[0].Placement.IsFloating = &floating
	pin.Projections[0].Placement.WindowSize = []int{800, 600}
	return pin
}
