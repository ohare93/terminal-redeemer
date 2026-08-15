//go:build linux

package hostleechsoak_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceattach"
	"github.com/jmo/terminal-redeemer/internal/slicecontroller"
	"github.com/jmo/terminal-redeemer/internal/slicelaunch"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

const (
	soakHostID = "10000000-0000-4000-8000-000000000001"
	soakKitty  = "/nix/store/terminal-redeemer-soak/bin/kitty"
)

type summary struct {
	Observations map[string]int
	Churn        map[string]int
	Effects      map[string]int
	Caps         map[string]metric
	Resources    map[string]int
	Restarts     int
}

type metric struct {
	Observed int
	Limit    int
}

type soak struct {
	t             *testing.T
	root          string
	now           time.Time
	engine        *slicecontroller.Engine
	store         *slicecontroller.Store
	launchStore   *slicelaunch.Store
	tokenStore    *slicerpc.TokenStore
	config        slicecontroller.ControllerConfig
	local         map[string]slicecontroller.OwnedWindow
	active        map[string]bool
	nextPID       int
	nextWindow    uint64
	summary       summary
	maxima        map[string]int
	createdTokens []string
}

type soakHostTransaction struct {
	session, plan, prepare, kitty, placement, cleanup, proof int
}

func (tx *soakHostTransaction) EnsureSession(context.Context, slicerpc.TokenRecord) (bool, error) {
	tx.session++
	return false, nil
}
func (tx *soakHostTransaction) PlanKitty(_ context.Context, record slicerpc.TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	tx.plan++
	return sliceattach.ExactSocketIdentity{Path: filepath.Join("/tmp", "tr-soak-routed-"+record.Token[:12]), SocketDevice: 10, SocketInode: 20}, nil
}
func (tx *soakHostTransaction) PrepareKitty(_ context.Context, record slicerpc.TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	tx.prepare++
	return sliceattach.ExactSocketIdentity{Path: record.PreparedSocketPath, SocketDevice: record.PreparedSocketDevice, SocketInode: record.PreparedSocketInode, MarkerDevice: 10, MarkerInode: 30}, nil
}
func (tx *soakHostTransaction) EnsureKitty(context.Context, slicerpc.TokenRecord) (int, uint64, bool, error) {
	tx.kitty++
	return 4321, 77, false, nil
}
func (tx *soakHostTransaction) Place(context.Context, slicerpc.TokenRecord, uint64) error {
	tx.placement++
	return nil
}
func (tx *soakHostTransaction) CleanupKitty(context.Context, slicerpc.TokenRecord) error {
	tx.cleanup++
	return nil
}

type soakNiri struct{ state niriipc.State }

func (n *soakNiri) Snapshot(context.Context) (niriipc.State, error) { return n.state, nil }
func (*soakNiri) Action(context.Context, any) error                 { return nil }

type soakRPCRemote struct {
	server         *slicerpc.Server
	loseNext       bool
	interruptSleep bool
	calls          int
}

func (remote *soakRPCRemote) Call(ctx context.Context, request slicerpc.Request) (slicerpc.Response, error) {
	remote.calls++
	response := remote.server.Handle(ctx, request)
	if remote.loseNext {
		remote.loseNext = false
		return slicerpc.Response{}, errors.New("injected response loss after durable host commit")
	}
	return response, nil
}

type soakWorkspace string

func (workspace soakWorkspace) Current(context.Context) (string, error) {
	return string(workspace), nil
}

type soakSelection bool

func (selected soakSelection) Selected(string) (bool, error) { return bool(selected), nil }

type soakLocal struct{ calls *int }

func (local soakLocal) Launch(context.Context) error { *local.calls++; return nil }

type soakHandoff struct{ engine *slicecontroller.Engine }

func (handoff soakHandoff) Send(_ context.Context, intent slicelaunch.Intent) error {
	status := "launch_pending"
	switch intent.Status {
	case slicelaunch.IntentLaunched:
		status = "launched"
	case slicelaunch.IntentFailed:
		status = "not_created"
	}
	_, err := handoff.engine.SetLaunchHandoff(slicecontroller.LaunchHandoff{
		Token: intent.Token, Status: status, HostTerminalID: intent.HostTerminalID,
		SessionName: intent.SessionName, WorkspaceName: intent.WorkspaceName,
		SourceID: intent.SourceID, SourceEpoch: intent.SourceEpoch, RuntimeWindowID: intent.RuntimeWindowID,
	})
	return err
}

func TestSoakChildHelper(t *testing.T) {
	if os.Getenv("TERMINAL_REDEEMER_SOAK_CHILD") != "1" {
		return
	}
	os.Exit(0)
}

