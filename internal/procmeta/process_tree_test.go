package procmeta

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestReadProcArgsPreservesInteriorEmptyArguments(t *testing.T) {
	root := t.TempDir()
	writeProcessTreeFixture(t, root, 10, 1, 5, []byte("command\x00\x00tail\x00"))
	args, err := readProcArgs(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"command", "", "tail"}) {
		t.Fatalf("args=%#v", args)
	}
}

type cancelAfterChecksContext struct {
	context.Context
	checks int
	after  int
}

func (ctx *cancelAfterChecksContext) Err() error {
	ctx.checks++
	if ctx.checks >= ctx.after {
		return context.Canceled
	}
	return nil
}

func TestProcessTableChecksCancellationDuringScan(t *testing.T) {
	root := t.TempDir()
	writeProcessTreeFixture(t, root, 100, 1, 100, []byte("process\x00"))
	for pid := 101; pid < 110; pid++ {
		writeProcessTreeFixture(t, root, pid, pid-1, pid, []byte("process\x00"))
	}
	ctx := &cancelAfterChecksContext{Context: context.Background(), after: 3}
	_, _, _, err := processTable(ctx, root, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("processTable error = %v, want cancellation", err)
	}
	if ctx.checks < 3 {
		t.Fatalf("process table did not scan before cancellation: %d checks", ctx.checks)
	}
}

func TestDescendantArgvMatchHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DescendantArgvMatchContext(ctx, t.TempDir(), 100, func([]string) bool { return true }); err == nil {
		t.Fatal("canceled process observation returned no error")
	}
}

