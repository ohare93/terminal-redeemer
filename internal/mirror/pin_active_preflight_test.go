package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareApplyUsesExactActiveSessionInventory(t *testing.T) {
	tests := []struct {
		name     string
		snapshot Snapshot
		pin      Pin
		want     []ApplyStatus
	}{
		{
			name: "visible row is not active evidence",
			snapshot: Snapshot{
				ActiveSessions: []string{},
				Windows:        []Window{{AppID: "kitty", ZellijSession: "Alpha", Terminal: &Terminal{ZellijSession: "Alpha"}}},
			},
			pin:  testPin("Alpha"),
			want: []ApplyStatus{ApplyMissing},
		},
		{
			name: "session matching is case sensitive",
			snapshot: Snapshot{
				ActiveSessions: []string{"Alpha"},
				Windows:        []Window{{AppID: "kitty", ZellijSession: "alpha", Terminal: &Terminal{ZellijSession: "alpha"}}},
			},
			pin:  testPin("alpha"),
			want: []ApplyStatus{ApplyMissing},
		},
		{
			name: "exact active session is ready",
			snapshot: Snapshot{
				ActiveSessions: []string{"Alpha"},
				Windows:        []Window{{AppID: "kitty", ZellijSession: "Alpha", Terminal: &Terminal{ZellijSession: "Alpha"}}},
			},
			pin:  testPin("Alpha"),
			want: []ApplyStatus{ApplyReady},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active, err := exactActiveSessionInventory(test.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			result := prepareApply(test.pin, active, ProjectionInventory{}, []OwnedWorkspace{{ID: 1, Index: 1, Name: "work"}})
			if len(result.Items) != len(test.want) {
				t.Fatalf("items=%#v", result.Items)
			}
			for i, want := range test.want {
				if result.Items[i].Status != want {
					t.Fatalf("item %d status=%q want=%q result=%#v", i, result.Items[i].Status, want, result)
				}
			}
		})
	}
}

func TestApplyPinnedRejectsMissingActiveInventoryBeforeMutation(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "absent")
	runner := &pinRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := ApplyPinned(ctx, ApplyConfig{
		Snapshot:        Snapshot{Host: "lattice", Profile: "default", ActiveSessions: nil, Windows: []Window{}},
		SourceHost:      "lattice",
		SSHCommand:      "ssh",
		LauncherCommand: "kitty",
		AppID:           "owned",
		NiriCommand:     "niri",
		StateDir:        stateDir,
		Timeout:         time.Second,
		PollInterval:    time.Millisecond,
	}, ApplyDeps{Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "no complete ACTIVE Zellij inventory") || !strings.Contains(err.Error(), "upgrade required") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("version-skew preflight executed commands: %#v", runner.commands)
	}
	if _, statErr := os.Stat(stateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("version-skew preflight mutated state: %v", statErr)
	}
}

func TestApplyPinnedAcceptsCompleteEmptyActiveInventoryAsMissing(t *testing.T) {
	pin := testPin("Alpha")
	snapshot := activeSnapshot("lattice")
	snapshot.Windows = []Window{{AppID: "kitty", ZellijSession: "Alpha", Terminal: &Terminal{ZellijSession: "Alpha"}}}
	runner := &pinRunner{}
	cfg := applyTestConfig(t, pin, snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := ApplyPinned(ctx, cfg, ApplyDeps{
		Runner:      runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) { return nil, nil },
		Workspaces:  func(context.Context) ([]OwnedWorkspace, error) { return nil, nil },
		Inspect: func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error) {
			return ProjectionInventory{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Status != ApplyMissing {
		t.Fatalf("result=%#v", result)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("complete empty ACTIVE inventory launched: %#v", runner.commands)
	}
}
