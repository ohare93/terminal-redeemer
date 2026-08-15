package resume

import (
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
)

func TestSelectLatestPriorBootWithoutFallingBackFromEmpty(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	checkpoints := []checkpoints.Checkpoint{
		{ObservedAt: now.Add(-2 * time.Hour), Host: "local", Profile: "default", BootID: "boot-old", State: stateWithTerminal("old", "old-session", model.WorkspaceRef{Index: 1})},
		{ObservedAt: now.Add(-time.Hour), Host: "local", Profile: "default", BootID: "boot-newer", State: model.State{}},
		{ObservedAt: now.Add(-time.Minute), Host: "local", Profile: "default", BootID: "boot-current", State: stateWithTerminal("current", "current-session", model.WorkspaceRef{Index: 1})},
	}

	got := Select(checkpoints, SelectOptions{CurrentBootID: "boot-current", Host: "local", Profile: "default", Now: now, MaxAge: 24 * time.Hour})
	if got.Status != CandidateEmpty {
		t.Fatalf("status = %q, want empty", got.Status)
	}
	if got.Checkpoint == nil || got.Checkpoint.BootID != "boot-newer" {
		t.Fatalf("selected checkpoint = %#v", got.Checkpoint)
	}
}

func TestSelectExcludesLegacyAndWrongPartition(t *testing.T) {
	now := time.Now().UTC()
	checkpoints := []checkpoints.Checkpoint{
		{ObservedAt: now.Add(-time.Minute), Host: "local", Profile: "default", State: stateWithTerminal("legacy", "legacy", model.WorkspaceRef{Index: 1})},
		{ObservedAt: now.Add(-2 * time.Minute), Host: "other", Profile: "default", BootID: "other-host", State: stateWithTerminal("other", "other", model.WorkspaceRef{Index: 1})},
	}
	got := Select(checkpoints, SelectOptions{CurrentBootID: "current", Host: "local", Profile: "default", Now: now, MaxAge: time.Hour})
	if got.Status != CandidateNotFound || got.Checkpoint != nil {
		t.Fatalf("selection = %#v, want not found", got)
	}
}

func TestSelectMarksLatestCandidateStaleWithoutFallback(t *testing.T) {
	now := time.Now().UTC()
	got := Select([]checkpoints.Checkpoint{
		{ObservedAt: now.Add(-48 * time.Hour), BootID: "older", State: stateWithTerminal("older", "older", model.WorkspaceRef{Index: 1})},
		{ObservedAt: now.Add(-25 * time.Hour), BootID: "latest", State: stateWithTerminal("latest", "latest", model.WorkspaceRef{Index: 1})},
	}, SelectOptions{CurrentBootID: "current", Now: now, MaxAge: 24 * time.Hour})
	if got.Status != CandidateStale || got.Checkpoint.BootID != "latest" {
		t.Fatalf("selection = %#v", got)
	}
}

func TestPlannerReconcilesAlreadyOpenDuplicateUnavailableAndReady(t *testing.T) {
	now := time.Now().UTC()
	captured := model.State{
		Windows: []model.Window{
			terminalWindow("w-open-a", "open", model.WorkspaceRef{Name: "dev", Output: "DP-1", Index: 2}),
			terminalWindow("w-open-b", "open", model.WorkspaceRef{Name: "dev", Output: "DP-1", Index: 2}),
			terminalWindow("w-duplicate-a", "duplicate", model.WorkspaceRef{Index: 2}),
			terminalWindow("w-duplicate-b", "duplicate", model.WorkspaceRef{Index: 2}),
			terminalWindow("w-missing", "missing", model.WorkspaceRef{Index: 2}),
			terminalWindow("w-ready", "ready", model.WorkspaceRef{Name: "dev", Index: 9}),
		},
	}
	selection := Selection{Status: CandidateReady, Checkpoint: &checkpoints.Checkpoint{ObservedAt: now, BootID: "prior", State: captured}}
	current := model.State{
		Workspaces: []model.Workspace{{ID: "runtime-dev", Name: "dev", Output: "DP-2", Index: 4}, {ID: "runtime-two", Output: "DP-1", Index: 2}},
		Windows:    []model.Window{terminalWindow("current", "open", model.WorkspaceRef{})},
	}

	plan := NewPlanner(PlannerConfig{UnresolvedWorkspace: UnresolvedSkip}).Build(selection, current, []string{"open", "duplicate", "ready"})
	assertItemStatus(t, plan, "w-open-a", StatusAlreadyOpen)
	assertItemStatus(t, plan, "w-open-b", StatusSkipped)
	assertItemStatus(t, plan, "w-duplicate-a", StatusReady)
	assertItemStatus(t, plan, "w-duplicate-b", StatusSkipped)
	assertItemStatus(t, plan, "w-missing", StatusUnavailable)
	ready := assertItemStatus(t, plan, "w-ready", StatusReady)
	if ready.Workspace == nil || ready.Workspace.Method != "name" || ready.Workspace.ID != "runtime-dev" {
		t.Fatalf("ready workspace = %#v", ready.Workspace)
	}
	if plan.Summary.Ready != 2 || plan.Summary.AlreadyOpen != 1 || plan.Summary.Unavailable != 1 || plan.Summary.Skipped != 2 {
		t.Fatalf("summary = %#v", plan.Summary)
	}
}

