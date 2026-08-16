package mirror

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func followSnapshot(sessions ...string) Snapshot {
	windows := make([]Window, 0, len(sessions))
	for i, session := range sessions {
		windows = append(windows, Window{
			Order: i, SourceWindowID: i + 1, AppID: "kitty", WorkspaceID: "remote-dev", WorkspaceIndex: 2, WorkspaceName: "Dev", Output: "DP-1",
			ZellijSession: session, Terminal: &Terminal{CWD: "/" + session, ZellijSession: session},
		})
	}
	return Snapshot{
		Host: "lattice", Profile: "default", GeneratedAt: time.Now(),
		Workspaces:     []Workspace{{ID: "remote-empty", Index: 1}, {ID: "remote-dev", Index: 2, Name: "Dev", Output: "DP-1"}},
		ActiveSessions: append([]string(nil), sessions...), Windows: windows,
	}
}

func selectedWorkspace(snapshot Snapshot) SourceWorkspaceSelection {
	return SelectionForWorkspace(snapshot.Workspaces[1])
}

func TestFollowWorkspaceChoicesIncludeEmptyAndOnlyEligibleVisibleKitty(t *testing.T) {
	snapshot := followSnapshot("A")
	snapshot.Windows = append(snapshot.Windows,
		Window{Order: 2, SourceWindowID: 2, AppID: "firefox", WorkspaceID: "remote-dev", WorkspaceIndex: 2, WorkspaceName: "Dev", Output: "DP-1"},
		Window{Order: 3, Headless: true, AppID: "zellij", ZellijSession: "headless", Terminal: &Terminal{ZellijSession: "headless"}},
		Window{Order: 4, SourceWindowID: 3, AppID: "kitty", WorkspaceID: "remote-dev", WorkspaceIndex: 2, WorkspaceName: "Dev", Output: "DP-1", Terminal: &Terminal{}},
	)
	snapshot.ActiveSessions = append(snapshot.ActiveSessions, "headless")
	choices, err := FollowWorkspaceChoices(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 || choices[0].Workspace.Index != 1 || choices[0].EligibleSessions != 0 || choices[1].EligibleSessions != 1 || choices[1].VisibleTerminals != 2 {
		t.Fatalf("choices=%#v", choices)
	}
}

func TestFollowSnapshotExactJoinsAndGlobalSessionUniquenessFailClosed(t *testing.T) {
	base := followSnapshot("A")
	duplicateElsewhere := followSnapshot("A")
	duplicateElsewhere.Workspaces = append(duplicateElsewhere.Workspaces, Workspace{ID: "remote-other", Index: 3, Name: "Other", Output: "DP-2"})
	duplicateElsewhere.Windows = append(duplicateElsewhere.Windows, Window{
		Order: 1, SourceWindowID: 2, AppID: "kitty", WorkspaceID: "remote-other", WorkspaceIndex: 3, WorkspaceName: "Other", Output: "DP-2",
		ZellijSession: "A", Terminal: &Terminal{ZellijSession: "A"},
	})
	cases := []Snapshot{
		{Host: "lattice", Windows: []Window{}},
		func() Snapshot { s := base; s.ActiveSessions = nil; return s }(),
		func() Snapshot { s := base; s.Windows[0].WorkspaceIndex = 3; return s }(),
		func() Snapshot { s := base; s.Windows[0].WorkspaceName = " Dev "; return s }(),
		func() Snapshot { s := base; s.Windows[0].Output = "DP-2"; return s }(),
		duplicateElsewhere,
	}
	for i, snapshot := range cases {
		if _, err := FollowWorkspaceChoices(snapshot); err == nil {
			t.Fatalf("case %d accepted malformed/incomplete snapshot", i)
		}
		result := FollowOnce(context.Background(), followConfig(), snapshot, SourceWorkspaceSelection{ID: "remote-dev"}, FrozenDestination{ID: "local"}, &FollowState{}, FollowDeps{Runner: &followSim{}})
		if result.Healthy {
			t.Fatalf("case %d follow poll remained healthy: %#v", i, result)
		}
	}
}

func TestFollowRejectsSessionDuplicatesIncludingHeadless(t *testing.T) {
	cases := map[string]Snapshot{
		"visible and headless": func() Snapshot {
			snapshot := followSnapshot("A")
			snapshot.Windows = append(snapshot.Windows, Window{
				Order: 1, Headless: true, AppID: "zellij", ZellijSession: "A", Terminal: &Terminal{ZellijSession: "A"},
			})
			return snapshot
		}(),
		"duplicate headless": func() Snapshot {
			snapshot := followSnapshot()
			snapshot.ActiveSessions = []string{"A"}
			snapshot.Windows = []Window{
				{Order: 0, Headless: true, AppID: "zellij", ZellijSession: "A", Terminal: &Terminal{ZellijSession: "A"}},
				{Order: 1, Headless: true, AppID: "zellij", ZellijSession: "A", Terminal: &Terminal{ZellijSession: "A"}},
			}
			return snapshot
		}(),
	}
	for name, snapshot := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := FollowWorkspaceChoices(snapshot); err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("duplicate source session accepted: %v", err)
			}
			sim := newFollowSim()
			result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, &FollowState{}, sim.deps())
			if result.Healthy || result.Attempted != 0 || len(sim.commands) != 0 {
				t.Fatalf("duplicate source session caused effects: result=%#v commands=%v", result, sim.commands)
			}
		})
	}
}

