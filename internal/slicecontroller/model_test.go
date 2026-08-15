package slicecontroller

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/slicelayout"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
)

// modelController is deliberately smaller than Engine. It models only the
// published authority, destructive-evidence, user-intent, and bounded-effect
// rules; it has no projections, cleanup implementation, Store, or Engine calls.
type modelController struct {
	host     string
	epoch    string
	revision uint64
	digest   string
	retired  map[string]bool
	all      bool
	selected map[string]bool
	pickups  map[string]bool
	drops    map[string]modelDrop
	sources  map[string]modelSource
	undo     []modelUndo
	grace    time.Duration
	confirms int
}

type modelDrop struct {
	name     string
	count    int
	since    time.Time
	deadline time.Time
}

type modelSource struct {
	epoch, session, name, workspace string
	lifecycle                       SourceLifecycle
	absence                         int
	since, deadline                 time.Time
}

type modelUndo struct {
	kind, key, source, session string
	previous                   bool
	previousDrop               modelDrop
}

type modelObservation struct {
	quality   sliceprotocol.Quality
	host      string
	epoch     string
	revision  uint64
	digest    string
	sources   map[string]modelSource
	live      map[string]bool
	conflicts map[string]string
}

func newModelController() *modelController {
	return &modelController{
		retired:  map[string]bool{},
		selected: map[string]bool{},
		pickups:  map[string]bool{},
		drops:    map[string]modelDrop{},
		sources:  map[string]modelSource{},
		grace:    5 * time.Second,
		confirms: 2,
	}
}

func (m *modelController) observe(ob modelObservation, now time.Time) sliceprotocol.AcceptanceDecision {
	decision := sliceprotocol.DecisionAccepted
	if m.host != "" && ob.host != m.host {
		decision = sliceprotocol.DecisionConflict
	} else if ob.quality == sliceprotocol.QualityDegraded {
		decision = sliceprotocol.DecisionDegraded
	} else if m.host == "" {
		decision = sliceprotocol.DecisionAccepted
	} else if m.retired[ob.epoch] {
		decision = sliceprotocol.DecisionReplay
	} else if ob.epoch != m.epoch {
		decision = sliceprotocol.DecisionFullResync
	} else if ob.revision < m.revision {
		decision = sliceprotocol.DecisionStale
	} else if ob.revision == m.revision && ob.digest != m.digest {
		decision = sliceprotocol.DecisionConflict
	} else if ob.revision == m.revision {
		decision = sliceprotocol.DecisionDuplicate
	}
	if decision != sliceprotocol.DecisionAccepted && decision != sliceprotocol.DecisionFullResync {
		return decision
	}
	if decision == sliceprotocol.DecisionFullResync {
		m.retired[m.epoch] = true
		for id, source := range m.sources {
			if source.epoch == m.epoch {
				source.lifecycle = SourceReplaced
				m.sources[id] = source
			}
		}
	}
	m.host, m.epoch, m.revision, m.digest = ob.host, ob.epoch, ob.revision, ob.digest

	// Complete inventories with any per-source conflict are not independent
	// session-absence evidence.
	if len(ob.conflicts) == 0 {
		for id, drop := range m.drops {
			if ob.live[id] {
				drop.count, drop.since, drop.deadline = 0, time.Time{}, time.Time{}
				m.drops[id] = drop
				continue
			}
			drop.count++
			if drop.since.IsZero() {
				drop.since, drop.deadline = now, now.Add(m.grace)
			}
			if drop.count >= m.confirms && !now.Before(drop.deadline) {
				delete(m.drops, id)
			} else {
				m.drops[id] = drop
			}
		}
	}

	for id, incoming := range ob.sources {
		incoming.lifecycle, incoming.absence = SourceEligible, 0
		incoming.since, incoming.deadline = time.Time{}, time.Time{}
		m.sources[id] = incoming
	}
	for id, source := range m.sources {
		if source.epoch != ob.epoch || source.lifecycle == SourceClosed || source.lifecycle == SourceReplaced {
			continue
		}
		if _, present := ob.sources[id]; present {
			continue
		}
		if code := ob.conflicts[id]; code != "" {
			source.lifecycle, source.absence = SourceConflict, 0
			source.since, source.deadline = time.Time{}, time.Time{}
			m.sources[id] = source
			continue
		}
		source.absence++
		if source.since.IsZero() {
			source.since, source.deadline = now, now.Add(m.grace)
		}
		source.lifecycle = SourceGoneGrace
		if source.absence >= m.confirms || !now.Before(source.deadline) {
			source.lifecycle = SourceClosed
			delete(m.pickups, id)
		}
		m.sources[id] = source
	}
	return decision
}

func (m *modelController) selectWorkspace(key string, enabled bool) {
	previous := m.selected[key]
	if previous == enabled {
		return
	}
	if enabled {
		m.selected[key] = true
	} else {
		delete(m.selected, key)
	}
	m.undo = append(m.undo, modelUndo{kind: "workspace", key: key, previous: previous})
}

func (m *modelController) selectAll(enabled bool) {
	m.all = enabled
}

func (m *modelController) pickup(id string, enabled bool) {
	previous := m.pickups[id]
	if previous == enabled {
		return
	}
	if enabled {
		m.pickups[id] = true
	} else {
		delete(m.pickups, id)
	}
	m.undo = append(m.undo, modelUndo{kind: "pickup", source: id, previous: previous})
}

func (m *modelController) close(id string, enabled bool, now time.Time) {
	source, ok := m.sources[id]
	if !ok {
		return
	}
	prior, previous := m.drops[source.session]
	if previous == enabled {
		return
	}
	m.undo = append(m.undo, modelUndo{kind: "close", source: id, session: source.session, previous: previous, previousDrop: prior})
	if enabled {
		m.drops[source.session] = modelDrop{name: source.name}
	} else {
		delete(m.drops, source.session)
	}
	_ = now // Creation time is intentionally not part of the safety oracle.
}

func (m *modelController) hasUndoTarget() bool {
	for index := len(m.undo) - 1; index >= 0; index-- {
		a := m.undo[index]
		if a.kind == "workspace" || a.kind == "close" {
			return true
		}
		source, ok := m.sources[a.source]
		if ok && source.lifecycle != SourceClosed && source.lifecycle != SourceReplaced {
			return true
		}
	}
	return false
}