func TestBoundedHostLeechSoak(t *testing.T) {
	iterations := 2000
	if raw := os.Getenv("TERMINAL_REDEEMER_SOAK_ITERATIONS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2000 || parsed > 50000 {
			t.Fatalf("TERMINAL_REDEEMER_SOAK_ITERATIONS must be within [2000,50000]")
		}
		iterations = parsed
	}
	root, err := os.MkdirTemp("/tmp", "tr-soak-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}

	s := newSoak(t, root)
	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs := countFDs(t)
	baselineChildren := countChildren(t)

	state, effects, err := s.engine.SelectWorkspace("Work", true)
	if err != nil {
		t.Fatal(err)
	}
	s.handleEffects(effects)
	s.observeCaps(state)

	epochIndex := 1
	epoch := soakUUID(epochIndex)
	revision := uint64(0)
	var current *sliceprotocol.Authoritative
	var retired *sliceprotocol.Authoritative

	for i := 1; i <= iterations; i++ {
		s.now = s.now.Add(25 * time.Millisecond)
		var envelope sliceprotocol.Envelope
		kind := "complete"
		switch {
		case current == nil || i%5 == 0:
			if i > 1 && i%500 == 0 {
				if current != nil {
					copy := cloneAuthority(*current)
					retired = &copy
				}
				epochIndex++
				epoch = soakUUID(epochIndex)
				revision = 0
				s.summary.Churn["epoch_rotations"]++
			}
			revision++
			authority := makeAuthority(epoch, revision, i, s.now)
			current = &authority
			envelope = completeEnvelope(authority, s.now)
		case i%5 == 1:
			kind = "degraded"
			envelope = degradedEnvelope(s.now)
		case i%5 == 2:
			kind = "duplicate"
			envelope = completeEnvelope(*current, s.now)
		case i%5 == 3:
			kind = "conflict"
			conflicted := cloneAuthority(*current)
			conflicted.LiveSessionIDs = append(conflicted.LiveSessionIDs, opaqueID("ses_", 100000+i))
			envelope = completeEnvelope(conflicted, s.now)
		default:
			if retired != nil && i%10 == 9 {
				kind = "replay"
				envelope = completeEnvelope(*retired, s.now)
			} else if current.Revision > 1 {
				kind = "stale"
				stale := cloneAuthority(*current)
				stale.Revision--
				envelope = completeEnvelope(stale, s.now)
			} else {
				kind = "duplicate"
				envelope = completeEnvelope(*current, s.now)
			}
		}
		state, effects, decision, err := s.engine.ApplyEnvelope(envelope, s.now)
		if err != nil {
			t.Fatalf("iteration %d %s observation: %v", i, kind, err)
		}
		s.summary.Observations[string(decision)]++
		s.summary.Churn["observation_events"]++
		s.handleEffects(effects)
		s.observeCaps(state)

		if i%157 == 0 {
			state, effects, err = s.engine.SelectWorkspace("Work", false)
			if err != nil {
				t.Fatalf("deselect: %v", err)
			}
			s.handleEffects(effects)
			state, effects, err = s.engine.SelectWorkspace("Work", true)
			if err != nil {
				t.Fatalf("reselect: %v", err)
			}
			s.handleEffects(effects)
			s.summary.Churn["selection_changes"] += 2
			s.observeCaps(state)
		}

		if i%211 == 0 {
			state, effects, err = s.engine.SelectAll(true)
			if err != nil {
				t.Fatalf("enable all: %v", err)
			}
			s.handleEffects(effects)
			s.observeCaps(state)
			state, effects, err = s.engine.SelectAll(false)
			if err != nil {
				t.Fatalf("disable all: %v", err)
			}
			s.handleEffects(effects)
			s.observeCaps(state)
			s.summary.Churn["all_eligible_changes"] += 2
		}

		if sourceID := s.currentSource(); sourceID != "" {
			if i%101 == 0 {
				state, effects, err = s.engine.Pickup(sourceID, true)
				if err == nil {
					s.handleEffects(effects)
					s.summary.Churn["pickups"]++
					s.observeCaps(state)
				}
			}
			if i%131 == 0 {
				state, effects, err = s.engine.Close(sourceID)
				if err == nil {
					s.handleEffects(effects)
					s.summary.Churn["drops"]++
					s.observeCaps(state)
					state, effects, err = s.engine.Reopen(sourceID)
					if err != nil {
						t.Fatalf("reopen: %v", err)
					}
					s.handleEffects(effects)
					s.summary.Churn["reopens"]++
					s.observeCaps(state)
				}
			}
			if i%503 == 0 && s.active[sourceID] {
				s.exerciseRecovery(sourceID)
			}
		}

		if i%6 == 0 {
			s.exerciseRoutedIntent()
		}
		if i%100 == 0 {
			s.exercisePreparedNamespace(i)
		}
		if i%125 == 0 {
			s.spawnChild()
		}
		if i%333 == 0 {
			s.restart()
		}
	}

	s.exerciseRoutedRestartReplay()
	s.exerciseHandoffCompaction()
	final, err := s.engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	s.observeCaps(final)
	if err := final.Validate(); err != nil {
		t.Fatalf("final controller authority invalid: %v", err)
	}
	if len(final.LaunchHandoffs)+len(final.RetiredHandoffTokens) != len(s.createdTokens) {
		t.Fatalf("handoff/tombstone authority lost: live=%d retired=%d created=%d", len(final.LaunchHandoffs), len(final.RetiredHandoffTokens), len(s.createdTokens))
	}
	for _, token := range final.RetiredHandoffTokens {
		if !contains(s.createdTokens, token) {
			t.Fatalf("non-exact handoff tombstone appeared")
		}
	}

	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	endGoroutines := runtime.NumGoroutine()
	endFDs := countFDs(t)
	endChildren := countChildren(t)
	if endGoroutines > baselineGoroutines+4 {
		t.Fatalf("goroutine growth: before=%d after=%d", baselineGoroutines, endGoroutines)
	}
	if endFDs > baselineFDs+2 {
		t.Fatalf("fd growth: before=%d after=%d", baselineFDs, endFDs)
	}
	if endChildren != baselineChildren {
		t.Fatalf("child process leak: before=%d after=%d", baselineChildren, endChildren)
	}
	if remaining := countPrefixDirs(filepath.Join(root, "attach-private"), "att-"); remaining != 0 {
		t.Fatalf("prepared namespace leak: %d", remaining)
	}
	if remaining := countPrefixDirs(filepath.Join(root, "shim-caches"), "cache-"); remaining != 0 {
		t.Fatalf("shim cache leak: %d", remaining)
	}

	s.summary.Resources["goroutine_delta"] = endGoroutines - baselineGoroutines
	s.summary.Resources["fd_delta"] = endFDs - baselineFDs
	s.summary.Resources["child_processes_remaining"] = endChildren - baselineChildren
	s.summary.Resources["prepared_namespaces_remaining"] = 0
	s.summary.Resources["temporary_caches_remaining"] = 0
	s.summary.Caps = map[string]metric{
		"sources":                     {s.maxima["sources"], slicecontroller.MaxTerminalSources},
		"projections":                 {s.maxima["projections"], slicecontroller.MaxTerminalSources},
		"session_drops":               {s.maxima["session_drops"], slicecontroller.MaxTerminalSources},
		"selected_workspaces":         {s.maxima["selected_workspaces"], slicecontroller.MaxSelectedWorkspaces},
		"successor_gates":             {s.maxima["successor_gates"], slicecontroller.MaxSuccessorGates},
		"pending_cleanups":            {s.maxima["pending_cleanups"], slicecontroller.MaxSuccessorGates},
		"pickups":                     {s.maxima["pickups"], slicecontroller.MaxTerminalSources},
		"lineage":                     {s.maxima["lineage"], slicecontroller.MaxLineageRecords},
		"launch_handoffs":             {s.maxima["launch_handoffs"], slicecontroller.MaxLaunchHandoffs},
		"handoff_tombstones":          {s.maxima["handoff_tombstones"], slicecontroller.MaxRetiredHandoffTombstones},
		"spatial_records":             {s.maxima["spatial_records"], slicecontroller.MaxSpatialRecords},
		"audit":                       {s.maxima["audit"], slicecontroller.MaxAuditEntries},
		"undo":                        {s.maxima["undo"], slicecontroller.MaxUndoEntries},
		"retired_epoch_tombstones":    {s.maxima["retired_epochs"], sliceprotocol.MaxRetiredEpochTombstones},
		"routed_intent_files":         {countJSONFiles(t, filepath.Join(root, "leech", "slice", "launch", "intents")), slicelaunch.MaxIntentFiles},
		"host_token_journal_records":  {countTokenRecords(t, s.tokenStore.Root()), slicerpc.MaxTokenRecords},
		"host_session_creates":        {s.summary.Effects["host_session_create"], 1},
		"host_kitty_starts":           {s.summary.Effects["host_kitty_start"], 1},
		"host_placements":             {s.summary.Effects["host_placement"], 1},
		"host_source_commits":         {s.summary.Effects["host_source_commit"], 1},
		"routed_projection_launches":  {s.summary.Effects["routed_local_projection_launch"], 1},
		"routed_transport_attempts":   {s.summary.Effects["routed_transport_attempts"], 2},
		"controller_state_bytes":      {fileSize(t, filepath.Join(s.store.Root(), "current.json")), slicecontroller.MaxControllerStateBytes},
		"projection_argv_entries":     {s.maxima["projection_argv_entries"], slicecontroller.MaxProjectionArgvEntries},
		"projection_argv_entry_bytes": {s.maxima["projection_argv_entry_bytes"], slicecontroller.MaxProjectionArgvEntryBytes},
		"projection_argv_total_bytes": {s.maxima["projection_argv_total_bytes"], slicecontroller.MaxProjectionArgvTotalBytes},
	}
	for name, value := range s.summary.Caps {
		if value.Observed > value.Limit {
			t.Fatalf("%s cap exceeded: %+v", name, value)
		}
	}
	if s.summary.Effects["duplicate_active_projection"] != 0 || s.summary.Effects["host_target_spatial"] != 0 {
		t.Fatalf("unsafe duplicate/host effect reached: %+v", s.summary.Effects)
	}
	for _, name := range []string{"host_session_create", "host_kitty_start", "host_placement", "host_source_commit", "routed_local_projection_launch"} {
		if s.summary.Effects[name] != 1 {
			t.Fatalf("routed retry/replay cardinality %s=%d, want 1", name, s.summary.Effects[name])
		}
	}
	if got := s.summary.Effects["routed_transport_attempts"]; got != 2 {
		t.Fatalf("routed retry/replay transport attempts=%d, want 2", got)
	}
}

