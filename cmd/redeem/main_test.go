package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
)

func TestHelpByDefault(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var err bytes.Buffer
	code := run(nil, &out, &err)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(out.String(), "redeem - terminal session history and restore") {
		t.Fatalf("expected help output, got %q", out.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var err bytes.Buffer
	code := run([]string{"nope"}, &out, &err)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(err.String(), "unknown command") {
		t.Fatalf("expected unknown command message, got %q", err.String())
	}
}

func TestCaptureOnceEndToEndWithFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	err := os.WriteFile(fixturePath, []byte(`{
		"workspaces": [{"id": "ws-1", "idx": 1, "name": "main"}],
		"windows": [{"id": 101, "app_id": "kitty", "title": "shell", "workspace_id": "ws-1", "pid": 4242}]
	}`), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stateDir := filepath.Join(root, "state")

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"capture", "once", "--state-dir", stateDir, "--fixture", fixturePath, "--host", "host-a", "--profile", "default"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	store, err := events.NewStore(stateDir)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	got, _, err := store.ReadSince(0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected capture once to append at least one event")
	}
}

func TestHistoryInspectAtTimestamp(t *testing.T) {
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
	defer writer.Close()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "shell"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"history", "inspect", "--state-dir", root, "--at", "2026-02-15T10:00:00Z"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "\"title\": \"shell\"") {
		t.Fatalf("expected history output with title, got %q", out.String())
	}
}

func TestRestoreApplyPreview(t *testing.T) {
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
	defer writer.Close()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "shell"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"restore", "apply", "--state-dir", root, "--at", "2026-02-15T10:00:00Z"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "restore_plan") {
		t.Fatalf("expected restore plan output, got %q", out.String())
	}
}

func TestPruneRunCommand(t *testing.T) {
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
	t0 := time.Now().UTC().AddDate(0, 0, -40)
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "old"}, StateHash: "sha256:old"}); err != nil {
		t.Fatalf("append old event: %v", err)
	}
	_ = writer.Close()

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"prune", "run", "--state-dir", root, "--days", "30"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "prune_summary") {
		t.Fatalf("expected prune summary output, got %q", out.String())
	}
}
