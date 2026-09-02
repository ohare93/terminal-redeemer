package capture

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/storelock"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type staticCataloger struct {
	catalog zellijlive.Catalog
	err     error
}

func (c staticCataloger) Observe(context.Context) (zellijlive.Catalog, error) {
	return c.catalog, c.err
}

func activeCatalog(names ...string) zellijlive.Catalog {
	catalog := zellijlive.Catalog{Sessions: make(map[string]zellijlive.Session), Names: append([]string(nil), names...)}
	for _, name := range names {
		catalog.Sessions[name] = zellijlive.Session{Name: name, Status: zellijlive.StatusActive}
	}
	return catalog
}

type sequenceCataloger struct {
	catalogs []zellijlive.Catalog
	calls    int
}

func (c *sequenceCataloger) Observe(context.Context) (zellijlive.Catalog, error) {
	i := c.calls
	c.calls++
	if i >= len(c.catalogs) {
		i = len(c.catalogs) - 1
	}
	return c.catalogs[i], nil
}

type sequenceCollector struct {
	mu     sync.Mutex
	states []model.State
	errs   []error
	calls  int
}

type coordinatedCollector struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (c *coordinatedCollector) Collect(context.Context) (model.State, error) {
	c.mu.Lock()
	call := c.calls
	c.calls++
	c.mu.Unlock()
	if call == 0 {
		close(c.firstStarted)
		<-c.releaseFirst
		return capturedState("older-observation"), nil
	}
	return capturedState("newer-observation"), nil
}

func (c *sequenceCollector) Collect(context.Context) (model.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	c.calls++
	if i < len(c.errs) && c.errs[i] != nil {
		return model.State{}, c.errs[i]
	}
	if len(c.states) == 0 {
		return model.State{}, nil
	}
	if i >= len(c.states) {
		i = len(c.states) - 1
	}
	return c.states[i], nil
}

func capturedState(title string) model.State {
	return model.State{Windows: []model.Window{{Key: "w:kitty:1", AppID: "kitty", Title: title}}}
}

func newTestRunner(t *testing.T, root, boot string, collector Collector, now func() time.Time) (*Runner, *checkpoints.Store) {
	t.Helper()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return NewRunner(Config{
		Collector: collector, CheckpointStore: store, Cataloger: staticCataloger{catalog: activeCatalog()}, StateDir: root,
		BootIDSource: func() (string, error) { return boot, nil },
		Host:         "host", Profile: "default", Now: now,
	}), store
}

func TestCaptureOnceMaintainsOneLatestCheckpointPerBoot(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	times := []time.Time{t0, t0.Add(time.Minute)}
	runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{capturedState("first"), capturedState("latest")}}, func() time.Time {
		n := times[0]
		times = times[1:]
		return n
	})

	first, err := runner.CaptureOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.CaptureOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.CheckpointPath != second.CheckpointPath {
		t.Fatalf("rolling path changed: %q != %q", first.CheckpointPath, second.CheckpointPath)
	}
	got, err := store.Read("boot-a", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(t0.Add(time.Minute)) || got.State.Windows[0].Title != "latest" || got.V != checkpoints.SchemaVersion {
		t.Fatalf("checkpoint was not refreshed: %#v", got)
	}
	all, issues, err := checkpoints.List(root)
	if err != nil || len(issues) != 0 || len(all) != 1 {
		t.Fatalf("checkpoints=%#v issues=%#v err=%v", all, issues, err)
	}
}

func TestCaptureFailureLeavesPublishedCheckpointUsable(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().UTC()
	collector := &sequenceCollector{states: []model.State{capturedState("valid")}, errs: []error{nil, errors.New("query failed")}}
	runner, store := newTestRunner(t, root, "boot-a", collector, func() time.Time { return t0 })
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err == nil {
		t.Fatal("expected collection failure")
	}
	got, err := store.Read("boot-a", "host", "default")
	if err != nil || got.State.Windows[0].Title != "valid" {
		t.Fatalf("prior checkpoint unusable after interrupted capture: %#v err=%v", got, err)
	}
}

