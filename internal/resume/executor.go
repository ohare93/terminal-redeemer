package resume

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
)

type LayoutStatus string

const (
	LayoutNotRequested LayoutStatus = "not_requested"
	LayoutApplied      LayoutStatus = "applied"
	LayoutDegraded     LayoutStatus = "degraded"
	LayoutUnsupported  LayoutStatus = "unsupported"
)

type LaunchSpec struct {
	Command string
	Args    []string
	Env     []string
}

type Process interface {
	PID() int
	Done() <-chan error
	Kill() error
}

type Launcher interface {
	Start(context.Context, LaunchSpec) (Process, error)
}

type ObservedWindow struct {
	ID          int
	PID         int
	AppID       string
	WorkspaceID string
}

type WindowObserver interface {
	Windows(context.Context) ([]ObservedWindow, error)
}

type AttachmentProbe interface {
	Attached(context.Context, int, string) (bool, error)
}

type WorkspaceMover interface {
	MoveToWorkspace(context.Context, int, WorkspaceTarget) error
}

type LayoutResult struct {
	Status LayoutStatus
	Reason string
}

type LayoutApplier interface {
	ApplyLayout(context.Context, int, model.Placement) LayoutResult
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type ExecutorConfig struct {
	LauncherCommand string
	Timeout         time.Duration
	PollInterval    time.Duration
}

type Executor struct {
	Config   ExecutorConfig
	Launcher Launcher
	Observer WindowObserver
	Probe    AttachmentProbe
	Mover    WorkspaceMover
	Layout   LayoutApplier
	Sleeper  Sleeper
}

// Apply executes actionable plan items sequentially. Sequential execution is
// intentional: it keeps process ownership and exact PID correlation local to
// one item while unrelated windows may appear freely.
func (e Executor) Apply(ctx context.Context, plan Plan) Plan {
	plan.Items = append([]Item(nil), plan.Items...)
	for i := range plan.Items {
		item := &plan.Items[i]
		if item.Status != StatusReady && item.Status != StatusDegraded {
			continue
		}
		e.applyItem(ctx, item)
	}
	plan.Summary = Summary{}
	plan.summarize()
	return plan
}

func (e Executor) applyItem(ctx context.Context, item *Item) {
	if err := e.validate(); err != nil {
		item.Status = StatusFailed
		item.Reason = err.Error()
		return
	}

	open, err := e.sessionAlreadyOpen(ctx, item.Session)
	if err != nil {
		item.Status = StatusFailed
		item.Reason = "cannot safely check for an already-open session: " + err.Error()
		return
	}
	if open {
		item.Status = StatusAlreadyOpen
		item.Reason = "matching Zellij session became open before launch"
		return
	}

	spec := KittyLaunchSpec(e.Config.LauncherCommand, *item)
	process, err := e.Launcher.Start(ctx, spec)
	if err != nil {
		item.Status = StatusFailed
		item.Reason = "Kitty launch failed: " + err.Error()
		return
	}
	if process == nil || process.PID() <= 0 || process.Done() == nil {
		if process != nil {
			_ = process.Kill()
		}
		item.Status = StatusFailed
		item.Reason = "launcher does not provide a reliable client PID for Niri correlation"
		return
	}

	window, outcome, reason := e.waitForWindow(ctx, process)
	if outcome != "" {
		_ = process.Kill()
		item.Status = outcome
		item.Reason = reason
		return
	}

	attached, outcome, reason := e.waitForAttachment(ctx, process, item.Session)
	if !attached {
		_ = process.Kill()
		item.Status = outcome
		item.Reason = reason
		return
	}

	wasDegraded := item.Status == StatusDegraded
	if !wasDegraded {
		if item.Workspace == nil {
			_ = process.Kill()
			item.Status = StatusFailed
			item.Reason = "ready item has no resolved workspace target"
			return
		}
		moveCtx, cancel := context.WithTimeout(ctx, e.timeout())
		err = e.Mover.MoveToWorkspace(moveCtx, window.ID, *item.Workspace)
		cancel()
		if err != nil {
			// Attachment succeeded. Keep the terminal open so a rerun observes it
			// as already_open instead of creating a duplicate.
			item.Status = StatusFailed
			item.Reason = "workspace move failed; attached terminal left open: " + err.Error()
			return
		}
		if ok, reason := e.waitForWorkspace(ctx, process, window, item.Workspace.ID); !ok {
			item.Status = StatusFailed
			item.Reason = reason + "; attached terminal left open"
			return
		}
		item.Status = StatusRestored
		item.Reason = ""
	} else {
		item.Status = StatusDegraded
		if item.Reason == "" {
			item.Reason = "session attached without a resolved workspace target"
		}
	}

	e.applyOptionalLayout(ctx, item, window.ID)
}

func (e Executor) validate() error {
	if strings.TrimSpace(e.Config.LauncherCommand) == "" {
		return errors.New("resume launcher command is empty")
	}
	if e.Launcher == nil || e.Observer == nil || e.Probe == nil || e.Mover == nil {
		return errors.New("resume executor dependency is unavailable")
	}
	if e.timeout() <= 0 || e.pollInterval() <= 0 || e.pollInterval() > e.timeout() {
		return errors.New("resume timeout and poll interval must be positive, and poll interval must not exceed timeout")
	}
	return nil
}

func (e Executor) sessionAlreadyOpen(ctx context.Context, session string) (bool, error) {
	windows, err := e.Observer.Windows(ctx)
	if err != nil {
		return false, err
	}
	for _, window := range windows {
		if !isTerminal(window.AppID) || window.PID <= 0 {
			continue
		}
		attached, err := e.Probe.Attached(ctx, window.PID, session)
		if err != nil {
			return false, err
		}
		if attached {
			return true, nil
		}
	}
	return false, nil
}

func (e Executor) waitForWindow(ctx context.Context, process Process) (ObservedWindow, Status, string) {
	waitCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	var lastErr error
	for {
		select {
		case err := <-process.Done():
			if err == nil {
				return ObservedWindow{}, StatusFailed, "launcher exited before exact Niri PID correlation; forking or daemonizing launchers are unsupported"
			}
			return ObservedWindow{}, StatusUnavailable, processExitReason("Zellij attach process exited before its Niri window appeared", err)
		default:
		}

		windows, err := e.Observer.Windows(waitCtx)
		if err != nil {
			lastErr = err
		} else {
			matches := make([]ObservedWindow, 0, 1)
			for _, window := range windows {
				if window.PID == process.PID() {
					matches = append(matches, window)
				}
			}
			if len(matches) == 1 && matches[0].ID > 0 {
				return matches[0], "", ""
			}
			if len(matches) > 1 {
				return ObservedWindow{}, StatusFailed, "Niri reported multiple windows for the launched client PID; correlation is ambiguous"
			}
		}
		if err := e.sleep(waitCtx); err != nil {
			if lastErr != nil {
				return ObservedWindow{}, StatusFailed, "exact launched-PID correlation timed out: " + lastErr.Error()
			}
			return ObservedWindow{}, StatusFailed, "exact launched-PID correlation timed out; launcher may fork, daemonize, or lack Niri PID support"
		}
	}
}

func (e Executor) waitForAttachment(ctx context.Context, process Process, session string) (bool, Status, string) {
	waitCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	var lastErr error
	confirmations := 0
	for {
		select {
		case err := <-process.Done():
			return false, StatusUnavailable, processExitReason("zellij attach exited without attachment evidence", err)
		default:
		}
		attached, err := e.Probe.Attached(waitCtx, process.PID(), session)
		if err != nil {
			lastErr = err
			confirmations = 0
		} else if !attached {
			confirmations = 0
		} else {
			// Evidence must remain true across two polls, and the owning launch
			// process must still be alive after each observation. This prevents a
			// transient attach child from being accepted while it is exiting.
			select {
			case err := <-process.Done():
				return false, StatusUnavailable, processExitReason("zellij attach exited during attachment confirmation", err)
			default:
				confirmations++
			}
			if confirmations >= 2 {
				return true, "", ""
			}
		}
		if err := e.sleep(waitCtx); err != nil {
			if lastErr != nil {
				return false, StatusFailed, "attachment evidence timed out: " + lastErr.Error()
			}
			return false, StatusFailed, "attachment evidence timed out"
		}
	}
}

func (e Executor) waitForWorkspace(ctx context.Context, process Process, expected ObservedWindow, workspaceID string) (bool, string) {
	waitCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	var lastErr error
	for {
		select {
		case err := <-process.Done():
			return false, processExitReason("attached terminal exited before workspace movement was observed", err)
		default:
		}
		windows, err := e.Observer.Windows(waitCtx)
		if err != nil {
			lastErr = err
		} else {
			for _, window := range windows {
				if window.ID == expected.ID && window.PID == expected.PID && window.WorkspaceID == workspaceID {
					return true, ""
				}
			}
		}
		if err := e.sleep(waitCtx); err != nil {
			if lastErr != nil {
				return false, "workspace movement could not be verified: " + lastErr.Error()
			}
			return false, "workspace movement was not observed before timeout"
		}
	}
}

func (e Executor) applyOptionalLayout(ctx context.Context, item *Item, windowID int) {
	if item.CapturedPlacement == nil {
		item.LayoutStatus = LayoutNotRequested
		return
	}
	if e.Layout == nil {
		item.LayoutStatus = LayoutUnsupported
		item.LayoutReason = "optional Niri layout actions are unavailable"
		return
	}
	layoutCtx, cancel := context.WithTimeout(ctx, e.timeout())
	result := e.Layout.ApplyLayout(layoutCtx, windowID, *item.CapturedPlacement)
	cancel()
	item.LayoutStatus = result.Status
	item.LayoutReason = result.Reason
}

func (e Executor) timeout() time.Duration { return e.Config.Timeout }

func (e Executor) pollInterval() time.Duration { return e.Config.PollInterval }

func (e Executor) sleep(ctx context.Context) error {
	if e.Sleeper != nil {
		return e.Sleeper.Sleep(ctx, e.pollInterval())
	}
	timer := time.NewTimer(e.pollInterval())
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func processExitReason(prefix string, err error) string {
	if err == nil {
		return prefix
	}
	return prefix + ": " + err.Error()
}

// KittyLaunchSpec deliberately uses no shell and preserves Kitty's normal app
// ID. Zellij receives attach-only argv; resume never creates a missing session.
func KittyLaunchSpec(command string, item Item) LaunchSpec {
	args := make([]string, 0, 6)
	if cwd := strings.TrimSpace(item.CWD); cwd != "" {
		args = append(args, "--directory", cwd)
	}
	args = append(args, "zellij", "attach", "--", item.Session)
	return LaunchSpec{Command: strings.TrimSpace(command), Args: args, Env: withoutZellijEnvironment(os.Environ())}
}

func withoutZellijEnvironment(env []string) []string {
	blocked := map[string]struct{}{
		"ZELLIJ": {}, "ZELLIJ_SESSION_NAME": {}, "ZELLIJ_PANE_ID": {},
		"ZELLIJ_TAB_INDEX": {}, "ZELLIJ_TAB_NAME": {},
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := blocked[name]; !ok {
			out = append(out, entry)
		}
	}
	return out
}

type ExecLauncher struct{}

func (ExecLauncher) Start(ctx context.Context, spec LaunchSpec) (Process, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("empty command")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Do not bind Kitty's lifetime to the short-lived resume command context;
	// successful attached terminals must survive after Apply returns.
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &execProcess{cmd: cmd, done: make(chan error, 1)}
	go func() {
		process.done <- cmd.Wait()
		close(process.done)
	}()
	return process, nil
}

type execProcess struct {
	cmd  *exec.Cmd
	done chan error
	once sync.Once
}

func (p *execProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execProcess) Done() <-chan error { return p.done }

func descendantPIDs(rootPID int) []int {
	children, err := processChildren("/proc")
	if err != nil {
		return nil
	}
	queue := append([]int(nil), children[rootPID]...)
	out := make([]int, 0, len(queue))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	// Kill leaves before their parents.
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func (p *execProcess) Kill() error {
	var err error
	p.once.Do(func() {
		if p != nil && p.cmd != nil && p.cmd.Process != nil {
			// Kitty may put its PTY child in another process group. Snapshot and
			// kill descendants before the parent can exit and reparent them, then
			// kill the dedicated outer group as a final sweep.
			for _, pid := range descendantPIDs(p.cmd.Process.Pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			err = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				err = p.cmd.Process.Kill()
			}
		}
	})
	return err
}
