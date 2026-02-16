package niri

import (
	"encoding/json"
	"fmt"

	"github.com/jmo/terminal-redeemer/internal/model"
)

type snapshotPayload struct {
	Workspaces []workspacePayload `json:"workspaces"`
	Windows    []windowPayload    `json:"windows"`
}

type workspacePayload struct {
	ID    any    `json:"id"`
	Index int    `json:"idx"`
	Name  any    `json:"name"`
}

type windowPayload struct {
	ID          int    `json:"id"`
	AppID       any    `json:"app_id"`
	Title       string `json:"title"`
	WorkspaceID any    `json:"workspace_id"`
	PID         int    `json:"pid"`
}

func ParseSnapshot(raw []byte) (model.State, error) {
	var payload snapshotPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		var windowsOnly []windowPayload
		if windowsErr := json.Unmarshal(raw, &windowsOnly); windowsErr != nil {
			return model.State{}, fmt.Errorf("decode niri snapshot: %w", err)
		}
		payload = snapshotPayload{Windows: windowsOnly}
	}

	state := model.State{
		Workspaces: make([]model.Workspace, 0, len(payload.Workspaces)),
		Windows:    make([]model.Window, 0, len(payload.Windows)),
	}

	for _, workspace := range payload.Workspaces {
		workspaceID, _ := valueAsString(workspace.ID)
		workspaceName, _ := valueAsString(workspace.Name)
		state.Workspaces = append(state.Workspaces, model.Workspace{
			ID:    workspaceID,
			Index: workspace.Index,
			Name:  workspaceName,
		})
	}

	for _, window := range payload.Windows {
		appID, _ := valueAsString(window.AppID)
		workspaceID, _ := valueAsString(window.WorkspaceID)
		state.Windows = append(state.Windows, model.Window{
			Key:         fmt.Sprintf("w:%s:%d", appID, window.ID),
			AppID:       appID,
			WorkspaceID: workspaceID,
			PID:         window.PID,
			Title:       window.Title,
		})
	}

	return model.Normalize(state), nil
}

func valueAsString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, x != ""
	case float64:
		return fmt.Sprintf("%.0f", x), true
	case int:
		return fmt.Sprintf("%d", x), true
	default:
		return fmt.Sprint(x), true
	}
}
