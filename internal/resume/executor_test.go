package resume

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type fakeProcess struct {
	pid    int
	done   chan error
	kills  int
	killMu sync.Mutex
}

func newFakeProcess(pid int) *fakeProcess { return &fakeProcess{pid: pid, done: make(chan error, 1)} }
func (p *fakeProcess) PID() int           { return p.pid }
func (p *fakeProcess) Done() <-chan error { return p.done }
func (p *fakeProcess) Kill() error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	p.kills++
	return nil
}
func (p *fakeProcess) killCount() int {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	return p.kills
}

type fakeDesktop struct {
	mu                         sync.Mutex
	windows                    []ObservedWindow
	attached                   map[int]string
	moves                      []string
	moveErr                    error
	skipMoveObservation        bool
	orderActions               []string
	skipFocusObservation       map[int]bool
	skipColumnObservation      map[int]bool
	focusMutationID            int
	mutateAfterMoveObservation bool
	moveObservationPending     bool
	duplicateAfterAttachCalls  int
	attachCalls                int
}

func (d *fakeDesktop) Windows(context.Context) ([]ObservedWindow, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]ObservedWindow(nil), d.windows...)
	if d.moveObservationPending {
		d.moveObservationPending = false
		for i := range d.windows {
			if d.windows[i].IsFocused && d.windows[i].Column != nil {
				value := *d.windows[i].Column + 1
				d.windows[i].Column = &value
				break
			}
		}
	}
	return out, nil
}
func (d *fakeDesktop) Attached(_ context.Context, pid int, session string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attachCalls++
	attached := d.attached[pid] == session
	if attached && d.duplicateAfterAttachCalls > 0 && d.attachCalls == d.duplicateAfterAttachCalls {
		d.windows = append(d.windows, ObservedWindow{ID: 999, PID: 999, AppID: "firefox", WorkspaceID: "other"})
		d.attached[999] = session
	}
	return attached, nil
}
func (d *fakeDesktop) MoveToWorkspace(_ context.Context, id int, target WorkspaceTarget) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.moves = append(d.moves, target.ID)
	if d.moveErr != nil {
		return d.moveErr
	}
	if d.skipMoveObservation {
		return nil
	}
	for i := range d.windows {
		if d.windows[i].ID == id {
			if d.windows[i].WorkspaceID != target.ID {
				nextColumn := 1
				for j := range d.windows {
					if d.windows[j].ID != id && d.windows[j].WorkspaceID == target.ID && d.windows[j].Column != nil && *d.windows[j].Column >= nextColumn {
						nextColumn = *d.windows[j].Column + 1
					}
				}
				d.windows[i].Column = intPointer(nextColumn)
				d.windows[i].Row = intPointer(1)
			}
			d.windows[i].WorkspaceID = target.ID
			return nil
		}
	}
	return errors.New("window missing")
}
func (d *fakeDesktop) FocusWindow(_ context.Context, id int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.orderActions = append(d.orderActions, fmt.Sprintf("focus:%d", id))
	if d.skipFocusObservation[id] {
		return nil
	}
	found := false
	for i := range d.windows {
		d.windows[i].IsFocused = d.windows[i].ID == id
		found = found || d.windows[i].ID == id
	}
	if id == d.focusMutationID {
		for i := range d.windows {
			if d.windows[i].ID != id && d.windows[i].Column != nil {
				value := *d.windows[i].Column + 1
				d.windows[i].Column = &value
				break
			}
		}
	}
	if !found {
		return errors.New("window missing")
	}
	return nil
}
func (d *fakeDesktop) MoveColumnToIndex(_ context.Context, destination int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	focused := -1
	for i := range d.windows {
		if d.windows[i].IsFocused {
			focused = i
			break
		}
	}
	if focused < 0 {
		return errors.New("no focused window")
	}
	d.orderActions = append(d.orderActions, fmt.Sprintf("column:%d:%d", d.windows[focused].ID, destination))
	if d.skipColumnObservation[d.windows[focused].ID] || d.windows[focused].Column == nil {
		return nil
	}
	source := *d.windows[focused].Column
	workspaceID := d.windows[focused].WorkspaceID
	for i := range d.windows {
		if d.windows[i].WorkspaceID != workspaceID || d.windows[i].Column == nil {
			continue
		}
		column := *d.windows[i].Column
		switch {
		case column == source:
			column = destination
		case source < destination && column > source && column <= destination:
			column--
		case source > destination && column >= destination && column < source:
			column++
		}
		d.windows[i].Column = intPointer(column)
	}
	if d.mutateAfterMoveObservation {
		d.moveObservationPending = true
	}
	return nil
}

