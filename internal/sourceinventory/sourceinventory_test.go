package sourceinventory

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type fakeProcesses struct {
	values map[int]zellijlive.ProcessEvidence
	err    error
}

func (fake fakeProcesses) Observe(_ context.Context, pid int) (zellijlive.ProcessEvidence, error) {
	return fake.values[pid], fake.err
}

type fakeNiri struct {
	state niriipc.State
	err   error
}

func (fake *fakeNiri) Snapshot(context.Context) (niriipc.State, error) { return fake.state, fake.err }

type fakeCataloger struct {
	catalog zellijlive.Catalog
	err     error
}

func (fake *fakeCataloger) Observe(context.Context) (zellijlive.Catalog, error) {
	return fake.catalog, fake.err
}

func completeNiriState() niriipc.State {
	output := "winit"
	workspace := uint64(1)
	name := "Dev"
	return niriipc.State{
		Outputs:    map[string]niriipc.Output{"winit": {Name: "winit", Logical: niriipc.Logical{Width: 1920, Height: 1080, Scale: 1, Transform: "Normal"}}},
		Workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Name: &name, Output: &output, IsActive: true}},
		Windows:    []niriipc.Window{{ID: 42, AppID: "kitty", PID: 100, WorkspaceID: &workspace, Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{900, 700}, WindowSize: []int{900, 700}}}},
	}
}