func TestSelectedSourceWorkspaceIsFrozenByRuntimeID(t *testing.T) {
	initial := followSnapshot("A")
	selection := selectedWorkspace(initial)
	if selection.ID != "remote-dev" {
		t.Fatalf("selection=%#v", selection)
	}

	renumbered := followSnapshot("A")
	renumbered.Workspaces[1].Index = 7
	renumbered.Windows[0].WorkspaceIndex = 7
	sim := newFollowSim()
	sim.exact = &Projection{Window: OwnedWindow{ID: 40, PID: 400}, SourceHost: "lattice", Session: "A"}
	result := FollowOnce(context.Background(), followConfig(), renumbered, selection, FrozenDestination{ID: "local"}, &FollowState{}, sim.deps())
	if !result.Healthy || result.Existing != 1 {
		t.Fatalf("same-ID renumber did not follow: %#v", result)
	}

	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.Workspaces = s.Workspaces[:1]; s.Windows = nil; s.ActiveSessions = []string{} },
		func(s *Snapshot) { s.Workspaces[1].ID = "replacement"; s.Windows[0].WorkspaceID = "replacement" },
	} {
		fresh := followSnapshot("A")
		mutate(&fresh)
		before := len(sim.commands)
		result = FollowOnce(context.Background(), followConfig(), fresh, selection, FrozenDestination{ID: "local"}, &FollowState{}, sim.deps())
		if result.Healthy || len(sim.commands) != before || !strings.Contains(result.Reason, "workspace ID") {
			t.Fatalf("source replacement/disappearance retargeted: %#v commands=%#v", result, sim.commands)
		}
	}
}

