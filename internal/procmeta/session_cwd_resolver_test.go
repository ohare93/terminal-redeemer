package procmeta

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestZellijSessionCWDResolverResolve(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcResolverEntry(t, root, 200922, 200880, "zellij", "/home/jmo", "zellij --layout minimal")
	writeProcResolverEntry(t, root, 6219, 1, "zellij", "/home/jmo", "zellij --server /run/user/1000/zellij/0.43.1/sensible-bee")
	writeProcResolverEntry(t, root, 6244, 6219, "zsh", "/home/jmo/Development/active/tools/terminal-redeemer", "zsh")

	resolver := NewZellijSessionCWDResolver(root)
	got, err := resolver.Resolve("sensible-bee")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "/home/jmo/Development/active/tools/terminal-redeemer" {
		t.Fatalf("expected child shell cwd, got %q", got)
	}
}

func TestZellijSessionCWDResolverMissingSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolver := NewZellijSessionCWDResolver(root)
	got, err := resolver.Resolve("missing")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty cwd for missing session, got %q", got)
	}
}

func writeProcResolverEntry(t *testing.T, root string, pid int, ppid int, comm string, cwd string, cmdline string) {
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
	cmdPayload := []byte(cmdline)
	if cmdline != "" {
		cmdPayload = append(cmdPayload, '\x00')
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdPayload, 0o600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatalf("write cwd symlink: %v", err)
	}
}
