package resume

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
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
	Column      *int
	Row         *int
	IsFocused   bool
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

type ColumnOrderer interface {
	FocusWindow(context.Context, int) error
	MoveColumnToIndex(context.Context, int) error
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type ExecutorConfig struct {
	LauncherCommand string
	Timeout         time.Duration
	PollInterval    time.Duration
	RecoveryMaxAge  time.Duration
}

type Executor struct {
	Config    ExecutorConfig
	Launcher  Launcher
	Observer  WindowObserver
	Probe     AttachmentProbe
	Mover     WorkspaceMover
	Layout    LayoutApplier
	Orderer   ColumnOrderer
	Sleeper   Sleeper
	Cataloger zellijlive.Cataloger
	Now       func() time.Time
}

// Apply performs reconciliation in two phases. The first phase verifies and
// attaches every target window, preserving the sequential exact-PID launch
// path. Only after all possible targets exist does the second phase order
// columns by exact window ID.
func (e Executor) Apply(ctx context.Context, plan Plan) Plan {
	plan.Items = append([]Item(nil), plan.Items...)
	if err := e.validate(); err != nil {
		for i := range plan.Items {
			if actionable(plan.Items[i].Status) {
				plan.Items[i].Status = StatusFailed
				plan.Items[i].Reason = err.Error()
			}
		}
		return resummarize(plan)
	}

	initial, focusedID := e.mapExisting(ctx, &plan)
	claimed := make(map[string]ObservedWindow)
	targets := make([]attachedTarget, 0, len(plan.Items))
	for i := range plan.Items {
		item := &plan.Items[i]
		if !actionable(item.Status) {
			continue
		}
		window, ok := initial[i]
		if !ok {
			window, ok = claimed[item.Session]
		}
		if !ok {
			matches, err := e.sessionWindows(ctx, item.Session)
			if err != nil {
				item.Status = StatusFailed
				item.Reason = "cannot safely check for an already-open session: " + err.Error()
				continue
			}
			if len(matches) > 1 {
				item.Status = StatusFailed
				item.Reason = "multiple Niri windows have exact attachment evidence for this Zellij session"
				continue
			}
			if len(matches) == 1 {
				window, ok = matches[0], true
			}
		}

		var process Process
		newWindow := false
		if !ok {
			window, process, ok = e.launchItem(ctx, item, plan.CapturedAt)
			newWindow = ok
			if !ok {
				continue
			}
		} else {
			item.Status = StatusAlreadyOpen
			item.Reason = "matching Zellij session is already open in exactly one verified Niri window"
			item.CurrentWindowKey = fmt.Sprintf("w:%s:%d", window.AppID, window.ID)
			item.CurrentWindowID = window.ID
		}
		if newWindow && !e.revalidateUniqueLaunchedAttachment(ctx, item, window, process) {
			continue
		}
		claimed[item.Session] = window

		if !e.placeAttached(ctx, item, window, process, newWindow) {
			continue
		}
		e.applyOptionalLayout(ctx, item, window.ID)
		targets = append(targets, attachedTarget{item: i, window: window})
	}

	e.orderColumns(ctx, &plan, targets, focusedID)
	return resummarize(plan)
}

type attachedTarget struct {
	item   int
	window ObservedWindow
}

func actionable(status Status) bool {
	return status == StatusReady || status == StatusDegraded || status == StatusAlreadyOpen
}

func resummarize(plan Plan) Plan {
	plan.Summary = Summary{}
	plan.summarize()
	return plan
}

// mapExisting is a mutation-free preflight. In particular, ambiguous existing
// attachments are rejected before any window for that item is launched or moved.
func (e Executor) mapExisting(ctx context.Context, plan *Plan) (map[int]ObservedWindow, int) {
	mapped := make(map[int]ObservedWindow)
	windows, err := e.Observer.Windows(ctx)
	if err != nil {
		for i := range plan.Items {
			if actionable(plan.Items[i].Status) {
				plan.Items[i].Status = StatusFailed
				plan.Items[i].Reason = "cannot observe existing Niri windows: " + err.Error()
			}
		}
		return mapped, 0
	}
	focusedID := 0
	for _, window := range windows {
		if window.IsFocused {
			focusedID = window.ID
		}
	}
	for i := range plan.Items {
		item := &plan.Items[i]
		if !actionable(item.Status) {
			continue
		}
		matches, probeErr := e.sessionWindowsFrom(ctx, windows, item.Session)
		if probeErr != nil {
			item.Status = StatusFailed
			item.Reason = "cannot verify existing session attachment: " + probeErr.Error()
			continue
		}
		switch len(matches) {
		case 1:
			mapped[i] = matches[0]
		case 0:
		default:
			item.Status = StatusFailed
			item.Reason = "multiple Niri windows have exact attachment evidence for this Zellij session"
		}
	}
	return mapped, focusedID
}

func (e Executor) launchItem(ctx context.Context, item *Item, recoveryObservedAt time.Time) (ObservedWindow, Process, bool) {
	if !e.revalidateAllCandidate(ctx, item, recoveryObservedAt) {
		return ObservedWindow{}, nil, false
	}
	process, err := e.Launcher.Start(ctx, KittyLaunchSpec(e.Config.LauncherCommand, *item))
	if err != nil {
		item.Status = StatusFailed
		item.Reason = "Kitty launch failed: " + err.Error()
		return ObservedWindow{}, nil, false
	}
	if process == nil || process.PID() <= 0 || process.Done() == nil {
		if process != nil {
			_ = process.Kill()
		}
		item.Status = StatusFailed
		item.Reason = "launcher does not provide a reliable client PID for Niri correlation"
		return ObservedWindow{}, nil, false
	}
	window, outcome, reason := e.waitForWindow(ctx, process)
	if outcome != "" {
		_ = process.Kill()
		item.Status, item.Reason = outcome, reason
		return ObservedWindow{}, nil, false
	}
	attached, outcome, reason := e.waitForAttachment(ctx, process, item.Session)
	if !attached {
		_ = process.Kill()
		item.Status, item.Reason = outcome, reason
		return ObservedWindow{}, nil, false
	}
	return window, process, true
}

func (e Executor) revalidateUniqueLaunchedAttachment(ctx context.Context, item *Item, launched ObservedWindow, process Process) bool {
	matches, err := e.allAttachedSessionWindows(ctx, item.Session)
	if err == nil && len(matches) == 1 && matches[0].ID == launched.ID && matches[0].PID == launched.PID {
		return true
	}
	_ = process.Kill()
	item.Status = StatusFailed
	if err != nil {
		item.Reason = "cannot safely revalidate exact session attachments before placement: " + err.Error()
	} else if len(matches) > 1 {
		item.Reason = "another exact session attachment appeared before placement; only the newly launched Kitty was terminated"
	} else {
		item.Reason = "the newly launched exact session attachment changed before placement; the launched Kitty was terminated"
	}
	return false
}

func (e Executor) placeAttached(ctx context.Context, item *Item, window ObservedWindow, process Process, newWindow bool) bool {
	if item.Workspace == nil {
		if item.Status == StatusReady {
			if process != nil {
				_ = process.Kill()
			}
			item.Status = StatusFailed
			item.Reason = "ready item has no resolved workspace target"
			return false
		}
		if newWindow {
			item.Status = StatusDegraded
			if item.Reason == "" {
				item.Reason = "session attached without a resolved workspace target"
			}
		}
		return true
	}

	moveCtx, cancel := context.WithTimeout(ctx, e.timeout())
	err := e.Mover.MoveToWorkspace(moveCtx, window.ID, *item.Workspace)
	cancel()
	if err != nil {
		item.Status = StatusFailed
		item.Reason = "workspace move failed; attached terminal left open: " + err.Error()
		return false
	}
	if ok, reason := e.waitForWorkspace(ctx, process, window, item.Workspace.ID); !ok {
		item.Status = StatusFailed
		item.Reason = reason + "; attached terminal left open"
		return false
	}
	if newWindow {
		item.Status = StatusRestored
		item.Reason = ""
	}
	return true
}

func (e Executor) revalidateAllCandidate(ctx context.Context, item *Item, recoveryObservedAt time.Time) bool {
	if item.CandidateSource == "" {
		return true
	}
	if e.Cataloger == nil {
		item.Status = StatusFailed
		item.Reason = "exact Zellij catalog re-observation is unavailable before resume --all launch"
		return false
	}
	observeCtx, cancel := context.WithTimeout(ctx, e.timeout())
	catalog, err := e.Cataloger.Observe(observeCtx)
	cancel()
	if err != nil {
		item.Status = StatusFailed
		item.Reason = "exact Zellij catalog re-observation failed before launch: " + err.Error()
		return false
	}
	live := catalog.Exact(item.Session)
	switch zellijlive.Status(item.ZellijStatus) {
	case zellijlive.StatusActive:
		if live.Status != zellijlive.StatusActive {
			item.Status = StatusUnavailable
			item.Reason = "planned active session changed to exact Zellij safety status " + string(live.Status) + "; resurrection is blocked"
			return false
		}
	case zellijlive.StatusDeadResurrectable:
		if item.CandidateSource != CandidateSourcePriorActive {
			item.Status = StatusUnavailable
			item.Reason = "dead-session resurrection is not permitted by candidate source " + item.CandidateSource
			return false
		}
		switch live.Status {
		case zellijlive.StatusActive:
			// A previously eligible dead prior-active session may become active.
		case zellijlive.StatusDeadResurrectable:
			if e.Config.RecoveryMaxAge <= 0 || recoveryObservedAt.IsZero() || checkpointAge(recoveryObservedAt, e.now()) > e.Config.RecoveryMaxAge {
				item.Status = StatusStale
				item.Reason = "dead-resurrectable prior-active session blocked because recovery point exceeds maximum age"
				return false
			}
		default:
			item.Status = StatusUnavailable
			item.Reason = "prior-active session changed to exact Zellij safety status " + string(live.Status)
			return false
		}
	default:
		item.Status = StatusUnavailable
		item.Reason = "planned resume --all item lacks a permitted exact Zellij safety status"
		return false
	}
	return true
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
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

func (e Executor) sessionWindows(ctx context.Context, session string) ([]ObservedWindow, error) {
	windows, err := e.Observer.Windows(ctx)
	if err != nil {
		return nil, err
	}
	return e.sessionWindowsFrom(ctx, windows, session)
}

func (e Executor) allAttachedSessionWindows(ctx context.Context, session string) ([]ObservedWindow, error) {
	windows, err := e.Observer.Windows(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]ObservedWindow, 0, 1)
	for _, window := range windows {
		if window.PID <= 0 || window.ID <= 0 {
			continue
		}
		attached, err := e.Probe.Attached(ctx, window.PID, session)
		if err != nil {
			return nil, err
		}
		if attached {
			matches = append(matches, window)
		}
	}
	return matches, nil
}

func (e Executor) sessionWindowsFrom(ctx context.Context, windows []ObservedWindow, session string) ([]ObservedWindow, error) {
	matches := make([]ObservedWindow, 0, 1)
	for _, window := range windows {
		if !isTerminal(window.AppID) || window.PID <= 0 || window.ID <= 0 {
			continue
		}
		attached, err := e.Probe.Attached(ctx, window.PID, session)
		if err != nil {
			return nil, err
		}
		if attached {
			matches = append(matches, window)
		}
	}
	return matches, nil
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
	return e.waitForObserved(ctx, process, expected, func(window ObservedWindow) bool {
		return window.WorkspaceID == workspaceID
	}, "workspace movement")
}

func (e Executor) waitForObserved(ctx context.Context, process Process, expected ObservedWindow, matches func(ObservedWindow) bool, description string) (bool, string) {
	waitCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	var lastErr error
	for {
		if process != nil {
			select {
			case err := <-process.Done():
				return false, processExitReason("attached terminal exited before "+description+" was observed", err)
			default:
			}
		}
		windows, err := e.Observer.Windows(waitCtx)
		if err != nil {
			lastErr = err
		} else {
			for _, window := range windows {
				if window.ID == expected.ID && (expected.PID <= 0 || window.PID == expected.PID) && matches(window) {
					return true, ""
				}
			}
		}
		if err := e.sleep(waitCtx); err != nil {
			if lastErr != nil {
				return false, description + " could not be verified: " + lastErr.Error()
			}
			return false, description + " was not observed before timeout"
		}
	}
}

func (e Executor) orderColumns(ctx context.Context, plan *Plan, targets []attachedTarget, focusedID int) {
	ordered := make([]attachedTarget, 0, len(targets))
	for _, target := range targets {
		item := &plan.Items[target.item]
		if item.CapturedPlacement == nil || item.CapturedPlacement.Column == nil {
			continue
		}
		if item.Workspace == nil {
			markColumnResult(item, LayoutUnsupported, "column order requires a resolved workspace")
			continue
		}
		ordered = append(ordered, target)
	}
	if len(ordered) == 0 {
		return
	}
	if e.Orderer == nil {
		for _, target := range ordered {
			markColumnResult(&plan.Items[target.item], LayoutUnsupported, "verified Niri column ordering is unavailable")
		}
		return
	}

	if focusedID > 0 {
		defer func() {
			restoreCtx, cancel := context.WithTimeout(ctx, e.timeout())
			defer cancel()
			if e.Orderer.FocusWindow(restoreCtx, focusedID) == nil {
				_, _ = e.waitForObserved(restoreCtx, nil, ObservedWindow{ID: focusedID}, func(window ObservedWindow) bool {
					return window.IsFocused
				}, "focus restoration")
			}
		}()
	}

	// Moving a column to an absolute index shifts columns between its source and
	// destination. Highest requested indices must therefore be fixed first; an
	// ascending pass can displace an already-verified target when unrelated
	// columns occupy the requested range.
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := plan.Items[ordered[i].item], plan.Items[ordered[j].item]
		if workspaceLess(left.Workspace, right.Workspace) {
			return true
		}
		if workspaceLess(right.Workspace, left.Workspace) {
			return false
		}
		if *left.CapturedPlacement.Column != *right.CapturedPlacement.Column {
			return *left.CapturedPlacement.Column > *right.CapturedPlacement.Column
		}
		return left.Session < right.Session
	})

	for start := 0; start < len(ordered); {
		workspaceID := plan.Items[ordered[start].item].Workspace.ID
		end := start + 1
		for end < len(ordered) && plan.Items[ordered[end].item].Workspace.ID == workspaceID {
			end++
		}
		group := ordered[start:end]
		if reason := stackedReason(plan, group); reason != "" {
			for _, target := range group {
				markColumnResult(&plan.Items[target.item], LayoutUnsupported, reason)
			}
			start = end
			continue
		}

		expected, orderErr := e.observeWorkspace(ctx, workspaceID)
		if orderErr != nil {
			orderErr = fmt.Errorf("capture workspace before ordering: %w", orderErr)
		}
		// A lower-index target can start to the right of an already-fixed target
		// and shift it once. Repeat the descending pass until every exact target
		// is fixed; each corrective high-to-low move leaves lower fixed indices
		// untouched.
		for pass := 0; orderErr == nil && !targetsAtExactColumns(expected, workspaceID, plan, group); pass++ {
			if pass > len(group) {
				orderErr = errors.New("absolute target columns did not converge")
				break
			}
			moved := false
			for _, target := range group {
				item := &plan.Items[target.item]
				column := *item.CapturedPlacement.Column
				if windowAtExactColumn(expected, target.window.ID, workspaceID, column) {
					continue
				}
				moved = true

				beforeFocus, err := e.observeWorkspace(ctx, workspaceID)
				if err != nil {
					orderErr = fmt.Errorf("capture workspace before focus: %w", err)
					break
				}
				if !sameWorkspaceState(beforeFocus, expected) {
					orderErr = errors.New("workspace changed unexpectedly between verified ordering actions")
					break
				}
				afterFocus, err := expectedFocusedState(beforeFocus, target.window.ID)
				if err != nil {
					orderErr = err
					break
				}
				actionCtx, cancel := context.WithTimeout(ctx, e.timeout())
				err = e.Orderer.FocusWindow(actionCtx, target.window.ID)
				cancel()
				if err != nil {
					orderErr = fmt.Errorf("focus exact window %d: %w", target.window.ID, err)
					break
				}
				if _, err = e.waitForWorkspaceTransition(ctx, workspaceID, beforeFocus, afterFocus, "exact focus"); err != nil {
					orderErr = err
					break
				}

				beforeMove, err := e.observeWorkspace(ctx, workspaceID)
				if err != nil {
					orderErr = fmt.Errorf("capture workspace before column move: %w", err)
					break
				}
				if !sameWorkspaceState(beforeMove, afterFocus) {
					orderErr = errors.New("workspace changed unexpectedly after exact focus")
					break
				}
				afterMove, err := expectedColumnMove(beforeMove, target.window.ID, column)
				if err != nil {
					orderErr = err
					break
				}
				actionCtx, cancel = context.WithTimeout(ctx, e.timeout())
				err = e.Orderer.MoveColumnToIndex(actionCtx, column)
				cancel()
				if err != nil {
					orderErr = fmt.Errorf("move focused column to index %d: %w", column, err)
					break
				}
				if _, err = e.waitForWorkspaceTransition(ctx, workspaceID, beforeMove, afterMove, "whole-column move"); err != nil {
					orderErr = err
					break
				}
				expected = afterMove
			}
			if !moved && orderErr == nil {
				orderErr = errors.New("absolute target columns made no progress")
			}
		}
		if orderErr == nil {
			final, err := e.observeWorkspace(ctx, workspaceID)
			if err != nil {
				orderErr = fmt.Errorf("final workspace observation: %w", err)
			} else if !sameWorkspaceState(final, expected) {
				orderErr = errors.New("workspace changed unexpectedly before final ordering verification")
			} else {
				for _, target := range group {
					column := *plan.Items[target.item].CapturedPlacement.Column
					if !windowAtExactColumn(final, target.window.ID, workspaceID, column) {
						orderErr = fmt.Errorf("final workspace observation does not place exact window %d at column %d", target.window.ID, column)
						break
					}
				}
			}
		}
		if orderErr != nil {
			for _, target := range group {
				markColumnResult(&plan.Items[target.item], LayoutDegraded, "workspace column ordering failed: "+orderErr.Error())
			}
		} else {
			for _, target := range group {
				markColumnResult(&plan.Items[target.item], LayoutApplied, "")
			}
		}
		start = end
	}
}

