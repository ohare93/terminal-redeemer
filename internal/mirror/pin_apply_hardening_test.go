package mirror

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/resume"
)

func TestPrepareApplyFailsClosedOnlyForMatchingAmbiguousCandidate(t *testing.T) {
	pin := testPin("A")
	snapshot := activeSnapshot("lattice", "A")
	inventory := ProjectionInventory{
		Ambiguous: []OwnedWindow{{ID: 9}},
		AmbiguousCandidates: map[int][]Projection{
			9: {{SourceHost: "lattice", Session: "A"}, {SourceHost: "other", Session: "B"}},
		},
	}
	result := prepareApply(pin, activeSet(snapshot.ActiveSessions...), inventory, []OwnedWorkspace{{ID: 1, Index: 1, Name: "work"}})
	if result.Items[0].Status != ApplyAmbiguous {
		t.Fatalf("matching ambiguous candidate did not block launch: %#v", result)
	}
	inventory.AmbiguousCandidates[9] = []Projection{{SourceHost: "lattice", Session: "B"}, {SourceHost: "other", Session: "A"}}
	result = prepareApply(pin, activeSet(snapshot.ActiveSessions...), inventory, []OwnedWorkspace{{ID: 1, Index: 1, Name: "work"}})
	if result.Items[0].Status != ApplyReady {
		t.Fatalf("unrelated ambiguous candidates globally blocked launch: %#v", result)
	}
}

func TestApplyPinnedRetainsStructuredPlacementDegradation(t *testing.T) {
	pin := testPin("A")
	snapshot := activeSnapshot("lattice", "A")
	runner := &pinRunner{niriErr: errors.New("niri action unavailable")}
	deps := ApplyDeps{
		Runner: runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) {
			for _, command := range runner.commands {
				if command.Name == "kitty" {
					return []OwnedWindow{{ID: 10, PID: 100}}, nil
				}
			}
			return nil, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) {
			return []OwnedWorkspace{{ID: 1, Index: 1, Name: "work"}}, nil
		},
		Inspect: func(_ context.Context, windows []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
			if len(windows) == 1 {
				return ProjectionInventory{Exact: []Projection{{Window: windows[0], SourceHost: "lattice", Session: "A", CorrelationToken: runner.token}}}, nil
			}
			return ProjectionInventory{}, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	cfg := applyTestConfig(t, pin, snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := ApplyPinned(ctx, cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Status != ApplyOpened || item.LayoutStatus != resume.LayoutDegraded || !strings.Contains(item.LayoutReason, "niri action unavailable") || item.Reason != "" {
		t.Fatalf("item=%#v", item)
	}
}
