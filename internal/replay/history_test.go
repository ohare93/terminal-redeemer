package replay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
)

func TestListEventsMissingFileReturnsEmpty(t *testing.T) {
	t.Parallel()

	got, err := ListEvents(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no events, got %d", len(got))
	}
}

func TestListEventsSkipsInvalidLinesAndAppliesInclusiveBounds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := events.NewStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	writer, err := store.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Second)
	t2 := t0.Add(2 * time.Second)

	for idx, ts := range []time.Time{t0, t1, t2} {
		if _, err := writer.Append(events.Event{
			V:         1,
			TS:        ts,
			Host:      "host-a",
			Profile:   "default",
			EventType: "window_patch",
			WindowKey: "w-1",
			Patch:     map[string]any{"title": idx},
			StateHash: "sha256:x",
		}); err != nil {
			t.Fatalf("append event %d: %v", idx, err)
		}
	}

	f, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		t.Fatalf("write invalid line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close events file: %v", err)
	}

	from := t0
	to := t1
	got, err := ListEvents(root, &from, &to)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events at inclusive bounds, got %d", len(got))
	}
	if !got[0].TS.Equal(t0) || !got[1].TS.Equal(t1) {
		t.Fatalf("unexpected events returned: %#v", got)
	}

	fromAfterTo := t2
	toBeforeFrom := t1
	got, err = ListEvents(root, &fromAfterTo, &toBeforeFrom)
	if err != nil {
		t.Fatalf("list events with inverted bounds: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no events for inverted bounds, got %d", len(got))
	}
}
