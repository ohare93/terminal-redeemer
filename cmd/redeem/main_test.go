package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/replay"
	"github.com/jmo/terminal-redeemer/internal/resume"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "terminal-redeemer-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", root)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func TestHelpByDefault(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	code := run(nil, &out, &errBuf)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(out.String(), "redeem - terminal session history and restore") || !strings.Contains(out.String(), "Snapshot, discover, and mirror live terminal sessions") {
		t.Fatalf("expected discoverable mirror help output, got %q", out.String())
	}
	if stderrWithoutWarning(errBuf.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", errBuf.String())
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

func TestRemovedSliceCommandIsUnknown(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var err bytes.Buffer
	code := run([]string{"slice", "--help"}, &out, &err)
	if code != 2 || !strings.Contains(err.String(), "unknown command: slice") {
		t.Fatalf("code=%d stderr=%q", code, err.String())
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
		{name: "mirror snapshot", args: []string{"mirror", "snapshot", "--help"}},
		{name: "mirror list", args: []string{"mirror", "list", "--help"}},
		{name: "mirror open", args: []string{"mirror", "open", "--help"}},
		{name: "mirror new", args: []string{"mirror", "new", "--help"}},
		{name: "mirror status", args: []string{"mirror", "status", "--help"}},
		{name: "mirror close", args: []string{"mirror", "close", "--help"}},
		{name: "mirror paste-image", args: []string{"mirror", "paste-image", "--help"}},
		{name: "restore apply", args: []string{"restore", "apply", "--help"}},
		{name: "restore tui", args: []string{"restore", "tui", "--help"}},
		{name: "resume", args: []string{"resume", "--help"}},
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
		{name: "mirror snapshot unknown flag", args: []string{"mirror", "snapshot", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "restore apply missing at", args: []string{"restore", "apply"}, want: "restore apply requires --at"},
		{name: "restore tui unknown flag", args: []string{"restore", "tui", "--no-such-flag"}, want: "flag provided but not defined"},
		{name: "resume invalid timeout", args: []string{"resume", "--timeout", "0s"}, want: "--timeout and --poll-interval must be positive"},
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

func TestResumeDryRunSelectsPriorBootAndOnlyListsSessions(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Add(-time.Minute)
	event := events.Event{
		V: 1, TS: now, Host: "local", Profile: "default", BootID: "prior-boot",
		EventType: "state_full", StateHash: "sha256:resume",
		State: map[string]any{
			"workspaces": []any{map[string]any{"id": "old-id", "index": 2, "name": "dev", "output": "DP-1"}},
			"windows": []any{map[string]any{
				"key": "w-terminal", "app_id": "kitty", "workspace_id": "old-id",
				"workspace_ref": map[string]any{"name": "dev", "output": "DP-1", "index": 2},
				"terminal":      map[string]any{"cwd": "/tmp/project", "session_tag": "session-a"},
			}},
		},
	}
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "events.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := replay.ListCheckpoints(root)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoint fixture invalid: count=%d err=%v", len(checkpoints), err)
	}
	fixture := filepath.Join(t.TempDir(), "niri.json")
	if err := os.WriteFile(fixture, []byte(`{"workspaces":[{"id":"current-id","idx":5,"name":"dev","output":"DP-2"}],"windows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "unexpected")
	zellij := filepath.Join(bin, "zellij")
	script := "#!/bin/sh\nif [ \"$1 $2\" != \"list-sessions --short\" ]; then echo x > " + marker + "; exit 9; fi\nprintf 'session-a\\n'\n"
	if err := os.WriteFile(zellij, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("stateDir: "+root+"\nhost: local\nprofile: default\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, stderr bytes.Buffer
	code := run([]string{"--config", configPath, "resume", "--dry-run", "--fixture", fixture}, &out, &stderr)
	if code != 0 {
		t.Fatalf("resume code = %d, stderr=%q", code, stderr.String())
	}
	for _, want := range []string{
		`resume_candidate status=ready boot_id="prior-boot"`,
		`resume_item window_key="w-terminal" session="session-a" status=ready workspace_method=name`,
		"resume_summary ready=1 already_open=0 unavailable=0 degraded=0 stale=0 failed=0 skipped=0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dry run attempted an unexpected Zellij command")
	}
}

func TestResumeWaitsForNiriBeforeCheckpointSelection(t *testing.T) {
	stateDir := t.TempDir()
	missingFixture := filepath.Join(t.TempDir(), "not-ready.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("stateDir: "+stateDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, stderr bytes.Buffer
	code := run([]string{"--config", configPath, "resume", "--dry-run", "--fixture", missingFixture, "--timeout", "15ms", "--poll-interval", "2ms"}, &out, &stderr)
	if code != 1 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "resume Niri readiness failed") || !strings.Contains(stderr.String(), "verify NIRI_SOCKET") {
		t.Fatalf("expected actionable readiness error, got %q", stderr.String())
	}
	if strings.Contains(out.String(), "resume_candidate") {
		t.Fatalf("checkpoint selection ran before Niri readiness: %q", out.String())
	}
}

func TestPrintResumePlanGuidesForensicSelectionForNonActionableCandidates(t *testing.T) {
	for _, status := range []resume.CandidateStatus{resume.CandidateEmpty, resume.CandidateStale, resume.CandidateNotFound} {
		t.Run(string(status), func(t *testing.T) {
			var out bytes.Buffer
			printResumePlan(&out, resume.Plan{CandidateStatus: status, Reason: "not actionable"})
			for _, command := range []string{"redeem restore tui", "redeem restore apply --at <RFC3339>"} {
				if !strings.Contains(out.String(), command) {
					t.Fatalf("guidance missing %q: %s", command, out.String())
				}
			}
		})
	}

	var ready bytes.Buffer
	printResumePlan(&ready, resume.Plan{CandidateStatus: resume.CandidateReady})
	if strings.Contains(ready.String(), "resume_guidance") {
		t.Fatalf("ready candidate should not include forensic guidance: %s", ready.String())
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

func TestMirrorSnapshotEndToEndWithFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	err := os.WriteFile(fixturePath, []byte(`{
		"workspaces": [
			{"id": "ws-2", "idx": 2},
			{"id": "ws-1", "idx": 1}
		],
		"windows": [
			{"id": 20, "app_id": "kitty", "title": "second", "workspace_id": "ws-2", "pid": 0, "layout": {"pos_in_scrolling_layout": [1, 1]}},
			{"id": 10, "app_id": "kitty", "title": "first", "workspace_id": "ws-1", "pid": 0, "layout": {"pos_in_scrolling_layout": [1, 1]}}
		]
	}`), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"mirror", "snapshot", "--fixture", fixturePath, "--host", "lattice", "--profile", "default"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d stderr=%q", code, stderr.String())
	}

	got := out.String()
	if !strings.Contains(got, `"host": "lattice"`) {
		t.Fatalf("expected host in mirror output, got %q", got)
	}
	firstIndex := strings.Index(got, `"title": "first"`)
	secondIndex := strings.Index(got, `"title": "second"`)
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("expected ordered mirror output first before second, got %q", got)
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

	configPath := filepath.Join(root, "config.yaml")
	configPayload := []byte("stateDir: " + root + "\nrestore:\n  appAllowlist: {}\n  terminal:\n    command: kitty\n    zellijAttachOrCreate: true\n")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var previewOut bytes.Buffer
	var previewErr bytes.Buffer
	previewCode := run([]string{"--config", configPath, "restore", "apply", "--at", "2026-02-15T10:00:00Z"}, &previewOut, &previewErr)
	if previewCode != 0 {
		t.Fatalf("expected preview code 0, got %d stderr=%q", previewCode, previewErr.String())
	}
	if !strings.Contains(previewOut.String(), "restore_plan ready=0 skipped=1") {
		t.Fatalf("unexpected preview summary: %q", previewOut.String())
	}

	var applyOut bytes.Buffer
	var applyErr bytes.Buffer
	applyCode := run([]string{"--config", configPath, "restore", "apply", "--at", "2026-02-15T10:00:00Z", "--yes"}, &applyOut, &applyErr)
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
	if stderrWithoutWarning(stderr.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", stderr.String())
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
	fixturePath := filepath.Join(root, "niri.json")
	if err := os.WriteFile(fixturePath, []byte(`{"workspaces":[],"windows":[]}`), 0o600); err != nil {
		t.Fatalf("write doctor fixture: %v", err)
	}
	t.Setenv("REDEEM_NIRI_FIXTURE", fixturePath)
	// Set HOME to temp dir so localInstallPath() doesn't find a real ~/.local/bin/redeem.
	t.Setenv("HOME", root)

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--config", configPath, "doctor"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d output=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "doctor_summary total=12 passed=12 failed=0") {
		t.Fatalf("unexpected doctor summary: %q", out.String())
	}
	for _, name := range []string{"boot_id", "state_paths", "niri_readiness", "resume_launcher", "zellij_listing", "resume_policy", "startup_service", "checkpoints_integrity"} {
		if !strings.Contains(out.String(), "doctor_check name="+name+" status=pass") {
			t.Fatalf("doctor output missing passing %s check: %q", name, out.String())
		}
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("doctor must not create the configured state directory: %v", err)
	}
	if stderrWithoutWarning(stderr.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", stderr.String())
	}
}

func TestMirrorOpenDryRunFromSnapshotFile(t *testing.T) {
	root := t.TempDir()
	snapshotPath := filepath.Join(root, "snapshot.json")
	payload := `{"host":"source","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":0,"source_window_id":1,"app_id":"kitty","title":"work","zellij_session":"session-a","terminal":{"cwd":"/tmp/project","zellij_session":"session-a"}}]}`
	if err := os.WriteFile(snapshotPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"mirror", "open", "--snapshot-file", snapshotPath, "--host", "source", "--all", "--dry-run", "--no-clipboard", "--mode", "attach"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, part := range []string{"'kitty'", "'source[0]: work'", "'ssh'", "'attach'", "'session-a'", "'/tmp/project'"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("dry-run missing %q: %s", part, out.String())
		}
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"mirror", "open", "--mode", "watch"}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "unsupported by pinned Zellij") {
		t.Fatalf("watch code=%d stderr=%q", code, stderr.String())
	}
}

func TestMirrorNewDryRunCreatesRemoteSessionWithoutLocalFallback(t *testing.T) {
	original := newMirrorSessionName
	defer func() { newMirrorSessionName = original }()
	newMirrorSessionName = func() (string, error) {
		return "redeem-0123456789abcdef0123456789abcdef", nil
	}

	var out, stderr bytes.Buffer
	code := run([]string{"mirror", "new", "--host", "user@lattice", "--ssh-command", "ssh", "--launcher-command", "kitty", "--app-id", "owned-mirror", "--dry-run", "--no-clipboard"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"kitty", "owned-mirror", "user@lattice", "--create", "redeem-0123456789abcdef0123456789abcdef", "--on-force-close", "detach"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run missing %q: %s", want, out.String())
		}
	}
	for _, forbidden := range []string{"${SHELL", "exec sh", "exec bash"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("dry-run contains local fallback %q: %s", forbidden, out.String())
		}
	}
}

func TestMirrorOpenInteractivePickerIntegrationAndCancellation(t *testing.T) {
	original := chooseMirrorSessions
	defer func() { chooseMirrorSessions = original }()

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	payload := `{"host":"source","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":0,"source_window_id":1,"app_id":"kitty","title":"first","workspace_name":"Dev","zellij_session":"alpha","terminal":{"cwd":"/tmp/a","zellij_session":"alpha"}},{"order":1,"source_window_id":2,"app_id":"kitty","title":"second","workspace_name":"Chat","zellij_session":"beta","terminal":{"cwd":"/tmp/b","zellij_session":"beta"}}]}`
	if err := os.WriteFile(snapshotPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	chooseMirrorSessions = func(windows []mirror.Window) ([]mirror.Window, bool, error) {
		called = true
		if len(windows) != 2 || mirror.SessionName(windows[0]) != "alpha" || windows[0].WorkspaceName != "Dev" {
			t.Fatalf("picker received unexpected discovery: %#v", windows)
		}
		return []mirror.Window{windows[1]}, false, nil
	}
	var out, stderr bytes.Buffer
	code := run([]string{"mirror", "open", "--snapshot-file", snapshotPath, "--dry-run", "--no-clipboard"}, &out, &stderr)
	if code != 0 || !called || !strings.Contains(out.String(), "second") || strings.Contains(out.String(), "first") {
		t.Fatalf("code=%d called=%t out=%q stderr=%q", code, called, out.String(), stderr.String())
	}

	chooseMirrorSessions = func([]mirror.Window) ([]mirror.Window, bool, error) {
		return nil, true, nil
	}
	out.Reset()
	stderr.Reset()
	code = run([]string{"mirror", "open", "--snapshot-file", snapshotPath}, &out, &stderr)
	if code != 0 || out.Len() != 0 || stderrWithoutWarning(stderr.String()) != "" {
		t.Fatalf("cancel code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestMirrorOpenNoninteractiveFlagsBypassPickerAndPreserveOrdering(t *testing.T) {
	original := chooseMirrorSessions
	defer func() { chooseMirrorSessions = original }()
	chooseMirrorSessions = func([]mirror.Window) ([]mirror.Window, bool, error) {
		t.Fatal("noninteractive selection invoked picker")
		return nil, false, nil
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	payload := `{"host":"source","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":0,"source_window_id":1,"app_id":"kitty","title":"first","zellij_session":"alpha","terminal":{"cwd":"/tmp/a","zellij_session":"alpha"}},{"order":1,"source_window_id":2,"app_id":"kitty","title":"second","zellij_session":"beta","terminal":{"cwd":"/tmp/b","zellij_session":"beta"}}]}`
	if err := os.WriteFile(snapshotPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	base := []string{"mirror", "open", "--snapshot-file", snapshotPath, "--dry-run", "--no-clipboard"}
	tests := []struct {
		name      string
		flags     []string
		want      []string
		dontWant  []string
		wantOrder bool
	}{
		{name: "all", flags: []string{"--all"}, want: []string{"first", "second"}, wantOrder: true},
		{name: "repeatable session", flags: []string{"--session", "beta", "--session", "alpha"}, want: []string{"first", "second"}, wantOrder: true},
		{name: "select", flags: []string{"--select", "2"}, want: []string{"second"}, dontWant: []string{"first"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := run(append(append([]string(nil), base...), tc.flags...), &out, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q: %s", want, out.String())
				}
			}
			for _, unwanted := range tc.dontWant {
				if strings.Contains(out.String(), unwanted) {
					t.Fatalf("output unexpectedly contains %q: %s", unwanted, out.String())
				}
			}
			if tc.wantOrder && strings.Index(out.String(), "first") > strings.Index(out.String(), "second") {
				t.Fatalf("launch ordering changed: %s", out.String())
			}
		})
	}
}

func TestMirrorCLIParseAndMalformedSnapshotErrors(t *testing.T) {
	var out bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"mirror", "open", "--mode", "edit"}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "invalid mirror mode") {
		t.Fatalf("invalid mode code=%d stderr=%q", code, stderr.String())
	}

	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"windows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"mirror", "list", "--snapshot-file", path}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "malformed remote mirror snapshot") {
		t.Fatalf("malformed code=%d stderr=%q", code, stderr.String())
	}
}

func TestMirrorCloseDryRunUsesOwnedWindowFilter(t *testing.T) {
	root := t.TempDir()
	niri := filepath.Join(root, "fake-niri")
	script := `#!/bin/sh
printf '%s' '[{"id":11,"app_id":"owned","title":"source[0]: one","workspace_id":2},{"id":12,"app_id":"kitty","title":"other"}]'
`
	if err := os.WriteFile(niri, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"mirror", "close", "--host", "source", "--app-id", "owned", "--niri-command", niri, "--dry-run"}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "would close id=11") || strings.Contains(out.String(), "id=12") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
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

// stderrWithoutWarning strips the local-install warning line from stderr output
// so tests are not affected by whether ~/.local/bin/redeem exists on the runner.
func stderrWithoutWarning(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "warning:") && strings.Contains(line, ".local/bin/redeem") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
