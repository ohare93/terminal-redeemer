package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
	cmd := exec.CommandContext(ctx, request.Name, request.Args...)
	cmd.Stdin = bytes.NewReader(request.Stdin)
	if r.Env != nil {
		cmd.Env = append([]string(nil), r.Env...)
	}
	return cmd
}

func (r ExecRunner) Output(ctx context.Context, request Command) ([]byte, error) {
	cmd := r.command(ctx, request)
	output, err := cmd.Output()
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
	if err == nil {
		return nil
	}
	if len(output) > 0 {
		return fmt.Errorf("%s: %w: %s", request.Name, err, strings.TrimSpace(string(output)))
	}
	return fmt.Errorf("%s: %w", request.Name, err)
}

// GraphicalEnvironment merges the Niri/Wayland variables exported by the
// user manager into env. This lets a short SSH-invoked helper launch a local
// graphical client without inheriting a display from the SSH session.
func GraphicalEnvironment(env []string) []string {
	if hasEnv(env, "NIRI_SOCKET") && hasEnv(env, "WAYLAND_DISPLAY") {
		return append([]string(nil), env...)
	}

	out, err := exec.Command("systemctl", "--user", "show-environment").Output()
	if err != nil {
		return append([]string(nil), env...)
	}

	merged := append([]string(nil), env...)
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch name {
		case "NIRI_SOCKET", "WAYLAND_DISPLAY", "DISPLAY", "XDG_RUNTIME_DIR", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE", "DBUS_SESSION_BUS_ADDRESS":
			merged = setEnv(merged, name, value)
		}
	}
	return merged
}

func hasEnv(env []string, name string) bool {
	prefix := name + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) && strings.TrimSpace(strings.TrimPrefix(entry, prefix)) != "" {
			return true
		}
	}
	return false
}

func setEnv(env []string, name string, value string) []string {
	prefix := name + "="
	entry := prefix + value
	for i := range env {
		if strings.HasPrefix(env[i], prefix) {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}
