package mirror

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/resume"
)

const (
	DefaultFollowInterval   = 5 * time.Second
	MinimumFollowInterval   = 2 * time.Second
	MaximumFollowBackoff    = 30 * time.Second
	DefaultFollowMaxPerPoll = 4
	DefaultFollowMaxTotal   = 64
)

type WorkspaceChoice struct {
	Workspace        Workspace
	VisibleTerminals int
	EligibleSessions int
}

// FollowWorkspaceChoices validates the complete source inventory and returns
// every workspace, including empty ones, in Niri index order.
func FollowWorkspaceChoices(snapshot Snapshot) ([]WorkspaceChoice, error) {
	byID, active, err := validateFollowSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]*WorkspaceChoice, len(snapshot.Workspaces))
	choices := make([]WorkspaceChoice, 0, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		choices = append(choices, WorkspaceChoice{Workspace: workspace})
		counts[workspace.ID] = &choices[len(choices)-1]
	}
	seenSessions := make(map[string]string)
	for _, window := range snapshot.Windows {
		if window.Headless || !strings.EqualFold(strings.TrimSpace(window.AppID), "kitty") || window.Terminal == nil {
			continue
		}
		choice := counts[window.WorkspaceID]
		if choice == nil {
			return nil, fmt.Errorf("visible source window %d references unknown workspace %q", window.SourceWindowID, window.WorkspaceID)
		}
		choice.VisibleTerminals++
		session, err := exactVisibleSession(window, active)
		if err != nil {
			return nil, err
		}
		if session == "" {
			continue
		}
		if prior, exists := seenSessions[session]; exists {
			return nil, fmt.Errorf("source session %q is ambiguous across workspaces %s and %s", session, prior, window.WorkspaceID)
		}
		seenSessions[session] = window.WorkspaceID
		choice.EligibleSessions++
	}
	_ = byID
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].Workspace.Index != choices[j].Workspace.Index {
			return choices[i].Workspace.Index < choices[j].Workspace.Index
		}
		return choices[i].Workspace.ID < choices[j].Workspace.ID
	})
	return choices, nil
}

