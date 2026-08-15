package slicecontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceattach"
	"github.com/jmo/terminal-redeemer/internal/slicelayout"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

const sourceA = "src_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const sourceB = "src_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const sessionA = "ses_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const sessionB = "ses_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func testSource(id, session, name, workspace string) sliceprotocol.Source {
	key, _ := sliceprotocol.NormalizeWorkspaceName(workspace)
	return sliceprotocol.Source{SourceID: id, RuntimeWindowID: 42, Session: sliceprotocol.Session{ID: session, Name: name, Status: "active"}, Workspace: sliceprotocol.Workspace{RuntimeID: 2, Name: workspace, Key: key}, Output: &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"}, Layout: sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: 1, Tile: 1}, TileWidth: 960, TileHeight: 540, WindowWidth: 960, WindowHeight: 540}}
}
func envelope(epoch string, revision uint64, sources []sliceprotocol.Source, at time.Time) sliceprotocol.Envelope {
	live := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, source := range sources {
		if !seen[source.Session.ID] {
			live = append(live, source.Session.ID)
			seen[source.Session.ID] = true
		}
	}
	return envelopeWithLive(epoch, revision, sources, live, at)
}
func envelopeWithLive(epoch string, revision uint64, sources []sliceprotocol.Source, live []string, at time.Time) sliceprotocol.Envelope {
	if sources == nil {
		sources = []sliceprotocol.Source{}
	}
	if live == nil {
		live = []string{}
	}
	return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: at}, Authoritative: &sliceprotocol.Authoritative{SourceEpoch: epoch, Revision: revision, ObservedAt: at, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: live, Sources: sources, Conflicts: []sliceprotocol.Conflict{}}}
}
func degraded(authority *sliceprotocol.Authoritative, at time.Time) sliceprotocol.Envelope {
	return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityDegraded, AttemptedAt: at, DegradedReasons: []sliceprotocol.Reason{{Code: sliceprotocol.ReasonNiriReplayTimeout}}}, Authoritative: authority}
}

type producerNiri struct {
	state niriipc.State
	err   error
}

func (fake *producerNiri) Snapshot(context.Context) (niriipc.State, error) {
	return fake.state, fake.err
}

type producerCatalog struct {
	catalog zellijlive.Catalog
}

func (fake *producerCatalog) Observe(context.Context) (zellijlive.Catalog, error) {
	return fake.catalog, nil
}

type producerProcesses map[int]zellijlive.ProcessEvidence

func (fake producerProcesses) Observe(_ context.Context, pid int) (zellijlive.ProcessEvidence, error) {
	return fake[pid], nil
}

func newEngine(t *testing.T, now *time.Time) (*Engine, *Store) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Initialize(Namespace{Host: "host", Leech: "leech"}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: store, Config: ControllerConfig{RetryWindow: 10 * time.Second, RetryInitialBackoff: time.Second, RetryMaxBackoff: 2 * time.Second, RetryMaxAttempts: 2, SourceGoneGrace: 5 * time.Second, SourceGoneConfirmations: 2}, Now: func() time.Time { return *now }}
	return engine, store
}

func prepareSuccessfulLaunch(t *testing.T, engine *Engine, sourceID string, pid int) {
	t.Helper()
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	p := state.Projections[sourceID]
	argv := []string{"/store/bin/kitty", "--config", "NONE", "--class", p.AppID, "-e", "redeem", "slice", "projection-run", "--source-id", p.ProcessSourceID, "--session", p.ExpectedSessionName, "--token", p.AttachToken}
	if _, err := engine.PrepareProjection(sourceID, "/store/bin/kitty", argv); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RecordLaunch(sourceID, pid, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStoreEnrollmentMissingAfterUseSingletonAndModes(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Initialize(Namespace{Host: "h", Leech: "l"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Initialize(state.Namespace); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second init: %v", err)
	}
	for _, path := range []string{store.root, store.marker, store.current} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if path != store.root {
			want = 0o600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o", path, info.Mode().Perm())
		}
	}
	lock, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire(); !errors.Is(err, ErrControllerLocked) {
		t.Fatalf("singleton lock: %v", err)
	}
	_ = lock.Close()
	if err := os.Remove(store.current); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("missing after enrollment: %v", err)
	}
	if _, err := store.Initialize(state.Namespace); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("reinit after loss: %v", err)
	}
}

func TestStoreRejectsDuplicateKeyOrInvalidUTF8Authority(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`{"schema_version":1,"schema_version":1}`), {0xff, '\n'}} {
		store, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Initialize(Namespace{Host: "h", Leech: "l"}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.current, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read(); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("hostile authority accepted: %v", err)
		}
	}
}

func TestStoreRejectsForbiddenControllerAuthorityValuesWithoutMigration(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Initialize(Namespace{Host: "h", Leech: "l"})
	if err != nil {
		t.Fatal(err)
	}
	baseline := &slicelayout.Spatial{WorkspaceName: "work", WorkspaceKey: "work", Mode: slicelayout.Tiled, WidthPercent: 50, HeightPercent: 50}
	state.Spatial[sourceA] = SpatialRecord{Baseline: baseline, PendingBaseline: baseline}
	if err := store.Write(state); err != nil {
		t.Fatal(err)
	}
	decoded, err := store.Read()
	if err != nil || decoded.AuthorityMode != "host_location" || decoded.LeechWriteAuthorized || decoded.Spatial[sourceA].Baseline == nil || decoded.Spatial[sourceA].PendingBaseline == nil {
		t.Fatalf("current schema-v2 compatibility fields did not round trip: %#v err=%v", decoded, err)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(payload, &old); err != nil {
		t.Fatal(err)
	}
	if old["authority_mode"] != "host_location" || old["leech_write_authorized"] != false {
		t.Fatalf("current fixed authority fields changed: %#v", old)
	}
	old["authority_mode"] = "leech_location"
	old["leech_write_authorized"] = true
	payload, _ = json.Marshal(old)
	if err := os.WriteFile(store.current, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.current)
	if _, err := store.Read(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("old controller authority accepted: %v", err)
	}
	after, _ := os.ReadFile(store.current)
	if !bytes.Equal(before, after) {
		t.Fatal("failed-closed read rewrote old authority")
	}
	if _, err := store.Initialize(state.Namespace); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("old authority was silently re-enrolled: %v", err)
	}
}

func TestStoreRejectsOversizedStateBeforeReplacingReadableAuthority(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Initialize(Namespace{Host: "h", Leech: "l"})
	if err != nil {
		t.Fatal(err)
	}
	oversized := before
	oversized.Audit = append(oversized.Audit, AuditEntry{Generation: oversized.Generation, At: time.Now().UTC(), Kind: "oversized", Detail: strings.Repeat("x", MaxControllerStateBytes)})
	if err := store.Write(oversized); err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("oversized write=%v", err)
	}
	after, err := store.Read()
	if err != nil || after.Generation != before.Generation || len(after.Audit) != len(before.Audit) {
		t.Fatalf("oversized write replaced readable authority: %#v %v", after, err)
	}
}

func TestProjectionArgvAuthorityBounds(t *testing.T) {
	base := NewState(Namespace{Host: "h", Leech: "l"}, "controller")
	base.Sources[sourceA] = TrackedSource{SourceID: sourceA, SourceEpoch: "11111111-1111-4111-8111-111111111111", SessionID: sessionA, SessionName: "s", Lifecycle: SourceEligible}
	projection := Projection{SourceID: sourceA, AppID: "app", AttachToken: "token", ExpectedSessionName: "s", ProcessSourceID: sourceA, ExpectedKittyExecutable: "/store/bin/kitty", ExpectedKittyArgv: []string{"kitty", "--class"}, Status: ProjectionLaunching, CreatedAt: time.Now().UTC()}
	for name, argv := range map[string][]string{"entry_count": func() []string {
		v := make([]string, MaxProjectionArgvEntries+1)
		for i := range v {
			v[i] = "x"
		}
		return v
	}(), "entry_bytes": {"kitty", strings.Repeat("x", MaxProjectionArgvEntryBytes+1)}, "aggregate": func() []string {
		v := make([]string, MaxProjectionArgvEntries)
		for i := range v {
			v[i] = strings.Repeat("x", 300)
		}
		return v
	}()} {
		t.Run(name, func(t *testing.T) {
			state := base
			state.Projections = map[string]Projection{}
			p := projection
			p.ExpectedKittyArgv = argv
			state.Projections[sourceA] = p
			if err := state.Validate(); err == nil {
				t.Fatal("oversized argv authority accepted")
			}
		})
	}
}

func projectionArgvWithBytes(total int) []string {
	var out []string
	for total > 0 {
		size := MaxProjectionArgvEntryBytes
		if total < size {
			size = total
		}
		out = append(out, strings.Repeat("x", size))
		total -= size
	}
	return out
}

func TestPrepareProjectionExactAggregateBoundary(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	exact := projectionArgvWithBytes(MaxProjectionArgvTotalBytes)
	state, err := engine.PrepareProjection(sourceA, "/store/bin/kitty", exact)
	if err != nil || !reflect.DeepEqual(state.Projections[sourceA].ExpectedKittyArgv, exact) {
		t.Fatalf("exact aggregate boundary rejected: %v", err)
	}
	over := projectionArgvWithBytes(MaxProjectionArgvTotalBytes + 1)
	if _, err := engine.PrepareProjection(sourceA, "/store/bin/kitty", over); err == nil {
		t.Fatal("over-bound aggregate prepared")
	}
	after, _ := engine.Status()
	if !reflect.DeepEqual(after.Projections[sourceA].ExpectedKittyArgv, exact) {
		t.Fatal("rejected over-bound prepare changed durable authority")
	}
}

func TestStoreRejectsSymlinkedControllerHierarchy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "slice"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "slice", "controller")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root); err == nil {
		t.Fatal("symlink hierarchy accepted")
	}
}

func TestDesiredSelectionDuplicateRevisionCloseReopenUndo(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "work-a", "Work")}, now)
	if _, effects, decision, err := engine.ApplyEnvelope(env, now); err != nil || decision != sliceprotocol.DecisionAccepted || len(effects) != 0 {
		t.Fatalf("first snapshot: %s %#v %v", decision, effects, err)
	}
	state, effects, err := engine.SelectWorkspace(" work ", true)
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection {
		t.Fatalf("select: %#v %v", effects, err)
	}
	mapping := state.Projections[sourceA]
	if mapping.AppID == "" || mapping.Status != ProjectionLaunching {
		t.Fatalf("mapping not durable before effect: %#v", mapping)
	}
	if _, effects, decision, err := engine.ApplyEnvelope(env, now.Add(time.Second)); err != nil || decision != sliceprotocol.DecisionDuplicate || len(effects) != 0 {
		t.Fatalf("duplicate: %s %#v %v", decision, effects, err)
	}
	prepareSuccessfulLaunch(t, engine, sourceA, 123)
	state, effects, err = engine.Close(sourceA)
	if err != nil || state.ClosedByUser[sessionA].SessionID != sessionA || len(effects) == 0 || effects[0].Kind != EffectCloseProjection {
		t.Fatalf("close: %#v %#v %v", state, effects, err)
	}
	state, _, err = engine.Reopen(sourceA)
	if _, dropped := state.ClosedByUser[sessionA]; err != nil || dropped {
		t.Fatalf("reopen: %v %#v", err, state)
	}
	state, _, err = engine.Undo()
	if _, dropped := state.ClosedByUser[sessionA]; err != nil || !dropped {
		t.Fatalf("undo reopen should restore close: %v %#v", err, state.ClosedByUser)
	}
}