func TestCaptureEmptyPostRestartObservationRetainsStickyPlacementSameAndNewBoot(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	column, row, floating := 3, 2, false
	visible := model.State{Windows: []model.Window{{
		Key: "w:kitty:1", AppID: "kitty", WorkspaceID: "runtime-1",
		WorkspaceRef: &model.WorkspaceRef{Name: "dev", Output: "DP-1", Index: 1},
		Placement:    &model.Placement{Column: &column, Row: &row, IsFloating: &floating, TileSize: []float64{800, 600}, WindowSize: []int{802, 602}},
		Terminal:     &model.Terminal{CWD: "/work/project", SessionTag: "alpha", SessionTagExact: true},
	}}}
	times := []time.Time{t0, t0.Add(time.Minute)}
	runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{visible, {}}}, func() time.Time {
		now := times[0]
		times = times[1:]
		return now
	})
	runner.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("boot-a", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	assertStickySession(t, got, t0)
	if len(got.State.Windows) != 0 {
		t.Fatalf("empty Niri observation not retained: %#v", got.State)
	}

	newBoot, _ := newTestRunner(t, root, "boot-b", &sequenceCollector{states: []model.State{{}}}, func() time.Time { return t0.Add(2 * time.Minute) })
	newBoot.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
	if _, err := newBoot.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	carried, err := store.Read("boot-b", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	assertStickySession(t, carried, t0)
}

func TestCaptureCarriesObservedColumnOccupancySameAndNewBoot(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	column, targetRow, otherRow := 3, 0, 1
	workspace := model.WorkspaceRef{Name: "dev", Index: 1}
	visible := model.State{Windows: []model.Window{
		{
			Key: "w:kitty:1", AppID: "kitty", WorkspaceID: "runtime-1", WorkspaceRef: &workspace,
			Placement: &model.Placement{Column: &column, Row: &targetRow},
			Terminal:  &model.Terminal{SessionTag: "alpha", SessionTagExact: true},
		},
		{
			Key: "w:browser:2", AppID: "firefox", WorkspaceID: "runtime-1", WorkspaceRef: &workspace,
			Placement: &model.Placement{Column: &column, Row: &otherRow},
		},
	}}
	times := []time.Time{t0, t0.Add(time.Minute)}
	runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{visible, {}}}, func() time.Time {
		now := times[0]
		times = times[1:]
		return now
	})
	runner.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	sameBoot, err := store.Read("boot-a", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	if session := sameBoot.Recovery.Sessions[0]; !session.CapturedColumnOccupied || session.Visible || session.PlacementObservedAt == nil || !session.PlacementObservedAt.Equal(t0) {
		t.Fatalf("same-boot occupancy was not retained atomically: %#v", session)
	}

	newBoot, _ := newTestRunner(t, root, "boot-b", &sequenceCollector{states: []model.State{{}}}, func() time.Time { return t0.Add(2 * time.Minute) })
	newBoot.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
	if _, err := newBoot.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	carried, err := store.Read("boot-b", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	if session := carried.Recovery.Sessions[0]; !session.CapturedColumnOccupied || session.Visible || session.PlacementObservedAt == nil || !session.PlacementObservedAt.Equal(t0) {
		t.Fatalf("new-boot occupancy was not retained atomically: %#v", session)
	}
}

func TestCaptureTitleFallbackCannotOverwriteStickyPlacement(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	column, row, replacementColumn := 3, 0, 9
	initial := recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "dev", Index: 1}, &model.Placement{Column: &column, Row: &row})
	titleFallback := recoveryWindow("alpha", false, &model.WorkspaceRef{Name: "unrelated", Index: 8}, &model.Placement{Column: &replacementColumn})
	times := []time.Time{t0, t0.Add(time.Minute)}
	runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{{Windows: []model.Window{initial}}, {Windows: []model.Window{titleFallback}}}}, func() time.Time {
		now := times[0]
		times = times[1:]
		return now
	})
	runner.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
	runner.cwdResolver = nil
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("boot-a", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	session := got.Recovery.Sessions[0]
	if session.Visible || session.WorkspaceRef == nil || session.WorkspaceRef.Name != "dev" || session.Placement == nil || session.Placement.Column == nil || *session.Placement.Column != column {
		t.Fatalf("title-derived association changed recovery placement: %#v", session)
	}
	if session.PlacementObservedAt == nil || !session.PlacementObservedAt.Equal(t0) {
		t.Fatalf("title-derived association refreshed observation time: %v", session.PlacementObservedAt)
	}
}

