package resume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/model"
)

type staticSnapshot []byte

func (s staticSnapshot) Snapshot(context.Context) ([]byte, error) { return s, nil }

func TestSnapshotObserverPreservesExactNiriIdentity(t *testing.T) {
	observer := SnapshotObserver{Source: staticSnapshot(`{"windows":[{"id":42,"pid":900,"app_id":"kitty","workspace_id":7},{"id":43,"pid":901,"app_id":"kitty","workspace_id":8}]}`)}
	windows, err := observer.Windows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ObservedWindow{{ID: 42, PID: 900, AppID: "kitty", WorkspaceID: "7"}, {ID: 43, PID: 901, AppID: "kitty", WorkspaceID: "8"}}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %#v, want %#v", windows, want)
	}
}

func TestProcAttachmentProbeRequiresLiveExactAttachOnlyDescendant(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 100, 1, []string{"kitty", "zellij", "attach", "--", "target"})
	writeFakeProcess(t, root, 101, 100, []string{"zellij", "attach", "--", "target"})
	writeFakeProcess(t, root, 102, 100, []string{"zellij", "attach", "--", "target", "--create"})
	writeFakeProcess(t, root, 103, 100, []string{"zellij", "attach", "target"})
	writeFakeProcess(t, root, 104, 100, []string{"zellij", "attach", "--", "-leading"})
	probe := ProcAttachmentProbe{ProcRoot: root}

	attached, err := probe.Attached(context.Background(), 100, "target")
	if err != nil || !attached {
		t.Fatalf("exact attach descendant: attached=%v err=%v", attached, err)
	}
	attached, err = probe.Attached(context.Background(), 100, "-leading")
	if err != nil || !attached {
		t.Fatalf("leading-dash session: attached=%v err=%v", attached, err)
	}
	attached, err = probe.Attached(context.Background(), 100, "other")
	if err != nil || attached {
		t.Fatalf("wrong session: attached=%v err=%v", attached, err)
	}

	if err := os.RemoveAll(filepath.Join(root, "101")); err != nil {
		t.Fatal(err)
	}
	attached, err = probe.Attached(context.Background(), 100, "target")
	if err != nil || attached {
		t.Fatalf("Kitty argv, legacy no-delimiter argv, extra args, or --create must not count as evidence: attached=%v err=%v", attached, err)
	}
}

func writeFakeProcess(t *testing.T, root string, pid, ppid int, args []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	taskDir := filepath.Join(dir, "task", strconv.Itoa(pid))
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (process) S " + strconv.Itoa(ppid) + " 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "children"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if parent, err := os.OpenFile(filepath.Join(root, strconv.Itoa(ppid), "task", strconv.Itoa(ppid), "children"), os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		if _, err := parent.WriteString(strconv.Itoa(pid) + " "); err != nil {
			_ = parent.Close()
			t.Fatal(err)
		}
		if err := parent.Close(); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte{}
	for _, arg := range args {
		payload = append(payload, []byte(arg)...)
		payload = append(payload, 0)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

type recordingActions struct {
	calls []actionCall
	fail  string
}

type actionCall struct {
	action string
	args   []string
}

func (r *recordingActions) Run(_ context.Context, action string, args ...string) error {
	r.calls = append(r.calls, actionCall{action: action, args: append([]string(nil), args...)})
	if action == r.fail {
		return errors.New("unsupported")
	}
	return nil
}

func TestNiriActionsMoveUsesExactWindowAndResolvedWorkspace(t *testing.T) {
	runner := &recordingActions{}
	actions := NiriActions{Runner: runner}
	if err := actions.MoveToWorkspace(context.Background(), 42, WorkspaceTarget{ID: "runtime-7", Name: "dev", Index: 7}); err != nil {
		t.Fatal(err)
	}
	want := actionCall{action: "move-window-to-workspace", args: []string{"--window-id", "42", "--focus", "false", "dev"}}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestNiriOptionalLayoutReportsUnsupportedColumnSeparately(t *testing.T) {
	runner := &recordingActions{}
	floating := true
	column := 3
	result := (NiriActions{Runner: runner}).ApplyLayout(context.Background(), 42, model.Placement{
		Column: &column, IsFloating: &floating, WindowSize: []int{900, 700},
	})
	if result.Status != LayoutDegraded || result.Reason == "" {
		t.Fatalf("layout result = %#v", result)
	}
	wantActions := []string{"move-window-to-floating", "set-window-width", "set-window-height"}
	if len(runner.calls) != len(wantActions) {
		t.Fatalf("calls = %#v", runner.calls)
	}
	for i, want := range wantActions {
		if runner.calls[i].action != want {
			t.Fatalf("call %d = %#v, want %s", i, runner.calls[i], want)
		}
	}
}
