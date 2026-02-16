package events

import (
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

func TestEventSnapshotIntegrationDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	eventStore, err := NewStore(root)
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
	t.Cleanup(func() {
		_ = writer.Close()
	})

	base := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	var lastOffset int64
	for i := range 4 {
		lastOffset, err = writer.Append(Event{
			V:         1,
			TS:        base.Add(time.Duration(i) * time.Second),
			Host:      "host-a",
			Profile:   "default",
			EventType: "window_patch",
			WindowKey: "w-1",
			Patch:     map[string]any{"title": "a"},
			StateHash: "sha256:abc",
		})
		if err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}

		if snapshots.ShouldSnapshot(i+1, 2) {
			_, err := snapStore.Write(snapshots.Snapshot{
				V:               1,
				CreatedAt:       base.Add(time.Duration(i) * time.Second),
				Host:            "host-a",
				Profile:         "default",
				LastEventOffset: lastOffset,
				StateHash:       "sha256:abc",
				State:           map[string]any{"event_count": i + 1},
			})
			if err != nil {
				t.Fatalf("write snapshot %d: %v", i, err)
			}
		}
	}

	got, _, err := snapStore.LoadNearest(base.Add(3 * time.Second))
	if err != nil {
		t.Fatalf("load nearest snapshot: %v", err)
	}

	if got.LastEventOffset != lastOffset {
		t.Fatalf("expected last offset %d, got %d", lastOffset, got.LastEventOffset)
	}
}
