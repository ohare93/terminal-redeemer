package mirror

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type fakeReader map[int]procmeta.ProcessInfo

func (r fakeReader) Inspect(pid int) (procmeta.ProcessInfo, error) {
	return r[pid], nil
}

type fakeVerifier map[string]bool

func (v fakeVerifier) Exists(session string) (bool, error) {
	return v[session], nil
}

type fakeResolver map[string]string

func (r fakeResolver) Resolve(session string) (string, error) {
	return r[session], nil
}

type fakeLister []string

func (l fakeLister) List(context.Context) ([]string, error) {
	return append([]string(nil), l...), nil
}

type fakeCataloger struct {
	catalog zellijlive.Catalog
}

func (c fakeCataloger) Observe(context.Context) (zellijlive.Catalog, error) {
	return c.catalog, nil
}

func TestCaptureOrdersWindowsAndIncludesZellijSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	payload := []byte(`{
		"workspaces": [
			{"id": "ws-2", "idx": 2, "output": "DP-1"},
			{"id": "ws-1", "idx": 1, "output": "DP-1"}
		],
		"windows": [
			{"id": 30, "app_id": "kitty", "title": "third", "workspace_id": "ws-2", "pid": 300, "layout": {"pos_in_scrolling_layout": [1, 1], "tile_size": [100, 50], "window_size": [98, 48]}},
			{"id": 20, "app_id": "kitty", "title": "second", "workspace_id": "ws-1", "pid": 200, "layout": {"pos_in_scrolling_layout": [2, 1]}},
			{"id": 10, "app_id": "kitty", "title": "first", "workspace_id": "ws-1", "pid": 100, "layout": {"pos_in_scrolling_layout": [1, 1]}},
			{"id": 40, "app_id": "firefox", "title": "browser", "workspace_id": "ws-1", "pid": 400, "layout": {"pos_in_scrolling_layout": [3, 1]}}
		]
	}`)
	if err := os.WriteFile(fixturePath, payload, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	snapshot, err := Capture(context.Background(), Options{
		Host:        "lattice",
		Profile:     "default",
		FixturePath: fixturePath,
		GeneratedAt: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		Reader: fakeReader{
			100: {CWD: "/home/jmo/one", Env: map[string]string{"ZELLIJ_SESSION_NAME": "one"}},
			200: {CWD: "/home/jmo/two", Env: map[string]string{"ZELLIJ_SESSION_NAME": "two"}},
			300: {CWD: "/home/jmo/three", Env: map[string]string{"ZELLIJ_SESSION_NAME": "three"}},
		},
		ProcessMetadata: procmeta.Config{IncludeSessionTag: true},
	})
	if err != nil {
		t.Fatalf("capture mirror snapshot: %v", err)
	}

	if snapshot.Host != "lattice" {
		t.Fatalf("expected host lattice, got %q", snapshot.Host)
	}
	if len(snapshot.Windows) != 4 {
		t.Fatalf("expected 4 windows, got %d", len(snapshot.Windows))
	}

	wantTitles := []string{"first", "second", "browser", "third"}
	for i, want := range wantTitles {
		if got := snapshot.Windows[i].Title; got != want {
			t.Fatalf("window %d title: got %q want %q", i, got, want)
		}
		if snapshot.Windows[i].Order != i {
			t.Fatalf("window %d order: got %d", i, snapshot.Windows[i].Order)
		}
	}

	first := snapshot.Windows[0]
	if first.ZellijSession != "one" {
		t.Fatalf("expected top-level zellij session one, got %q", first.ZellijSession)
	}
	if first.Terminal == nil || first.Terminal.CWD != "/home/jmo/one" || first.Terminal.ZellijSession != "one" {
		t.Fatalf("unexpected terminal metadata: %#v", first.Terminal)
	}
	if len(first.Terminal.Project) != 1 || first.Terminal.Project[0].Label != "one" {
		t.Fatalf("visible project identity = %#v", first.Terminal.Project)
	}
	if snapshot.Windows[2].Terminal != nil {
		t.Fatalf("expected non-terminal browser to omit terminal metadata: %#v", snapshot.Windows[2].Terminal)
	}
}

