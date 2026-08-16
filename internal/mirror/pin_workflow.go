package mirror

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/resume"
)

type SaveResult struct {
	Pin       Pin
	Untracked int
	Ambiguous int
}

// BuildPin joins exact live local projections to exact sessions from one fresh
// transport-bound source snapshot. Local Niri observation order is retained.
func BuildPin(snapshot Snapshot, sourceHost string, windows []OwnedWindow, workspaces []OwnedWorkspace, inventory ProjectionInventory) (SaveResult, error) {
	if err := ValidateDestination(sourceHost); err != nil {
		return SaveResult{}, err
	}
	if snapshot.Host != sourceHost {
		return SaveResult{}, fmt.Errorf("fresh source snapshot host %q does not match requested SSH destination %q", snapshot.Host, sourceHost)
	}
	profile := snapshot.Profile
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
	projectionByWindow := make(map[int]Projection, len(inventory.Exact))
	counts := make(map[string]int)
	for _, projection := range inventory.Exact {
		projectionByWindow[projection.Window.ID] = projection
		if projection.SourceHost == sourceHost {
			counts[projection.Session]++
		}
	}
	pin := Pin{V: PinSchemaVersion, SourceHost: sourceHost, SourceProfile: profile, Projections: []PinnedProjection{}}
	ambiguous := len(inventory.Ambiguous)
	for localOrder, local := range windows {
		projection, exact := projectionByWindow[local.ID]
		if !exact || projection.SourceHost != sourceHost {
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
		workspaceID, _ := valueAsString(local.WorkspaceID)
		workspace := workspaceByID[workspaceID]
		name, _ := valueAsString(workspace.Name)
		floating := local.IsFloating
		item := PinnedProjection{
			Session:   projection.Session,
			Workspace: model.WorkspaceRef{Name: strings.TrimSpace(name), Index: workspace.Index},
			Order:     localOrder,
			Placement: model.Placement{
				IsFloating: &floating,
				TileSize:   append([]float64(nil), local.Layout.TileSize...),
				WindowSize: append([]int(nil), local.Layout.WindowSize...),
			},
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

var errProjectionAmbiguous = errors.New("projection evidence is ambiguous")

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
	Status   ApplyStatus
	Reason   string
	WindowID int
	target   resume.WorkspaceTarget
}

type ApplyResult struct {
	Items     []ApplyItem
	Untracked int
	Ambiguous int
}

type ApplyConfig struct {
	Snapshot        Snapshot
	SourceHost      string
	SSHCommand      string
	SSHOptions      []string
	LauncherCommand string
	AppID           string
	NiriCommand     string
	StateDir        string
	Timeout         time.Duration
	PollInterval    time.Duration
	DryRun          bool
}

type ApplyDeps struct {
	Runner      Runner
	ListWindows func(context.Context) ([]OwnedWindow, error)
	Workspaces  func(context.Context) ([]OwnedWorkspace, error)
	Inspect     func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error)
	Sleep       func(context.Context, time.Duration) error
}

func ApplyPinned(ctx context.Context, cfg ApplyConfig, deps ApplyDeps) (result ApplyResult, returnErr error) {
	if _, ok := ctx.Deadline(); !ok {
		return ApplyResult{}, fmt.Errorf("mirror apply requires a context deadline")
	}
	if cfg.Snapshot.Host != cfg.SourceHost {
		return ApplyResult{}, fmt.Errorf("fresh source snapshot host does not match requested SSH destination")
	}
	if err := validateText("source profile", cfg.Snapshot.Profile, 128, false); err != nil {
		return ApplyResult{}, err
	}
	if cfg.Timeout <= 0 || cfg.PollInterval <= 0 || cfg.PollInterval > cfg.Timeout {
		return ApplyResult{}, fmt.Errorf("apply timeout and poll interval must be positive, and poll interval must not exceed timeout")
	}
	if deps.Runner == nil {
		return ApplyResult{}, fmt.Errorf("mirror apply runner is unavailable")
	}
	manager := WindowManager{Runner: deps.Runner, NiriCommand: cfg.NiriCommand}
	if deps.ListWindows == nil {
		deps.ListWindows = func(ctx context.Context) ([]OwnedWindow, error) { return manager.List(ctx, cfg.AppID, "") }
	}
	if deps.Workspaces == nil {
		deps.Workspaces = manager.Workspaces
	}
	if deps.Inspect == nil {
		deps.Inspect = InspectProjections
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepContext
	}
	evidence := ProjectionEvidenceConfig{SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions}

	store, err := OpenPinStore(cfg.StateDir)
	if err != nil {
		return ApplyResult{}, err
	}
	// Dry-run is intentionally read-only. Mutating apply holds the same
	// host/profile lock as pin replacement while it reads and applies the pin.
	if !cfg.DryRun {
		lock, err := store.acquire(cfg.SourceHost, cfg.Snapshot.Profile)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("lock mirror apply: %w", err)
		}
		defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	}
	pin, err := store.Read(cfg.SourceHost, cfg.Snapshot.Profile)
	if err != nil {
		return ApplyResult{}, err
	}

	windows, err := deps.ListWindows(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	inventory, err := deps.Inspect(ctx, windows, evidence)
	if err != nil {
		return ApplyResult{}, err
	}
	workspaces, workspaceErr := deps.Workspaces(ctx)
	result = prepareApply(pin, cfg.Snapshot, inventory, workspaces)
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
		beforeWindows, beforeInventory, err := observeApplyWindows(ctx, cfg, deps, evidence)
		if err != nil {
			item.Status, item.Reason = ApplyFailed, "cannot safely recheck exact projection: "+err.Error()
			continue
		}
		matching := exactProjectionIDs(beforeInventory, cfg.SourceHost, item.Session)
		switch len(matching) {
		case 0:
		case 1:
			item.Status, item.Reason = ApplyAlreadyOpen, "exact projection appeared before launch"
			continue
		default:
			item.Status, item.Reason = ApplyAmbiguous, "multiple exact projections appeared before launch"
			continue
		}
		beforeOwned := ownedWindowIDs(beforeWindows)
		token, err := RandomID()
		if err != nil {
			item.Status, item.Reason = ApplyFailed, "create projection correlation token: "+err.Error()
			continue
		}
		window := Window{Order: item.Order, Title: item.Session, ZellijSession: item.Session, Terminal: &Terminal{CWD: item.RemoteCWD, ZellijSession: item.Session}}
		plan, err := PlanLaunch(window, LaunchConfig{SourceHost: cfg.SourceHost, SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions, LauncherCommand: cfg.LauncherCommand, AppID: cfg.AppID, CorrelationToken: token})
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
		windowID, err := waitForNewProjection(ctx, cfg, *item, token, matching, beforeOwned, deps, evidence)
		if err != nil {
			item.Status = ApplyFailed
			if errors.Is(err, errProjectionAmbiguous) {
				item.Status = ApplyAmbiguous
			}
			item.Reason = err.Error()
			continue
		}
		item.WindowID = windowID
		issues := applyPinnedPlacement(ctx, cfg, *item, windowID, deps.Runner)
		item.Status = ApplyOpened
		if len(issues) > 0 {
			item.Reason = strings.Join(issues, "; ")
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
	result := ApplyResult{Untracked: len(inventory.Untracked), Ambiguous: len(inventory.Ambiguous)}
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
			if item.target.ID == "" {
				item.Reason = "destination workspace unavailable; launch will remain on current workspace"
			}
		}
		result.Items = append(result.Items, item)
	}
	return result
}

func containsSession(set map[string]struct{}, session string) bool { _, ok := set[session]; return ok }

func resolveWorkspaceReference(selector model.WorkspaceRef, workspaces []OwnedWorkspace) resume.WorkspaceTarget {
	if selector.Name != "" {
		var match *OwnedWorkspace
		for i := range workspaces {
			name, _ := valueAsString(workspaces[i].Name)
			if name == selector.Name {
				if match != nil {
					match = nil
					break
				}
				match = &workspaces[i]
			}
		}
		if match != nil {
			id, _ := valueAsString(match.ID)
			return resume.WorkspaceTarget{ID: id, Name: selector.Name, Index: match.Index}
		}
	}
	if selector.Index > 0 {
		for _, workspace := range workspaces {
			if workspace.Index == selector.Index {
				id, _ := valueAsString(workspace.ID)
				return resume.WorkspaceTarget{ID: id, Index: selector.Index}
			}
		}
	}
	return resume.WorkspaceTarget{}
}

func observeApplyWindows(ctx context.Context, cfg ApplyConfig, deps ApplyDeps, evidence ProjectionEvidenceConfig) ([]OwnedWindow, ProjectionInventory, error) {
	windows, err := deps.ListWindows(ctx)
	if err != nil {
		return nil, ProjectionInventory{}, err
	}
	inventory, err := deps.Inspect(ctx, windows, evidence)
	return windows, inventory, err
}

func exactProjectionIDs(inventory ProjectionInventory, host, session string) map[int]struct{} {
	ids := make(map[int]struct{})
	for _, projection := range inventory.Exact {
		if projection.SourceHost == host && projection.Session == session {
			ids[projection.Window.ID] = struct{}{}
		}
	}
	return ids
}

func ownedWindowIDs(windows []OwnedWindow) map[int]struct{} {
	ids := make(map[int]struct{}, len(windows))
	for _, window := range windows {
		ids[window.ID] = struct{}{}
	}
	return ids
}

func waitForNewProjection(ctx context.Context, cfg ApplyConfig, item ApplyItem, token string, beforeExact, beforeOwned map[int]struct{}, deps ApplyDeps, evidence ProjectionEvidenceConfig) (int, error) {
	waitCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			return 0, fmt.Errorf("new projection for %q did not appear: %w", item.Session, err)
		}
		_, inventory, err := observeApplyWindows(waitCtx, cfg, deps, evidence)
		if err == nil {
			newMatches := make([]int, 0, 1)
			for _, projection := range inventory.Exact {
				if projection.SourceHost != cfg.SourceHost || projection.Session != item.Session || projection.CorrelationToken != token {
					continue
				}
				_, wasExact := beforeExact[projection.Window.ID]
				_, wasOwned := beforeOwned[projection.Window.ID]
				if !wasExact && !wasOwned {
					newMatches = append(newMatches, projection.Window.ID)
				}
			}
			if len(newMatches) > 1 {
				return 0, fmt.Errorf("%w for %q", errProjectionAmbiguous, item.Session)
			}
			// A newly appeared ambiguous owned window could be this detached
			// launch; never claim a different exact window in that observation.
			for _, ambiguous := range inventory.Ambiguous {
				if _, existed := beforeOwned[ambiguous.ID]; !existed {
					return 0, fmt.Errorf("%w for %q", errProjectionAmbiguous, item.Session)
				}
			}
			if len(newMatches) == 1 {
				return newMatches[0], nil
			}
		}
		if err := deps.Sleep(waitCtx, cfg.PollInterval); err != nil {
			return 0, fmt.Errorf("new projection for %q did not appear: %w", item.Session, err)
		}
	}
}

type applyActionRunner struct {
	runner  Runner
	command string
	timeout time.Duration
}

func (r applyActionRunner) Run(ctx context.Context, action string, args ...string) error {
	actionCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.runner.Run(actionCtx, Command{Name: r.command, Args: append([]string{"msg", "action", action}, args...)})
}

func applyPinnedPlacement(ctx context.Context, cfg ApplyConfig, item ApplyItem, windowID int, runner Runner) []string {
	issues := make([]string, 0, 2)
	actions := resume.NiriActions{Runner: applyActionRunner{runner: runner, command: cfg.NiriCommand, timeout: cfg.Timeout}}
	if item.target.ID != "" {
		if err := actions.MoveToWorkspace(ctx, windowID, item.target); err != nil {
			issues = append(issues, err.Error())
		}
	} else {
		issues = append(issues, "destination workspace unavailable")
	}
	layout := actions.ApplyLayout(ctx, windowID, item.Placement)
	if layout.Reason != "" {
		issues = append(issues, layout.Reason)
	}
	return issues
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
