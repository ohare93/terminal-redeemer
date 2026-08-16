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

type validatedFollowSource struct {
	byID     map[string]Workspace
	active   map[string]struct{}
	visible  map[string]int
	eligible map[string][]Window
}

// FollowWorkspaceChoices validates the complete source inventory and returns
// every workspace, including empty ones, in Niri index order.
func FollowWorkspaceChoices(snapshot Snapshot) ([]WorkspaceChoice, error) {
	source, err := validateFollowSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	choices := make([]WorkspaceChoice, 0, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		choices = append(choices, WorkspaceChoice{
			Workspace:        workspace,
			VisibleTerminals: source.visible[workspace.ID],
			EligibleSessions: len(source.eligible[workspace.ID]),
		})
	}
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].Workspace.Index != choices[j].Workspace.Index {
			return choices[i].Workspace.Index < choices[j].Workspace.Index
		}
		return choices[i].Workspace.ID < choices[j].Workspace.ID
	})
	return choices, nil
}

func validateFollowSnapshot(snapshot Snapshot) (validatedFollowSource, error) {
	if err := validateText("source profile", snapshot.Profile, 128, false); err != nil {
		return validatedFollowSource{}, err
	}
	if snapshot.Workspaces == nil {
		return validatedFollowSource{}, fmt.Errorf("source snapshot has no complete workspace inventory (source Redeem upgrade required)")
	}
	if snapshot.ActiveSessions == nil {
		return validatedFollowSource{}, fmt.Errorf("source snapshot has no complete ACTIVE Zellij inventory (source Redeem upgrade required)")
	}
	source := validatedFollowSource{
		byID:     make(map[string]Workspace, len(snapshot.Workspaces)),
		active:   make(map[string]struct{}, len(snapshot.ActiveSessions)),
		visible:  make(map[string]int, len(snapshot.Workspaces)),
		eligible: make(map[string][]Window, len(snapshot.Workspaces)),
	}
	byIndex := make(map[int]string, len(snapshot.Workspaces))
	byName := make(map[string]string, len(snapshot.Workspaces))
	for i, workspace := range snapshot.Workspaces {
		if workspace.ID == "" || len(workspace.ID) > 256 || strings.IndexAny(workspace.ID, "\r\n\x00") >= 0 || workspace.Index <= 0 {
			return validatedFollowSource{}, fmt.Errorf("source workspace %d has invalid identity", i)
		}
		if _, exists := source.byID[workspace.ID]; exists {
			return validatedFollowSource{}, fmt.Errorf("source workspace ID %q is duplicated", workspace.ID)
		}
		if previous, exists := byIndex[workspace.Index]; exists {
			return validatedFollowSource{}, fmt.Errorf("source workspace index %d is ambiguous (%s and %s)", workspace.Index, previous, workspace.ID)
		}
		if len(workspace.Name) > 256 || workspace.Name != strings.TrimSpace(workspace.Name) || strings.IndexAny(workspace.Name, "\r\n\x00") >= 0 {
			return validatedFollowSource{}, fmt.Errorf("source workspace %q has invalid name", workspace.ID)
		}
		if len(workspace.Output) > 256 || workspace.Output != strings.TrimSpace(workspace.Output) || strings.IndexAny(workspace.Output, "\r\n\x00") >= 0 {
			return validatedFollowSource{}, fmt.Errorf("source workspace %q has invalid output", workspace.ID)
		}
		if workspace.Name != "" {
			if previous, exists := byName[workspace.Name]; exists {
				return validatedFollowSource{}, fmt.Errorf("source workspace name %q is ambiguous (%s and %s)", workspace.Name, previous, workspace.ID)
			}
			byName[workspace.Name] = workspace.ID
		}
		source.byID[workspace.ID] = workspace
		byIndex[workspace.Index] = workspace.ID
	}
	for _, session := range snapshot.ActiveSessions {
		if err := ValidateSession(session); err != nil {
			return validatedFollowSource{}, fmt.Errorf("malformed ACTIVE session inventory: %w", err)
		}
		if _, exists := source.active[session]; exists {
			return validatedFollowSource{}, fmt.Errorf("ACTIVE session %q is duplicated", session)
		}
		source.active[session] = struct{}{}
	}

	orders := make(map[int]struct{}, len(snapshot.Windows))
	windowIDs := make(map[int]struct{}, len(snapshot.Windows))
	seenSessions := make(map[string]string)
	for _, window := range snapshot.Windows {
		if window.Order < 0 {
			return validatedFollowSource{}, fmt.Errorf("source window has invalid order %d", window.Order)
		}
		if _, exists := orders[window.Order]; exists {
			return validatedFollowSource{}, fmt.Errorf("source window order %d is duplicated", window.Order)
		}
		orders[window.Order] = struct{}{}
		if window.Headless {
			session := SessionName(window)
			if _, exists := source.active[session]; !exists {
				return validatedFollowSource{}, fmt.Errorf("headless source session %q is absent from ACTIVE inventory", session)
			}
			continue
		}
		if window.SourceWindowID <= 0 {
			return validatedFollowSource{}, fmt.Errorf("visible source window has invalid ID")
		}
		if _, exists := windowIDs[window.SourceWindowID]; exists {
			return validatedFollowSource{}, fmt.Errorf("source window ID %d is duplicated", window.SourceWindowID)
		}
		windowIDs[window.SourceWindowID] = struct{}{}
		workspace, exists := source.byID[window.WorkspaceID]
		if !exists {
			return validatedFollowSource{}, fmt.Errorf("source window %d references unknown workspace %q", window.SourceWindowID, window.WorkspaceID)
		}
		if window.WorkspaceIndex != workspace.Index || window.WorkspaceName != workspace.Name || window.Output != workspace.Output {
			return validatedFollowSource{}, fmt.Errorf("source window %d has inconsistent workspace metadata", window.SourceWindowID)
		}
		if !strings.EqualFold(window.AppID, "kitty") || window.Terminal == nil {
			continue
		}
		source.visible[workspace.ID]++
		session, err := exactVisibleSession(window, source.active)
		if err != nil {
			return validatedFollowSource{}, err
		}
		if session == "" {
			continue
		}
		if prior, exists := seenSessions[session]; exists {
			return validatedFollowSource{}, fmt.Errorf("source session %q is ambiguous across workspaces %s and %s", session, prior, workspace.ID)
		}
		seenSessions[session] = workspace.ID
		source.eligible[workspace.ID] = append(source.eligible[workspace.ID], window)
	}
	for id := range source.eligible {
		sort.SliceStable(source.eligible[id], func(i, j int) bool {
			return source.eligible[id][i].Order < source.eligible[id][j].Order
		})
	}
	return source, nil
}

