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
	ID     any `json:"id"`
	Index  int `json:"idx"`
	Name   any `json:"name"`
	Output any `json:"output"`
}

type windowPayload struct {
	ID          int            `json:"id"`
	AppID       any            `json:"app_id"`
	Title       string         `json:"title"`
	WorkspaceID any            `json:"workspace_id"`
	PID         int            `json:"pid"`
	IsFloating  *bool          `json:"is_floating"`
	Layout      *layoutPayload `json:"layout"`
}

type layoutPayload struct {
	PosInScrollingLayout []int     `json:"pos_in_scrolling_layout"`
	TileSize             []float64 `json:"tile_size"`
	WindowSize           []int     `json:"window_size"`
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

	workspaceRefs := make(map[string]model.WorkspaceRef, len(payload.Workspaces))
	for _, workspace := range payload.Workspaces {
		workspaceID, _ := valueAsString(workspace.ID)
		workspaceName, _ := valueAsString(workspace.Name)
		workspaceOutput, _ := valueAsString(workspace.Output)
		state.Workspaces = append(state.Workspaces, model.Workspace{
			ID:     workspaceID,
			Index:  workspace.Index,
			Name:   workspaceName,
			Output: workspaceOutput,
		})
		if workspaceID != "" && (workspaceName != "" || workspaceOutput != "" || workspace.Index > 0) {
			workspaceRefs[workspaceID] = model.WorkspaceRef{
				Name:   workspaceName,
				Output: workspaceOutput,
				Index:  workspace.Index,
			}
		}
	}

	for _, window := range payload.Windows {
		appID, _ := valueAsString(window.AppID)
		workspaceID, _ := valueAsString(window.WorkspaceID)
		var workspaceRef *model.WorkspaceRef
		if ref, ok := workspaceRefs[workspaceID]; ok {
			refCopy := ref
			workspaceRef = &refCopy
		}
		state.Windows = append(state.Windows, model.Window{
			Key:          fmt.Sprintf("w:%s:%d", appID, window.ID),
			AppID:        appID,
			WorkspaceID:  workspaceID,
			WorkspaceRef: workspaceRef,
			PID:          window.PID,
			Title:        window.Title,
			Placement:    placementFromPayload(window),
		})
	}

	return model.Normalize(state), nil
}

func placementFromPayload(window windowPayload) *model.Placement {
	placement := &model.Placement{IsFloating: window.IsFloating}
	if window.Layout != nil {
		if len(window.Layout.PosInScrollingLayout) > 0 {
			column := window.Layout.PosInScrollingLayout[0]
			placement.Column = &column
		}
		if len(window.Layout.PosInScrollingLayout) > 1 {
			row := window.Layout.PosInScrollingLayout[1]
			placement.Row = &row
		}
		placement.TileSize = append([]float64(nil), window.Layout.TileSize...)
		placement.WindowSize = append([]int(nil), window.Layout.WindowSize...)
	}
	if placement.Column == nil && placement.IsFloating == nil && len(placement.TileSize) == 0 && len(placement.WindowSize) == 0 {
		return nil
	}
	return placement
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
