package replay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

type Engine struct {
	eventsPath string
	snapshots  *snapshots.Store
}

func NewEngine(root string) (*Engine, error) {
	snapshotStore, err := snapshots.NewStore(root)
	if err != nil {
		return nil, err
	}
	return &Engine{
		eventsPath: filepath.Join(root, "events.jsonl"),
		snapshots:  snapshotStore,
	}, nil
}

func (e *Engine) At(at time.Time) (model.State, error) {
	state := model.State{}
	cursor := int64(0)

	snapshot, _, err := e.snapshots.LoadNearest(at)
	if err == nil {
		state = decodeSnapshotState(snapshot)
		cursor = snapshot.LastEventOffset
	} else if !errors.Is(err, snapshots.ErrNoSnapshot) {
		return model.State{}, err
	}

	f, err := os.Open(e.eventsPath)
	if errors.Is(err, os.ErrNotExist) {
		return model.Normalize(state), nil
	}
	if err != nil {
		return model.State{}, fmt.Errorf("open events file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err := f.Seek(cursor, io.SeekStart); err != nil {
		return model.State{}, fmt.Errorf("seek events cursor: %w", err)
	}

	windowsByKey := make(map[string]model.Window)
	for _, window := range state.Windows {
		windowsByKey[window.Key] = window
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if err := event.Validate(); err != nil {
			continue
		}
		if event.TS.After(at) {
			continue
		}
		switch event.EventType {
		case "window_patch":
			applyWindowPatch(windowsByKey, event.WindowKey, event.Patch)
		case "state_full":
			state = decodeEventState(event.State)
			windowsByKey = make(map[string]model.Window, len(state.Windows))
			for _, window := range state.Windows {
				windowsByKey[window.Key] = window
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return model.State{}, fmt.Errorf("scan events: %w", err)
	}

	state.Windows = make([]model.Window, 0, len(windowsByKey))
	for _, window := range windowsByKey {
		state.Windows = append(state.Windows, window)
	}

	return model.Normalize(state), nil
}

func decodeEventState(raw map[string]any) model.State {
	if raw == nil {
		return model.State{}
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return model.State{}
	}
	var state model.State
	if err := json.Unmarshal(payload, &state); err != nil {
		return model.State{}
	}
	return state
}

func decodeSnapshotState(snapshot snapshots.Snapshot) model.State {
	payload, err := json.Marshal(snapshot.State)
	if err != nil {
		return model.State{}
	}
	var state model.State
	if err := json.Unmarshal(payload, &state); err != nil {
		return model.State{}
	}
	return state
}

func applyWindowPatch(windows map[string]model.Window, key string, patch map[string]any) {
	if patch == nil {
		return
	}
	if deleted, ok := patch["deleted"].(bool); ok && deleted {
		delete(windows, key)
		return
	}

	window := windows[key]
	window.Key = key

	if appID, ok := patch["app_id"].(string); ok {
		window.AppID = appID
	}
	if workspaceID, ok := patch["workspace_id"].(string); ok {
		window.WorkspaceID = workspaceID
	}
	if title, ok := patch["title"].(string); ok {
		window.Title = title
	}
	if pid, ok := patch["pid"].(float64); ok {
		window.PID = int(pid)
	}
	if terminalRaw, ok := patch["terminal"]; ok {
		if terminalRaw == nil {
			window.Terminal = nil
		} else {
			window.Terminal = decodeTerminal(terminalRaw)
		}
	}

	windows[key] = window
}

func decodeTerminal(raw any) *model.Terminal {
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var terminal model.Terminal
	if err := json.Unmarshal(payload, &terminal); err != nil {
		return nil
	}
	if terminal.CWD == "" && terminal.SessionTag == "" && len(terminal.ProcessTags) == 0 {
		return nil
	}
	sort.Strings(terminal.ProcessTags)
	return &terminal
}