func TestFrozenDestinationSurvivesRenumberAndNeverRetargets(t *testing.T) {
	source := Workspace{ID: "source", Index: 2, Name: "Dev"}
	frozen, err := ResolveFollowDestination(source, []OwnedWorkspace{{ID: "local-dev", Index: 2, Name: "Dev"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := frozen.CurrentTarget([]OwnedWorkspace{{ID: "replacement", Index: 2, Name: "Other"}, {ID: "local-dev", Index: 7, Name: "Dev"}})
	if err != nil || target.ID != "local-dev" || target.Name != "Dev" || target.Index != 7 {
		t.Fatalf("target=%#v err=%v", target, err)
	}
	if _, err := frozen.CurrentTarget([]OwnedWorkspace{{ID: "replacement", Index: 2, Name: "Dev"}}); err == nil {
		t.Fatal("retargeted to workspace that inherited selector")
	}
	if _, err := frozen.CurrentTarget([]OwnedWorkspace{{ID: "local-dev", Index: 7, Name: "Dev"}, {ID: "other", Index: 8, Name: "Dev"}}); err == nil {
		t.Fatal("accepted duplicate current destination name")
	}

	numbered, err := ResolveFollowDestination(Workspace{ID: "source", Index: 3}, []OwnedWorkspace{{ID: 99, Index: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if target, err := numbered.CurrentTarget([]OwnedWorkspace{{ID: 100, Index: 3}, {ID: 99, Index: 8}}); err != nil || target.ID != "99" || target.Index != 8 {
		t.Fatalf("numbered target=%#v err=%v", target, err)
	}
	if _, err := numbered.CurrentTarget([]OwnedWorkspace{{ID: 99, Index: 8}, {ID: 100, Index: 8}}); err == nil {
		t.Fatal("accepted duplicate current destination index")
	}
}

type followSim struct {
	commands         []Command
	launchedSessions []string
	exact            *Projection
	correlate        bool
	moves            int
	workspaces       []OwnedWorkspace
}

func newFollowSim() *followSim {
	return &followSim{correlate: true, workspaces: []OwnedWorkspace{{ID: "local", Index: 2, Name: "Dev"}}}
}

func (sim *followSim) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}

func (sim *followSim) Run(_ context.Context, command Command) error {
	sim.commands = append(sim.commands, command)
	if command.Name == "niri" {
		sim.moves++
		return nil
	}
	if command.Name != "kitty" {
		return nil
	}
	joined := strings.Join(command.Args, " ")
	session := ""
	for _, candidate := range []string{"A", "B", "C"} {
		if strings.Contains(joined, "'"+candidate+"'") {
			session = candidate
			break
		}
	}
	sim.launchedSessions = append(sim.launchedSessions, session)
	if !sim.correlate {
		return nil
	}
	marker := projectionTokenEnvironment + "="
	i := strings.Index(joined, marker)
	if i < 0 || i+len(marker)+32 > len(joined) {
		return errors.New("missing token")
	}
	token := joined[i+len(marker) : i+len(marker)+32]
	window := OwnedWindow{ID: 40 + len(sim.launchedSessions), PID: 400 + len(sim.launchedSessions)}
	sim.exact = &Projection{Window: window, SourceHost: "lattice", Session: session, CorrelationToken: token}
	return nil
}

func (sim *followSim) deps() FollowDeps {
	return FollowDeps{
		Runner: sim,
		ListWindows: func(context.Context) ([]OwnedWindow, error) {
			if sim.exact == nil {
				return nil, nil
			}
			return []OwnedWindow{sim.exact.Window}, nil
		},
		Inspect: func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error) {
			if sim.exact == nil {
				return ProjectionInventory{}, nil
			}
			return ProjectionInventory{Exact: []Projection{*sim.exact}}, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) { return sim.workspaces, nil },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		Token:      func() (string, error) { return strings.Repeat("a", 32), nil },
	}
}

func followConfig() FollowConfig {
	return FollowConfig{SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned", NiriCommand: "niri", Timeout: time.Second, EvidenceInterval: time.Millisecond, MaxPerPoll: 2, MaxTotal: 4}
}

func TestFollowUsesSourceOrderAndFiniteAttemptBounds(t *testing.T) {
	snapshot := followSnapshot("C", "A", "B")
	sim := newFollowSim()
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, &FollowState{}, sim.deps())
	if !result.Healthy || result.Deferred != 1 || result.Attempted != 2 || result.Confirmed != 2 || strings.Join(sim.launchedSessions, ",") != "C,A" || len(result.Items) != 3 || result.Items[0].Session != "B" || result.Items[0].Status != FollowDeferred {
		t.Fatalf("result=%#v launched=%v", result, sim.launchedSessions)
	}
}

func TestFollowPlansWholeBatchBeforeAnyEffect(t *testing.T) {
	snapshot := followSnapshot("A", "B")
	sim := newFollowSim()
	calls := 0
	deps := sim.deps()
	deps.Token = func() (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("late token failure")
		}
		return strings.Repeat("a", 32), nil
	}
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if result.Healthy || result.Attempted != 0 || len(sim.commands) != 0 || !strings.Contains(result.Reason, "token") {
		t.Fatalf("partial effect escaped plan gate: %#v commands=%#v", result, sim.commands)
	}
}

func TestFollowExactTokenMovesOnceAndManualCloseReopens(t *testing.T) {
	snapshot := followSnapshot("A")
	sim := newFollowSim()
	state := &FollowState{}
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, sim.deps())
	if !result.Healthy || result.Confirmed != 1 || sim.moves != 1 || state.TotalAttempts != 1 || state.TotalConfirmed != 1 {
		t.Fatalf("first=%#v moves=%d state=%#v", result, sim.moves, state)
	}
	result = FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, sim.deps())
	if result.Existing != 1 || result.Attempted != 0 || sim.moves != 1 {
		t.Fatalf("existing=%#v moves=%d", result, sim.moves)
	}
	sim.exact = nil
	result = FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, sim.deps())
	if result.Confirmed != 1 || sim.moves != 2 || state.TotalAttempts != 2 {
		t.Fatalf("reopen=%#v moves=%d state=%#v", result, sim.moves, state)
	}
}

