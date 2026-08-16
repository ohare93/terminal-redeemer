package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPlanSourceAttachUsesExactQuotedArgvAndOptionalWorkspace(t *testing.T) {
	command, err := PlanSourceAttach(SourceAttachConfig{
		SourceHost: "user@lattice", SSHCommand: "ssh", SSHOptions: []string{"-p", "2222"},
		RemoteCommand: "/nix/store/redeem bin", Session: "redeem-0123456789abcdef0123456789abcdef", Workspace: "agentleman's",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name != "ssh" || !reflect.DeepEqual(command.Args[:4], []string{"-p", "2222", "--", "user@lattice"}) {
		t.Fatalf("SSH boundary: %#v", command)
	}
	want := `'/nix/store/redeem bin' 'mirror' 'attach-local' '--session' 'redeem-0123456789abcdef0123456789abcdef' '--workspace' 'agentleman'"'"'s'`
	if command.Args[4] != want {
		t.Fatalf("remote argv = %q, want %q", command.Args[4], want)
	}

	withoutWorkspace, err := PlanSourceAttach(SourceAttachConfig{
		SourceHost: "lattice", SSHCommand: "ssh", RemoteCommand: "redeem",
		Session: "redeem-0123456789abcdef0123456789abcdef",
	})
	if err != nil || strings.Contains(withoutWorkspace.Args[len(withoutWorkspace.Args)-1], "--workspace") {
		t.Fatalf("optional workspace changed helper argv: %#v err=%v", withoutWorkspace, err)
	}
}

func TestWorkspaceAndGeneratedSessionValidation(t *testing.T) {
	for _, valid := range []string{"", "agentleman", "workspace 1", "12", "følg"} {
		if err := ValidateWorkspaceReference(valid); err != nil {
			t.Errorf("valid workspace %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"0", "-1", "--help", " leading", "trailing ", "bad\nname", strings.Repeat("a", 129)} {
		if err := ValidateWorkspaceReference(invalid); err == nil {
			t.Errorf("invalid workspace accepted: %q", invalid)
		}
	}
	if _, err := PlanSourceAttach(SourceAttachConfig{SourceHost: "lattice", SSHCommand: "ssh", RemoteCommand: "redeem", Session: "existing"}); err == nil {
		t.Fatal("source helper accepted arbitrary existing session")
	}
}

func TestDualNewCoordinatorRunsCreatorOnceThenHelperAfterExactReadiness(t *testing.T) {
	runner := &recordingRunner{}
	acquisitions := 0
	waits := 0
	coordinator := DualNewCoordinator{
		Runner: runner,
		Acquire: func(context.Context) (Snapshot, error) {
			acquisitions++
			if acquisitions == 1 {
				return Snapshot{Windows: []Window{{ZellijSession: "other"}}}, nil
			}
			return Snapshot{Windows: []Window{{ZellijSession: "redeem-0123456789abcdef0123456789abcdef"}}}, nil
		},
		Wait:    func(context.Context, time.Duration) error { waits++; return nil },
		Timeout: 2 * time.Second, PollInterval: time.Second,
	}
	creator := LaunchPlan{Session: "redeem-0123456789abcdef0123456789abcdef", Command: Command{Name: "kitty", Args: []string{"--detach"}}}
	helper := Command{Name: "ssh", Args: []string{"lattice", "helper"}}
	result, err := coordinator.Run(context.Background(), creator, helper)
	if err != nil || result.SourceError != nil {
		t.Fatalf("run = %#v err=%v", result, err)
	}
	if acquisitions != 2 || waits != 1 || len(runner.runCalls) != 2 || runner.runCalls[0].Name != "kitty" || runner.runCalls[1].Name != "ssh" {
		t.Fatalf("ordering calls=%#v acquisitions=%d waits=%d", runner.runCalls, acquisitions, waits)
	}
}

func TestDualNewCoordinatorTreatsReadinessAndHelperFailuresAsNonfatal(t *testing.T) {
	creator := LaunchPlan{Session: "redeem-0123456789abcdef0123456789abcdef", Command: Command{Name: "kitty"}}
	helper := Command{Name: "ssh"}

	t.Run("timeout does not retry creator", func(t *testing.T) {
		runner := &recordingRunner{}
		coordinator := DualNewCoordinator{
			Runner:  runner,
			Acquire: func(context.Context) (Snapshot, error) { return Snapshot{}, errors.New("offline") },
			Wait:    func(context.Context, time.Duration) error { return nil }, Timeout: time.Second, PollInterval: time.Second,
		}
		result, err := coordinator.Run(context.Background(), creator, helper)
		if err != nil || result.SourceError == nil || len(runner.runCalls) != 1 || runner.runCalls[0].Name != "kitty" {
			t.Fatalf("result=%#v err=%v calls=%#v", result, err, runner.runCalls)
		}
	})

	t.Run("helper failure", func(t *testing.T) {
		runner := &recordingRunner{runErr: errors.New("old source")}
		// Only the helper should fail; make creator succeed through a dedicated runner.
		calls := 0
		runner.onRun = func(Command) {
			calls++
			if calls == 1 {
				runner.runErr = nil
			} else {
				runner.runErr = errors.New("old source")
			}
		}
		coordinator := DualNewCoordinator{Runner: runner, Acquire: func(context.Context) (Snapshot, error) {
			return Snapshot{Windows: []Window{{ZellijSession: creator.Session}}}, nil
		}}
		result, err := coordinator.Run(context.Background(), creator, helper)
		if err != nil || result.SourceError == nil || len(runner.runCalls) != 2 {
			t.Fatalf("result=%#v err=%v calls=%#v", result, err, runner.runCalls)
		}
	})
}

func TestPlanLocalAttachIsDetachedAttachOnly(t *testing.T) {
	plan, err := PlanLocalAttach("redeem-0123456789abcdef0123456789abcdef", "agentleman", "kitty")
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderCommand(plan.Command)
	for _, want := range []string{"'--detach'", "'--class' 'kitty'", "'zellij' 'attach'", "'options' '--on-force-close' 'detach'"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %s: %s", want, rendered)
		}
	}
	if strings.Contains(rendered, "--create") {
		t.Fatalf("source attach may not create: %s", rendered)
	}
}

type sequenceWindowLister struct {
	windows [][]OwnedWindow
	calls   int
}

func (lister *sequenceWindowLister) List(context.Context) ([]OwnedWindow, error) {
	index := lister.calls
	lister.calls++
	if index >= len(lister.windows) {
		index = len(lister.windows) - 1
	}
	return append([]OwnedWindow(nil), lister.windows[index]...), nil
}

type sessionCheckerStub struct {
	active bool
	err    error
}

func (checker sessionCheckerStub) Active(context.Context, string) (bool, error) {
	return checker.active, checker.err
}

type probeStub struct {
	attached map[int]bool
}

func (probe probeStub) Attached(_ context.Context, pid int, _ string) (bool, error) {
	return probe.attached[pid], nil
}

func TestLocalAttacherSuppressesDuplicateAndMovesNewExactWindowOnce(t *testing.T) {
	plan, _ := PlanLocalAttach("redeem-0123456789abcdef0123456789abcdef", "agentleman", "kitty")

	t.Run("already attached", func(t *testing.T) {
		runner := &recordingRunner{}
		lister := &sequenceWindowLister{windows: [][]OwnedWindow{{{ID: 9, PID: 90}}}}
		result, err := (LocalAttacher{Runner: runner, Windows: lister, Probe: probeStub{attached: map[int]bool{90: true}}, Sessions: sessionCheckerStub{active: true}}).Attach(context.Background(), plan)
		if err != nil || !result.AlreadyOpen || result.WindowID != 9 || len(runner.runCalls) != 0 {
			t.Fatalf("result=%#v err=%v calls=%#v", result, err, runner.runCalls)
		}
	})

	t.Run("launch then move", func(t *testing.T) {
		runner := &recordingRunner{}
		lister := &sequenceWindowLister{windows: [][]OwnedWindow{{}, {}, {{ID: 11, PID: 110}}}}
		result, err := (LocalAttacher{
			Runner: runner, Windows: lister, Probe: probeStub{attached: map[int]bool{110: true}}, Sessions: sessionCheckerStub{active: true}, NiriCommand: "niri",
			Wait: func(context.Context, time.Duration) error { return nil }, Timeout: 2 * time.Second, PollInterval: time.Second,
		}).Attach(context.Background(), plan)
		if err != nil || result.WindowID != 11 || result.PlacementError != nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if len(runner.runCalls) != 2 || runner.runCalls[0].Name != "kitty" || strings.Join(runner.runCalls[1].Args, " ") != "msg action move-window-to-workspace --window-id 11 --focus false agentleman" {
			t.Fatalf("calls=%#v", runner.runCalls)
		}
	})
}

func TestLocalAttacherPlacementFailureKeepsAttachedResult(t *testing.T) {
	plan, _ := PlanLocalAttach("redeem-0123456789abcdef0123456789abcdef", "agentleman", "kitty")
	runner := &recordingRunner{runErr: errors.New("move unsupported")}
	calls := 0
	runner.onRun = func(Command) {
		calls++
		if calls == 1 {
			runner.runErr = nil
		} else {
			runner.runErr = errors.New("move unsupported")
		}
	}
	lister := &sequenceWindowLister{windows: [][]OwnedWindow{{}, {{ID: 11, PID: 110}}}}
	result, err := (LocalAttacher{
		Runner: runner, Windows: lister, Probe: probeStub{attached: map[int]bool{110: true}}, Sessions: sessionCheckerStub{active: true}, NiriCommand: "niri",
	}).Attach(context.Background(), plan)
	if err != nil || result.WindowID != 11 || result.PlacementError == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestLocalProcAttachmentProbeRequiresExactDescendantArgv(t *testing.T) {
	root := t.TempDir()
	writeProcFixture(t, root, 100, 1, []string{"kitty", "--title", "redeem-0123456789abcdef0123456789abcdef"})
	writeProcFixture(t, root, 101, 100, []string{"zellij", "attach", "redeem-0123456789abcdef0123456789abcdef", "options", "--on-force-close", "detach"})
	writeProcFixture(t, root, 102, 100, []string{"zellij", "attach", "redeem-ffffffffffffffffffffffffffffffff", "options", "--on-force-close", "detach"})
	probe := LocalProcAttachmentProbe{ProcRoot: root}
	attached, err := probe.Attached(context.Background(), 100, "redeem-0123456789abcdef0123456789abcdef")
	if err != nil || !attached {
		t.Fatalf("attached=%v err=%v", attached, err)
	}
	attached, err = probe.Attached(context.Background(), 100, "redeem-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || attached {
		t.Fatalf("substring/title-only evidence accepted: attached=%v err=%v", attached, err)
	}
}

func writeProcFixture(t *testing.T, root string, pid int, ppid int, argv []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (proc name) S " + strconv.Itoa(ppid) + " 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGraphicalEnvironmentPreservesCompleteSession(t *testing.T) {
	env := []string{"PATH=/bin", "NIRI_SOCKET=/run/niri.sock", "WAYLAND_DISPLAY=wayland-1"}
	got := GraphicalEnvironment(env)
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("environment changed: %#v", got)
	}
	got[0] = "changed"
	if env[0] == "changed" {
		t.Fatal("environment was not copied")
	}
}
