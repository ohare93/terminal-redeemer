package capture

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/bootid"
	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/storelock"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type Collector interface {
	Collect(ctx context.Context) (model.State, error)
}

type CheckpointStore interface {
	Write(checkpoint checkpoints.Checkpoint) (string, error)
	History(host, profile string) ([]checkpoints.Checkpoint, error)
}

type Config struct {
	Collector       Collector
	CheckpointStore CheckpointStore
	Cataloger       zellijlive.Cataloger
	CWDResolver     procmeta.SessionCWDResolver
	StateDir        string
	BootIDSource    bootid.Source
	Host            string
	Profile         string
	Now             func() time.Time
}

type Runner struct {
	collector       Collector
	checkpointStore CheckpointStore
	cataloger       zellijlive.Cataloger
	cwdResolver     procmeta.SessionCWDResolver
	stateDir        string
	bootIDSource    bootid.Source
	host            string
	profile         string
	now             func() time.Time
}

type Result struct {
	CheckpointPath string
	StateHash      string
}

func NewRunner(config Config) *Runner {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	bootIDSource := config.BootIDSource
	if bootIDSource == nil {
		bootIDSource = bootid.Current
	}
	cataloger := config.Cataloger
	if cataloger == nil {
		cataloger = zellijlive.CommandCataloger{}
	}
	resolver := config.CWDResolver
	if resolver == nil {
		resolver = procmeta.NewZellijSessionCWDResolver("")
	}
	return &Runner{
		collector: config.Collector, checkpointStore: config.CheckpointStore,
		cataloger: cataloger, cwdResolver: resolver,
		stateDir: strings.TrimSpace(config.StateDir), bootIDSource: bootIDSource,
		host: strings.TrimSpace(config.Host), profile: strings.TrimSpace(config.Profile), now: now,
	}
}