func activeCatalog() zellijlive.Catalog {
	session := zellijlive.Session{Name: "project", ID: "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Status: zellijlive.StatusActive}
	return zellijlive.Catalog{Sessions: map[string]zellijlive.Session{"project": session, "headless": {Name: "headless", ID: "ses_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Status: zellijlive.StatusActive}}, Names: []string{"headless", "project"}}
}

func TestInventoryCardinalityMatrix(t *testing.T) {
	epoch := "11111111-1111-4111-8111-111111111111"
	base := completeNiriState()

	tests := []struct {
		name         string
		windows      []niriipc.Window
		processes    map[int]zellijlive.ProcessEvidence
		catalog      zellijlive.Catalog
		processErr   error
		want         int
		wantConflict sliceprotocol.ConflictCode
		wantErr      sliceprotocol.ReasonCode
	}{
		{name: "zero", windows: nil, processes: map[int]zellijlive.ProcessEvidence{}, catalog: zellijlive.Catalog{Sessions: map[string]zellijlive.Session{}}, want: 0},
		{name: "one", windows: base.Windows, processes: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}, catalog: activeCatalog(), want: 1},
		{name: "dead session", windows: base.Windows, processes: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}, catalog: zellijlive.Catalog{Sessions: map[string]zellijlive.Session{"project": {Name: "project", ID: "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Status: zellijlive.StatusDeadResurrectable}}}, wantConflict: sliceprotocol.ConflictSessionDeadResurrectable},
		{name: "prefix-only session", windows: base.Windows, processes: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}, catalog: zellijlive.Catalog{Sessions: map[string]zellijlive.Session{"project": {Name: "project", ID: "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Status: zellijlive.StatusPrefixOnly}}}, wantConflict: sliceprotocol.ConflictSessionPrefixOnly},
		{name: "process evidence loss", windows: base.Windows, processes: map[int]zellijlive.ProcessEvidence{}, catalog: activeCatalog(), processErr: errors.New("unavailable"), wantErr: sliceprotocol.ReasonProcessObservationIncomplete},
		{name: "many", windows: []niriipc.Window{
			base.Windows[0],
			{ID: 43, AppID: "kitty", PID: 101, WorkspaceID: base.Windows[0].WorkspaceID, Layout: niriipc.Layout{Position: []int{2, 1}, TileSize: []float64{800, 600}, WindowSize: []int{800, 600}}},
			{ID: 44, AppID: "kitty", PID: 102, WorkspaceID: base.Windows[0].WorkspaceID, Layout: niriipc.Layout{Position: []int{3, 1}, TileSize: []float64{700, 500}, WindowSize: []int{700, 500}}},
		}, processes: map[int]zellijlive.ProcessEvidence{
			100: {KittyVerified: true, Candidates: []string{"project"}},
			101: {KittyVerified: true, Candidates: []string{"second"}},
			102: {KittyVerified: true, Candidates: []string{"third"}},
		}, catalog: zellijlive.Catalog{Sessions: map[string]zellijlive.Session{
			"project": {Name: "project", ID: "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Status: zellijlive.StatusActive},
			"second":  {Name: "second", ID: "ses_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Status: zellijlive.StatusActive},
			"third":   {Name: "third", ID: "ses_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", Status: zellijlive.StatusActive},
		}, Names: []string{"project", "second", "third"}}, want: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := base
			state.Windows = tc.windows
			builder := Builder{Processes: fakeProcesses{values: tc.processes, err: tc.processErr}}
			sources, conflicts, err := builder.Build(context.Background(), epoch, state, tc.catalog)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), string(tc.wantErr)) {
					t.Fatalf("error=%v, want reason %s", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(sources) != tc.want {
				t.Fatalf("sources=%d conflicts=%+v err=%v", len(sources), conflicts, err)
			}
			if tc.wantConflict == "" && len(conflicts) != 0 {
				t.Fatalf("unexpected conflicts=%+v", conflicts)
			}
			if tc.wantConflict != "" && (len(conflicts) != 1 || conflicts[0].Code != tc.wantConflict) {
				t.Fatalf("conflicts=%+v, want one %s", conflicts, tc.wantConflict)
			}
			seen := map[string]bool{}
			for _, source := range sources {
				if seen[source.SourceID] {
					t.Fatalf("duplicate source identity %q", source.SourceID)
				}
				seen[source.SourceID] = true
			}
		})
	}
}

func TestBuilderOneWindowTwoSessionCandidatesIsAmbiguousConflict(t *testing.T) {
	epoch := "11111111-1111-4111-8111-111111111111"
	state := completeNiriState()
	catalog := zellijlive.Catalog{Sessions: map[string]zellijlive.Session{
		"project": {Name: "project", ID: "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Status: zellijlive.StatusActive},
		"other":   {Name: "other", ID: "ses_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Status: zellijlive.StatusActive},
	}, Names: []string{"other", "project"}}
	builder := Builder{Processes: fakeProcesses{values: map[int]zellijlive.ProcessEvidence{
		100: {KittyVerified: true, Candidates: []string{"project", "other"}},
	}}}

	sources, conflicts, err := builder.Build(context.Background(), epoch, state, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 || len(conflicts) != 1 {
		t.Fatalf("ambiguous one-window binding published: sources=%+v conflicts=%+v", sources, conflicts)
	}
	wantID, err := SourceID(epoch, state.Windows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if conflicts[0].Code != sliceprotocol.ConflictSessionCandidateAmbiguous || conflicts[0].SourceID != wantID {
		t.Fatalf("wrong ambiguity taxonomy: %+v", conflicts[0])
	}
}

func TestBuilderEligibleConflictDuplicateAndHeadlessScope(t *testing.T) {
	epoch := "11111111-1111-4111-8111-111111111111"
	state := completeNiriState()
	builder := Builder{Processes: fakeProcesses{values: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}}}
	sources, conflicts, err := builder.Build(context.Background(), epoch, state, activeCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || len(conflicts) != 0 || sources[0].Workspace.Key != "dev" || sources[0].Session.Name != "project" {
		t.Fatalf("unexpected inventory: %+v %+v", sources, conflicts)
	}
	if sources[0].Session.Name == "headless" {
		t.Fatal("headless session became a source")
	}

	workspace := uint64(1)
	state.Windows = append(state.Windows, niriipc.Window{ID: 43, AppID: "custom-kitty", PID: 101, WorkspaceID: &workspace, Layout: niriipc.Layout{Position: []int{2, 1}, TileSize: []float64{900, 700}, WindowSize: []int{900, 700}}})
	builder.Processes = fakeProcesses{values: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}, 101: {KittyVerified: true, Candidates: []string{"project"}}}}
	sources, conflicts, err = builder.Build(context.Background(), epoch, state, activeCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 || len(conflicts) != 2 {
		t.Fatalf("duplicate binding not rejected: %+v %+v", sources, conflicts)
	}
	for _, conflict := range conflicts {
		if conflict.Code != sliceprotocol.ConflictSessionDuplicateBinding {
			t.Fatalf("wrong conflict: %+v", conflict)
		}
	}
}

func TestPublisherRevisionDegradedRetentionEpochRotationAndRuntimeReuse(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uuids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	index := 0
	uuid := func() (string, error) { value := uuids[index]; index++; return value, nil }
	publisher := Publisher{Store: store, UUID: uuid}
	if _, err := publisher.Initialize(); err != nil {
		t.Fatal(err)
	}
	niri := &fakeNiri{state: completeNiriState()}
	catalog := &fakeCataloger{catalog: activeCatalog()}
	fingerprint := "1111111111111111111111111111111111111111111111111111111111111111"
	now := time.Unix(100, 0).UTC()
	publisher.Niri = niri
	publisher.Catalog = catalog
	publisher.Builder = Builder{Processes: fakeProcesses{values: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}}}
	publisher.Fingerprint = func() (string, error) { return fingerprint, nil }
	publisher.Now = func() time.Time { return now }
	first, err := publisher.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Observation.Quality != sliceprotocol.QualityComplete || first.Authoritative.Revision != 1 || len(first.Authoritative.LiveSessionIDs) != 2 || len(first.Authoritative.Sources) != 1 {
		t.Fatalf("complete authority must include verified headless live sessions without making them sources: %+v", first)
	}
	firstID := first.Authoritative.Sources[0].SourceID
	now = now.Add(time.Second)
	second, _ := publisher.Snapshot(context.Background())
	firstHash, _ := sliceprotocol.SemanticHash(*first.Authoritative)
	secondHash, _ := sliceprotocol.SemanticHash(*second.Authoritative)
	if second.Authoritative.Revision != 2 || secondHash != firstHash || !second.Authoritative.ObservedAt.After(first.Authoritative.ObservedAt) {
		t.Fatalf("unchanged completed poll did not advance revision with stable semantics: %+v", second.Authoritative)
	}
	niri.state.Windows[0].Layout.WindowSize[0]++
	now = now.Add(time.Second)
	changed, _ := publisher.Snapshot(context.Background())
	if changed.Authoritative.Revision != 3 {
		t.Fatalf("revision=%d", changed.Authoritative.Revision)
	}
	niri.err = &niriipc.ObservationError{Code: sliceprotocol.ReasonNiriReplayTimeout, Err: errors.New("private detail")}
	now = now.Add(time.Second)
	degraded, _ := publisher.Snapshot(context.Background())
	if degraded.Observation.Quality != sliceprotocol.QualityDegraded || degraded.Authoritative.Revision != 3 {
		t.Fatalf("degraded lost authority: %+v", degraded)
	}
	niri.err = nil
	catalog.err = errors.New("list command failed with empty output")
	now = now.Add(time.Second)
	catalogDegraded, _ := publisher.Snapshot(context.Background())
	if catalogDegraded.Observation.Quality != sliceprotocol.QualityDegraded || catalogDegraded.Observation.DegradedReasons[0].Code != sliceprotocol.ReasonZellijCatalogUnavailable || catalogDegraded.Authoritative.Revision != 3 || len(catalogDegraded.Authoritative.LiveSessionIDs) != 2 {
		t.Fatalf("catalog failure did not retain live-session authority: %+v", catalogDegraded)
	}
	catalog.err = nil
	identityCalls := 0
	publisher.Fingerprint = func() (string, error) {
		identityCalls++
		if identityCalls == 1 {
			return "1111111111111111111111111111111111111111111111111111111111111111", nil
		}
		return "3333333333333333333333333333333333333333333333333333333333333333", nil
	}
	now = now.Add(time.Second)
	identityChanged, _ := publisher.Snapshot(context.Background())
	if identityChanged.Observation.Quality != sliceprotocol.QualityDegraded || identityChanged.Observation.DegradedReasons[0].Code != sliceprotocol.ReasonSourceIdentityChanged || identityChanged.Authoritative.Revision != 3 {
		t.Fatalf("mid-poll identity change was published: %+v", identityChanged)
	}
	fingerprint = "2222222222222222222222222222222222222222222222222222222222222222"
	publisher.Fingerprint = func() (string, error) { return fingerprint, nil }
	now = now.Add(time.Second)
	rotated, _ := publisher.Snapshot(context.Background())
	if rotated.Authoritative.Revision != 1 || rotated.Authoritative.SourceEpoch == first.Authoritative.SourceEpoch || rotated.Authoritative.Sources[0].SourceID == firstID {
		t.Fatalf("epoch/runtime reuse did not rotate: %+v", rotated.Authoritative)
	}
	delete(catalog.catalog.Sessions, "headless")
	catalog.catalog.Names = []string{"project"}
	now = now.Add(time.Second)
	headlessRemoved, _ := publisher.Snapshot(context.Background())
	if headlessRemoved.Authoritative.Revision != 2 || len(headlessRemoved.Authoritative.Sources) != 1 || len(headlessRemoved.Authoritative.LiveSessionIDs) != 1 {
		t.Fatalf("headless-only catalog change did not publish complete authority: %+v", headlessRemoved.Authoritative)
	}
}

func TestPublisherHeadlessRevisionPreservesSourcesAndRestoresOutput(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uuids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "11111111-1111-4111-8111-111111111111"}
	uuidIndex := 0
	publisher := Publisher{Store: store, UUID: func() (string, error) {
		value := uuids[uuidIndex]
		uuidIndex++
		return value, nil
	}}
	if _, err := publisher.Initialize(); err != nil {
		t.Fatal(err)
	}
	niri := &fakeNiri{state: completeNiriState()}
	niri.state.Outputs = map[string]niriipc.Output{}
	niri.state.Workspaces[0].Output = nil
	catalog := &fakeCataloger{catalog: activeCatalog()}
	now := time.Unix(200, 0).UTC()
	publisher.Niri = niri
	publisher.Catalog = catalog
	publisher.Builder = Builder{Processes: fakeProcesses{values: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}}}
	publisher.Fingerprint = func() (string, error) { return strings.Repeat("1", 64), nil }
	publisher.Now = func() time.Time { return now }

	headless, err := publisher.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if headless.SchemaVersion != sliceprotocol.SchemaVersion || headless.Observation.Quality != sliceprotocol.QualityComplete || headless.Authoritative.Revision != 1 || len(headless.Authoritative.Sources) != 1 || headless.Authoritative.Sources[0].Output != nil || len(headless.Authoritative.LiveSessionIDs) != 2 {
		t.Fatalf("incomplete headless authority: %+v", headless)
	}
	if headless.Authoritative.Sources[0].Session.Name != "project" {
		t.Fatalf("windowless session became a source: %+v", headless.Authoritative.Sources)
	}
	sourceID := headless.Authoritative.Sources[0].SourceID

	niri.state = completeNiriState()
	now = now.Add(time.Second)
	restored, err := publisher.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Authoritative.Revision != 2 || len(restored.Authoritative.Sources) != 1 || restored.Authoritative.Sources[0].SourceID != sourceID || restored.Authoritative.Sources[0].Output == nil || restored.Authoritative.Sources[0].Output.LogicalWidth != 1920 {
		t.Fatalf("output restoration changed identity or omitted geometry: %+v", restored.Authoritative)
	}
}

func TestStoreInitializationCorruptionAndFingerprintPrivacy(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Store: store, UUID: func() (string, error) { return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil }}
	if _, err := publisher.Initialize(); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Initialize(); err == nil {
		t.Fatal("initializer overwrote authority")
	}
	if err := os.WriteFile(store.Path(), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("corruption accepted: %v", err)
	}

	socketPath := filepath.Join(t.TempDir(), "niri.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	fingerprint, err := NiriFingerprint("secret-boot", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	alternatePath := filepath.Join(filepath.Dir(socketPath), "alternate.sock")
	if err := os.Link(socketPath, alternatePath); err != nil {
		t.Fatal(err)
	}
	alternateFingerprint, err := NiriFingerprint("secret-boot", alternatePath)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != alternateFingerprint {
		t.Fatalf("path spelling changed socket identity: %q %q", fingerprint, alternateFingerprint)
	}
	if fingerprint == "" || fingerprint == socketPath || fingerprint == alternatePath || fingerprint == "secret-boot" {
		t.Fatalf("fingerprint leaked private input: %q", fingerprint)
	}
	serialized, err := json.Marshal(State{StorageVersion: StorageVersion, Initialized: true, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", PrivateFingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{socketPath, alternatePath, "secret-boot"} {
		if strings.Contains(string(serialized), privateValue) {
			t.Fatalf("serialized state leaked %q: %s", privateValue, serialized)
		}
	}
}

func TestEnrollmentMarkerPreventsMissingAfterUseAndCrashReinitialization(t *testing.T) {
	t.Run("missing current after use", func(t *testing.T) {
		store, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		publisher := Publisher{Store: store, UUID: func() (string, error) { return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil }}
		if _, err := publisher.Initialize(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(store.Path()); err != nil {
			t.Fatal(err)
		}
		if _, err := publisher.Initialize(); !errors.Is(err, ErrNamespaceUsed) {
			t.Fatalf("missing-after-use reinitialized: %v", err)
		}
	})
	t.Run("marker survives authority commit failure", func(t *testing.T) {
		store, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store.rename = func(string, string) error { return errors.New("injected current rename failure") }
		publisher := Publisher{Store: store, UUID: func() (string, error) { return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil }}
		if _, err := publisher.Initialize(); err == nil {
			t.Fatal("expected authority commit failure")
		}
		present, err := store.EnrollmentMarkerPresent()
		if err != nil || !present {
			t.Fatalf("enrollment marker missing after crash boundary: %v", err)
		}
		store.rename = os.Rename
		if _, err := publisher.Initialize(); !errors.Is(err, ErrNamespaceUsed) {
			t.Fatalf("crash boundary allowed new identity: %v", err)
		}
	})
}

func TestStoreWriteFailureDoesNotReplaceCurrentGeneration(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := State{StorageVersion: StorageVersion, Initialized: true, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	if err := store.Write(initial); err != nil {
		t.Fatal(err)
	}
	store.syncFile = func(*os.File) error { return errors.New("injected fsync failure") }
	if err := store.Write(State{StorageVersion: StorageVersion, Initialized: true, SourceHostID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}); err == nil {
		t.Fatal("expected failure")
	}
	store.syncFile = func(file *os.File) error { return file.Sync() }
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.SourceHostID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("failed write replaced authority: %+v", state)
	}
}

func TestSourceIDIsEpochScopedAndOpaque(t *testing.T) {
	one, _ := SourceID("11111111-1111-4111-8111-111111111111", 42)
	two, _ := SourceID("22222222-2222-4222-8222-222222222222", 42)
	if one == two || one == "42" {
		t.Fatalf("invalid source IDs %q %q", one, two)
	}
}

func TestBuilderWorkspaceCollisionAndHostileMetadata(t *testing.T) {
	epoch := "11111111-1111-4111-8111-111111111111"
	state := completeNiriState()
	output := "winit"
	compat := "ＤＥＶ"
	workspace2 := uint64(2)
	state.Workspaces = append(state.Workspaces, niriipc.Workspace{ID: 2, Index: 2, Name: &compat, Output: &output})
	state.Windows = append(state.Windows, niriipc.Window{ID: 43, AppID: "kitty", PID: 101, WorkspaceID: &workspace2, Layout: niriipc.Layout{Position: []int{2, 1}, TileSize: []float64{900, 700}, WindowSize: []int{900, 700}}})
	catalog := activeCatalog()
	other := zellijlive.Session{Name: "other", ID: "ses_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", Status: zellijlive.StatusActive}
	catalog.Sessions["other"] = other
	catalog.Names = append(catalog.Names, "other")
	builder := Builder{Processes: fakeProcesses{values: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}, 101: {KittyVerified: true, Candidates: []string{"other"}}}}}
	sources, conflicts, err := builder.Build(context.Background(), epoch, state, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 || len(conflicts) != 2 {
		t.Fatalf("collision not reported: %+v %+v", sources, conflicts)
	}
	for _, item := range conflicts {
		if item.Code != sliceprotocol.ConflictWorkspaceNameCollision {
			t.Fatalf("wrong conflict: %+v", item)
		}
	}
	hostile := "bad\x00name"
	state.Workspaces[0].Name = &hostile
	if _, _, err := builder.Build(context.Background(), epoch, state, catalog); err == nil {
		t.Fatal("hostile workspace metadata accepted")
	}
}

func TestStoreRenameAndDirectorySyncFailuresAreVisible(t *testing.T) {
	newState := func(id string) State {
		return State{StorageVersion: StorageVersion, Initialized: true, SourceHostID: id}
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := newState("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	second := newState("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err := store.Write(first); err != nil {
		t.Fatal(err)
	}
	store.rename = func(string, string) error { return errors.New("injected rename failure") }
	if err := store.Write(second); err == nil {
		t.Fatal("expected rename failure")
	}
	store.rename = os.Rename
	got, err := store.Read()
	if err != nil || got.SourceHostID != first.SourceHostID {
		t.Fatalf("rename failure changed authority: %+v %v", got, err)
	}
	store.syncDir = func(string) error { return errors.New("injected directory fsync failure") }
	if err := store.Write(second); err == nil {
		t.Fatal("expected directory sync failure")
	}
	store.syncDir = syncDirectory
	got, err = store.Read()
	if err != nil || got.SourceHostID != second.SourceHostID {
		t.Fatalf("post-rename generation is invalid: %+v %v", got, err)
	}
}

func TestPublisherRevisionOverflowDegradesAndRetainsAuthority(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnrollmentMarker(); err != nil {
		t.Fatal(err)
	}
	host := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	epoch := "11111111-1111-4111-8111-111111111111"
	fingerprint := "1111111111111111111111111111111111111111111111111111111111111111"
	now := time.Unix(100, 0).UTC()
	authority := sliceprotocol.Authoritative{SourceEpoch: epoch, Revision: ^uint64(0), ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []sliceprotocol.Source{}, Conflicts: []sliceprotocol.Conflict{}}
	hash, _ := sliceprotocol.SemanticHash(authority)
	if err := store.Write(State{StorageVersion: StorageVersion, Initialized: true, SourceHostID: host, PrivateFingerprint: fingerprint, SemanticHash: hash, Authority: &authority}); err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Store: store, Niri: &fakeNiri{state: completeNiriState()}, Catalog: &fakeCataloger{catalog: activeCatalog()}, Builder: Builder{Processes: fakeProcesses{values: map[int]zellijlive.ProcessEvidence{100: {KittyVerified: true, Candidates: []string{"project"}}}}}, Fingerprint: func() (string, error) { return fingerprint, nil }, Now: func() time.Time { return now.Add(time.Second) }}
	envelope, err := publisher.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Observation.Quality != sliceprotocol.QualityDegraded || envelope.Observation.DegradedReasons[0].Code != sliceprotocol.ReasonRevisionOverflow || envelope.Authoritative.Revision != ^uint64(0) {
		t.Fatalf("overflow not retained: %+v", envelope)
	}
}

func TestBuilderShuffledSourcesHaveCanonicalOrderAndExactSpatialMetadata(t *testing.T) {
	epoch := "11111111-1111-4111-8111-111111111111"
	outputName := "DP-1"
	alpha := "Alpha"
	beta := "Beta"
	alphaID := uint64(1)
	betaID := uint64(2)
	state := niriipc.State{
		Outputs:    map[string]niriipc.Output{"DP-1": {Name: "DP-1", Logical: niriipc.Logical{X: 10, Y: 20, Width: 2560, Height: 1440, Scale: 1.5, Transform: "Flipped180"}}},
		Workspaces: []niriipc.Workspace{{ID: 2, Index: 2, Name: &beta, Output: &outputName}, {ID: 1, Index: 1, Name: &alpha, Output: &outputName, IsActive: true}},
		Windows: []niriipc.Window{
			{ID: 50, AppID: "kitty", PID: 150, WorkspaceID: &betaID, IsFloating: true, Layout: niriipc.Layout{TileSize: []float64{700, 500}, WindowSize: []int{680, 480}}},
			{ID: 40, AppID: "kitty", PID: 140, WorkspaceID: &alphaID, Layout: niriipc.Layout{Position: []int{2, 1}, TileSize: []float64{800, 600}, WindowSize: []int{780, 580}}},
			{ID: 60, AppID: "kitty", PID: 160, WorkspaceID: &alphaID, Layout: niriipc.Layout{Position: []int{1, 2}, TileSize: []float64{900, 650}, WindowSize: []int{880, 630}}},
			{ID: 70, AppID: "kitty", PID: 170, WorkspaceID: &alphaID, Layout: niriipc.Layout{Position: []int{3, 1}, TileSize: []float64{600, 400}, WindowSize: []int{580, 380}}},
			{ID: 80, AppID: "kitty", PID: 180, WorkspaceID: &alphaID, Layout: niriipc.Layout{Position: []int{4, 1}, TileSize: []float64{600, 400}, WindowSize: []int{580, 380}}},
		},
	}
	catalog := zellijlive.Catalog{Sessions: map[string]zellijlive.Session{}, Names: []string{"alpha-one", "alpha-two", "beta"}}
	for i, name := range []string{"alpha-one", "alpha-two", "beta"} {
		catalog.Sessions[name] = zellijlive.Session{Name: name, ID: "ses_" + strings.Repeat(string(rune('A'+i)), 43), Status: zellijlive.StatusActive}
	}
	processes := fakeProcesses{values: map[int]zellijlive.ProcessEvidence{
		150: {KittyVerified: true, Candidates: []string{"beta"}}, 140: {KittyVerified: true, Candidates: []string{"alpha-two"}}, 160: {KittyVerified: true, Candidates: []string{"alpha-one"}},
		170: {KittyVerified: false}, 180: {KittyVerified: true, Candidates: []string{"ghost"}},
	}}
	sources, conflicts, err := (Builder{Processes: processes}).Build(context.Background(), epoch, state, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 || len(conflicts) != 2 {
		t.Fatalf("unexpected inventory sizes: sources=%+v conflicts=%+v", sources, conflicts)
	}
	if got := []string{sources[0].Session.Name, sources[1].Session.Name, sources[2].Session.Name}; !reflect.DeepEqual(got, []string{"alpha-one", "alpha-two", "beta"}) {
		t.Fatalf("source order=%v", got)
	}
	out := sources[0].Output
	if out.Name != "DP-1" || out.LogicalX != 10 || out.LogicalY != 20 || out.LogicalWidth != 2560 || out.LogicalHeight != 1440 || out.Scale != 1.5 || out.Transform != "Flipped180" {
		t.Fatalf("output metadata changed: %+v", out)
	}
	if sources[0].Layout.Mode != "tiled" || sources[0].Layout.Position == nil || *sources[0].Layout.Position != (sliceprotocol.Position{Column: 1, Tile: 2}) || sources[0].Layout.TileWidth != 900 || sources[0].Layout.TileHeight != 650 || sources[0].Layout.WindowWidth != 880 || sources[0].Layout.WindowHeight != 630 {
		t.Fatalf("tiled metadata changed: %+v", sources[0].Layout)
	}
	floating := sources[2].Layout
	if floating.Mode != "floating" || floating.Position != nil || floating.TileWidth != 700 || floating.TileHeight != 500 || floating.WindowWidth != 680 || floating.WindowHeight != 480 {
		t.Fatalf("floating metadata changed: %+v", floating)
	}
	if conflicts[0].Code != sliceprotocol.ConflictKittyProcessUnverified || conflicts[1].Code != sliceprotocol.ConflictSessionMissing {
		t.Fatalf("conflict order=%+v", conflicts)
	}
}