func TestDescendantArgvMatchRejectsPIDReplacementAfterTreeSnapshot(t *testing.T) {
	root := t.TempDir()
	writeProcessTreeFixture(t, root, 100, 1, 10, []byte("kitty\x00"))
	writeProcessTreeFixture(t, root, 101, 100, 11, []byte("zellij\x00attach\x00target\x00"))
	matched, err := descendantArgvMatch(context.Background(), root, 100, func(args []string) bool {
		return reflect.DeepEqual(args, []string{"zellij", "attach", "target"})
	}, func() {
		writeProcessStat(t, root, 101, 100, 99)
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("accepted cmdline from PID whose start identity changed after tree snapshot")
	}
}

func TestObserveZellijSessionEvidenceRecognizesNixWrappedKitty(t *testing.T) {
	root := t.TempDir()
	writeProcessTreeFixture(t, root, 100, 1, 10, []byte("/nix/store/example-kitty/bin/.kitty-wrapped\x00"))
	if err := os.Symlink("/nix/store/example-kitty/bin/.kitty-wrapped", filepath.Join(root, "100", "exe")); err != nil {
		t.Fatal(err)
	}
	writeProcessTreeFixture(t, root, 101, 100, 11, []byte("zellij\x00attach\x00--\x00wrapped-session\x00"))

	evidence, err := ObserveZellijSessionEvidence(context.Background(), root, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.KittyVerified || !evidence.Complete || !reflect.DeepEqual(evidence.Candidates, []string{"wrapped-session"}) {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestKittyBasenameRejectsWrappedNearMatch(t *testing.T) {
	if isKittyBasename(".kitty-wrapped-extra") {
		t.Fatal("accepted a suffix near-match for .kitty-wrapped")
	}
}

func TestObserveZellijSessionEvidenceRequiresLiteralUniqueCompleteAttach(t *testing.T) {
	root := t.TempDir()
	writeProcessTreeFixture(t, root, 100, 1, 10, []byte("kitty\x00"))
	if err := os.WriteFile(filepath.Join(root, "100", "comm"), []byte("kitty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeProcessTreeFixture(t, root, 101, 100, 11, []byte("zellij\x00attach\x00--\x00Exact-Session\x00"))
	writeProcessTreeFixture(t, root, 102, 100, 12, []byte("zellij\x00attach\x00missing-separator\x00"))
	evidence, err := ObserveZellijSessionEvidence(context.Background(), root, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Complete || !evidence.KittyVerified || !reflect.DeepEqual(evidence.Candidates, []string{"Exact-Session"}) {
		t.Fatalf("evidence = %+v", evidence)
	}

	writeProcessTreeFixture(t, root, 103, 100, 13, []byte("/bin/zellij\x00attach\x00--\x00other\x00"))
	evidence, err = ObserveZellijSessionEvidence(context.Background(), root, 100)
	if err != nil || !evidence.Complete || !reflect.DeepEqual(evidence.Candidates, []string{"Exact-Session", "other"}) {
		t.Fatalf("ambiguous evidence = %+v, err=%v", evidence, err)
	}
}

func TestObserveZellijSessionEvidenceRejectsIncompleteOrReplacedProcesses(t *testing.T) {
	fixture := func(t *testing.T) string {
		root := t.TempDir()
		writeProcessTreeFixture(t, root, 100, 1, 10, []byte("kitty\x00"))
		if err := os.WriteFile(filepath.Join(root, "100", "comm"), []byte("kitty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeProcessTreeFixture(t, root, 101, 100, 11, []byte("zellij\x00attach\x00--\x00target\x00"))
		return root
	}
	t.Run("root replacement", func(t *testing.T) {
		root := fixture(t)
		evidence, err := observeZellijSessionEvidence(context.Background(), root, 100, func() { writeProcessStat(t, root, 100, 1, 99) })
		if err != nil || evidence.Complete || evidence.KittyVerified || len(evidence.Candidates) != 0 {
			t.Fatalf("replacement evidence = %+v, err=%v", evidence, err)
		}
	})
	t.Run("descendant disappears", func(t *testing.T) {
		root := fixture(t)
		evidence, err := observeZellijSessionEvidence(context.Background(), root, 100, func() { _ = os.RemoveAll(filepath.Join(root, "101")) })
		if err != nil || evidence.Complete || len(evidence.Candidates) != 0 {
			t.Fatalf("disappearance evidence = %+v, err=%v", evidence, err)
		}
	})
	t.Run("descendant metadata unreadable", func(t *testing.T) {
		root := fixture(t)
		if err := os.Remove(filepath.Join(root, "101", "cmdline")); err != nil {
			t.Fatal(err)
		}
		evidence, err := ObserveZellijSessionEvidence(context.Background(), root, 100)
		if err != nil || evidence.Complete || len(evidence.Candidates) != 0 {
			t.Fatalf("unreadable evidence = %+v, err=%v", evidence, err)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{"descendant stat unreadable", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "101", "stat")); err != nil {
				t.Fatal(err)
			}
		}},
		{"descendant stat malformed", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "101", "stat"), []byte("malformed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"child edge malformed", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "100", "task", "100", "children"), []byte("not-a-pid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fixture(t)
			test.mutate(t, root)
			evidence, err := ObserveZellijSessionEvidence(context.Background(), root, 100)
			if err != nil || evidence.Complete || len(evidence.Candidates) != 0 {
				t.Fatalf("incomplete evidence = %+v, err=%v", evidence, err)
			}
		})
	}
}

func TestObserveZellijSessionEvidenceRejectsTraversalBounds(t *testing.T) {
	newRoot := func(t *testing.T) string {
		root := t.TempDir()
		writeProcessTreeFixture(t, root, 1, 0, 1, []byte("kitty\x00"))
		if err := os.WriteFile(filepath.Join(root, "1", "comm"), []byte("kitty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	assertIncomplete := func(t *testing.T, root string) {
		evidence, err := ObserveZellijSessionEvidence(context.Background(), root, 1)
		if err != nil || evidence.Complete {
			t.Fatalf("bounded evidence = %+v, err=%v", evidence, err)
		}
	}
	t.Run("node bound", func(t *testing.T) {
		root := newRoot(t)
		for pid := 2; pid <= maxProcessNodes+1; pid++ {
			writeProcessTreeFixture(t, root, pid, 1, pid, []byte("sh\x00"))
		}
		assertIncomplete(t, root)
	})
	t.Run("depth bound", func(t *testing.T) {
		root := newRoot(t)
		parent := 1
		for depth := 1; depth <= maxProcessDepth+1; depth++ {
			pid := depth + 1
			writeProcessTreeFixture(t, root, pid, parent, pid, []byte("sh\x00"))
			parent = pid
		}
		assertIncomplete(t, root)
	})
	t.Run("task bound", func(t *testing.T) {
		root := newRoot(t)
		for task := 2; task <= maxProcessTasks+1; task++ {
			dir := filepath.Join(root, "1", "task", strconv.Itoa(task))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "children"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		assertIncomplete(t, root)
	})
	t.Run("metadata bound", func(t *testing.T) {
		root := newRoot(t)
		writeProcessTreeFixture(t, root, 2, 1, 2, []byte(strings.Repeat("x", maxProcessMetadataBytes+1)))
		assertIncomplete(t, root)
	})
}

func FuzzReadProcArgsBoundedMetadata(f *testing.F) {
	f.Add([]byte("zellij\x00attach\x00--\x00session\x00"))
	f.Add([]byte{0xff})
	f.Add([]byte(strings.Repeat("x", maxProcessMetadataBytes+1)))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > maxProcessMetadataBytes+1 {
			return
		}
		root := t.TempDir()
		writeProcessTreeFixture(t, root, 1, 0, 1, payload)
		args, err := readProcArgs(root, 1)
		if len(payload) > maxProcessMetadataBytes && err == nil {
			t.Fatal("accepted over-limit process metadata")
		}
		if err == nil {
			for _, arg := range args {
				if strings.ContainsRune(arg, 0) {
					t.Fatal("decoded argument contains NUL")
				}
			}
		}
	})
}

func TestDescendantArgvMatchRejectsReparentedDescendantAfterTreeSnapshot(t *testing.T) {
	root := t.TempDir()
	writeProcessTreeFixture(t, root, 100, 1, 10, []byte("kitty\x00"))
	writeProcessTreeFixture(t, root, 101, 100, 11, []byte("zellij\x00attach\x00target\x00"))
	matched, err := descendantArgvMatch(context.Background(), root, 100, func(args []string) bool { return len(args) > 0 }, func() {
		writeProcessStat(t, root, 101, 999, 11)
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("accepted cmdline from process reparented after tree snapshot")
	}
}

func writeProcessTreeFixture(t *testing.T, root string, pid int, ppid int, start int, cmdline []byte) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "task", strconv.Itoa(pid)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProcessStat(t, root, pid, ppid, start)
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdline, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task", strconv.Itoa(pid), "children"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	parentChildren := filepath.Join(root, strconv.Itoa(ppid), "task", strconv.Itoa(ppid), "children")
	if file, err := os.OpenFile(parentChildren, os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		if _, err := file.WriteString(strconv.Itoa(pid) + " "); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func writeProcessStat(t *testing.T, root string, pid int, ppid int, start int) {
	t.Helper()
	fields := []string{"S", strconv.Itoa(ppid)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.Itoa(start))
	payload := strconv.Itoa(pid) + " (proc) " + strings.Join(fields, " ")
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "stat"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}