func TestLaunchingProjectionUsesConfiguredRecoveryWindowForPublication(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "work-a", "Work")}, now)
	if _, _, _, err := engine.ApplyEnvelope(env, now); err != nil {
		t.Fatal(err)
	}
	state, _, err := engine.SelectWorkspace("Work", true)
	if err != nil {
		t.Fatal(err)
	}
	projection := state.Projections[sourceA]
	if _, err = engine.PrepareProjection(sourceA, "/kitty", []string{"/kitty", "--class", projection.AppID}); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.RecordLaunch(sourceA, 42, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(9 * time.Second)
	state, _, err = engine.ObserveLocal("leech-epoch", nil)
	if err != nil || state.Projections[sourceA].ExpectedPID != 42 {
		t.Fatalf("launch was retired before configured recovery window: %#v %v", state.Projections, err)
	}
	now = now.Add(2 * time.Second)
	state, _, err = engine.ObserveLocal("leech-epoch", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Projections[sourceA]; ok {
		t.Fatalf("expired launch was retained: %#v", state.Projections[sourceA])
	}
}

func TestDynamicSelectionPickupMoveDropAndRemovalMatrix(t *testing.T) {
	now := time.Date(2026, 7, 25, 13, 0, 0, 0, time.UTC)
	engine, _ := newEngine(t, &now)
	epoch := "11111111-1111-4111-8111-111111111111"
	a := testSource(sourceA, sessionA, "a", "Work")
	b := testSource(sourceB, sessionB, "b", "Other")
	b.RuntimeWindowID = 43
	if _, effects, decision, err := engine.ApplyEnvelope(envelope(epoch, 1, []sliceprotocol.Source{a, b}, now), now); err != nil || decision != sliceprotocol.DecisionAccepted || len(effects) != 0 {
		t.Fatalf("initial effects=%+v decision=%s err=%v", effects, decision, err)
	}
	state, effects, err := engine.SelectWorkspace("Work", true)
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection || effects[0].SourceID != sourceA {
		t.Fatalf("selection state=%+v effects=%+v err=%v", state.SelectedWorkspaces, effects, err)
	}
	state, effects, err = engine.Pickup(sourceB, true)
	if err != nil || !state.Pickups[sourceB] || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection || effects[0].SourceID != sourceB {
		t.Fatalf("pickup state=%+v effects=%+v err=%v", state.Pickups, effects, err)
	}

	// A moves out of the selected workspace while a new C appears in it.
	movedA := testSource(sourceA, sessionA, "a", "Other")
	const sourceC = "src_ccccccccccccccccccccccccccccccccccccccccccc"
	const sessionC = "ses_ccccccccccccccccccccccccccccccccccccccccccc"
	c := testSource(sourceC, sessionC, "c", "Work")
	c.RuntimeWindowID = 44
	now = now.Add(time.Second)
	state, effects, decision, err := engine.ApplyEnvelope(envelope(epoch, 2, []sliceprotocol.Source{movedA, b, c}, now), now)
	if err != nil || decision != sliceprotocol.DecisionAccepted || state.Sources[sourceA].WorkspaceKey != "other" {
		t.Fatalf("move state=%+v effects=%+v decision=%s err=%v", state.Sources[sourceA], effects, decision, err)
	}
	var launchedC, closedA bool
	for _, effect := range effects {
		launchedC = launchedC || effect.Kind == EffectLaunchProjection && effect.SourceID == sourceC
		closedA = closedA || effect.Kind == EffectCloseProjection && effect.SourceID == sourceA
	}
	if !launchedC || !closedA {
		t.Fatalf("dynamic add/move effects=%+v", effects)
	}

	state, effects, err = engine.Pickup(sourceB, false)
	if err != nil || state.Pickups[sourceB] || len(effects) != 1 || effects[0].Kind != EffectCloseProjection || effects[0].SourceID != sourceB {
		t.Fatalf("drop state=%+v effects=%+v err=%v", state.Pickups, effects, err)
	}

	// Removal cannot close on one complete absence; the second complete revision
	// crosses the configured confirmation threshold.
	now = now.Add(time.Second)
	state, effects, _, err = engine.ApplyEnvelope(envelope(epoch, 3, []sliceprotocol.Source{movedA, b}, now), now)
	if err != nil || len(effects) != 0 || state.Sources[sourceC].Lifecycle != SourceGoneGrace {
		t.Fatalf("first removal state=%+v effects=%+v err=%v", state.Sources[sourceC], effects, err)
	}
	now = now.Add(time.Second)
	state, effects, _, err = engine.ApplyEnvelope(envelope(epoch, 4, []sliceprotocol.Source{movedA, b}, now), now)
	if err != nil || state.Sources[sourceC].Lifecycle != SourceClosed || len(effects) != 1 || effects[0].Kind != EffectCloseProjection || effects[0].SourceID != sourceC {
		t.Fatalf("confirmed removal state=%+v effects=%+v err=%v", state.Sources[sourceC], effects, err)
	}
}

func TestAllEligibleSelectionComposesWithWorkspacePickupAndClose(t *testing.T) {
	now := time.Date(2026, 7, 25, 13, 30, 0, 0, time.UTC)
	engine, _ := newEngine(t, &now)
	epoch := "11111111-1111-4111-8111-111111111111"
	named := testSource(sourceA, sessionA, "named", "Work")
	unnamed := testSource(sourceB, sessionB, "unnamed", "")
	unnamed.RuntimeWindowID = 43
	if _, _, _, err := engine.ApplyEnvelope(envelope(epoch, 1, []sliceprotocol.Source{named, unnamed}, now), now); err != nil {
		t.Fatal(err)
	}

	state, effects, err := engine.SelectAll(true)
	if err != nil || !state.AllEligible || len(effects) != 2 || len(state.Undo) != 0 {
		t.Fatalf("enable all state=%+v effects=%+v err=%v", state, effects, err)
	}
	encoded, _ := json.Marshal(state)
	if !bytes.Contains(encoded, []byte(`"all_eligible":true`)) {
		t.Fatalf("enabled all state omitted authority: %s", encoded)
	}
	generation := state.Generation
	if state, effects, err = engine.SelectAll(true); err != nil || len(effects) != 0 || state.Generation != generation {
		t.Fatalf("duplicate enable changed state generation=%d effects=%+v err=%v", state.Generation, effects, err)
	}

	state, effects, err = engine.Close(sourceB)
	if err != nil || state.Wanted(sourceB) || len(effects) != 1 || effects[0].Kind != EffectCloseProjection {
		t.Fatalf("close under all state=%+v effects=%+v err=%v", state.ClosedByUser, effects, err)
	}

	const sourceC = "src_ccccccccccccccccccccccccccccccccccccccccccc"
	const sessionC = "ses_ccccccccccccccccccccccccccccccccccccccccccc"
	future := testSource(sourceC, sessionC, "future", "Other")
	future.RuntimeWindowID = 44
	now = now.Add(time.Second)
	state, effects, _, err = engine.ApplyEnvelope(envelope(epoch, 2, []sliceprotocol.Source{named, unnamed, future}, now), now)
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection || effects[0].SourceID != sourceC {
		t.Fatalf("future all source state=%+v effects=%+v err=%v", state.Sources[sourceC], effects, err)
	}
	if _, _, err = engine.SelectWorkspace("Work", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = engine.Pickup(sourceC, true); err != nil {
		t.Fatal(err)
	}
	state, effects, err = engine.SelectAll(false)
	if err != nil || state.AllEligible || len(effects) != 0 || !state.Wanted(sourceA) || !state.Wanted(sourceC) || state.Wanted(sourceB) {
		t.Fatalf("disable all composition state=%+v effects=%+v err=%v", state, effects, err)
	}
	encoded, _ = json.Marshal(state)
	if bytes.Contains(encoded, []byte(`"all_eligible"`)) {
		t.Fatalf("disabled all state is not prior-reader compatible: %s", encoded)
	}
	if state, effects, err = engine.Pickup(sourceC, false); err != nil || state.Pickups[sourceC] || len(effects) != 1 || effects[0].SourceID != sourceC {
		t.Fatalf("remove pickup state=%+v effects=%+v err=%v", state.Pickups, effects, err)
	}
	if state, effects, err = engine.SelectWorkspace("Work", false); err != nil || state.Wanted(sourceA) || len(effects) != 1 || effects[0].SourceID != sourceA {
		t.Fatalf("remove workspace state=%+v effects=%+v err=%v", state.SelectedWorkspaces, effects, err)
	}
}

func TestInterruptedAllEnableRecoversUnstartedFanout(t *testing.T) {
	now := time.Date(2026, 7, 25, 13, 45, 0, 0, time.UTC)
	engine, store := newEngine(t, &now)
	if _, effects, err := engine.SelectAll(true); err != nil || len(effects) != 0 {
		t.Fatalf("enable all before discovery effects=%+v err=%v", effects, err)
	}
	const count = 32
	sources := make([]sliceprotocol.Source, 0, count)
	for i := 0; i < count; i++ {
		source := testSource(fmt.Sprintf("src_%043x", i+1), fmt.Sprintf("ses_%043x", i+1), fmt.Sprintf("session-%d", i+1), "Work")
		source.RuntimeWindowID = uint64(100 + i)
		sources = append(sources, source)
	}
	_, effects, _, err := engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, sources, now), now)
	if err != nil || len(effects) != count {
		t.Fatalf("poll fanout effects=%d err=%v", len(effects), err)
	}
	err = engine.ExecuteEffects(context.Background(), effects, func(_ context.Context, effects []Effect) error {
		first := effects[0]
		projection := first.Projection
		if _, err := engine.PrepareProjection(first.SourceID, "/kitty", []string{"/kitty", "--class", projection.AppID}); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.RecordLaunch(first.SourceID, 123, nil); err != nil {
			t.Fatal(err)
		}
		return context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("execute error=%v", err)
	}
	state, err := engine.Status()
	if err != nil || !state.AllEligible || len(state.Projections) != 1 {
		t.Fatalf("interrupted state projections=%d all=%t err=%v", len(state.Projections), state.AllEligible, err)
	}
	for _, source := range sources[1:] {
		tracked := state.Sources[source.SourceID]
		if tracked.Connection != ConnectionReconnecting || tracked.Recovery == nil {
			t.Fatalf("unstarted source did not enter bounded recovery: %+v", tracked)
		}
	}

	// Restart from the durable state without any successful local observation;
	// the shared executor recovery must still make every unstarted source
	// eligible for the normal bounded retry path.
	restarted := &Engine{Store: store, Config: engine.Config, Now: func() time.Time { return now }}
	if _, err := restarted.PrepareStartup(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	state, effects, err = restarted.Tick()
	if err != nil || len(effects) != count-1 || len(state.Projections) != count {
		t.Fatalf("restart retry convergence projections=%d effects=%d err=%v", len(state.Projections), len(effects), err)
	}
}

func TestControlAllAndPickupRemoveUseSerializedEngineOperations(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	if _, _, _, err := engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "Work")}, now), now); err != nil {
		t.Fatal(err)
	}
	handler := ControlHandler{Engine: engine}
	response := handler.Handle(context.Background(), NewControlRequest(VerbAllEnable, struct{}{}))
	if response.Outcome.Status != "ok" || response.State == nil || !response.State.AllEligible {
		t.Fatalf("enable all response=%+v", response)
	}
	response = handler.Handle(context.Background(), NewControlRequest(VerbAllDisable, struct{}{}))
	if response.Outcome.Status != "ok" || response.State == nil || response.State.AllEligible {
		t.Fatalf("disable all response=%+v", response)
	}
	response = handler.Handle(context.Background(), NewControlRequest(VerbPickup, SourcePayload{SourceID: sourceA}))
	if response.Outcome.Status != "ok" || response.State == nil || !response.State.Pickups[sourceA] {
		t.Fatalf("pickup response=%+v", response)
	}
	response = handler.Handle(context.Background(), NewControlRequest(VerbPickupRemove, SourcePayload{SourceID: sourceA}))
	if response.Outcome.Status != "ok" || response.State == nil || response.State.Pickups[sourceA] {
		t.Fatalf("pickup remove response=%+v", response)
	}
}

func TestInitialProjectionEffectsFollowHostColumnTileOrder(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	a := testSource(sourceA, sessionA, "a", "work")
	b := testSource(sourceB, sessionB, "b", "work")
	b.RuntimeWindowID = 43
	a.Layout.Position = &sliceprotocol.Position{Column: 2, Tile: 1}
	b.Layout.Position = &sliceprotocol.Position{Column: 1, Tile: 2}
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{a, b}, now), now)
	_, effects, err := engine.SelectWorkspace("work", true)
	if err != nil || len(effects) != 2 || effects[0].SourceID != sourceB || effects[1].SourceID != sourceA {
		t.Fatalf("order effects=%#v err=%v", effects, err)
	}
}