type fakeLauncher struct {
	next      int
	desktop   *fakeDesktop
	specs     []LaunchSpec
	processes []*fakeProcess
	noWindow  bool
	noAttach  bool
	zeroPID   bool
	exitSet   bool
	exit      error
}

func (l *fakeLauncher) Start(_ context.Context, spec LaunchSpec) (Process, error) {
	l.specs = append(l.specs, spec)
	l.next++
	pid := 1000 + l.next
	if l.zeroPID {
		pid = 0
	}
	process := newFakeProcess(pid)
	l.processes = append(l.processes, process)
	if !l.noWindow && pid > 0 {
		l.desktop.mu.Lock()
		l.desktop.windows = append(l.desktop.windows, ObservedWindow{ID: 2000 + l.next, PID: pid, AppID: "kitty", WorkspaceID: "current"})
		if !l.noAttach {
			session := spec.Args[len(spec.Args)-1]
			l.desktop.attached[pid] = session
		}
		l.desktop.mu.Unlock()
	}
	if l.exitSet || l.exit != nil {
		process.done <- l.exit
	}
	return process, nil
}

type fakeLayout struct{ result LayoutResult }

func (l fakeLayout) ApplyLayout(context.Context, int, model.Placement) LayoutResult { return l.result }

type sequenceCataloger struct {
	catalogs []zellijlive.Catalog
	err      error
	calls    int
}

func (c *sequenceCataloger) Observe(context.Context) (zellijlive.Catalog, error) {
	c.calls++
	if c.err != nil {
		return zellijlive.Catalog{}, c.err
	}
	if len(c.catalogs) == 0 {
		return zellijlive.Catalog{}, nil
	}
	index := c.calls - 1
	if index >= len(c.catalogs) {
		index = len(c.catalogs) - 1
	}
	return c.catalogs[index], nil
}

type probeObservation struct {
	attached bool
	err      error
}

type sequenceProbe struct {
	mu           sync.Mutex
	observations []probeObservation
	calls        int
}

func (p *sequenceProbe) Attached(context.Context, int, string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	p.calls++
	if index >= len(p.observations) {
		return false, nil
	}
	observation := p.observations[index]
	return observation.attached, observation.err
}

type exitDuringConfirmationProbe struct {
	launcher *fakeLauncher
	calls    int
}

func (p *exitDuringConfirmationProbe) Attached(context.Context, int, string) (bool, error) {
	p.calls++
	if p.calls == 2 {
		p.launcher.processes[0].done <- errors.New("attach exited")
	}
	return true, nil
}

func testExecutor(desktop *fakeDesktop, launcher *fakeLauncher) Executor {
	return Executor{
		Config:   ExecutorConfig{LauncherCommand: "/usr/bin/kitty", Timeout: 10 * time.Millisecond, PollInterval: time.Millisecond},
		Launcher: launcher,
		Observer: desktop,
		Probe:    desktop,
		Mover:    desktop,
	}
}

func readyItem(key, session, workspace string) Item {
	return Item{WindowKey: key, AppID: "kitty", Session: session, CWD: "/tmp/" + session, Status: StatusReady, Workspace: &WorkspaceTarget{ID: workspace, Name: workspace}}
}

func TestExecutorAllReobservesExactCatalogBeforeEachLaunch(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	cataloger := &sequenceCataloger{catalogs: []zellijlive.Catalog{
		testCatalog(zellijlive.Session{Name: "first", Status: zellijlive.StatusActive}),
		testCatalog(zellijlive.Session{Name: "second", Status: zellijlive.StatusActive}),
	}}
	executor := testExecutor(desktop, launcher)
	executor.Cataloger = cataloger
	executor.Config.RecoveryMaxAge = time.Hour
	executor.Now = func() time.Time { return now }
	first := readyItem("a", "first", "ws-a")
	first.CandidateSource, first.ZellijStatus = CandidateSourceCurrentActive, string(zellijlive.StatusActive)
	second := readyItem("b", "second", "ws-b")
	second.CandidateSource, second.ZellijStatus = CandidateSourceCurrentActive, string(zellijlive.StatusActive)

	got := executor.Apply(context.Background(), Plan{CapturedAt: now.Add(-time.Minute), Items: []Item{first, second}})
	if got.Summary.Restored != 2 || cataloger.calls != 2 || len(launcher.specs) != 2 {
		t.Fatalf("result=%#v catalog_calls=%d launches=%d", got, cataloger.calls, len(launcher.specs))
	}
}

