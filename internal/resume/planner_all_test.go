package resume

import (
	"reflect"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

func TestAllSameBootTargetsOnlyExactActiveCatalogWithStickyFallback(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	column := 3
	observed := now.Add(-time.Minute)
	checkpoint := checkpoints.Checkpoint{
		V: checkpoints.SchemaVersion, BootID: "current", Host: "local", Profile: "default", ObservedAt: now.Add(-time.Second),
		Recovery: model.RecoveryInventory{
			ActiveSessions: []string{"placed", "old-catalog-name"},
			Sessions: []model.RecoverySession{
				{Name: "placed", CWD: "/work", WorkspaceRef: &model.WorkspaceRef{Name: "dev"}, Placement: &model.Placement{Column: &column}, PlacementObservedAt: &observed},
				{Name: "old-catalog-name"},
			},
		},
	}
	selection := SelectAll([]checkpoints.Checkpoint{checkpoint}, SelectOptions{CurrentBootID: "current", Host: "local", Profile: "default", Now: now, MaxAge: time.Hour})
	catalog := testCatalog(
		zellijlive.Session{Name: "placed", Status: zellijlive.StatusActive},
		zellijlive.Session{Name: "new-active", Status: zellijlive.StatusActive},
		zellijlive.Session{Name: "unrelated-dead", Status: zellijlive.StatusDeadResurrectable},
	)
	current := model.State{Workspaces: []model.Workspace{{ID: "ws-dev", Index: 2, Name: "dev"}}}
	plan := NewPlanner(PlannerConfig{}).BuildAll(selection, current, catalog, AllOptions{Now: now, MaxAge: time.Hour})

	if plan.CandidateSource != CandidateSourceCurrentActive || len(plan.Items) != 2 {
		t.Fatalf("plan source/items = %q/%#v", plan.CandidateSource, plan.Items)
	}
	placed := assertSessionStatus(t, plan, "placed", StatusReady)
	if placed.ZellijStatus != string(zellijlive.StatusActive) || placed.PlacementSource != PlacementSourceCurrentSticky || placed.Workspace == nil || placed.Workspace.ID != "ws-dev" || placed.CapturedPlacement == nil || *placed.CapturedPlacement.Column != 3 {
		t.Fatalf("placed item lost exact evidence: %#v", placed)
	}
	fallback := assertSessionStatus(t, plan, "new-active", StatusDegraded)
	if fallback.PlacementSource != PlacementSourceNone || fallback.Reason != "sticky workspace or placement unavailable; launch on current workspace" {
		t.Fatalf("fallback not explicit: %#v", fallback)
	}
	assertNoSession(t, plan, "unrelated-dead")
	assertNoSession(t, plan, "old-catalog-name")
}

func TestAllRebootIntersectsNewestPriorAllowListWithFullCatalogStatuses(t *testing.T) {
	now := time.Now().UTC()
	prior := allCheckpoint("prior", now.Add(-time.Hour), "active", "dead", "missing", "duplicate", "invalid", "prefix")
	older := allCheckpoint("older", now.Add(-2*time.Hour), "historical-dead")
	selection := SelectAll([]checkpoints.Checkpoint{older, prior}, SelectOptions{CurrentBootID: "current", Now: now, MaxAge: 24 * time.Hour})
	catalog := testCatalog(
		zellijlive.Session{Name: "active", Status: zellijlive.StatusActive},
		zellijlive.Session{Name: "dead", Status: zellijlive.StatusDeadResurrectable},
		zellijlive.Session{Name: "missing", Status: zellijlive.StatusMissing},
		zellijlive.Session{Name: "duplicate", Status: zellijlive.StatusDuplicate},
		zellijlive.Session{Name: "invalid", Status: zellijlive.StatusSocketInvalid},
		zellijlive.Session{Name: "prefix", Status: zellijlive.StatusPrefixOnly},
		zellijlive.Session{Name: "historical-dead", Status: zellijlive.StatusDeadResurrectable},
		zellijlive.Session{Name: "cache-only", Status: zellijlive.StatusDeadResurrectable},
	)
	plan := NewPlanner(PlannerConfig{}).BuildAll(selection, model.State{}, catalog, AllOptions{Now: now, MaxAge: 24 * time.Hour})

	if plan.BootID != "prior" || plan.CandidateSource != CandidateSourcePriorActive || len(plan.Items) != 6 {
		t.Fatalf("wrong prior allow-list: %#v", plan)
	}
	assertSessionStatus(t, plan, "active", StatusDegraded)
	assertSessionStatus(t, plan, "dead", StatusDegraded)
	for _, name := range []string{"missing", "duplicate", "invalid", "prefix"} {
		item := assertSessionStatus(t, plan, name, StatusUnavailable)
		if item.ZellijStatus == "" || item.Reason == "" {
			t.Fatalf("exclusion lacks exact reason: %#v", item)
		}
	}
	assertNoSession(t, plan, "historical-dead")
	assertNoSession(t, plan, "cache-only")
}

func TestAllStalePriorBlocksOnlyDeadResurrectionAndOldPlacementOnlyWarns(t *testing.T) {
	now := time.Now().UTC()
	observed := now.Add(-72 * time.Hour)
	column := 1
	checkpoint := allCheckpoint("prior", observed, "active", "dead")
	for i := range checkpoint.Recovery.Sessions {
		checkpoint.Recovery.Sessions[i].WorkspaceRef = &model.WorkspaceRef{Index: 1}
		checkpoint.Recovery.Sessions[i].Placement = &model.Placement{Column: &column}
		checkpoint.Recovery.Sessions[i].PlacementObservedAt = &observed
	}
	selection := SelectAll([]checkpoints.Checkpoint{checkpoint}, SelectOptions{CurrentBootID: "current", Now: now, MaxAge: 24 * time.Hour})
	catalog := testCatalog(
		zellijlive.Session{Name: "active", Status: zellijlive.StatusActive},
		zellijlive.Session{Name: "dead", Status: zellijlive.StatusDeadResurrectable},
	)
	current := model.State{Workspaces: []model.Workspace{{ID: "one", Index: 1}}}
	plan := NewPlanner(PlannerConfig{}).BuildAll(selection, current, catalog, AllOptions{Now: now, MaxAge: 24 * time.Hour})

	active := assertSessionStatus(t, plan, "active", StatusReady)
	if active.PlacementWarning == "" || active.Workspace == nil {
		t.Fatalf("old active placement blocked or warning absent: %#v", active)
	}
	assertSessionStatus(t, plan, "dead", StatusStale)
}

func TestAllAlreadyOpenRetainsExactNiriIdentityAndRerunPlanIsIdempotent(t *testing.T) {
	now := time.Now().UTC()
	checkpoint := allCheckpoint("current", now, "open")
	selection := SelectAll([]checkpoints.Checkpoint{checkpoint}, SelectOptions{CurrentBootID: "current", Now: now, MaxAge: time.Hour})
	catalog := testCatalog(zellijlive.Session{Name: "open", Status: zellijlive.StatusActive})
	current := model.State{Windows: []model.Window{{Key: "w:kitty:417", AppID: "kitty", Terminal: &model.Terminal{SessionTag: "open", SessionTagExact: true}}}}
	planner := NewPlanner(PlannerConfig{})
	first := planner.BuildAll(selection, current, catalog, AllOptions{Now: now, MaxAge: time.Hour})
	second := planner.BuildAll(selection, current, catalog, AllOptions{Now: now, MaxAge: time.Hour})

	item := assertSessionStatus(t, first, "open", StatusAlreadyOpen)
	if item.CurrentWindowKey != "w:kitty:417" || item.CurrentWindowID != 417 {
		t.Fatalf("current identity lost: %#v", item)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same evidence produced different rerun plans:\n%#v\n%#v", first, second)
	}
}

func TestSelectAllRequiresSchemaThreeAndDoesNotFallbackFromNewestEmptyPrior(t *testing.T) {
	now := time.Now().UTC()
	legacy := allCheckpoint("legacy", now.Add(-time.Minute), "legacy")
	legacy.V = 2
	empty := allCheckpoint("empty", now.Add(-2*time.Minute))
	older := allCheckpoint("older", now.Add(-time.Hour), "older")
	selection := SelectAll([]checkpoints.Checkpoint{older, empty, legacy}, SelectOptions{CurrentBootID: "current", Now: now, MaxAge: time.Hour})
	if selection.Status != CandidateEmpty || selection.Checkpoint == nil || selection.Checkpoint.BootID != "empty" {
		t.Fatalf("selection fell back or used legacy inventory: %#v", selection)
	}
}

func allCheckpoint(boot string, observed time.Time, sessions ...string) checkpoints.Checkpoint {
	metadata := make([]model.RecoverySession, 0, len(sessions))
	for _, name := range sessions {
		metadata = append(metadata, model.RecoverySession{Name: name})
	}
	return checkpoints.Checkpoint{
		V: checkpoints.SchemaVersion, BootID: boot, ObservedAt: observed,
		Recovery: model.RecoveryInventory{ActiveSessions: append([]string(nil), sessions...), Sessions: metadata},
	}
}

func testCatalog(sessions ...zellijlive.Session) zellijlive.Catalog {
	catalog := zellijlive.Catalog{Sessions: make(map[string]zellijlive.Session, len(sessions))}
	for _, session := range sessions {
		catalog.Sessions[session.Name] = session
		catalog.Names = append(catalog.Names, session.Name)
	}
	return catalog
}

func assertSessionStatus(t *testing.T, plan Plan, session string, status Status) Item {
	t.Helper()
	for _, item := range plan.Items {
		if item.Session == session {
			if item.Status != status {
				t.Fatalf("session %q status=%q want=%q item=%#v", session, item.Status, status, item)
			}
			return item
		}
	}
	t.Fatalf("session %q absent from %#v", session, plan.Items)
	return Item{}
}

func assertNoSession(t *testing.T, plan Plan, session string) {
	t.Helper()
	for _, item := range plan.Items {
		if item.Session == session {
			t.Fatalf("unrelated session %q selected: %#v", session, item)
		}
	}
}