func TestHeadlessInventorySelectsAndLaunchesExistingSources(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	source := testSource(sourceA, sessionA, "a", "work")
	source.Output = nil
	state, effects, decision, err := engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{source}, now), now)
	if err != nil || decision != sliceprotocol.DecisionAccepted || len(effects) != 0 || state.Sources[sourceA].Lifecycle != SourceEligible {
		t.Fatalf("headless inventory was not accepted: state=%+v effects=%+v decision=%s err=%v", state, effects, decision, err)
	}
	state, effects, err = engine.SelectAll(true)
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection || effects[0].SourceID != sourceA || state.Projections[sourceA].Status != ProjectionLaunching {
		t.Fatalf("headless all-eligible launch failed: state=%+v effects=%+v err=%v", state, effects, err)
	}
}

func TestDegradedAndDuplicateNeverAdvanceAbsence(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	first := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(first, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	state, _ := engine.Status()
	prior := state.Inventory
	now = now.Add(time.Second)
	if state, _, decision, err := engine.ApplyEnvelope(degraded(prior, now), now); err != nil || decision != sliceprotocol.DecisionDegraded || state.Sources[sourceA].AbsenceCount != 0 {
		t.Fatalf("degraded advanced absence: %#v %s %v", state.Sources[sourceA], decision, err)
	}
	missing := envelope(first.Authoritative.SourceEpoch, 2, []sliceprotocol.Source{}, now.Add(time.Second))
	state, _, _, _ = engine.ApplyEnvelope(missing, now.Add(time.Second))
	if state.Sources[sourceA].Lifecycle != SourceGoneGrace || state.Sources[sourceA].AbsenceCount != 1 {
		t.Fatalf("first absence closed: %#v", state.Sources[sourceA])
	}
	state, _, decision, _ := engine.ApplyEnvelope(missing, now.Add(2*time.Second))
	if decision != sliceprotocol.DecisionDuplicate || state.Sources[sourceA].AbsenceCount != 1 {
		t.Fatalf("duplicate advanced absence: %#v", state.Sources[sourceA])
	}
	secondMissing := envelope(first.Authoritative.SourceEpoch, 3, []sliceprotocol.Source{}, now.Add(3*time.Second))
	state, effects, _, _ := engine.ApplyEnvelope(secondMissing, now.Add(3*time.Second))
	if state.Sources[sourceA].Lifecycle != SourceClosed || len(effects) == 0 || effects[0].Kind != EffectCloseProjection {
		t.Fatalf("confirmed close: %#v %#v", state.Sources[sourceA], effects)
	}
}

func TestCompletePerSourceConflictPreservesProjectionWithoutAbsence(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	first := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(first, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	conflicted := envelope(first.Authoritative.SourceEpoch, 2, []sliceprotocol.Source{}, now.Add(time.Second))
	conflicted.Authoritative.Conflicts = []sliceprotocol.Conflict{{Code: sliceprotocol.ConflictSessionMissing, SourceID: sourceA, SessionID: sessionA}}
	state, effects, _, err := engine.ApplyEnvelope(conflicted, now.Add(time.Second))
	if err != nil || state.Sources[sourceA].Lifecycle != SourceConflict || state.Sources[sourceA].AbsenceCount != 0 || len(effects) != 0 {
		t.Fatalf("conflict became absence: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
}

func TestSourceGoneGraceDeadlineClosesOnlyOwnedProjection(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	first := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(first, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 123)
	missing := envelope(first.Authoritative.SourceEpoch, 2, []sliceprotocol.Source{}, now.Add(time.Second))
	state, _, _, _ := engine.ApplyEnvelope(missing, now.Add(time.Second))
	now = state.Sources[sourceA].AbsenceDeadline.Add(time.Millisecond)
	state, effects, err := engine.Tick()
	if err != nil || state.Sources[sourceA].Lifecycle != SourceClosed || len(effects) != 1 || effects[0].SourceID != sourceA {
		t.Fatalf("grace close: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
}

func TestConnectedRequiresAttachConfirmationAndOwnedNiriEvidence(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 123)
	state, _, _ := engine.ObserveLocal("leech", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 123}})
	if state.Sources[sourceA].Connection == ConnectionConnected {
		t.Fatal("ownership alone marked attachment connected")
	}
	state, err := engine.AttachmentConnected(sourceA)
	if err != nil || state.Sources[sourceA].Connection != ConnectionConnected {
		t.Fatalf("confirmed state=%#v err=%v", state.Sources[sourceA], err)
	}
}

func TestAttachmentLossCommitsRecoveryAndCloseBeforeRetry(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 123)
	state, effects, err := engine.AttachmentLost(sourceA)
	if err != nil || state.Sources[sourceA].Connection != ConnectionReconnecting || state.Sources[sourceA].Recovery == nil || len(effects) != 1 || effects[0].Kind != EffectCloseProjection {
		t.Fatalf("attachment loss: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
}

func TestRecoverySucceedsInsideOriginalPersistedWindowWithoutBudgetReset(t *testing.T) {
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	engine, store := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 123)
	state, _, _ := engine.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 123, AppID: stateProjectionApp(t, engine, sourceA)}})
	state, err := engine.AttachmentConnected(sourceA)
	if err != nil || state.Sources[sourceA].Connection != ConnectionConnected {
		t.Fatalf("initial connection state=%+v err=%v", state.Sources[sourceA], err)
	}

	state, effects, err := engine.AttachmentLost(sourceA)
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectCloseProjection || state.Sources[sourceA].Recovery == nil {
		t.Fatalf("loss state=%+v effects=%+v err=%v", state.Sources[sourceA], effects, err)
	}
	originalExpiry := state.Sources[sourceA].Recovery.ExpiresAt
	originalStarted := state.Sources[sourceA].Recovery.StartedAt
	// The close effect has completed locally; observation removes only the old
	// closing mapping while retaining the original absolute retry authority.
	state, _, err = engine.ObserveLocal("leech-epoch", nil)
	if err != nil || state.Projections[sourceA].SourceID != "" || state.Sources[sourceA].Recovery == nil || !state.Sources[sourceA].Recovery.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("closed mapping/reset budget state=%+v projections=%+v err=%v", state.Sources[sourceA], state.Projections, err)
	}

	// Prove restart reads the same absolute window rather than minting a new one.
	restarted := &Engine{Store: store, Config: engine.Config, Now: func() time.Time { return now }}
	state, err = restarted.Status()
	if err != nil || state.Sources[sourceA].Recovery == nil || !state.Sources[sourceA].Recovery.ExpiresAt.Equal(originalExpiry) || !state.Sources[sourceA].Recovery.StartedAt.Equal(originalStarted) {
		t.Fatalf("restart changed recovery: %+v err=%v", state.Sources[sourceA].Recovery, err)
	}
	now = state.Sources[sourceA].Recovery.NextAttemptAt
	state, effects, err = restarted.Tick()
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection || !now.Before(originalExpiry) {
		t.Fatalf("retry state=%+v effects=%+v err=%v", state.Sources[sourceA], effects, err)
	}
	if state.Sources[sourceA].Recovery == nil || !state.Sources[sourceA].Recovery.ExpiresAt.Equal(originalExpiry) || !state.Sources[sourceA].Recovery.StartedAt.Equal(originalStarted) {
		t.Fatalf("retry reset persisted budget: %+v", state.Sources[sourceA].Recovery)
	}

	mapping := state.Projections[sourceA]
	argv := []string{"/store/bin/kitty", "--class", mapping.AppID, "-e", "redeem", "slice", "projection-run", "--source-id", sourceA, "--session", mapping.ExpectedSessionName, "--token", mapping.AttachToken}
	if _, err = restarted.PrepareProjection(sourceA, "/store/bin/kitty", argv); err != nil {
		t.Fatal(err)
	}
	if state, err = restarted.RecordLaunch(sourceA, 456, nil); err != nil || state.Sources[sourceA].Recovery == nil || !state.Sources[sourceA].Recovery.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("launch reset recovery: %+v err=%v", state.Sources[sourceA].Recovery, err)
	}
	state, _, err = restarted.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 19, PID: 456, AppID: mapping.AppID}})
	if err != nil || state.Projections[sourceA].Status != ProjectionOwned || state.Projections[sourceA].NiriWindowID != 19 || state.Sources[sourceA].Recovery == nil || !state.Sources[sourceA].Recovery.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("exact mapping did not preserve retry authority: source=%+v projection=%+v err=%v", state.Sources[sourceA], state.Projections[sourceA], err)
	}
	state, err = restarted.AttachmentConnected(sourceA)
	if err != nil || state.Sources[sourceA].Connection != ConnectionConnected || state.Sources[sourceA].Recovery != nil || !state.Projections[sourceA].AttachConfirmed {
		t.Fatalf("in-window readiness did not complete recovery: source=%+v projection=%+v err=%v", state.Sources[sourceA], state.Projections[sourceA], err)
	}
}

func stateProjectionApp(t *testing.T, engine *Engine, sourceID string) string {
	t.Helper()
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	return state.Projections[sourceID].AppID
}

func TestSessionDropSurvivesSourceAndEpochReplacementAndHeadlessPresence(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	epochA := "11111111-1111-4111-8111-111111111111"
	epochB := "22222222-2222-4222-8222-222222222222"
	_, _, _, _ = engine.ApplyEnvelope(envelope(epochA, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "one", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	state, _, err := engine.Close(sourceA)
	if err != nil || state.Wanted(sourceA) {
		t.Fatalf("close=%+v err=%v", state.ClosedByUser, err)
	}
	now = now.Add(time.Second)
	state, effects, decision, err := engine.ApplyEnvelope(envelope(epochA, 2, []sliceprotocol.Source{testSource(sourceB, sessionA, "one", "work")}, now), now)
	if err != nil || decision != sliceprotocol.DecisionAccepted || state.Wanted(sourceB) || len(effects) != 0 {
		t.Fatalf("same-epoch replacement escaped drop: decision=%s effects=%+v state=%+v err=%v", decision, effects, state.ClosedByUser, err)
	}
	now = now.Add(time.Second)
	state, effects, decision, err = engine.ApplyEnvelope(envelopeWithLive(epochB, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "one", "work")}, []string{sessionA}, now), now)
	if err != nil || decision != sliceprotocol.DecisionFullResync || state.Wanted(sourceA) || len(effects) != 0 {
		t.Fatalf("cross-epoch replacement escaped drop: decision=%s effects=%+v state=%+v err=%v", decision, effects, state.ClosedByUser, err)
	}
	now = now.Add(10 * time.Second)
	state, _, decision, err = engine.ApplyEnvelope(envelopeWithLive(epochB, 2, nil, []string{sessionA}, now), now)
	drop, dropped := state.ClosedByUser[sessionA]
	if err != nil || decision != sliceprotocol.DecisionAccepted || !dropped || drop.AbsenceCount != 0 || !drop.AbsenceDeadline.IsZero() {
		t.Fatalf("headless live session advanced absence: decision=%s drop=%+v err=%v", decision, drop, err)
	}
}

func TestDifferentVerifiedSessionIsWantedDespiteExistingDrop(t *testing.T) {
	state := NewState(Namespace{Host: "host", Leech: "leech"}, "controller")
	state.SelectedWorkspaces["work"] = "work"
	state.Sources[sourceB] = TrackedSource{SourceID: sourceB, SourceEpoch: "epoch", SessionID: sessionB, SessionName: "fresh", WorkspaceKey: "work", Lifecycle: SourceEligible}
	state.ClosedByUser[sessionA] = SessionDrop{SessionID: sessionA, SessionName: "old", CreatedAt: time.Now().UTC()}
	if !state.Wanted(sourceB) {
		t.Fatal("drop for another verified session suppressed fresh routed source")
	}
}

