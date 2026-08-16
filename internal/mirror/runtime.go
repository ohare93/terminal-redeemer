package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/procrun"
)

// Command is an argv-based process request. Stdin may contain binary data.
type Command struct {
	Name  string
	Args  []string
	Stdin []byte
}

// Runner isolates all SSH, Niri, Kitty, and clipboard process execution.
type Runner interface {
	Output(context.Context, Command) ([]byte, error)
	Run(context.Context, Command) error
}

type ExecRunner struct {
	Env []string
}

func (r ExecRunner) command(ctx context.Context, request Command) *exec.Cmd {
	cmd := procrun.CommandContext(ctx, request.Name, request.Args...)
	cmd.Stdin = bytes.NewReader(request.Stdin)
	if r.Env != nil {
		cmd.Env = append([]string(nil), r.Env...)
	}
	return cmd
}

func (r ExecRunner) Output(ctx context.Context, request Command) ([]byte, error) {
	cmd := r.command(ctx, request)
	output, err := cmd.Output()
	err = procrun.ContextError(ctx, err)
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return nil, fmt.Errorf("%s: %w: %s", request.Name, err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return nil, fmt.Errorf("%s: %w", request.Name, err)
}

func (r ExecRunner) Run(ctx context.Context, request Command) error {
	cmd := r.command(ctx, request)
	output, err := cmd.CombinedOutput()
	err = procrun.ContextError(ctx, err)
	if err == nil {
		return nil
	}
	if len(output) > 0 {
		return fmt.Errorf("%s: %w: %s", request.Name, err, strings.TrimSpace(string(output)))
	}
	return fmt.Errorf("%s: %w", request.Name, err)
}
