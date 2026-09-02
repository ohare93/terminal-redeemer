package checkpoints

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
)

func testCheckpoint(t *testing.T, boot, host, profile string, observed time.Time) Checkpoint {
	t.Helper()
	state := model.State{Workspaces: []model.Workspace{}, Windows: []model.Window{}}
	hash, err := state.Hash()
	if err != nil {
		t.Fatal(err)
	}
	integrityHash, err := RecoveryIntegrityHash(state, model.RecoveryInventory{})
	if err != nil {
		t.Fatal(err)
	}
	return Checkpoint{V: SchemaVersion, BootID: boot, Host: host, Profile: profile, ObservedAt: observed, State: state, StateHash: hash, IntegrityHash: integrityHash}
}

func TestReadAcceptsLegacyTitleSensitiveHash(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := model.State{Windows: []model.Window{{Key: "w-1", AppID: "kitty", Title: "legacy title"}}}
	legacyHash, err := state.HashWithTitles()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := Checkpoint{
		V: 1, BootID: "boot-old", Host: "host", Profile: "default",
		ObservedAt: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC), State: state,
		StateHash: legacyHash, EventOffset: 10,
	}
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("new writes must reject the legacy title-sensitive hash")
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(checkpoint.BootID, checkpoint.Host, checkpoint.Profile), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(checkpoint.BootID, checkpoint.Host, checkpoint.Profile)
	if err != nil {
		t.Fatalf("read legacy checkpoint: %v", err)
	}
	if got.StateHash != legacyHash || got.State.Windows[0].Title != "legacy title" {
		t.Fatalf("legacy checkpoint changed: %#v", got)
	}
}

func TestReadAcceptsSchemaTwoCheckpoint(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := model.State{Windows: []model.Window{{Key: "w-1", AppID: "kitty"}}}
	hash, err := state.Hash()
	if err != nil {
		t.Fatal(err)
	}
	legacy := Checkpoint{V: 2, BootID: "boot-v2", Host: "host", Profile: "default", ObservedAt: time.Now().UTC(), State: state, StateHash: hash}
	payload, _ := json.Marshal(legacy)
	if err := os.WriteFile(store.Path(legacy.BootID, legacy.Host, legacy.Profile), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(legacy.BootID, legacy.Host, legacy.Profile); err != nil {
		t.Fatalf("read schema-2 checkpoint: %v", err)
	}
}

func TestRecoveryIntegrityDetectsTampering(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := testCheckpoint(t, "boot-a", "host", "default", time.Now().UTC())
	checkpoint.Recovery = model.RecoveryInventory{
		ActiveSessions: []string{"alpha"},
		Sessions:       []model.RecoverySession{{Name: "alpha", CWD: "/original", Visible: false}},
	}
	checkpoint.IntegrityHash, err = RecoveryIntegrityHash(checkpoint.State, checkpoint.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Write(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte("/original"), []byte("/tampered"), 1)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("boot-a", "host", "default"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered recovery payload accepted: %v", err)
	}
}

func TestStoreRoundTripAndIdentityPaths(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	first := testCheckpoint(t, "boot/a", "host", "default", now)
	second := testCheckpoint(t, "boot/a", "host", "other", now)
	pathA, err := store.Write(first)
	if err != nil {
		t.Fatal(err)
	}
	pathB, err := store.Write(second)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("event_offset")) {
		t.Fatalf("schema-2 checkpoint retained event timeline metadata: %s", payload)
	}
	if pathA == pathB || filepath.Dir(pathA) != filepath.Join(root, "checkpoints") {
		t.Fatalf("unsafe/colliding paths: %q %q", pathA, pathB)
	}
	info, err := os.Stat(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("checkpoint permissions=%#o, want 0600", got)
	}
	got, err := store.Read(first.BootID, first.Host, first.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(now) || got.StateHash != first.StateHash {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestWriteUsesFileSyncRenameDirectorySyncOrdering(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]string, 0, 3)
	store.syncFile = func(file *os.File) error {
		steps = append(steps, "file_fsync")
		return file.Sync()
	}
	store.rename = func(oldPath, newPath string) error {
		steps = append(steps, "rename")
		return os.Rename(oldPath, newPath)
	}
	store.syncDir = func(path string) error {
		steps = append(steps, "directory_fsync")
		return syncDirectory(path)
	}
	if _, err := store.Write(testCheckpoint(t, "boot-a", "host", "default", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	want := []string{"file_fsync", "rename", "directory_fsync"}
	if len(steps) != len(want) {
		t.Fatalf("steps=%v", steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("steps=%v want=%v", steps, want)
		}
	}
}

func TestInterruptedPublishPreservesUsablePriorCheckpoint(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	original := testCheckpoint(t, "boot-a", "host", "default", now)
	if _, err := store.Write(original); err != nil {
		t.Fatal(err)
	}
	store.rename = func(string, string) error { return errors.New("rename failed") }
	newer := original
	newer.ObservedAt = now.Add(time.Minute)
	if _, err := store.Write(newer); err == nil {
		t.Fatal("expected replacement failure")
	}
	got, err := store.Read(original.BootID, original.Host, original.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(original.ObservedAt) {
		t.Fatalf("failed replacement changed checkpoint: %#v", got)
	}
	matches, err := filepath.Glob(filepath.Join(store.dir, ".checkpoint-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v %v", matches, err)
	}
}

func TestListReportsCorruptionWithoutHidingValidCheckpoint(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(testCheckpoint(t, "boot-a", "host", "default", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "corrupt.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid, issues, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 1 || len(issues) != 1 || !errors.Is(issues[0].Err, ErrInvalid) {
		t.Fatalf("valid=%#v issues=%#v", valid, issues)
	}
}

func TestPrunePreservesCurrentAndNewestPriorWhileRemovingOlderExpiredBoots(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	for boot, observed := range map[string]time.Time{
		"oldest-prior": now.Add(-72 * time.Hour),
		"newest-prior": now.Add(-48 * time.Hour),
		"current":      now.Add(-36 * time.Hour),
	} {
		if _, err := store.Write(testCheckpoint(t, boot, "host", "default", observed)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Prune(root, now.Add(-24*time.Hour), "current")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	valid, issues, err := List(root)
	if err != nil || len(issues) != 0 || len(valid) != 2 {
		t.Fatalf("remaining=%#v issues=%#v err=%v", valid, issues, err)
	}
	remaining := map[string]bool{}
	for _, checkpoint := range valid {
		remaining[checkpoint.BootID] = true
	}
	if !remaining["current"] || !remaining["newest-prior"] || remaining["oldest-prior"] {
		t.Fatalf("remaining boots=%v", remaining)
	}
}
