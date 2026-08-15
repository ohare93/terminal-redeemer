package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/replay"
	"github.com/jmo/terminal-redeemer/internal/resume"
	"github.com/jmo/terminal-redeemer/internal/slicecontroller"
	"github.com/jmo/terminal-redeemer/internal/sliceenv"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/slicetui"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
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
	if !strings.Contains(out.String(), "redeem - terminal session history and restore") || !strings.Contains(out.String(), "Continuously project and manage live host terminals") {
		t.Fatalf("expected discoverable slice help output, got %q", out.String())
	}
	if stderrWithoutWarning(errBuf.String()) != "" {
		t.Fatalf("expected empty stderr (ignoring local-install warning), got %q", errBuf.String())
	}
}

func TestSliceManageDispatchAndValidation(t *testing.T) {
	root := t.TempDir()
	original := runSliceManageUI
	defer func() { runSliceManageUI = original }()

	called := false
	runSliceManageUI = func(client slicetui.Client, refresh, timeout time.Duration) error {
		called = true
		socket, ok := client.(slicetui.SocketClient)
		if !ok || !strings.HasSuffix(socket.Path, "/slice/controller/control.sock") || socket.Timeout != 2*time.Second {
			t.Fatalf("client=%#v", client)
		}
		if refresh != 3*time.Second || timeout != 2*time.Second {
			t.Fatalf("refresh=%s timeout=%s", refresh, timeout)
		}
		return nil
	}
	var out, stderr bytes.Buffer
	code := run([]string{"slice", "manage", "--state-dir", root, "--timeout", "2s", "--refresh-interval", "3s"}, &out, &stderr)
	if code != 0 || !called {
		t.Fatalf("code=%d called=%t stderr=%s", code, called, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "slice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("slice manage created controller state directories: %v", err)
	}

	called = false
	stderr.Reset()
	if code = run([]string{"slice", "manage", "--state-dir", root, "--refresh-interval", "0s"}, &out, &stderr); code != 2 || called || !strings.Contains(stderr.String(), "requires positive") {
		t.Fatalf("invalid code=%d called=%t stderr=%s", code, called, stderr.String())
	}

	runSliceManageUI = func(slicetui.Client, time.Duration, time.Duration) error { return errors.New("tui failed") }
	stderr.Reset()
	if code = run([]string{"slice", "manage", "--state-dir", root}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "tui failed") {
		t.Fatalf("failure code=%d stderr=%s", code, stderr.String())
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
		{name: "mirror snapshot", args: []string{"mirror", "snapshot", "--help"}},
		{name: "mirror list", args: []string{"mirror", "list", "--help"}},
		{name: "mirror open", args: []string{"mirror", "open", "--help"}},
		{name: "mirror new", args: []string{"mirror", "new", "--help"}},
		{name: "mirror status", args: []string{"mirror", "status", "--help"}},
		{name: "mirror close", args: []string{"mirror", "close", "--help"}},
		{name: "mirror paste-image", args: []string{"mirror", "paste-image", "--help"}},
		{name: "slice inventory init", args: []string{"slice", "inventory", "init", "--help"}},
		{name: "slice inventory snapshot", args: []string{"slice", "inventory", "snapshot", "--help"}},
		{name: "slice manage", args: []string{"slice", "manage", "--help"}},
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

func TestSliceInventoryInitAndVersionNegotiation(t *testing.T) {
	root := t.TempDir()
	var out, stderr bytes.Buffer
	code := run([]string{"slice", "inventory", "init", "--state-dir", root}, &out, &stderr)
	if code != 0 {
		t.Fatalf("init code=%d stderr=%s", code, stderr.String())
	}
	var initialized struct {
		SchemaVersion uint32 `json:"schema_version"`
		SourceHostID  string `json:"source_host_id"`
		Initialized   bool   `json:"initialized"`
	}
	if err := json.Unmarshal(out.Bytes(), &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.SchemaVersion != sliceprotocol.SchemaVersion || initialized.SourceHostID == "" || !initialized.Initialized {
		t.Fatalf("unexpected init output: %+v", initialized)
	}
	if _, err := os.Stat(filepath.Join(root, "slice", "source-inventory", "current.json")); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	stderr.Reset()
	code = run([]string{"slice", "inventory", "snapshot", "--state-dir", root, "--accept-schema-version", "99"}, &out, &stderr)
	if code != 2 {
		t.Fatalf("negotiation code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), `"unsupported_schema_version"`) || !strings.Contains(out.String(), `"supported_schema_versions"`) {
		t.Fatalf("unexpected negotiation output: %s", out.String())
	}
}

func TestSliceInventoryInitRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	var out, stderr bytes.Buffer
	if code := run([]string{"slice", "inventory", "init", "--state-dir", root}, &out, &stderr); code != 0 {
		t.Fatalf("first init=%d %s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "inventory", "init", "--state-dir", root}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "already initialized") {
		t.Fatalf("second init=%d stderr=%s", code, stderr.String())
	}
}

func TestSliceInventorySnapshotJSONAndErrorContractsHermetic(t *testing.T) {
	original := collectSliceInventorySnapshot
	defer func() { collectSliceInventorySnapshot = original }()
	initRoot := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		var out, stderr bytes.Buffer
		if code := run([]string{"slice", "inventory", "init", "--state-dir", root}, &out, &stderr); code != 0 {
			t.Fatalf("init=%d stderr=%s", code, stderr.String())
		}
		return root
	}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	host := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	t.Run("complete", func(t *testing.T) {
		root := initRoot(t)
		authority := sliceprotocol.Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []sliceprotocol.Source{}, Conflicts: []sliceprotocol.Conflict{}}
		collectSliceInventorySnapshot = func(context.Context, sliceInventorySnapshotOptions) (sliceprotocol.Envelope, error) {
			return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: host, Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: now}, Authoritative: &authority}, nil
		}
		var out, stderr bytes.Buffer
		code := run([]string{"slice", "inventory", "snapshot", "--state-dir", root}, &out, &stderr)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		envelope, err := sliceprotocol.Decode(bytes.NewReader(out.Bytes()))
		if err != nil || envelope.Observation.Quality != sliceprotocol.QualityComplete || envelope.Authoritative.Revision != 1 {
			t.Fatalf("complete JSON=%s err=%v", out.String(), err)
		}
	})
	t.Run("degraded", func(t *testing.T) {
		root := initRoot(t)
		collectSliceInventorySnapshot = func(context.Context, sliceInventorySnapshotOptions) (sliceprotocol.Envelope, error) {
			return sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: host, Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityDegraded, AttemptedAt: now, DegradedReasons: []sliceprotocol.Reason{{Code: sliceprotocol.ReasonZellijCatalogUnavailable}}}}, nil
		}
		var out, stderr bytes.Buffer
		code := run([]string{"slice", "inventory", "snapshot", "--state-dir", root}, &out, &stderr)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		envelope, err := sliceprotocol.Decode(bytes.NewReader(out.Bytes()))
		if err != nil || envelope.Observation.Quality != sliceprotocol.QualityDegraded || envelope.Authoritative != nil {
			t.Fatalf("degraded JSON=%s err=%v", out.String(), err)
		}
	})
	t.Run("collector error", func(t *testing.T) {
		root := initRoot(t)
		collectSliceInventorySnapshot = func(context.Context, sliceInventorySnapshotOptions) (sliceprotocol.Envelope, error) {
			return sliceprotocol.Envelope{}, errors.New("typed authority unavailable")
		}
		var out, stderr bytes.Buffer
		code := run([]string{"slice", "inventory", "snapshot", "--state-dir", root}, &out, &stderr)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "slice inventory snapshot failed") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
		}
	})
	collectSliceInventorySnapshot = original
	t.Run("missing initialization", func(t *testing.T) {
		var out, stderr bytes.Buffer
		code := run([]string{"slice", "inventory", "snapshot", "--state-dir", t.TempDir(), "--niri-socket", ""}, &out, &stderr)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "enrollment marker is missing") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
		}
	})
	t.Run("missing authority after enrollment", func(t *testing.T) {
		root := initRoot(t)
		if err := os.Remove(filepath.Join(root, "slice", "source-inventory", "current.json")); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run([]string{"slice", "inventory", "snapshot", "--state-dir", root, "--niri-socket", ""}, &out, &stderr)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "source inventory state not found") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
		}
	})
	t.Run("corrupt authority", func(t *testing.T) {
		root := initRoot(t)
		if err := os.WriteFile(filepath.Join(root, "slice", "source-inventory", "current.json"), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run([]string{"slice", "inventory", "snapshot", "--state-dir", root, "--niri-socket", ""}, &out, &stderr)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "source inventory state invalid") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
		}
	})
}

