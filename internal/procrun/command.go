package procrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const waitDelay = 100 * time.Millisecond

// CommandContext returns a Linux process-group-bounded command. When the
// context expires, the whole group is killed so descendants cannot retain
// stdout or stderr pipes and keep Wait blocked indefinitely.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}

// ContextError preserves both useful command failures and the context cause.
func ContextError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	return errors.Join(ctx.Err(), err)
}
