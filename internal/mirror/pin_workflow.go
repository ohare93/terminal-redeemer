package mirror

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type SaveResult struct {
	Pin       Pin
	Untracked int
	Ambiguous int
}

// BuildPin joins only exact live local projections to exact sessions from one
// fresh source snapshot. Duplicate local projections fail closed as ambiguous.
func BuildPin(snapshot Snapshot, sourceHost string, windows []OwnedWindow, workspaces []OwnedWorkspace, inventory ProjectionInventory) (SaveResult, error) {
	if err := ValidateDestination(sourceHost); err != nil {
		return SaveResult{}, err
	}
	profile := strings.TrimSpace(snapshot.Profile)
	if err := validateText("source profile", profile, 128, false); err != nil {
		return SaveResult{}, err
	}
	sources := make(map[string]Window)
	for _, window := range Discover(snapshot) {
		session := SessionName(window)
		if _, exists := sources[session]; !exists {
			sources[session] = window
		}
	}
	workspaceByID := make(map[string]OwnedWorkspace, len(workspaces))
	for _, workspace := range workspaces {
		id, _ := valueAsString(workspace.ID)
		if id != "" {
			workspaceByID[id] = workspace
		}
	}
	windowByID := make(map[int]OwnedWindow, len(windows))
	for _, window := range windows {
		windowByID[window.ID] = window
	}
	counts := make(map[string]int)
	for _, projection := range inventory.Exact {
		if projection.SourceHost == sourceHost {
			counts[projection.Session]++
		}
	}
	pin := Pin{V: PinSchemaVersion, SourceHost: sourceHost, SourceProfile: profile, Projections: []PinnedProjection{}}
	ambiguous := 0
	for _, projection := range inventory.Exact {
		if projection.SourceHost != sourceHost {
			continue
		}
		if counts[projection.Session] != 1 {
			ambiguous++
			continue
		}
		source, found := sources[projection.Session]
		if !found {
			continue
		}
		local := windowByID[projection.Window.ID]
		workspaceID, _ := valueAsString(local.WorkspaceID)
		workspace := workspaceByID[workspaceID]
		name, _ := valueAsString(workspace.Name)
		item := PinnedProjection{
			Session: projection.Session, Workspace: WorkspaceSelector{Name: strings.TrimSpace(name), Index: workspace.Index},
			Order: source.Order, IsFloating: local.IsFloating,
			TileSize: append([]float64(nil), local.Layout.TileSize...), WindowSize: append([]int(nil), local.Layout.WindowSize...),
		}
		if source.Terminal != nil {
			item.RemoteCWD = strings.TrimSpace(source.Terminal.CWD)
		}
		pin.Projections = append(pin.Projections, item)
	}
	pin = normalizePin(pin)
	if err := pin.Validate(); err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Pin: pin, Untracked: len(inventory.Untracked), Ambiguous: ambiguous}, nil
}

type ApplyStatus string

const (
	ApplyReady       ApplyStatus = "ready"
	ApplyOpened      ApplyStatus = "opened"
	ApplyAlreadyOpen ApplyStatus = "already_open"
	ApplyMissing     ApplyStatus = "missing"
	ApplyAmbiguous   ApplyStatus = "ambiguous"
	ApplyFailed      ApplyStatus = "failed"
)

type ApplyItem struct {
	PinnedProjection
	Status       ApplyStatus
	Reason       string
	WindowID     int
	LayoutIssues []string
	target       string
}

type ApplyResult struct {
	Items     []ApplyItem
	Untracked int
}

type ApplyConfig struct {
	Pin             Pin
	Snapshot        Snapshot
	SourceHost      string
	SSHCommand      string
	SSHOptions      []string
	LauncherCommand string
	AppID           string
	NiriCommand     string
	Timeout         time.Duration
	PollInterval    time.Duration
	DryRun          bool
}

type ApplyDeps struct {
	Runner      Runner
	Manager     WindowManager
	Inspector   ProjectionInspector
	ListWindows func(context.Context) ([]OwnedWindow, error)
	Workspaces  func(context.Context) ([]OwnedWorkspace, error)
	Sleep       func(context.Context, time.Duration) error
}

