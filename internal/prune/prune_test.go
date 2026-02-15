package prune

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

func TestAgeBasedPruningEventsAndSnapshots(t *testing.T) {
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
	defer writer.Close()

	snapStore, err := snapshots.NewStore(root)
	if err != nil {
		t.Fatalf("new snapshot store: %v", err)
	}

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	oldTS := now.AddDate(0, 0, -40)
	newTS := now.AddDate(0, 0, -5)

	olderTS := oldTS.Add(-24 * time.Hour)
	if _, err := writer.Append(events.Event{V: 1, TS: olderTS, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "older"}, StateHash: "sha256:pre"}); err != nil {
		t.Fatalf("append older event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: oldTS, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "old"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append old event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: newTS, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "new"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append new event: %v", err)
	}

	if _, err := snapStore.Write(snapshots.Snapshot{V: 1, CreatedAt: oldTS, Host: "host-a", Profile: "default", LastEventOffset: 10, StateHash: "sha256:a", State: map[string]any{"windows": []any{}}}); err != nil {
		t.Fatalf("write old snapshot: %v", err)
	}
	if _, err := snapStore.Write(snapshots.Snapshot{V: 1, CreatedAt: oldTS.Add(-24 * time.Hour), Host: "host-a", Profile: "default", LastEventOffset: 5, StateHash: "sha256:oldest", State: map[string]any{"windows": []any{}}}); err != nil {
		t.Fatalf("write oldest snapshot: %v", err)
	}
	if _, err := snapStore.Write(snapshots.Snapshot{V: 1, CreatedAt: newTS, Host: "host-a", Profile: "default", LastEventOffset: 20, StateHash: "sha256:b", State: map[string]any{"windows": []any{}}}); err != nil {
		t.Fatalf("write new snapshot: %v", err)
	}
	_ = writer.Close()

	runner := NewRunner(root, 30, func() time.Time { return now })
	summary, err := runner.Run()
	if err != nil {
		t.Fatalf("prune run: %v", err)
	}
	if summary.EventsPruned == 0 || summary.SnapshotsPruned == 0 {
		t.Fatalf("expected old data pruned, got %+v", summary)
	}
}

func TestPruneSafetyWithActiveLock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		t.Fatalf("make meta dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "meta", "lock"), []byte("1234"), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	runner := NewRunner(root, 30, time.Now)
	if _, err := runner.Run(); err == nil {
		t.Fatal("expected active lock error")
	}
}

func TestNoDataLossForCurrentReplayWindow(t *testing.T) {
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
	defer writer.Close()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	cutoffOlder := now.AddDate(0, 0, -50)
	cutoffEdge := now.AddDate(0, 0, -31)
	inside := now.AddDate(0, 0, -5)

	if _, err := writer.Append(events.Event{V: 1, TS: cutoffOlder, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "older"}, StateHash: "sha256:1"}); err != nil {
		t.Fatalf("append older: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: cutoffEdge, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "edge"}, StateHash: "sha256:2"}); err != nil {
		t.Fatalf("append edge: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: inside, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "inside"}, StateHash: "sha256:3"}); err != nil {
		t.Fatalf("append inside: %v", err)
	}
	_ = writer.Close()

	runner := NewRunner(root, 30, func() time.Time { return now })
	if _, err := runner.Run(); err != nil {
		t.Fatalf("prune run: %v", err)
	}

	remaining, _, err := eventStore.ReadSince(0)
	if err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	if len(remaining) < 2 {
		t.Fatalf("expected at least anchor+inside events preserved, got %d", len(remaining))
	}
	if remaining[len(remaining)-1].Patch["title"] != "inside" {
		t.Fatalf("expected latest event preserved, got %#v", remaining[len(remaining)-1].Patch)
	}
}