func TestExecutorAllBlocksPlannedActiveThatBecomesDead(t *testing.T) {
	now := time.Now().UTC()
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Cataloger = &sequenceCataloger{catalogs: []zellijlive.Catalog{testCatalog(zellijlive.Session{Name: "session", Status: zellijlive.StatusDeadResurrectable})}}
	executor.Config.RecoveryMaxAge = 24 * time.Hour
	executor.Now = func() time.Time { return now }
	item := readyItem("a", "session", "ws")
	item.CandidateSource, item.ZellijStatus = CandidateSourcePriorActive, string(zellijlive.StatusActive)

	got := executor.Apply(context.Background(), Plan{CapturedAt: now.Add(-72 * time.Hour), Items: []Item{item}})
	if got.Items[0].Status != StatusUnavailable || !strings.Contains(got.Items[0].Reason, "resurrection is blocked") || len(launcher.specs) != 0 {
		t.Fatalf("result=%#v launches=%d", got.Items[0], len(launcher.specs))
	}
}

func TestExecutorAllAllowsEligiblePlannedDeadThatBecomesActive(t *testing.T) {
	now := time.Now().UTC()
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Cataloger = &sequenceCataloger{catalogs: []zellijlive.Catalog{testCatalog(zellijlive.Session{Name: "session", Status: zellijlive.StatusActive})}}
	executor.Config.RecoveryMaxAge = time.Hour
	executor.Now = func() time.Time { return now }
	item := readyItem("a", "session", "ws")
	item.CandidateSource, item.ZellijStatus = CandidateSourcePriorActive, string(zellijlive.StatusDeadResurrectable)

	got := executor.Apply(context.Background(), Plan{CapturedAt: now.Add(-time.Minute), Items: []Item{item}})
	if got.Items[0].Status != StatusRestored || len(launcher.specs) != 1 {
		t.Fatalf("result=%#v launches=%d", got.Items[0], len(launcher.specs))
	}
}

func TestExecutorAllRechecksPriorRecoveryAgeForDeadSession(t *testing.T) {
	now := time.Now().UTC()
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Cataloger = &sequenceCataloger{catalogs: []zellijlive.Catalog{testCatalog(zellijlive.Session{Name: "session", Status: zellijlive.StatusDeadResurrectable})}}
	executor.Config.RecoveryMaxAge = time.Hour
	executor.Now = func() time.Time { return now }
	item := readyItem("a", "session", "ws")
	item.CandidateSource, item.ZellijStatus = CandidateSourcePriorActive, string(zellijlive.StatusDeadResurrectable)

	got := executor.Apply(context.Background(), Plan{CapturedAt: now.Add(-time.Hour - time.Second), Items: []Item{item}})
	if got.Items[0].Status != StatusStale || !strings.Contains(got.Items[0].Reason, "exceeds maximum age") || len(launcher.specs) != 0 {
		t.Fatalf("result=%#v launches=%d", got.Items[0], len(launcher.specs))
	}
}

func TestExecutorRestoresMultipleSessionsByExactPIDAndRerunIsIdempotent(t *testing.T) {
	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SESSION_NAME", "outer")
	desktop := &fakeDesktop{
		windows:  []ObservedWindow{{ID: 77, PID: 777, AppID: "kitty", WorkspaceID: "unrelated"}},
		attached: map[int]string{777: "unrelated-session"},
	}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	plan := Plan{Items: []Item{readyItem("a", "session-a", "ws-a"), readyItem("b", "session-b", "ws-b")}}

	got := executor.Apply(context.Background(), plan)
	if got.Items[0].Status != StatusRestored || got.Items[1].Status != StatusRestored || got.Summary.Restored != 2 {
		t.Fatalf("unexpected results: %#v", got)
	}
	if len(desktop.moves) != 2 || desktop.moves[0] != "ws-a" || desktop.moves[1] != "ws-b" {
		t.Fatalf("moves = %#v", desktop.moves)
	}
	if len(launcher.specs) != 2 {
		t.Fatalf("launch count = %d", len(launcher.specs))
	}
	for i, spec := range launcher.specs {
		wantSession := "session-" + string(rune('a'+i))
		want := []string{"--directory", "/tmp/" + wantSession, "zellij", "attach", "--", wantSession}
		if strings.Join(spec.Args, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("launch argv = %#v, want %#v", spec.Args, want)
		}
		for _, env := range spec.Env {
			if strings.HasPrefix(env, "ZELLIJ=") || strings.HasPrefix(env, "ZELLIJ_SESSION_NAME=") {
				t.Fatalf("nested Zellij environment leaked: %q", env)
			}
		}
	}
	if desktop.windows[0].WorkspaceID != "unrelated" {
		t.Fatal("unrelated concurrent Kitty window was moved")
	}

	rerun := executor.Apply(context.Background(), plan)
	if rerun.Items[0].Status != StatusAlreadyOpen || rerun.Items[1].Status != StatusAlreadyOpen {
		t.Fatalf("rerun results = %#v", rerun.Items)
	}
	if len(launcher.specs) != 2 {
		t.Fatalf("rerun created extra windows: %d launches", len(launcher.specs))
	}
}

