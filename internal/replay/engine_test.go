package replay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

func TestReplayFromEmptyLog(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	state, err := engine.At(time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("replay at: %v", err)
	}
	if len(state.Windows) != 0 {
		t.Fatalf("expected empty windows, got %d", len(state.Windows))
	}
}

func TestReplayWithSnapshotAndTail(t *testing.T) {
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

	writer, err := eventStore.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	offsetA, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "a"}, StateHash: "sha256:a"})
	if err != nil {
		t.Fatalf("append A: %v", err)
	}
	_, err = writer.Append(events.Event{V: 1, TS: t0.Add(2 * time.Second), Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "b"}, StateHash: "sha256:b"})
	if err != nil {
		t.Fatalf("append B: %v", err)
	}

	_, err = snapStore.Write(snapshots.Snapshot{V: 1, CreatedAt: t0.Add(1 * time.Second), Host: "host-a", Profile: "default", LastEventOffset: offsetA, StateHash: "sha256:a", State: map[string]any{"workspaces": []any{}, "windows": []any{map[string]any{"key": "w-1", "app_id": "kitty", "workspace_id": "ws-1", "title": "a"}}}})
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	engine, err := NewEngine(root)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	state, err := engine.At(t0.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("replay at: %v", err)
	}
	if len(state.Windows) != 1 || state.Windows[0].Title != "b" {
		t.Fatalf("expected title b from tail replay, got %#v", state.Windows)
	}
}

func TestReplaySkipsCorruptedLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	eventStore, err := events.NewStore(root)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	writer, err := eventStore.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "a"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append A: %v", err)
	}

	f, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		t.Fatalf("write corrupt line: %v", err)
	}
	if _, err := f.WriteString("{\"v\":1,\"ts\":\"2026-02-15T10:00:01Z\",\"host\":\"host-a\",\"profile\":\"default\",\"event_type\":\"window_patch\",\"window_key\":\"w-1\",\"patch\":{\"title\":\"b\"},\"state_hash\":\"sha256:b\"}\n"); err != nil {
		t.Fatalf("write good line: %v", err)
	}
	_ = f.Close()

	engine, err := NewEngine(root)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	state, err := engine.At(t0.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("replay at: %v", err)
	}
	if len(state.Windows) != 1 || state.Windows[0].Title != "b" {
		t.Fatalf("expected replay to skip corrupt line and apply valid one, got %#v", state.Windows)
	}
}

func TestReplayTimestampBoundaryInclusive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	eventStore, err := events.NewStore(root)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	writer, err := eventStore.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "a"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append A: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: t0.Add(1 * time.Second), Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "b"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append B: %v", err)
	}

	engine, err := NewEngine(root)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	stateAtBoundary, err := engine.At(t0)
	if err != nil {
		t.Fatalf("replay at boundary: %v", err)
	}
	if len(stateAtBoundary.Windows) != 1 || stateAtBoundary.Windows[0].Title != "a" {
		t.Fatalf("expected first event included at boundary, got %#v", stateAtBoundary.Windows)
	}

	stateBeforeSecond, err := engine.At(t0.Add(500 * time.Millisecond))
	if err != nil {
		t.Fatalf("replay before second: %v", err)
	}
	if len(stateBeforeSecond.Windows) != 1 || stateBeforeSecond.Windows[0].Title != "a" {
		t.Fatalf("expected second event excluded before boundary, got %#v", stateBeforeSecond.Windows)
	}
}
