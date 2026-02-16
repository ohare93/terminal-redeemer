package restore

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type NiriWindowMover struct{}

func (NiriWindowMover) MoveToWorkspace(ctx context.Context, windowID int, workspaceRef string) error {
	workspaceRef = strings.TrimSpace(workspaceRef)
	if windowID <= 0 || workspaceRef == "" {
		return fmt.Errorf("invalid move request")
	}

	if err := runNiriAction(ctx, "move-window-to-workspace", "--window-id", strconv.Itoa(windowID), workspaceRef); err != nil {
		return err
	}
	return nil
}

func runNiriAction(ctx context.Context, action string, args ...string) error {
	cmdArgs := []string{"msg", "action", action}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "niri", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return fmt.Errorf("niri action %s failed: %w", action, err)
	}
	return fmt.Errorf("niri action %s failed: %w: %s", action, err, output)
}