func newSoak(t *testing.T, root string) *soak {
	t.Helper()
	now := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	controllerRoot := filepath.Join(root, "controller")
	if err := os.Mkdir(controllerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := slicecontroller.NewStore(controllerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Initialize(slicecontroller.Namespace{Host: soakHostID, Leech: "20000000-0000-4000-8000-000000000002"}); err != nil {
		t.Fatal(err)
	}
	launchRoot := filepath.Join(root, "leech")
	if err := os.Mkdir(launchRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	launchStore, err := slicelaunch.NewStore(launchRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launchStore.Mode(false); err != nil {
		t.Fatal(err)
	}
	tokenRoot := filepath.Join(root, "host")
	if err := os.Mkdir(tokenRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenStore, err := slicerpc.NewTokenStore(tokenRoot)
	if err != nil {
		t.Fatal(err)
	}
	cfg := slicecontroller.ControllerConfig{
		Namespace:               slicecontroller.Namespace{Host: soakHostID, Leech: "20000000-0000-4000-8000-000000000002"},
		RetryWindow:             5 * time.Second,
		RetryInitialBackoff:     100 * time.Millisecond,
		RetryMaxBackoff:         400 * time.Millisecond,
		RetryMaxAttempts:        4,
		SourceGoneGrace:         250 * time.Millisecond,
		SourceGoneConfirmations: 2,
	}
	s := &soak{
		t:           t,
		root:        root,
		now:         now,
		store:       store,
		launchStore: launchStore,
		tokenStore:  tokenStore,
		config:      cfg,
		local:       map[string]slicecontroller.OwnedWindow{},
		active:      map[string]bool{},
		nextPID:     20000,
		nextWindow:  50000,
		maxima:      map[string]int{},
		summary: summary{
			Observations: map[string]int{},
			Churn:        map[string]int{},
			Effects:      map[string]int{},
			Resources:    map[string]int{},
		},
	}
	s.engine = s.newEngine()
	return s
}

func (s *soak) newEngine() *slicecontroller.Engine {
	return &slicecontroller.Engine{Store: s.store, Config: s.config, Now: func() time.Time { return s.now }}
}

func (s *soak) restart() {
	before, err := s.engine.Status()
	if err != nil {
		s.t.Fatal(err)
	}
	recoveries := map[string]slicecontroller.Recovery{}
	for id, source := range before.Sources {
		if source.Recovery != nil {
			recoveries[id] = *source.Recovery
		}
	}
	s.engine = s.newEngine()
	after, err := s.engine.Status()
	if err != nil {
		s.t.Fatal(err)
	}
	for id, want := range recoveries {
		got := after.Sources[id].Recovery
		if got == nil || got.StartedAt != want.StartedAt || got.ExpiresAt != want.ExpiresAt || got.Attempt != want.Attempt || got.NextAttemptAt != want.NextAttemptAt {
			s.t.Fatalf("restart reset retry authority for %s: got=%+v want=%+v", id, got, want)
		}
	}
	s.summary.Restarts++
	s.summary.Churn["controller_restarts"]++
}

func (s *soak) handleEffects(effects []slicecontroller.Effect) {
	queue := append([]slicecontroller.Effect(nil), effects...)
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 256 {
			s.t.Fatal("effect reconciliation did not converge")
		}
		effect := queue[0]
		queue = queue[1:]
		switch effect.Kind {
		case slicecontroller.EffectLaunchProjection:
			s.summary.Effects["projection_launch"]++
			if s.active[effect.SourceID] {
				s.summary.Effects["duplicate_active_projection"]++
				s.t.Fatalf("duplicate active projection for %s", effect.SourceID)
			}
			argv := []string{soakKitty, "slice-projection", effect.SourceID, effect.Projection.AppID}
			if _, err := s.engine.PrepareProjection(effect.SourceID, soakKitty, argv); err != nil {
				s.t.Fatalf("prepare projection: %v", err)
			}
			s.nextPID++
			s.nextWindow++
			if _, err := s.engine.RecordLaunch(effect.SourceID, s.nextPID, nil); err != nil {
				s.t.Fatalf("record launch: %v", err)
			}
			window := slicecontroller.OwnedWindow{SourceID: effect.SourceID, WindowID: s.nextWindow, PID: s.nextPID, AppID: effect.Projection.AppID}
			s.local[effect.SourceID] = window
			s.active[effect.SourceID] = true
			state, more, err := s.engine.ObserveLocal(soakUUID(9000), s.localWindows())
			if err != nil {
				s.t.Fatalf("observe launched projection: %v", err)
			}
			queue = append(queue, more...)
			if _, ok := state.Projections[effect.SourceID]; ok {
				if _, err := s.engine.AttachmentConnected(effect.SourceID); err != nil {
					s.t.Fatalf("connect projection: %v", err)
				}
			}
		case slicecontroller.EffectCloseProjection:
			s.summary.Effects["projection_close"]++
			delete(s.local, effect.SourceID)
			delete(s.active, effect.SourceID)
			_, more, err := s.engine.ObserveLocal(soakUUID(9000), s.localWindows())
			if err != nil {
				s.t.Fatalf("observe closed projection: %v", err)
			}
			queue = append(queue, more...)
		case slicecontroller.EffectApplySpatial:
			s.summary.Effects["spatial"]++
			if effect.Proposal != nil && effect.Proposal.Target == "host" {
				s.summary.Effects["host_target_spatial"]++
			}
		default:
			s.t.Fatalf("unknown effect %q", effect.Kind)
		}
	}
}

func (s *soak) exerciseRecovery(sourceID string) {
	state, effects, err := s.engine.AttachmentLost(sourceID)
	if err != nil {
		s.t.Fatalf("attachment loss: %v", err)
	}
	s.handleEffects(effects)
	recovery := state.Sources[sourceID].Recovery
	if recovery == nil {
		s.t.Fatalf("attachment loss did not persist recovery")
	}
	want := *recovery
	s.restart()
	after, err := s.engine.Status()
	if err != nil {
		s.t.Fatal(err)
	}
	got := after.Sources[sourceID].Recovery
	if got == nil || got.StartedAt != want.StartedAt || got.ExpiresAt != want.ExpiresAt || got.Attempt != want.Attempt {
		s.t.Fatalf("recovery budget changed across restart")
	}
	s.now = got.NextAttemptAt.Add(time.Millisecond)
	state, effects, err = s.engine.Tick()
	if err != nil {
		s.t.Fatalf("retry tick: %v", err)
	}
	if r := state.Sources[sourceID].Recovery; r != nil && r.Attempt > s.config.RetryMaxAttempts {
		s.t.Fatalf("retry attempt exceeded cap")
	}
	s.handleEffects(effects)
	s.summary.Churn["reconnects"]++
}

func (s *soak) exerciseRoutedRestartReplay() {
	leechRoot := filepath.Join(s.root, "routed-restart-leech")
	hostRoot := filepath.Join(s.root, "routed-restart-host")
	controllerRoot := filepath.Join(s.root, "routed-restart-controller")
	for _, root := range []string{leechRoot, hostRoot, controllerRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			s.t.Fatal(err)
		}
	}
	launchStore, err := slicelaunch.NewStore(leechRoot)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err = launchStore.Mode(false); err != nil {
		s.t.Fatal(err)
	}
	if _, err = launchStore.SetMode(true); err != nil {
		s.t.Fatal(err)
	}
	tokenStore, err := slicerpc.NewTokenStore(hostRoot)
	if err != nil {
		s.t.Fatal(err)
	}
	controllerStore, err := slicecontroller.NewStore(controllerRoot)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err = controllerStore.Initialize(slicecontroller.Namespace{Host: soakHostID, Leech: "20000000-0000-4000-8000-000000000002"}); err != nil {
		s.t.Fatal(err)
	}
	controller := &slicecontroller.Engine{Store: controllerStore, Config: s.config, Now: func() time.Time { return s.now }}
	if _, _, err = controller.SelectWorkspace("Work", true); err != nil {
		s.t.Fatal(err)
	}

	tx := &soakHostTransaction{}
	epoch := "33333333-3333-4333-8333-333333333333"
	name := "Work"
	output := "DP-1"
	niri := &soakNiri{state: niriipc.State{Workspaces: []niriipc.Workspace{
		{ID: 3, Index: 1, Name: &name, Output: &output, IsActive: true, IsFocused: true},
		{ID: 4, Index: 2, Output: &output},
	}}}
	newServer := func(tokens *slicerpc.TokenStore) *slicerpc.Server {
		return &slicerpc.Server{
			SourceHostID: soakHostID, SourceEpoch: epoch, SourceFingerprint: strings.Repeat("a", 64),
			Tokens: tokens, HostTransaction: tx, Niri: niri, Now: func() time.Time { return s.now },
			ProveCommit: func(_ context.Context, record slicerpc.TokenRecord) (string, string, error) {
				tx.proof++
				id, deriveErr := sourceinventory.SourceID(epoch, record.NiriWindowID)
				return id, epoch, deriveErr
			},
		}
	}
	remote := &soakRPCRemote{server: newServer(tokenStore), loseNext: true, interruptSleep: true}
	localCalls := 0
	newRouter := func(store *slicelaunch.Store, rpc *soakRPCRemote) slicelaunch.Router {
		return slicelaunch.Router{
			Store: store, DefaultEnabled: true, Workspace: soakWorkspace("Work"), Selection: soakSelection(true),
			Local: soakLocal{calls: &localCalls}, Remote: rpc, Handoff: soakHandoff{engine: controller},
			RetryWindow: 5 * time.Second, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxAttempts: 3,
			Now: func() time.Time { return s.now }, Sleep: func(context.Context, time.Duration) error {
				if rpc.interruptSleep {
					rpc.interruptSleep = false
					return errors.New("deterministic router process interruption")
				}
				return nil
			},
		}
	}
	first, firstErr := newRouter(launchStore, remote).Route(context.Background())
	if firstErr == nil || first.Intent == nil || first.Intent.Status != slicelaunch.IntentPending || first.Intent.Attempt != 1 {
		s.t.Fatalf("lost routed response did not persist in-budget intent: result=%+v err=%v", first, firstErr)
	}
	beforeRestart := *first.Intent
	hostBefore, err := tokenStore.Read(beforeRestart.Token)
	if err != nil || hostBefore.Status != slicerpc.TokenLaunched || hostBefore.Stage != "committed" {
		s.t.Fatalf("host transaction did not commit before response loss: record=%+v err=%v", hostBefore, err)
	}

	// Reconstruct every durable owner. A read is evidence of restart stability,
	// not counted as replay; the later Router.Reconnect call must cross RPC.
	restartedLaunchStore, err := slicelaunch.NewStore(leechRoot)
	if err != nil {
		s.t.Fatal(err)
	}
	restartedTokenStore, err := slicerpc.NewTokenStore(hostRoot)
	if err != nil {
		s.t.Fatal(err)
	}
	restartedControllerStore, err := slicecontroller.NewStore(controllerRoot)
	if err != nil {
		s.t.Fatal(err)
	}
	controller = &slicecontroller.Engine{Store: restartedControllerStore, Config: s.config, Now: func() time.Time { return s.now }}
	restartedIntent, err := restartedLaunchStore.Read(beforeRestart.Token)
	if err != nil {
		s.t.Fatal(err)
	}
	if restartedIntent.Token != beforeRestart.Token || restartedIntent.SessionName != beforeRestart.SessionName || restartedIntent.RetryExpiresAt != beforeRestart.RetryExpiresAt || restartedIntent.Attempt != beforeRestart.Attempt {
		s.t.Fatalf("routed retry authority changed across store/router restart: before=%+v after=%+v", beforeRestart, restartedIntent)
	}
	hostAfterRestart, err := restartedTokenStore.Read(beforeRestart.Token)
	if err != nil || hostAfterRestart.Token != hostBefore.Token || hostAfterRestart.SessionName != hostBefore.SessionName || hostAfterRestart.Stage != hostBefore.Stage || hostAfterRestart.Status != hostBefore.Status {
		s.t.Fatalf("host journal changed across reconstruction: before=%+v after=%+v err=%v", hostBefore, hostAfterRestart, err)
	}

	s.now = s.now.Add(time.Second)
	replayRemote := &soakRPCRemote{server: newServer(restartedTokenStore)}
	second, err := newRouter(restartedLaunchStore, replayRemote).Reconnect(context.Background(), beforeRestart.Token)
	if err != nil || second.Intent == nil || second.Intent.Status != slicelaunch.IntentLaunched {
		s.t.Fatalf("production same-token reconnect/replay failed: result=%+v err=%v", second, err)
	}
	if second.Intent.Token != beforeRestart.Token || second.Intent.SessionName != beforeRestart.SessionName {
		s.t.Fatalf("reconnect minted routed identity: before=%+v after=%+v", beforeRestart, second.Intent)
	}
	if second.Intent.RetryExpiresAt != beforeRestart.RetryExpiresAt || second.Intent.Attempt != 2 || second.Intent.Attempt > 3 {
		s.t.Fatalf("restart reset absolute deadline/attempt budget: before=%+v after=%+v", beforeRestart, second.Intent)
	}
	if remote.calls != 1 || replayRemote.calls != 1 || localCalls != 0 {
		s.t.Fatalf("routed calls/fallback mismatch: initial=%d replay=%d local=%d", remote.calls, replayRemote.calls, localCalls)
	}
	if tx.session != 1 || tx.plan != 1 || tx.prepare != 1 || tx.kitty != 1 || tx.placement != 1 || tx.cleanup != 0 || tx.proof != 2 {
		s.t.Fatalf("duplicate or missing host effects across replay: %+v", tx)
	}

	// Publish the committed source once through the real controller. Duplicate
	// and higher-revision same-semantic authority must not create another local
	// projection effect.
	sessionID := opaqueID("ses_", 777777)
	key, _ := sliceprotocol.NormalizeWorkspaceName("Work")
	authority := sliceprotocol.Authoritative{
		SourceEpoch: epoch, Revision: 1, ObservedAt: s.now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization,
		LiveSessionIDs: []string{sessionID}, Conflicts: []sliceprotocol.Conflict{},
		Sources: []sliceprotocol.Source{{
			SourceID: second.Intent.SourceID, RuntimeWindowID: second.Intent.RuntimeWindowID,
			Session:   sliceprotocol.Session{ID: sessionID, Name: second.Intent.SessionName, Status: "active"},
			Workspace: sliceprotocol.Workspace{RuntimeID: 3, Name: "Work", Key: key},
			Output:    &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"},
			Layout:    sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: 1, Tile: 1}, TileWidth: 0.5, TileHeight: 0.5, WindowWidth: 960, WindowHeight: 540},
		}},
	}
	_, launchEffects, decision, err := controller.ApplyEnvelope(completeEnvelope(authority, s.now), s.now)
	if err != nil || decision != sliceprotocol.DecisionAccepted {
		s.t.Fatalf("routed source publication failed: decision=%s effects=%+v err=%v", decision, launchEffects, err)
	}
	launchCount := countEffectKind(launchEffects, slicecontroller.EffectLaunchProjection)
	_, duplicateEffects, duplicateDecision, err := controller.ApplyEnvelope(completeEnvelope(authority, s.now.Add(time.Millisecond)), s.now.Add(time.Millisecond))
	if err != nil || duplicateDecision != sliceprotocol.DecisionDuplicate {
		s.t.Fatalf("duplicate routed source publication failed: decision=%s err=%v", duplicateDecision, err)
	}
	authority.Revision++
	authority.ObservedAt = s.now.Add(2 * time.Millisecond)
	_, repeatedEffects, repeatedDecision, err := controller.ApplyEnvelope(completeEnvelope(authority, authority.ObservedAt), authority.ObservedAt)
	if err != nil || repeatedDecision != sliceprotocol.DecisionAccepted {
		s.t.Fatalf("higher routed source publication failed: decision=%s err=%v", repeatedDecision, err)
	}
	launchCount += countEffectKind(duplicateEffects, slicecontroller.EffectLaunchProjection)
	launchCount += countEffectKind(repeatedEffects, slicecontroller.EffectLaunchProjection)
	controllerState, err := controller.Status()
	if err != nil || launchCount != 1 || len(controllerState.Projections) != 1 {
		s.t.Fatalf("duplicate local projection reachable after routed replay: launches=%d projections=%d err=%v", launchCount, len(controllerState.Projections), err)
	}
	adopted := controllerState.LaunchHandoffs[beforeRestart.Token]
	if adopted.Status != "launched" || adopted.Token != beforeRestart.Token || adopted.SessionName != beforeRestart.SessionName || adopted.SourceID != second.Intent.SourceID || adopted.SourceEpoch != second.Intent.SourceEpoch || adopted.RuntimeWindowID != second.Intent.RuntimeWindowID {
		s.t.Fatalf("controller did not adopt exact routed token/session/source: %+v", adopted)
	}

	s.summary.Restarts++
	s.summary.Churn["routed_stack_reconstructions"]++
	s.summary.Churn["routed_restart_replays"]++
	s.summary.Effects["host_session_create"] += tx.session
	s.summary.Effects["host_kitty_start"] += tx.kitty
	s.summary.Effects["host_placement"] += tx.placement
	s.summary.Effects["host_source_commit"]++
	s.summary.Effects["routed_local_projection_launch"] += launchCount
	s.summary.Effects["routed_transport_attempts"] += remote.calls + replayRemote.calls
}

func countEffectKind(effects []slicecontroller.Effect, kind slicecontroller.EffectKind) int {
	count := 0
	for _, effect := range effects {
		if effect.Kind == kind {
			count++
		}
	}
	return count
}

func (s *soak) exerciseRoutedIntent() {
	const maxUnique = 20
	if len(s.createdTokens) >= maxUnique {
		if _, err := s.launchStore.Read(s.createdTokens[len(s.createdTokens)%maxUnique]); err != nil {
			s.t.Fatalf("same-token intent replay: %v", err)
		}
		s.summary.Churn["routed_replays"]++
		return
	}
	var intent slicelaunch.Intent
	if len(s.createdTokens) < maxUnique {
		createdIntent, err := s.launchStore.Create("Work", 5*time.Second, s.now)
		if err != nil {
			s.t.Fatalf("create routed intent: %v", err)
		}
		createdIntent.Status = slicelaunch.IntentFailed
		createdIntent.UpdatedAt = s.now.Add(time.Millisecond)
		if err := s.launchStore.Write(createdIntent); err != nil {
			s.t.Fatalf("terminal routed intent: %v", err)
		}
		record, created, err := s.tokenStore.CreatePending(soakHostID, createdIntent.Token, s.now)
		if err != nil || !created || record.Token != createdIntent.Token {
			s.t.Fatalf("host token journal create: created=%t err=%v", created, err)
		}
		intent = createdIntent
	} else {
		sum := sha256.Sum256([]byte(fmt.Sprintf("terminal-redeemer-soak-intent/%d", len(s.createdTokens))))
		intent = slicelaunch.Intent{Token: fmt.Sprintf("%x", sum[:]), SessionName: slicerpc.StableSessionName(fmt.Sprintf("%x", sum[:])), WorkspaceName: "Work"}
	}
	if _, err := s.engine.SetLaunchHandoff(slicecontroller.LaunchHandoff{Token: intent.Token, Status: "launch_pending", SessionName: intent.SessionName, WorkspaceName: intent.WorkspaceName, UpdatedAt: s.now}); err != nil {
		s.t.Fatalf("pending handoff: %v", err)
	}
	if _, err := s.engine.SetLaunchHandoff(slicecontroller.LaunchHandoff{Token: intent.Token, Status: "not_created", SessionName: intent.SessionName, WorkspaceName: intent.WorkspaceName, UpdatedAt: s.now.Add(time.Millisecond)}); err != nil {
		s.t.Fatalf("terminal handoff: %v", err)
	}
	s.createdTokens = append(s.createdTokens, intent.Token)
	s.summary.Churn["routed_intents"]++
}

func (s *soak) exerciseHandoffCompaction() {
	state, err := s.engine.Status()
	if err != nil {
		s.t.Fatal(err)
	}
	const total = slicecontroller.MaxLaunchHandoffs + 77
	for len(s.createdTokens) < total {
		index := len(s.createdTokens)
		sum := sha256.Sum256([]byte(fmt.Sprintf("terminal-redeemer-soak-compaction/%d", index)))
		token := fmt.Sprintf("%x", sum[:])
		state.LaunchHandoffs[token] = slicecontroller.LaunchHandoff{
			Token: token, Status: "not_created", SessionName: slicerpc.StableSessionName(token),
			WorkspaceName: "Work", UpdatedAt: s.now.Add(time.Duration(index) * time.Nanosecond),
		}
		s.createdTokens = append(s.createdTokens, token)
	}
	if err := state.Compact(); err != nil {
		s.t.Fatalf("handoff compaction: %v", err)
	}
	if err := s.store.Write(state); err != nil {
		s.t.Fatalf("write compacted handoff authority: %v", err)
	}
	s.engine = s.newEngine()
	s.observeCaps(state)
}

func (s *soak) exercisePreparedNamespace(i int) {
	base := filepath.Join(s.root, "real-sockets")
	version := filepath.Join(base, zellijlive.SocketContractDir)
	if err := os.MkdirAll(version, 0o700); err != nil {
		s.t.Fatal(err)
	}
	_ = os.Chmod(base, 0o700)
	_ = os.Chmod(version, 0o700)
	session := "soak-session"
	socket := filepath.Join(version, session)
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		s.t.Fatalf("listen exact socket: %v", err)
	}
	defer listener.Close()
	privateRoot := filepath.Join(s.root, "attach-private")
	token := fmt.Sprintf("soak-%08d", i)
	planned, outcome := sliceattach.PlanExactSocket(base, privateRoot, session, token, os.Getuid())
	if outcome.Status != "" {
		s.t.Fatalf("plan exact socket: %+v", outcome)
	}
	prepared, outcome := sliceattach.PreparePlannedExactSocket(base, privateRoot, session, token, os.Getuid(), planned)
	if outcome.Status != "" {
		s.t.Fatalf("prepare exact socket: %+v", outcome)
	}
	cache := filepath.Join(s.root, "shim-caches", fmt.Sprintf("cache-%08d", i))
	wrapper := sliceattach.PreparedWrapper{
		Command:   "/nix/store/soak-zellij/bin/zellij",
		Session:   session,
		Identity:  prepared,
		ShimCache: cache,
		UID:       os.Getuid(),
		Version:   func(context.Context, string) error { return nil },
		Run:       fakeAttachRun,
	}
	if result := wrapper.Attach(context.Background()); result.Status != sliceattach.StatusDetached {
		s.t.Fatalf("prepared wrapper: %+v", result)
	}
	if err := os.RemoveAll(cache); err != nil {
		s.t.Fatal(err)
	}
	s.summary.Churn["prepared_namespaces"]++
}