func TestSessionDropExpiryRequiresConsecutiveCompleteAbsenceAndGrace(t *testing.T) {
	newDropped := func(t *testing.T) (*Engine, *time.Time) {
		t.Helper()
		now := time.Now().UTC()
		engine, _ := newEngine(t, &now)
		epoch := "11111111-1111-4111-8111-111111111111"
		_, _, _, _ = engine.ApplyEnvelope(envelope(epoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "one", "work")}, now), now)
		_, _, _ = engine.SelectWorkspace("work", true)
		_, _, _ = engine.Close(sourceA)
		return engine, &now
	}
	t.Run("confirmations before grace", func(t *testing.T) {
		engine, now := newDropped(t)
		epoch := "11111111-1111-4111-8111-111111111111"
		*now = (*now).Add(time.Second)
		state, _, _, _ := engine.ApplyEnvelope(envelopeWithLive(epoch, 2, nil, []string{}, *now), *now)
		deadline := state.ClosedByUser[sessionA].AbsenceDeadline
		*now = (*now).Add(time.Second)
		state, _, _, _ = engine.ApplyEnvelope(envelopeWithLive(epoch, 3, nil, []string{}, *now), *now)
		if _, ok := state.ClosedByUser[sessionA]; !ok {
			t.Fatal("confirmations expired drop before grace")
		}
		*now = deadline.Add(time.Millisecond)
		state, _, err := engine.Tick()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := state.ClosedByUser[sessionA]; ok {
			t.Fatal("tick did not expire confirmed absence after grace")
		}
	})
	t.Run("grace before confirmations", func(t *testing.T) {
		engine, now := newDropped(t)
		epoch := "11111111-1111-4111-8111-111111111111"
		*now = (*now).Add(time.Second)
		state, _, _, _ := engine.ApplyEnvelope(envelopeWithLive(epoch, 2, nil, []string{}, *now), *now)
		*now = state.ClosedByUser[sessionA].AbsenceDeadline.Add(time.Millisecond)
		state, _, _ = engine.Tick()
		if _, ok := state.ClosedByUser[sessionA]; !ok {
			t.Fatal("time alone expired drop")
		}
		state, _, _, _ = engine.ApplyEnvelope(envelopeWithLive(epoch, 3, nil, []string{}, *now), *now)
		if _, ok := state.ClosedByUser[sessionA]; ok {
			t.Fatal("later confirmation did not expire elapsed absence")
		}
	})
}

func TestPublisherAndControllerExpireStableAbsentSessionOnlyAfterConfirmationsAndGrace(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventoryStore, err := sourceinventory.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uuids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "11111111-1111-4111-8111-111111111111"}
	uuidIndex := 0
	niri := &producerNiri{state: niriipc.State{
		Outputs:    map[string]niriipc.Output{"DP-1": {Name: "DP-1", Logical: niriipc.Logical{Width: 1920, Height: 1080, Scale: 1, Transform: "Normal"}}},
		Workspaces: []niriipc.Workspace{{ID: 2, Index: 1, Name: func() *string { value := "Work"; return &value }(), Output: func() *string { value := "DP-1"; return &value }(), IsActive: true}},
		Windows:    []niriipc.Window{{ID: 42, AppID: "kitty", PID: 100, WorkspaceID: func() *uint64 { value := uint64(2); return &value }(), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}},
	}}
	catalog := &producerCatalog{catalog: zellijlive.Catalog{Sessions: map[string]zellijlive.Session{"one": {Name: "one", ID: sessionA, Status: zellijlive.StatusActive}}, Names: []string{"one"}}}
	publisher := sourceinventory.Publisher{
		Store: inventoryStore, Niri: niri, Catalog: catalog,
		Builder:     sourceinventory.Builder{Processes: producerProcesses{100: {KittyVerified: true, Candidates: []string{"one"}}}},
		Fingerprint: func() (string, error) { return strings.Repeat("1", 64), nil },
		UUID: func() (string, error) {
			value := uuids[uuidIndex]
			uuidIndex++
			return value, nil
		},
		Now: func() time.Time { return now },
	}
	if _, err := publisher.Initialize(); err != nil {
		t.Fatal(err)
	}
	engine, _ := newEngine(t, &now)
	first, err := publisher.Snapshot(context.Background())
	if err != nil || first.Authoritative.Revision != 1 || len(first.Authoritative.Sources) != 1 {
		t.Fatalf("initial producer snapshot=%+v err=%v", first, err)
	}
	sourceID := first.Authoritative.Sources[0].SourceID
	if _, _, decision, err := engine.ApplyEnvelope(first, now); err != nil || decision != sliceprotocol.DecisionAccepted {
		t.Fatalf("initial controller acceptance=%s err=%v", decision, err)
	}
	if _, _, err := engine.SelectWorkspace("Work", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Close(sourceID); err != nil {
		t.Fatal(err)
	}

	// The producer keeps publishing the same empty semantic inventory. Each
	// successfully completed poll must nevertheless supply distinct evidence.
	niri.state.Windows = []niriipc.Window{}
	catalog.catalog = zellijlive.Catalog{Sessions: map[string]zellijlive.Session{}, Names: []string{}}
	now = now.Add(time.Second)
	absentOne, err := publisher.Snapshot(context.Background())
	if err != nil || absentOne.Authoritative.Revision != 2 {
		t.Fatalf("first absent producer snapshot=%+v err=%v", absentOne, err)
	}
	state, _, decision, err := engine.ApplyEnvelope(absentOne, now)
	firstEvidence := state.ClosedByUser[sessionA]
	if err != nil || decision != sliceprotocol.DecisionAccepted || firstEvidence.AbsenceCount != 1 || firstEvidence.AbsenceDeadline.IsZero() {
		t.Fatalf("first absence decision=%s drop=%+v err=%v", decision, firstEvidence, err)
	}

	// A degraded poll retains the last revision and cannot become another
	// confirmation.
	now = now.Add(time.Second)
	niri.err = &niriipc.ObservationError{Code: sliceprotocol.ReasonNiriReplayTimeout, Err: errors.New("timeout")}
	degradedPoll, err := publisher.Snapshot(context.Background())
	if err != nil || degradedPoll.Observation.Quality != sliceprotocol.QualityDegraded || degradedPoll.Authoritative.Revision != 2 {
		t.Fatalf("degraded producer snapshot=%+v err=%v", degradedPoll, err)
	}
	state, _, decision, err = engine.ApplyEnvelope(degradedPoll, now)
	if drop, ok := state.ClosedByUser[sessionA]; err != nil || decision != sliceprotocol.DecisionDegraded || !ok || !reflect.DeepEqual(drop, firstEvidence) {
		t.Fatalf("degraded poll changed evidence: decision=%s drop=%+v err=%v", decision, drop, err)
	}
	niri.err = nil

	now = now.Add(time.Second)
	absentTwo, err := publisher.Snapshot(context.Background())
	if err != nil || absentTwo.Authoritative.Revision != 3 {
		t.Fatalf("second stable absent producer snapshot=%+v err=%v", absentTwo, err)
	}
	firstHash, _ := sliceprotocol.SemanticHash(*absentOne.Authoritative)
	secondHash, _ := sliceprotocol.SemanticHash(*absentTwo.Authoritative)
	if firstHash != secondHash {
		t.Fatalf("stable absence semantics changed: %s != %s", firstHash, secondHash)
	}
	state, _, decision, err = engine.ApplyEnvelope(absentTwo, now)
	confirmed := state.ClosedByUser[sessionA]
	if err != nil || decision != sliceprotocol.DecisionAccepted || confirmed.AbsenceCount != 2 || !confirmed.AbsenceSince.Equal(firstEvidence.AbsenceSince) || !confirmed.AbsenceDeadline.Equal(firstEvidence.AbsenceDeadline) {
		t.Fatalf("second absence decision=%s drop=%+v err=%v", decision, confirmed, err)
	}

	now = confirmed.AbsenceDeadline.Add(-time.Millisecond)
	state, _, err = engine.Tick()
	if _, ok := state.ClosedByUser[sessionA]; err != nil || !ok {
		t.Fatalf("drop expired before grace: present=%v err=%v", ok, err)
	}
	now = confirmed.AbsenceDeadline.Add(time.Millisecond)
	state, _, err = engine.Tick()
	if _, ok := state.ClosedByUser[sessionA]; err != nil || ok {
		t.Fatalf("drop did not expire after stable confirmations and grace: present=%v err=%v", ok, err)
	}
}

func TestSessionDropPresenceResetsEvidenceAndRejectedObservationsDoNotAdvance(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	epoch := "11111111-1111-4111-8111-111111111111"
	first := envelope(epoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "one", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(first, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	_, _, _ = engine.Close(sourceA)
	now = now.Add(time.Second)
	absent := envelopeWithLive(epoch, 2, nil, []string{}, now)
	state, _, _, _ := engine.ApplyEnvelope(absent, now)
	firstSince := state.ClosedByUser[sessionA].AbsenceSince
	for _, rejected := range []sliceprotocol.Envelope{degraded(absent.Authoritative, now.Add(time.Second)), absent, first} {
		state, _, _, _ = engine.ApplyEnvelope(rejected, now.Add(time.Second))
		if state.ClosedByUser[sessionA].AbsenceCount != 1 || !state.ClosedByUser[sessionA].AbsenceSince.Equal(firstSince) {
			t.Fatalf("rejected observation advanced absence: %+v", state.ClosedByUser[sessionA])
		}
	}
	now = now.Add(2 * time.Second)
	conflicted := envelopeWithLive(epoch, 3, nil, []string{}, now)
	conflicted.Authoritative.Conflicts = []sliceprotocol.Conflict{{Code: sliceprotocol.ConflictSessionMissing, SourceID: sourceA}}
	state, _, decision, err := engine.ApplyEnvelope(conflicted, now)
	if err != nil || decision != sliceprotocol.DecisionAccepted || state.ClosedByUser[sessionA].AbsenceCount != 1 || !state.ClosedByUser[sessionA].AbsenceSince.Equal(firstSince) {
		t.Fatalf("per-source conflict changed drop evidence: decision=%s drop=%+v err=%v", decision, state.ClosedByUser[sessionA], err)
	}
	now = now.Add(time.Second)
	state, _, _, _ = engine.ApplyEnvelope(envelopeWithLive(epoch, 4, nil, []string{sessionA}, now), now)
	if drop := state.ClosedByUser[sessionA]; drop.AbsenceCount != 0 || !drop.AbsenceSince.IsZero() || !drop.AbsenceDeadline.IsZero() {
		t.Fatalf("presence did not reset evidence: %+v", drop)
	}
	now = now.Add(time.Second)
	state, _, _, _ = engine.ApplyEnvelope(envelopeWithLive(epoch, 5, nil, []string{}, now), now)
	if drop := state.ClosedByUser[sessionA]; drop.AbsenceCount != 1 || !drop.AbsenceSince.Equal(now) {
		t.Fatalf("new absence sequence did not restart: %+v", drop)
	}
}

func TestSessionDropRetiredEpochReplayAndSameRevisionConflictPreserveAllEvidence(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	epochA := "11111111-1111-4111-8111-111111111111"
	epochB := "22222222-2222-4222-8222-222222222222"
	_, _, _, _ = engine.ApplyEnvelope(envelope(epochA, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "one", "work")}, now), now)
	_, _, _ = engine.Close(sourceA)
	now = now.Add(time.Second)
	_, _, _, _ = engine.ApplyEnvelope(envelopeWithLive(epochA, 2, nil, []string{}, now), now)
	now = now.Add(time.Second)
	state, _, decision, err := engine.ApplyEnvelope(envelopeWithLive(epochB, 1, nil, []string{}, now), now)
	expected, retained := state.ClosedByUser[sessionA]
	if err != nil || decision != sliceprotocol.DecisionFullResync || !retained || expected.AbsenceCount != 2 || expected.AbsenceSince.IsZero() || expected.AbsenceDeadline.IsZero() {
		t.Fatalf("pending drop setup: decision=%s retained=%v drop=%+v err=%v", decision, retained, expected, err)
	}

	replay := envelopeWithLive(epochA, 99, nil, []string{}, now.Add(time.Second))
	state, _, decision, err = engine.ApplyEnvelope(replay, now.Add(time.Second))
	if actual, ok := state.ClosedByUser[sessionA]; err != nil || decision != sliceprotocol.DecisionReplay || !ok || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("retired replay changed drop: decision=%s retained=%v want=%+v got=%+v err=%v", decision, ok, expected, actual, err)
	}

	conflict := envelopeWithLive(epochB, 1, nil, []string{sessionA}, now.Add(2*time.Second))
	state, _, decision, err = engine.ApplyEnvelope(conflict, now.Add(2*time.Second))
	if actual, ok := state.ClosedByUser[sessionA]; err != nil || decision != sliceprotocol.DecisionConflict || !ok || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("same-revision conflict changed drop: decision=%s retained=%v want=%+v got=%+v err=%v", decision, ok, expected, actual, err)
	}
}

func TestStatusJSONExposesSessionDropEvidence(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	epoch := "11111111-1111-4111-8111-111111111111"
	_, _, _, _ = engine.ApplyEnvelope(envelope(epoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "one", "work")}, now), now)
	_, _, _ = engine.Close(sourceA)
	now = now.Add(time.Second)
	state, _, _, _ := engine.ApplyEnvelope(envelopeWithLive(epoch, 2, nil, []string{}, now), now)
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{sessionA, `"session_name":"one"`, `"absence_count":1`, `"absence_since":`, `"absence_deadline":`} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Fatalf("status lacks %q: %s", want, payload)
		}
	}
}