func TestKittyLaunchSpecKeepsLeadingDashSessionAndCWDAsArgv(t *testing.T) {
	spec := KittyLaunchSpec("kitty", Item{CWD: "/tmp/a b", Session: "-name; touch /tmp/owned"})
	want := []string{"--directory", "/tmp/a b", "zellij", "attach", "--", "-name; touch /tmp/owned"}
	if !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("argv = %#v, want %#v", spec.Args, want)
	}
}

func TestExecutorRejectsAmbiguousSamePIDNiriMatches(t *testing.T) {
	desktop := &fakeDesktop{
		windows:  []ObservedWindow{{ID: 99, PID: 1001, AppID: "kitty", WorkspaceID: "other"}},
		attached: map[int]string{},
	}
	launcher := &fakeLauncher{desktop: desktop}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "correlation is ambiguous") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 1 || len(desktop.moves) != 0 {
		t.Fatalf("ambiguous launch cleanup/moves: kills=%d moves=%#v", launcher.processes[0].killCount(), desktop.moves)
	}
}

func TestExecutorCorrelationTimeoutKillsOnlyLaunchedProcess(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop, noWindow: true}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "exact launched-PID correlation timed out") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 1 {
		t.Fatalf("timeout cleanup kills = %d", launcher.processes[0].killCount())
	}
}

func TestExecutorAttachmentEvidenceTimeoutKillsLaunchedProcess(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop, noAttach: true}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "attachment evidence timed out") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 1 {
		t.Fatal("unverified attachment process was not cleaned up")
	}
}

func TestExecutorTransientAttachmentEvidenceCannotRestore(t *testing.T) {
	tests := []struct {
		name         string
		observations []probeObservation
	}{
		{name: "disappearing descendant resets confirmation", observations: []probeObservation{{attached: true}, {attached: false}, {attached: true}}},
		{name: "probe error resets confirmation", observations: []probeObservation{{attached: true}, {err: errors.New("attach descendant exited")}, {attached: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desktop := &fakeDesktop{attached: map[int]string{}}
			launcher := &fakeLauncher{desktop: desktop, noAttach: true}
			probe := &sequenceProbe{observations: tt.observations}
			executor := testExecutor(desktop, launcher)
			executor.Probe = probe
			got := executor.Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
			if got.Items[0].Status == StatusRestored || got.Items[0].Status != StatusFailed {
				t.Fatalf("transient evidence result = %#v", got.Items[0])
			}
			if len(desktop.moves) != 0 || launcher.processes[0].killCount() != 1 {
				t.Fatalf("transient evidence moved or leaked: moves=%#v kills=%d", desktop.moves, launcher.processes[0].killCount())
			}
		})
	}
}

func TestExecutorLaunchMustRemainAliveThroughSecondAttachmentPoll(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop, noAttach: true}
	executor := testExecutor(desktop, launcher)
	executor.Probe = &exitDuringConfirmationProbe{launcher: launcher}
	got := executor.Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusUnavailable || got.Items[0].Status == StatusRestored {
		t.Fatalf("exited confirmation result = %#v", got.Items[0])
	}
	if len(desktop.moves) != 0 || launcher.processes[0].killCount() != 1 {
		t.Fatalf("exited confirmation moved or leaked: moves=%#v kills=%d", desktop.moves, launcher.processes[0].killCount())
	}
}

func TestExecutorFailedAttachExitIsUnavailableAndCleanedUp(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop, noWindow: true, exitSet: true, exit: errors.New("exit status 1")}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "missing", "ws")}})
	if got.Items[0].Status != StatusUnavailable || !strings.Contains(got.Items[0].Reason, "attach process exited") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 1 {
		t.Fatalf("failed attach cleanup kills = %d", launcher.processes[0].killCount())
	}
}

func TestExecutorSuccessfulMoveMustBeObserved(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}, skipMoveObservation: true}
	launcher := &fakeLauncher{desktop: desktop}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "movement was not observed") || !strings.Contains(got.Items[0].Reason, "left open") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if len(desktop.moves) != 1 || launcher.processes[0].killCount() != 0 {
		t.Fatalf("unobserved move behavior: moves=%#v kills=%d", desktop.moves, launcher.processes[0].killCount())
	}
}