func TestCapturePartialPlacementRetainsPriorAtomicObservation(t *testing.T) {
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		second model.Window
	}{
		{
			name:   "workspace only",
			second: recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "review", Index: 2}, nil),
		},
		{
			name: "workspace and column only",
			second: func() model.Window {
				column := 7
				return recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "review", Index: 2}, &model.Placement{Column: &column})
			}(),
		},
		{
			name: "workspace and row only",
			second: func() model.Window {
				row := 4
				return recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "review", Index: 2}, &model.Placement{Row: &row})
			}(),
		},
		{
			name: "workspace and explicitly non-floating without layout",
			second: func() model.Window {
				floating := false
				return recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "review", Index: 2}, &model.Placement{IsFloating: &floating})
			}(),
		},
		{
			name: "layout only",
			second: func() model.Window {
				column := 7
				return recoveryWindow("alpha", true, nil, &model.Placement{Column: &column})
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			column, targetRow, otherRow := 3, 0, 1
			initial := recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "dev", Index: 1}, &model.Placement{Column: &column, Row: &targetRow})
			other := model.Window{Key: "w:browser:2", AppID: "firefox", WorkspaceRef: &model.WorkspaceRef{Name: "dev", Index: 1}, Placement: &model.Placement{Column: &column, Row: &otherRow}}
			times := []time.Time{t0, t0.Add(time.Minute)}
			runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{{Windows: []model.Window{initial, other}}, {Windows: []model.Window{test.second}}}}, func() time.Time {
				now := times[0]
				times = times[1:]
				return now
			})
			runner.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
			if _, err := runner.CaptureOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.CaptureOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := store.Read("boot-a", "host", "default")
			if err != nil {
				t.Fatal(err)
			}
			session := got.Recovery.Sessions[0]
			if !session.Visible || session.WorkspaceRef == nil || session.WorkspaceRef.Name != "dev" || session.Placement == nil || session.Placement.Column == nil || *session.Placement.Column != column || !session.CapturedColumnOccupied {
				t.Fatalf("partial observation changed atomic placement evidence: %#v", session)
			}
			if session.PlacementObservedAt == nil || !session.PlacementObservedAt.Equal(t0) {
				t.Fatalf("partial observation refreshed aggregate timestamp: %v", session.PlacementObservedAt)
			}
		})
	}
}

func TestCapturePartialPlacementForNewSessionRemainsUntrusted(t *testing.T) {
	column, row, nonFloating := 7, 4, false
	workspace := &model.WorkspaceRef{Name: "dev", Index: 1}
	for _, test := range []struct {
		name      string
		placement *model.Placement
	}{
		{name: "column only", placement: &model.Placement{Column: &column}},
		{name: "row only", placement: &model.Placement{Row: &row}},
		{name: "non-floating without layout", placement: &model.Placement{IsFloating: &nonFloating}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, store := newTestRunner(t, t.TempDir(), "boot-a", &sequenceCollector{states: []model.State{{Windows: []model.Window{
				recoveryWindow("alpha", true, workspace, test.placement),
			}}}}, time.Now)
			runner.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
			if _, err := runner.CaptureOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := store.Read("boot-a", "host", "default")
			if err != nil {
				t.Fatal(err)
			}
			session := got.Recovery.Sessions[0]
			if !session.Visible || session.WorkspaceRef != nil || session.Placement != nil || session.PlacementObservedAt != nil || session.CapturedColumnOccupied {
				t.Fatalf("partial first observation persisted unsafe placement: %#v", session)
			}
		})
	}
}

