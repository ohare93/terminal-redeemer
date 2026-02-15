package tui

import (
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/restore"
)

func TestTimestampSelection(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{}
	timestamps := []time.Time{
		time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 15, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC),
	}

	m := NewModel(plan, timestamps)
	m.NextTimestamp()
	m.NextTimestamp()
	m.NextTimestamp()

	if got := m.SelectedTimestamp(); !got.Equal(timestamps[2]) {
		t.Fatalf("expected last timestamp selected, got %s", got)
	}

	m.PrevTimestamp()
	if got := m.SelectedTimestamp(); !got.Equal(timestamps[1]) {
		t.Fatalf("expected second timestamp selected, got %s", got)
	}
}

func TestToggleByWorkspaceAndWindow(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{Items: []restore.Item{
		{WindowKey: "w-1", Status: restore.StatusReady, Command: "kitty", WorkspaceID: "ws-a", AppID: "kitty"},
		{WindowKey: "w-2", Status: restore.StatusReady, Command: "kitty", WorkspaceID: "ws-a", AppID: "kitty"},
		{WindowKey: "w-3", Status: restore.StatusReady, Command: "kitty", WorkspaceID: "ws-b", AppID: "kitty"},
		{WindowKey: "w-4", Status: restore.StatusSkipped, Reason: "app not allowlisted", WorkspaceID: "ws-a", AppID: "firefox"},
	}}

	m := NewModel(plan, nil)
	m.SetMode(ModeItems)

	m.ToggleWorkspace("ws-a")
	if m.IsSelected("w-1") || m.IsSelected("w-2") {
		t.Fatal("expected ws-a windows to be toggled off")
	}
	if !m.IsSelected("w-3") {
		t.Fatal("expected ws-b window to remain selected")
	}

	m.ToggleWindow("w-3")
	if m.IsSelected("w-3") {
		t.Fatal("expected w-3 toggled off")
	}

	m.ToggleWorkspace("ws-a")
	if !m.IsSelected("w-1") || !m.IsSelected("w-2") {
		t.Fatal("expected ws-a ready windows to be toggled back on")
	}
	if m.IsSelected("w-4") {
		t.Fatal("expected non-ready window to remain unselected")
	}

	m.ToggleApp("ws-a", "kitty")
	if m.IsSelected("w-1") || m.IsSelected("w-2") {
		t.Fatal("expected ws-a kitty windows toggled off by app")
	}
	m.ToggleWindow("w-1")

	if got := m.WorkspaceSelectionState("ws-a"); got != SelectionPartial {
		t.Fatalf("expected ws-a selection partial, got %v", got)
	}
	if got := m.AppSelectionState("ws-a", "kitty"); got != SelectionPartial {
		t.Fatalf("expected ws-a/kitty selection partial, got %v", got)
	}
	if got := m.AppSelectionState("ws-a", "firefox"); got != SelectionUnavailable {
		t.Fatalf("expected ws-a/firefox selection unavailable, got %v", got)
	}
}

func TestActionPreviewGeneration(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{Items: []restore.Item{
		{WindowKey: "w-1", Status: restore.StatusReady, Command: "kitty --directory /tmp", AppID: "kitty"},
		{WindowKey: "w-2", Status: restore.StatusSkipped, Reason: "app not allowlisted", AppID: "firefox"},
	}}

	m := NewModel(plan, nil)
	preview := m.PreviewLines()
	if len(preview) != 2 {
		t.Fatalf("expected two preview lines, got %d", len(preview))
	}
	if preview[0] != "w-1 ready: kitty --directory /tmp" {
		t.Fatalf("unexpected preview line: %q", preview[0])
	}
	if preview[1] != "w-2 skipped: app not allowlisted" {
		t.Fatalf("unexpected skipped preview line: %q", preview[1])
	}
}

func TestActionPreviewTracksSelectionsAndDegradedFallbackReason(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{Items: []restore.Item{
		{WindowKey: "w-1", Status: restore.StatusReady, Command: "kitty --directory /tmp", WorkspaceID: "ws-1", AppID: "kitty"},
		{WindowKey: "w-2", Status: restore.StatusDegraded, Reason: "", WorkspaceID: "ws-1", AppID: "kitty"},
	}}

	m := NewModel(plan, nil)
	m.ToggleWindow("w-1")
	preview := m.PreviewLines()

	if len(preview) != 2 {
		t.Fatalf("expected two preview lines, got %d", len(preview))
	}
	if preview[0] != "w-1 skipped: excluded in tui" {
		t.Fatalf("unexpected ready->skipped preview line: %q", preview[0])
	}
	if preview[1] != "w-2 degraded: no reason" {
		t.Fatalf("unexpected degraded fallback preview line: %q", preview[1])
	}
}