func TestExecutorMoveFailureLeavesAttachedWindowForSafeRerun(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}, moveErr: errors.New("move denied")}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	plan := Plan{Items: []Item{readyItem("a", "session", "ws")}}
	got := executor.Apply(context.Background(), plan)
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "left open") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 0 {
		t.Fatal("successfully attached terminal was killed after required move failure")
	}
	rerun := executor.Apply(context.Background(), plan)
	if rerun.Items[0].Status != StatusFailed || !strings.Contains(rerun.Items[0].Reason, "left open") || len(launcher.specs) != 1 {
		t.Fatalf("unsafe rerun: result=%#v launches=%d", rerun.Items[0], len(launcher.specs))
	}
}

func TestExecutorRejectsDaemonizingLauncherWithoutGuessing(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop, noWindow: true, exitSet: true}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "daemonizing launchers are unsupported") {
		t.Fatalf("result = %#v", got.Items[0])
	}
}

func TestExecutorRejectsLauncherWithoutCorrelationPID(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop, zeroPID: true}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "session", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "reliable client PID") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 1 {
		t.Fatal("unsupported launcher process was not cleaned up")
	}
}

func TestExecutorDegradedItemAttachesWithoutClaimingRestored(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	item := readyItem("a", "session", "")
	item.Status = StatusDegraded
	item.Workspace = nil
	item.Reason = "workspace target unresolved; leave window on current workspace"
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].Status != StatusDegraded || got.Summary.Degraded != 1 || got.Summary.Restored != 0 {
		t.Fatalf("result = %#v", got)
	}
	if len(desktop.moves) != 0 || launcher.processes[0].killCount() != 0 {
		t.Fatalf("degraded behavior: moves=%#v kills=%d", desktop.moves, launcher.processes[0].killCount())
	}
}

func TestExecutorReadyItemWithoutWorkspaceFailsAndCleansUp(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	item := readyItem("a", "session", "")
	item.Workspace = nil
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "no resolved workspace") {
		t.Fatalf("result = %#v", got.Items[0])
	}
	if len(desktop.moves) != 0 || launcher.processes[0].killCount() != 1 {
		t.Fatalf("missing workspace behavior: moves=%#v kills=%d", desktop.moves, launcher.processes[0].killCount())
	}
}

func TestExecutorOptionalLayoutFailureDoesNotFalsifyRequiredSuccess(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutDegraded, Reason: "column unsupported"}}
	item := readyItem("a", "session", "ws")
	floating := false
	item.CapturedPlacement = &model.Placement{Column: intPointer(2), IsFloating: &floating}
	got := executor.Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].Status != StatusRestored || got.Items[0].LayoutStatus != LayoutDegraded {
		t.Fatalf("result = %#v", got.Items[0])
	}
}

func TestExecutorSuppressesDuplicateReadyItemsWithinExecution(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	plan := Plan{Items: []Item{readyItem("a", "same", "ws-a"), readyItem("b", "same", "ws-b")}}
	got := testExecutor(desktop, launcher).Apply(context.Background(), plan)
	if got.Items[0].Status != StatusRestored || got.Items[1].Status != StatusAlreadyOpen || len(launcher.specs) != 1 {
		t.Fatalf("result=%#v launches=%d", got.Items, len(launcher.specs))
	}
}

