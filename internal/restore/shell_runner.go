package restore

import (
	"context"
	"os/exec"
	"time"
)

type ShellRunner struct {
	StartupCheck time.Duration
}

func (r ShellRunner) Run(ctx context.Context, command string) error {
	return r.run(ctx, command)
}

func (r ShellRunner) run(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	startupCheck := r.StartupCheck
	if startupCheck <= 0 {
		startupCheck = 200 * time.Millisecond
	}

	select {
	case err := <-done:
		return err
	case <-time.After(startupCheck):
		return nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return ctx.Err()
	}
}
