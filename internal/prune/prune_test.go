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

func TestPruneBoundsRetainedBootCheckpoints(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoints.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	writeCheckpoint(t, store, "old", now.Add(-40*24*time.Hour))
	writeCheckpoint(t, store, "current", now.Add(-time.Hour))

	summary, err := NewRunner(root, 30, func() time.Time { return now }).Run()
	if err != nil {
		t.Fatal(err)
	}
	if summary.CheckpointsPruned != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	valid, issues, err := checkpoints.List(root)
	if err != nil || len(issues) != 0 || len(valid) != 1 || valid[0].BootID != "current" {
		t.Fatalf("valid=%#v issues=%#v err=%v", valid, issues, err)
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
	if _, err := NewRunner(root, 30, time.Now).Run(); !errors.Is(err, ErrActiveWriter) {
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
	if _, err := NewRunner(root, 30, time.Now).Run(); err != nil {
		t.Fatalf("stale marker retained lock: %v", err)
	}
}
