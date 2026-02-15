package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/config"
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

func TestSubcommandHelpExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "capture once", args: []string{"capture", "once", "--help"}},
		{name: "capture run", args: []string{"capture", "run", "--help"}},
		{name: "history list", args: []string{"history", "list", "--help"}},
		{name: "history inspect", args: []string{"history", "inspect", "--help"}},
		{name: "restore apply", args: []string{"restore", "apply", "--help"}},
		{name: "restore tui", args: []string{"restore", "tui", "--help"}},
		{name: "prune run", args: []string{"prune", "run", "--help"}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			var stderr bytes.Buffer
			code := run(tc.args, &out, &stderr)

			if code != 0 {
				t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "Usage of") {
				t.Fatalf("expected help usage output on stderr, got %q", stderr.String())
			}
		})
	}
}

func TestInvalidUsageExitCodesRemainTwo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "capture once unknown flag", args: []string{"capture", "once", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "capture run unknown flag", args: []string{"capture", "run", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "history list unknown flag", args: []string{"history", "list", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "restore apply missing at", args: []string{"restore", "apply"}, want: "restore apply requires --at"},
		{name: "restore tui unknown flag", args: []string{"restore", "tui", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "prune run unknown flag", args: []string{"prune", "run", "--no-such-flag"}, want: "flag provided but not defined"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			var stderr bytes.Buffer
			code := run(tc.args, &out, &stderr)

			if code != 2 {
				t.Fatalf("expected code 2, got %d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("expected stderr containing %q, got %q", tc.want, stderr.String())
			}
		})
	}
}

func TestHistoryInspectDefaultsToLatest(t *testing.T) {
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
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "old"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append old event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: t0.Add(2 * time.Second), Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "new"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append new event: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"history", "inspect", "--state-dir", root}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "\"title\": \"new\"") {
		t.Fatalf("expected latest state output, got %q", out.String())
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

func TestCaptureOnceEndToEndWithCommandSnapshotter(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")

	t.Setenv("REDEEM_NIRI_CMD", "printf '{\"workspaces\":[{\"id\":\"ws-1\",\"idx\":1}],\"windows\":[{\"id\":101,\"app_id\":\"kitty\",\"workspace_id\":\"ws-1\",\"pid\":4242}]}'")

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"capture", "once", "--state-dir", stateDir, "--host", "host-a", "--profile", "default"}, &out, &stderr)
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
	defer func() {
		_ = writer.Close()
	}()

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
	defer func() {
		_ = writer.Close()
	}()

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

func TestHistoryInspectInvalidTimestamp(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"history", "inspect", "--state-dir", t.TempDir(), "--at", "not-a-time"}, &out, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "invalid --at") {
		t.Fatalf("expected invalid --at error, got %q", stderr.String())
	}
}

func TestRestoreApplyInvalidTimestamp(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"restore", "apply", "--state-dir", t.TempDir(), "--at", "not-a-time"}, &out, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "invalid --at") {
		t.Fatalf("expected invalid --at error, got %q", stderr.String())
	}
}

func TestParseAtSpecSupportsRelativeAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	got, err := parseAtSpec("1m", now)
	if err != nil {
		t.Fatalf("parse 1m: %v", err)
	}
	if !got.Equal(now.Add(-1 * time.Minute)) {
		t.Fatalf("expected now-1m, got %s", got)
	}

	got, err = parseAtSpec("2d", now)
	if err != nil {
		t.Fatalf("parse 2d: %v", err)
	}
	if !got.Equal(now.Add(-48 * time.Hour)) {
		t.Fatalf("expected now-48h, got %s", got)
	}

	got, err = parseAtSpec("1h30m", now)
	if err != nil {
		t.Fatalf("parse 1h30m: %v", err)
	}
	if !got.Equal(now.Add(-90 * time.Minute)) {
		t.Fatalf("expected now-90m, got %s", got)
	}
}

func TestHistoryInspectRelativeAt(t *testing.T) {
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

	now := time.Now().UTC()
	if _, err := writer.Append(events.Event{V: 1, TS: now.Add(-2 * time.Minute), Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "title": "older"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append older event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: now.Add(-20 * time.Second), Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "newer"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append newer event: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"history", "inspect", "--state-dir", root, "--at", "1m"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "\"title\": \"older\"") {
		t.Fatalf("expected state at 1m ago to include older title, got %q", out.String())
	}
}

func TestHistoryListEmptyStateDir(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"history", "list", "--state-dir", t.TempDir()}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("expected empty output for empty history, got %q", out.String())
	}
}

