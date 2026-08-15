// Package resume selects and reconciles a prior-boot terminal checkpoint.
// It contains no launch or compositor mutation code so plans can be tested
// without live Niri or Zellij processes.
package resume

import (
	"sort"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
)

type Status string

const (
	StatusReady       Status = "ready"
	StatusRestored    Status = "restored"
	StatusAlreadyOpen Status = "already_open"
	StatusUnavailable Status = "unavailable"
	StatusDegraded    Status = "degraded"
	StatusStale       Status = "stale"
	StatusFailed      Status = "failed"
	StatusSkipped     Status = "skipped"
)

type CandidateStatus string

const (
	CandidateReady    CandidateStatus = "ready"
	CandidateEmpty    CandidateStatus = "empty"
	CandidateStale    CandidateStatus = "stale"
	CandidateNotFound CandidateStatus = "not_found"
)

type SelectOptions struct {
	CurrentBootID string
	Host          string
	Profile       string
	Now           time.Time
	MaxAge        time.Duration
}

type Selection struct {
	Status     CandidateStatus
	Checkpoint *checkpoints.Checkpoint
	Age        time.Duration
	Reason     string
}

// Select chooses by capture time before inspecting terminal contents. This is
// important: an empty checkpoint from a newer prior boot must not cause a
// fallback to an older boot that happened to contain terminals.
func Select(candidates []checkpoints.Checkpoint, options SelectOptions) Selection {
	currentBootID := strings.TrimSpace(options.CurrentBootID)
	var selected *checkpoints.Checkpoint
	for i := range candidates {
		checkpoint := candidates[i]
		bootID := strings.TrimSpace(checkpoint.BootID)
		if bootID == "" || bootID == currentBootID {
			continue
		}
		if options.Host != "" && checkpoint.Host != options.Host {
			continue
		}
		if options.Profile != "" && checkpoint.Profile != options.Profile {
			continue
		}
		if selected == nil || checkpoint.ObservedAt.After(selected.ObservedAt) {
			copy := checkpoint
			selected = &copy
		}
	}
	if selected == nil {
		return Selection{Status: CandidateNotFound, Reason: "no boot-aware prior-boot checkpoint"}
	}

	selection := Selection{Status: CandidateReady, Checkpoint: selected}
	if !options.Now.IsZero() {
		selection.Age = options.Now.Sub(selected.ObservedAt)
		if selection.Age < 0 {
			selection.Age = 0
		}
	}
	if options.MaxAge > 0 && selection.Age > options.MaxAge {
		selection.Status = CandidateStale
		selection.Reason = "checkpoint exceeds maximum age"
		return selection
	}
	if len(terminalWindows(selected.State)) == 0 {
		selection.Status = CandidateEmpty
		selection.Reason = "checkpoint contains no terminal windows"
	}
	return selection
}

type UnresolvedWorkspacePolicy string

const (
	UnresolvedSkip    UnresolvedWorkspacePolicy = "skip"
	UnresolvedCurrent UnresolvedWorkspacePolicy = "current"
	UnresolvedFail    UnresolvedWorkspacePolicy = "fail"
)

type PlannerConfig struct {
	UnresolvedWorkspace UnresolvedWorkspacePolicy
}

type Planner struct {
	config PlannerConfig
}

func NewPlanner(config PlannerConfig) *Planner {
	switch config.UnresolvedWorkspace {
	case UnresolvedSkip, UnresolvedCurrent, UnresolvedFail:
	default:
		config.UnresolvedWorkspace = UnresolvedCurrent
	}
	return &Planner{config: config}
}

type WorkspaceTarget struct {
	ID     string
	Name   string
	Output string
	Index  int
	Method string
}

type Item struct {
	WindowKey         string
	AppID             string
	Session           string
	CWD               string
	Status            Status
	Reason            string
	CapturedWorkspace model.WorkspaceRef
	CapturedPlacement *model.Placement
	Workspace         *WorkspaceTarget
	LayoutStatus      LayoutStatus
	LayoutReason      string
}

type Plan struct {
	CandidateStatus CandidateStatus
	BootID          string
	CapturedAt      time.Time
	Age             time.Duration
	Reason          string
	Items           []Item
	Summary         Summary
}

type Summary struct {
	Ready       int
	Restored    int
	AlreadyOpen int
	Unavailable int
	Degraded    int
	Stale       int
	Failed      int
	Skipped     int
}

