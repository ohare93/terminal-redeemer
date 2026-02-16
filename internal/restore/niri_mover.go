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

	if err := runNiriAction(ctx, "focus-window", strconv.Itoa(windowID)); err != nil {
		return err
	}
	if err := runNiriAction(ctx, "move-window-to-workspace", workspaceRef); err != nil {
		return err
	}
	return nil
}

func runNiriAction(ctx context.Context, action string, arg string) error {
	cmd := exec.CommandContext(ctx, "niri", "msg", "action", action, arg)
	return cmd.Run()
}
