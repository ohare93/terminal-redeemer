package niri

import "testing"

func TestParseSnapshotFixture(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "workspaces": [
    {"id": "ws-1", "idx": 1, "name": "main"},
    {"id": "ws-2", "idx": 2, "name": "code"}
  ],
  "windows": [
    {"id": 101, "app_id": "kitty", "title": "shell", "workspace_id": "ws-2", "pid": 4242},
    {"id": 102, "app_id": "firefox", "title": "docs", "workspace_id": "ws-1", "pid": 5252}
  ]
}`)

	state, err := ParseSnapshot(raw)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}

	if len(state.Workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(state.Workspaces))
	}
	if len(state.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(state.Windows))
	}
	var kittyPID int
	for _, window := range state.Windows {
		if window.AppID == "kitty" {
			kittyPID = window.PID
		}
	}
	if kittyPID != 4242 {
		t.Fatalf("expected kitty pid 4242, got %d", kittyPID)
	}
}

func TestParseSnapshotWindowsArray(t *testing.T) {
	t.Parallel()

	raw := []byte(`[
  {"id": 101, "app_id": "kitty", "title": "shell", "workspace_id": 2, "pid": 4242},
  {"id": 102, "app_id": "firefox", "title": "docs", "workspace_id": 1, "pid": 5252}
]`)

	state, err := ParseSnapshot(raw)
	if err != nil {
		t.Fatalf("parse snapshot windows array: %v", err)
	}

	if len(state.Workspaces) != 0 {
		t.Fatalf("expected 0 workspaces for windows-only input, got %d", len(state.Workspaces))
	}
	if len(state.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(state.Windows))
	}
	if state.Windows[0].WorkspaceID == "" || state.Windows[1].WorkspaceID == "" {
		t.Fatalf("expected workspace ids normalized to strings, got %#v", state.Windows)
	}
}

func TestParseSnapshotWorkspaceIDsNormalizeFromNumbers(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "workspaces": [
    {"id": 5, "idx": 2, "name": null},
    {"id": 2, "idx": 1, "name": null}
  ],
  "windows": []
}`)

	state, err := ParseSnapshot(raw)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	if len(state.Workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(state.Workspaces))
	}
	if state.Workspaces[0].ID == "" || state.Workspaces[1].ID == "" {
		t.Fatalf("expected normalized workspace IDs, got %#v", state.Workspaces)
	}
}