func fakeAttachRun(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
	return nil
}

func (s *soak) spawnChild() {
	executable, err := os.Executable()
	if err != nil {
		s.t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSoakChildHelper$")
	cmd.Env = append(os.Environ(), "TERMINAL_REDEEMER_SOAK_CHILD=1")
	if err := cmd.Run(); err != nil {
		s.t.Fatalf("bounded child churn: %v", err)
	}
	s.summary.Churn["child_processes_started"]++
}

func (s *soak) currentSource() string {
	state, err := s.engine.Status()
	if err != nil || state.Inventory == nil {
		return ""
	}
	for _, source := range state.Inventory.Sources {
		tracked := state.Sources[source.SourceID]
		if tracked.Lifecycle == slicecontroller.SourceEligible {
			return source.SourceID
		}
	}
	return ""
}

func (s *soak) localWindows() []slicecontroller.OwnedWindow {
	windows := make([]slicecontroller.OwnedWindow, 0, len(s.local))
	for _, window := range s.local {
		windows = append(windows, window)
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].SourceID < windows[j].SourceID })
	return windows
}

func (s *soak) observeCaps(state slicecontroller.State) {
	values := map[string]int{
		"sources": len(state.Sources), "projections": len(state.Projections), "session_drops": len(state.ClosedByUser),
		"selected_workspaces": len(state.SelectedWorkspaces), "pickups": len(state.Pickups), "successor_gates": len(state.SuccessorGates),
		"pending_cleanups": len(state.PendingCleanups), "lineage": len(state.Lineage),
		"launch_handoffs": len(state.LaunchHandoffs), "handoff_tombstones": len(state.RetiredHandoffTokens), "spatial_records": len(state.Spatial),
		"audit": len(state.Audit), "undo": len(state.Undo), "retired_epochs": len(state.Acceptance.RetiredEpochs),
	}
	for _, projection := range state.Projections {
		if len(projection.ExpectedKittyArgv) > values["projection_argv_entries"] {
			values["projection_argv_entries"] = len(projection.ExpectedKittyArgv)
		}
		total := 0
		for _, entry := range projection.ExpectedKittyArgv {
			total += len(entry)
			if len(entry) > values["projection_argv_entry_bytes"] {
				values["projection_argv_entry_bytes"] = len(entry)
			}
		}
		if total > values["projection_argv_total_bytes"] {
			values["projection_argv_total_bytes"] = total
		}
	}
	for name, value := range values {
		if value > s.maxima[name] {
			s.maxima[name] = value
		}
	}
	if err := state.Validate(); err != nil {
		s.t.Fatalf("controller cap/state invariant: %v", err)
	}
}

