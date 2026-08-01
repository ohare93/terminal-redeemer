package zellijlive

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func shortSocketTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "zl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestProcObserverUsesExactProcessEvidenceNotTitle(t *testing.T) {
	root := t.TempDir()
	kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
	if err := os.Symlink("/nix/store/example/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
		t.Fatal(err)
	}
	child := writeProcFixture(t, root, 2, 1, "zellij", nil, nil)
	if err := os.WriteFile(filepath.Join(child, "cmdline"), []byte("zellij\x00attach\x00--\x00project\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "environ"), []byte("ZELLIJ_SESSION_NAME=project\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := (ProcObserver{ProcRoot: root}).Observe(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.KittyVerified || len(evidence.Candidates) != 1 || evidence.Candidates[0] != "project" {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
}

func TestCommandCatalogClassifiesActiveDeadPrefixAndNeverAttaches(t *testing.T) {
	root := shortSocketTempDir(t)
	base := filepath.Join(root, "sockets")
	versionDir := filepath.Join(base, SocketContractDir)
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(versionDir, "project-long"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cache := filepath.Join(root, "cache")
	if err := os.MkdirAll(filepath.Join(cache, "zellij", SocketContractDir, "session_info", "dead"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "args.log")
	script := filepath.Join(root, "zellij")
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nif [ \"$1\" = --version ]; then echo 'zellij " + PinnedVersion + "'; exit 0; fi\necho project-long\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: cache, BootID: "boot", UID: os.Getuid()}).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Exact("project-long").Status != StatusActive {
		t.Fatalf("active: %+v", catalog.Exact("project-long"))
	}
	if catalog.Exact("project").Status != StatusPrefixOnly {
		t.Fatalf("prefix: %+v", catalog.Exact("project"))
	}
	if catalog.Exact("dead").Status != StatusDeadResurrectable {
		t.Fatalf("dead: %+v", catalog.Exact("dead"))
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "attach") || strings.Contains(string(log), "create") {
		t.Fatalf("inventory used unsafe command: %s", log)
	}
}

func TestConflictingEnvironmentAndArgvRemainAmbiguous(t *testing.T) {
	values := candidatesFrom([]string{"zellij", "attach", "--", "target"}, []string{"ZELLIJ_SESSION_NAME=outer"})
	if len(values) != 2 || values[0] != "outer" || values[1] != "target" {
		t.Fatalf("conflicting evidence collapsed: %v", values)
	}
}

func TestSafeSessionNameAndIdentity(t *testing.T) {
	if !SafeSessionName("project-1") || SafeSessionName("../project") || SafeSessionName("bad name") {
		t.Fatal("session name validation failed")
	}
	if SessionID("boot", "name", 1, 2) == SessionID("boot", "name", 1, 3) {
		t.Fatal("socket reuse did not rotate session identity")
	}
}

func TestCommandCatalogRejectsDuplicateListing(t *testing.T) {
	root := shortSocketTempDir(t)
	base := filepath.Join(root, "sockets")
	versionDir := filepath.Join(base, SocketContractDir)
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(versionDir, "dup"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	script := filepath.Join(root, "zellij")
	content := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij " + PinnedVersion + "'; else printf 'dup\\ndup\\n'; fi\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: filepath.Join(root, "cache"), BootID: "boot", UID: os.Getuid()}).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Exact("dup").Status != StatusDuplicate {
		t.Fatalf("duplicate became active: %+v", catalog.Exact("dup"))
	}
}

func writeProcFixture(t *testing.T, root string, pid, ppid int, comm string, args, environ []byte) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(fmt.Sprintf("%d (%s) S %d 0 0", pid, comm, ppid)), 0o600); err != nil {
		t.Fatal(err)
	}
	taskDir := filepath.Join(dir, "task", strconv.Itoa(pid))
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "children"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ppid > 0 {
		parentChildren := filepath.Join(root, strconv.Itoa(ppid), "task", strconv.Itoa(ppid), "children")
		file, err := os.OpenFile(parentChildren, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(file, "%d ", pid); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if args == nil {
		args = []byte("sh\x00")
	}
	if environ == nil {
		environ = []byte{}
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), args, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), environ, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProcObserverFindsChildCreatedByNonLeaderThread(t *testing.T) {
	root := t.TempDir()
	kitty := writeProcFixture(t, root, 100, 0, "kitty", nil, nil)
	if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
		t.Fatal(err)
	}
	writeProcFixture(t, root, 200, 100, "zellij", []byte("zellij\x00attach\x00project\x00"), nil)
	leaderChildren := filepath.Join(root, "100", "task", "100", "children")
	if err := os.WriteFile(leaderChildren, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	workerTask := filepath.Join(root, "100", "task", "101")
	if err := os.MkdirAll(workerTask, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerTask, "children"), []byte("200 "), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := (ProcObserver{ProcRoot: root}).Observe(context.Background(), 100)
	if err != nil || !evidence.KittyVerified || len(evidence.Candidates) != 1 || evidence.Candidates[0] != "project" {
		t.Fatalf("non-leader child was not observed: evidence=%+v err=%v", evidence, err)
	}
}

func TestProcObserverRejectsTruncatedNodeAndDepthTraversal(t *testing.T) {
	t.Run("node bound", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		for pid := 2; pid <= maxProcessNodes+2; pid++ {
			writeProcFixture(t, root, pid, 1, "sh", nil, nil)
		}
		if _, err := (ProcObserver{ProcRoot: root}).Observe(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "node bound") {
			t.Fatalf("partial node traversal accepted: %v", err)
		}
	})
	t.Run("depth bound", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		parent := 1
		for depth := 1; depth <= maxProcessDepth+1; depth++ {
			pid := depth + 1
			writeProcFixture(t, root, pid, parent, "sh", nil, nil)
			parent = pid
		}
		if _, err := (ProcObserver{ProcRoot: root}).Observe(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "depth bound") {
			t.Fatalf("partial depth traversal accepted: %v", err)
		}
	})
}

func TestExactProcessAndVersionBasenamesRejectNearMatches(t *testing.T) {
	root := t.TempDir()
	process := writeProcFixture(t, root, 1, 0, "notkitty", nil, nil)
	if err := os.Symlink("/bin/notkitty", filepath.Join(process, "exe")); err != nil {
		t.Fatal(err)
	}
	evidence, err := (ProcObserver{ProcRoot: root}).Observe(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.KittyVerified {
		t.Fatal("notkitty accepted as Kitty")
	}
	if got := candidatesFrom([]string{"zellij-helper", "attach", "project"}, nil); len(got) != 0 {
		t.Fatalf("zellij-helper accepted: %v", got)
	}
	base := filepath.Join(t.TempDir(), "sockets")
	if err := os.MkdirAll(filepath.Join(base, SocketContractDir), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "zellij")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij 0.44.30'; else exit 0; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: t.TempDir(), BootID: "boot", UID: os.Getuid()}).Observe(context.Background()); err == nil {
		t.Fatal("near-match Zellij version accepted")
	}
}

func TestCommandCatalogFailureSingletonAndScannerTaxonomy(t *testing.T) {
	t.Run("failed empty listing", func(t *testing.T) {
		root, err := os.MkdirTemp("/tmp", "zl-fail-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
		base := filepath.Join(root, "sockets")
		versionDir := filepath.Join(base, SocketContractDir)
		if err := os.MkdirAll(versionDir, 0o700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(versionDir, "project"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		script := filepath.Join(root, "zellij")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij "+PinnedVersion+"'; exit 0; fi\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: filepath.Join(root, "cache"), BootID: "boot", UID: os.Getuid()}).Observe(context.Background()); err == nil {
			t.Fatal("failed empty listing accepted")
		}
	})
	t.Run("successful empty listing", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "sockets")
		if err := os.MkdirAll(filepath.Join(base, SocketContractDir), 0o700); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(root, "zellij")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij "+PinnedVersion+"'; fi\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		catalog, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: filepath.Join(root, "cache"), BootID: "boot", UID: os.Getuid()}).Observe(context.Background())
		if err != nil || len(catalog.Names) != 0 {
			t.Fatalf("successful empty catalog rejected: %+v %v", catalog, err)
		}
	})
	t.Run("singleton list without socket", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "sockets")
		if err := os.MkdirAll(filepath.Join(base, SocketContractDir), 0o700); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(root, "zellij")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij "+PinnedVersion+"'; else echo missing; fi\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		catalog, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: filepath.Join(root, "cache"), BootID: "boot", UID: os.Getuid()}).Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if catalog.Exact("missing").Status != StatusMissing {
			t.Fatalf("list-only singleton taxonomy: %+v", catalog.Exact("missing"))
		}
	})
	t.Run("oversized line", func(t *testing.T) {
		if _, err := parseLines([]byte(strings.Repeat("x", 70<<10) + "\n")); err == nil {
			t.Fatal("oversized catalog line silently truncated")
		}
		root := t.TempDir()
		base := filepath.Join(root, "sockets")
		if err := os.MkdirAll(filepath.Join(base, SocketContractDir), 0o700); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(root, "zellij")
		content := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij " + PinnedVersion + "'; else printf '%070000d\\n' 0; fi\n"
		if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: filepath.Join(root, "cache"), BootID: "boot", UID: os.Getuid()}).Observe(context.Background()); err == nil {
			t.Fatal("partial oversized command catalog published")
		}
	})
}

