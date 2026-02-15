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
	ID    string `json:"id"`
	Index int    `json:"idx"`
	Name  string `json:"name"`
}

type windowPayload struct {
	ID          int    `json:"id"`
	AppID       string `json:"app_id"`
	Title       string `json:"title"`
	WorkspaceID string `json:"workspace_id"`
	PID         int    `json:"pid"`
}

func ParseSnapshot(raw []byte) (model.State, error) {
	var payload snapshotPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return model.State{}, fmt.Errorf("decode niri snapshot: %w", err)
	}

	state := model.State{
		Workspaces: make([]model.Workspace, 0, len(payload.Workspaces)),
		Windows:    make([]model.Window, 0, len(payload.Windows)),
	}

	for _, workspace := range payload.Workspaces {
		state.Workspaces = append(state.Workspaces, model.Workspace{
			ID:    workspace.ID,
			Index: workspace.Index,
			Name:  workspace.Name,
		})
	}

	for _, window := range payload.Windows {
		state.Windows = append(state.Windows, model.Window{
			Key:         fmt.Sprintf("w:%s:%d", window.AppID, window.ID),
			AppID:       window.AppID,
			WorkspaceID: window.WorkspaceID,
			PID:         window.PID,
			Title:       window.Title,
		})
	}

	return model.Normalize(state), nil
}