func TestExecutorTwoPhaseRestoresExistingAndNewWindowsThenOrdersColumns(t *testing.T) {
	columnOne, columnTwo := 1, 2
	desktop := &fakeDesktop{
		windows: []ObservedWindow{
			{ID: 77, PID: 777, AppID: "firefox", WorkspaceID: "other", IsFocused: true},
			{ID: 88, PID: 888, AppID: "kitty", WorkspaceID: "old"},
		},
		attached: map[int]string{888: "existing"},
	}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop

	newItem := readyItem("new", "new", "ws")
	newItem.Workspace.Index = 2
	newItem.CapturedPlacement = &model.Placement{Column: &columnTwo}
	existing := readyItem("existing", "existing", "ws")
	existing.Status = StatusAlreadyOpen
	existing.Workspace.Index = 2
	existing.CapturedPlacement = &model.Placement{Column: &columnOne}

	got := executor.Apply(context.Background(), Plan{Items: []Item{newItem, existing}})
	if got.Items[0].Status != StatusRestored || got.Items[1].Status != StatusAlreadyOpen {
		t.Fatalf("statuses = %#v", got.Items)
	}
	if len(launcher.specs) != 1 || !reflect.DeepEqual(desktop.moves, []string{"ws", "ws"}) {
		t.Fatalf("launches=%d moves=%#v", len(launcher.specs), desktop.moves)
	}
	wantOrder := []string{"focus:2001", "column:2001:2", "focus:77"}
	if !reflect.DeepEqual(desktop.orderActions, wantOrder) {
		t.Fatalf("order actions=%#v want=%#v", desktop.orderActions, wantOrder)
	}
	for _, window := range desktop.windows {
		if (window.ID == 88 || window.ID == 2001) && (window.Row == nil || *window.Row != 1) {
			t.Fatalf("moved window row = %#v, want 1", window)
		}
	}
	if got.Items[0].LayoutStatus != LayoutApplied || got.Items[1].LayoutStatus != LayoutApplied {
		t.Fatalf("layout statuses = %#v", got.Items)
	}

	rerun := executor.Apply(context.Background(), Plan{Items: []Item{newItem, existing}})
	if len(launcher.specs) != 1 || rerun.Items[0].Status != StatusAlreadyOpen || rerun.Items[1].Status != StatusAlreadyOpen {
		t.Fatalf("rerun launched duplicate or lost attachments: launches=%d items=%#v", len(launcher.specs), rerun.Items)
	}
}

func TestExecutorRejectsDuplicateAttachRaceBeforePlacement(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}, duplicateAfterAttachCalls: 2}
	launcher := &fakeLauncher{desktop: desktop}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "same", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "another exact session attachment appeared") {
		t.Fatalf("result=%#v", got.Items[0])
	}
	if launcher.processes[0].killCount() != 1 || len(desktop.moves) != 0 {
		t.Fatalf("duplicate attach race cleanup/moves: kills=%d moves=%#v", launcher.processes[0].killCount(), desktop.moves)
	}
	if len(desktop.windows) != 2 || desktop.windows[1].ID != 999 {
		t.Fatalf("pre-existing raced attachment was mutated: %#v", desktop.windows)
	}
}

func TestExecutorRejectsDuplicateExistingAttachmentsBeforeMutation(t *testing.T) {
	desktop := &fakeDesktop{
		windows: []ObservedWindow{
			{ID: 1, PID: 101, AppID: "kitty", WorkspaceID: "old"},
			{ID: 2, PID: 102, AppID: "kitty", WorkspaceID: "old"},
		},
		attached: map[int]string{101: "same", 102: "same"},
	}
	launcher := &fakeLauncher{desktop: desktop}
	got := testExecutor(desktop, launcher).Apply(context.Background(), Plan{Items: []Item{readyItem("a", "same", "ws")}})
	if got.Items[0].Status != StatusFailed || !strings.Contains(got.Items[0].Reason, "multiple Niri windows") {
		t.Fatalf("result=%#v", got.Items[0])
	}
	if len(launcher.specs) != 0 || len(desktop.moves) != 0 {
		t.Fatalf("ambiguous attachment mutated desktop: launches=%d moves=%#v", len(launcher.specs), desktop.moves)
	}
}

func TestExecutorOrdersTargetsDescendingAcrossUnrelatedColumns(t *testing.T) {
	for _, columns := range [][]int{{1, 2, 3}, {1, 3, 2}} {
		t.Run(fmt.Sprint(columns), func(t *testing.T) {
			desktop := &fakeDesktop{
				windows: []ObservedWindow{
					{ID: 10, PID: 110, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(columns[0])}, // target b
					{ID: 20, PID: 120, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(columns[1])}, // target a
					{ID: 30, PID: 130, AppID: "firefox", WorkspaceID: "ws", Column: intPointer(columns[2]), IsFocused: true},
				},
				attached: map[int]string{110: "b", 120: "a"},
			}
			launcher := &fakeLauncher{desktop: desktop}
			executor := testExecutor(desktop, launcher)
			executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
			executor.Orderer = desktop
			a := readyItem("a", "a", "ws")
			a.Status = StatusAlreadyOpen
			a.CapturedPlacement = &model.Placement{Column: intPointer(1)}
			b := readyItem("b", "b", "ws")
			b.Status = StatusAlreadyOpen
			b.CapturedPlacement = &model.Placement{Column: intPointer(2)}

			got := executor.Apply(context.Background(), Plan{Items: []Item{a, b}})
			if got.Items[0].LayoutStatus != LayoutApplied || got.Items[1].LayoutStatus != LayoutApplied {
				t.Fatalf("layout results=%#v", got.Items)
			}
			if len(desktop.orderActions) < 3 || !reflect.DeepEqual(desktop.orderActions[:2], []string{"focus:10", "column:10:2"}) {
				t.Fatalf("ordering did not begin at the highest requested index: %#v", desktop.orderActions)
			}
			if columns[1] == 1 && (len(desktop.orderActions) < 5 || !reflect.DeepEqual(desktop.orderActions[2:4], []string{"focus:20", "column:20:1"})) {
				t.Fatalf("displaced lower target was not repaired: %#v", desktop.orderActions)
			}
			final, _ := desktop.Windows(context.Background())
			if !windowAtExactColumn(final, 20, "ws", 1) || !windowAtExactColumn(final, 10, "ws", 2) {
				t.Fatalf("absolute target order was displaced: %#v", final)
			}
		})
	}
}

