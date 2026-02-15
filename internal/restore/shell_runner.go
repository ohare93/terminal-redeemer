package restore

import (
	"context"
	"os/exec"
)

type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	return cmd.Run()
}
