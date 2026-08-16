package mirror

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/storelock"
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

type AttachLocalConfig struct {
	Session         string
	Workspace       string
	LauncherCommand string
	NiriCommand     string
	StateDir        string
	ProcRoot        string
	Environment     []string
}

type LocalAttachResult struct {
	WindowID       int
	AlreadyOpen    bool
	PlacementError error
}

// AttachLocal is the source-side entry point. Callers must provide a real
// deadline so a blocked catalog, launcher, Niri query, or process observation
// cannot keep the short SSH helper alive indefinitely.
func AttachLocal(ctx context.Context, cfg AttachLocalConfig) (LocalAttachResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		return LocalAttachResult{}, fmt.Errorf("source attachment requires a context deadline")
	}
	plan, err := PlanLocalAttach(cfg.Session, cfg.Workspace, cfg.LauncherCommand)
	if err != nil {
		return LocalAttachResult{}, err
	}
	if strings.TrimSpace(cfg.StateDir) == "" {
		return LocalAttachResult{}, fmt.Errorf("state directory must not be empty")
	}
	baseRunner := ExecRunner{}
	env, err := graphicalEnvironment(ctx, cfg.Environment, baseRunner)
	if err != nil {
		return LocalAttachResult{}, err
	}
	runner := ExecRunner{Env: env}
	niriCommand := strings.TrimSpace(cfg.NiriCommand)
	if niriCommand == "" {
		niriCommand = "niri"
	}
	return attachLocal(ctx, plan, cfg.StateDir, localAttachDeps{
		runner: runner,
		listWindows: func(ctx context.Context) ([]OwnedWindow, error) {
			return (WindowManager{Runner: runner, NiriCommand: niriCommand}).List(ctx, "kitty", "")
		},
		attached:     (localProcAttachmentProbe{procRoot: cfg.ProcRoot}).attached,
		catalog:      zellijlive.CommandCataloger{},
		niriCommand:  niriCommand,
		pollInterval: DefaultSourcePollInterval,
	})
}

type catalogObserver interface {
	Observe(context.Context) (zellijlive.Catalog, error)
}

type localAttachDeps struct {
	runner       Runner
	listWindows  func(context.Context) ([]OwnedWindow, error)
	attached     func(context.Context, int, string) (bool, error)
	catalog      catalogObserver
	niriCommand  string
	pollInterval time.Duration
}

func attachLocal(ctx context.Context, plan LocalAttachPlan, stateDir string, deps localAttachDeps) (result LocalAttachResult, returnErr error) {
	if _, ok := ctx.Deadline(); !ok {
		return LocalAttachResult{}, fmt.Errorf("source attachment requires a context deadline")
	}
	if deps.runner == nil || deps.listWindows == nil || deps.attached == nil || deps.catalog == nil {
		return LocalAttachResult{}, fmt.Errorf("local attachment observer is unavailable")
	}
	lock, err := acquireAttachLock(ctx, stateDir, plan.Session, deps.pollInterval)
	if err != nil {
		return LocalAttachResult{}, err
	}
	defer func() {
		if err := lock.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release source attachment lock: %w", err))
		}
	}()

	matches, err := matchingLocalWindows(ctx, plan.Session, deps)
	if err != nil {
		return LocalAttachResult{}, err
	}
	if len(matches) > 1 {
		return LocalAttachResult{}, fmt.Errorf("multiple local Kitty windows attach exact session %q", plan.Session)
	}
	if len(matches) == 1 {
		return LocalAttachResult{WindowID: matches[0].ID, AlreadyOpen: true}, nil
	}
	if err := waitForActiveSession(ctx, plan.Session, deps.catalog, deps.pollInterval); err != nil {
		return LocalAttachResult{}, err
	}
	// Recheck while holding the per-session process lock. This closes the race
	// with a window that appeared while readiness was being established.
	matches, err = matchingLocalWindows(ctx, plan.Session, deps)
	if err != nil {
		return LocalAttachResult{}, err
	}
	if len(matches) > 1 {
		return LocalAttachResult{}, fmt.Errorf("multiple local Kitty windows attach exact session %q", plan.Session)
	}
	if len(matches) == 1 {
		return LocalAttachResult{WindowID: matches[0].ID, AlreadyOpen: true}, nil
	}
	if err := deps.runner.Run(ctx, plan.Command); err != nil {
		return LocalAttachResult{}, fmt.Errorf("launch detached source Kitty: %w", err)
	}

	for {
		matches, err = matchingLocalWindows(ctx, plan.Session, deps)
		if err != nil {
			return LocalAttachResult{}, err
		}
		if len(matches) > 1 {
			return LocalAttachResult{}, fmt.Errorf("multiple local Kitty windows attach exact session %q", plan.Session)
		}
		if len(matches) == 1 {
			result := LocalAttachResult{WindowID: matches[0].ID}
			if plan.Workspace != "" {
				move := Command{Name: deps.niriCommand, Args: []string{"msg", "action", "move-window-to-workspace", "--window-id", strconv.Itoa(matches[0].ID), "--focus", "false", plan.Workspace}}
				if err := deps.runner.Run(ctx, move); err != nil {
					result.PlacementError = err
				}
			}
			return result, nil
		}
		if err := waitInterval(ctx, deps.pollInterval); err != nil {
			return LocalAttachResult{}, fmt.Errorf("source Kitty for session %q did not appear: %w", plan.Session, err)
		}
	}
}