// CaptureOnce performs one complete query and atomically replaces this boot's
// rolling checkpoint while holding the repository's single-writer lock.
func (r *Runner) CaptureOnce(ctx context.Context) (Result, error) {
	if r.checkpointStore == nil {
		return Result{}, fmt.Errorf("rolling checkpoint store is unavailable")
	}
	if r.cataloger == nil {
		return Result{}, fmt.Errorf("active Zellij catalog is unavailable")
	}
	if r.stateDir == "" {
		return Result{}, fmt.Errorf("state directory is required")
	}

	// Collection and catalog validation are part of the serialized transaction,
	// so a failed or ambiguous observation cannot supersede a usable checkpoint.
	lock, err := storelock.Acquire(r.stateDir)
	if err != nil {
		return Result{}, fmt.Errorf("acquire checkpoint writer lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	state, err := r.collector.Collect(ctx)
	if err != nil {
		return Result{}, err
	}
	state = model.Normalize(state)

	catalog, err := r.cataloger.Observe(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("observe active Zellij catalog: %w", err)
	}
	active, err := authoritativeActiveSessions(catalog)
	if err != nil {
		return Result{}, fmt.Errorf("observe active Zellij catalog: %w", err)
	}

	bootID, err := r.bootIDSource()
	if err != nil {
		return Result{}, fmt.Errorf("read boot ID: %w", err)
	}
	bootID = strings.TrimSpace(bootID)
	if bootID == "" {
		return Result{}, fmt.Errorf("boot ID is empty")
	}

	history, err := r.checkpointStore.History(r.host, r.profile)
	if err != nil {
		return Result{}, fmt.Errorf("read checkpoint history: %w", err)
	}
	observedAt := r.now().UTC()
	recovery := buildRecoveryInventory(state, active, history, observedAt, r.cwdResolver)
	stateHash, err := state.Hash()
	if err != nil {
		return Result{}, err
	}
	integrityHash, err := checkpoints.RecoveryIntegrityHash(state, recovery)
	if err != nil {
		return Result{}, err
	}

	path, err := r.checkpointStore.Write(checkpoints.Checkpoint{
		V: checkpoints.SchemaVersion, BootID: bootID, Host: r.host, Profile: r.profile,
		ObservedAt: observedAt, State: state, StateHash: stateHash,
		Recovery: recovery, IntegrityHash: integrityHash,
	})
	if err != nil {
		return Result{}, fmt.Errorf("publish rolling checkpoint: %w", err)
	}
	return Result{CheckpointPath: path, StateHash: stateHash}, nil
}

func authoritativeActiveSessions(catalog zellijlive.Catalog) ([]string, error) {
	seenNames := make(map[string]bool, len(catalog.Names))
	for _, name := range catalog.Names {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || seenNames[name] {
			return nil, fmt.Errorf("ambiguous catalog name %q", name)
		}
		seenNames[name] = true
		if _, ok := catalog.Sessions[name]; !ok {
			return nil, fmt.Errorf("catalog name %q has no session record", name)
		}
	}
	active := make([]string, 0, len(catalog.Sessions))
	for name, session := range catalog.Sessions {
		if !seenNames[name] || session.Name != name {
			return nil, fmt.Errorf("ambiguous catalog record %q", name)
		}
		switch session.Status {
		case zellijlive.StatusActive:
			active = append(active, name)
		case zellijlive.StatusDeadResurrectable:
			// Resurrection cache entries are explicitly outside the allow-list.
		case zellijlive.StatusDuplicate, zellijlive.StatusSocketInvalid,
			zellijlive.StatusMissing, zellijlive.StatusPrefixOnly:
			return nil, fmt.Errorf("session %q has non-authoritative status %q", name, session.Status)
		default:
			return nil, fmt.Errorf("session %q has unknown status %q", name, session.Status)
		}
	}
	sort.Strings(active)
	return active, nil
}

func buildRecoveryInventory(state model.State, active []string, history []checkpoints.Checkpoint, observedAt time.Time, resolver procmeta.SessionCWDResolver) model.RecoveryInventory {
	byName := make(map[string]model.RecoverySession, len(active))
	for _, name := range active {
		byName[name] = model.RecoverySession{Name: name}
	}

	// History is oldest-first. Walk newest-first, retaining the newest CWD and
	// newest actual placement independently for each still-active session.
	for i := len(history) - 1; i >= 0; i-- {
		for _, prior := range recoverySessions(history[i]) {
			current, activeNow := byName[prior.Name]
			if !activeNow {
				continue
			}
			if current.CWD == "" && prior.CWD != "" {
				current.CWD = prior.CWD
			}
			if current.PlacementObservedAt == nil && prior.PlacementObservedAt != nil {
				current.WorkspaceRef = prior.WorkspaceRef
				current.Placement = prior.Placement
				current.PlacementObservedAt = prior.PlacementObservedAt
				current.CapturedColumnOccupied = prior.CapturedColumnOccupied
			}
			byName[prior.Name] = current
		}
	}

	visible := make(map[string][]model.Window)
	for _, window := range state.Windows {
		if window.Terminal == nil || !window.Terminal.SessionTagExact {
			continue
		}
		name := window.Terminal.SessionTag
		if _, activeNow := byName[name]; activeNow {
			visible[name] = append(visible[name], window)
		}
	}

	sessions := make([]model.RecoverySession, 0, len(active))
	for _, name := range active {
		session := byName[name]
		matches := visible[name]
		session.Visible = len(matches) > 0
		if len(matches) == 1 {
			window := matches[0]
			if cwd := strings.TrimSpace(window.Terminal.CWD); cwd != "" {
				session.CWD = cwd
			}
			// Workspace, layout, stack occupancy, and timestamp are one observed
			// placement fact. A partial observation must not mix new components
			// with occupancy provenance retained from an older capture.
			if completeRecoveryPlacement(window) {
				session.WorkspaceRef = window.WorkspaceRef
				session.Placement = window.Placement
				session.CapturedColumnOccupied = recoveryColumnOccupied(state, window)
				timestamp := observedAt
				session.PlacementObservedAt = &timestamp
			}
		}
		if resolver != nil && len(matches) != 1 {
			if cwd, err := resolver.Resolve(name); err == nil && strings.TrimSpace(cwd) != "" {
				session.CWD = strings.TrimSpace(cwd)
			}
		}
		sessions = append(sessions, session)
	}
	return model.NormalizeRecovery(model.RecoveryInventory{ActiveSessions: active, Sessions: sessions})
}

func completeRecoveryPlacement(window model.Window) bool {
	if window.WorkspaceRef == nil || window.Placement == nil {
		return false
	}
	placement := window.Placement
	return placement.IsFloating != nil && *placement.IsFloating || placement.Column != nil && placement.Row != nil
}

// recoveryColumnOccupied is evaluated only for an exactly associated visible
// target from a successful full-state collection. Missing layout for another
// window in the same workspace is treated conservatively as possible stacking.
func recoveryColumnOccupied(state model.State, target model.Window) bool {
	if target.Placement == nil || target.Placement.Column == nil || target.Placement.Row == nil || *target.Placement.Row != 0 {
		return false
	}
	for _, other := range state.Windows {
		if other.Key == target.Key || !sameCapturedWorkspace(state.Workspaces, target, other) {
			continue
		}
		if other.Placement == nil || other.Placement.Column == nil {
			return true
		}
		if *other.Placement.Column == *target.Placement.Column {
			return true
		}
	}
	return false
}

func sameCapturedWorkspace(workspaces []model.Workspace, left, right model.Window) bool {
	if left.WorkspaceID != "" && right.WorkspaceID != "" {
		return left.WorkspaceID == right.WorkspaceID
	}
	leftRef, leftOK := recoveryWorkspaceRef(left, workspaces)
	rightRef, rightOK := recoveryWorkspaceRef(right, workspaces)
	if !leftOK || !rightOK {
		return false
	}
	if leftRef.Name != "" && rightRef.Name != "" {
		return leftRef.Name == rightRef.Name
	}
	if leftRef.Output != "" && rightRef.Output != "" && leftRef.Index > 0 && rightRef.Index > 0 {
		return leftRef.Output == rightRef.Output && leftRef.Index == rightRef.Index
	}
	return leftRef.Index > 0 && leftRef.Index == rightRef.Index
}

func recoveryWorkspaceRef(window model.Window, workspaces []model.Workspace) (model.WorkspaceRef, bool) {
	if window.WorkspaceRef != nil {
		ref := *window.WorkspaceRef
		if ref.Name != "" || ref.Output != "" || ref.Index > 0 {
			return ref, true
		}
	}
	for _, workspace := range workspaces {
		if window.WorkspaceID != "" && workspace.ID == window.WorkspaceID {
			return model.WorkspaceRef{Name: workspace.Name, Output: workspace.Output, Index: workspace.Index}, true
		}
	}
	return model.WorkspaceRef{}, false
}

func recoverySessions(checkpoint checkpoints.Checkpoint) []model.RecoverySession {
	if checkpoint.V >= 3 {
		return checkpoint.Recovery.Sessions
	}
	byName := make(map[string][]model.Window)
	for _, window := range checkpoint.State.Windows {
		if window.Terminal != nil && window.Terminal.SessionTag != "" {
			byName[window.Terminal.SessionTag] = append(byName[window.Terminal.SessionTag], window)
		}
	}
	out := make([]model.RecoverySession, 0, len(byName))
	for name, windows := range byName {
		if len(windows) != 1 {
			continue
		}
		window := windows[0]
		session := model.RecoverySession{Name: name, CWD: window.Terminal.CWD, Visible: true}
		if window.WorkspaceRef != nil || window.Placement != nil {
			session.WorkspaceRef = window.WorkspaceRef
			session.Placement = window.Placement
			observed := checkpoint.ObservedAt.UTC()
			session.PlacementObservedAt = &observed
		}
		out = append(out, session)
	}
	return out
}