func TestRestoreApplyPreviewAndApplyParityForSkippedOnlyPlan(t *testing.T) {
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
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "firefox", "workspace_id": "ws-1", "title": "web"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	var previewOut bytes.Buffer
	var previewErr bytes.Buffer
	previewCode := run([]string{"restore", "apply", "--state-dir", root, "--at", "2026-02-15T10:00:00Z"}, &previewOut, &previewErr)
	if previewCode != 0 {
		t.Fatalf("expected preview code 0, got %d stderr=%q", previewCode, previewErr.String())
	}
	if !strings.Contains(previewOut.String(), "restore_plan ready=0 skipped=1") {
		t.Fatalf("unexpected preview summary: %q", previewOut.String())
	}

	var applyOut bytes.Buffer
	var applyErr bytes.Buffer
	applyCode := run([]string{"restore", "apply", "--state-dir", root, "--at", "2026-02-15T10:00:00Z", "--yes"}, &applyOut, &applyErr)
	if applyCode != 0 {
		t.Fatalf("expected apply code 0, got %d stderr=%q", applyCode, applyErr.String())
	}
	if !strings.Contains(applyOut.String(), "restore_summary restored=0 skipped=1 failed=0") {
		t.Fatalf("unexpected apply summary: %q", applyOut.String())
	}
}

func TestRestoreApplyReportsDegradedSkippedAndFailedItems(t *testing.T) {
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
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-term", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "terminal": map[string]any{"cwd": "/tmp/project"}}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append terminal event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-skip", Patch: map[string]any{"app_id": "firefox", "workspace_id": "ws-1"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append skipped event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-fail", Patch: map[string]any{"app_id": "code", "workspace_id": "ws-1"}, StateHash: "sha256:c"}); err != nil {
		t.Fatalf("append failed event: %v", err)
	}

	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + root + "\nrestore:\n  appAllowlist:\n    code: \"false\"\n  terminal:\n    command: kitty\n    zellijAttachOrCreate: true\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "restore", "apply", "--at", "2026-02-15T10:00:00Z", "--yes"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected apply code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "restore_item window_key=w-term status=degraded reason=\"missing terminal session tag\"") {
		t.Fatalf("expected degraded detail line, got %q", out.String())
	}
	if !strings.Contains(out.String(), "restore_item window_key=w-skip status=skipped reason=\"app not allowlisted\"") {
		t.Fatalf("expected skipped detail line, got %q", out.String())
	}
	if !strings.Contains(out.String(), "restore_item window_key=w-fail status=failed error=") {
		t.Fatalf("expected failed detail line, got %q", out.String())
	}
	if !strings.Contains(out.String(), "restore_summary restored=0 skipped=2 failed=1") {
		t.Fatalf("unexpected apply summary: %q", out.String())
	}
}

func TestRestoreApplyPreviewUsesConfiguredRestoreSettings(t *testing.T) {
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
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-term", Patch: map[string]any{"app_id": "kitty", "workspace_id": "ws-1", "terminal": map[string]any{"cwd": "/tmp/project"}}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append terminal event: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-app", Patch: map[string]any{"app_id": "code", "workspace_id": "ws-1"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append app event: %v", err)
	}

	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + root + "\nrestore:\n  appAllowlist:\n    code: \"true\"\n  terminal:\n    command: kitty\n    zellijAttachOrCreate: false\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "restore", "apply", "--at", "2026-02-15T10:00:00Z"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected preview code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "restore_plan ready=2 skipped=0 degraded=0") {
		t.Fatalf("expected config-driven ready plan, got %q", out.String())
	}
}

func TestRestoreApplyDryRunPrintsActionsWithoutExecuting(t *testing.T) {
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
	defer func() { _ = writer.Close() }()

	t0 := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-app", Patch: map[string]any{"app_id": "code", "workspace_id": "ws-1"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append app event: %v", err)
	}

	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + root + "\nrestore:\n  appAllowlist:\n    code: \"false\"\n  terminal:\n    command: kitty\n    zellijAttachOrCreate: true\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "restore", "apply", "--at", "2026-02-15T10:00:00Z", "--dry-run"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected dry-run code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "Would Restore:") {
		t.Fatalf("expected would-restore section, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Summary: would_restore=1 skipped=0 degraded=0") {
		t.Fatalf("unexpected dry-run summary: %q", out.String())
	}
}

