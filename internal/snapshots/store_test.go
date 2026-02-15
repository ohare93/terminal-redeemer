package snapshots

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotWriteReadRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new snapshot store: %v", err)
	}

	want := Snapshot{
		V:               1,
		CreatedAt:       time.Date(2026, 2, 15, 10, 20, 0, 0, time.UTC),
		Host:            "host-a",
		Profile:         "default",
		LastEventOffset: 123,
		StateHash:       "sha256:snap",
		State: map[string]any{
			"windows": map[string]any{"w:1": map[string]any{"workspace_idx": 2}},
		},
	}

	path, err := store.Write(want)
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	got, err := store.Read(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	if got.LastEventOffset != want.LastEventOffset {
		t.Fatalf("last_event_offset mismatch: want %d got %d", want.LastEventOffset, got.LastEventOffset)
	}
	if got.StateHash != want.StateHash {
		t.Fatalf("state_hash mismatch: want %q got %q", want.StateHash, got.StateHash)
	}
}

func TestLoadNearestAtOrBeforeTimestamp(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new snapshot store: %v", err)
	}

	base := time.Date(2026, 2, 15, 10, 20, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(10 * time.Minute), base.Add(20 * time.Minute)}

	for i, ts := range times {
		_, err := store.Write(Snapshot{
			V:               1,
			CreatedAt:       ts,
			Host:            "host-a",
			Profile:         "default",
			LastEventOffset: int64(i + 1),
			StateHash:       "sha256:snap",
			State:           map[string]any{"i": i},
		})
		if err != nil {
			t.Fatalf("write snapshot %d: %v", i, err)
		}
	}

	got, gotPath, err := store.LoadNearest(base.Add(15 * time.Minute))
	if err != nil {
		t.Fatalf("load nearest: %v", err)
	}

	if got.LastEventOffset != 2 {
		t.Fatalf("expected offset 2, got %d", got.LastEventOffset)
	}

	if filepath.Base(gotPath) != "1771151400.json" {
		t.Fatalf("unexpected snapshot path: %s", gotPath)
	}
}

func TestShouldSnapshotCadence(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		events int
		every  int
		want   bool
	}{
		{events: 0, every: 100, want: false},
		{events: 1, every: 100, want: false},
		{events: 100, every: 100, want: true},
		{events: 200, every: 100, want: true},
		{events: 201, every: 100, want: false},
		{events: 10, every: 0, want: false},
	}

	for _, tc := range testCases {
		got := ShouldSnapshot(tc.events, tc.every)
		if got != tc.want {
			t.Fatalf("events=%d every=%d: want %v got %v", tc.events, tc.every, tc.want, got)
		}
	}
}
