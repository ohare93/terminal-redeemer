package capture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/diff"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

func TestCaptureOnceWritesStateFullEveryTime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	eventStore, err := events.NewStore(root)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	snapStore, err := snapshots.NewStore(root)
	if err != nil {
		t.Fatalf("new snapshot store: %v", err)
	}

	state := model.State{
		Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}},
		Windows:    []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "shell"}},
	}

	collector := &sequenceCollector{states: []model.State{state, state}}
	runner := NewRunner(Config{
		Collector:     collector,
		DiffEngine:    diff.NewEngine(),
		EventStore:    eventStore,
		SnapshotStore: snapStore,
		SnapshotEvery: 100,
		Host:          "host-a",
		Profile:       "default",
		Source:        "test",
		Now:           func() time.Time { return time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC) },
		Logger:        io.Discard,
	})

	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("capture once first: %v", err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("capture once second: %v", err)
	}

	got, _, err := eventStore.ReadSince(0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two full-state events, got %d", len(got))
	}
	for i, event := range got {
		if event.EventType != "state_full" {
			t.Fatalf("event[%d] type = %q, want state_full", i, event.EventType)
		}
	}
}

func TestCaptureRunLoopsAndContinuesOnRecoverableErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	eventStore, err := events.NewStore(root)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	snapStore, err := snapshots.NewStore(root)
	if err != nil {
		t.Fatalf("new snapshot store: %v", err)
	}

	stateA := model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "a"}}}
	stateB := model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "b"}}}

	var logs bytes.Buffer
	collector := &sequenceCollector{sequence: []collectResult{{state: stateA}, {err: errors.New("temporary niri error")}, {state: stateB}}}
	runner := NewRunner(Config{
		Collector:     collector,
		DiffEngine:    diff.NewEngine(),
		EventStore:    eventStore,
		SnapshotStore: snapStore,
		SnapshotEvery: 100,
		Host:          "host-a",
		Profile:       "default",
		Source:        "test",
		Now:           func() time.Time { return time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC) },
		Logger:        &logs,
	})

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- runner.CaptureRun(ctx, ticks)
	}()

	ticks <- time.Now()
	ticks <- time.Now()
	ticks <- time.Now()
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("capture run: %v", err)
	}

	got, _, err := eventStore.ReadSince(0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events from successful ticks, got %d", len(got))
	}
	if !bytes.Contains(logs.Bytes(), []byte("capture_once_error")) {
		t.Fatalf("expected recoverable error log, got %q", logs.String())
	}
}

func TestSnapshotCadenceHonored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	eventStore, err := events.NewStore(root)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	snapStore, err := snapshots.NewStore(root)
	if err != nil {
		t.Fatalf("new snapshot store: %v", err)
	}

	stateA := model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "a"}}}
	stateB := model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "b"}}}
	stateC := model.State{Workspaces: []model.Workspace{{ID: "ws-1", Index: 1}}, Windows: []model.Window{{Key: "w-1", AppID: "kitty", WorkspaceID: "ws-1", Title: "c"}}}

	collector := &sequenceCollector{states: []model.State{stateA, stateB, stateC}}
	runner := NewRunner(Config{
		Collector:     collector,
		DiffEngine:    diff.NewEngine(),
		EventStore:    eventStore,
		SnapshotStore: snapStore,
		SnapshotEvery: 2,
		Host:          "host-a",
		Profile:       "default",
		Source:        "test",
		Now:           func() time.Time { return time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC) },
		Logger:        io.Discard,
	})

	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("capture 1: %v", err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("capture 2: %v", err)
	}
	if _, err := runner.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("capture 3: %v", err)
	}

	entries, err := filepath.Glob(filepath.Join(root, "snapshots", "*.json"))
	if err != nil {
		t.Fatalf("glob snapshots: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one snapshot at cadence 2, got %d", len(entries))
	}
}

type collectResult struct {
	state model.State
	err   error
}

type sequenceCollector struct {
	states   []model.State
	sequence []collectResult
	index    int
}

func (s *sequenceCollector) Collect(_ context.Context) (model.State, error) {
	if len(s.sequence) > 0 {
		if s.index >= len(s.sequence) {
			return s.sequence[len(s.sequence)-1].state, s.sequence[len(s.sequence)-1].err
		}
		result := s.sequence[s.index]
		s.index++
		return result.state, result.err
	}

	if s.index >= len(s.states) {
		return s.states[len(s.states)-1], nil
	}
	state := s.states[s.index]
	s.index++
	return state, nil
}