func TestCaptureExtractsVerifiedSessionFromTitle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixturePath := filepath.Join(root, "niri.json")
	payload := []byte(`{
		"workspaces": [{"id": "ws-1", "idx": 1}],
		"windows": [{"id": 10, "app_id": "kitty", "title": "title-session | π - work", "workspace_id": "ws-1", "pid": 100}]
	}`)
	if err := os.WriteFile(fixturePath, payload, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	snapshot, err := Capture(context.Background(), Options{
		FixturePath: fixturePath,
		Reader:      fakeReader{100: {CWD: "/home/jmo", Env: map[string]string{}}},
		Verifier:    fakeVerifier{"title-session": true},
		Resolver:    fakeResolver{"title-session": "/home/jmo/project"},
		ProcessMetadata: procmeta.Config{
			IncludeSessionTag: true,
		},
	})
	if err != nil {
		t.Fatalf("capture mirror snapshot: %v", err)
	}

	if len(snapshot.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(snapshot.Windows))
	}
	window := snapshot.Windows[0]
	if window.ZellijSession != "title-session" {
		t.Fatalf("expected title-derived session, got %q", window.ZellijSession)
	}
	if window.Terminal == nil || window.Terminal.CWD != "/home/jmo/project" {
		t.Fatalf("expected resolver-upgraded cwd, got %#v", window.Terminal)
	}
	if len(window.Terminal.Project) != 1 || window.Terminal.Project[0].Label != "project" {
		t.Fatalf("project identity did not follow resolver-upgraded cwd: %#v", window.Terminal.Project)
	}
}

func TestLiveSessionListerExcludesCacheOnlyDeadSessions(t *testing.T) {
	t.Parallel()

	lister := liveSessionLister{cataloger: fakeCataloger{catalog: zellijlive.Catalog{
		Names: []string{"active", "cache-only"},
		Sessions: map[string]zellijlive.Session{
			"active":     {Name: "active", Status: zellijlive.StatusActive},
			"cache-only": {Name: "cache-only", Status: zellijlive.StatusDeadResurrectable},
		},
	}}}

	sessions, err := lister.List(context.Background())
	if err != nil {
		t.Fatalf("list live sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != "active" {
		t.Fatalf("live sessions = %q, want only active", sessions)
	}
}

func TestCaptureAddsHeadlessSessionsWithoutExactDuplicates(t *testing.T) {
	t.Parallel()

	fixturePath := filepath.Join(t.TempDir(), "niri.json")
	payload := []byte(`{
		"workspaces": [{"id": "ws-1", "idx": 1, "name": "Dev"}],
		"windows": [{"id": 10, "app_id": "kitty", "title": "visible", "workspace_id": "ws-1", "pid": 100}]
	}`)
	if err := os.WriteFile(fixturePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := Capture(context.Background(), Options{
		Host: "lattice", FixturePath: fixturePath,
		Reader:          fakeReader{100: {CWD: "/visible", Env: map[string]string{"ZELLIJ_SESSION_NAME": "visible"}}},
		Resolver:        fakeResolver{"visible": "/visible", "detached": "/headless/project"},
		Lister:          fakeLister{"detached", "visible", "detached"},
		ProcessMetadata: procmeta.Config{IncludeSessionTag: true},
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	windows := Discover(snapshot)
	if len(windows) != 2 || SessionName(windows[0]) != "visible" || SessionName(windows[1]) != "detached" {
		t.Fatalf("discovery = %#v", windows)
	}
	headless := windows[1]
	if !headless.Headless || headless.SourceWindowID != 0 || headless.AppID != "zellij" || headless.Terminal == nil || headless.Terminal.CWD != "/headless/project" {
		t.Fatalf("headless metadata = %#v", headless)
	}
	if len(headless.Terminal.Project) != 1 || headless.Terminal.Project[0].Label != "project" {
		t.Fatalf("headless project identity = %#v", headless.Terminal.Project)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSnapshot(encoded)
	if err != nil || len(Discover(decoded)) != 2 {
		t.Fatalf("decode headless snapshot: %#v err=%v", decoded, err)
	}
}

func TestLegacySnapshotFixtureRemainsUnversionedAndSeparate(t *testing.T) {
	payload, err := os.ReadFile("testdata/legacy-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeSnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Host != "legacy-host" || len(Discover(snapshot)) != 1 {
		t.Fatalf("unexpected legacy fixture: %+v", snapshot)
	}
	if terminal := Discover(snapshot)[0].Terminal; terminal == nil || len(terminal.Project) != 0 {
		t.Fatalf("legacy snapshot unexpectedly requires project metadata: %#v", terminal)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(payload, &shape); err != nil {
		t.Fatal(err)
	}
	if _, found := shape["schema_version"]; found {
		t.Fatal("legacy payload acquired schema_version")
	}
	if _, found := shape["observation"]; found {
		t.Fatal("legacy payload acquired authoritative observation")
	}
}
