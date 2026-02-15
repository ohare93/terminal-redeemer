package model

import "testing"

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