func TestManualLocalDisappearanceBecomesClosedByUser(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 123)
	_, _, _ = engine.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 123}})
	_, _ = engine.AttachmentConnected(sourceA)
	state, effects, err := engine.ObserveLocal("leech-epoch", nil)
	if _, dropped := state.ClosedByUser[sessionA]; err != nil || !dropped || len(effects) != 0 {
		t.Fatalf("manual disappearance: %#v %#v %v", state.ClosedByUser, effects, err)
	}
}

func TestRetryBudgetPersistsExhaustsAndExplicitReconnect(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	_, _ = engine.RecordLaunch(sourceA, 0, errors.New("start"))
	state, _ := engine.Status()
	expires := state.Sources[sourceA].Recovery.ExpiresAt
	restarted := &Engine{Store: store, Config: engine.Config, Now: func() time.Time { return now }}
	state, _ = restarted.Status()
	if !state.Sources[sourceA].Recovery.ExpiresAt.Equal(expires) {
		t.Fatal("restart reset retry budget")
	}
	now = expires.Add(time.Millisecond)
	state, effects, err := restarted.Tick()
	if err != nil || len(effects) != 0 || state.Sources[sourceA].Connection != ConnectionDisconnected || state.SuccessorGates[sourceA].SessionID != sessionA {
		t.Fatalf("exhaustion: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
	state, effects, err = restarted.Reconnect(sourceA)
	if err != nil || state.Sources[sourceA].Connection != ConnectionReconnecting || len(effects) != 0 || len(state.Projections) != 0 {
		t.Fatalf("explicit reconnect: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
	now = state.Sources[sourceA].Recovery.NextAttemptAt
	state, effects, err = restarted.Tick()
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectLaunchProjection {
		t.Fatalf("scheduled reconnect: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
}

func TestRecoveryDeadlineExpiresWhileSourceIsConflicted(t *testing.T) {
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	engine, _ := newEngine(t, &now)
	epoch := "11111111-1111-4111-8111-111111111111"
	_, _, _, _ = engine.ApplyEnvelope(envelope(epoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	state, err := engine.RecordLaunch(sourceA, 0, errors.New("start recovery"))
	if err != nil || state.Sources[sourceA].Recovery == nil {
		t.Fatalf("start recovery: source=%+v err=%v", state.Sources[sourceA], err)
	}
	deadline := *state.Sources[sourceA].Recovery

	now = now.Add(100 * time.Millisecond)
	conflicted := envelopeWithLive(epoch, 2, nil, []string{sessionA}, now)
	conflicted.Authoritative.Conflicts = []sliceprotocol.Conflict{{Code: sliceprotocol.ConflictSessionMissing, SourceID: sourceA, SessionID: sessionA}}
	state, effects, decision, err := engine.ApplyEnvelope(conflicted, now)
	if err != nil || decision != sliceprotocol.DecisionAccepted || state.Sources[sourceA].Lifecycle != SourceConflict || state.Sources[sourceA].Recovery == nil || *state.Sources[sourceA].Recovery != deadline {
		t.Fatalf("conflict did not retain recovery identity: decision=%s source=%+v effects=%+v err=%v", decision, state.Sources[sourceA], effects, err)
	}

	now = deadline.ExpiresAt.Add(time.Millisecond)
	state, effects, err = engine.Tick()
	wantGate := SuccessorGate{OldSourceID: sourceA, SessionID: sessionA, CreatedAt: now}
	if err != nil || state.Sources[sourceA].Lifecycle != SourceConflict || state.Sources[sourceA].Connection != ConnectionDisconnected || state.Sources[sourceA].Recovery != nil || len(state.SuccessorGates) != 1 || !reflect.DeepEqual(state.SuccessorGates[sourceA], wantGate) {
		t.Fatalf("conflicted expiration was not stable and exactly gated: source=%+v gates=%+v effects=%+v err=%v", state.Sources[sourceA], state.SuccessorGates, effects, err)
	}
	if _, projected := state.Projections[sourceA]; projected {
		t.Fatalf("conflicted expiration retained a projection: %+v", state.Projections[sourceA])
	}
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			t.Fatalf("conflicted expiration launched a projection: %+v", effects)
		}
	}
}

func TestRecoveryDeadlineExpiresWhileSourceIsDeselected(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	engine, _ := newEngine(t, &now)
	epoch := "11111111-1111-4111-8111-111111111111"
	_, _, _, _ = engine.ApplyEnvelope(envelope(epoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	state, err := engine.RecordLaunch(sourceA, 0, errors.New("start recovery"))
	if err != nil || state.Sources[sourceA].Recovery == nil {
		t.Fatalf("start recovery: source=%+v err=%v", state.Sources[sourceA], err)
	}
	deadline := *state.Sources[sourceA].Recovery

	now = now.Add(100 * time.Millisecond)
	state, effects, err := engine.SelectWorkspace("work", false)
	if err != nil || state.Wanted(sourceA) || state.Sources[sourceA].Recovery == nil || *state.Sources[sourceA].Recovery != deadline {
		t.Fatalf("deselection did not retain recovery identity: source=%+v effects=%+v err=%v", state.Sources[sourceA], effects, err)
	}

	now = deadline.ExpiresAt.Add(time.Millisecond)
	state, effects, err = engine.Tick()
	wantGate := SuccessorGate{OldSourceID: sourceA, SessionID: sessionA, CreatedAt: now}
	if err != nil || state.Wanted(sourceA) || state.Sources[sourceA].Connection != ConnectionDisconnected || state.Sources[sourceA].Recovery != nil || len(state.SuccessorGates) != 1 || !reflect.DeepEqual(state.SuccessorGates[sourceA], wantGate) {
		t.Fatalf("deselected expiration was not stable and exactly gated: source=%+v gates=%+v effects=%+v err=%v", state.Sources[sourceA], state.SuccessorGates, effects, err)
	}
	if _, projected := state.Projections[sourceA]; projected {
		t.Fatalf("deselected expiration retained a projection: %+v", state.Projections[sourceA])
	}
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			t.Fatalf("deselected expiration launched a projection: %+v", effects)
		}
	}
}

func TestEpochLineageAndExhaustedSuccessorGate(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	oldEpoch := "11111111-1111-4111-8111-111111111111"
	newEpoch := "22222222-2222-4222-8222-222222222222"
	_, _, _, _ = engine.ApplyEnvelope(envelope(oldEpoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	_, _ = engine.RecordLaunch(sourceA, 0, errors.New("start"))
	now = now.Add(time.Second)
	successor := testSource(sourceB, sessionA, "a", "work")
	state, effects, decision, err := engine.ApplyEnvelope(envelope(newEpoch, 1, []sliceprotocol.Source{successor}, now), now)
	if err != nil || decision != sliceprotocol.DecisionFullResync || state.Sources[sourceB].Recovery == nil || len(effects) > 1 {
		t.Fatalf("lineage: %s %#v %#v %v", decision, state.Sources, effects, err)
	}
	// Exhausted gates block automatic adoption after another epoch.
	now = state.Sources[sourceB].Recovery.ExpiresAt.Add(time.Second)
	state, _, _ = engine.Tick()
	if state.Sources[sourceB].Connection != ConnectionDisconnected {
		t.Fatal("not exhausted")
	}
	thirdEpoch := "33333333-3333-4333-8333-333333333333"
	next := testSource(sourceA, sessionA, "a", "work")
	state, effects, _, _ = engine.ApplyEnvelope(envelope(thirdEpoch, 1, []sliceprotocol.Source{next}, now.Add(time.Second)), now.Add(time.Second))
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			t.Fatalf("gated successor auto-adopted: %#v", effects)
		}
	}
	if !successorGated(state, state.Sources[sourceA]) {
		t.Fatalf("successor gate missing: %#v", state.SuccessorGates)
	}
	state, _, err = engine.Reconnect(sourceA)
	if err != nil || state.Lineage[sourceB].SuccessorSourceID != sourceA || state.Lineage[sourceB].Status != "rebound" {
		t.Fatalf("explicit successor reconnect did not persist lineage: %#v %v", state.Lineage, err)
	}
}

func TestRoutedLaunchHandoffStableIdentity(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	state, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "token-1", Status: "launch_pending", HostTerminalID: "term_1"})
	if err != nil || state.LaunchHandoffs["token-1"].Status != "launch_pending" {
		t.Fatal(err)
	}
	if _, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "token-1", Status: "launched", HostTerminalID: "term_2"}); err == nil {
		t.Fatal("identity conflict accepted")
	}
}

type fakeProc struct {
	exe  map[int]string
	argv map[int][]string
}

func (p fakeProc) Exe(pid int) (string, error) {
	v, ok := p.exe[pid]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (p fakeProc) Cmdline(pid int) ([]string, error) {
	v, ok := p.argv[pid]
	if !ok {
		return nil, errors.New("missing")
	}
	return v, nil
}
func TestPositiveOwnershipIgnoresTitlesAndRejectsPIDOrArgvMismatch(t *testing.T) {
	state := NewState(Namespace{Host: "h", Leech: "l"}, "c")
	p := newProjection(sourceA, "session-a", time.Now())
	p.ExpectedPID = 22
	p.ExpectedKittyExecutable = "/nix/store/x/bin/kitty"
	p.ExpectedKittyArgv = []string{"/nix/store/x/bin/kitty", "--class", p.AppID, "-e", "redeem", "slice", "projection-run", "--source-id", sourceA, "--session", "session-a", "--token", p.AttachToken}
	state.Projections[sourceA] = p
	niri := niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 22, AppID: p.AppID, Title: "spoof", IsFocused: true}}}
	proc := fakeProc{exe: map[int]string{22: "/nix/store/x/bin/kitty"}, argv: map[int][]string{22: append([]string(nil), p.ExpectedKittyArgv...)}}
	owned := VerifyOwnedWindows(state, niri, proc)
	if len(owned) != 1 || !owned[0].Focused {
		t.Fatalf("ownership: %#v", owned)
	}
	niri.Windows[0].PID = 23
	if got := VerifyOwnedWindows(state, niri, proc); len(got) != 0 {
		t.Fatal("wrong PID owned")
	}
	niri.Windows[0].PID = 22
	proc.argv[22] = append(append([]string(nil), p.ExpectedKittyArgv...), "--extra")
	if got := VerifyOwnedWindows(state, niri, proc); len(got) != 0 {
		t.Fatal("near app argv owned")
	}
}

