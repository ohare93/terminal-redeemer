package resume

import (
	"sort"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

const (
	CandidateSourceCurrentActive = "current_boot_active_catalog"
	CandidateSourcePriorActive   = "prior_boot_active_allow_list"
	PlacementSourceCurrentSticky = "current_boot_sticky"
	PlacementSourcePriorRecovery = "prior_boot_recovery"
	PlacementSourceNone          = "none"
)

// SelectAll chooses the recovery point which supplies placement and, after a
// boot change, the authoritative prior-active allow-list. A schema-3 checkpoint
// from the current boot switches selection to current catalog ACTIVE sessions.
func SelectAll(candidates []checkpoints.Checkpoint, options SelectOptions) Selection {
	currentBootID := strings.TrimSpace(options.CurrentBootID)
	var current, prior *checkpoints.Checkpoint
	for i := range candidates {
		checkpoint := candidates[i]
		if checkpoint.V < checkpoints.SchemaVersion || strings.TrimSpace(checkpoint.BootID) == "" {
			continue
		}
		if options.Host != "" && checkpoint.Host != options.Host {
			continue
		}
		if options.Profile != "" && checkpoint.Profile != options.Profile {
			continue
		}
		copy := checkpoint
		if checkpoint.BootID == currentBootID {
			if current == nil || checkpoint.ObservedAt.After(current.ObservedAt) {
				current = &copy
			}
		} else if prior == nil || checkpoint.ObservedAt.After(prior.ObservedAt) {
			prior = &copy
		}
	}
	if current != nil {
		return Selection{
			Status: CandidateReady, CandidateSource: CandidateSourceCurrentActive,
			Checkpoint: current, Age: checkpointAge(current.ObservedAt, options.Now),
			Reason: "current-boot ACTIVE Zellij catalog with sticky checkpoint placement",
		}
	}
	if prior == nil {
		return Selection{Status: CandidateNotFound, CandidateSource: CandidateSourcePriorActive, Reason: "no eligible schema-3 prior-boot active recovery point"}
	}
	selection := Selection{
		Status: CandidateReady, CandidateSource: CandidateSourcePriorActive,
		Checkpoint: prior, Age: checkpointAge(prior.ObservedAt, options.Now),
		Reason: "newest eligible prior-boot active recovery allow-list",
	}
	if len(prior.Recovery.ActiveSessions) == 0 {
		selection.Status = CandidateEmpty
		selection.Reason = "prior recovery point contains no active sessions"
	} else if options.MaxAge > 0 && selection.Age > options.MaxAge {
		selection.Status = CandidateStale
		selection.Reason = "prior recovery point exceeds maximum age; dead-session resurrection is blocked"
	}
	return selection
}

func checkpointAge(observedAt, now time.Time) time.Duration {
	if now.IsZero() {
		return 0
	}
	age := now.Sub(observedAt)
	if age < 0 {
		return 0
	}
	return age
}

type AllOptions struct {
	Now    time.Time
	MaxAge time.Duration
}

// BuildAll changes only candidate selection and explanation. The resulting
// items use the same executor and attach-only launch contract as plain resume.
func (p *Planner) BuildAll(selection Selection, current model.State, catalog zellijlive.Catalog, options AllOptions) Plan {
	plan := Plan{
		CandidateStatus: selection.Status, CandidateSource: selection.CandidateSource,
		Age: selection.Age, Reason: selection.Reason,
	}
	if selection.Checkpoint == nil {
		return plan
	}
	checkpoint := selection.Checkpoint
	plan.BootID = checkpoint.BootID
	plan.CapturedAt = checkpoint.ObservedAt
	if selection.Status == CandidateEmpty {
		return plan
	}

	metadata := make(map[string]model.RecoverySession, len(checkpoint.Recovery.Sessions))
	for _, session := range checkpoint.Recovery.Sessions {
		metadata[session.Name] = session
	}

	var candidates []string
	if selection.CandidateSource == CandidateSourceCurrentActive {
		for _, name := range catalog.Names {
			if catalog.Exact(name).Status == zellijlive.StatusActive {
				candidates = append(candidates, name)
			}
		}
	} else {
		candidates = append(candidates, checkpoint.Recovery.ActiveSessions...)
	}
	sort.Strings(candidates)
	open := currentExactSessionWindows(current)
	for _, name := range candidates {
		recovery := metadata[name]
		item := Item{
			WindowKey: "recovery:" + name, AppID: "kitty", Session: name, CWD: strings.TrimSpace(recovery.CWD),
			CandidateSource: selection.CandidateSource, ZellijStatus: string(catalog.Exact(name).Status),
		}
		if selection.CandidateSource == CandidateSourceCurrentActive {
			item.PlacementSource = PlacementSourceCurrentSticky
		} else {
			item.PlacementSource = PlacementSourcePriorRecovery
		}
		copyRecoveryPlacement(&item, recovery, options)

		live := catalog.Exact(name)
		switch live.Status {
		case zellijlive.StatusActive:
			// Current active attachment is never blocked by checkpoint age.
		case zellijlive.StatusDeadResurrectable:
			if selection.CandidateSource != CandidateSourcePriorActive {
				continue
			}
			if selection.Status == CandidateStale {
				item.Status = StatusStale
				item.Reason = "dead-resurrectable prior-active session blocked because recovery point exceeds maximum age"
				plan.Items = append(plan.Items, item)
				continue
			}
		case zellijlive.StatusDuplicate, zellijlive.StatusSocketInvalid,
			zellijlive.StatusMissing, zellijlive.StatusPrefixOnly:
			item.Status = StatusUnavailable
			item.Reason = "prior-active session excluded by exact Zellij safety status " + string(live.Status)
			plan.Items = append(plan.Items, item)
			continue
		default:
			item.Status = StatusUnavailable
			item.Reason = "prior-active session excluded by unknown Zellij safety status"
			plan.Items = append(plan.Items, item)
			continue
		}

		if window, ok := open[name]; ok {
			item.Status = StatusAlreadyOpen
			item.Reason = "matching Zellij session is already open in an exactly observed Niri window"
			item.CurrentWindowKey = window.Key
			item.CurrentWindowID, _ = runtimeWindowID(window.Key)
			p.resolveAllPlacement(&item, current, true)
			plan.Items = append(plan.Items, item)
			continue
		}
		p.resolveAllPlacement(&item, current, false)
		plan.Items = append(plan.Items, item)
	}
	plan.summarize()
	return plan
}

func currentExactSessionWindows(state model.State) map[string]model.Window {
	out := make(map[string]model.Window)
	windows := append([]model.Window(nil), state.Windows...)
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].Key < windows[j].Key })
	for _, window := range windows {
		if !isTerminal(window.AppID) || window.Terminal == nil || !window.Terminal.SessionTagExact {
			continue
		}
		if session := strings.TrimSpace(window.Terminal.SessionTag); session != "" {
			if _, exists := out[session]; !exists {
				out[session] = window
			}
		}
	}
	return out
}