func validateFollowSnapshot(snapshot Snapshot) (map[string]Workspace, map[string]struct{}, error) {
	if err := validateText("source profile", snapshot.Profile, 128, false); err != nil {
		return nil, nil, err
	}
	if snapshot.Workspaces == nil {
		return nil, nil, fmt.Errorf("source snapshot has no complete workspace inventory (source Redeem upgrade required)")
	}
	if snapshot.ActiveSessions == nil {
		return nil, nil, fmt.Errorf("source snapshot has no complete ACTIVE Zellij inventory (source Redeem upgrade required)")
	}
	byID := make(map[string]Workspace, len(snapshot.Workspaces))
	byIndex := make(map[int]string, len(snapshot.Workspaces))
	byName := make(map[string]string, len(snapshot.Workspaces))
	for i, workspace := range snapshot.Workspaces {
		if workspace.ID == "" || len(workspace.ID) > 256 || strings.IndexAny(workspace.ID, "\r\n\x00") >= 0 || workspace.Index <= 0 {
			return nil, nil, fmt.Errorf("source workspace %d has invalid identity", i)
		}
		if _, exists := byID[workspace.ID]; exists {
			return nil, nil, fmt.Errorf("source workspace ID %q is duplicated", workspace.ID)
		}
		if previous, exists := byIndex[workspace.Index]; exists {
			return nil, nil, fmt.Errorf("source workspace index %d is ambiguous (%s and %s)", workspace.Index, previous, workspace.ID)
		}
		if len(workspace.Name) > 256 || workspace.Name != strings.TrimSpace(workspace.Name) || strings.IndexAny(workspace.Name, "\r\n\x00") >= 0 {
			return nil, nil, fmt.Errorf("source workspace %q has invalid name", workspace.ID)
		}
		if len(workspace.Output) > 256 || workspace.Output != strings.TrimSpace(workspace.Output) || strings.IndexAny(workspace.Output, "\r\n\x00") >= 0 {
			return nil, nil, fmt.Errorf("source workspace %q has invalid output", workspace.ID)
		}
		if workspace.Name != "" {
			if previous, exists := byName[workspace.Name]; exists {
				return nil, nil, fmt.Errorf("source workspace name %q is ambiguous (%s and %s)", workspace.Name, previous, workspace.ID)
			}
			byName[workspace.Name] = workspace.ID
		}
		byID[workspace.ID] = workspace
		byIndex[workspace.Index] = workspace.ID
	}
	active := make(map[string]struct{}, len(snapshot.ActiveSessions))
	for _, session := range snapshot.ActiveSessions {
		if err := ValidateSession(session); err != nil {
			return nil, nil, fmt.Errorf("malformed ACTIVE session inventory: %w", err)
		}
		if _, exists := active[session]; exists {
			return nil, nil, fmt.Errorf("ACTIVE session %q is duplicated", session)
		}
		active[session] = struct{}{}
	}
	orders := make(map[int]struct{}, len(snapshot.Windows))
	windowIDs := make(map[int]struct{}, len(snapshot.Windows))
	for _, window := range snapshot.Windows {
		if window.Order < 0 {
			return nil, nil, fmt.Errorf("source window has invalid order %d", window.Order)
		}
		if _, exists := orders[window.Order]; exists {
			return nil, nil, fmt.Errorf("source window order %d is duplicated", window.Order)
		}
		orders[window.Order] = struct{}{}
		if window.Headless {
			session := SessionName(window)
			if _, exists := active[session]; !exists {
				return nil, nil, fmt.Errorf("headless source session %q is absent from ACTIVE inventory", session)
			}
			continue
		}
		if window.SourceWindowID <= 0 {
			return nil, nil, fmt.Errorf("visible source window has invalid ID")
		}
		if _, exists := windowIDs[window.SourceWindowID]; exists {
			return nil, nil, fmt.Errorf("source window ID %d is duplicated", window.SourceWindowID)
		}
		windowIDs[window.SourceWindowID] = struct{}{}
		workspace, exists := byID[window.WorkspaceID]
		if !exists {
			return nil, nil, fmt.Errorf("source window %d references unknown workspace %q", window.SourceWindowID, window.WorkspaceID)
		}
		if window.WorkspaceIndex != workspace.Index || strings.TrimSpace(window.WorkspaceName) != strings.TrimSpace(workspace.Name) {
			return nil, nil, fmt.Errorf("source window %d has inconsistent workspace metadata", window.SourceWindowID)
		}
	}
	return byID, active, nil
}

func exactVisibleSession(window Window, active map[string]struct{}) (string, error) {
	if window.Terminal == nil {
		return "", nil
	}
	top := strings.TrimSpace(window.ZellijSession)
	terminal := strings.TrimSpace(window.Terminal.ZellijSession)
	if top != "" && terminal != "" && top != terminal {
		return "", fmt.Errorf("source window %d has ambiguous session evidence", window.SourceWindowID)
	}
	session := top
	if session == "" {
		session = terminal
	}
	if session == "" {
		return "", nil
	}
	if err := ValidateSession(session); err != nil {
		return "", err
	}
	if _, ok := active[session]; !ok {
		return "", nil
	}
	return session, nil
}

type SourceWorkspaceSelection struct {
	Name  string
	Index int
}

func SelectionForWorkspace(workspace Workspace) SourceWorkspaceSelection {
	if strings.TrimSpace(workspace.Name) != "" {
		return SourceWorkspaceSelection{Name: strings.TrimSpace(workspace.Name)}
	}
	return SourceWorkspaceSelection{Index: workspace.Index}
}

