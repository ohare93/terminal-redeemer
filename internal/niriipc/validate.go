package niriipc

import (
	"errors"
	"fmt"
	"math"

	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
)

func Validate(state State) error {
	if len(state.Outputs) > 1 {
		return reason(sliceprotocol.ReasonNiriUnsupportedTopology, errors.New("MVP supports at most one output"))
	}
	headless := len(state.Outputs) == 0
	workspaceIDs := make(map[uint64]Workspace, len(state.Workspaces))
	activeOutputs := map[string]struct{}{}
	allOutputs := map[string]struct{}{}
	for _, workspace := range state.Workspaces {
		if workspace.ID == 0 {
			return reason(sliceprotocol.ReasonNiriMalformed, errors.New("workspace ID must be positive"))
		}
		if _, found := workspaceIDs[workspace.ID]; found {
			return reason(sliceprotocol.ReasonNiriMalformed, errors.New("duplicate workspace ID"))
		}
		if headless {
			if workspace.Output != nil {
				return reason(sliceprotocol.ReasonNiriMissingOutput, fmt.Errorf("headless workspace %d retains an output reference", workspace.ID))
			}
		} else {
			if workspace.Output == nil || *workspace.Output == "" {
				return reason(sliceprotocol.ReasonNiriMissingOutput, fmt.Errorf("workspace %d has no output", workspace.ID))
			}
			if _, found := state.Outputs[*workspace.Output]; !found {
				return reason(sliceprotocol.ReasonNiriMissingOutput, fmt.Errorf("workspace %d references missing output", workspace.ID))
			}
			allOutputs[*workspace.Output] = struct{}{}
			if workspace.IsActive {
				activeOutputs[*workspace.Output] = struct{}{}
			}
		}
		workspaceIDs[workspace.ID] = workspace
	}
	if !headless {
		if len(activeOutputs) != 1 || len(allOutputs) != 1 {
			return reason(sliceprotocol.ReasonNiriUnsupportedTopology, errors.New("MVP requires one active output"))
		}
		for name, output := range state.Outputs {
			if _, used := allOutputs[name]; !used {
				return reason(sliceprotocol.ReasonNiriUnsupportedTopology, fmt.Errorf("output %s has no workspace authority", name))
			}
			if output.Logical.Width <= 0 || output.Logical.Height <= 0 || output.Logical.Scale <= 0 || math.IsNaN(output.Logical.Scale) || math.IsInf(output.Logical.Scale, 0) {
				return reason(sliceprotocol.ReasonNiriInvalidGeometry, fmt.Errorf("output %s has invalid logical geometry", name))
			}
		}
	}
	windowIDs := map[uint64]struct{}{}
	for _, window := range state.Windows {
		if window.ID == 0 {
			return reason(sliceprotocol.ReasonNiriMalformed, errors.New("window ID must be positive"))
		}
		if _, found := windowIDs[window.ID]; found {
			return reason(sliceprotocol.ReasonNiriMalformed, errors.New("duplicate window ID"))
		}
		windowIDs[window.ID] = struct{}{}
		if window.WorkspaceID == nil {
			return reason(sliceprotocol.ReasonNiriMissingWorkspace, fmt.Errorf("window %d has no workspace", window.ID))
		}
		workspace, found := workspaceIDs[*window.WorkspaceID]
		if !found {
			return reason(sliceprotocol.ReasonNiriMissingWorkspace, fmt.Errorf("window %d references missing workspace", window.ID))
		}
		if !headless {
			if _, ok := activeOutputs[*workspace.Output]; !ok {
				return reason(sliceprotocol.ReasonNiriUnsupportedTopology, fmt.Errorf("window %d is outside active output", window.ID))
			}
		}
		if len(window.Layout.TileSize) != 2 || len(window.Layout.WindowSize) != 2 || window.Layout.TileSize[0] < 0 || window.Layout.TileSize[1] < 0 || window.Layout.WindowSize[0] <= 0 || window.Layout.WindowSize[1] <= 0 {
			return reason(sliceprotocol.ReasonNiriInvalidGeometry, fmt.Errorf("window %d has invalid layout size", window.ID))
		}
		for _, value := range window.Layout.TileSize {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return reason(sliceprotocol.ReasonNiriInvalidGeometry, fmt.Errorf("window %d has non-finite layout", window.ID))
			}
		}
		if window.IsFloating {
			if len(window.Layout.Position) != 0 {
				return reason(sliceprotocol.ReasonNiriInvalidGeometry, fmt.Errorf("floating window %d has tiled position", window.ID))
			}
		} else if len(window.Layout.Position) != 2 || window.Layout.Position[0] <= 0 || window.Layout.Position[1] <= 0 {
			return reason(sliceprotocol.ReasonNiriInvalidGeometry, fmt.Errorf("tiled window %d has invalid position", window.ID))
		}
	}
	return nil
}