func TestIncompleteLocalProcessEvidenceCannotCloseProjection(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	state, effects, err := engine.ObserveLocalWithConflicts("leech", nil, map[string]string{sourceA: "projection_process_evidence_incomplete"})
	if _, dropped := state.ClosedByUser[sessionA]; err != nil || dropped || state.Sources[sourceA].Lifecycle != SourceConflict || len(effects) != 0 {
		t.Fatalf("incomplete evidence was destructive: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
}

func TestCrashWindowReAdoptsOnlyUniqueExactProjection(t *testing.T) {
	state := NewState(Namespace{Host: "h", Leech: "l"}, "c")
	p := newProjection(sourceA, "session-a", time.Now())
	state.Sources[sourceA] = TrackedSource{SourceID: sourceA, SourceEpoch: "epoch", SessionID: sessionA, SessionName: "session-a", Lifecycle: SourceEligible}
	argv := []string{"/store/bin/kitty", "--class", p.AppID, "-e", "redeem", "slice", "projection-run", "--source-id", sourceA, "--session", "session-a", "--token", p.AttachToken}
	p.ExpectedKittyExecutable = "/store/bin/kitty"
	p.ExpectedKittyArgv = append([]string(nil), argv...)
	state.Projections[sourceA] = p
	proc := fakeProc{exe: map[int]string{22: "/store/bin/kitty", 23: "/store/bin/kitty"}, argv: map[int][]string{22: argv, 23: argv}}
	niri := niriipc.State{Windows: []niriipc.Window{{ID: 1, PID: 22, AppID: p.AppID}}}
	owned, conflicts := VerifyOwnedWindowsWithConflicts(state, niri, proc)
	if len(owned) != 1 || len(conflicts) != 0 {
		t.Fatalf("unique adoption=%#v conflicts=%#v", owned, conflicts)
	}
	niri.Windows = append(niri.Windows, niriipc.Window{ID: 2, PID: 23, AppID: p.AppID})
	owned, conflicts = VerifyOwnedWindowsWithConflicts(state, niri, proc)
	if len(owned) != 0 || conflicts[sourceA] != "projection_ownership_ambiguous" {
		t.Fatalf("ambiguous adoption=%#v conflicts=%#v", owned, conflicts)
	}
}

func TestReadinessRelayRejectsAuthStallAndWrongMarker(t *testing.T) {
	token := "ready_abc"
	for name, input := range map[string]string{"auth_prompt": "Password: ", "wrong_marker": sliceattach.ReadyMarker("ready_wrong"), "stall": ""} {
		t.Run(name, func(t *testing.T) {
			reader, writer := io.Pipe()
			ready := make(chan struct{})
			done := make(chan error, 1)
			var output bytes.Buffer
			go func() { done <- RelayAttachReadiness(reader, &output, token, ready) }()
			if input != "" {
				_, _ = writer.Write([]byte(input))
			}
			select {
			case <-ready:
				t.Fatal("false readiness accepted")
			case <-time.After(20 * time.Millisecond):
			}
			_ = writer.Close()
			<-done
		})
	}
	ready := make(chan struct{})
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- RelayAttachReadiness(strings.NewReader("banner\n"+strings.Replace(sliceattach.ReadyMarker(token), "\n", "\r\n", 1)+"terminal\n"), &out, token, ready)
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("exact marker not recognized")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ATTACH_READY") || !strings.Contains(out.String(), "banner") || !strings.Contains(out.String(), "terminal") {
		t.Fatalf("relay output=%q", out.String())
	}
}

func TestProjectionCommandExactArgvAndCleanEnvironment(t *testing.T) {
	source := TrackedSource{SourceID: sourceA, SessionName: "session-a"}
	p := newProjection(sourceA, "session-a", time.Now())
	cfg := ProjectionCommandConfig{KittyCommand: "/kitty", SelfCommand: "/redeem", TransportCommand: "/ssh", SourceHost: "host", ControlSocket: "/tmp/control.sock", RemoteSelfCommand: "/remote/redeem", TransportOptions: []string{"-p", "22"}, GraphicalContext: map[string]string{"NIRI_SOCKET": "/run/niri.sock", "WAYLAND_DISPLAY": "wayland-1", "XDG_RUNTIME_DIR": "/run/user/1"}}
	plan, err := BuildProjectionCommand(cfg, source, p)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.KittyArgs, " ")
	for _, part := range []string{"--class " + p.AppID, "slice projection-run", "--session session-a", "--token " + p.AttachToken} {
		if !strings.Contains(joined, part) {
			t.Fatalf("missing %q: %s", part, joined)
		}
	}
	if len(plan.Environment) != 3 {
		t.Fatalf("environment leaked: %#v", plan.Environment)
	}
	if strings.Contains(joined, "sh -c") || strings.Contains(joined, "watch") || strings.Contains(joined, "attach-or-create") {
		t.Fatalf("unsafe argv: %s", joined)
	}
	cfg.TransportOptions = make([]string, MaxProjectionTransportOptions+1)
	for i := range cfg.TransportOptions {
		cfg.TransportOptions[i] = "-v"
	}
	if _, err := BuildProjectionCommand(cfg, source, p); err == nil {
		t.Fatal("unbounded transport option count accepted by projection builder")
	}
}

func TestProjectionTransportBudgetCoversWorstCaseGeneratedArgv(t *testing.T) {
	if MaxProjectionFixedGeneratedArgvBytes+MaxProjectionTransportOptionBytes != MaxProjectionArgvTotalBytes {
		t.Fatal("shared projection budgets drifted")
	}
	source := TrackedSource{SourceID: strings.Repeat("s", 128), SessionName: strings.Repeat("n", 128)}
	p := newProjection(source.SourceID, source.SessionName, time.Now())
	p.AppID = strings.Repeat("a", MaxProjectionArgvEntryBytes)
	p.AttachToken = strings.Repeat("t", 128)
	p.ProcessSourceID = source.SourceID
	p.ExpectedSessionName = source.SessionName
	long := strings.Repeat("p", MaxProjectionArgvEntryBytes)
	options := make([]string, MaxProjectionTransportOptions)
	for i := range options {
		options[i] = strings.Repeat("o", MaxProjectionTransportOptionBytes/MaxProjectionTransportOptions)
	}
	cfg := ProjectionCommandConfig{KittyCommand: long, SelfCommand: long, TransportCommand: long, SourceHost: long, ControlSocket: long, RemoteSelfCommand: long, TransportOptions: options, GraphicalContext: map[string]string{"NIRI_SOCKET": "/niri", "WAYLAND_DISPLAY": "wayland-1", "XDG_RUNTIME_DIR": "/run/user/1"}}
	plan, err := BuildProjectionCommand(cfg, source, p)
	if err != nil {
		t.Fatalf("reserved fixed/path boundary insufficient: %v", err)
	}
	total := len(plan.KittyCommand)
	for _, entry := range plan.KittyArgs {
		total += len(entry)
	}
	if total > MaxProjectionArgvTotalBytes {
		t.Fatalf("generated argv=%d exceeds=%d", total, MaxProjectionArgvTotalBytes)
	}
}

func TestControlStrictSocketSerialization(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- ServeControl(ctx, store.SocketPath(), time.Second, ControlHandler{Engine: engine}) }()
	deadline := time.Now().Add(time.Second)
	for {
		if info, err := os.Stat(store.SocketPath()); err == nil && info.Mode().Perm() == 0o600 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket not ready with mode 0600")
		}
		time.Sleep(time.Millisecond)
	}
	info, _ := os.Stat(store.SocketPath())
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %o", info.Mode().Perm())
	}
	response, err := CallControl(ctx, store.SocketPath(), time.Second, NewControlRequest(VerbStatus, struct{}{}))
	if err != nil || response.Outcome.Status != "ok" || response.State == nil {
		t.Fatalf("status: %#v %v", response, err)
	}
	conn, err := net.Dial("unix", store.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte(`{"schema_version":1,"schema_version":1,"request_id":"x","verb":"status"}`))
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	var bad ControlResponse
	if err := json.NewDecoder(conn).Decode(&bad); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if bad.Outcome.Code != "invalid_request" {
		t.Fatalf("duplicate key accepted: %#v", bad)
	}
	cancel()
	<-errCh
}

func TestSpatialOriginPersistedBeforeEffect(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	result := slicelayout.Result{Status: slicelayout.PlanComplete, Proposals: []slicelayout.Proposal{{Target: slicelayout.Leech, SourceID: sourceA, RuntimeWindowID: 9, TargetCompositorEpoch: "leech", Origin: slicelayout.Origin{ControllerID: "c", Generation: 2, From: slicelayout.Host, Mode: "host_location"}, VerifyAfterWrite: true, Changes: []slicelayout.Change{{Kind: slicelayout.ChangeWidth, Percent: 50}}}}}
	state, effects, err := engine.RecordSpatial(sourceA, result)
	if err != nil || len(effects) != 1 || state.Spatial[sourceA].LastApplied == nil {
		t.Fatalf("origin not persisted: %#v %#v %v", state.Spatial, effects, err)
	}
	if _, err := engine.CompleteSpatial(sourceA); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeControlRejectsHostileMetadataAndBounds(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`{"schema_version":1,"request_id":"x","verb":"status","extra":1}`), []byte(`{"schema_version":1,"request_id":"x\n","verb":"status"}`), bytes.Repeat([]byte("x"), MaxControlBytes+1)} {
		if _, err := DecodeControlRequest(bytes.NewReader(payload)); err == nil {
			t.Fatalf("accepted hostile request of %d bytes", len(payload))
		}
	}
}

func TestZeroSuccessorEpochReplacementRecordsUnresolvedAndNeverRelaunchesOld(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	oldEpoch := "11111111-1111-4111-8111-111111111111"
	_, _, _, _ = engine.ApplyEnvelope(envelope(oldEpoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	_, _ = engine.RecordLaunch(sourceA, 0, errors.New("start"))
	now = now.Add(time.Second)
	state, effects, _, err := engine.ApplyEnvelope(envelope("22222222-2222-4222-8222-222222222222", 1, []sliceprotocol.Source{}, now), now)
	if err != nil || state.Lineage[sourceA].Status != "unresolved" || state.Sources[sourceA].Lifecycle != SourceReplaced {
		t.Fatalf("state=%#v lineage=%#v err=%v", state.Sources[sourceA], state.Lineage, err)
	}
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection && effect.SourceID == sourceA {
			t.Fatalf("invalid old source relaunched: %#v", effects)
		}
	}
}

func TestCleanupGateBlocksDistinctNewLaunchUntilCompletion(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	oldEpoch := "11111111-1111-4111-8111-111111111111"
	_, _, _, _ = engine.ApplyEnvelope(envelope(oldEpoch, 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	next := testSource(sourceB, sessionB, "b", "work")
	now = now.Add(time.Second)
	state, effects, _, err := engine.ApplyEnvelope(envelope("22222222-2222-4222-8222-222222222222", 1, []sliceprotocol.Source{next}, now), now)
	if err != nil || state.PendingCleanups[sourceA].SourceID == "" {
		t.Fatalf("cleanup gate=%#v err=%v", state.PendingCleanups, err)
	}
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			t.Fatalf("new launch escaped cleanup gate: %#v", effects)
		}
	}
	if _, _, err := engine.Reconnect(sourceB); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("explicit reconnect bypassed cleanup: %v", err)
	}
	state, _ = engine.Status()
	retrying := state.Sources[sourceB]
	retrying.Connection = ConnectionReconnecting
	engine.startRecovery(&retrying, now)
	state.Sources[sourceB] = retrying
	if err := store.Write(state); err != nil {
		t.Fatal(err)
	}
	now = retrying.Recovery.NextAttemptAt
	state, effects, _ = engine.Tick()
	if _, exists := state.Projections[sourceB]; exists {
		t.Fatal("Tick retry created mapping while cleanup pending")
	}
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			t.Fatalf("Tick retry bypassed cleanup: %#v", effects)
		}
	}
	if _, err := engine.RecordCleanupFailure(sourceA, "ownership_unproven"); err != nil {
		t.Fatal(err)
	}
	state, effects, _ = engine.Tick()
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection {
			t.Fatalf("failed cleanup allowed launch")
		}
	}
	if _, err := engine.CompleteCleanup(sourceA); err != nil {
		t.Fatal(err)
	}
	state, effects, _ = engine.Tick()
	found := false
	for _, effect := range effects {
		if effect.Kind == EffectLaunchProjection && effect.SourceID == sourceB {
			found = true
		}
	}
	if !found {
		t.Fatalf("new source not released after cleanup: state=%#v effects=%#v", state.PendingCleanups, effects)
	}
}

func TestReconnectRejectsIneligibleOrUndesiredSources(t *testing.T) {
	now := time.Now().UTC()
	for name, mutate := range map[string]func(*State){"unselected": func(*State) {}, "conflict": func(s *State) {
		source := s.Sources[sourceA]
		source.Lifecycle = SourceConflict
		s.Sources[sourceA] = source
	}, "replaced": func(s *State) {
		source := s.Sources[sourceA]
		source.Lifecycle = SourceReplaced
		s.Sources[sourceA] = source
	}, "closed": func(s *State) {
		s.ClosedByUser[sessionA] = SessionDrop{SessionID: sessionA, SessionName: "one", CreatedAt: now}
	}} {
		t.Run(name, func(t *testing.T) {
			engine, store := newEngine(t, &now)
			_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
			state, _ := engine.Status()
			mutate(&state)
			if name != "unselected" {
				state.SelectedWorkspaces["work"] = "work"
			}
			if err := store.Write(state); err != nil {
				t.Fatal(err)
			}
			before, _ := engine.Status()
			if _, _, err := engine.Reconnect(sourceA); err == nil {
				t.Fatal("invalid reconnect accepted")
			}
			after, _ := engine.Status()
			if len(after.Projections) != len(before.Projections) {
				t.Fatal("rejected reconnect created mapping")
			}
		})
	}
}