func TestCaptureCompletePlacementRefreshesAtomicObservation(t *testing.T) {
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	floating, nonFloating := true, false
	column, row := 8, 2
	for _, test := range []struct {
		name      string
		placement *model.Placement
	}{
		{name: "tiled", placement: &model.Placement{Column: &column, Row: &row, IsFloating: &nonFloating}},
		{name: "floating", placement: &model.Placement{IsFloating: &floating, WindowSize: []int{900, 700}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			priorColumn, priorRow, otherRow := 3, 0, 1
			initial := recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "dev", Index: 1}, &model.Placement{Column: &priorColumn, Row: &priorRow})
			other := model.Window{Key: "w:browser:2", AppID: "firefox", WorkspaceRef: &model.WorkspaceRef{Name: "dev", Index: 1}, Placement: &model.Placement{Column: &priorColumn, Row: &otherRow}}
			replacement := recoveryWindow("alpha", true, &model.WorkspaceRef{Name: "review", Index: 2}, test.placement)
			times := []time.Time{t0, t0.Add(time.Minute)}
			runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{{Windows: []model.Window{initial, other}}, {Windows: []model.Window{replacement}}}}, func() time.Time {
				now := times[0]
				times = times[1:]
				return now
			})
			runner.cataloger = staticCataloger{catalog: activeCatalog("alpha")}
			if _, err := runner.CaptureOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.CaptureOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := store.Read("boot-a", "host", "default")
			if err != nil {
				t.Fatal(err)
			}
			session := got.Recovery.Sessions[0]
			if session.WorkspaceRef == nil || session.WorkspaceRef.Name != "review" || session.Placement == nil || session.PlacementObservedAt == nil || !session.PlacementObservedAt.Equal(t0.Add(time.Minute)) || session.CapturedColumnOccupied {
				t.Fatalf("complete placement did not atomically refresh: %#v", session)
			}
			if test.name == "tiled" && (session.Placement.Column == nil || *session.Placement.Column != column || session.Placement.Row == nil || *session.Placement.Row != row) {
				t.Fatalf("tiled placement not refreshed: %#v", session.Placement)
			}
			if test.name == "floating" && (session.Placement.IsFloating == nil || !*session.Placement.IsFloating || len(session.Placement.WindowSize) != 2) {
				t.Fatalf("floating placement not refreshed: %#v", session.Placement)
			}
		})
	}
}

func recoveryWindow(session string, exact bool, workspace *model.WorkspaceRef, placement *model.Placement) model.Window {
	return model.Window{
		Key: "w:kitty:1", AppID: "kitty", WorkspaceRef: workspace, Placement: placement,
		Terminal: &model.Terminal{SessionTag: session, SessionTagExact: exact},
	}
}

func assertStickySession(t *testing.T, checkpoint checkpoints.Checkpoint, placementTime time.Time) {
	t.Helper()
	if got := checkpoint.Recovery.ActiveSessions; len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("active allow-list=%v", got)
	}
	session := checkpoint.Recovery.Sessions[0]
	if session.Visible || session.CWD != "/work/project" || session.WorkspaceRef == nil || session.WorkspaceRef.Name != "dev" {
		t.Fatalf("sticky session metadata=%#v", session)
	}
	if session.Placement == nil || session.Placement.Column == nil || *session.Placement.Column != 3 || session.Placement.Row == nil || *session.Placement.Row != 2 || session.Placement.IsFloating == nil || *session.Placement.IsFloating {
		t.Fatalf("sticky placement=%#v", session.Placement)
	}
	if session.PlacementObservedAt == nil || !session.PlacementObservedAt.Equal(placementTime) {
		t.Fatalf("placement time=%v want %v", session.PlacementObservedAt, placementTime)
	}
}