func (e Executor) observeWorkspace(ctx context.Context, workspaceID string) ([]ObservedWindow, error) {
	observeCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	windows, err := e.Observer.Windows(observeCtx)
	if err != nil {
		return nil, err
	}
	out := make([]ObservedWindow, 0, len(windows))
	for _, window := range windows {
		if window.WorkspaceID == workspaceID {
			out = append(out, window)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (e Executor) waitForWorkspaceTransition(ctx context.Context, workspaceID string, before, expected []ObservedWindow, description string) ([]ObservedWindow, error) {
	waitCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	var lastErr error
	for {
		observed, err := e.observeWorkspace(waitCtx, workspaceID)
		if err != nil {
			lastErr = err
		} else if sameWorkspaceState(observed, expected) {
			return observed, nil
		} else if !sameWorkspaceState(observed, before) {
			return nil, fmt.Errorf("%s produced an unexpected workspace mutation", description)
		}
		if err := e.sleep(waitCtx); err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("%s could not be observed: %w", description, lastErr)
			}
			return nil, fmt.Errorf("%s was not observed before timeout", description)
		}
	}
}

func expectedFocusedState(before []ObservedWindow, targetID int) ([]ObservedWindow, error) {
	after := append([]ObservedWindow(nil), before...)
	found := false
	for i := range after {
		after[i].IsFocused = after[i].ID == targetID
		found = found || after[i].ID == targetID
	}
	if !found {
		return nil, fmt.Errorf("exact window %d is absent from its affected workspace", targetID)
	}
	return after, nil
}

func expectedColumnMove(before []ObservedWindow, targetID, destination int) ([]ObservedWindow, error) {
	after := append([]ObservedWindow(nil), before...)
	var source *int
	for i := range before {
		if before[i].ID == targetID {
			if !before[i].IsFocused {
				return nil, fmt.Errorf("exact window %d lost focus before column move", targetID)
			}
			source = before[i].Column
			break
		}
	}
	if source == nil {
		return nil, fmt.Errorf("exact window %d has no observable source column", targetID)
	}
	for i := range after {
		if after[i].Column == nil {
			continue
		}
		column := *after[i].Column
		switch {
		case column == *source:
			column = destination
		case *source < destination && column > *source && column <= destination:
			column--
		case *source > destination && column >= destination && column < *source:
			column++
		}
		value := column
		after[i].Column = &value
	}
	return after, nil
}

func sameWorkspaceState(left, right []ObservedWindow) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID || left[i].PID != right[i].PID || left[i].AppID != right[i].AppID || left[i].WorkspaceID != right[i].WorkspaceID || left[i].IsFocused != right[i].IsFocused || !sameOptionalInt(left[i].Column, right[i].Column) || !sameOptionalInt(left[i].Row, right[i].Row) {
			return false
		}
	}
	return true
}

