package procrun

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextKillsPipeHoldingDescendantsWithinWallClockBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	cmd := CommandContext(ctx, "sh", "-c", "sleep 5 & child=$!; printf '%s\\n' \"$child\"; wait")
	output, err := cmd.Output()
	elapsed := time.Since(started)
	if !errors.Is(ContextError(ctx, err), context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("pipe-holding descendant kept command alive for %s", elapsed)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(output)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("child pid output %q: %v", output, parseErr)
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
