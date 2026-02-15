package tui

import (
	"fmt"
	"sort"
	"time"

	"github.com/jmo/terminal-redeemer/internal/restore"
)

type Mode int

const (
	ModeTimestamp Mode = iota
	ModeItems
	ModeConfirm
)

type Model struct {
	plan       restore.Plan
	timestamps []time.Time
	tsIndex    int
	mode       Mode
	selected   map[string]bool
	itemsByWS  map[string][]string
}

func NewModel(plan restore.Plan, timestamps []time.Time) *Model {
	m := &Model{
		plan:       plan,
		timestamps: append([]time.Time(nil), timestamps...),
		mode:       ModeTimestamp,
		selected:   make(map[string]bool, len(plan.Items)),
		itemsByWS:  make(map[string][]string),
	}

	for _, item := range plan.Items {
		if item.Status == restore.StatusReady {
			m.selected[item.WindowKey] = true
		}
		m.itemsByWS[item.WorkspaceID] = append(m.itemsByWS[item.WorkspaceID], item.WindowKey)
	}

	for ws := range m.itemsByWS {
		sort.Strings(m.itemsByWS[ws])
	}

	return m
}

func (m *Model) SetMode(mode Mode) {
	m.mode = mode
}

func (m *Model) SelectedTimestamp() time.Time {
	if len(m.timestamps) == 0 {
		return time.Time{}
	}
	return m.timestamps[m.tsIndex]
}

func (m *Model) NextTimestamp() {
	if len(m.timestamps) == 0 {
		return
	}
	if m.tsIndex < len(m.timestamps)-1 {
		m.tsIndex++
	}
}

func (m *Model) PrevTimestamp() {
	if len(m.timestamps) == 0 {
		return
	}
	if m.tsIndex > 0 {
		m.tsIndex--
	}
}

func (m *Model) ToggleWorkspace(workspaceID string) {
	keys := m.itemsByWS[workspaceID]
	if len(keys) == 0 {
		return
	}
	allSelected := true
	for _, key := range keys {
		if !m.selected[key] {
			allSelected = false
			break
		}
	}
	for _, key := range keys {
		m.selected[key] = !allSelected
	}
}

func (m *Model) ToggleWindow(windowKey string) {
	m.selected[windowKey] = !m.selected[windowKey]
}

func (m *Model) IsSelected(windowKey string) bool {
	return m.selected[windowKey]
}

func (m *Model) SelectedMap() map[string]bool {
	out := make(map[string]bool, len(m.selected))
	for key, selected := range m.selected {
		out[key] = selected
	}
	return out
}

func (m *Model) PreviewLines() []string {
	lines := make([]string, 0)
	for _, item := range m.plan.Items {
		if item.Status != restore.StatusReady {
			continue
		}
		if !m.selected[item.WindowKey] {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", item.WindowKey, item.Command))
	}
	sort.Strings(lines)
	return lines
}