func (m *modelController) undoOne() bool {
	var a modelUndo
	found := false
	for len(m.undo) > 0 {
		a = m.undo[len(m.undo)-1]
		m.undo = m.undo[:len(m.undo)-1]
		if a.kind == "workspace" || a.kind == "close" {
			found = true
			break
		}
		source, ok := m.sources[a.source]
		if ok && source.lifecycle != SourceClosed && source.lifecycle != SourceReplaced {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	switch a.kind {
	case "workspace":
		if a.previous {
			m.selected[a.key] = true
		} else {
			delete(m.selected, a.key)
		}
	case "pickup":
		if a.previous {
			m.pickups[a.source] = true
		} else {
			delete(m.pickups, a.source)
		}
	case "close":
		if a.previous {
			m.drops[a.session] = a.previousDrop
		} else {
			delete(m.drops, a.session)
		}
	}
	return true
}

func (m *modelController) tick(now time.Time) {
	for id, drop := range m.drops {
		if drop.count >= m.confirms && !drop.deadline.IsZero() && !now.Before(drop.deadline) {
			delete(m.drops, id)
		}
	}
	for id, source := range m.sources {
		if source.lifecycle == SourceGoneGrace && !source.deadline.IsZero() && !now.Before(source.deadline) {
			source.lifecycle = SourceClosed
			delete(m.pickups, id)
			m.sources[id] = source
		}
	}
}

func (m *modelController) wanted(id string) bool {
	source, ok := m.sources[id]
	if !ok || (source.lifecycle != SourceEligible && source.lifecycle != SourceGoneGrace) {
		return false
	}
	return (m.all || m.selected[source.workspace] || m.pickups[id]) && func() bool { _, dropped := m.drops[source.session]; return !dropped }()
}

type modelOp struct {
	Kind     string `json:"kind"`
	Slot     int    `json:"slot,omitempty"`
	Enabled  bool   `json:"enabled,omitempty"`
	Millis   int    `json:"millis,omitempty"`
	Token    int    `json:"token,omitempty"`
	Required bool   `json:"required,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
}

type modelWitnesses struct {
	undoTarget, undoNoTarget                          int
	recoveryRetry, recoveryRestart, recoverySuccessor int
	conflictedExpiration, undesiredExpiration         int
	attachmentLoss, attachmentExhaustion              int
	cleanupBlockedLaunch, cleanupBlockedReconnect     int
	successorBlockedLaunch, reconnectSuccess          int
	processLoss, handoffMonotonic                     int
}

type modelRun struct {
	engine       *Engine
	store        *Store
	oracle       *modelController
	now          time.Time
	epochIndex   int
	revision     uint64
	last         *sliceprotocol.Authoritative
	retired      *sliceprotocol.Authoritative
	history      map[uint64]sliceprotocol.Authoritative
	pid          int
	localEpoch   int
	lastDecision sliceprotocol.AcceptanceDecision
	witnesses    modelWitnesses
}

func newModelRun(t *testing.T) (*modelRun, error) {
	t.Helper()
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	store, err := NewStore(t.TempDir())
	if err != nil {
		return nil, err
	}
	if _, err := store.Initialize(Namespace{Host: "host", Leech: "leech"}); err != nil {
		return nil, err
	}
	r := &modelRun{store: store, oracle: newModelController(), now: now, epochIndex: 1, history: map[uint64]sliceprotocol.Authoritative{}, pid: 1000}
	r.engine = &Engine{Store: store, Config: ControllerConfig{RetryWindow: 10 * time.Second, RetryInitialBackoff: time.Second, RetryMaxBackoff: 2 * time.Second, RetryMaxAttempts: 3, SourceGoneGrace: 5 * time.Second, SourceGoneConfirmations: 2}, Now: func() time.Time { return r.now }}
	return r, nil
}

func modelUUID(index int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", index, index)
}

func modelSession(slot int) (string, string) {
	if slot == 0 {
		return sessionA, "one"
	}
	return sessionB, "two"
}

func (r *modelRun) source(slot int) sliceprotocol.Source {
	runtimeID := uint64(slot + 41)
	id, err := sourceinventory.SourceID(modelUUID(r.epochIndex), runtimeID)
	if err != nil {
		panic(err)
	}
	session, name := modelSession(slot)
	source := testSource(id, session, name, "Work")
	source.RuntimeWindowID = runtimeID
	source.Layout.Position = &sliceprotocol.Position{Column: slot + 1, Tile: 1}
	return source
}

func authorityDigest(a sliceprotocol.Authoritative) string {
	// Independent, intentionally non-cryptographic semantic representation.
	var parts []string
	for _, id := range a.LiveSessionIDs {
		parts = append(parts, "live="+id)
	}
	for _, source := range a.Sources {
		position := "floating"
		if source.Layout.Position != nil {
			position = fmt.Sprintf("%d/%d", source.Layout.Position.Column, source.Layout.Position.Tile)
		}
		parts = append(parts, fmt.Sprintf("source=%s:%d:%s:%s:%s:%d:%s:%s:%g:%g:%g:%d:%d:%d:%s", source.SourceID, source.RuntimeWindowID, source.Session.ID, source.Session.Name, source.Workspace.Key, source.Workspace.RuntimeID, source.Output.Name, source.Output.Transform, source.Output.Scale, source.Layout.TileWidth, source.Layout.TileHeight, source.Layout.WindowWidth, source.Layout.WindowHeight, source.Output.LogicalWidth, position))
	}
	for _, conflict := range a.Conflicts {
		parts = append(parts, fmt.Sprintf("conflict=%s:%s:%s", conflict.Code, conflict.SourceID, conflict.SessionID))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func modelObservationFromEnvelope(env sliceprotocol.Envelope) modelObservation {
	ob := modelObservation{quality: env.Observation.Quality, host: env.SourceHostID, sources: map[string]modelSource{}, live: map[string]bool{}, conflicts: map[string]string{}}
	if env.Authoritative == nil {
		return ob
	}
	a := env.Authoritative
	ob.epoch, ob.revision, ob.digest = a.SourceEpoch, a.Revision, authorityDigest(*a)
	for _, id := range a.LiveSessionIDs {
		ob.live[id] = true
	}
	for _, source := range a.Sources {
		ob.sources[source.SourceID] = modelSource{epoch: a.SourceEpoch, session: source.Session.ID, name: source.Session.Name, workspace: source.Workspace.Key}
	}
	for _, conflict := range a.Conflicts {
		ob.conflicts[conflict.SourceID] = string(conflict.Code)
	}
	return ob
}

func (r *modelRun) complete(kind string) sliceprotocol.Envelope {
	r.revision++
	sources := []sliceprotocol.Source{r.source(0), r.source(1)}
	live := []string{sessionA, sessionB}
	var conflicts []sliceprotocol.Conflict
	switch kind {
	case "headless":
		sources = sources[1:]
	case "absent":
		sources, live = sources[1:], []string{sessionB}
	case "source_conflict":
		missing := sources[0]
		sources = sources[1:]
		conflicts = []sliceprotocol.Conflict{{Code: sliceprotocol.ConflictSessionMissing, SourceID: missing.SourceID, SessionID: missing.Session.ID}}
	case "order":
		sources[0].Layout.Position = &sliceprotocol.Position{Column: 3, Tile: 2}
	}
	a := sliceprotocol.Authoritative{SourceEpoch: modelUUID(r.epochIndex), Revision: r.revision, ObservedAt: r.now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: live, Sources: sources, Conflicts: conflicts}
	a = sliceprotocol.Canonicalize(a)
	r.last = &a
	r.history[a.Revision] = a
	return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: r.now}, Authoritative: &a}
}

func cloneModelAuthority(value sliceprotocol.Authoritative) sliceprotocol.Authoritative {
	payload, _ := json.Marshal(value)
	var out sliceprotocol.Authoritative
	_ = json.Unmarshal(payload, &out)
	return out
}

func (r *modelRun) observation(kind string) sliceprotocol.Envelope {
	switch kind {
	case "complete", "headless", "absent", "source_conflict", "order":
		return r.complete(kind)
	case "rotate":
		if r.last != nil {
			copy := cloneModelAuthority(*r.last)
			r.retired = &copy
		}
		r.epochIndex++
		r.revision = 0
		r.history = map[uint64]sliceprotocol.Authoritative{}
		return r.complete("complete")
	case "degraded":
		var authority *sliceprotocol.Authoritative
		if r.last != nil {
			copy := cloneModelAuthority(*r.last)
			authority = &copy
		}
		return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityDegraded, AttemptedAt: r.now, DegradedReasons: []sliceprotocol.Reason{{Code: sliceprotocol.ReasonNiriReplayTimeout}}}, Authoritative: authority}
	case "duplicate":
		if r.last == nil {
			return r.complete("complete")
		}
		copy := cloneModelAuthority(*r.last)
		return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: r.now}, Authoritative: &copy}
	case "stale":
		if r.last == nil || r.last.Revision <= 1 {
			return r.observation("duplicate")
		}
		copy := cloneModelAuthority(r.history[r.last.Revision-1])
		copy.ObservedAt = r.now
		return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: r.now}, Authoritative: &copy}
	case "conflict":
		if r.last == nil {
			return r.complete("complete")
		}
		copy := cloneModelAuthority(*r.last)
		if len(copy.Sources) > 0 {
			copy.Sources[0].Layout.WindowWidth++
		} else if len(copy.LiveSessionIDs) == 0 {
			copy.LiveSessionIDs = append(copy.LiveSessionIDs, sessionA)
		} else {
			copy.LiveSessionIDs = append(copy.LiveSessionIDs, sessionB)
		}
		copy = sliceprotocol.Canonicalize(copy)
		return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: r.now}, Authoritative: &copy}
	case "replay":
		if r.retired == nil {
			return r.observation("duplicate")
		}
		copy := cloneModelAuthority(*r.retired)
		return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: r.now}, Authoritative: &copy}
	default:
		panic("unknown observation: " + kind)
	}
}

func (r *modelRun) currentSource(slot int) string {
	state, err := r.engine.Status()
	if err != nil || state.Inventory == nil {
		return ""
	}
	session, _ := modelSession(slot)
	for _, source := range state.Inventory.Sources {
		if source.Session.ID == session {
			return source.SourceID
		}
	}
	for id, source := range state.Sources {
		if source.SessionID == session && source.SourceEpoch == state.Acceptance.SourceEpoch {
			return id
		}
	}
	return ""
}

func (r *modelRun) apply(op modelOp) ([]Effect, error) {
	var effects []Effect
	switch op.Kind {
	case "complete", "headless", "absent", "source_conflict", "order", "rotate", "degraded", "duplicate", "stale", "conflict", "replay":
		env := r.observation(op.Kind)
		expected := r.oracle.observe(modelObservationFromEnvelope(env), r.now)
		_, gotEffects, decision, err := r.engine.ApplyEnvelope(env, r.now)
		if err != nil {
			return nil, err
		}
		if decision != expected {
			return nil, fmt.Errorf("decision=%s want=%s", decision, expected)
		}
		r.lastDecision, effects = decision, gotEffects
	case "select":
		_, got, err := r.engine.SelectWorkspace("Work", op.Enabled)
		if err != nil {
			return nil, err
		}
		r.oracle.selectWorkspace("work", op.Enabled)
		effects = got
	case "all":
		_, got, err := r.engine.SelectAll(op.Enabled)
		if err != nil {
			return nil, err
		}
		r.oracle.selectAll(op.Enabled)
		effects = got
	case "pickup":
		id := r.currentSource(op.Slot)
		if id == "" {
			return nil, nil
		}
		_, got, err := r.engine.Pickup(id, op.Enabled)
		if err != nil {
			return nil, err
		}
		r.oracle.pickup(id, op.Enabled)
		effects = got
	case "close", "reopen":
		id := r.currentSource(op.Slot)
		if id == "" {
			return nil, nil
		}
		enabled := op.Kind == "close"
		var err error
		if enabled {
			_, effects, err = r.engine.Close(id)
		} else {
			_, effects, err = r.engine.Reopen(id)
		}
		if err != nil {
			return nil, err
		}
		r.oracle.close(id, enabled, r.now)
	case "undo":
		expectsTarget := r.oracle.hasUndoTarget()
		_, got, err := r.engine.Undo()
		if expectsTarget {
			if err != nil {
				return nil, fmt.Errorf("oracle expected undo target: %w", err)
			}
			if !r.oracle.undoOne() {
				return nil, errors.New("oracle undo target disappeared")
			}
			r.witnesses.undoTarget++
			effects = got
		} else {
			if err == nil || !strings.Contains(err.Error(), "nothing to undo") {
				return nil, fmt.Errorf("oracle expected nothing-to-undo error, got %v", err)
			}
			r.witnesses.undoNoTarget++
		}
	case "advance":
		r.now = r.now.Add(time.Duration(op.Millis) * time.Millisecond)
		r.oracle.tick(r.now)
		_, got, err := r.engine.Tick()
		if err != nil {
			return nil, err
		}
		effects = got
	case "expire_conflicted":
		id := r.currentSource(op.Slot)
		state, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		tracked, known := state.Sources[id]
		expected, modeled := r.oracle.sources[id]
		if id == "" || !known || !modeled || tracked.Connection != ConnectionReconnecting || tracked.Recovery == nil || tracked.Lifecycle != SourceConflict || expected.lifecycle != SourceConflict {
			return nil, fmt.Errorf("required conflicted-expiration precondition missing: slot=%d source=%q tracked=%+v oracle=%+v", op.Slot, id, tracked, expected)
		}
		deadline := *tracked.Recovery
		if !deadline.ExpiresAt.Equal(deadline.StartedAt.Add(r.engine.config().RetryWindow)) {
			return nil, fmt.Errorf("conflicted recovery deadline is not the oracle absolute window: %+v", deadline)
		}
		r.now = deadline.ExpiresAt.Add(time.Millisecond)
		r.oracle.tick(r.now)
		state, effects, statusErr = r.engine.Tick()
		if statusErr != nil {
			return nil, statusErr
		}
		if err := r.assertUnsafeExpiration(state, id, expected.session, effects, "conflicted"); err != nil {
			return nil, err
		}
		r.witnesses.conflictedExpiration++
	case "expire_undesired":
		id := r.currentSource(op.Slot)
		state, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		tracked, known := state.Sources[id]
		expected, modeled := r.oracle.sources[id]
		if id == "" || !known || !modeled || tracked.Connection != ConnectionReconnecting || tracked.Recovery == nil || !r.oracle.wanted(id) {
			return nil, fmt.Errorf("required undesired-expiration precondition missing: slot=%d source=%q tracked=%+v oracle_wanted=%t", op.Slot, id, tracked, r.oracle.wanted(id))
		}
		deadline := *tracked.Recovery
		if !deadline.ExpiresAt.Equal(deadline.StartedAt.Add(r.engine.config().RetryWindow)) {
			return nil, fmt.Errorf("undesired recovery deadline is not the oracle absolute window: %+v", deadline)
		}
		state, effects, statusErr = r.engine.SelectWorkspace("Work", false)
		if statusErr != nil {
			return nil, statusErr
		}
		r.oracle.selectWorkspace("work", false)
		retained := state.Sources[id].Recovery
		if retained == nil || *retained != deadline || state.Wanted(id) || r.oracle.wanted(id) {
			return nil, fmt.Errorf("deselection changed recovery identity or remained desired: source=%+v oracle_wanted=%t", state.Sources[id], r.oracle.wanted(id))
		}
		r.now = deadline.ExpiresAt.Add(time.Millisecond)
		r.oracle.tick(r.now)
		state, tickEffects, statusErr := r.engine.Tick()
		if statusErr != nil {
			return nil, statusErr
		}
		effects = append(effects, tickEffects...)
		if err := r.assertUnsafeExpiration(state, id, expected.session, effects, "undesired"); err != nil {
			return nil, err
		}
		r.witnesses.undesiredExpiration++
	case "launch_fail":
		id := r.currentSource(op.Slot)
		state, err := r.engine.Status()
		if err != nil {
			return nil, err
		}
		tracked := state.Sources[id]
		if id == "" || state.Projections[id].Status != ProjectionLaunching || tracked.Lifecycle != SourceEligible || !r.oracle.wanted(id) {
			_, gotErr := r.engine.RecordLaunch("model-no-target", 0, errors.New("model process loss"))
			if gotErr == nil || !strings.Contains(gotErr.Error(), "mapping missing") {
				return nil, fmt.Errorf("oracle expected launch-failure no-target error, got %v", gotErr)
			}
			return nil, nil
		}
		_, err = r.engine.RecordLaunch(id, 0, errors.New("model process loss"))
		return nil, err
	case "connect":
		id := r.currentSource(op.Slot)
		state, err := r.engine.Status()
		if err != nil {
			return nil, err
		}
		p, connectable := state.Projections[id]
		tracked := state.Sources[id]
		connectable = id != "" && connectable && p.Status == ProjectionLaunching && tracked.Lifecycle == SourceEligible && r.oracle.wanted(id)
		if !connectable {
			if op.Required {
				return nil, fmt.Errorf("required connect precondition missing: slot=%d source=%q projection=%+v", op.Slot, id, p)
			}
			_, gotErr := r.engine.AttachmentConnected("model-no-target")
			if gotErr == nil || !strings.Contains(gotErr.Error(), "not connectable") {
				return nil, fmt.Errorf("oracle expected connect no-target error, got %v", gotErr)
			}
			return nil, nil
		}
		r.pid++
		argv := []string{"/model/kitty", "--class", p.AppID, "-e", "redeem", "slice", "projection-run", "--source-id", id, "--session", p.ExpectedSessionName, "--token", p.AttachToken}
		if _, err = r.engine.PrepareProjection(id, "/model/kitty", argv); err != nil {
			return nil, err
		}
		if _, err = r.engine.RecordLaunch(id, r.pid, nil); err != nil {
			return nil, err
		}
		r.localEpoch++
		local := []OwnedWindow{{SourceID: id, WindowID: uint64(r.pid), PID: r.pid, AppID: p.AppID}}
		for otherID, projection := range state.Projections {
			if otherID != id && projection.Status == ProjectionOwned {
				local = append(local, OwnedWindow{SourceID: otherID, WindowID: projection.NiriWindowID, PID: projection.ExpectedPID, AppID: projection.AppID})
			}
		}
		if _, _, err = r.engine.ObserveLocal(fmt.Sprintf("local-%d", r.localEpoch), local); err != nil {
			return nil, err
		}
		_, err = r.engine.AttachmentConnected(id)
		return nil, err
	case "attachment_loss":
		id := r.currentSource(op.Slot)
		state, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		tracked, known := state.Sources[id]
		_, projected := state.Projections[id]
		applicable := known && projected && tracked.Lifecycle == SourceEligible && tracked.Connection == ConnectionConnected
		if id == "" || !applicable {
			if op.Required {
				return nil, fmt.Errorf("required attachment-loss precondition missing for slot %d: source=%+v projected=%t", op.Slot, tracked, projected)
			}
			_, _, gotErr := r.engine.AttachmentLost("model-no-target")
			if gotErr == nil || !strings.Contains(gotErr.Error(), "unknown source") {
				return nil, fmt.Errorf("oracle expected attachment-loss unknown-source error, got %v", gotErr)
			}
			return nil, nil
		}
		_, got, err := r.engine.AttachmentLost(id)
		if err != nil {
			return nil, fmt.Errorf("oracle expected attachment loss success: %w", err)
		}
		if op.Required && len(got) == 0 {
			return nil, errors.New("required attachment loss produced no close effect")
		}
		r.witnesses.attachmentLoss++
		effects = got
	case "process_loss":
		id := r.currentSource(op.Slot)
		state, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		if id == "" || state.Projections[id].SourceID == "" || state.Sources[id].Lifecycle != SourceEligible || state.Sources[id].Recovery != nil {
			if op.Required {
				return nil, fmt.Errorf("required process-loss evidence precondition missing: slot=%d source=%q", op.Slot, id)
			}
			return nil, nil // Explicit oracle no-target outcome: no owned mapping can carry process evidence.
		}
		var retained []OwnedWindow
		for otherID, projection := range state.Projections {
			if otherID != id && projection.Status == ProjectionOwned {
				retained = append(retained, OwnedWindow{SourceID: otherID, WindowID: projection.NiriWindowID, PID: projection.ExpectedPID, AppID: projection.AppID})
			}
		}
		_, got, err := r.engine.ObserveLocalWithConflicts("local-model", retained, map[string]string{id: "projection_process_evidence_incomplete"})
		if err != nil {
			return nil, err
		}
		if source, ok := r.oracle.sources[id]; ok {
			source.lifecycle = SourceConflict
			r.oracle.sources[id] = source
		}
		r.witnesses.processLoss++
		effects = got
	case "cleanup":
		state, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		closing := false
		for _, projection := range state.Projections {
			closing = closing || projection.Status == ProjectionClosing
		}
		if !closing {
			if op.Required {
				return nil, errors.New("required cleanup precondition missing: no closing projection")
			}
			return nil, nil // Explicit oracle no-target outcome; absence is not cleanup evidence for a live projection.
		}
		_, got, err := r.engine.ObserveLocal("local-model", nil)
		if err != nil {
			return nil, err
		}
		effects = got
	case "restart":
		r.engine = &Engine{Store: r.store, Config: r.engine.Config, Now: func() time.Time { return r.now }}
		_, err := r.engine.PrepareStartup()
		return nil, err
	case "reconnect":
		id := r.currentSource(op.Slot)
		state, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		expect := reconnectOracleOutcome(state, id)
		if op.Required && op.Outcome == "" && expect != "success" {
			return nil, fmt.Errorf("required reconnect precondition missing: oracle outcome=%s source=%q", expect, id)
		}
		_, got, err := r.engine.Reconnect(func() string {
			if id == "" {
				return "model-no-target"
			}
			return id
		}())
		if expect == "success" {
			if err != nil {
				return nil, fmt.Errorf("oracle expected reconnect success: %w", err)
			}
			r.witnesses.reconnectSuccess++
			effects = got
		} else {
			if err == nil || !strings.Contains(err.Error(), expect) {
				return nil, fmt.Errorf("oracle expected reconnect error containing %q, got %v", expect, err)
			}
			if op.Outcome == "cleanup_error" && expect != "cleanup" {
				return nil, fmt.Errorf("required cleanup reconnect outcome was %q", expect)
			}
			if expect == "cleanup" {
				r.witnesses.cleanupBlockedReconnect++
			}
		}
	case "handoff_pending", "handoff_failed", "handoff_regress":
		token := fmt.Sprintf("model-token-%d", op.Token)
		status := map[string]string{"handoff_pending": "launch_pending", "handoff_failed": "failed", "handoff_regress": "launch_pending"}[op.Kind]
		before, statusErr := r.engine.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		prior, exists := before.LaunchHandoffs[token]
		expectError := op.Kind == "handoff_regress" || (op.Kind == "handoff_failed" && !exists)
		if op.Kind == "handoff_pending" && exists && prior.Status != "launch_pending" {
			expectError = true
		}
		_, err := r.engine.SetLaunchHandoff(LaunchHandoff{Token: token, Status: status, HostTerminalID: "model-terminal"})
		if expectError {
			if err == nil {
				return nil, fmt.Errorf("oracle expected monotonic handoff rejection: token=%s prior=%s next=%s", token, prior.Status, status)
			}
			after, _ := r.engine.Status()
			if !reflect.DeepEqual(before.LaunchHandoffs[token], after.LaunchHandoffs[token]) {
				return nil, errors.New("rejected same-token handoff changed authority")
			}
			r.witnesses.handoffMonotonic++
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("oracle expected handoff transition success: %w", err)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown operation %q", op.Kind)
	}
	return effects, nil
}

func (r *modelRun) assertUnsafeExpiration(state State, sourceID, sessionID string, effects []Effect, path string) error {
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			return fmt.Errorf("%s expiration launched projection %+v", path, effect)
		}
	}
	source, ok := state.Sources[sourceID]
	if !ok || source.Connection != ConnectionDisconnected || source.Recovery != nil {
		return fmt.Errorf("%s expiration did not reach stable disconnected: %+v", path, source)
	}
	if _, projected := state.Projections[sourceID]; projected {
		return fmt.Errorf("%s expiration retained projection %+v", path, state.Projections[sourceID])
	}
	want := SuccessorGate{OldSourceID: sourceID, SessionID: sessionID, CreatedAt: r.now}
	if !reflect.DeepEqual(state.SuccessorGates[sourceID], want) {
		return fmt.Errorf("%s expiration gate=%+v want=%+v", path, state.SuccessorGates[sourceID], want)
	}
	return nil
}

func reconnectOracleOutcome(state State, sourceID string) string {
	source, exists := state.Sources[sourceID]
	if !exists {
		return "unknown source"
	}
	if len(state.PendingCleanups) > 0 {
		return "cleanup"
	}
	gateID, gate, gated := sourceID, state.SuccessorGates[sourceID], false
	if gate.SessionID != "" {
		gated = true
	} else {
		for oldID, candidate := range state.SuccessorGates {
			if candidate.SessionID == source.SessionID {
				gateID, gate, gated = oldID, candidate, true
				break
			}
		}
	}
	if gated {
		if _, dropped := state.ClosedByUser[gate.SessionID]; dropped {
			return "closed_by_user"
		}
		eligible := 0
		for id, candidate := range state.Sources {
			_, dropped := state.ClosedByUser[candidate.SessionID]
			desired := state.Wanted(id) || (state.Pickups[gateID] && !dropped)
			if candidate.Lifecycle == SourceEligible && candidate.SessionID == gate.SessionID && desired {
				eligible++
			}
		}
		if eligible != 1 {
			return "exactly one"
		}
		return "success"
	}
	if _, dropped := state.ClosedByUser[source.SessionID]; dropped {
		return "closed_by_user"
	}
	if source.Lifecycle != SourceEligible || !state.Wanted(sourceID) {
		return "eligible desired"
	}
	return "success"
}

func (r *modelRun) check(pre State, op modelOp, effects []Effect) error {
	state, err := r.engine.Status()
	if err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("invalid state: %w", err)
	}
	if len(state.Sources) > MaxTerminalSources || len(state.Projections) > MaxTerminalSources || len(state.ClosedByUser) > MaxTerminalSources || len(state.SelectedWorkspaces) > MaxSelectedWorkspaces || len(state.SuccessorGates) > MaxSuccessorGates || len(state.PendingCleanups) > MaxSuccessorGates || len(state.Lineage) > MaxLineageRecords || len(state.LaunchHandoffs) > MaxLaunchHandoffs || len(state.RetiredHandoffTokens) > MaxRetiredHandoffTombstones || len(state.Spatial) > MaxSpatialRecords || len(state.Audit) > MaxAuditEntries || len(state.Undo) > MaxUndoEntries || len(state.Acceptance.RetiredEpochs) > sliceprotocol.MaxRetiredEpochTombstones {
		return errors.New("persisted state cap exceeded")
	}
	if state.AllEligible != r.oracle.all {
		return fmt.Errorf("all eligible=%t oracle=%t", state.AllEligible, r.oracle.all)
	}
	for key := range r.oracle.selected {
		if state.SelectedWorkspaces[key] == "" {
			return fmt.Errorf("selected workspace %q missing", key)
		}
	}
	for key := range state.SelectedWorkspaces {
		if !r.oracle.selected[key] {
			return fmt.Errorf("unexpected selected workspace %q", key)
		}
	}
	if state.Acceptance.SourceEpoch != r.oracle.epoch || state.Acceptance.Revision != r.oracle.revision {
		return fmt.Errorf("accepted authority=(%s,%d) oracle=(%s,%d)", state.Acceptance.SourceEpoch, state.Acceptance.Revision, r.oracle.epoch, r.oracle.revision)
	}
	if len(state.ClosedByUser) != len(r.oracle.drops) {
		return fmt.Errorf("session drops=%d oracle=%d production_undo=%+v oracle_undo=%+v production_drops=%+v oracle_drops=%+v", len(state.ClosedByUser), len(r.oracle.drops), state.Undo, r.oracle.undo, state.ClosedByUser, r.oracle.drops)
	}
	for id, expected := range r.oracle.drops {
		actual, ok := state.ClosedByUser[id]
		if !ok || actual.AbsenceCount != expected.count || !actual.AbsenceSince.Equal(expected.since) || !actual.AbsenceDeadline.Equal(expected.deadline) {
			return fmt.Errorf("drop %s=%+v oracle=%+v", id, actual, expected)
		}
	}
	for id, expected := range r.oracle.sources {
		if expected.epoch != r.oracle.epoch {
			continue
		}
		actual, ok := state.Sources[id]
		if !ok || actual.Lifecycle != expected.lifecycle || actual.AbsenceCount != expected.absence || !actual.AbsenceSince.Equal(expected.since) || !actual.AbsenceDeadline.Equal(expected.deadline) {
			return fmt.Errorf("source %s=%+v oracle=%+v", id, actual, expected)
		}
	}

	seenEffects := map[string]bool{}
	for _, effect := range effects {
		key := string(effect.Kind) + ":" + effect.SourceID
		if seenEffects[key] {
			return fmt.Errorf("duplicate effect %s", key)
		}
		seenEffects[key] = true
		if effect.Kind == EffectLaunchProjection {
			actual := state.Sources[effect.SourceID]
			_, dropped := r.oracle.drops[actual.SessionID]
			picked := r.oracle.pickups[effect.SourceID]
			for pickedID := range r.oracle.pickups {
				picked = picked || r.oracle.sources[pickedID].session == actual.SessionID
			}
			desired := (r.oracle.all || r.oracle.selected[actual.WorkspaceKey] || picked) && !dropped
			eligible := actual.Lifecycle == SourceEligible || actual.Lifecycle == SourceGoneGrace
			if !desired || !eligible {
				return fmt.Errorf("launch for oracle-unwanted source %s desired=%t lifecycle=%s", effect.SourceID, desired, actual.Lifecycle)
			}
		}
		if effect.Kind == EffectApplySpatial && (effect.Proposal == nil || effect.Proposal.Target != slicelayout.Leech) {
			return errors.New("host-target or unscoped spatial effect")
		}
	}
	appIDs := map[string]string{}
	for id, projection := range state.Projections {
		if old := appIDs[projection.AppID]; old != "" && old != id {
			return fmt.Errorf("duplicate owned projection app id %s", projection.AppID)
		}
		appIDs[projection.AppID] = id
	}

	// An active recovery budget is an absolute deadline. Restarts, retries,
	// source observations, and cross-epoch unique successor handoffs may retain
	// it but may never extend it.
	beforeRecovery := map[string]Recovery{}
	for _, source := range pre.Sources {
		if source.Recovery != nil && source.SourceEpoch == pre.Acceptance.SourceEpoch {
			beforeRecovery[source.SessionID] = *source.Recovery
		}
	}
	for id, source := range state.Sources {
		if op.Kind == "reconnect" || source.SourceEpoch != state.Acceptance.SourceEpoch {
			continue
		}
		if prior, ok := beforeRecovery[source.SessionID]; ok && source.Recovery != nil {
			if source.Recovery.StartedAt != prior.StartedAt || source.Recovery.ExpiresAt != prior.ExpiresAt || source.Recovery.Generation != prior.Generation {
				return fmt.Errorf("recovery deadline reset for session %s", source.SessionID)
			}
			switch op.Kind {
			case "restart":
				r.witnesses.recoveryRestart++
			case "rotate":
				if _, sameID := pre.Sources[id]; !sameID {
					r.witnesses.recoverySuccessor++
				}
			}
			for _, effect := range effects {
				if effect.Kind == EffectLaunchProjection && effect.SourceID == id {
					r.witnesses.recoveryRetry++
				}
			}
		}
	}
	if len(pre.PendingCleanups) > 0 && op.Kind != "cleanup" {
		for _, effect := range effects {
			if effect.Kind == EffectLaunchProjection {
				return errors.New("cleanup gate allowed a launch")
			}
		}
	}
	if len(state.PendingCleanups) > 0 && op.Kind != "cleanup" {
		launched := false
		for _, effect := range effects {
			launched = launched || effect.Kind == EffectLaunchProjection
		}
		if launched {
			return errors.New("cleanup barrier allowed a launch")
		}
		r.witnesses.cleanupBlockedLaunch++
	}
	for id, next := range state.Sources {
		prior, existed := pre.Sources[id]
		if existed && prior.Connection != ConnectionDisconnected && next.Connection == ConnectionDisconnected {
			if state.SuccessorGates[id].SessionID != next.SessionID || next.Recovery != nil {
				return fmt.Errorf("attachment exhaustion lacked stable successor gate for %s", id)
			}
			r.witnesses.attachmentExhaustion++
		}
	}
	if op.Kind != "reconnect" {
		for gateID, gate := range state.SuccessorGates {
			blocked := true
			for _, effect := range effects {
				next := state.Sources[effect.SourceID]
				if effect.Kind == EffectLaunchProjection && next.SessionID == gate.SessionID {
					blocked = false
					break
				}
			}
			if !blocked {
				return fmt.Errorf("successor gate %s allowed automatic launch", gateID)
			}
			r.witnesses.successorBlockedLaunch++
		}
	}
	return nil
}

func (r *modelRun) assertMandatoryWitnesses() error {
	w := r.witnesses
	missing := []string{}
	for name, count := range map[string]int{
		"undo target": w.undoTarget, "undo no-target": w.undoNoTarget,
		"recovery retry retention": w.recoveryRetry, "recovery restart retention": w.recoveryRestart,
		"recovery unique-successor retention": w.recoverySuccessor,
		"conflicted recovery expiration":      w.conflictedExpiration, "undesired recovery expiration": w.undesiredExpiration,
		"attachment loss":       w.attachmentLoss,
		"attachment exhaustion": w.attachmentExhaustion, "cleanup blocks launch": w.cleanupBlockedLaunch,
		"cleanup blocks reconnect": w.cleanupBlockedReconnect, "successor blocks launch": w.successorBlockedLaunch,
		"explicit reconnect": w.reconnectSuccess, "process-loss evidence": w.processLoss,
		"monotonic handoff": w.handoffMonotonic,
	} {
		if count == 0 {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("mandatory model milestones missing: %s; witnesses=%+v", strings.Join(missing, ", "), w)
	}
	return nil
}

func controllerPrefix() []modelOp {
	return []modelOp{
		{Kind: "undo", Outcome: "no_target"},
		{Kind: "complete"}, {Kind: "all", Enabled: true}, {Kind: "all", Enabled: false},
		{Kind: "select", Enabled: true}, {Kind: "undo", Outcome: "target"},
		{Kind: "cleanup"}, {Kind: "select", Enabled: true},
		{Kind: "connect", Slot: 0, Required: true}, {Kind: "attachment_loss", Slot: 0, Required: true},
		{Kind: "restart", Required: true}, {Kind: "cleanup"}, {Kind: "source_conflict"},
		{Kind: "advance", Millis: 1000}, {Kind: "complete"}, {Kind: "rotate", Required: true}, {Kind: "replay"},
		{Kind: "reconnect", Slot: 0, Outcome: "cleanup_error"}, {Kind: "cleanup", Required: true},
		{Kind: "process_loss", Slot: 1, Required: true}, {Kind: "advance", Millis: 2100},
		{Kind: "connect", Slot: 0, Required: true}, {Kind: "attachment_loss", Slot: 0, Required: true},
		{Kind: "cleanup", Required: true}, {Kind: "expire_undesired", Slot: 0, Required: true}, {Kind: "select", Enabled: true},
		{Kind: "rotate", Required: true}, {Kind: "cleanup", Required: true}, {Kind: "reconnect", Slot: 0, Required: true},
		{Kind: "advance", Millis: 1000}, {Kind: "connect", Slot: 0, Required: true},
		{Kind: "handoff_pending", Token: 1}, {Kind: "handoff_failed", Token: 1}, {Kind: "handoff_regress", Token: 1},
		{Kind: "attachment_loss", Slot: 0, Required: true}, {Kind: "cleanup", Required: true},
		{Kind: "source_conflict"}, {Kind: "expire_conflicted", Slot: 0, Required: true}, {Kind: "complete"},
		{Kind: "duplicate"}, {Kind: "degraded"}, {Kind: "stale"}, {Kind: "conflict"},
		{Kind: "source_conflict"}, {Kind: "complete"}, {Kind: "order"},
		{Kind: "close", Slot: 0}, {Kind: "headless"}, {Kind: "absent"},
		{Kind: "duplicate"}, {Kind: "degraded"}, {Kind: "conflict"}, {Kind: "absent"},
		{Kind: "advance", Millis: 5100}, {Kind: "reopen", Slot: 0}, {Kind: "undo"},
		{Kind: "pickup", Slot: 1, Enabled: true}, {Kind: "pickup", Slot: 1, Enabled: false}, {Kind: "undo"},
	}
}

func generatedControllerOps(seed int64, count int) []modelOp {
	rng := rand.New(rand.NewSource(seed))
	kinds := []string{"complete", "complete", "headless", "absent", "source_conflict", "order", "rotate", "replay", "degraded", "duplicate", "stale", "conflict", "select", "all", "close", "reopen", "launch_fail", "connect", "attachment_loss", "process_loss", "cleanup", "advance", "restart", "reconnect", "handoff"}
	ops := append([]modelOp(nil), controllerPrefix()...)
	for len(ops) < count {
		kind := kinds[rng.Intn(len(kinds))]
		if kind == "handoff" {
			token := 100 + len(ops)
			ops = append(ops, modelOp{Kind: "handoff_pending", Token: token}, modelOp{Kind: "handoff_failed", Token: token}, modelOp{Kind: "handoff_regress", Token: token})
			continue
		}
		op := modelOp{Kind: kind, Slot: rng.Intn(2), Enabled: rng.Intn(2) == 0}
		if kind == "advance" {
			op.Millis = []int{100, 900, 1000, 1100, 2100, 5100, 11000}[rng.Intn(7)]
		}
		ops = append(ops, op)
	}
	return ops
}

func TestModelControllerGeneratedSequences(t *testing.T) {
	seeds := []int64{1, 7, 19, 42, 0x5eed, 0xdeadbeef, 0x600dface, 0x7fffffffffffffed}
	if raw := os.Getenv("TERMINAL_REDEEMER_MODEL_SEED"); raw != "" {
		seed, err := strconv.ParseInt(raw, 0, 64)
		if err != nil {
			t.Fatalf("TERMINAL_REDEEMER_MODEL_SEED: %v", err)
		}
		seeds = []int64{seed}
	}
	steps := 280
	if raw := os.Getenv("TERMINAL_REDEEMER_MODEL_STEPS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < len(controllerPrefix()) || value > 10000 {
			t.Fatalf("TERMINAL_REDEEMER_MODEL_STEPS must be within [%d,10000]", len(controllerPrefix()))
		}
		steps = value
	}
	for _, seed := range seeds {
		seed := seed
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			run, err := newModelRun(t)
			if err != nil {
				t.Fatalf("seed=%d setup error=%v", seed, err)
			}
			ops := generatedControllerOps(seed, steps)
			for index, op := range ops {
				pre, err := run.engine.Status()
				if err != nil {
					replay, _ := json.Marshal(ops[:index+1])
					t.Fatalf("seed=%d step=%d status error=%v operations=%s", seed, index, err, replay)
				}
				effects, applyErr := run.apply(op)
				if applyErr == nil {
					applyErr = run.check(pre, op, effects)
				}
				if applyErr != nil {
					replay, _ := json.Marshal(ops[:index+1])
					t.Fatalf("seed=%d step=%d operation=%+v error=%v\nreplay: TERMINAL_REDEEMER_MODEL_SEED=%d TERMINAL_REDEEMER_MODEL_STEPS=%d go test ./internal/slicecontroller -run '^TestModelControllerGeneratedSequences$'\noperations=%s", seed, index, op, applyErr, seed, index+1, replay)
				}
			}
			if err := run.assertMandatoryWitnesses(); err != nil {
				replay, _ := json.Marshal(ops)
				t.Fatalf("seed=%d end assertion: %v\noperations=%s", seed, err, replay)
			}
			t.Logf("seed=%d mandatory_witnesses=%+v", seed, run.witnesses)
		})
	}
}

func TestModelHostLocationGeneratedConvergence(t *testing.T) {
	seeds := []int64{3, 11, 29, 101, 0x51a7e}
	for _, seed := range seeds {
		seed := seed
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			hostWorkspace, leechWorkspace := "Dev", "Dev"
			hostMode, leechMode := slicelayout.Tiled, slicelayout.Tiled
			hostWidth, hostHeight, leechWidth, leechHeight := 50, 50, 50, 50
			hostOrder := sliceprotocol.Position{Column: 1, Tile: 1}
			leechOrder := hostOrder
			var operations []string
			for step := 0; step < 160; step++ {
				operation := rng.Intn(7)
				switch operation {
				case 0:
					hostWorkspace = []string{"Dev", "Ops"}[rng.Intn(2)]
				case 1:
					hostMode = []slicelayout.LayoutMode{slicelayout.Tiled, slicelayout.Floating}[rng.Intn(2)]
				case 2:
					hostWidth = 20 + rng.Intn(71)
				case 3:
					hostHeight = 20 + rng.Intn(71)
				case 4:
					leechWorkspace, leechMode = []string{"Dev", "Ops"}[rng.Intn(2)], []slicelayout.LayoutMode{slicelayout.Tiled, slicelayout.Floating}[rng.Intn(2)]
					leechWidth, leechHeight = 20+rng.Intn(71), 20+rng.Intn(71)
				case 5:
					hostOrder = sliceprotocol.Position{Column: 1 + rng.Intn(3), Tile: 1 + rng.Intn(3)}
				case 6:
					leechOrder = sliceprotocol.Position{Column: 1 + rng.Intn(3), Tile: 1 + rng.Intn(3)}
				}
				operations = append(operations, fmt.Sprintf("kind=%d host_workspace=%s host_mode=%s host_size=%dx%d host_order=%d/%d leech_workspace=%s leech_mode=%s leech_size=%dx%d leech_order=%d/%d", operation, hostWorkspace, hostMode, hostWidth, hostHeight, hostOrder.Column, hostOrder.Tile, leechWorkspace, leechMode, leechWidth, leechHeight, leechOrder.Column, leechOrder.Tile))
				host := modelLayoutObservation("source-model", "host-epoch", 10, hostWorkspace, hostMode, hostWidth, hostHeight, hostOrder)
				leech := modelLayoutObservation("source-model", "leech-epoch", 20, leechWorkspace, leechMode, leechWidth, leechHeight, leechOrder)
				input := slicelayout.Input{ControllerID: "model-controller", Generation: uint64(step + 1), Host: host, Leech: &leech, HostWorkspaces: modelLayoutWorkspaces(), LeechWorkspaces: modelLayoutWorkspaces(), Ownership: slicelayout.Ownership{SourceID: host.SourceID, HostCompositorEpoch: host.SourceEpoch, LeechCompositorEpoch: leech.SourceEpoch, HostRuntimeWindowID: host.RuntimeWindowID, LeechRuntimeWindowID: leech.RuntimeWindowID, ProjectionPositivelyOwned: true}}
				result := slicelayout.Plan(input)
				want := map[slicelayout.ChangeKind]bool{}
				if strings.ToLower(hostWorkspace) != strings.ToLower(leechWorkspace) {
					want[slicelayout.ChangeWorkspace] = true
				}
				if hostMode != leechMode {
					want[slicelayout.ChangeLayoutMode] = true
				}
				if hostWidth != leechWidth {
					want[slicelayout.ChangeWidth] = true
				}
				if hostHeight != leechHeight {
					want[slicelayout.ChangeHeight] = true
				}
				got := map[slicelayout.ChangeKind]bool{}
				for _, proposal := range result.Proposals {
					if proposal.Target != slicelayout.Leech || proposal.Focus || !proposal.VerifyAfterWrite {
						failSpatialModel(t, seed, step, operations, fmt.Errorf("unsafe proposal %+v", proposal))
					}
					for _, change := range proposal.Changes {
						if got[change.Kind] {
							failSpatialModel(t, seed, step, operations, fmt.Errorf("duplicate change %s", change.Kind))
						}
						got[change.Kind] = true
					}
				}
				if !reflect.DeepEqual(got, want) {
					failSpatialModel(t, seed, step, operations, fmt.Errorf("changes=%v oracle=%v result=%+v", got, want, result))
				}
				orderDiffers := (hostMode == slicelayout.Tiled) != (leechMode == slicelayout.Tiled) || (hostMode == slicelayout.Tiled && leechMode == slicelayout.Tiled && hostOrder != leechOrder)
				if (len(result.OrderDrift) != 0) != orderDiffers {
					failSpatialModel(t, seed, step, operations, fmt.Errorf("order drift=%+v expected=%t", result.OrderDrift, orderDiffers))
				}
				// Apply every supported host property. Tiled order is deliberately
				// report-only and is never copied by a proposal.
				if len(result.Proposals) > 0 {
					leechWorkspace, leechMode, leechWidth, leechHeight = hostWorkspace, hostMode, hostWidth, hostHeight
				}
			}
		})
	}
}

func modelLayoutObservation(source, epoch string, runtime uint64, workspace string, mode slicelayout.LayoutMode, width, height int, order sliceprotocol.Position) slicelayout.Observation {
	key, _ := sliceprotocol.NormalizeWorkspaceName(workspace)
	ob := slicelayout.Observation{Quality: slicelayout.Complete, SourceID: source, SourceEpoch: epoch, RuntimeWindowID: runtime, Output: sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1000, LogicalHeight: 1000, Scale: 1, Transform: "normal"}, Workspace: slicelayout.Workspace{RuntimeID: 1, Name: workspace, Key: key}, Mode: mode, WindowWidth: width * 10, WindowHeight: height * 10}
	if mode == slicelayout.Tiled {
		copy := order
		ob.Order = &copy
	}
	return ob
}

func modelLayoutWorkspaces() []slicelayout.Workspace {
	return []slicelayout.Workspace{{RuntimeID: 1, Name: "Dev", Key: "dev"}, {RuntimeID: 2, Name: "Ops", Key: "ops"}}
}

func failSpatialModel(t *testing.T, seed int64, step int, operations []string, err error) {
	t.Helper()
	payload, _ := json.Marshal(operations)
	t.Fatalf("seed=%d step=%d error=%v\nreplay operations=%s", seed, step, err, payload)
}
