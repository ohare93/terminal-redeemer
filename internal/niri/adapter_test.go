package niri

import (
	"os"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/model"
)

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

func TestParseSnapshotCapturesDurableWorkspaceAndOptionalPlacementFixture(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/placement.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	state, err := ParseSnapshot(raw)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	if got := state.Workspaces[0]; got.Name != "work" || got.Index != 1 || got.Output != "DP-1" || got.ID != "10" {
		t.Fatalf("named workspace metadata lost: %#v", got)
	}
	if got := state.Workspaces[1]; got.Name != "" || got.Index != 2 || got.Output != "HDMI-A-1" || got.ID != "20" {
		t.Fatalf("unnamed workspace metadata lost: %#v", got)
	}

	byTitle := make(map[string]model.Window, len(state.Windows))
	for _, window := range state.Windows {
		byTitle[window.Title] = window
	}
	named := byTitle["named"]
	if named.WorkspaceID != "10" || named.WorkspaceRef == nil {
		t.Fatalf("workspace evidence/reference missing: %#v", named)
	}
	if ref := *named.WorkspaceRef; ref.Name != "work" || ref.Output != "DP-1" || ref.Index != 1 {
		t.Fatalf("durable named workspace reference = %#v", ref)
	}
	if named.Placement == nil || named.Placement.Column == nil || *named.Placement.Column != 3 || named.Placement.Row == nil || *named.Placement.Row != 1 {
		t.Fatalf("column/row placement missing: %#v", named.Placement)
	}
	if named.Placement.IsFloating == nil || *named.Placement.IsFloating {
		t.Fatalf("observed false floating state missing: %#v", named.Placement)
	}
	if len(named.Placement.TileSize) != 2 || named.Placement.TileSize[0] != 800.5 || len(named.Placement.WindowSize) != 2 {
		t.Fatalf("tile dimensions missing: %#v", named.Placement)
	}

	unnamed := byTitle["unnamed"]
	if unnamed.WorkspaceRef == nil || unnamed.WorkspaceRef.Name != "" || unnamed.WorkspaceRef.Output != "HDMI-A-1" || unnamed.WorkspaceRef.Index != 2 {
		t.Fatalf("unnamed durable reference missing output/index: %#v", unnamed.WorkspaceRef)
	}
	if unnamed.Placement == nil || unnamed.Placement.IsFloating == nil || !*unnamed.Placement.IsFloating || unnamed.Placement.Column != nil {
		t.Fatalf("optional floating placement not preserved: %#v", unnamed.Placement)
	}

	legacy := byTitle["legacy-fields-absent"]
	if legacy.Placement != nil {
		t.Fatalf("absent optional layout should remain omitted: %#v", legacy.Placement)
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