func TestUncorrelatedLaunchChargesLifetimeAttemptBudget(t *testing.T) {
	snapshot := followSnapshot("A")
	sim := newFollowSim()
	sim.correlate = false
	cfg := followConfig()
	cfg.MaxTotal = 1
	deps := sim.deps()
	deps.Sleep = func(context.Context, time.Duration) error { return errors.New("no exact projection") }
	state := &FollowState{}
	result := FollowOnce(context.Background(), cfg, snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, deps)
	if result.Healthy || result.Attempted != 1 || result.Uncertain != 1 || state.TotalAttempts != 1 || len(sim.launchedSessions) != 1 {
		t.Fatalf("first=%#v state=%#v", result, state)
	}
	result = FollowOnce(context.Background(), cfg, snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, deps)
	if !result.Healthy || result.Attempted != 0 || result.Deferred != 1 || len(sim.launchedSessions) != 1 || result.TotalAttempts != 1 {
		t.Fatalf("budget retried uncertain launch: %#v launches=%v", result, sim.launchedSessions)
	}
}

func TestFollowStopsBatchWhenPostLaunchEvidenceDegrades(t *testing.T) {
	snapshot := followSnapshot("A", "B")
	sim := newFollowSim()
	deps := sim.deps()
	workspaceCalls := 0
	deps.Workspaces = func(context.Context) ([]OwnedWorkspace, error) {
		workspaceCalls++
		if workspaceCalls > 1 {
			return nil, errors.New("niri disappeared")
		}
		return sim.workspaces, nil
	}
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if result.Healthy || result.Attempted != 1 || result.Confirmed != 1 || result.Deferred != 1 || len(sim.launchedSessions) != 1 || sim.moves != 0 {
		t.Fatalf("degraded batch continued: %#v launches=%v moves=%d", result, sim.launchedSessions, sim.moves)
	}
}

func TestFollowStrictCorrelationDegradationStopsRemainingBatch(t *testing.T) {
	snapshot := followSnapshot("A", "B")
	sim := newFollowSim()
	deps := sim.deps()
	inspectCalls := 0
	deps.Inspect = func(_ context.Context, _ []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
		inspectCalls++
		if inspectCalls == 1 {
			return ProjectionInventory{}, nil
		}
		if sim.exact == nil {
			return ProjectionInventory{}, errors.New("launched projection missing")
		}
		return ProjectionInventory{
			Exact:     []Projection{*sim.exact},
			Untracked: []OwnedWindow{{ID: 99, PID: 999}},
		}, nil
	}
	state := &FollowState{}
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, deps)
	if result.Healthy || result.Attempted != 1 || result.Confirmed != 0 || result.Uncertain != 1 || result.Deferred != 1 || strings.Join(sim.launchedSessions, ",") != "A" || sim.moves != 0 || !strings.Contains(result.Reason, "evidence degraded") {
		t.Fatalf("degraded correlation continued batch: result=%#v launches=%v moves=%d state=%#v", result, sim.launchedSessions, sim.moves, state)
	}
}

