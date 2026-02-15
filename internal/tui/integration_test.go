package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jmo/terminal-redeemer/internal/restore"
)

func TestTimestampSelectionReloadsPlan(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	plans := map[int64]restore.Plan{
		t0.UnixNano(): {Items: []restore.Item{{WindowKey: "w-old", Status: restore.StatusReady, Command: "kitty --directory /old", WorkspaceID: "ws-1", AppID: "kitty"}}},
		t1.UnixNano(): {Items: []restore.Item{{WindowKey: "w-new", Status: restore.StatusReady, Command: "kitty --directory /new", WorkspaceID: "ws-2", AppID: "kitty"}}},
	}

	loadedAt := make([]time.Time, 0, 1)
	app := NewAppWithPlanLoader(plans[t0.UnixNano()], []time.Time{t0, t1}, t0, func(ts time.Time) (restore.Plan, error) {
		loadedAt = append(loadedAt, ts)
		return plans[ts.UnixNano()], nil
	})

	app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(loadedAt) != 1 || !loadedAt[0].Equal(t1) {
		t.Fatalf("expected plan loaded at second timestamp, got %#v", loadedAt)
	}

	if len(app.rows) == 0 || app.rows[len(app.rows)-1].windowKey != "w-new" {
		t.Fatalf("expected rows rebuilt from selected timestamp plan, got %#v", app.rows)
	}

	preview := app.model.PreviewLines()
	if len(preview) != 1 || preview[0] != "w-new ready: kitty --directory /new" {
		t.Fatalf("unexpected preview for selected timestamp: %#v", preview)
	}
}

func TestDefaultTimestampSelectionUsesLatest(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	plans := map[int64]restore.Plan{
		t0.UnixNano(): {Items: []restore.Item{{WindowKey: "w-old", Status: restore.StatusReady, Command: "old", WorkspaceID: "ws-1", AppID: "kitty"}}},
		t1.UnixNano(): {Items: []restore.Item{{WindowKey: "w-new", Status: restore.StatusReady, Command: "new", WorkspaceID: "ws-2", AppID: "kitty"}}},
	}

	loadedAt := make([]time.Time, 0, 1)
	app := NewAppWithPlanLoader(plans[t1.UnixNano()], []time.Time{t0, t1}, time.Time{}, func(ts time.Time) (restore.Plan, error) {
		loadedAt = append(loadedAt, ts)
		return plans[ts.UnixNano()], nil
	})

	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(loadedAt) != 1 || !loadedAt[0].Equal(t1) {
		t.Fatalf("expected latest timestamp selected by default, got %#v", loadedAt)
	}
}

func TestSelectionsMapToRestorePlan(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{Items: []restore.Item{
		{WindowKey: "w-1", Status: restore.StatusReady, Command: "kitty --directory /tmp"},
		{WindowKey: "w-2", Status: restore.StatusReady, Command: "code"},
		{WindowKey: "w-3", Status: restore.StatusSkipped},
	}}

	selected := map[string]bool{
		"w-1": true,
		"w-2": false,
		"w-3": true,
	}

	filtered := FilterPlan(plan, selected)

	if filtered.Items[0].Status != restore.StatusReady {
		t.Fatalf("expected first item ready, got %s", filtered.Items[0].Status)
	}
	if filtered.Items[1].Status != restore.StatusSkipped {
		t.Fatalf("expected second item skipped by selection, got %s", filtered.Items[1].Status)
	}
	if filtered.Items[1].Reason != "excluded in tui" {
		t.Fatalf("unexpected exclusion reason: %q", filtered.Items[1].Reason)
	}
	if filtered.Items[2].Status != restore.StatusSkipped {
		t.Fatalf("expected existing skipped item to remain skipped, got %s", filtered.Items[2].Status)
	}
}

func TestFilterPlanExecutionParityForSelectionEdgeCases(t *testing.T) {
	t.Parallel()

	plan := restore.Plan{Items: []restore.Item{
		{WindowKey: "w-ready-selected", Status: restore.StatusReady, Command: "ok"},
		{WindowKey: "w-ready-unselected", Status: restore.StatusReady, Command: "skip-me"},
		{WindowKey: "w-degraded", Status: restore.StatusDegraded, Reason: "missing terminal session tag"},
	}}
	selected := map[string]bool{
		"w-ready-selected":   true,
		"w-ready-unselected": false,
		"w-degraded":         true,
	}

	filtered := FilterPlan(plan, selected)
	executor := restore.NewExecutor(recordingRunner{failOn: ""})
	result := executor.Execute(context.Background(), filtered)

	if result.Summary.Restored != 1 || result.Summary.Skipped != 2 || result.Summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", result.Summary)
	}
	if filtered.Items[1].Status != restore.StatusSkipped || filtered.Items[1].Reason != "excluded in tui" {
		t.Fatalf("expected unselected ready item skipped by tui filter, got %+v", filtered.Items[1])
	}
}

type recordingRunner struct {
	failOn string
}

func (r recordingRunner) Run(_ context.Context, command string) error {
	if command == r.failOn {
		return errors.New("boom")
	}
	return nil
}
