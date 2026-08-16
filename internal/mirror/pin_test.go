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

func TestPinStoreAtomicModeCorruptionAndPruneIsolation(t *testing.T) {
	root := t.TempDir()
	store, err := NewPinStore(root)
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
	got, err := store.Read("lattice", "default")
	if err != nil || !reflect.DeepEqual(got, pin) {
		t.Fatalf("read=%#v err=%v", got, err)
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

func TestPinValidationRejectsUntypedOrUnboundedData(t *testing.T) {
	cases := []Pin{
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "bad\n", Workspace: WorkspaceSelector{Index: 1}}}},
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "ok", RemoteCWD: strings.Repeat("x", 4097), Workspace: WorkspaceSelector{Index: 1}}}},
		{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: "ok", Workspace: WorkspaceSelector{}}}},
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

func TestBuildPinJoinsExactSessionsInSourceOrderAcrossWorkspaces(t *testing.T) {
	snapshot := Snapshot{Host: "lattice", Profile: "default", Windows: []Window{
		{Order: 5, SourceWindowID: 2, AppID: "kitty", ZellijSession: "B", Terminal: &Terminal{CWD: "/b", ZellijSession: "B"}},
		{Order: 1, SourceWindowID: 1, AppID: "kitty", ZellijSession: "A", Terminal: &Terminal{CWD: "/a", ZellijSession: "A"}},
	}}
	windows := []OwnedWindow{
		{ID: 10, WorkspaceID: 7, IsFloating: true, Layout: OwnedLayout{WindowSize: []int{800, 600}}},
		{ID: 11, WorkspaceID: 8, Layout: OwnedLayout{TileSize: []float64{900, 700}}},
	}
	inventory := ProjectionInventory{Exact: []Projection{
		{Window: windows[0], SourceHost: "lattice", Session: "B"},
		{Window: windows[1], SourceHost: "lattice", Session: "A"},
	}, Untracked: []OwnedWindow{{ID: 99}}}
	result, err := BuildPin(snapshot, "lattice", windows, []OwnedWorkspace{{ID: 7, Index: 2, Name: "two"}, {ID: 8, Index: 1, Name: "one"}}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Untracked != 1 || len(result.Pin.Projections) != 2 {
		t.Fatalf("result=%#v", result)
	}
	if result.Pin.Projections[0].Session != "A" || result.Pin.Projections[0].Workspace.Name != "one" || result.Pin.Projections[1].Session != "B" || result.Pin.Projections[1].RemoteCWD != "/b" {
		t.Fatalf("pin=%#v", result.Pin)
	}
}

func TestPrepareApplyMissingDuplicateCaseDistinctAndNoop(t *testing.T) {
	pin := testPin("Alpha")
	pin.Projections = append(pin.Projections, PinnedProjection{Session: "alpha", Workspace: WorkspaceSelector{Index: 2}, Order: 1})
	snapshot := Snapshot{Host: "lattice", Profile: "default", Windows: []Window{{Order: 0, AppID: "zellij", Headless: true, ZellijSession: "Alpha"}}}
	inventory := ProjectionInventory{Exact: []Projection{{SourceHost: "other", Session: "Alpha"}}}
	result := prepareApply(pin, snapshot, inventory, []OwnedWorkspace{{ID: 1, Index: 1}, {ID: 2, Index: 2}})
	if result.Items[0].Status != ApplyReady || result.Items[1].Status != ApplyMissing {
		t.Fatalf("items=%#v", result.Items)
	}
	inventory.Exact = []Projection{{SourceHost: "lattice", Session: "Alpha"}, {SourceHost: "lattice", Session: "Alpha"}}
	result = prepareApply(pin, snapshot, inventory, nil)
	if result.Items[0].Status != ApplyAmbiguous {
		t.Fatalf("items=%#v", result.Items)
	}
	empty := testPin("unused")
	empty.Projections = []PinnedProjection{}
	if got := prepareApply(empty, Snapshot{}, ProjectionInventory{}, nil); len(got.Items) != 0 {
		t.Fatalf("noop=%#v", got)
	}
}

type testProjectionInspector struct {
	inspect func([]OwnedWindow) ProjectionInventory
}

func (i testProjectionInspector) Inspect(_ context.Context, windows []OwnedWindow) (ProjectionInventory, error) {
	return i.inspect(windows), nil
}

type pinRunner struct {
	commands    []Command
	failSession string
}

func (r *pinRunner) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}
func (r *pinRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	if command.Name == "kitty" && strings.Contains(strings.Join(command.Args, " "), r.failSession) {
		return errors.New("launch failed")
	}
	return nil
}

func TestApplyPinnedPartialLaunchPlacementAndDuplicateIdempotence(t *testing.T) {
	pin := testPin("A")
	pin.Projections = append(pin.Projections, PinnedProjection{Session: "B", Workspace: WorkspaceSelector{Index: 1}, Order: 1})
	snapshot := Snapshot{Host: "lattice", Profile: "default", Windows: []Window{
		{Order: 0, AppID: "zellij", Headless: true, ZellijSession: "A"},
		{Order: 1, AppID: "zellij", Headless: true, ZellijSession: "B"},
	}}
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
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) { return []OwnedWorkspace{{ID: 1, Index: 1}}, nil },
		Inspector: testProjectionInspector{inspect: func(windows []OwnedWindow) ProjectionInventory {
			if len(windows) == 1 {
				return ProjectionInventory{Exact: []Projection{{Window: windows[0], SourceHost: "lattice", Session: "A"}}}
			}
			return ProjectionInventory{}
		}},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := ApplyPinned(ctx, ApplyConfig{Pin: pin, Snapshot: snapshot, SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned", NiriCommand: "niri", Timeout: time.Second, PollInterval: time.Millisecond}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != ApplyOpened || result.Items[0].WindowID != 10 || result.Items[1].Status != ApplyFailed {
		t.Fatalf("result=%#v", result)
	}
	if len(runner.commands) < 4 {
		t.Fatalf("commands=%#v", runner.commands)
	}

	duplicateRunner := &pinRunner{}
	deps.Runner = duplicateRunner
	deps.ListWindows = func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{{ID: 10, PID: 100}}, nil }
	one := testPin("A")
	result, err = ApplyPinned(ctx, ApplyConfig{Pin: one, Snapshot: snapshot, SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned", NiriCommand: "niri", Timeout: time.Second, PollInterval: time.Millisecond}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != ApplyAlreadyOpen || len(duplicateRunner.commands) != 0 {
		t.Fatalf("duplicate apply mutated: %#v commands=%#v", result, duplicateRunner.commands)
	}
}

func testPin(session string) Pin {
	return Pin{V: 1, SourceHost: "lattice", SourceProfile: "default", Projections: []PinnedProjection{{Session: session, RemoteCWD: "/tmp", Workspace: WorkspaceSelector{Name: "work", Index: 1}, Order: 0, IsFloating: true, WindowSize: []int{800, 600}}}}
}