func TestSliceInventoryNegotiationPrecedesObservation(t *testing.T) {
	original := collectSliceInventorySnapshot
	defer func() { collectSliceInventorySnapshot = original }()
	called := false
	collectSliceInventorySnapshot = func(context.Context, sliceInventorySnapshotOptions) (sliceprotocol.Envelope, error) {
		called = true
		return sliceprotocol.Envelope{}, errors.New("must not run")
	}
	var out, stderr bytes.Buffer
	code := run([]string{"slice", "inventory", "snapshot", "--accept-schema-version", "99"}, &out, &stderr)
	if code != 2 || called || !strings.Contains(out.String(), "unsupported_schema_version") {
		t.Fatalf("code=%d called=%v out=%s stderr=%s", code, called, out.String(), stderr.String())
	}
}

type neverEOFReadCloser struct {
	once   sync.Once
	closed chan struct{}
}

func (reader *neverEOFReadCloser) Read([]byte) (int, error) { <-reader.closed; return 0, os.ErrClosed }
func (reader *neverEOFReadCloser) Close() error {
	reader.once.Do(func() { close(reader.closed) })
	return nil
}
func TestSliceRPCRequestTimeoutIncludesStdinIngestion(t *testing.T) {
	original := sliceRPCInput
	defer func() { sliceRPCInput = original }()
	sliceRPCInput = &neverEOFReadCloser{closed: make(chan struct{})}
	var out, stderr bytes.Buffer
	started := time.Now()
	code := run([]string{"slice", "rpc", "--state-dir", t.TempDir(), "--timeout", "10ms"}, &out, &stderr)
	if code != 2 || time.Since(started) > time.Second || !strings.Contains(out.String(), "invalid_request") {
		t.Fatalf("code=%d elapsed=%s out=%s stderr=%s", code, time.Since(started), out.String(), stderr.String())
	}
}