func sameOptionalInt(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func targetsAtExactColumns(windows []ObservedWindow, workspaceID string, plan *Plan, targets []attachedTarget) bool {
	for _, target := range targets {
		if !windowAtExactColumn(windows, target.window.ID, workspaceID, *plan.Items[target.item].CapturedPlacement.Column) {
			return false
		}
	}
	return true
}

func windowAtExactColumn(windows []ObservedWindow, id int, workspaceID string, column int) bool {
	for _, window := range windows {
		if window.ID == id {
			return window.WorkspaceID == workspaceID && window.Column != nil && *window.Column == column
		}
	}
	return false
}

func workspaceLess(left, right *WorkspaceTarget) bool {
	if left.Index != right.Index {
		return left.Index < right.Index
	}
	if left.Output != right.Output {
		return left.Output < right.Output
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.ID < right.ID
}

func stackedReason(plan *Plan, group []attachedTarget) string {
	columns := make(map[int]struct{}, len(group))
	for _, target := range group {
		item := plan.Items[target.item]
		placement := item.CapturedPlacement
		if item.CapturedColumnOccupied || placement.Row != nil && *placement.Row > 1 {
			return "captured stacked rows are unsupported without consume or expel actions"
		}
		column := *placement.Column
		if _, exists := columns[column]; exists {
			return "captured stacked rows are unsupported without consume or expel actions"
		}
		columns[column] = struct{}{}
	}
	return ""
}

func markColumnResult(item *Item, status LayoutStatus, reason string) {
	if status == LayoutApplied {
		if item.LayoutStatus == LayoutNotRequested || item.LayoutStatus == "" {
			item.LayoutStatus = LayoutApplied
		}
		return
	}
	if status == LayoutDegraded || item.LayoutStatus == LayoutDegraded {
		item.LayoutStatus = LayoutDegraded
	} else {
		item.LayoutStatus = status
	}
	if reason != "" {
		if item.LayoutReason != "" {
			item.LayoutReason += "; "
		}
		item.LayoutReason += reason
	}
}

func (e Executor) applyOptionalLayout(ctx context.Context, item *Item, windowID int) {
	if item.CapturedPlacement == nil {
		item.LayoutStatus = LayoutNotRequested
		return
	}
	_, _, haveSize := preferredSize(*item.CapturedPlacement)
	if item.CapturedPlacement.IsFloating == nil && !haveSize {
		item.LayoutStatus = LayoutNotRequested
		return
	}
	if e.Layout == nil {
		item.LayoutStatus = LayoutUnsupported
		item.LayoutReason = "optional Niri floating and sizing actions are unavailable"
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
	pids, err := procmeta.DescendantPIDs("/proc", rootPID)
	if err != nil {
		return nil
	}
	return pids
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