func TestDegradedHostCannotTurnLocalLossIntoManualClose(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	env := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now)
	_, _, _, _ = engine.ApplyEnvelope(env, now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	_, _, _ = engine.ObserveLocal("leech", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 22}})
	_, _ = engine.AttachmentConnected(sourceA)
	_, _ = engine.RecordObservationFailure("transport_disconnected")
	state, effects, err := engine.ObserveLocal("leech", nil)
	if _, dropped := state.ClosedByUser[sessionA]; err != nil || dropped || len(effects) != 0 {
		t.Fatalf("degraded loss became manual close: %#v %#v %v", state.ClosedByUser, effects, err)
	}
	conflict := envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "changed", "work")}, now)
	state, _, decision, err := engine.ApplyEnvelope(conflict, now.Add(time.Second))
	if err != nil || decision != sliceprotocol.DecisionConflict || state.ObservationQuality != sliceprotocol.QualityDegraded {
		t.Fatalf("conflicting snapshot retained destructive health: %s %#v %v", decision, state, err)
	}
	state, effects, err = engine.ObserveLocal("leech", nil)
	if _, dropped := state.ClosedByUser[sessionA]; err != nil || dropped || len(effects) != 0 {
		t.Fatalf("conflicting authority allowed manual close: %#v %#v %v", state.ClosedByUser, effects, err)
	}
}

func TestV1RejectsHostTargetSpatialProposal(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	proposal := slicelayout.Proposal{Target: slicelayout.Host, SourceID: sourceA, RuntimeWindowID: 42, TargetCompositorEpoch: "epoch", Origin: slicelayout.Origin{ControllerID: "controller", Generation: 2, From: slicelayout.Leech, Mode: "host_location"}, VerifyAfterWrite: true, Changes: []slicelayout.Change{{Kind: slicelayout.ChangeWidth, Percent: 50}}}
	if _, effects, err := engine.RecordSpatial(sourceA, slicelayout.Result{Status: slicelayout.PlanComplete, Proposals: []slicelayout.Proposal{proposal}}); err == nil || len(effects) != 0 {
		t.Fatalf("host-target proposal accepted: effects=%+v err=%v", effects, err)
	}
}

func TestSpatialFailureClearsOriginAndRetriesBoundedly(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	proposal := slicelayout.Proposal{Target: slicelayout.Leech, SourceID: sourceA, RuntimeWindowID: 42, TargetCompositorEpoch: "epoch", Origin: slicelayout.Origin{ControllerID: "controller", Generation: 2, From: slicelayout.Host, Mode: "host_location"}, VerifyAfterWrite: true, Changes: []slicelayout.Change{{Kind: slicelayout.ChangeWidth, Percent: 50}}}
	_, _, _ = engine.RecordSpatial(sourceA, slicelayout.Result{Status: slicelayout.PlanComplete, Proposals: []slicelayout.Proposal{proposal}})
	state, err := engine.RecordSpatialFailure(sourceA, "spatial_execution_failed")
	if err != nil || state.Spatial[sourceA].LastApplied != nil || state.Spatial[sourceA].Recovery == nil || state.Spatial[sourceA].Recovery.Stable || state.AuthorityMode != "host_location" {
		t.Fatalf("first failure=%#v err=%v", state.Spatial[sourceA], err)
	}
	now = state.Spatial[sourceA].Recovery.NextAttemptAt
	proposal.Origin.Generation = 3
	state, _, err = engine.RecordSpatial(sourceA, slicelayout.Result{Status: slicelayout.PlanComplete, Proposals: []slicelayout.Proposal{proposal}})
	if err != nil || state.Spatial[sourceA].LastApplied == nil {
		t.Fatalf("transient failure wedged next origin: %#v %v", state.Spatial[sourceA], err)
	}
	state, _ = engine.RecordSpatialFailure(sourceA, "spatial_execution_failed")
	if !state.Spatial[sourceA].Recovery.Stable || state.Spatial[sourceA].Conflict != "spatial_retry_exhausted" {
		t.Fatalf("retry did not exhaust: %#v", state.Spatial[sourceA])
	}
}

func TestLaunchedHandoffExplicitReplayRestartsDisconnectedExactSource(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	state, _ := store.Read()
	key, _ := sliceprotocol.NormalizeWorkspaceName("Work")
	epoch := "11111111-1111-4111-8111-111111111111"
	routedSource, _ := sourceinventory.SourceID(epoch, 42)
	state.SelectedWorkspaces[key] = "Work"
	token := "route-token"
	sessionName := slicerpc.StableSessionName(token)
	state.Sources[routedSource] = TrackedSource{SourceID: routedSource, SourceEpoch: epoch, SessionID: sessionA, SessionName: sessionName, WorkspaceKey: key, Lifecycle: SourceEligible, Connection: ConnectionDisconnected}
	name := "Work"
	state.Inventory = &sliceprotocol.Authoritative{SourceEpoch: epoch, Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{sessionA}, Sources: []sliceprotocol.Source{{SourceID: routedSource, RuntimeWindowID: 42, Session: sliceprotocol.Session{ID: sessionA, Name: sessionName, Status: "active"}, Workspace: sliceprotocol.Workspace{RuntimeID: 2, Name: name, Key: key}, Output: &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1, LogicalHeight: 1, Scale: 1, Transform: "normal"}, Layout: sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: 1, Tile: 1}, TileWidth: 1, TileHeight: 1, WindowWidth: 1, WindowHeight: 1}}}, Conflicts: []sliceprotocol.Conflict{}}
	hash, _ := sliceprotocol.SemanticHash(*state.Inventory)
	state.Acceptance = sliceprotocol.AcceptanceState{SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceEpoch: epoch, Revision: 1, SemanticHash: hash}
	if err := store.Write(state); err != nil {
		t.Fatal(err)
	}
	var got []Effect
	handler := ControlHandler{Engine: engine, Execute: func(_ context.Context, effects []Effect) error { got = append(got, effects...); return nil }}
	handoff := LaunchHandoff{Token: token, Status: "launched", HostTerminalID: "term_route", SessionName: sessionName, WorkspaceName: "Work", SourceID: routedSource, SourceEpoch: epoch, RuntimeWindowID: 42}
	response := handler.Handle(context.Background(), NewControlRequest(VerbLaunchHandoff, handoff))
	if response.Outcome.Status != "ok" || len(got) != 0 || response.State == nil || response.State.Sources[routedSource].Connection != ConnectionReconnecting || response.State.Sources[routedSource].Recovery == nil {
		t.Fatalf("response=%+v effects=%+v", response, got)
	}
}

func TestLaunchedHandoffMismatchedAuthorityRemainsPendingWithoutReconnect(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	state, _ := store.Read()
	epoch := "11111111-1111-4111-8111-111111111111"
	id, _ := sourceinventory.SourceID(epoch, 42)
	key, _ := sliceprotocol.NormalizeWorkspaceName("Other")
	state.Sources[id] = TrackedSource{SourceID: id, SourceEpoch: epoch, SessionID: sessionA, SessionName: "different", WorkspaceKey: key, Lifecycle: SourceEligible, Connection: ConnectionDisconnected}
	if err := store.Write(state); err != nil {
		t.Fatal(err)
	}
	handler := ControlHandler{Engine: engine}
	handoff := LaunchHandoff{Token: "mismatch", Status: "launched", HostTerminalID: "term_x", SessionName: slicerpc.StableSessionName("mismatch"), WorkspaceName: "Work", SourceID: id, SourceEpoch: epoch, RuntimeWindowID: 42}
	response := handler.Handle(context.Background(), NewControlRequest(VerbLaunchHandoff, handoff))
	if response.Outcome.Status != "ok" || response.State.LaunchHandoffs["mismatch"].Status != "launch_pending" || response.State.Sources[id].Connection != ConnectionDisconnected {
		t.Fatalf("response=%+v", response)
	}
}

func TestDefiniteNoncreationTerminallyResolvesPendingHandoffWithoutSourceAction(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	sessionName := slicerpc.StableSessionName("no-create")
	if _, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "no-create", Status: "launch_pending", SessionName: sessionName, WorkspaceName: "Work"}); err != nil {
		t.Fatal(err)
	}
	state, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "no-create", Status: "not_created", SessionName: sessionName, WorkspaceName: "Work"})
	if err != nil || state.LaunchHandoffs["no-create"].Status != "not_created" || state.LaunchHandoffs["no-create"].HostTerminalID != "" {
		t.Fatalf("state=%+v err=%v", state.LaunchHandoffs["no-create"], err)
	}
	if _, err = engine.SetLaunchHandoff(LaunchHandoff{Token: "no-create", Status: "launch_pending"}); err == nil {
		t.Fatal("terminal noncreation regressed")
	}
}

func TestLaunchHandoffTransitionsAreMonotonic(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	if _, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "tok", Status: "launch_pending", HostTerminalID: "term_a"}); err != nil {
		t.Fatal(err)
	}
	state, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "tok", Status: "launch_pending"})
	if err != nil || state.LaunchHandoffs["tok"].HostTerminalID != "term_a" {
		t.Fatalf("identity erased: %#v %v", state.LaunchHandoffs["tok"], err)
	}
	if _, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "tok", Status: "launched", HostTerminalID: "term_a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "tok", Status: "launch_pending", HostTerminalID: "term_a"}); err == nil {
		t.Fatal("launched status regressed")
	}
	if _, err := engine.SetLaunchHandoff(LaunchHandoff{Token: "tok2", Status: "failed"}); err == nil {
		t.Fatal("resolved status without identity accepted")
	}
}

func TestStartupCreatesAndExhaustsBoundedRecoveryForOrdinaryConnectedState(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	state, _ := engine.Status()
	source := testSource(sourceA, sessionA, "a", "work")
	state.Sources[sourceA] = TrackedSource{SourceID: sourceA, SourceEpoch: "epoch", SessionID: sessionA, SessionName: "a", WorkspaceKey: "work", Lifecycle: SourceEligible, Connection: ConnectionConnected}
	state.SelectedWorkspaces["work"] = "work"
	state.Inventory = &sliceprotocol.Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{sessionA}, Sources: []sliceprotocol.Source{source}, Conflicts: []sliceprotocol.Conflict{}}
	state.Acceptance.SourceEpoch = state.Inventory.SourceEpoch
	state.Acceptance.Revision = 1
	state.Acceptance.SemanticHash, _ = sliceprotocol.SemanticHash(*state.Inventory)
	if err := store.Write(state); err != nil {
		t.Fatal(err)
	}
	state, err := engine.PrepareStartup()
	recovery := state.Sources[sourceA].Recovery
	if err != nil || state.Sources[sourceA].Connection != ConnectionReconnecting || recovery == nil || !recovery.ExpiresAt.After(recovery.StartedAt) {
		t.Fatalf("startup recovery=%#v err=%v", state.Sources[sourceA], err)
	}
	now = recovery.ExpiresAt.Add(time.Millisecond)
	state, effects, err := engine.Tick()
	if err != nil || len(effects) != 0 || state.Sources[sourceA].Connection != ConnectionDisconnected || state.Sources[sourceA].Recovery != nil {
		t.Fatalf("startup recovery did not exhaust: %#v %#v %v", state.Sources[sourceA], effects, err)
	}
}

func TestStartupDowngradesConnectedWithoutRefreshingRecovery(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	state, _ := engine.Status()
	source := testSource(sourceA, sessionA, "a", "work")
	state.Sources[sourceA] = TrackedSource{SourceID: sourceA, SourceEpoch: "epoch", SessionID: sessionA, SessionName: "a", WorkspaceKey: "work", Lifecycle: SourceEligible, Connection: ConnectionConnected, Recovery: &Recovery{Generation: 1, StartedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Second), NextAttemptAt: now}}
	state.SelectedWorkspaces["work"] = "work"
	state.Inventory = &sliceprotocol.Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{sessionA}, Sources: []sliceprotocol.Source{source}, Conflicts: []sliceprotocol.Conflict{}}
	state.Acceptance.SourceEpoch = state.Inventory.SourceEpoch
	state.Acceptance.Revision = 1
	state.Acceptance.SemanticHash, _ = sliceprotocol.SemanticHash(*state.Inventory)
	if err := store.Write(state); err != nil {
		t.Fatal(err)
	}
	before := state.Sources[sourceA].Recovery.ExpiresAt
	state, err := engine.PrepareStartup()
	if err != nil || state.Sources[sourceA].Connection != ConnectionReconnecting || !state.Sources[sourceA].Recovery.ExpiresAt.Equal(before) {
		t.Fatalf("startup=%#v err=%v", state.Sources[sourceA], err)
	}
}