func exactVisibleSession(window Window, active map[string]struct{}) (string, error) {
	if window.Terminal == nil {
		return "", nil
	}
	top := window.ZellijSession
	terminal := window.Terminal.ZellijSession
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

type SourceWorkspaceSelection struct{ ID string }

func SelectionForWorkspace(workspace Workspace) SourceWorkspaceSelection {
	return SourceWorkspaceSelection{ID: workspace.ID}
}

func sourceWorkspace(source validatedFollowSource, selection SourceWorkspaceSelection) (Workspace, error) {
	if selection.ID == "" {
		return Workspace{}, fmt.Errorf("selected source workspace has no frozen runtime ID")
	}
	workspace, ok := source.byID[selection.ID]
	if !ok {
		return Workspace{}, fmt.Errorf("selected source workspace ID %q no longer exists", selection.ID)
	}
	return workspace, nil
}

type FrozenDestination struct{ ID string }

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
	return FrozenDestination{ID: id}, nil
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
	if name != "" {
		matches := 0
		for _, workspace := range workspaces {
			other, _ := valueAsString(workspace.Name)
			if other == name {
				matches++
			}
		}
		if matches != 1 {
			return resume.WorkspaceTarget{}, fmt.Errorf("frozen destination %q has an ambiguous current name", destination.ID)
		}
		return resume.WorkspaceTarget{ID: destination.ID, Name: name, Index: current.Index}, nil
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
}

type FollowState struct {
	TotalAttempts  int
	TotalConfirmed int
	TotalUncertain int
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
	FollowOpened   FollowItemStatus = "opened"
	FollowDeferred FollowItemStatus = "deferred"
	FollowFailed   FollowItemStatus = "failed"
)

type FollowItem struct {
	Session  string
	Status   FollowItemStatus
	WindowID int
	Reason   string
}

type FollowPollResult struct {
	Healthy        bool
	Eligible       int
	Existing       int
	Deferred       int
	Attempted      int
	Confirmed      int
	Uncertain      int
	TotalAttempts  int
	TotalConfirmed int
	TotalUncertain int
	Items          []FollowItem
	Reason         string
}

type FollowDeps struct {
	Runner      Runner
	ListWindows func(context.Context) ([]OwnedWindow, error)
	Workspaces  func(context.Context) ([]OwnedWorkspace, error)
	Inspect     func(context.Context, []OwnedWindow, ProjectionEvidenceConfig) (ProjectionInventory, error)
	Sleep       func(context.Context, time.Duration) error
	Token       func() (string, error)
}

type plannedFollowLaunch struct {
	window Window
	token  string
	plan   LaunchPlan
}

func FollowOnce(ctx context.Context, cfg FollowConfig, snapshot Snapshot, selection SourceWorkspaceSelection, destination FrozenDestination, state *FollowState, deps FollowDeps) FollowPollResult {
	result := followResultTotals(state)
	if err := validateFollowConfig(cfg); err != nil {
		result.Reason = err.Error()
		return result
	}
	source, err := validateFollowSnapshot(snapshot)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	workspace, err := sourceWorkspace(source, selection)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	eligible := source.eligible[workspace.ID]
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

	// This is the single full-poll safety gate. No launch effect is attempted
	// until source, local evidence, destination, every token, and every launch
	// plan have all validated successfully.
	evidence := ProjectionEvidenceConfig{SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions}
	beforeWindows, inventory, err := observeFollow(ctx, deps, evidence)
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
	remaining := cfg.MaxTotal - state.TotalAttempts
	limit := cfg.MaxPerPoll
	if remaining < limit {
		limit = remaining
	}
	if limit < 0 {
		limit = 0
	}
	if len(missing) > limit {
		for _, window := range missing[limit:] {
			result.Items = append(result.Items, FollowItem{Session: SessionName(window), Status: FollowDeferred})
		}
		result.Deferred = len(missing) - limit
		missing = missing[:limit]
	}

	plans := make([]plannedFollowLaunch, 0, len(missing))
	for _, window := range missing {
		token, err := deps.Token()
		if err != nil {
			result.Reason = "prepare launch token: " + err.Error()
			return result
		}
		plan, err := PlanLaunch(window, LaunchConfig{
			SourceHost: cfg.SourceHost, SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions,
			LauncherCommand: cfg.LauncherCommand, AppID: cfg.AppID, CorrelationToken: token,
		})
		if err != nil {
			result.Reason = "prepare launch: " + err.Error()
			return result
		}
		plans = append(plans, plannedFollowLaunch{window: window, token: token, plan: plan})
	}

	result.Healthy = true
	if len(plans) == 0 && len(missing) == 0 && result.Deferred > 0 {
		result.Reason = "lifetime launch-attempt limit reached"
	}
	beforeIDs := ownedWindowIDs(beforeWindows)
	for i, planned := range plans {
		item := FollowItem{Session: SessionName(planned.window)}
		if err := ctx.Err(); err != nil {
			failFollowBatch(&result, &item, err.Error(), len(plans)-i-1)
			break
		}
		state.TotalAttempts++ // charge before invoking the detached launcher
		result.Attempted++
		setFollowResultTotals(&result, state)

		launchCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		launchErr := deps.Runner.Run(launchCtx, planned.plan.Command)
		cancel()
		if launchErr != nil {
			state.TotalUncertain++
			result.Uncertain++
			setFollowResultTotals(&result, state)
			failFollowBatch(&result, &item, launchErr.Error(), len(plans)-i-1)
			break
		}
		applyCfg := ApplyConfig{
			SourceHost: cfg.SourceHost, SSHCommand: cfg.SSHCommand, SSHOptions: cfg.SSHOptions,
			AppID: cfg.AppID, NiriCommand: cfg.NiriCommand, Timeout: cfg.Timeout, PollInterval: cfg.EvidenceInterval,
		}
		windowID, correlationErr := waitForNewProjection(ctx, applyCfg,
			ApplyItem{PinnedProjection: PinnedProjection{Session: item.Session}}, planned.token, nil, beforeIDs,
			ApplyDeps{ListWindows: deps.ListWindows, Inspect: deps.Inspect, Sleep: deps.Sleep}, evidence)
		if correlationErr != nil {
			state.TotalUncertain++
			result.Uncertain++
			setFollowResultTotals(&result, state)
			failFollowBatch(&result, &item, correlationErr.Error(), len(plans)-i-1)
			break
		}

		item.WindowID = windowID
		item.Status = FollowOpened
		state.TotalConfirmed++
		result.Confirmed++
		setFollowResultTotals(&result, state)

		// Re-resolve only the selector for the already-frozen runtime ID. A
		// renamed or renumbered replacement can never receive this action.
		current, workspaceErr := deps.Workspaces(ctx)
		if workspaceErr != nil {
			failOpenedFollowBatch(&result, &item, "opened but frozen destination inventory is unavailable: "+workspaceErr.Error(), len(plans)-i-1)
			break
		}
		target, targetErr := destination.CurrentTarget(current)
		if targetErr != nil {
			failOpenedFollowBatch(&result, &item, "opened but not moved: "+targetErr.Error(), len(plans)-i-1)
			break
		}
		actions := resume.NiriActions{Runner: applyActionRunner{runner: deps.Runner, command: cfg.NiriCommand, timeout: cfg.Timeout}}
		if moveErr := actions.MoveToWorkspace(ctx, windowID, target); moveErr != nil {
			failOpenedFollowBatch(&result, &item, "opened but move failed: "+moveErr.Error(), len(plans)-i-1)
			break
		}
		result.Items = append(result.Items, item)
	}
	return result
}

func followResultTotals(state *FollowState) FollowPollResult {
	result := FollowPollResult{}
	setFollowResultTotals(&result, state)
	return result
}

func setFollowResultTotals(result *FollowPollResult, state *FollowState) {
	result.TotalAttempts = state.TotalAttempts
	result.TotalConfirmed = state.TotalConfirmed
	result.TotalUncertain = state.TotalUncertain
}

func failFollowBatch(result *FollowPollResult, item *FollowItem, reason string, deferred int) {
	result.Healthy = false
	item.Status = FollowFailed
	item.Reason = reason
	result.Reason = reason
	result.Deferred += deferred
	result.Items = append(result.Items, *item)
}

func failOpenedFollowBatch(result *FollowPollResult, item *FollowItem, reason string, deferred int) {
	result.Healthy = false
	item.Reason = reason
	result.Reason = reason
	result.Deferred += deferred
	result.Items = append(result.Items, *item)
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