func TestExecutorRepairsHigherTargetShiftedByLowerTarget(t *testing.T) {
	desktop := &fakeDesktop{
		windows: []ObservedWindow{
			{ID: 10, PID: 110, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(1)},
			{ID: 30, PID: 130, AppID: "firefox", WorkspaceID: "ws", Column: intPointer(2), IsFocused: true},
			{ID: 40, PID: 140, AppID: "firefox", WorkspaceID: "ws", Column: intPointer(3)},
			{ID: 20, PID: 120, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(4)},
		},
		attached: map[int]string{110: "high", 120: "low"},
	}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop
	high := readyItem("high", "high", "ws")
	high.Status = StatusAlreadyOpen
	high.CapturedPlacement = &model.Placement{Column: intPointer(2)}
	low := readyItem("low", "low", "ws")
	low.Status = StatusAlreadyOpen
	low.CapturedPlacement = &model.Placement{Column: intPointer(1)}

	got := executor.Apply(context.Background(), Plan{Items: []Item{low, high}})
	if got.Items[0].LayoutStatus != LayoutApplied || got.Items[1].LayoutStatus != LayoutApplied {
		t.Fatalf("layout results=%#v", got.Items)
	}
	wantPrefix := []string{"focus:10", "column:10:2", "focus:20", "column:20:1", "focus:10", "column:10:2"}
	if len(desktop.orderActions) < len(wantPrefix) || !reflect.DeepEqual(desktop.orderActions[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("higher target was not repaired by repeated descending pass: %#v", desktop.orderActions)
	}
}

func TestExecutorFinalWorkspaceObservationRejectsPostMoveRace(t *testing.T) {
	desktop := &fakeDesktop{
		windows:  []ObservedWindow{{ID: 10, PID: 110, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(2), IsFocused: true}},
		attached: map[int]string{110: "target"}, mutateAfterMoveObservation: true,
	}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop
	item := readyItem("a", "target", "ws")
	item.Status = StatusAlreadyOpen
	item.CapturedPlacement = &model.Placement{Column: intPointer(1)}

	got := executor.Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].LayoutStatus != LayoutDegraded || !strings.Contains(got.Items[0].LayoutReason, "final ordering verification") {
		t.Fatalf("post-move race result=%#v", got.Items[0])
	}
}

func TestExecutorUnexpectedFocusMutationStopsAffectedWorkspace(t *testing.T) {
	desktop := &fakeDesktop{
		windows: []ObservedWindow{
			{ID: 10, PID: 110, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(1)},
			{ID: 20, PID: 120, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(2)},
			{ID: 30, PID: 130, AppID: "firefox", WorkspaceID: "ws", Column: intPointer(3), IsFocused: true},
		},
		attached: map[int]string{110: "high", 120: "low"}, focusMutationID: 10,
	}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop
	high := readyItem("high", "high", "ws")
	high.Status = StatusAlreadyOpen
	high.CapturedPlacement = &model.Placement{Column: intPointer(2)}
	low := readyItem("low", "low", "ws")
	low.Status = StatusAlreadyOpen
	low.CapturedPlacement = &model.Placement{Column: intPointer(1)}

	got := executor.Apply(context.Background(), Plan{Items: []Item{low, high}})
	if got.Items[0].LayoutStatus != LayoutDegraded || got.Items[1].LayoutStatus != LayoutDegraded {
		t.Fatalf("unexpected mutation was reported as applied: %#v", got.Items)
	}
	if !strings.Contains(got.Items[0].LayoutReason, "unexpected workspace mutation") {
		t.Fatalf("unexpected mutation reason=%q", got.Items[0].LayoutReason)
	}
	if len(desktop.orderActions) != 2 || desktop.orderActions[0] != "focus:10" || desktop.orderActions[1] != "focus:30" {
		t.Fatalf("ordering did not stop before another column action: %#v", desktop.orderActions)
	}
}

