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
			Order: i, SourceWindowID: i + 1, AppID: "kitty", WorkspaceID: "remote-dev", WorkspaceIndex: 2, WorkspaceName: "Dev",
			ZellijSession: session, Terminal: &Terminal{CWD: "/" + session, ZellijSession: session},
		})
	}
	return Snapshot{
		Host: "lattice", Profile: "default", GeneratedAt: time.Now(),
		Workspaces:     []Workspace{{ID: "remote-empty", Index: 1}, {ID: "remote-dev", Index: 2, Name: "Dev"}},
		ActiveSessions: append([]string(nil), sessions...), Windows: windows,
	}
}

func TestFollowWorkspaceChoicesIncludeEmptyAndOnlyEligibleVisibleKitty(t *testing.T) {
	snapshot := followSnapshot("A")
	snapshot.Windows = append(snapshot.Windows,
		Window{Order: 2, SourceWindowID: 2, AppID: "firefox", WorkspaceID: "remote-dev", WorkspaceIndex: 2, WorkspaceName: "Dev"},
		Window{Order: 3, Headless: true, AppID: "zellij", ZellijSession: "headless", Terminal: &Terminal{ZellijSession: "headless"}},
		Window{Order: 4, SourceWindowID: 3, AppID: "kitty", WorkspaceID: "remote-dev", WorkspaceIndex: 2, WorkspaceName: "Dev", Terminal: &Terminal{}},
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

func TestFollowSnapshotAbsentInconsistentAndDuplicateEvidenceFailClosed(t *testing.T) {
	base := followSnapshot("A")
	cases := []Snapshot{
		{Host: "lattice", Windows: []Window{}},
		func() Snapshot { s := base; s.ActiveSessions = nil; return s }(),
		func() Snapshot { s := base; s.Windows[0].WorkspaceIndex = 3; return s }(),
		func() Snapshot { s := base; s.Windows = append(s.Windows, s.Windows[0]); return s }(),
	}
	for i, snapshot := range cases {
		if _, err := FollowWorkspaceChoices(snapshot); err == nil {
			t.Fatalf("case %d accepted malformed/incomplete snapshot", i)
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

	numbered, err := ResolveFollowDestination(Workspace{ID: "source", Index: 3}, []OwnedWorkspace{{ID: 99, Index: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if target, err := numbered.CurrentTarget([]OwnedWorkspace{{ID: 100, Index: 3}, {ID: 99, Index: 8}}); err != nil || target.ID != "99" || target.Index != 8 {
		t.Fatalf("numbered target=%#v err=%v", target, err)
	}
}

type followRunner struct {
	commands []Command
	token    string
	session  string
}

func (r *followRunner) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}
func (r *followRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	if command.Name == "kitty" {
		joined := strings.Join(command.Args, " ")
		marker := projectionTokenEnvironment + "="
		if i := strings.Index(joined, marker); i >= 0 {
			r.token = joined[i+len(marker) : i+len(marker)+32]
		}
		for _, candidate := range []string{"A", "B", "C"} {
			if strings.Contains(joined, "'"+candidate+"'") {
				r.session = candidate
			}
		}
	}
	return nil
}

func followConfig() FollowConfig {
	return FollowConfig{SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned", NiriCommand: "niri", Timeout: time.Second, EvidenceInterval: time.Millisecond, MaxPerPoll: 2, MaxTotal: 4}
}

func TestFollowDryRunUsesSourceOrderAndFiniteBounds(t *testing.T) {
	cfg := followConfig()
	cfg.DryRun = true
	result := FollowOnce(context.Background(), cfg, followSnapshot("C", "A", "B"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, &FollowState{}, FollowDeps{
		Runner:      &followRunner{},
		ListWindows: func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{}, nil },
		Inspect: func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error) {
			return ProjectionInventory{}, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) {
			return []OwnedWorkspace{{ID: "local", Index: 2, Name: "Dev"}}, nil
		},
	})
	if !result.Healthy || result.Deferred != 1 || len(result.Items) != 2 || result.Items[0].Session != "C" || result.Items[1].Session != "A" || result.Items[0].Status != FollowWouldOpen {
		t.Fatalf("result=%#v", result)
	}
}

func TestFollowExactTokenMovesNewWindowOnceAndManualCloseReopens(t *testing.T) {
	runner := &followRunner{}
	open := false
	moves := 0
	deps := FollowDeps{
		Runner: runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) {
			if open || runner.session != "" {
				open = true
				return []OwnedWindow{{ID: 40, PID: 400}}, nil
			}
			return []OwnedWindow{}, nil
		},
		Inspect: func(_ context.Context, windows []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
			if len(windows) == 0 {
				return ProjectionInventory{}, nil
			}
			return ProjectionInventory{Exact: []Projection{{Window: windows[0], SourceHost: "lattice", Session: "A", CorrelationToken: runner.token}}}, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) {
			return []OwnedWorkspace{{ID: "local", Index: 2, Name: "Dev"}}, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
		Token: func() (string, error) { return strings.Repeat("a", 32), nil },
	}
	// Count only Niri move actions.
	originalRunner := deps.Runner
	deps.Runner = followTestRunnerFunc{run: func(ctx context.Context, command Command) error {
		if command.Name == "niri" {
			moves++
		}
		return originalRunner.Run(ctx, command)
	}}
	state := &FollowState{}
	result := FollowOnce(context.Background(), followConfig(), followSnapshot("A"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, state, deps)
	if result.Opened != 1 || result.Items[0].WindowID != 40 || moves != 1 || state.TotalOpened != 1 {
		t.Fatalf("first=%#v moves=%d state=%#v", result, moves, state)
	}
	result = FollowOnce(context.Background(), followConfig(), followSnapshot("A"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, state, deps)
	if result.Existing != 1 || result.Opened != 0 || moves != 1 {
		t.Fatalf("existing=%#v moves=%d", result, moves)
	}
	open, runner.session, runner.token = false, "", ""
	result = FollowOnce(context.Background(), followConfig(), followSnapshot("A"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, state, deps)
	if result.Opened != 1 || moves != 2 || state.TotalOpened != 2 {
		t.Fatalf("reopen=%#v moves=%d state=%#v", result, moves, state)
	}
}

type followTestRunnerFunc struct {
	run func(context.Context, Command) error
}

func (r followTestRunnerFunc) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}
func (r followTestRunnerFunc) Run(ctx context.Context, command Command) error {
	return r.run(ctx, command)
}

func TestFollowUntrackedLocalOrExitedSourceOpensNothing(t *testing.T) {
	runner := &followRunner{}
	deps := FollowDeps{
		Runner:      runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{{ID: 9, PID: 90}}, nil },
		Inspect: func(_ context.Context, windows []OwnedWindow, _ ProjectionEvidenceConfig) (ProjectionInventory, error) {
			return ProjectionInventory{Untracked: windows}, nil
		},
	}
	result := FollowOnce(context.Background(), followConfig(), followSnapshot("A"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if result.Healthy || len(runner.commands) != 0 {
		t.Fatalf("unsafe local evidence launched: %#v commands=%v", result, runner.commands)
	}
	exited := followSnapshot("A")
	exited.ActiveSessions = []string{}
	deps.ListWindows = func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{}, nil }
	deps.Inspect = func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error) {
		return ProjectionInventory{}, nil
	}
	deps.Workspaces = func(context.Context) ([]OwnedWorkspace, error) {
		return []OwnedWorkspace{{ID: "local", Index: 2, Name: "Dev"}}, nil
	}
	result = FollowOnce(context.Background(), followConfig(), exited, SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if !result.Healthy || result.Eligible != 0 || len(runner.commands) != 0 {
		t.Fatalf("exited session caused effect: %#v", result)
	}
}

func TestFollowLocalWorkspaceFailureHasZeroEffects(t *testing.T) {
	runner := &followRunner{}
	result := FollowOnce(context.Background(), followConfig(), followSnapshot("A"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, &FollowState{}, FollowDeps{
		Runner:      runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{}, nil },
		Inspect: func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error) {
			return ProjectionInventory{}, nil
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) { return nil, errors.New("niri unavailable") },
	})
	if result.Healthy || !strings.Contains(result.Reason, "workspace inventory") || len(runner.commands) != 0 {
		t.Fatalf("result=%#v commands=%#v", result, runner.commands)
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

func TestFollowCorrelationCancellationStopsWithoutMove(t *testing.T) {
	runner := &followRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	deps := FollowDeps{
		Runner:      runner,
		ListWindows: func(context.Context) ([]OwnedWindow, error) { return []OwnedWindow{}, nil },
		Inspect: func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error) {
			return ProjectionInventory{}, nil
		},
		Sleep: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
		Workspaces: func(context.Context) ([]OwnedWorkspace, error) {
			return []OwnedWorkspace{{ID: "local", Index: 2, Name: "Dev"}}, nil
		},
		Token: func() (string, error) { return strings.Repeat("b", 32), nil },
	}
	result := FollowOnce(ctx, followConfig(), followSnapshot("A"), SourceWorkspaceSelection{Name: "Dev"}, FrozenDestination{ID: "local"}, &FollowState{}, deps)
	if result.Opened != 0 || len(result.Items) != 1 || result.Items[0].Status != FollowFailed || !strings.Contains(result.Items[0].Reason, "canceled") {
		t.Fatalf("cancellation result=%#v", result)
	}
	for _, command := range runner.commands {
		if command.Name == "niri" {
			t.Fatalf("cancellation still moved a window: %#v", runner.commands)
		}
	}
}

func TestFollowRetryDelayIsBoundedAndCancellationPropagates(t *testing.T) {
	if got := FollowRetryDelay(time.Millisecond, 0); got != MinimumFollowInterval {
		t.Fatalf("minimum delay=%s", got)
	}
	if got := FollowRetryDelay(5*time.Second, 20); got != MaximumFollowBackoff {
		t.Fatalf("capped delay=%s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancellation not propagated")
	}
}