// Build reconciles a selection against an already-enriched current Niri state
// and a single Zellij list-sessions result. Session names are case-sensitive.
func (p *Planner) Build(selection Selection, current model.State, availableSessions []string) Plan {
	plan := Plan{
		CandidateStatus: selection.Status,
		Age:             selection.Age,
		Reason:          selection.Reason,
	}
	if selection.Checkpoint == nil {
		return plan
	}
	plan.BootID = selection.Checkpoint.BootID
	plan.CapturedAt = selection.Checkpoint.ObservedAt

	capturedWindows := terminalWindows(selection.Checkpoint.State)
	if selection.Status == CandidateEmpty {
		return plan
	}

	if selection.Status == CandidateStale {
		for _, window := range capturedWindows {
			item := newItem(window, selection.Checkpoint.State)
			item.Status = StatusStale
			item.Reason = selection.Reason
			plan.Items = append(plan.Items, item)
		}
		plan.summarize()
		return plan
	}

	available := stringSet(availableSessions)
	open := currentSessions(current)
	seenCaptured := make(map[string]struct{})
	for _, window := range capturedWindows {
		item := newItem(window, selection.Checkpoint.State)
		session := item.Session
		if session == "" {
			item.Status = StatusUnavailable
			item.Reason = "captured terminal has no verified Zellij session"
			plan.Items = append(plan.Items, item)
			continue
		}
		if _, ok := seenCaptured[session]; ok {
			item.Status = StatusSkipped
			item.Reason = "duplicate captured Zellij session"
			plan.Items = append(plan.Items, item)
			continue
		}
		seenCaptured[session] = struct{}{}
		if _, ok := open[session]; ok {
			item.Status = StatusAlreadyOpen
			item.Reason = "matching Zellij session is already open in a terminal window"
			plan.Items = append(plan.Items, item)
			continue
		}
		if _, ok := available[session]; !ok {
			item.Status = StatusUnavailable
			item.Reason = "Zellij session is not available"
			plan.Items = append(plan.Items, item)
			continue
		}

		if target, ok := ResolveWorkspace(item.CapturedWorkspace, current.Workspaces); ok {
			item.Status = StatusReady
			item.Workspace = &target
			plan.Items = append(plan.Items, item)
			continue
		}
		switch p.config.UnresolvedWorkspace {
		case UnresolvedCurrent:
			item.Status = StatusDegraded
			item.Reason = "workspace target unresolved; leave window on current workspace"
		case UnresolvedFail:
			item.Status = StatusFailed
			item.Reason = "workspace target unresolved"
		default:
			item.Status = StatusSkipped
			item.Reason = "workspace target unresolved"
		}
		plan.Items = append(plan.Items, item)
	}
	plan.summarize()
	return plan
}

// ResolveWorkspace applies the cross-boot preference from the ADR: workspace
// name, then output plus index, then index alone.
func ResolveWorkspace(captured model.WorkspaceRef, current []model.Workspace) (WorkspaceTarget, bool) {
	workspaces := append([]model.Workspace(nil), current...)
	sort.SliceStable(workspaces, func(i, j int) bool {
		if workspaces[i].Index != workspaces[j].Index {
			return workspaces[i].Index < workspaces[j].Index
		}
		return workspaces[i].ID < workspaces[j].ID
	})
	if name := strings.TrimSpace(captured.Name); name != "" {
		for _, workspace := range workspaces {
			if strings.TrimSpace(workspace.Name) == name {
				return workspaceTarget(workspace, "name"), true
			}
		}
	}
	output := strings.TrimSpace(captured.Output)
	if output != "" && captured.Index > 0 {
		for _, workspace := range workspaces {
			if strings.TrimSpace(workspace.Output) == output && workspace.Index == captured.Index {
				return workspaceTarget(workspace, "output_index"), true
			}
		}
	}
	if captured.Index > 0 {
		for _, workspace := range workspaces {
			if workspace.Index == captured.Index {
				return workspaceTarget(workspace, "index"), true
			}
		}
	}
	return WorkspaceTarget{}, false
}

func workspaceTarget(workspace model.Workspace, method string) WorkspaceTarget {
	return WorkspaceTarget{ID: workspace.ID, Name: workspace.Name, Output: workspace.Output, Index: workspace.Index, Method: method}
}

func newItem(window model.Window, state model.State) Item {
	item := Item{WindowKey: window.Key, AppID: window.AppID}
	if window.Placement != nil {
		placement := *window.Placement
		placement.TileSize = append([]float64(nil), placement.TileSize...)
		placement.WindowSize = append([]int(nil), placement.WindowSize...)
		item.CapturedPlacement = &placement
	}
	if window.Terminal != nil {
		item.Session = strings.TrimSpace(window.Terminal.SessionTag)
		item.CWD = strings.TrimSpace(window.Terminal.CWD)
	}
	item.CapturedWorkspace = capturedWorkspaceRef(window, state.Workspaces)
	return item
}

func capturedWorkspaceRef(window model.Window, workspaces []model.Workspace) model.WorkspaceRef {
	if window.WorkspaceRef != nil {
		ref := *window.WorkspaceRef
		if strings.TrimSpace(ref.Name) != "" || strings.TrimSpace(ref.Output) != "" || ref.Index > 0 {
			return ref
		}
	}
	id := strings.TrimSpace(window.WorkspaceID)
	for _, workspace := range workspaces {
		if strings.TrimSpace(workspace.ID) == id {
			return model.WorkspaceRef{Name: workspace.Name, Output: workspace.Output, Index: workspace.Index}
		}
	}
	return model.WorkspaceRef{}
}

func terminalWindows(state model.State) []model.Window {
	out := make([]model.Window, 0)
	for _, window := range state.Windows {
		if isTerminal(window.AppID) {
			out = append(out, window)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func currentSessions(state model.State) map[string]struct{} {
	out := make(map[string]struct{})
	for _, window := range state.Windows {
		if !isTerminal(window.AppID) || window.Terminal == nil {
			continue
		}
		if session := strings.TrimSpace(window.Terminal.SessionTag); session != "" {
			out[session] = struct{}{}
		}
	}
	return out
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func isTerminal(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "kitty", "alacritty", "foot", "wezterm":
		return true
	default:
		return false
	}
}

func (p *Plan) summarize() {
	for _, item := range p.Items {
		switch item.Status {
		case StatusReady:
			p.Summary.Ready++
		case StatusRestored:
			p.Summary.Restored++
		case StatusAlreadyOpen:
			p.Summary.AlreadyOpen++
		case StatusUnavailable:
			p.Summary.Unavailable++
		case StatusDegraded:
			p.Summary.Degraded++
		case StatusStale:
			p.Summary.Stale++
		case StatusFailed:
			p.Summary.Failed++
		case StatusSkipped:
			p.Summary.Skipped++
		}
	}
}
