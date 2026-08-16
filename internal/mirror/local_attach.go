package mirror

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type LocalAttachPlan struct {
	Session   string
	Workspace string
	Command   Command
}

func PlanLocalAttach(session string, workspace string, launcherCommand string) (LocalAttachPlan, error) {
	if !generatedSessionPattern.MatchString(session) {
		return LocalAttachPlan{}, fmt.Errorf("invalid generated mirror session name %q", session)
	}
	if err := ValidateWorkspaceReference(workspace); err != nil {
		return LocalAttachPlan{}, err
	}
	if strings.TrimSpace(launcherCommand) == "" {
		return LocalAttachPlan{}, fmt.Errorf("launcher command must not be empty")
	}
	args := []string{
		"--detach",
		"--class", "kitty",
		"--override", "confirm_os_window_close=0",
		"--title", "redeem source: " + session,
		"-e", "zellij", "attach", session, "options", "--on-force-close", "detach",
	}
	return LocalAttachPlan{Session: session, Workspace: workspace, Command: Command{Name: launcherCommand, Args: args}}, nil
}

type SessionChecker interface {
	Active(context.Context, string) (bool, error)
}

type LiveSessionChecker struct{}

func (LiveSessionChecker) Active(ctx context.Context, session string) (bool, error) {
	catalog, err := (zellijlive.CommandCataloger{}).Observe(ctx)
	if err != nil {
		return false, err
	}
	return catalog.Exact(session).Status == zellijlive.StatusActive, nil
}

type LocalWindowLister interface {
	List(context.Context) ([]OwnedWindow, error)
}

type KittyWindowLister struct {
	Manager WindowManager
}

func (lister KittyWindowLister) List(ctx context.Context) ([]OwnedWindow, error) {
	return lister.Manager.List(ctx, "kitty", "")
}

type AttachmentProbe interface {
	Attached(context.Context, int, string) (bool, error)
}

// LocalProcAttachmentProbe accepts only the exact local argv emitted by
// PlanLocalAttach. Seeing the session in a title or in Kitty's own argv is not
// attachment evidence.
type LocalProcAttachmentProbe struct {
	ProcRoot string
}

func (probe LocalProcAttachmentProbe) Attached(_ context.Context, rootPID int, session string) (bool, error) {
	if rootPID <= 0 || !generatedSessionPattern.MatchString(session) {
		return false, nil
	}
	return procmeta.DescendantArgvMatch(probe.ProcRoot, rootPID, func(args []string) bool {
		return len(args) == 6 && filepath.Base(args[0]) == "zellij" &&
			args[1] == "attach" && args[2] == session && args[3] == "options" &&
			args[4] == "--on-force-close" && args[5] == "detach"
	})
}

type LocalAttacher struct {
	Runner       Runner
	Windows      LocalWindowLister
	Probe        AttachmentProbe
	Sessions     SessionChecker
	Wait         WaitFunc
	Timeout      time.Duration
	PollInterval time.Duration
	NiriCommand  string
}

type LocalAttachResult struct {
	WindowID       int
	AlreadyOpen    bool
	PlacementError error
}

func (attacher LocalAttacher) Attach(ctx context.Context, plan LocalAttachPlan) (LocalAttachResult, error) {
	if attacher.Windows == nil || attacher.Probe == nil || attacher.Sessions == nil {
		return LocalAttachResult{}, fmt.Errorf("local attachment observer is unavailable")
	}
	matches, err := attacher.matchingWindows(ctx, plan.Session)
	if err != nil {
		return LocalAttachResult{}, err
	}
	if len(matches) > 1 {
		return LocalAttachResult{}, fmt.Errorf("multiple local Kitty windows attach exact session %q", plan.Session)
	}
	if len(matches) == 1 {
		return LocalAttachResult{WindowID: matches[0].ID, AlreadyOpen: true}, nil
	}
	active, err := attacher.Sessions.Active(ctx, plan.Session)
	if err != nil {
		return LocalAttachResult{}, fmt.Errorf("verify exact Zellij session: %w", err)
	}
	if !active {
		return LocalAttachResult{}, fmt.Errorf("exact Zellij session %q is not active", plan.Session)
	}

	runner := attacher.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if err := runner.Run(ctx, plan.Command); err != nil {
		return LocalAttachResult{}, fmt.Errorf("launch detached source Kitty: %w", err)
	}

	timeout := attacher.Timeout
	if timeout <= 0 {
		timeout = DefaultSourceReadyTimeout
	}
	interval := attacher.PollInterval
	if interval <= 0 {
		interval = DefaultSourcePollInterval
	}
	wait := attacher.Wait
	if wait == nil {
		wait = waitContext
	}
	attempts := int(timeout/interval) + 1
	for attempt := 0; attempt < attempts; attempt++ {
		matches, err = attacher.matchingWindows(ctx, plan.Session)
		if err != nil {
			return LocalAttachResult{}, err
		}
		if len(matches) > 1 {
			return LocalAttachResult{}, fmt.Errorf("multiple local Kitty windows attach exact session %q", plan.Session)
		}
		if len(matches) == 1 {
			result := LocalAttachResult{WindowID: matches[0].ID}
			if plan.Workspace != "" {
				command := strings.TrimSpace(attacher.NiriCommand)
				if command == "" {
					command = "niri"
				}
				move := Command{Name: command, Args: []string{"msg", "action", "move-window-to-workspace", "--window-id", strconv.Itoa(matches[0].ID), "--focus", "false", plan.Workspace}}
				if err := runner.Run(ctx, move); err != nil {
					result.PlacementError = err
				}
			}
			return result, nil
		}
		if attempt+1 < attempts {
			if err := wait(ctx, interval); err != nil {
				return LocalAttachResult{}, err
			}
		}
	}
	return LocalAttachResult{}, fmt.Errorf("source Kitty for session %q did not appear within %s", plan.Session, timeout)
}

func (attacher LocalAttacher) matchingWindows(ctx context.Context, session string) ([]OwnedWindow, error) {
	windows, err := attacher.Windows.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list local Kitty windows: %w", err)
	}
	matches := make([]OwnedWindow, 0, 1)
	for _, window := range windows {
		attached, err := attacher.Probe.Attached(ctx, window.PID, session)
		if err != nil {
			return nil, fmt.Errorf("inspect local Kitty window %d: %w", window.ID, err)
		}
		if attached {
			matches = append(matches, window)
		}
	}
	return matches, nil
}