func TestExecutorOrderingVerificationFailureDegradesOnlyAffectedWorkspace(t *testing.T) {
	for _, failure := range []string{"focus", "column"} {
		t.Run(failure, func(t *testing.T) {
			column := 2
			desktop := &fakeDesktop{attached: map[int]string{}, skipFocusObservation: map[int]bool{}, skipColumnObservation: map[int]bool{}}
			launcher := &fakeLauncher{desktop: desktop}
			executor := testExecutor(desktop, launcher)
			executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
			executor.Orderer = desktop
			first := readyItem("a", "first", "ws-a")
			first.Workspace.Index = 1
			first.CapturedPlacement = &model.Placement{Column: &column}
			second := readyItem("b", "second", "ws-b")
			second.Workspace.Index = 2
			second.CapturedPlacement = &model.Placement{Column: &column}
			if failure == "focus" {
				desktop.skipFocusObservation[2001] = true
			} else {
				desktop.skipColumnObservation[2001] = true
			}

			got := executor.Apply(context.Background(), Plan{Items: []Item{first, second}})
			if got.Items[0].LayoutStatus != LayoutDegraded || got.Items[1].LayoutStatus != LayoutApplied {
				t.Fatalf("layout results=%#v", got.Items)
			}
			if !strings.Contains(got.Items[0].LayoutReason, "workspace column ordering failed") {
				t.Fatalf("failure reason=%q", got.Items[0].LayoutReason)
			}
		})
	}
}

func TestExecutorLivePlacementRowOneAllowsOrderingActions(t *testing.T) {
	desktop := &fakeDesktop{
		windows: []ObservedWindow{
			{ID: 10, PID: 110, AppID: "kitty", WorkspaceID: "ws", Column: intPointer(2), Row: intPointer(1)},
			{ID: 20, PID: 120, AppID: "firefox", WorkspaceID: "ws", Column: intPointer(1), Row: intPointer(1), IsFocused: true},
		},
		attached: map[int]string{110: "target"},
	}
	executor := testExecutor(desktop, &fakeLauncher{desktop: desktop})
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop
	item := readyItem("a", "target", "ws")
	item.Status = StatusAlreadyOpen
	// This matches the live dry-run evidence: placement_row=1.
	item.CapturedPlacement = &model.Placement{Column: intPointer(1), Row: intPointer(1)}

	got := executor.Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].LayoutStatus != LayoutApplied {
		t.Fatalf("row-one ordering result=%#v", got.Items[0])
	}
	want := []string{"focus:10", "column:10:1", "focus:20"}
	if !reflect.DeepEqual(desktop.orderActions, want) {
		t.Fatalf("row-one ordering actions=%#v want=%#v", desktop.orderActions, want)
	}
}

func TestExecutorRejectsHiddenCapturedStackWithoutGuessing(t *testing.T) {
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop
	item := readyItem("a", "hidden-stack", "ws")
	item.CapturedPlacement = &model.Placement{Column: intPointer(1), Row: intPointer(1)}
	item.CapturedColumnOccupied = true
	got := executor.Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].LayoutStatus != LayoutUnsupported || !strings.Contains(got.Items[0].LayoutReason, "stacked rows") || len(desktop.orderActions) != 0 {
		t.Fatalf("hidden stack result=%#v actions=%#v", got.Items[0], desktop.orderActions)
	}
}

func TestExecutorRejectsCapturedStackedRowsWithoutGuessing(t *testing.T) {
	column, row := 1, 2
	desktop := &fakeDesktop{attached: map[int]string{}}
	launcher := &fakeLauncher{desktop: desktop}
	executor := testExecutor(desktop, launcher)
	executor.Layout = fakeLayout{result: LayoutResult{Status: LayoutNotRequested}}
	executor.Orderer = desktop
	item := readyItem("a", "stacked", "ws")
	item.CapturedPlacement = &model.Placement{Column: &column, Row: &row}
	got := executor.Apply(context.Background(), Plan{Items: []Item{item}})
	if got.Items[0].Status != StatusRestored || got.Items[0].LayoutStatus != LayoutUnsupported || !strings.Contains(got.Items[0].LayoutReason, "stacked rows") {
		t.Fatalf("result=%#v", got.Items[0])
	}
	if len(desktop.orderActions) != 0 {
		t.Fatalf("stacked restore guessed ordering actions: %#v", desktop.orderActions)
	}
}

func intPointer(value int) *int { return &value }