func ApplyPinned(ctx context.Context, cfg ApplyConfig, deps ApplyDeps) (ApplyResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		return ApplyResult{}, fmt.Errorf("mirror apply requires a context deadline")
	}
	if err := cfg.Pin.Validate(); err != nil {
		return ApplyResult{}, err
	}
	if cfg.Pin.SourceHost != cfg.SourceHost || cfg.Pin.SourceProfile != cfg.Snapshot.Profile {
		return ApplyResult{}, fmt.Errorf("pin identity does not match fresh source snapshot")
	}
	if cfg.Timeout <= 0 || cfg.PollInterval <= 0 || cfg.PollInterval > cfg.Timeout {
		return ApplyResult{}, fmt.Errorf("apply timeout and poll interval must be positive, and poll interval must not exceed timeout")
	}
	if deps.Runner == nil || deps.Inspector == nil {
		return ApplyResult{}, fmt.Errorf("mirror apply dependency is unavailable")
	}
	if deps.Manager.Runner == nil {
		deps.Manager.Runner = deps.Runner
	}
	if strings.TrimSpace(deps.Manager.NiriCommand) == "" {
		deps.Manager.NiriCommand = cfg.NiriCommand
	}
	if deps.ListWindows == nil {
		deps.ListWindows = func(ctx context.Context) ([]OwnedWindow, error) { return deps.Manager.List(ctx, cfg.AppID, "") }
	}
	if deps.Workspaces == nil {
		deps.Workspaces = deps.Manager.Workspaces
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepContext
	}
	windows, err := deps.ListWindows(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	inventory, err := deps.Inspector.Inspect(ctx, windows)
	if err != nil {
		return ApplyResult{}, err
	}
	workspaces, workspaceErr := deps.Workspaces(ctx)
	result := prepareApply(cfg.Pin, cfg.Snapshot, inventory, workspaces)
	if workspaceErr != nil {
		for i := range result.Items {
			if result.Items[i].Status == ApplyReady {
				result.Items[i].Reason = "destination workspace inventory unavailable: " + workspaceErr.Error()
			}
		}
	}
	if cfg.DryRun {
		return result, nil
	}
	for i := range result.Items {
		item := &result.Items[i]
		if item.Status != ApplyReady {
			continue
		}
		openCount, err := exactProjectionCount(ctx, cfg, item.Session, deps)
		if err != nil {
			item.Status, item.Reason = ApplyFailed, "cannot safely recheck exact projection: "+err.Error()
			continue
		}
		if openCount > 1 {
			item.Status, item.Reason = ApplyAmbiguous, "multiple exact projections appeared before launch"
			continue
		}
		if openCount == 1 {
			item.Status, item.Reason = ApplyAlreadyOpen, "exact projection appeared before launch"
			continue
		}
		window := Window{Order: item.Order, Title: item.Session, ZellijSession: item.Session, Terminal: &Terminal{CWD: item.RemoteCWD, ZellijSession: item.Session}}
		plan, err := PlanLaunch(window, LaunchConfig{SourceHost: cfg.SourceHost, SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions, LauncherCommand: cfg.LauncherCommand, AppID: cfg.AppID})
		if err != nil {
			item.Status, item.Reason = ApplyFailed, err.Error()
			continue
		}
		launchCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		err = deps.Runner.Run(launchCtx, plan.Command)
		cancel()
		if err != nil {
			item.Status, item.Reason = ApplyFailed, "attach-only launch failed: "+err.Error()
			continue
		}
		windowID, err := waitForProjection(ctx, cfg, *item, deps)
		if err != nil {
			item.Status, item.Reason = ApplyFailed, err.Error()
			continue
		}
		item.WindowID = windowID
		item.LayoutIssues = applyPinnedPlacement(ctx, cfg, *item, windowID, deps.Runner)
		item.Status = ApplyOpened
		if len(item.LayoutIssues) > 0 {
			item.Reason = strings.Join(item.LayoutIssues, "; ")
		}
	}
	return result, nil
}

