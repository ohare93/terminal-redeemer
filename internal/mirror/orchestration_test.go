package mirror

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	outputs     []outputResult
	outputCalls []Command
	runCalls    []Command
	runErr      error
	onRun       func(Command)
}

type outputResult struct {
	data []byte
	err  error
}

func (runner *recordingRunner) Output(_ context.Context, command Command) ([]byte, error) {
	runner.outputCalls = append(runner.outputCalls, command)
	if len(runner.outputs) == 0 {
		return nil, errors.New("unexpected output call")
	}
	result := runner.outputs[0]
	runner.outputs = runner.outputs[1:]
	return result.data, result.err
}

func (runner *recordingRunner) Run(_ context.Context, command Command) error {
	runner.runCalls = append(runner.runCalls, command)
	if runner.onRun != nil {
		runner.onRun(command)
	}
	return runner.runErr
}

func validRemoteSnapshot() []byte {
	return []byte(`{"host":"source-node","profile":"default","generated_at":"2026-07-10T12:00:00Z","windows":[{"order":2,"source_window_id":9,"app_id":"kitty","title":"work","zellij_session":"session-a","terminal":{"cwd":"/tmp/work","zellij_session":"session-a"}}]}`)
}

func TestAcquireRemoteUsesArgvAndDecodesJSON(t *testing.T) {
	runner := &recordingRunner{outputs: []outputResult{{data: validRemoteSnapshot()}}}
	snapshot, err := AcquireRemote(context.Background(), runner, RemoteConfig{
		Host: "user@source-node", SSHCommand: "custom-ssh", SSHOptions: []string{"-p", "2222"},
		SnapshotCommand: []string{"redeem tool", "mirror", "snapshot", "a'b"},
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if snapshot.Host != "source-node" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	call := runner.outputCalls[0]
	if call.Name != "custom-ssh" || strings.Join(call.Args[:4], "|") != "-p|2222|--|user@source-node" {
		t.Fatalf("unexpected SSH argv: %#v", call)
	}
	if got := call.Args[4]; got != `'redeem tool' 'mirror' 'snapshot' 'a'"'"'b'` {
		t.Fatalf("remote command was not explicitly quoted: %q", got)
	}
}

func TestDecodeSnapshotRejectsMalformedRemoteOutput(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`not-json`), []byte(`{}`), []byte(`{"host":"x","generated_at":"2026-01-01T00:00:00Z","windows":[{"app_id":"kitty"}]}`)} {
		if _, err := DecodeSnapshot(payload); err == nil {
			t.Fatalf("expected malformed payload error for %s", payload)
		}
	}
}

func TestDiscoverFiltersOrdersAndDeduplicatesExactSessions(t *testing.T) {
	snapshot := Snapshot{Windows: []Window{
		{Order: 3, SourceWindowID: 3, AppID: "kitty", ZellijSession: "later"},
		{Order: 1, SourceWindowID: 1, AppID: "firefox", ZellijSession: "skip-app"},
		{Order: 2, SourceWindowID: 2, AppID: "Kitty", Terminal: &Terminal{ZellijSession: "first"}},
		{Order: 0, SourceWindowID: 4, AppID: "kitty"},
		{Order: 4, AppID: "zellij", Headless: true, ZellijSession: "first"},
		{Order: 5, AppID: "zellij", Headless: true, ZellijSession: "headless"},
	}}
	got := Discover(snapshot)
	if len(got) != 3 || SessionName(got[0]) != "first" || SessionName(got[1]) != "later" || SessionName(got[2]) != "headless" || got[0].Headless {
		t.Fatalf("unexpected discovery: %#v", got)
	}
}

