package diff

import (
	"testing"

	"github.com/jmo/terminal-redeemer/internal/model"
)

func TestUnchangedStateEmitsNoPatches(t *testing.T) {
	t.Parallel()

	state := model.State{
		Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}},
		Windows:    []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "shell"}},
	}

	engine := NewEngine()
	patches, changed, err := engine.Diff(state, state)
	if err != nil {
		t.Fatalf("diff unchanged: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false")
	}
	if len(patches) != 0 {
		t.Fatalf("expected no patches, got %d", len(patches))
	}
}

func TestSingleFieldChangeEmitsSparsePatch(t *testing.T) {
	t.Parallel()

	before := model.State{
		Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}},
		Windows:    []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "old title"}},
	}
	after := model.State{
		Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}},
		Windows:    []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "new title"}},
	}

	engine := NewEngine()
	patches, changed, err := engine.Diff(before, after)
	if err != nil {
		t.Fatalf("diff single field: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if len(patches) != 1 {
		t.Fatalf("expected one patch, got %d", len(patches))
	}

	patch := patches[0]
	if patch.WindowKey != "w-1" {
		t.Fatalf("expected patch for w-1, got %q", patch.WindowKey)
	}
	if len(patch.Fields) != 1 {
		t.Fatalf("expected sparse patch with one field, got %#v", patch.Fields)
	}
	if patch.Fields["title"] != "new title" {
		t.Fatalf("expected title patch, got %#v", patch.Fields)
	}
}

func TestOptionalFieldAddRemoveBehavior(t *testing.T) {
	t.Parallel()

	baseWindow := model.Window{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "shell"}
	withMeta := baseWindow
	withMeta.Terminal = &model.Terminal{CWD: "/tmp", SessionTag: "sess-a"}

	engine := NewEngine()

	addPatches, changed, err := engine.Diff(
		model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{baseWindow}},
		model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{withMeta}},
	)
	if err != nil {
		t.Fatalf("diff add optional: %v", err)
	}
	if !changed || len(addPatches) != 1 {
		t.Fatalf("expected one add patch, changed=%v len=%d", changed, len(addPatches))
	}
	if addPatches[0].Fields["terminal"] == nil {
		t.Fatalf("expected terminal add payload, got %#v", addPatches[0].Fields)
	}

	removePatches, changed, err := engine.Diff(
		model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{withMeta}},
		model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{baseWindow}},
	)
	if err != nil {
		t.Fatalf("diff remove optional: %v", err)
	}
	if !changed || len(removePatches) != 1 {
		t.Fatalf("expected one remove patch, changed=%v len=%d", changed, len(removePatches))
	}
	if value, ok := removePatches[0].Fields["terminal"]; !ok || value != nil {
		t.Fatalf("expected terminal nil tombstone, got %#v", removePatches[0].Fields)
	}
}
