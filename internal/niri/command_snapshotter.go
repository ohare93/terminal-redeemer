package niri

import (
	"context"
	"fmt"
	"os/exec"
)

type CommandRunner interface {
	Run(ctx context.Context, command string) ([]byte, error)
}

type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, command string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	return cmd.Output()
}

type CommandSnapshotter struct {
	Command string
	Runner  CommandRunner
}

func (s CommandSnapshotter) Snapshot(ctx context.Context) ([]byte, error) {
	runner := s.Runner
	if runner == nil {
		runner = ShellRunner{}
	}
	out, err := runner.Run(ctx, s.Command)
	if err != nil {
		return nil, fmt.Errorf("run niri snapshot command: %w", err)
	}
	return out, nil
}
