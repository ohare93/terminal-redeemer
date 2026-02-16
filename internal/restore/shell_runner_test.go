package restore

import (
	"context"
	"testing"
	"time"
)

func TestShellRunnerReturnsQuicklyForLongRunningCommand(t *testing.T) {
	t.Parallel()

	runner := ShellRunner{StartupCheck: 20 * time.Millisecond}
	start := time.Now()
	err := runner.Run(context.Background(), "sleep 0.1")
	if err != nil {
		t.Fatalf("run long-lived command: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 90*time.Millisecond {
		t.Fatalf("expected non-blocking launch, elapsed=%s", elapsed)
	}
}

func TestShellRunnerReturnsErrorForImmediateFailure(t *testing.T) {
	t.Parallel()

	runner := ShellRunner{StartupCheck: 300 * time.Millisecond}
	err := runner.Run(context.Background(), "definitely-not-a-command")
	if err == nil {
		t.Fatal("expected launch failure error")
	}
}