func TestFollowStrictCorrelationObservationErrorStopsRemainingBatch(t *testing.T) {
	snapshot := followSnapshot("A", "B")
	sim := newFollowSim()
	deps := sim.deps()
	inspectCalls := 0
	deps.Inspect = func(_ context.Context, _ []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
		inspectCalls++
		if inspectCalls == 1 {
			return ProjectionInventory{}, nil
		}
		return ProjectionInventory{}, errors.New("proc inventory vanished")
	}
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if result.Healthy || result.Attempted != 1 || result.Confirmed != 0 || result.Uncertain != 1 || result.Deferred != 1 || strings.Join(sim.launchedSessions, ",") != "A" || sim.moves != 0 || !strings.Contains(result.Reason, "evidence unavailable") {
		t.Fatalf("observation error continued batch: result=%#v launches=%v moves=%d", result, sim.launchedSessions, sim.moves)
	}
}

func TestFollowUntrackedLocalOrExitedSourceOpensNothing(t *testing.T) {
	snapshot := followSnapshot("A")
	sim := newFollowSim()
	deps := sim.deps()
	deps.ListWindows = func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{{ID: 9, PID: 90}}, nil }
	deps.Inspect = func(_ context.Context, windows []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
		return ProjectionInventory{Untracked: windows}, nil
	}
	result := FollowOnce(context.Background(), followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if result.Healthy || len(sim.commands) != 0 {
		t.Fatalf("unsafe local evidence launched: %#v commands=%v", result, sim.commands)
	}

	exited := followSnapshot("A")
	exited.ActiveSessions = []string{}
	result = FollowOnce(context.Background(), followConfig(), exited, selectedWorkspace(exited), FrozenDestination{ID: "local"}, &FollowState{}, newFollowSim().deps())
	if !result.Healthy || result.Eligible != 0 || result.Attempted != 0 {
		t.Fatalf("exited session caused effect: %#v", result)
	}
}

func TestFollowLockSerializesForegroundOperations(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireFollowLock(root, "lattice", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireFollowLock(root, "lattice", "default"); err == nil {
		t.Fatal("concurrent follower acquired the same host/profile lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireFollowLock(root, "lattice", "default")
	if err != nil {
		t.Fatalf("released follow lock remained stuck: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFollowCorrelationCancellationStopsAndChargesAttempt(t *testing.T) {
	snapshot := followSnapshot("A")
	sim := newFollowSim()
	sim.correlate = false
	ctx, cancel := context.WithCancel(context.Background())
	deps := sim.deps()
	deps.Sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	state := &FollowState{}
	result := FollowOnce(ctx, followConfig(), snapshot, selectedWorkspace(snapshot), FrozenDestination{ID: "local"}, state, deps)
	if result.Healthy || result.Attempted != 1 || result.Uncertain != 1 || state.TotalAttempts != 1 || len(result.Items) != 1 || !strings.Contains(result.Items[0].Reason, "canceled") || sim.moves != 0 {
		t.Fatalf("cancellation result=%#v state=%#v moves=%d", result, state, sim.moves)
	}
}

func TestFollowRetryDelayIsBounded(t *testing.T) {
	if got := FollowRetryDelay(time.Millisecond, 0); got != MinimumFollowInterval {
		t.Fatalf("minimum delay=%s", got)
	}
	if got := FollowRetryDelay(5*time.Second, 20); got != MaximumFollowBackoff {
		t.Fatalf("capped delay=%s", got)
	}
}