type fakeLocalNiri struct {
	state   niriipc.State
	states  []niriipc.State
	next    int
	actions []any
}

func (f *fakeLocalNiri) Snapshot(context.Context) (niriipc.State, error) {
	if len(f.states) > 0 {
		index := f.next
		if index >= len(f.states) {
			index = len(f.states) - 1
		}
		f.next++
		return f.states[index], nil
	}
	return f.state, nil
}
func (f *fakeLocalNiri) Action(_ context.Context, action any) error {
	f.actions = append(f.actions, action)
	return nil
}

func TestFocusedCloseRollbackRestoresOnlyNewIntentAndPreservesPriorExclusion(t *testing.T) {
	now := time.Now().UTC()
	engine, _ := newEngine(t, &now)
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	state, _, _ := engine.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 22, AppID: stateProjectionApp(t, engine, sourceA), Focused: true}})
	state, _ = engine.AttachmentConnected(sourceA)
	beforeSource := state.Sources[sourceA]
	beforeProjection := state.Projections[sourceA]
	beforeUndo := append([]UndoAction(nil), state.Undo...)

	committed, effects, token, err := engine.CloseFocused(sourceA)
	if err != nil || token == nil || len(effects) != 1 || !effects[0].FocusRequired || committed.ClosedByUser[sessionA].SessionID != sessionA || committed.Projections[sourceA].Status != ProjectionClosing || committed.Sources[sourceA].Connection != "" || len(committed.Undo) != len(beforeUndo)+1 {
		t.Fatalf("focused close was not durable before effect: state=%+v effects=%+v token=%+v err=%v", committed, effects, token, err)
	}
	rolledBack, err := engine.RollbackFocusedClose(token)
	if err != nil || len(rolledBack.ClosedByUser) != 0 || !reflect.DeepEqual(rolledBack.Sources[sourceA], beforeSource) || !reflect.DeepEqual(rolledBack.Projections[sourceA], beforeProjection) || !reflect.DeepEqual(rolledBack.Undo, beforeUndo) {
		t.Fatalf("focused rollback retained or changed intent: state=%+v err=%v", rolledBack, err)
	}

	prior, _, err := engine.Close(sourceA)
	if err != nil || prior.ClosedByUser[sessionA].SessionID != sessionA {
		t.Fatalf("generic prior close failed: state=%+v err=%v", prior, err)
	}
	priorGeneration := prior.Generation
	priorUndo := append([]UndoAction(nil), prior.Undo...)
	unchanged, effects, token, err := engine.CloseFocused(sourceA)
	if err != nil || token != nil || len(effects) != 0 || unchanged.Generation != priorGeneration || unchanged.ClosedByUser[sessionA].SessionID != sessionA || !reflect.DeepEqual(unchanged.Undo, priorUndo) {
		t.Fatalf("focused close altered prior exclusion: state=%+v effects=%+v token=%+v err=%v", unchanged, effects, token, err)
	}
}

func TestFocusedFallbackReprovesAfterLockAndRejectsReusedID(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	state, _ := engine.Status()
	p := state.Projections[sourceA]
	client := &fakeLocalNiri{state: niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 99, AppID: "unrelated", IsFocused: true}}}}
	proc := fakeProc{exe: map[int]string{99: "/store/bin/other"}, argv: map[int][]string{99: {"other"}}}
	err := FocusedCloseFallback(context.Background(), store, engine.Config, client, proc, OwnedWindow{SourceID: sourceA, WindowID: 9, PID: 22, AppID: p.AppID, Focused: true}, time.Millisecond)
	if err == nil || len(client.actions) != 0 {
		t.Fatalf("reused ID closed: actions=%#v err=%v", client.actions, err)
	}
	state, _ = engine.Status()
	if _, dropped := state.ClosedByUser[sessionA]; dropped {
		t.Fatal("failed reproof committed close")
	}
}

func TestFocusedFallbackRollsBackWhenFocusChangesAfterCommit(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	state, _, _ := engine.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 22, AppID: stateProjectionApp(t, engine, sourceA), Focused: true}})
	state, _ = engine.AttachmentConnected(sourceA)
	mapping := state.Projections[sourceA]
	beforeUndo := len(state.Undo)
	focused := niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 22, AppID: mapping.AppID, IsFocused: true}}}
	unfocused := niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 22, AppID: mapping.AppID, IsFocused: false}}}
	client := &fakeLocalNiri{states: []niriipc.State{focused, unfocused}}
	proc := fakeProc{exe: map[int]string{22: mapping.ExpectedKittyExecutable}, argv: map[int][]string{22: mapping.ExpectedKittyArgv}}
	err := FocusedCloseFallback(context.Background(), store, engine.Config, client, proc, OwnedWindow{SourceID: sourceA, WindowID: 9, PID: 22, AppID: mapping.AppID, Focused: true}, time.Millisecond)
	if err == nil || len(client.actions) != 0 {
		t.Fatalf("post-commit focus race closed: actions=%#v err=%v", client.actions, err)
	}
	state, _ = engine.Status()
	if len(state.ClosedByUser) != 0 || state.Projections[sourceA].Status != ProjectionOwned || state.Sources[sourceA].Connection != ConnectionConnected || len(state.Undo) != beforeUndo {
		t.Fatalf("fallback focus failure retained close intent: closed=%+v projection=%+v source=%+v undo=%+v", state.ClosedByUser, state.Projections[sourceA], state.Sources[sourceA], state.Undo)
	}
}

func TestLeechSpatialReproofRejectsReusedIDWithoutAction(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	_, _, _ = engine.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 22}})
	proposal := slicelayout.Proposal{Target: slicelayout.Leech, TargetCompositorEpoch: "leech-epoch", SourceID: sourceA, RuntimeWindowID: 9, Origin: slicelayout.Origin{ControllerID: "controller", Generation: 2, From: slicelayout.Host, Mode: "host_location"}, VerifyAfterWrite: true, Changes: []slicelayout.Change{{Kind: slicelayout.ChangeWidth, Percent: 50}}}
	_, _, _ = engine.RecordSpatial(sourceA, slicelayout.Result{Status: slicelayout.PlanComplete, Proposals: []slicelayout.Proposal{proposal}})
	client := &fakeLocalNiri{state: niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 99, AppID: "unrelated"}}}}
	proc := fakeProc{exe: map[int]string{99: "/store/bin/other"}, argv: map[int][]string{99: {"other"}}}
	if err := ExecuteLeechSpatial(context.Background(), store, client, proc, proposal, "leech-epoch", time.Millisecond); err == nil || len(client.actions) != 0 {
		t.Fatalf("reused spatial ID mutated: actions=%#v err=%v", client.actions, err)
	}
}

func TestLeechSpatialReprovesBeforeEveryIndividualAction(t *testing.T) {
	now := time.Now().UTC()
	engine, store := newEngine(t, &now)
	_, _, _, _ = engine.ApplyEnvelope(envelope("11111111-1111-4111-8111-111111111111", 1, []sliceprotocol.Source{testSource(sourceA, sessionA, "a", "work")}, now), now)
	_, _, _ = engine.SelectWorkspace("work", true)
	prepareSuccessfulLaunch(t, engine, sourceA, 22)
	_, _, _ = engine.ObserveLocal("leech-epoch", []OwnedWindow{{SourceID: sourceA, WindowID: 9, PID: 22}})
	state, _ := engine.Status()
	mapping := state.Projections[sourceA]
	proposal := slicelayout.Proposal{Target: slicelayout.Leech, TargetCompositorEpoch: "leech-epoch", SourceID: sourceA, RuntimeWindowID: 9, Origin: slicelayout.Origin{ControllerID: "controller", Generation: 2, From: slicelayout.Host, Mode: "host_location"}, VerifyAfterWrite: true, Changes: []slicelayout.Change{{Kind: slicelayout.ChangeWidth, Percent: 50}, {Kind: slicelayout.ChangeHeight, Percent: 50}}}
	_, _, _ = engine.RecordSpatial(sourceA, slicelayout.Result{Status: slicelayout.PlanComplete, Proposals: []slicelayout.Proposal{proposal}})
	valid := niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 22, AppID: mapping.AppID}}}
	reused := niriipc.State{Windows: []niriipc.Window{{ID: 9, PID: 99, AppID: "unrelated"}}}
	client := &fakeLocalNiri{states: []niriipc.State{valid, valid, reused}}
	proc := fakeProc{exe: map[int]string{22: mapping.ExpectedKittyExecutable, 99: "/store/bin/other"}, argv: map[int][]string{22: mapping.ExpectedKittyArgv, 99: {"other"}}}
	err := ExecuteLeechSpatial(context.Background(), store, client, proc, proposal, "leech-epoch", time.Millisecond)
	if err == nil || len(client.actions) != 1 {
		t.Fatalf("second action escaped fresh reproof: actions=%#v err=%v", client.actions, err)
	}
}

func TestControllerCompactionBoundsTerminalChurnAndPreservesActive(t *testing.T) {
	state := NewState(Namespace{Host: "h", Leech: "l"}, "controller")
	epoch := "11111111-1111-4111-8111-111111111111"
	for i := 0; i < MaxTerminalSources+100; i++ {
		suffix := fmt.Sprintf("%043d", i)
		id := "src_" + suffix
		state.Sources[id] = TrackedSource{SourceID: id, SourceEpoch: epoch, SessionID: "ses_" + suffix, SessionName: fmt.Sprintf("s%d", i), Lifecycle: SourceClosed}
	}
	active := "src_" + fmt.Sprintf("%043d", MaxTerminalSources+99)
	state.Pickups[active] = true
	if err := state.Compact(); err != nil {
		t.Fatal(err)
	}
	if len(state.Sources) > MaxTerminalSources || state.Sources[active].SourceID == "" {
		t.Fatalf("compaction len=%d active=%v", len(state.Sources), state.Sources[active])
	}
	for i := 0; i < MaxLaunchHandoffs+20; i++ {
		token := fmt.Sprintf("tok_%d", i)
		state.LaunchHandoffs[token] = LaunchHandoff{Token: token, Status: "launched", HostTerminalID: fmt.Sprintf("term_%d", i), UpdatedAt: time.Unix(int64(i+1), 0)}
	}
	state.LaunchHandoffs["pending"] = LaunchHandoff{Token: "pending", Status: "launch_pending", UpdatedAt: time.Now()}
	if err := state.Compact(); err != nil {
		t.Fatal(err)
	}
	if len(state.LaunchHandoffs) > MaxLaunchHandoffs || state.LaunchHandoffs["pending"].Token == "" || !retiredHandoffContains(state, "tok_0") || retiredHandoffContains(state, "never_used") {
		t.Fatal("handoff compaction lost exact pending or replay tombstone authority")
	}
	full := NewState(Namespace{Host: "h", Leech: "l"}, "controller")
	for i := 0; i < MaxRetiredHandoffTombstones; i++ {
		full.RetiredHandoffTokens = append(full.RetiredHandoffTokens, fmt.Sprintf("retired_%04d", i))
	}
	for i := 0; i <= MaxLaunchHandoffs; i++ {
		token := fmt.Sprintf("active_%04d", i)
		full.LaunchHandoffs[token] = LaunchHandoff{Token: token, Status: "launched", HostTerminalID: fmt.Sprintf("term_%04d", i), UpdatedAt: time.Unix(int64(i+1), 0)}
	}
	if err := full.Compact(); err == nil || !strings.Contains(err.Error(), "maintenance/re-enrollment") {
		t.Fatalf("exact handoff tombstone exhaustion did not fail explicitly: %v", err)
	}
	for i := 0; i <= MaxSuccessorGates; i++ {
		id := fmt.Sprintf("old_%d", i)
		state.SuccessorGates[id] = SuccessorGate{OldSourceID: id, SessionID: "session", CreatedAt: time.Now()}
	}
	if err := state.Compact(); err == nil {
		t.Fatal("non-prunable gate overflow did not fail safe")
	}
}

func TestStateJSONDoesNotContainTitlesSocketsOrProcessArgs(t *testing.T) {
	state := NewState(Namespace{Host: "h", Leech: "l"}, "c")
	payload, _ := json.Marshal(state)
	for _, secret := range []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "/proc/", "title"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("private evidence serialized: %s", secret)
		}
	}
	if reflect.DeepEqual(payload, []byte{}) {
		t.Fatal("empty state")
	}
}
