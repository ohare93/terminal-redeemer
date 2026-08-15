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
)

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
		Collector: collector, CheckpointStore: store, StateDir: root,
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
