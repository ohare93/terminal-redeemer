package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

type State struct {
	Workspaces []Workspace `json:"workspaces"`
	Windows    []Window    `json:"windows"`
}

type Workspace struct {
	ID     string `json:"id"`
	Index  int    `json:"index"`
	Name   string `json:"name,omitempty"`
	Output string `json:"output,omitempty"`
}

type Window struct {
	Key          string        `json:"key"`
	AppID        string        `json:"app_id"`
	WorkspaceID  string        `json:"workspace_id"`
	WorkspaceRef *WorkspaceRef `json:"workspace_ref,omitempty"`
	PID          int           `json:"pid,omitempty"`
	Title        string        `json:"title,omitempty"`
	Placement    *Placement    `json:"placement,omitempty"`
	Terminal     *Terminal     `json:"terminal,omitempty"`
}

// WorkspaceRef contains only cross-boot workspace selectors. WorkspaceID is
// retained separately on Window as source runtime evidence.
type WorkspaceRef struct {
	Name   string `json:"name,omitempty"`
	Output string `json:"output,omitempty"`
	Index  int    `json:"index,omitempty"`
}

// Placement is best-effort Niri layout evidence. Pointer scalars distinguish
// an observed zero/false value from a field absent in older Niri payloads.
type Placement struct {
	Column     *int      `json:"column,omitempty"`
	Row        *int      `json:"row,omitempty"`
	IsFloating *bool     `json:"is_floating,omitempty"`
	TileSize   []float64 `json:"tile_size,omitempty"`
	WindowSize []int     `json:"window_size,omitempty"`
}

// RecoveryInventory is the authoritative active-session projection captured
// alongside compositor state. Sessions contains exactly one metadata entry for
// every name in ActiveSessions.
type RecoveryInventory struct {
	ActiveSessions []string          `json:"active_sessions"`
	Sessions       []RecoverySession `json:"sessions"`
}

type RecoverySession struct {
	Name                string        `json:"name"`
	CWD                 string        `json:"cwd,omitempty"`
	WorkspaceRef        *WorkspaceRef `json:"workspace_ref,omitempty"`
	Placement           *Placement    `json:"placement,omitempty"`
	PlacementObservedAt *time.Time    `json:"placement_observed_at,omitempty"`
	Visible             bool          `json:"visible"`
}

type Terminal struct {
	CWD         string   `json:"cwd,omitempty"`
	ProcessTags []string `json:"process_tags,omitempty"`
	SessionTag  string   `json:"session_tag,omitempty"`
}

func Normalize(s State) State {
	out := State{
		Workspaces: append([]Workspace(nil), s.Workspaces...),
		Windows:    append([]Window(nil), s.Windows...),
	}

	sort.SliceStable(out.Workspaces, func(i, j int) bool {
		if out.Workspaces[i].Index != out.Workspaces[j].Index {
			return out.Workspaces[i].Index < out.Workspaces[j].Index
		}
		return out.Workspaces[i].ID < out.Workspaces[j].ID
	})

	sort.SliceStable(out.Windows, func(i, j int) bool {
		return out.Windows[i].Key < out.Windows[j].Key
	})

	for i := range out.Windows {
		if out.Windows[i].WorkspaceRef != nil {
			ref := *out.Windows[i].WorkspaceRef
			out.Windows[i].WorkspaceRef = &ref
		}
		if out.Windows[i].Placement != nil {
			placement := *out.Windows[i].Placement
			if placement.Column != nil {
				column := *placement.Column
				placement.Column = &column
			}
			if placement.Row != nil {
				row := *placement.Row
				placement.Row = &row
			}
			if placement.IsFloating != nil {
				floating := *placement.IsFloating
				placement.IsFloating = &floating
			}
			placement.TileSize = append([]float64(nil), placement.TileSize...)
			placement.WindowSize = append([]int(nil), placement.WindowSize...)
			out.Windows[i].Placement = &placement
		}
		if out.Windows[i].Terminal != nil {
			term := *out.Windows[i].Terminal
			if len(term.ProcessTags) > 0 {
				term.ProcessTags = append([]string(nil), term.ProcessTags...)
				sort.Strings(term.ProcessTags)
			}
			out.Windows[i].Terminal = &term
		}
	}

	return out
}

func NormalizeRecovery(in RecoveryInventory) RecoveryInventory {
	out := RecoveryInventory{
		ActiveSessions: append([]string(nil), in.ActiveSessions...),
		Sessions:       append([]RecoverySession(nil), in.Sessions...),
	}
	sort.Strings(out.ActiveSessions)
	sort.SliceStable(out.Sessions, func(i, j int) bool { return out.Sessions[i].Name < out.Sessions[j].Name })
	for i := range out.Sessions {
		entry := &out.Sessions[i]
		if entry.WorkspaceRef != nil {
			ref := *entry.WorkspaceRef
			entry.WorkspaceRef = &ref
		}
		if entry.Placement != nil {
			placement := *entry.Placement
			if placement.Column != nil {
				value := *placement.Column
				placement.Column = &value
			}
			if placement.Row != nil {
				value := *placement.Row
				placement.Row = &value
			}
			if placement.IsFloating != nil {
				value := *placement.IsFloating
				placement.IsFloating = &value
			}
			placement.TileSize = append([]float64(nil), placement.TileSize...)
			placement.WindowSize = append([]int(nil), placement.WindowSize...)
			entry.Placement = &placement
		}
		if entry.PlacementObservedAt != nil {
			observed := entry.PlacementObservedAt.UTC()
			entry.PlacementObservedAt = &observed
		}
	}
	return out
}

// Hash returns a semantic checkpoint hash. Window titles are deliberately
// excluded because they are volatile presentation metadata (for example shell
// commands and progress spinners), not resumable window identity or placement.
// Normalize still preserves titles in rolling checkpoints.
func (s State) Hash() (string, error) {
	norm := Normalize(s)
	for i := range norm.Windows {
		norm.Windows[i].Title = ""
	}
	return hashNormalized(norm)
}

// HashWithTitles reproduces the pre-semantic-hash format so rolling
// checkpoints written by older releases remain readable during migration.
// New checkpoints must use Hash.
func (s State) HashWithTitles() (string, error) {
	return hashNormalized(Normalize(s))
}

func hashNormalized(norm State) (string, error) {
	payload, err := json.Marshal(norm)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