func TestProcObserverRejectsUnreadableOrMalformedRelevantTreeEvidence(t *testing.T) {
	assertIncomplete := func(t *testing.T, root string) {
		t.Helper()
		_, err := (ProcObserver{ProcRoot: root}).Observe(context.Background(), 1)
		if err == nil || !errors.Is(err, ErrProcessObservationIncomplete) {
			t.Fatalf("incomplete process evidence accepted: %v", err)
		}
	}
	t.Run("unreadable root identity metadata", func(t *testing.T) {
		root := t.TempDir()
		taskDir := filepath.Join(root, "1", "task", "1")
		if err := os.MkdirAll(taskDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(taskDir, "children"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
	t.Run("malformed root children edge", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "1", "task", "1", "children"), []byte("not-a-pid"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
	t.Run("relevant child disappears", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		writeProcFixture(t, root, 2, 1, "zellij", []byte("zellij\x00attach\x00project\x00"), nil)
		if err := os.RemoveAll(filepath.Join(root, "2")); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
	t.Run("relevant child metadata disappears", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		child := writeProcFixture(t, root, 2, 1, "zellij", []byte("zellij\x00attach\x00project\x00"), nil)
		if err := os.Remove(filepath.Join(child, "cmdline")); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
	t.Run("malformed relevant child identity", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		child := writeProcFixture(t, root, 2, 1, "zellij", []byte("zellij\x00attach\x00project\x00"), nil)
		if err := os.WriteFile(filepath.Join(child, "stat"), []byte("malformed"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
	t.Run("malformed relevant child metadata", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		child := writeProcFixture(t, root, 2, 1, "zellij", []byte{0xff, 0}, nil)
		if _, err := os.Stat(child); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
	t.Run("malformed relevant child edge", func(t *testing.T) {
		root := t.TempDir()
		kitty := writeProcFixture(t, root, 1, 0, "kitty", nil, nil)
		if err := os.Symlink("/bin/kitty", filepath.Join(kitty, "exe")); err != nil {
			t.Fatal(err)
		}
		child := writeProcFixture(t, root, 2, 1, "zellij", []byte("zellij\x00attach\x00project\x00"), nil)
		if err := os.WriteFile(filepath.Join(child, "task", "2", "children"), []byte("3 broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(t, root)
	})
}

func TestCommandCatalogPropagatesDeadSessionCatalogReadFailures(t *testing.T) {
	setup := func(t *testing.T) (string, string, string) {
		t.Helper()
		root := t.TempDir()
		base := filepath.Join(root, "sockets")
		if err := os.MkdirAll(filepath.Join(base, SocketContractDir), 0o700); err != nil {
			t.Fatal(err)
		}
		cache := filepath.Join(root, "cache")
		script := filepath.Join(root, "zellij")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'zellij "+PinnedVersion+"'; fi\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return base, cache, script
	}
	t.Run("session_info is not a directory", func(t *testing.T) {
		base, cache, script := setup(t)
		deadDir := filepath.Join(cache, "zellij", SocketContractDir, "session_info")
		if err := os.MkdirAll(filepath.Dir(deadDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(deadDir, []byte("malformed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (CommandCataloger{Command: script, SocketBase: base, CacheHome: cache, BootID: "boot", UID: os.Getuid()}).Observe(context.Background()); err == nil || !strings.Contains(err.Error(), "resurrection catalog") {
			t.Fatalf("malformed dead catalog accepted: %v", err)
		}
	})
	t.Run("injected read failure", func(t *testing.T) {
		base, cache, script := setup(t)
		deadDir := filepath.Join(cache, "zellij", SocketContractDir, "session_info")
		cataloger := CommandCataloger{Command: script, SocketBase: base, CacheHome: cache, BootID: "boot", UID: os.Getuid()}
		cataloger.readDir = func(path string) ([]os.DirEntry, error) {
			if path == deadDir {
				return nil, errors.New("injected permission failure")
			}
			return os.ReadDir(path)
		}
		if _, err := cataloger.Observe(context.Background()); err == nil || !strings.Contains(err.Error(), "injected permission failure") {
			t.Fatalf("injected dead catalog failure ignored: %v", err)
		}
	})
}