func sourceWorkspace(snapshot Snapshot, selection SourceWorkspaceSelection) (Workspace, error) {
	var matches []Workspace
	for _, workspace := range snapshot.Workspaces {
		if selection.Name != "" && workspace.Name == selection.Name || selection.Name == "" && workspace.Index == selection.Index {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		label := selection.Name
		if label == "" {
			label = fmt.Sprintf("%d", selection.Index)
		}
		return Workspace{}, fmt.Errorf("selected source workspace %q is absent or ambiguous", label)
	}
	return matches[0], nil
}

type FrozenDestination struct {
	ID            string
	OriginalName  string
	OriginalIndex int
}

func ResolveFollowDestination(source Workspace, workspaces []OwnedWorkspace) (FrozenDestination, error) {
	var matches []OwnedWorkspace
	for _, workspace := range workspaces {
		name, _ := valueAsString(workspace.Name)
		if source.Name != "" && name == source.Name || source.Name == "" && workspace.Index == source.Index {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		return FrozenDestination{}, fmt.Errorf("matching local workspace is absent or ambiguous")
	}
	id, _ := valueAsString(matches[0].ID)
	if id == "" {
		return FrozenDestination{}, fmt.Errorf("matching local workspace has no runtime ID")
	}
	return FrozenDestination{ID: id, OriginalName: source.Name, OriginalIndex: source.Index}, nil
}

func (destination FrozenDestination) CurrentTarget(workspaces []OwnedWorkspace) (resume.WorkspaceTarget, error) {
	var current *OwnedWorkspace
	for i := range workspaces {
		id, _ := valueAsString(workspaces[i].ID)
		if id == destination.ID {
			if current != nil {
				return resume.WorkspaceTarget{}, fmt.Errorf("frozen destination ID %q is ambiguous", destination.ID)
			}
			current = &workspaces[i]
		}
	}
	if current == nil {
		return resume.WorkspaceTarget{}, fmt.Errorf("frozen destination ID %q no longer exists", destination.ID)
	}
	name, _ := valueAsString(current.Name)
	name = strings.TrimSpace(name)
	if name != "" {
		matches := 0
		for _, workspace := range workspaces {
			other, _ := valueAsString(workspace.Name)
			if strings.TrimSpace(other) == name {
				matches++
			}
		}
		if matches == 1 {
			return resume.WorkspaceTarget{ID: destination.ID, Name: name, Index: current.Index}, nil
		}
	}
	if current.Index > 0 {
		matches := 0
		for _, workspace := range workspaces {
			if workspace.Index == current.Index {
				matches++
			}
		}
		if matches == 1 {
			return resume.WorkspaceTarget{ID: destination.ID, Index: current.Index}, nil
		}
	}
	return resume.WorkspaceTarget{}, fmt.Errorf("frozen destination %q has no unique current selector", destination.ID)
}

type FollowConfig struct {
	SourceHost       string
	SSHCommand       string
	SSHOptions       []string
	LauncherCommand  string
	AppID            string
	NiriCommand      string
	Timeout          time.Duration
	EvidenceInterval time.Duration
	MaxPerPoll       int
	MaxTotal         int
	DryRun           bool
}

type FollowState struct {
	TotalOpened int
}

type FollowLock struct{ lock *pinLock }

// AcquireFollowLock serializes one foreground follower with save/apply for the
// same transport/profile. The lock carries no selection or projection state.
func AcquireFollowLock(stateDir, host, profile string) (*FollowLock, error) {
	store, err := OpenPinStore(stateDir)
	if err != nil {
		return nil, err
	}
	lock, err := store.acquire(host, profile)
	if err != nil {
		return nil, err
	}
	return &FollowLock{lock: lock}, nil
}

func (lock *FollowLock) Close() error {
	if lock == nil {
		return nil
	}
	return lock.lock.Close()
}

type FollowItemStatus string

const (
	FollowOpened    FollowItemStatus = "opened"
	FollowExisting  FollowItemStatus = "existing"
	FollowWouldOpen FollowItemStatus = "would_open"
	FollowFailed    FollowItemStatus = "failed"
)

type FollowItem struct {
	Session  string
	Status   FollowItemStatus
	WindowID int
	Reason   string
}

type FollowPollResult struct {
	Healthy  bool
	Eligible int
	Existing int
	Deferred int
	Opened   int
	Total    int
	Items    []FollowItem
	Reason   string
}

type FollowDeps struct {
	Runner      Runner
	ListWindows func(context.Context) ([]OwnedWindow, error)
	Workspaces  func(context.Context) ([]OwnedWorkspace, error)
	Inspect     func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error)
	Sleep       func(context.Context, time.Duration) error
	Token       func() (string, error)
}

func FollowOnce(ctx context.Context, cfg FollowConfig, snapshot Snapshot, selection SourceWorkspaceSelection, destination FrozenDestination, state *FollowState, deps FollowDeps) FollowPollResult {
	result := FollowPollResult{Total: state.TotalOpened}
	if err := validateFollowConfig(cfg); err != nil {
		result.Reason = err.Error()
		return result
	}
	_, active, err := validateFollowSnapshot(snapshot)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	workspace, err := sourceWorkspace(snapshot, selection)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	eligible, err := eligibleWorkspaceWindows(snapshot, workspace, active)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	result.Eligible = len(eligible)

	manager := WindowManager{Runner: deps.Runner, NiriCommand: cfg.NiriCommand}
	if deps.Runner == nil {
		deps.Runner = ExecRunner{}
		manager.Runner = deps.Runner
	}
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
	if deps.Token == nil {
		deps.Token = RandomID
	}
	evidence := ProjectionEvidenceConfig{SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions}
	_, inventory, err := observeFollow(ctx, deps, evidence)
	if err != nil {
		result.Reason = "local projection evidence unavailable: " + err.Error()
		return result
	}
	if len(inventory.Untracked) > 0 || len(inventory.Ambiguous) > 0 {
		result.Reason = fmt.Sprintf("local projection evidence incomplete (untracked=%d ambiguous=%d)", len(inventory.Untracked), len(inventory.Ambiguous))
		return result
	}
	currentWorkspaces, err := deps.Workspaces(ctx)
	if err != nil {
		result.Reason = "local workspace inventory unavailable: " + err.Error()
		return result
	}
	if _, err := destination.CurrentTarget(currentWorkspaces); err != nil {
		result.Reason = "frozen destination unavailable: " + err.Error()
		return result
	}
	open := make(map[string]int)
	for _, projection := range inventory.Exact {
		if projection.SourceHost == cfg.SourceHost {
			open[projection.Session]++
			if open[projection.Session] > 1 {
				result.Reason = fmt.Sprintf("local session %q has duplicate exact projections", projection.Session)
				return result
			}
		}
	}
	missing := make([]Window, 0, len(eligible))
	for _, window := range eligible {
		if open[SessionName(window)] == 1 {
			result.Existing++
		} else {
			missing = append(missing, window)
		}
	}
	remaining := cfg.MaxTotal - state.TotalOpened
	limit := cfg.MaxPerPoll
	if remaining < limit {
		limit = remaining
	}
	if limit < 0 {
		limit = 0
	}
	if len(missing) > limit {
		result.Deferred = len(missing) - limit
		missing = missing[:limit]
	}
	result.Healthy = true
	if cfg.DryRun {
		for _, window := range missing {
			result.Items = append(result.Items, FollowItem{Session: SessionName(window), Status: FollowWouldOpen})
		}
		return result
	}

	for _, window := range missing {
		item := FollowItem{Session: SessionName(window)}
		beforeWindows, beforeInventory, observeErr := observeFollow(ctx, deps, evidence)
		if observeErr != nil || len(beforeInventory.Untracked) > 0 || len(beforeInventory.Ambiguous) > 0 {
			item.Status, item.Reason = FollowFailed, "exact local pre-launch evidence became unavailable"
			result.Items = append(result.Items, item)
			continue
		}
		matching := exactProjectionIDs(beforeInventory, cfg.SourceHost, item.Session)
		if len(matching) == 1 {
			item.Status = FollowExisting
			result.Items = append(result.Items, item)
			continue
		}
		if len(matching) > 1 || hasAmbiguousCandidate(beforeInventory, cfg.SourceHost, item.Session, "") {
			item.Status, item.Reason = FollowFailed, "exact local pre-launch evidence is ambiguous"
			result.Items = append(result.Items, item)
			continue
		}
		token, tokenErr := deps.Token()
		if tokenErr != nil {
			item.Status, item.Reason = FollowFailed, tokenErr.Error()
			result.Items = append(result.Items, item)
			continue
		}
		plan, planErr := PlanLaunch(window, LaunchConfig{SourceHost: cfg.SourceHost, SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions, LauncherCommand: cfg.LauncherCommand, AppID: cfg.AppID, CorrelationToken: token})
		if planErr != nil {
			item.Status, item.Reason = FollowFailed, planErr.Error()
			result.Items = append(result.Items, item)
			continue
		}
		launchCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		launchErr := deps.Runner.Run(launchCtx, plan.Command)
		cancel()
		if launchErr != nil {
			item.Status, item.Reason = FollowFailed, launchErr.Error()
			result.Items = append(result.Items, item)
			continue
		}
		applyCfg := ApplyConfig{SourceHost: cfg.SourceHost, SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions, AppID: cfg.AppID, NiriCommand: cfg.NiriCommand, Timeout: cfg.Timeout, PollInterval: cfg.EvidenceInterval}
		windowID, correlationErr := waitForNewProjection(ctx, applyCfg, ApplyItem{PinnedProjection: PinnedProjection{Session: item.Session}}, token, matching, ownedWindowIDs(beforeWindows), ApplyDeps{ListWindows: deps.ListWindows, Inspect: deps.Inspect, Sleep: deps.Sleep}, evidence)
		if correlationErr != nil {
			item.Status, item.Reason = FollowFailed, correlationErr.Error()
			result.Items = append(result.Items, item)
			continue
		}
		item.WindowID = windowID
		current, workspaceErr := deps.Workspaces(ctx)
		if workspaceErr != nil {
			item.Status, item.Reason = FollowOpened, "opened but frozen destination inventory is unavailable: "+workspaceErr.Error()
		} else if target, targetErr := destination.CurrentTarget(current); targetErr != nil {
			item.Status, item.Reason = FollowOpened, "opened but not moved: "+targetErr.Error()
		} else {
			actions := resume.NiriActions{Runner: applyActionRunner{runner: deps.Runner, command: cfg.NiriCommand, timeout: cfg.Timeout}}
			if moveErr := actions.MoveToWorkspace(ctx, windowID, target); moveErr != nil {
				item.Status, item.Reason = FollowOpened, "opened but move failed: "+moveErr.Error()
			} else {
				item.Status = FollowOpened
			}
		}
		state.TotalOpened++
		result.Opened++
		result.Total = state.TotalOpened
		result.Items = append(result.Items, item)
	}
	return result
}

func validateFollowConfig(cfg FollowConfig) error {
	if err := ValidateDestination(cfg.SourceHost); err != nil {
		return err
	}
	if err := ValidateSSHOptions(cfg.SSHOptions); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.SSHCommand) == "" || strings.TrimSpace(cfg.LauncherCommand) == "" || strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.NiriCommand) == "" {
		return fmt.Errorf("follow commands and app ID must not be empty")
	}
	if cfg.Timeout <= 0 || cfg.EvidenceInterval <= 0 || cfg.EvidenceInterval > cfg.Timeout || cfg.MaxPerPoll <= 0 || cfg.MaxTotal <= 0 {
		return fmt.Errorf("follow timeout, evidence interval, and finite launch bounds must be positive")
	}
	return nil
}