func TestGlobalConfigAppliesCaptureDefaultsAndCLIOverrides(t *testing.T) {
	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	err := os.WriteFile(fixturePath, []byte(`{
		"workspaces": [{"id": "ws-1", "idx": 1, "name": "main"}],
		"windows": [{"id": 101, "app_id": "kitty", "title": "shell", "workspace_id": "ws-1", "pid": 4242}]
	}`), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stateFromConfig := filepath.Join(root, "state-config")
	overrideStateDir := filepath.Join(root, "state-override")
	configPath := filepath.Join(root, "redeem.yaml")
	err = os.WriteFile(configPath, []byte("stateDir: "+stateFromConfig+"\nhost: cfg-host\nprofile: cfg-profile\n"), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "capture", "once", "--fixture", fixturePath}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	store, err := events.NewStore(stateFromConfig)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	got, _, err := store.ReadSince(0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected captured event")
	}
	if got[0].Host != "cfg-host" || got[0].Profile != "cfg-profile" {
		t.Fatalf("expected config host/profile, got host=%q profile=%q", got[0].Host, got[0].Profile)
	}

	out.Reset()
	stderr.Reset()
	code = run([]string{"--config", configPath, "capture", "once", "--fixture", fixturePath, "--state-dir", overrideStateDir, "--host", "cli-host", "--profile", "cli-profile"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected override code 0, got %d stderr=%q", code, stderr.String())
	}

	overrideStore, err := events.NewStore(overrideStateDir)
	if err != nil {
		t.Fatalf("new override event store: %v", err)
	}
	overrideEvents, _, err := overrideStore.ReadSince(0)
	if err != nil {
		t.Fatalf("read override events: %v", err)
	}
	if len(overrideEvents) == 0 {
		t.Fatal("expected captured event with CLI overrides")
	}
	if overrideEvents[0].Host != "cli-host" || overrideEvents[0].Profile != "cli-profile" {
		t.Fatalf("expected CLI host/profile, got host=%q profile=%q", overrideEvents[0].Host, overrideEvents[0].Profile)
	}
}

func TestGlobalConfigExplicitMissingFileErrors(t *testing.T) {
	pathDir := t.TempDir()
	for _, cmd := range []string{"kitty", "zellij", "niri"} {
		cmdPath := filepath.Join(pathDir, cmd)
		err := os.WriteFile(cmdPath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
		if err != nil {
			t.Fatalf("write fake command %s: %v", cmd, err)
		}
	}
	t.Setenv("PATH", pathDir)

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml"), "doctor"}, &out, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(out.String(), "doctor_check name=config_load status=fail") {
		t.Fatalf("expected config check failure, got %q", out.String())
	}
	if !strings.Contains(out.String(), "doctor_summary total=") {
		t.Fatalf("expected doctor summary, got %q", out.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestDoctorPassExitCode(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + stateDir + "\nrestore:\n  terminal:\n    command: kitty\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	pathDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatalf("mkdir path dir: %v", err)
	}
	for _, cmd := range []string{"kitty", "zellij", "niri"} {
		cmdPath := filepath.Join(pathDir, cmd)
		err := os.WriteFile(cmdPath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
		if err != nil {
			t.Fatalf("write fake command %s: %v", cmd, err)
		}
	}
	t.Setenv("PATH", pathDir)

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "doctor"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d output=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "doctor_summary total=7 passed=7 failed=0") {
		t.Fatalf("unexpected doctor summary: %q", out.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestCaptureNiriCommandDefaultPrecedence(t *testing.T) {
	cfg := config.Defaults()

	t.Setenv("REDEEM_NIRI_CMD", "env-niri")
	if got := captureNiriCommandDefault(cfg); got != "env-niri" {
		t.Fatalf("expected env override for default command, got %q", got)
	}

	cfg.Capture.NiriCommand = "custom-niri --json"
	if got := captureNiriCommandDefault(cfg); got != "custom-niri --json" {
		t.Fatalf("expected explicit config command to win, got %q", got)
	}

	cfg.Capture.NiriCommand = ""
	if got := captureNiriCommandDefault(cfg); got != "env-niri" {
		t.Fatalf("expected env override when config command empty, got %q", got)
	}

	t.Setenv("REDEEM_NIRI_CMD", "")
	if got := captureNiriCommandDefault(config.Defaults()); got != config.Defaults().Capture.NiriCommand {
		t.Fatalf("expected builtin default when env unset, got %q", got)
	}
}

func TestRestoreApplyStateDirFlagOverridesConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configStateDir := filepath.Join(root, "state-config")
	overrideStateDir := filepath.Join(root, "state-override")

	store, err := events.NewStore(overrideStateDir)
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
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"app_id": "code", "workspace_id": "ws-1"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + configStateDir + "\nrestore:\n  appAllowlist:\n    code: \"code\"\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "restore", "apply", "--state-dir", overrideStateDir, "--at", "2026-02-15T10:00:00Z"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(out.String(), "restore_plan ready=1 skipped=0 degraded=0") {
		t.Fatalf("expected restore to use CLI state-dir override, got %q", out.String())
	}
}

func TestHistoryListFromToBoundaryInclusive(t *testing.T) {
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
	if _, err := writer.Append(events.Event{V: 1, TS: t0, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "a"}, StateHash: "sha256:a"}); err != nil {
		t.Fatalf("append t0: %v", err)
	}
	if _, err := writer.Append(events.Event{V: 1, TS: t1, Host: "host-a", Profile: "default", EventType: "window_patch", WindowKey: "w-1", Patch: map[string]any{"title": "b"}, StateHash: "sha256:b"}); err != nil {
		t.Fatalf("append t1: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"history", "list", "--state-dir", root, "--from", "2026-02-15T10:00:00Z", "--to", "2026-02-15T10:00:01Z"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 events in inclusive boundary range, got %d output=%q", len(lines), out.String())
	}
}

func TestParseOptionalTimestampWhitespace(t *testing.T) {
	t.Parallel()

	ts, err := parseOptionalTimestamp("   ")
	if err != nil {
		t.Fatalf("parse whitespace timestamp: %v", err)
	}
	if ts != nil {
		t.Fatalf("expected nil timestamp for whitespace input, got %v", ts)
	}
}