func completeEnvelope(authority sliceprotocol.Authoritative, now time.Time) sliceprotocol.Envelope {
	return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: soakHostID, Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: now}, Authoritative: &authority}
}

func degradedEnvelope(now time.Time) sliceprotocol.Envelope {
	return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: soakHostID, Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityDegraded, AttemptedAt: now, DegradedReasons: []sliceprotocol.Reason{{Code: sliceprotocol.ReasonAuthorityUnavailable}}}}
}

func makeAuthority(epoch string, revision uint64, iteration int, now time.Time) sliceprotocol.Authoritative {
	count := (iteration/5 + 1) % 5
	sources := make([]sliceprotocol.Source, 0, count)
	live := make([]string, 0, count+1)
	for index := 0; index < count; index++ {
		slot := (iteration + index) % 32
		runtimeID := uint64(slot + 1)
		sessionID := opaqueID("ses_", slot)
		sourceID, err := sourceinventory.SourceID(epoch, runtimeID)
		if err != nil {
			panic(err)
		}
		workspace := "Work"
		if slot%3 == 0 {
			workspace = "Other"
		}
		key, _ := sliceprotocol.NormalizeWorkspaceName(workspace)
		sources = append(sources, sliceprotocol.Source{
			SourceID: sourceID, RuntimeWindowID: runtimeID,
			Session:   sliceprotocol.Session{ID: sessionID, Name: fmt.Sprintf("session-%d", slot), Status: "active"},
			Workspace: sliceprotocol.Workspace{RuntimeID: uint64((slot % 2) + 1), Name: workspace, Key: key},
			Output:    &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"},
			Layout:    sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: index + 1, Tile: 1}, TileWidth: 0.5, TileHeight: 0.5, WindowWidth: 960, WindowHeight: 540},
		})
		live = append(live, sessionID)
	}
	if iteration%17 == 0 {
		live = append(live, opaqueID("ses_", 50000+iteration))
	}
	sort.Strings(live)
	sort.Slice(sources, func(i, j int) bool { return sources[i].SourceID < sources[j].SourceID })
	return sliceprotocol.Authoritative{SourceEpoch: epoch, Revision: revision, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: live, Sources: sources, Conflicts: []sliceprotocol.Conflict{}}
}

