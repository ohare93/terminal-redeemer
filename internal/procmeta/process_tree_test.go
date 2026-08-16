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
	for pid := 100; pid < 110; pid++ {
		writeProcessTreeFixture(t, root, pid, 1, pid, []byte("process\x00"))
	}
	ctx := &cancelAfterChecksContext{Context: context.Background(), after: 3}
	_, _, err := processTable(ctx, root)
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProcessStat(t, root, pid, ppid, start)
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdline, 0o600); err != nil {
		t.Fatal(err)
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