func TestPlannerMissingSessionIsUnavailable(t *testing.T) {
	window := terminalWindow("w-no-session", "", model.WorkspaceRef{Index: 1})
	selection := readySelection(model.State{Windows: []model.Window{window}})
	plan := NewPlanner(PlannerConfig{}).Build(selection, model.State{Workspaces: []model.Workspace{{ID: "1", Index: 1}}}, nil)
	assertItemStatus(t, plan, "w-no-session", StatusUnavailable)
}

func TestResolveWorkspacePreferenceAndFallbacks(t *testing.T) {
	current := []model.Workspace{
		{ID: "by-index", Name: "wrong", Output: "HDMI-A-1", Index: 3},
		{ID: "by-output-index", Name: "also-wrong", Output: "DP-1", Index: 3},
		{ID: "by-name", Name: "work", Output: "DP-2", Index: 8},
	}

	tests := []struct {
		name string
		ref  model.WorkspaceRef
		id   string
		via  string
	}{
		{name: "name wins", ref: model.WorkspaceRef{Name: "work", Output: "DP-1", Index: 3}, id: "by-name", via: "name"},
		{name: "output and index", ref: model.WorkspaceRef{Name: "gone", Output: "DP-1", Index: 3}, id: "by-output-index", via: "output_index"},
		{name: "index", ref: model.WorkspaceRef{Name: "gone", Output: "gone", Index: 3}, id: "by-index", via: "index"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveWorkspace(tt.ref, current)
			if !ok || got.ID != tt.id || got.Method != tt.via {
				t.Fatalf("ResolveWorkspace() = %#v, %v", got, ok)
			}
		})
	}
}

func TestUnresolvedWorkspacePolicies(t *testing.T) {
	selection := readySelection(stateWithTerminal("w", "session", model.WorkspaceRef{Name: "gone"}))
	tests := []struct {
		name   string
		policy UnresolvedWorkspacePolicy
		status Status
	}{
		{name: "default", policy: "", status: StatusDegraded},
		{name: "explicit skip", policy: UnresolvedSkip, status: StatusSkipped},
		{name: "explicit current", policy: UnresolvedCurrent, status: StatusDegraded},
		{name: "explicit fail", policy: UnresolvedFail, status: StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := NewPlanner(PlannerConfig{UnresolvedWorkspace: tt.policy}).Build(selection, model.State{}, []string{"session"})
			assertItemStatus(t, plan, "w", tt.status)
		})
	}
}

func TestStalePlanDoesNotRequireLiveStateOrAvailability(t *testing.T) {
	selection := readySelection(stateWithTerminal("w", "session", model.WorkspaceRef{Index: 1}))
	selection.Status = CandidateStale
	selection.Reason = "checkpoint exceeds maximum age"
	plan := NewPlanner(PlannerConfig{}).Build(selection, model.State{}, nil)
	assertItemStatus(t, plan, "w", StatusStale)
	if plan.Summary.Stale != 1 {
		t.Fatalf("summary = %#v", plan.Summary)
	}
}

func readySelection(state model.State) Selection {
	return Selection{Status: CandidateReady, Checkpoint: &checkpoints.Checkpoint{ObservedAt: time.Now().UTC(), BootID: "prior", State: state}}
}

func stateWithTerminal(key, session string, ref model.WorkspaceRef) model.State {
	return model.State{Windows: []model.Window{terminalWindow(key, session, ref)}}
}

func terminalWindow(key, session string, ref model.WorkspaceRef) model.Window {
	return model.Window{Key: key, AppID: "kitty", WorkspaceRef: &ref, Terminal: &model.Terminal{SessionTag: session, CWD: "/tmp"}}
}

func assertItemStatus(t *testing.T, plan Plan, key string, status Status) Item {
	t.Helper()
	for _, item := range plan.Items {
		if item.WindowKey == key {
			if item.Status != status {
				t.Fatalf("item %s status = %q, want %q (item=%#v)", key, item.Status, status, item)
			}
			return item
		}
	}
	t.Fatalf("item %s not found in %#v", key, plan.Items)
	return Item{}
}
