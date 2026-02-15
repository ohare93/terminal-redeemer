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
		{WindowKey: "w-1", Status: restore.StatusReady, Command: "kitty", WorkspaceID: "ws-a"},
		{WindowKey: "w-2", Status: restore.StatusReady, Command: "kitty", WorkspaceID: "ws-a"},
		{WindowKey: "w-3", Status: restore.StatusReady, Command: "kitty", WorkspaceID: "ws-b"},
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
}

func TestActionPreviewGeneration(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{Items: []restore.Item{
		{WindowKey: "w-1", Status: restore.StatusReady, Command: "kitty --directory /tmp"},
		{WindowKey: "w-2", Status: restore.StatusSkipped, Command: "ignored"},
	}}

	m := NewModel(plan, nil)
	preview := m.PreviewLines()
	if len(preview) != 1 {
		t.Fatalf("expected single preview line, got %d", len(preview))
	}
	if preview[0] != "w-1: kitty --directory /tmp" {
		t.Fatalf("unexpected preview line: %q", preview[0])
	}
}