func cloneAuthority(authority sliceprotocol.Authoritative) sliceprotocol.Authoritative {
	copy := authority
	copy.LiveSessionIDs = append([]string{}, authority.LiveSessionIDs...)
	copy.Sources = append([]sliceprotocol.Source{}, authority.Sources...)
	copy.Conflicts = append([]sliceprotocol.Conflict{}, authority.Conflicts...)
	return copy
}

func soakUUID(index int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", index, index) }

func opaqueID(prefix string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("terminal-redeemer-soak/%s/%d", prefix, index)))
	return prefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

func countFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("procfs fd accounting unavailable: %v", err)
	}
	return len(entries)
}

func countChildren(t *testing.T) int {
	t.Helper()
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Skipf("procfs child accounting unavailable: %v", err)
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		payload, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if err != nil {
			continue
		}
		for _, pid := range strings.Fields(string(payload)) {
			seen[pid] = true
		}
	}
	return len(seen)
}

func countPrefixDirs(root, prefix string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			count++
		}
	}
	return count
}

func fileSize(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > int64(^uint(0)>>1) {
		t.Fatal("file size does not fit int")
	}
	return int(info.Size())
}

func countJSONFiles(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func countTokenRecords(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !strings.HasPrefix(entry.Name(), ".") {
			count++
		}
	}
	return count
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
