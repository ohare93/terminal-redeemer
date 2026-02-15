package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
