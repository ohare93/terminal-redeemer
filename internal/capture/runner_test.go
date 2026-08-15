package capture

import (
	"context"
	"errors"
	"strings"
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

func TestCaptureRunContinuesAfterRecoverableError(t *testing.T) {
	root := t.TempDir()
	collector := &sequenceCollector{states: []model.State{capturedState("unused"), capturedState("success")}, errs: []error{errors.New("temporary"), nil}}
	var log strings.Builder
	runner, store := newTestRunner(t, root, "boot-a", collector, time.Now)
	runner.logger = &log
	ticks := make(chan time.Time, 2)
	ticks <- time.Now()
	ticks <- time.Now()
	close(ticks)
	if err := runner.CaptureRun(context.Background(), ticks); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "capture_once_error") {
		t.Fatalf("missing recoverable error log: %q", log.String())
	}
	if _, err := store.Read("boot-a", "host", "default"); err != nil {
		t.Fatalf("later capture did not succeed: %v", err)
	}
}
