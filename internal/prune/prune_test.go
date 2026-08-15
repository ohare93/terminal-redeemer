package prune

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/storelock"
)

func writeCheckpoint(t *testing.T, store *checkpoints.Store, boot string, at time.Time) {
	t.Helper()
	state := model.State{}
	hash, err := state.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(checkpoints.Checkpoint{V: checkpoints.SchemaVersion, BootID: boot, Host: "host", Profile: "default", ObservedAt: at, State: state, StateHash: hash}); err != nil {
		t.Fatal(err)
	}
}

func newTestPruneRunner(root string, days int, now time.Time, currentBootID string) *Runner {
	runner := NewRunner(root, days, func() time.Time { return now })
	runner.bootIDSource = func() (string, error) { return currentBootID, nil }
	return runner
}

func TestPrunePreservesSoleExpiredPriorCheckpoint(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	writeCheckpoint(t, store, "prior", now.Add(-40*24*time.Hour))

	summary, err := newTestPruneRunner(root, 30, now, "current").Run()
	if err != nil {
		t.Fatal(err)
	}
	if summary.CheckpointsPruned != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if _, err := store.Read("prior", "host", "default"); err != nil {
		t.Fatalf("sole prior-boot recovery candidate was pruned: %v", err)
	}
}

func TestPrunePreservesCurrentAndNewestPriorWhileBoundingOlderBoots(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	writeCheckpoint(t, store, "oldest-prior", now.Add(-60*24*time.Hour))
	writeCheckpoint(t, store, "newest-prior", now.Add(-40*24*time.Hour))
	writeCheckpoint(t, store, "current", now.Add(-35*24*time.Hour))

	summary, err := newTestPruneRunner(root, 30, now, "current").Run()
	if err != nil {
		t.Fatal(err)
	}
	if summary.CheckpointsPruned != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	valid, issues, err := checkpoints.List(root)
	if err != nil || len(issues) != 0 || len(valid) != 2 {
		t.Fatalf("valid=%#v issues=%#v err=%v", valid, issues, err)
	}
	remaining := map[string]bool{}
	for _, checkpoint := range valid {
		remaining[checkpoint.BootID] = true
	}
	if !remaining["current"] || !remaining["newest-prior"] || remaining["oldest-prior"] {
		t.Fatalf("remaining=%v", remaining)
	}
}

func TestPruneRefusesActiveWriter(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	writeCheckpoint(t, store, "old", time.Now().Add(-60*24*time.Hour))
	lock, err := storelock.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := newTestPruneRunner(root, 30, time.Now(), "current").Run(); !errors.Is(err, ErrActiveWriter) {
		t.Fatalf("expected active writer error, got %v", err)
	}
	if _, err := store.Read("old", "host", "default"); err != nil {
		t.Fatalf("prune mutated while writer held lock: %v", err)
	}
}

func TestPruneIgnoresStaleLockFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "meta", "lock"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestPruneRunner(root, 30, time.Now(), "current").Run(); err != nil {
		t.Fatalf("stale marker retained lock: %v", err)
	}
}