func TestPlanLaunchAttachMetadata(t *testing.T) {
	window := Window{Order: 4, SourceWindowID: 9, Title: "Project\nTitle", ZellijSession: "bad'; echo owned", Terminal: &Terminal{CWD: "/tmp/a'b"}}
	cfg := LaunchConfig{SourceHost: "source", SSHCommand: "ssh", SSHOptions: []string{"-o", "BatchMode=yes"}, LauncherCommand: "kitty", SelfCommand: "redeem", AppID: "redeem-mirror", Socket: "unix:/tmp/redeem.sock", Clipboard: true}
	plan, err := PlanLaunch(window, cfg)
	if err != nil {
		t.Fatalf("plan attach: %v", err)
	}
	if plan.Title != "source[4]: Project Title" || plan.RemoteCWD != "/tmp/a'b" {
		t.Fatalf("metadata lost: %#v", plan)
	}
	rendered := strings.Join(plan.Command.Args, " ")
	if !strings.Contains(rendered, "'env' '-u' 'ZELLIJ'") || !strings.Contains(rendered, "'zellij'") || !strings.Contains(rendered, "'attach'") || !strings.Contains(rendered, "'options' '--on-force-close' 'detach'") {
		t.Fatalf("missing exact attach/env scrub/detach policy: %s", rendered)
	}
	if strings.Contains(rendered, "'--create'") {
		t.Fatalf("existing-session attach must not create: %s", rendered)
	}
	if !strings.Contains(rendered, `'bad'"'"'; echo owned'`) || !strings.Contains(rendered, `cd -- '/tmp/a'"'"'b'`) {
		t.Fatalf("untrusted metadata was not quoted: %s", rendered)
	}
	if plan.Command.Args[len(plan.Command.Args)-3] != "--" || plan.Command.Args[len(plan.Command.Args)-2] != "source" {
		t.Fatalf("SSH host boundary missing: %#v", plan.Command.Args)
	}
}

func TestPlanNewCreatesOnlyGeneratedExactSessionAndDetaches(t *testing.T) {
	cfg := LaunchConfig{SourceHost: "user@lattice", SSHCommand: "ssh", SSHOptions: []string{"-p", "2222"}, LauncherCommand: "kitty", SelfCommand: "redeem", AppID: "redeem-mirror"}
	plan, err := PlanNew("redeem-0123456789abcdef0123456789abcdef", cfg)
	if err != nil {
		t.Fatalf("plan new: %v", err)
	}
	rendered := strings.Join(plan.Command.Args, " ")
	for _, want := range []string{"'zellij' 'attach' '--create' 'redeem-0123456789abcdef0123456789abcdef'", "'options' '--on-force-close' 'detach'"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("new launch missing %q: %s", want, rendered)
		}
	}
	for _, forbidden := range []string{"${SHELL", "exec sh", "exec bash", "||"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("new launch contains fallback %q: %s", forbidden, rendered)
		}
	}
	if _, err := PlanNew("existing-session", cfg); err == nil {
		t.Fatal("arbitrary existing name was accepted for create")
	}
}

func TestNewSessionNameIsSafeAndUnique(t *testing.T) {
	first, err := NewSessionName()
	if err != nil || !generatedSessionPattern.MatchString(first) {
		t.Fatalf("first session = %q err=%v", first, err)
	}
	second, err := NewSessionName()
	if err != nil || !generatedSessionPattern.MatchString(second) || first == second {
		t.Fatalf("second session = %q err=%v", second, err)
	}
}

func TestOwnedWindowFilteringAndCloseDryRun(t *testing.T) {
	raw := []byte(`[
		{"id":1,"pid":101,"app_id":"redeem-mirror","title":"source[0]: one","workspace_id":2},
		{"id":2,"app_id":"redeem-mirror","title":"other[0]: two"},
		{"id":3,"app_id":"kitty","title":"source[1]: unrelated"}
	]`)
	windows, err := DecodeOwnedWindows(raw, "redeem-mirror", "source")
	if err != nil || len(windows) != 1 || windows[0].ID != 1 || windows[0].PID != 101 {
		t.Fatalf("owned filter: %#v err=%v", windows, err)
	}
	runner := &recordingRunner{}
	manager := WindowManager{Runner: runner, NiriCommand: "niri"}
	if err := manager.Close(context.Background(), windows, true); err != nil {
		t.Fatal(err)
	}
	if len(runner.runCalls) != 0 {
		t.Fatal("dry-run executed Niri action")
	}
	if err := manager.Close(context.Background(), windows, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.runCalls[0].Args, " "); got != "msg action close-window --id 1" {
		t.Fatalf("close argv: %s", got)
	}
}