func TestCaptureDropsSessionOnlyAfterAuthoritativeCatalogRemoval(t *testing.T) {
	root := t.TempDir()
	cataloger := &sequenceCataloger{catalogs: []zellijlive.Catalog{activeCatalog("alpha"), activeCatalog()}}
	runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{{}, {}}}, time.Now)
	runner.cataloger = cataloger
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("boot-a", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Recovery.ActiveSessions) != 0 || len(got.Recovery.Sessions) != 0 {
		t.Fatalf("removed active session persisted: %#v", got.Recovery)
	}
}

func TestCatalogFailureOrAmbiguityLeavesPriorCheckpointUntouched(t *testing.T) {
	for name, cataloger := range map[string]zellijlive.Cataloger{
		"failure":   staticCataloger{err: errors.New("catalog failed")},
		"duplicate": staticCataloger{catalog: zellijlive.Catalog{Names: []string{"alpha", "alpha"}, Sessions: activeCatalog("alpha").Sessions}},
		"invalid": staticCataloger{catalog: zellijlive.Catalog{
			Names: []string{"alpha"}, Sessions: map[string]zellijlive.Session{"alpha": {Name: "alpha", Status: zellijlive.StatusSocketInvalid}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
			runner, store := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{capturedState("prior"), capturedState("replacement")}}, func() time.Time { return t0 })
			if _, err := runner.CaptureOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			runner.cataloger = cataloger
			if _, err := runner.CaptureOnce(context.Background()); err == nil {
				t.Fatal("expected catalog rejection")
			}
			got, err := store.Read("boot-a", "host", "default")
			if err != nil || got.State.Windows[0].Title != "prior" {
				t.Fatalf("prior checkpoint changed: %#v err=%v", got, err)
			}
		})
	}
}

func TestCaptureOnceHonorsRepositoryWriterLock(t *testing.T) {
	root := t.TempDir()
	runner, _ := newTestRunner(t, root, "boot-a", &sequenceCollector{states: []model.State{capturedState("blocked")}}, time.Now)
	lock, err := storelock.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := runner.CaptureOnce(context.Background()); !errors.Is(err, storelock.ErrLocked) {
		t.Fatalf("capture did not honor writer lock: %v", err)
	}
}

func TestConcurrentCaptureCannotPublishAnOlderPendingObservationAfterANewerOne(t *testing.T) {
	root := t.TempDir()
	collector := &coordinatedCollector{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	var nowMu sync.Mutex
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	runner, store := newTestRunner(t, root, "boot-a", collector, func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		now = now.Add(time.Minute)
		return now
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := runner.CaptureOnce(context.Background())
		firstDone <- err
	}()
	<-collector.firstStarted

	// Before collection was covered by the writer lock, this capture observed
	// and published newer state while the first observation was blocked. The
	// first capture then overwrote it after being released.
	_, concurrentErr := runner.CaptureOnce(context.Background())
	close(collector.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if !errors.Is(concurrentErr, storelock.ErrLocked) {
		t.Fatalf("concurrent capture collected outside the writer lock: %v", concurrentErr)
	}

	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("newer capture after serialized older capture: %v", err)
	}
	got, err := store.Read("boot-a", "host", "default")
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Windows[0].Title != "newer-observation" {
		t.Fatalf("older observation replaced newer checkpoint: %#v", got)
	}
	collector.mu.Lock()
	calls := collector.calls
	collector.mu.Unlock()
	if calls != 2 {
		t.Fatalf("collector calls=%d, want 2 (blocked capture must not collect)", calls)
	}
}
