package procmeta

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseNullSeparated(t *testing.T) {
	t.Parallel()

	got := parseNullSeparated([]byte("zellij\x00attach\x00session-a\x00"))
	if len(got) != 3 || got[0] != "zellij" || got[2] != "session-a" {
		t.Fatalf("unexpected args: %#v", got)
	}
}

func TestParseEnv(t *testing.T) {
	t.Parallel()

	env := parseEnv([]byte("ZELLIJ_SESSION_NAME=my-sess\x00PWD=/tmp\x00"))
	if env["ZELLIJ_SESSION_NAME"] != "my-sess" {
		t.Fatalf("expected session name from env, got %#v", env)
	}
}

func TestParseParentPIDFromStat(t *testing.T) {
	t.Parallel()

	ppid, err := parseParentPIDFromStat("1234 (kitty) S 567 1 1 0 -1 4194304 0 0 0 0 0 0 0 0 20 0 1 0 0 0 0 0 0 0 0 0 0 0 0")
	if err != nil {
		t.Fatalf("parse ppid: %v", err)
	}
	if ppid != 567 {
		t.Fatalf("expected ppid 567, got %d", ppid)
	}
}

func TestInspectPrefersDescendantShellCWD(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 100, 1, "kitty", "/home/jmo")
	writeProcEntry(t, root, 200, 100, "zsh", "/home/jmo/Development/active/tools/terminal-redeemer")

	reader := ProcReader{ProcRoot: root}
	info, err := reader.Inspect(100)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if info.CWD != "/home/jmo/Development/active/tools/terminal-redeemer" {
		t.Fatalf("expected descendant shell cwd, got %q", info.CWD)
	}
}

func TestInspectFallsBackToWindowPIDCWD(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 100, 1, "kitty", "/home/jmo")

	reader := ProcReader{ProcRoot: root}
	info, err := reader.Inspect(100)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if info.CWD != "/home/jmo" {
		t.Fatalf("expected fallback pid cwd, got %q", info.CWD)
	}
}

func writeProcEntry(t *testing.T, root string, pid int, ppid int, comm string, cwd string) {
	t.Helper()

	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir proc entry: %v", err)
	}

	stat := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o600); err != nil {
		t.Fatalf("write comm: %v", err)
	}
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatalf("write cwd symlink: %v", err)
	}
}