func prepareApply(pin Pin, snapshot Snapshot, inventory ProjectionInventory, workspaces []OwnedWorkspace) ApplyResult {
	active := make(map[string]struct{})
	for _, window := range Discover(snapshot) {
		active[SessionName(window)] = struct{}{}
	}
	open := make(map[string]int)
	for _, projection := range inventory.Exact {
		if projection.SourceHost == pin.SourceHost {
			open[projection.Session]++
		}
	}
	result := ApplyResult{Untracked: len(inventory.Untracked)}
	for _, pinned := range normalizePin(pin).Projections {
		item := ApplyItem{PinnedProjection: pinned}
		switch {
		case open[pinned.Session] > 1:
			item.Status, item.Reason = ApplyAmbiguous, "multiple exact projections are already open"
		case open[pinned.Session] == 1:
			item.Status = ApplyAlreadyOpen
		case !containsSession(active, pinned.Session):
			item.Status, item.Reason = ApplyMissing, "exact ACTIVE source session is unavailable"
		default:
			item.Status = ApplyReady
			item.target = resolveWorkspaceReference(pinned.Workspace, workspaces)
			if item.target == "" {
				item.Reason = "destination workspace unavailable; launch will remain on current workspace"
			}
		}
		result.Items = append(result.Items, item)
	}
	return result
}

func containsSession(set map[string]struct{}, session string) bool { _, ok := set[session]; return ok }

func resolveWorkspaceReference(selector WorkspaceSelector, workspaces []OwnedWorkspace) string {
	if selector.Name != "" {
		matches := 0
		for _, workspace := range workspaces {
			name, _ := valueAsString(workspace.Name)
			if name == selector.Name {
				matches++
			}
		}
		if matches == 1 {
			return selector.Name
		}
	}
	if selector.Index > 0 {
		for _, workspace := range workspaces {
			if workspace.Index == selector.Index {
				return strconv.Itoa(selector.Index)
			}
		}
	}
	return ""
}

func exactProjectionCount(ctx context.Context, cfg ApplyConfig, session string, deps ApplyDeps) (int, error) {
	windows, err := deps.ListWindows(ctx)
	if err != nil {
		return 0, err
	}
	inventory, err := deps.Inspector.Inspect(ctx, windows)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, projection := range inventory.Exact {
		if projection.SourceHost == cfg.SourceHost && projection.Session == session {
			count++
		}
	}
	return count, nil
}

func waitForProjection(ctx context.Context, cfg ApplyConfig, item ApplyItem, deps ApplyDeps) (int, error) {
	waitCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	for {
		windows, err := deps.ListWindows(waitCtx)
		if err == nil {
			inventory, inspectErr := deps.Inspector.Inspect(waitCtx, windows)
			if inspectErr == nil {
				matches := make([]Projection, 0, 1)
				for _, projection := range inventory.Exact {
					if projection.SourceHost == cfg.SourceHost && projection.Session == item.Session {
						matches = append(matches, projection)
					}
				}
				if len(matches) == 1 {
					return matches[0].Window.ID, nil
				}
				if len(matches) > 1 {
					return 0, fmt.Errorf("new projection evidence for %q is ambiguous", item.Session)
				}
			}
		}
		if err := deps.Sleep(waitCtx, cfg.PollInterval); err != nil {
			return 0, fmt.Errorf("new projection for %q did not appear: %w", item.Session, err)
		}
	}
}

func applyPinnedPlacement(ctx context.Context, cfg ApplyConfig, item ApplyItem, windowID int, runner Runner) []string {
	issues := make([]string, 0, 4)
	run := func(action string, args ...string) {
		actionCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		err := runner.Run(actionCtx, Command{Name: cfg.NiriCommand, Args: append([]string{"msg", "action", action}, args...)})
		cancel()
		if err != nil {
			issues = append(issues, err.Error())
		}
	}
	if item.target != "" {
		run("move-window-to-workspace", "--window-id", strconv.Itoa(windowID), "--focus", "false", item.target)
	} else {
		issues = append(issues, "destination workspace unavailable")
	}
	floatAction := "move-window-to-tiling"
	if item.IsFloating {
		floatAction = "move-window-to-floating"
	}
	run(floatAction, "--id", strconv.Itoa(windowID))
	width, height, ok := pinnedSize(item.PinnedProjection)
	if ok {
		run("set-window-width", "--id", strconv.Itoa(windowID), strconv.Itoa(width))
		run("set-window-height", "--id", strconv.Itoa(windowID), strconv.Itoa(height))
	}
	return issues
}

func pinnedSize(item PinnedProjection) (int, int, bool) {
	if item.IsFloating && len(item.WindowSize) == 2 {
		return item.WindowSize[0], item.WindowSize[1], true
	}
	if len(item.TileSize) == 2 {
		return int(math.Round(item.TileSize[0])), int(math.Round(item.TileSize[1])), true
	}
	if len(item.WindowSize) == 2 {
		return item.WindowSize[0], item.WindowSize[1], true
	}
	return 0, 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