func TestSliceRPCTypedInvalidAndTokenQuery(t *testing.T) {
	originalInput := sliceRPCInput
	defer func() { sliceRPCInput = originalInput }()
	root := t.TempDir()
	tokens, err := slicerpc.NewTokenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := tokens.CreatePending("host", "cli-token", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record.Status = slicerpc.TokenLaunched
	if err := tokens.Update(record); err != nil {
		t.Fatal(err)
	}
	sliceRPCInput = io.NopCloser(strings.NewReader(`{"schema_version":1,"accept_schema_versions":[1],"request_id":"cli-1","verb":"token_query","payload":{"token":"cli-token"}}`))
	var out, stderr bytes.Buffer
	code := run([]string{"slice", "rpc", "--state-dir", root}, &out, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var response slicerpc.Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Outcome.Status != slicerpc.StatusOK {
		t.Fatalf("response=%s err=%v", out.String(), err)
	}
	sliceRPCInput = io.NopCloser(strings.NewReader(`{"schema_version":99,"request_id":"cli-2","verb":"liveness"}`))
	out.Reset()
	stderr.Reset()
	code = run([]string{"slice", "rpc", "--state-dir", root}, &out, &stderr)
	if code != 2 || !strings.Contains(out.String(), "invalid_request") || !strings.Contains(out.String(), "supported_schema_versions") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
}

func ptrUint64(value uint64) *uint64 { return &value }
func TestSliceControllerCLIInitControlAndDisabledDefault(t *testing.T) {
	root := t.TempDir()
	var out, stderr bytes.Buffer
	if code := run([]string{"slice", "controller", "run", "--state-dir", root}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "opt-in") {
		t.Fatalf("disabled code=%d stderr=%s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "init", "--state-dir", root, "--host-id", "host-a", "--leech-id", "leech-a"}, &out, &stderr); code != 0 {
		t.Fatalf("init code=%d stderr=%s", code, stderr.String())
	}
	store, err := slicecontroller.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Read()
	if err != nil || state.Namespace.Host != "host-a" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	engine := &slicecontroller.Engine{Store: store, Config: slicecontroller.ControllerConfig{RetryWindow: time.Second, RetryInitialBackoff: time.Millisecond, RetryMaxBackoff: time.Millisecond, RetryMaxAttempts: 1, SourceGoneGrace: time.Second, SourceGoneConfirmations: 2}}
	const sourceID = "src_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const sessionID = "ses_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC()
	source := sliceprotocol.Source{SourceID: sourceID, RuntimeWindowID: 42, Session: sliceprotocol.Session{ID: sessionID, Name: "session-a", Status: "active"}, Workspace: sliceprotocol.Workspace{RuntimeID: 1, Name: "work", Key: "work"}, Output: &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"}, Layout: sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: 1, Tile: 1}, TileWidth: 960, TileHeight: 540, WindowWidth: 960, WindowHeight: 540}}
	envelope := sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: now}, Authoritative: &sliceprotocol.Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{sessionID}, Sources: []sliceprotocol.Source{source}, Conflicts: []sliceprotocol.Conflict{}}}
	if _, _, _, err := engine.ApplyEnvelope(envelope, now); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- slicecontroller.ServeControl(ctx, store.SocketPath(), time.Second, slicecontroller.ControlHandler{Engine: engine})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(store.SocketPath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket not ready")
		}
		time.Sleep(time.Millisecond)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "status", "--state-dir", root}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "controller_id") {
		t.Fatalf("status code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "all-enable", "--state-dir", root}, &out, &stderr); code != 0 {
		t.Fatalf("all-enable code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	var controlResponse slicecontroller.ControlResponse
	if err := json.Unmarshal(out.Bytes(), &controlResponse); err != nil || controlResponse.State == nil || !controlResponse.State.AllEligible {
		t.Fatalf("all-enable response=%s err=%v", out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "all-disable", "--state-dir", root}, &out, &stderr); code != 0 {
		t.Fatalf("all-disable code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	controlResponse = slicecontroller.ControlResponse{}
	if err := json.Unmarshal(out.Bytes(), &controlResponse); err != nil || controlResponse.State == nil || controlResponse.State.AllEligible {
		t.Fatalf("all-disable response=%s err=%v", out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "pickup", "--source-id", sourceID, "--state-dir", root}, &out, &stderr); code != 0 {
		t.Fatalf("pickup code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	controlResponse = slicecontroller.ControlResponse{}
	if err := json.Unmarshal(out.Bytes(), &controlResponse); err != nil || controlResponse.State == nil || !controlResponse.State.Pickups[sourceID] {
		t.Fatalf("pickup response=%s err=%v", out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "pickup-remove", "--source-id", sourceID, "--state-dir", root}, &out, &stderr); code != 0 {
		t.Fatalf("pickup-remove code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	controlResponse = slicecontroller.ControlResponse{}
	if err := json.Unmarshal(out.Bytes(), &controlResponse); err != nil || controlResponse.State == nil || controlResponse.State.Pickups[sourceID] {
		t.Fatalf("pickup-remove response=%s err=%v", out.String(), err)
	}
	cancel()
	<-done
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "controller", "init", "--state-dir", root, "--host-id", "host-a", "--leech-id", "leech-a"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "already initialized") {
		t.Fatalf("reinit code=%d stderr=%s", code, stderr.String())
	}
}

func TestControllerSpatialSkipsUnnamedSourcesWithoutStateChurn(t *testing.T) {
	root := t.TempDir()
	store, err := slicecontroller.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Initialize(slicecontroller.Namespace{Host: "host", Leech: "leech"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	engine := &slicecontroller.Engine{Store: store, Config: slicecontroller.ControllerConfig{RetryWindow: time.Second, RetryInitialBackoff: time.Millisecond, RetryMaxBackoff: time.Millisecond, RetryMaxAttempts: 1}, Now: func() time.Time { return now }}
	const sourceID = "src_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const sessionID = "ses_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source := sliceprotocol.Source{SourceID: sourceID, RuntimeWindowID: 42, Session: sliceprotocol.Session{ID: sessionID, Name: "session-a", Status: "active"}, Workspace: sliceprotocol.Workspace{RuntimeID: 1}, Output: &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"}, Layout: sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: 1, Tile: 1}, TileWidth: 960, TileHeight: 540, WindowWidth: 960, WindowHeight: 540}}
	envelope := sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: now}, Authoritative: &sliceprotocol.Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{sessionID}, Sources: []sliceprotocol.Source{source}, Conflicts: []sliceprotocol.Conflict{}}}
	if _, _, _, err = engine.ApplyEnvelope(envelope, now); err != nil {
		t.Fatal(err)
	}
	state, _, err := engine.SelectAll(true)
	if err != nil {
		t.Fatal(err)
	}
	projection := state.Projections[sourceID]
	workspaceName, outputName := "local", "DP-1"
	workspaceID := uint64(7)
	local := niriipc.State{Outputs: map[string]niriipc.Output{outputName: {Name: outputName, Logical: niriipc.Logical{Width: 1920, Height: 1080, Scale: 1, Transform: "normal"}}}, Workspaces: []niriipc.Workspace{{ID: workspaceID, Name: &workspaceName, Output: &outputName}}, Windows: []niriipc.Window{{ID: 9, AppID: projection.AppID, PID: 123, WorkspaceID: &workspaceID, Layout: niriipc.Layout{Position: []int{1, 1}, WindowSize: []int{960, 540}}}}}
	beforeGeneration := state.Generation
	executions := 0
	planControllerSpatial(context.Background(), engine, config.Defaults(), local, "leech-epoch", []slicecontroller.OwnedWindow{{SourceID: sourceID, WindowID: 9, PID: 123, AppID: projection.AppID}}, func(context.Context, []slicecontroller.Effect) error {
		executions++
		return nil
	})
	state, err = engine.Status()
	if err != nil || state.Generation != beforeGeneration || executions != 0 {
		t.Fatalf("unnamed spatial churn generation=%d want=%d executions=%d err=%v", state.Generation, beforeGeneration, executions, err)
	}
	if _, ok := state.Spatial[sourceID]; ok {
		t.Fatalf("unnamed source retained spatial authority: %+v", state.Spatial[sourceID])
	}
	selected, err := (launchSelection{stateDir: root}).Selected("work")
	if err != nil || selected {
		t.Fatalf("all-eligible changed routed launch selection: selected=%t err=%v", selected, err)
	}
}

type closeEffectProcessReader struct {
	exe  string
	argv []string
	err  error
}

func (r closeEffectProcessReader) Exe(int) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.exe, nil
}
func (r closeEffectProcessReader) Cmdline(int) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]string(nil), r.argv...), nil
}

func closeEffectNiriServer(t *testing.T, windows []niriipc.Window) (string, func() []string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "tr-close-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var mu sync.Mutex
	requests := []string{}
	eventSnapshots := 0
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			line, readErr := bufio.NewReader(conn).ReadString('\n')
			if readErr != nil {
				_ = conn.Close()
				continue
			}
			mu.Lock()
			requests = append(requests, strings.TrimSpace(line))
			mu.Unlock()
			switch {
			case strings.Contains(line, "EventStream"):
				eventSnapshots++
				current := windows
				if eventSnapshots > 1 {
					current = nil
				}
				windowJSON, _ := json.Marshal(current)
				_, _ = fmt.Fprintf(conn, "{\"Ok\":\"Handled\"}\n{\"WorkspacesChanged\":{\"workspaces\":[{\"id\":1,\"idx\":1,\"name\":\"work\",\"output\":\"DP-1\",\"is_active\":true}]}}\n{\"WindowsChanged\":{\"windows\":%s}}\n{\"ConfigLoaded\":{\"failed\":false}}\n", windowJSON)
			case strings.Contains(line, "Outputs"):
				_, _ = io.WriteString(conn, "{\"Ok\":{\"Outputs\":{\"DP-1\":{\"name\":\"DP-1\",\"logical\":{\"x\":0,\"y\":0,\"width\":1920,\"height\":1080,\"scale\":1,\"transform\":\"Normal\"}}}}}\n")
			case strings.Contains(line, "CloseWindow"):
				_, _ = io.WriteString(conn, "{\"Ok\":\"Handled\"}\n")
			default:
				_, _ = io.WriteString(conn, "{\"Err\":\"unexpected\"}\n")
			}
			_ = conn.Close()
		}
	}()
	return path, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func closeEffectHarness(t *testing.T) (*slicecontroller.Engine, string, []string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "tr-close-store-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	store, err := slicecontroller.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Initialize(slicecontroller.Namespace{Host: "host", Leech: "leech"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	engine := &slicecontroller.Engine{Store: store, Config: slicecontroller.ControllerConfig{RetryWindow: time.Minute, RetryInitialBackoff: time.Second, RetryMaxBackoff: time.Second, RetryMaxAttempts: 2, SourceGoneGrace: time.Second, SourceGoneConfirmations: 2}, Now: func() time.Time { return now }}
	const sourceID = "src_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const sessionID = "ses_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source := sliceprotocol.Source{SourceID: sourceID, RuntimeWindowID: 42, Session: sliceprotocol.Session{ID: sessionID, Name: "session-a", Status: "active"}, Workspace: sliceprotocol.Workspace{RuntimeID: 1, Name: "work", Key: "work"}, Output: &sliceprotocol.Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"}, Layout: sliceprotocol.Layout{Mode: "tiled", Position: &sliceprotocol.Position{Column: 1, Tile: 1}, TileWidth: 960, TileHeight: 540, WindowWidth: 960, WindowHeight: 540}}
	envelope := sliceprotocol.Envelope{SchemaVersion: sliceprotocol.SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: sliceprotocol.Observation{Quality: sliceprotocol.QualityComplete, AttemptedAt: now}, Authoritative: &sliceprotocol.Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, LiveSessionIDs: []string{sessionID}, Sources: []sliceprotocol.Source{source}, Conflicts: []sliceprotocol.Conflict{}}}
	if _, _, _, err = engine.ApplyEnvelope(envelope, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = engine.SelectWorkspace("work", true); err != nil {
		t.Fatal(err)
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	mapping := state.Projections[sourceID]
	executable := "/nix/store/test-kitty/bin/kitty"
	argv := []string{executable, "--class", mapping.AppID, "-e", "redeem", "slice", "projection-run", "--source-id", sourceID, "--session", mapping.ExpectedSessionName, "--token", mapping.AttachToken}
	if _, err = engine.PrepareProjection(sourceID, executable, argv); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.RecordLaunch(sourceID, 123, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = engine.ObserveLocal("leech-epoch", []slicecontroller.OwnedWindow{{SourceID: sourceID, WindowID: 9, PID: 123, AppID: mapping.AppID}}); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.AttachmentConnected(sourceID); err != nil {
		t.Fatal(err)
	}
	return engine, executable, argv
}

func TestExecuteSliceControllerCloseDropEffectsRequireExactOwnershipAndStayLocal(t *testing.T) {
	const sourceID = "src_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name       string
		kind       string
		windows    func(string) []niriipc.Window
		process    func(string, []string) closeEffectProcessReader
		wantAction bool
		wantErr    bool
		invokeDrop bool
	}{
		{name: "exact owned close", kind: "close", windows: func(app string) []niriipc.Window {
			return []niriipc.Window{{ID: 9, AppID: app, PID: 123, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}}
		}, process: func(exe string, argv []string) closeEffectProcessReader {
			return closeEffectProcessReader{exe: exe, argv: argv}
		}, wantAction: true},
		{name: "exact owned drop", kind: "drop", windows: func(app string) []niriipc.Window {
			return []niriipc.Window{{ID: 9, AppID: app, PID: 123, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}}
		}, process: func(exe string, argv []string) closeEffectProcessReader {
			return closeEffectProcessReader{exe: exe, argv: argv}
		}, wantAction: true, invokeDrop: true},
		{name: "hostile argv", kind: "close", windows: func(app string) []niriipc.Window {
			return []niriipc.Window{{ID: 9, AppID: app, PID: 123, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}}
		}, process: func(exe string, argv []string) closeEffectProcessReader {
			return closeEffectProcessReader{exe: exe, argv: append(argv, "--terminate-host-session")}
		}, wantErr: true},
		{name: "ambiguous windows", kind: "close", windows: func(app string) []niriipc.Window {
			return []niriipc.Window{{ID: 9, AppID: app, PID: 123, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}, {ID: 10, AppID: app, PID: 123, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{2, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}}
		}, process: func(exe string, argv []string) closeEffectProcessReader {
			return closeEffectProcessReader{exe: exe, argv: argv}
		}, wantErr: true},
		{name: "unowned app", kind: "close", windows: func(string) []niriipc.Window {
			return []niriipc.Window{{ID: 9, AppID: "unrelated", PID: 123, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}}
		}, process: func(exe string, argv []string) closeEffectProcessReader {
			return closeEffectProcessReader{exe: exe, argv: argv}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, executable, argv := closeEffectHarness(t)
			state, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			appID := state.Projections[sourceID].AppID
			socket, requests := closeEffectNiriServer(t, tc.windows(appID))
			sentinel := filepath.Join(t.TempDir(), "remote-command-used")
			command := filepath.Join(t.TempDir(), "forbidden-transport-rpc-zellij")
			if err := os.WriteFile(command, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 99\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults()
			cfg.Slice.TransportCommand = command
			cfg.Slice.ZellijCommand = command
			cfg.Slice.RPCCommand = []string{command, "slice", "rpc"}
			resolver := sliceenv.Resolver{Keys: []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"}, Environment: []string{"NIRI_SOCKET=" + socket, "WAYLAND_DISPLAY=wayland-test", "XDG_RUNTIME_DIR=/tmp"}}
			processes := tc.process(executable, argv)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var executeErr error
			if tc.invokeDrop {
				payload, _ := json.Marshal(slicecontroller.SourcePayload{SourceID: sourceID})
				response := (slicecontroller.ControlHandler{Engine: engine, Execute: func(ctx context.Context, effects []slicecontroller.Effect) error {
					return executeSliceControllerEffectsWithProcesses(ctx, engine, cfg, resolver, effects, processes)
				}}).Handle(ctx, slicecontroller.ControlRequest{SchemaVersion: slicecontroller.SchemaVersion, RequestID: "drop-1", Verb: slicecontroller.VerbDrop, Payload: payload})
				if response.Outcome.Status != "ok" {
					executeErr = errors.New(response.Outcome.Code)
				}
			} else {
				_, effects, closeErr := engine.Close(sourceID)
				if closeErr != nil || len(effects) != 1 || effects[0].Kind != slicecontroller.EffectCloseProjection {
					t.Fatalf("close effects=%+v err=%v", effects, closeErr)
				}
				executeErr = executeSliceControllerEffectsWithProcesses(ctx, engine, cfg, resolver, effects, processes)
			}
			if (executeErr != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", executeErr, tc.wantErr)
			}
			all := requests()
			actions := []string{}
			for _, request := range all {
				if strings.Contains(request, "Action") {
					actions = append(actions, request)
				}
				for _, forbidden := range []string{"zellij", "ssh", "rpc", "terminate", "kill", "quit"} {
					if strings.Contains(strings.ToLower(request), forbidden) {
						t.Fatalf("close/drop emitted forbidden remote/session verb %q: %s", forbidden, request)
					}
				}
			}
			if tc.wantAction {
				if len(actions) != 1 || actions[0] != `{"Action":{"CloseWindow":{"id":9}}}` {
					t.Fatalf("exact owned close actions=%v requests=%v", actions, all)
				}
			} else if len(actions) != 0 {
				t.Fatalf("unproven close emitted action: %v", actions)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("close/drop invoked transport/RPC/Zellij command: %v", err)
			}
		})
	}
}

func TestFocusedCloseControlSocketReprovesFocusBeforeMutation(t *testing.T) {
	const sourceID = "src_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name          string
		focusedAtExec bool
		injectFailure bool
	}{
		{name: "focus changed", focusedAtExec: false},
		{name: "non-focus effect failure", focusedAtExec: true, injectFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, executable, argv := closeEffectHarness(t)
			initialState, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			initialSource := initialState.Sources[sourceID]
			initialMapping := initialState.Projections[sourceID]
			initialUndo := append([]slicecontroller.UndoAction(nil), initialState.Undo...)
			processes := closeEffectProcessReader{exe: executable, argv: argv}
			initialWindows := []niriipc.Window{{ID: 9, AppID: initialMapping.AppID, PID: 123, IsFocused: true, WorkspaceID: ptrUint64(1), Layout: niriipc.Layout{Position: []int{1, 1}, TileSize: []float64{960, 540}, WindowSize: []int{960, 540}}}}
			initial := slicecontroller.VerifyOwnedWindows(initialState, niriipc.State{Windows: initialWindows}, processes)
			if len(initial) != 1 || !initial[0].Focused {
				t.Fatalf("initial focused ownership proof=%+v", initial)
			}

			// Model the destructive boundary after close-focused's initial proof.
			effectWindows := append([]niriipc.Window(nil), initialWindows...)
			effectWindows[0].IsFocused = tc.focusedAtExec
			niriSocket, requests := closeEffectNiriServer(t, effectWindows)
			cfg := config.Defaults()
			resolver := sliceenv.Resolver{Keys: []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"}, Environment: []string{"NIRI_SOCKET=" + niriSocket, "WAYLAND_DISPLAY=wayland-test", "XDG_RUNTIME_DIR=/tmp"}}
			focusEffect := make(chan bool, 1)
			rawExecute := func(ctx context.Context, effects []slicecontroller.Effect) error {
				for _, effect := range effects {
					if effect.Kind == slicecontroller.EffectCloseProjection {
						focusEffect <- effect.FocusRequired
					}
				}
				if tc.injectFailure {
					if _, err := engine.RecordObservationFailure("injected_effect_failure"); err != nil {
						return err
					}
					return errors.New("injected close effect failure")
				}
				return executeSliceControllerEffectsWithProcesses(ctx, engine, cfg, resolver, effects, processes)
			}
			operationMu := &sync.Mutex{}
			handler := slicecontroller.ControlHandler{Engine: engine, Serialize: operationMu, Execute: func(ctx context.Context, effects []slicecontroller.Effect) error {
				return engine.ExecuteEffects(ctx, effects, rawExecute)
			}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- slicecontroller.ServeControl(ctx, engine.Store.SocketPath(), time.Second, handler) }()
			deadline := time.Now().Add(time.Second)
			for {
				if _, statErr := os.Stat(engine.Store.SocketPath()); statErr == nil {
					break
				}
				select {
				case serveErr := <-done:
					cancel()
					t.Fatalf("control socket failed before ready: %v", serveErr)
				default:
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatalf("control socket not ready: %s", engine.Store.SocketPath())
				}
				time.Sleep(time.Millisecond)
			}

			response, callErr := slicecontroller.CallControl(context.Background(), engine.Store.SocketPath(), time.Second, slicecontroller.NewControlRequest(slicecontroller.VerbClose, slicecontroller.ClosePayload{SourceID: sourceID, FocusRequired: true}))
			cancel()
			if serveErr := <-done; serveErr != nil {
				t.Fatal(serveErr)
			}
			if callErr != nil || response.Outcome.Code != "effect_failed" {
				t.Fatalf("focused close response=%+v err=%v", response, callErr)
			}
			select {
			case required := <-focusEffect:
				if !required {
					t.Fatal("focused close marker was lost before effect execution")
				}
			default:
				t.Fatal("focused close effect was not executed")
			}

			rolledBack, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			if len(rolledBack.ClosedByUser) != 0 || rolledBack.Sources[sourceID].Connection != initialSource.Connection || rolledBack.Projections[sourceID].Status != initialMapping.Status || rolledBack.Projections[sourceID].NiriWindowID != initialMapping.NiriWindowID || len(rolledBack.Undo) != len(initialUndo) {
				t.Fatalf("failed focused close retained intent: closed=%+v source=%+v projection=%+v undo=%+v", rolledBack.ClosedByUser, rolledBack.Sources[sourceID], rolledBack.Projections[sourceID], rolledBack.Undo)
			}
			for _, undo := range rolledBack.Undo {
				if undo.Kind == "close" && undo.SourceID == sourceID {
					t.Fatalf("failed focused close retained undo intent: %+v", undo)
				}
			}
			owned := []slicecontroller.OwnedWindow{{SourceID: sourceID, WindowID: 9, PID: 123, AppID: initialMapping.AppID, Focused: tc.focusedAtExec}}
			if _, effects, observeErr := engine.ObserveLocal("leech-epoch", owned); observeErr != nil || len(effects) != 0 {
				t.Fatalf("post-rollback local observation effects=%+v err=%v", effects, observeErr)
			}
			if _, effects, tickErr := engine.Tick(); tickErr != nil || len(effects) != 0 {
				t.Fatalf("post-rollback reconciliation effects=%+v err=%v", effects, tickErr)
			}
			for _, request := range requests() {
				if strings.Contains(request, "CloseWindow") {
					t.Fatalf("failed focused close emitted later close mutation: %s", request)
				}
			}
		})
	}
}

func TestCurrentStaticWorkspaceRejectsUnnamedDuplicateCollisionAndAmbiguity(t *testing.T) {
	out := "DP-1"
	work := "Work"
	lower := "work"
	base := niriipc.State{Workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Name: &work, Output: &out, IsFocused: true}}}
	if got, err := currentStaticWorkspace(base); err != nil || got != "Work" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	cases := []niriipc.State{{Workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Output: &out, IsFocused: true}}}, {Workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Name: &work, Output: &out, IsFocused: true}, {ID: 2, Index: 2, Name: &work, Output: &out}}}, {Workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Name: &work, Output: &out, IsFocused: true}, {ID: 2, Index: 2, Name: &lower, Output: &out}}}, {Workspaces: []niriipc.Workspace{{ID: 1, Index: 1, Name: &work, Output: &out, IsFocused: true}, {ID: 2, Index: 2, Name: &lower, Output: &out, IsFocused: true}}}}
	for i, state := range cases {
		if _, err := currentStaticWorkspace(state); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestSliceModeCLIIsDisabledByDefaultAndInspectable(t *testing.T) {
	root := t.TempDir()
	var out, stderr bytes.Buffer
	if code := run([]string{"slice", "mode", "status", "--state-dir", root}, &out, &stderr); code != 0 || !strings.Contains(out.String(), `"enabled":false`) {
		t.Fatalf("status code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "mode", "enable", "--state-dir", root}, &out, &stderr); code != 0 || !strings.Contains(out.String(), `"enabled":true`) {
		t.Fatalf("enable code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"slice", "mode", "disable", "--state-dir", root}, &out, &stderr); code != 0 || !strings.Contains(out.String(), `"enabled":false`) {
		t.Fatalf("disable code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
}

func TestSliceAttachCLIExactExit(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "z")
	version := filepath.Join(base, zellijlive.SocketContractDir)
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(version, "exact"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	command := filepath.Join(root, "zellij")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij 0.44.3'; exit 0; fi\nsleep 0.3\nexit 0\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run([]string{"slice", "attach", "--session", "exact", "--token", "cli", "--real-socket-dir", base, "--private-root", filepath.Join(base, "private"), "--shim-cache", filepath.Join(base, "cache"), "--zellij-command", command}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), `"status":"detached"`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
}