func TestPasteImageCopiesUniquePathAndInjectsIt(t *testing.T) {
	tempDir := t.TempDir()
	runner := &recordingRunner{outputs: []outputResult{
		{data: []byte("text/plain\nimage/png\n")},
		{data: []byte{0x89, 'P', 'N', 'G'}},
	}}
	runner.onRun = func(command Command) {
		if command.Name == "scp" {
			path := command.Args[len(command.Args)-2]
			if _, err := os.Stat(path); err != nil {
				t.Errorf("image missing during scp: %v", err)
			}
		}
	}
	result, err := (PasteBridge{Runner: runner, ID: func() (string, error) { return "unique", nil }}).Paste(context.Background(), PasteConfig{
		SourceHost: "source", SSHCommand: "ssh", SSHOptions: []string{"-p", "22"}, SCPCommand: "scp",
		ClipboardCommand: "wl-paste", KittyCommand: "kitty", KittyTo: "unix:/tmp/k.sock", TempDir: tempDir,
		MIMETypes: []string{"image/png", "image/jpeg"},
	})
	if err != nil {
		t.Fatalf("paste: %v", err)
	}
	wantPath := tempDir + "/redeem-clipboard-unique.png"
	if !result.Image || result.RemotePath != wantPath || len(runner.runCalls) != 3 {
		t.Fatalf("unexpected result/calls: %#v %#v", result, runner.runCalls)
	}
	if runner.runCalls[0].Name != "ssh" || !strings.Contains(runner.runCalls[0].Args[len(runner.runCalls[0].Args)-1], ShellQuote(tempDir)) {
		t.Fatalf("mkdir command: %#v", runner.runCalls[0])
	}
	if runner.runCalls[1].Name != "scp" || runner.runCalls[1].Args[len(runner.runCalls[1].Args)-1] != "source:"+wantPath {
		t.Fatalf("scp argv: %#v", runner.runCalls[1])
	}
	if got := string(runner.runCalls[2].Stdin); got != wantPath {
		t.Fatalf("injected %q", got)
	}
	if _, err := os.Stat(wantPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local image not cleaned up: %v", err)
	}
}

func TestPasteNonImageForwardsControlV(t *testing.T) {
	runner := &recordingRunner{outputs: []outputResult{{data: []byte("text/plain\n")}}}
	result, err := (PasteBridge{Runner: runner}).Paste(context.Background(), PasteConfig{
		SourceHost: "source", SSHCommand: "ssh", SCPCommand: "scp", ClipboardCommand: "wl-paste",
		KittyCommand: "kitty", KittyTo: "unix:/tmp/k.sock", TempDir: "/tmp", MIMETypes: []string{"image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FellBack || len(runner.runCalls) != 1 || string(runner.runCalls[0].Stdin) != string([]byte{0x16}) {
		t.Fatalf("fallback: %#v %#v", result, runner.runCalls)
	}
}

func TestFilterSessionsReportsMissingDeterministically(t *testing.T) {
	_, err := FilterSessions([]Window{{ZellijSession: "a"}}, []string{"z", "b"})
	if err == nil || !strings.Contains(err.Error(), "b, z") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeSnapshotRejectsUnsafeProjectMetadata(t *testing.T) {
	for _, project := range []string{
		`[{"label":"bad\u001b[31m","background":{"r":1,"g":2,"b":3},"foreground":{"r":255,"g":255,"b":255}}]`,
		`[{"label":"one"},{"label":"two"},{"label":"three"}]`,
	} {
		payload := `{"host":"source","generated_at":"2026-07-10T12:00:00Z","windows":[{"source_window_id":1,"app_id":"kitty","zellij_session":"s","terminal":{"zellij_session":"s","project":` + project + `}}]}`
		if _, err := DecodeSnapshot([]byte(payload)); err == nil || !strings.Contains(err.Error(), "project") {
			t.Fatalf("unsafe project metadata accepted: %s err=%v", payload, err)
		}
	}
}

func TestDecodeSnapshotValidTimestamp(t *testing.T) {
	snapshot, err := DecodeSnapshot(validRemoteSnapshot())
	if err != nil || !snapshot.GeneratedAt.Equal(time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("decode: %#v %v", snapshot, err)
	}
}
