package events

import (
	"errors"
	"testing"
	"time"
)

func TestStoreAppendAndReadInOrder(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	writer, err := store.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	events := []Event{
		{V: 1, TS: time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC), Host: "host-a", Profile: "default", EventType: "window_patch", StateHash: "sha256:a"},
		{V: 1, TS: time.Date(2026, 2, 15, 10, 0, 1, 0, time.UTC), Host: "host-a", Profile: "default", EventType: "window_patch", StateHash: "sha256:b"},
	}

	for _, e := range events {
		if _, err := writer.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, _, err := store.ReadSince(0)
	if err != nil {
		t.Fatalf("read since: %v", err)
	}

	if len(got) != len(events) {
		t.Fatalf("expected %d events, got %d", len(events), len(got))
	}

	for i := range events {
		if got[i].StateHash != events[i].StateHash {
			t.Fatalf("event[%d] mismatch: want %q got %q", i, events[i].StateHash, got[i].StateHash)
		}
	}
}

func TestStoreRejectsMalformedEvent(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	writer, err := store.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	bad := Event{V: 1, TS: time.Now().UTC(), Host: "host-a", Profile: "default", StateHash: "sha256:x"}
	if _, err := writer.Append(bad); err == nil {
		t.Fatal("expected malformed event error")
	}

	got, _, err := store.ReadSince(0)
	if err != nil {
		t.Fatalf("read since: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("expected 0 events, got %d", len(got))
	}
}

func TestStoreReplayCursorTracking(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	writer, err := store.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	base := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		e := Event{V: 1, TS: base.Add(time.Duration(i) * time.Second), Host: "host-a", Profile: "default", EventType: "window_patch", StateHash: "sha256:seed"}
		if _, err := writer.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	firstBatch, cursor, err := store.ReadSince(0)
	if err != nil {
		t.Fatalf("read first batch: %v", err)
	}
	if len(firstBatch) != 3 {
		t.Fatalf("expected 3 events, got %d", len(firstBatch))
	}

	if _, err := writer.Append(Event{V: 1, TS: base.Add(4 * time.Second), Host: "host-a", Profile: "default", EventType: "window_patch", StateHash: "sha256:new"}); err != nil {
		t.Fatalf("append incremental: %v", err)
	}

	secondBatch, nextCursor, err := store.ReadSince(cursor)
	if err != nil {
		t.Fatalf("read second batch: %v", err)
	}
	if len(secondBatch) != 1 {
		t.Fatalf("expected 1 event, got %d", len(secondBatch))
	}
	if secondBatch[0].StateHash != "sha256:new" {
		t.Fatalf("expected new event hash, got %q", secondBatch[0].StateHash)
	}
	if nextCursor <= cursor {
		t.Fatalf("expected cursor advance: %d -> %d", cursor, nextCursor)
	}
}

func TestLockPreventsConcurrentWriters(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	writerA, err := store.AcquireWriter()
	if err != nil {
		t.Fatalf("acquire first writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writerA.Close()
	})

	_, err = store.AcquireWriter()
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}
