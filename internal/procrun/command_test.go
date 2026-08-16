package procrun

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextKillsPipeHoldingDescendantsWithinWallClockBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := CommandContext(ctx, "sh", "-c", "sleep 5 & child=$!; printf '%s\\n' \"$child\"; wait")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}

	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		lineCh <- lineResult{line: line, err: readErr}
	}()

	var childLine string
	select {
	case result := <-lineCh:
		if result.err != nil {
			cancel()
			_ = cmd.Wait()
			t.Fatalf("read child pid: %v", result.err)
		}
		childLine = result.line
	case <-time.After(2 * time.Second):
		cancel()
		_ = cmd.Wait()
		t.Fatal("command did not report its child pid")
	}

	pid, parseErr := strconv.Atoi(strings.TrimSpace(childLine))
	if parseErr != nil || pid <= 0 {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("child pid output %q: %v", childLine, parseErr)
	}

	started := time.Now()
	cancel()
	err = cmd.Wait()
	elapsed := time.Since(started)
	if !errors.Is(ContextError(ctx, err), context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("pipe-holding descendant kept command alive for %s", elapsed)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		killErr := syscall.Kill(pid, 0)
		if errors.Is(killErr, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d remains after group cancellation: %v", pid, killErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
