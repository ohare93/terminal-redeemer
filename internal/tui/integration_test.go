package tui

import (
	"testing"

	"github.com/jmo/terminal-redeemer/internal/restore"
)

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