func matchingLocalWindows(ctx context.Context, session string, deps localAttachDeps) ([]OwnedWindow, error) {
	windows, err := deps.listWindows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list local Kitty windows: %w", err)
	}
	matches := make([]OwnedWindow, 0, 1)
	for _, window := range windows {
		attached, err := deps.attached(ctx, window.PID, session)
		if err != nil {
			return nil, fmt.Errorf("inspect local Kitty window %d: %w", window.ID, err)
		}
		if attached {
			matches = append(matches, window)
		}
	}
	return matches, nil
}

func waitForActiveSession(ctx context.Context, session string, observer catalogObserver, interval time.Duration) error {
	var lastErr error
	for {
		catalog, err := observer.Observe(ctx)
		if err == nil {
			observed := catalog.Exact(session)
			if observed.Status == zellijlive.StatusActive {
				return nil
			}
			lastErr = fmt.Errorf("exact Zellij session %q has status %s", session, observed.Status)
		} else {
			lastErr = err
		}
		if err := waitInterval(ctx, interval); err != nil {
			if lastErr != nil {
				return fmt.Errorf("exact Zellij session %q did not become active: %v: %w", session, lastErr, err)
			}
			return fmt.Errorf("exact Zellij session %q did not become active: %w", session, err)
		}
	}
}

func waitInterval(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = DefaultSourcePollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func acquireAttachLock(ctx context.Context, stateDir string, session string, interval time.Duration) (*storelock.Lock, error) {
	sum := sha256.Sum256([]byte(session))
	lockRoot := filepath.Join(stateDir, "mirror", "attach-locks", fmt.Sprintf("%x", sum[:]))
	for {
		lock, err := storelock.Acquire(lockRoot)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, storelock.ErrLocked) {
			return nil, fmt.Errorf("acquire source attachment lock: %w", err)
		}
		if err := waitInterval(ctx, interval); err != nil {
			return nil, fmt.Errorf("wait for source attachment lock: %w", err)
		}
	}
}

type localProcAttachmentProbe struct {
	procRoot string
}

func (probe localProcAttachmentProbe) attached(ctx context.Context, rootPID int, session string) (bool, error) {
	if rootPID <= 0 || !generatedSessionPattern.MatchString(session) {
		return false, nil
	}
	return procmeta.DescendantArgvMatchContext(ctx, probe.procRoot, rootPID, func(args []string) bool {
		return len(args) == 6 && filepath.Base(args[0]) == "zellij" &&
			args[1] == "attach" && args[2] == session && args[3] == "options" &&
			args[4] == "--on-force-close" && args[5] == "detach"
	})
}

func graphicalEnvironment(ctx context.Context, env []string, runner Runner) ([]string, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	out, err := runner.Output(ctx, Command{Name: "systemctl", Args: []string{"--user", "show-environment"}})
	manager := parseGraphicalEnvironment(out)
	if err == nil && graphicalEnvironmentComplete(manager) {
		merged := append([]string(nil), env...)
		for _, name := range []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"} {
			merged = setEnv(merged, name, manager[name])
		}
		return merged, nil
	}
	inherited := environmentValues(env)
	if graphicalEnvironmentComplete(inherited) {
		return append([]string(nil), env...), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read graphical environment from user manager: %w", err)
	}
	return nil, fmt.Errorf("source user manager has no complete Niri/Wayland environment")
}

func parseGraphicalEnvironment(payload []byte) map[string]string {
	values := make(map[string]string, 3)
	for _, line := range strings.Split(string(payload), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch name {
		case "NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR":
			values[name] = value
		}
	}
	return values
}

func environmentValues(env []string) map[string]string {
	values := make(map[string]string, 3)
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}

func graphicalEnvironmentComplete(values map[string]string) bool {
	for _, name := range []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"} {
		if strings.TrimSpace(values[name]) == "" {
			return false
		}
	}
	return true
}

func setEnv(env []string, name string, value string) []string {
	prefix := name + "="
	entry := prefix + value
	for i := range env {
		if strings.HasPrefix(env[i], prefix) {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}
