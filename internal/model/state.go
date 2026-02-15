package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type State struct {
	Workspaces []Workspace `json:"workspaces"`
	Windows    []Window    `json:"windows"`
}

type Workspace struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Name  string `json:"name,omitempty"`
}

type Window struct {
	Key         string    `json:"key"`
	AppID       string    `json:"app_id"`
	WorkspaceID string    `json:"workspace_id"`
	Title       string    `json:"title,omitempty"`
	Terminal    *Terminal `json:"terminal,omitempty"`
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
		if out.Windows[i].Terminal == nil {
			continue
		}
		term := *out.Windows[i].Terminal
		if len(term.ProcessTags) > 0 {
			term.ProcessTags = append([]string(nil), term.ProcessTags...)
			sort.Strings(term.ProcessTags)
		}
		out.Windows[i].Terminal = &term
	}

	return out
}

func (s State) Hash() (string, error) {
	norm := Normalize(s)
	payload, err := json.Marshal(norm)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
