package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStateHashStableAcrossEquivalentOrdering(t *testing.T) {
	t.Parallel()

	stateA := State{
		Workspaces: []Workspace{
			{ID: "ws-2", Index: 2, Name: "code"},
			{ID: "ws-1", Index: 1, Name: "web"},
		},
		Windows: []Window{
			{Key: "w-2", AppID: "kitty", WorkspaceID: "ws-2", Title: "editor"},
			{Key: "w-1", AppID: "firefox", WorkspaceID: "ws-1", Title: "docs"},
		},
	}

	stateB := State{
		Workspaces: []Workspace{
			{ID: "ws-1", Index: 1, Name: "web"},
			{ID: "ws-2", Index: 2, Name: "code"},
		},
		Windows: []Window{
			{Key: "w-1", AppID: "firefox", WorkspaceID: "ws-1", Title: "docs"},
			{Key: "w-2", AppID: "kitty", WorkspaceID: "ws-2", Title: "editor"},
		},
	}

	hashA, err := stateA.Hash()
	if err != nil {
		t.Fatalf("hash stateA: %v", err)
	}
	hashB, err := stateB.Hash()
	if err != nil {
		t.Fatalf("hash stateB: %v", err)
	}

	if hashA != hashB {
		t.Fatalf("expected stable hash for equivalent states: %q != %q", hashA, hashB)
	}
}

func TestStateHashIgnoresWindowTitles(t *testing.T) {
	t.Parallel()

	before := State{Windows: []Window{{Key: "w-1", AppID: "kitty", Title: "running ⠐"}}}
	after := State{Windows: []Window{{Key: "w-1", AppID: "kitty", Title: "running ⠂"}}}

	beforeHash, err := before.Hash()
	if err != nil {
		t.Fatal(err)
	}
	afterHash, err := after.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if beforeHash != afterHash {
		t.Fatalf("title-only change affected semantic hash: %q != %q", beforeHash, afterHash)
	}
	if Normalize(after).Windows[0].Title != "running ⠂" {
		t.Fatal("Normalize must preserve the latest title in stored state")
	}
}

func TestStateHashTracksWorkspaceAndPlacementMetadata(t *testing.T) {
	t.Parallel()

	floating := false
	column := 2
	base := State{
		Workspaces: []Workspace{{ID: "runtime-7", Index: 1, Name: "work", Output: "DP-1"}},
		Windows: []Window{{
			Key: "w-1", AppID: "kitty", WorkspaceID: "runtime-7",
			WorkspaceRef: &WorkspaceRef{Name: "work", Index: 1, Output: "DP-1"},
			Placement:    &Placement{Column: &column, IsFloating: &floating, TileSize: []float64{800, 600}},
		}},
	}
	baseHash, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]State{
		"workspace output": func() State {
			changed := Normalize(base)
			changed.Workspaces[0].Output = "HDMI-A-1"
			return changed
		}(),
		"durable reference": func() State {
			changed := Normalize(base)
			changed.Windows[0].WorkspaceRef.Output = "HDMI-A-1"
			return changed
		}(),
		"placement": func() State {
			changed := Normalize(base)
			changed.Windows[0].Placement.TileSize[0] = 900
			return changed
		}(),
	}
	for name, changed := range cases {
		changedHash, err := changed.Hash()
		if err != nil {
			t.Fatalf("%s hash: %v", name, err)
		}
		if changedHash == baseHash {
			t.Fatalf("%s did not change state hash", name)
		}
	}

	normalized := Normalize(base)
	normalized.Windows[0].Placement.TileSize[0] = 1
	if base.Windows[0].Placement.TileSize[0] != 800 {
		t.Fatal("Normalize aliased placement slices")
	}
}

func TestOptionalPlacementFieldsOmitFromLegacyJSON(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(Window{Key: "w-1", AppID: "kitty", WorkspaceID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "workspace_ref") || strings.Contains(string(payload), "placement") {
		t.Fatalf("optional additive fields should be omitted: %s", payload)
	}
}

func TestNormalizeStateOrderingInvariants(t *testing.T) {
	t.Parallel()

	input := State{
		Workspaces: []Workspace{
			{ID: "ws-b", Index: 3},
			{ID: "ws-a", Index: 1},
			{ID: "ws-c", Index: 2},
		},
		Windows: []Window{
			{Key: "w-c", WorkspaceID: "ws-c", AppID: "kitty"},
			{Key: "w-a", WorkspaceID: "ws-a", AppID: "kitty"},
			{Key: "w-b", WorkspaceID: "ws-b", AppID: "kitty"},
		},
	}

	norm := Normalize(input)

	if norm.Workspaces[0].ID != "ws-a" || norm.Workspaces[1].ID != "ws-c" || norm.Workspaces[2].ID != "ws-b" {
		t.Fatalf("unexpected workspace order: %+v", norm.Workspaces)
	}
	if norm.Windows[0].Key != "w-a" || norm.Windows[1].Key != "w-b" || norm.Windows[2].Key != "w-c" {
		t.Fatalf("unexpected window order: %+v", norm.Windows)
	}
}