func copyRecoveryPlacement(item *Item, recovery model.RecoverySession, options AllOptions) {
	if recovery.WorkspaceRef != nil {
		item.CapturedWorkspace = *recovery.WorkspaceRef
	}
	if recovery.Placement != nil {
		placement := *recovery.Placement
		placement.TileSize = append([]float64(nil), placement.TileSize...)
		placement.WindowSize = append([]int(nil), placement.WindowSize...)
		item.CapturedPlacement = &placement
	}
	if recovery.PlacementObservedAt == nil {
		item.PlacementSource = PlacementSourceNone
		item.PlacementWarning = "no sticky placement observation is available"
		return
	}
	observed := recovery.PlacementObservedAt.UTC()
	item.PlacementObservedAt = &observed
	item.PlacementAge = checkpointAge(observed, options.Now)
	if options.MaxAge > 0 && item.PlacementAge > options.MaxAge {
		item.PlacementWarning = "sticky placement exceeds maximum checkpoint age; active attachment remains allowed"
	}
}

func (p *Planner) resolveAllPlacement(item *Item, current model.State, alreadyOpen bool) {
	if item.CapturedPlacement != nil {
		if target, ok := ResolveWorkspace(item.CapturedWorkspace, current.Workspaces); ok {
			item.Workspace = &target
			if !alreadyOpen {
				item.Status = StatusReady
			}
			return
		}
	}
	if alreadyOpen {
		if item.PlacementWarning == "" {
			item.PlacementWarning = "sticky placement cannot be resolved; current window is retained for later reconciliation"
		}
		return
	}
	switch p.config.UnresolvedWorkspace {
	case UnresolvedSkip:
		item.Status = StatusSkipped
		item.Reason = "sticky workspace or placement unavailable; fallback policy is skip"
	case UnresolvedFail:
		item.Status = StatusFailed
		item.Reason = "sticky workspace or placement unavailable"
	default:
		item.Status = StatusDegraded
		item.Reason = "sticky workspace or placement unavailable; launch on current workspace"
	}
}