func eligibleWorkspaceWindows(snapshot Snapshot, workspace Workspace, active map[string]struct{}) ([]Window, error) {
	eligible := make([]Window, 0)
	seen := make(map[string]struct{})
	for _, window := range snapshot.Windows {
		if window.Headless || window.WorkspaceID != workspace.ID || !strings.EqualFold(strings.TrimSpace(window.AppID), "kitty") || window.Terminal == nil {
			continue
		}
		session, err := exactVisibleSession(window, active)
		if err != nil {
			return nil, err
		}
		if session == "" {
			continue
		}
		if _, exists := seen[session]; exists {
			return nil, fmt.Errorf("source session %q is ambiguous in selected workspace", session)
		}
		seen[session] = struct{}{}
		eligible = append(eligible, window)
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].Order < eligible[j].Order })
	return eligible, nil
}

func observeFollow(ctx context.Context, deps FollowDeps, evidence ProjectionEvidenceConfig) ([]OwnedWindow, ProjectionInventory, error) {
	windows, err := deps.ListWindows(ctx)
	if err != nil {
		return nil, ProjectionInventory{}, err
	}
	inventory, err := deps.Inspect(ctx, windows, evidence)
	return windows, inventory, err
}

func FollowRetryDelay(interval time.Duration, consecutiveFailures int) time.Duration {
	if interval < MinimumFollowInterval {
		interval = MinimumFollowInterval
	}
	if consecutiveFailures <= 0 {
		return interval
	}
	delay := interval
	for i := 1; i < consecutiveFailures && delay < MaximumFollowBackoff; i++ {
		delay *= 2
	}
	if delay > MaximumFollowBackoff {
		return MaximumFollowBackoff
	}
	return delay
}
