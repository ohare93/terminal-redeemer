package tui

import (
	"fmt"
	"sort"
	"strings"
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
	itemsByApp map[string][]string
	itemByKey  map[string]restore.Item
}

type SelectionState int

const (
	SelectionUnavailable SelectionState = iota
	SelectionNone
	SelectionPartial
	SelectionAll
)

func NewModel(plan restore.Plan, timestamps []time.Time) *Model {
	m := &Model{
		plan:       plan,
		timestamps: append([]time.Time(nil), timestamps...),
		mode:       ModeTimestamp,
		selected:   make(map[string]bool, len(plan.Items)),
		itemsByWS:  make(map[string][]string),
		itemsByApp: make(map[string][]string),
		itemByKey:  make(map[string]restore.Item, len(plan.Items)),
	}
	m.setPlan(plan)
	return m
}

func (m *Model) setPlan(plan restore.Plan) {
	m.plan = plan
	m.selected = make(map[string]bool, len(plan.Items))
	m.itemsByWS = make(map[string][]string)
	m.itemsByApp = make(map[string][]string)
	m.itemByKey = make(map[string]restore.Item, len(plan.Items))

	for _, item := range plan.Items {
		m.itemByKey[item.WindowKey] = item
		if item.Status == restore.StatusReady {
			m.selected[item.WindowKey] = true
		}
		m.itemsByWS[item.WorkspaceID] = append(m.itemsByWS[item.WorkspaceID], item.WindowKey)
		m.itemsByApp[m.appGroupKey(item.WorkspaceID, item.AppID)] = append(m.itemsByApp[m.appGroupKey(item.WorkspaceID, item.AppID)], item.WindowKey)
	}

	for ws := range m.itemsByWS {
		sort.Strings(m.itemsByWS[ws])
	}
	for app := range m.itemsByApp {
		sort.Strings(m.itemsByApp[app])
	}
}

func (m *Model) SetPlan(plan restore.Plan) {
	m.setPlan(plan)
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
	m.toggleByKeys(m.itemsByWS[workspaceID])
}

func (m *Model) ToggleApp(workspaceID string, appID string) {
	m.toggleByKeys(m.itemsByApp[m.appGroupKey(workspaceID, appID)])
}

func (m *Model) ToggleWindow(windowKey string) {
	item, ok := m.itemByKey[windowKey]
	if !ok || item.Status != restore.StatusReady {
		return
	}
	m.selected[windowKey] = !m.selected[windowKey]
}

func (m *Model) toggleByKeys(keys []string) {
	if len(keys) == 0 {
		return
	}
	allSelected := true
	hasSelectable := false
	for _, key := range keys {
		item, ok := m.itemByKey[key]
		if !ok || item.Status != restore.StatusReady {
			continue
		}
		hasSelectable = true
		if !m.selected[key] {
			allSelected = false
			break
		}
	}
	if !hasSelectable {
		return
	}
	for _, key := range keys {
		item, ok := m.itemByKey[key]
		if !ok || item.Status != restore.StatusReady {
			continue
		}
		m.selected[key] = !allSelected
	}
}

func (m *Model) IsSelected(windowKey string) bool {
	return m.selected[windowKey]
}

func (m *Model) IsSelectable(windowKey string) bool {
	item, ok := m.itemByKey[windowKey]
	return ok && item.Status == restore.StatusReady
}

func (m *Model) WorkspaceIDs() []string {
	out := make([]string, 0, len(m.itemsByWS))
	for ws := range m.itemsByWS {
		out = append(out, ws)
	}
	sort.Strings(out)
	return out
}

func (m *Model) AppIDs(workspaceID string) []string {
	seen := make(map[string]struct{})
	for _, key := range m.itemsByWS[workspaceID] {
		item, ok := m.itemByKey[key]
		if !ok {
			continue
		}
		seen[item.AppID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for appID := range seen {
		out = append(out, appID)
	}
	sort.Strings(out)
	return out
}

func (m *Model) WindowKeys(workspaceID string, appID string) []string {
	return append([]string(nil), m.itemsByApp[m.appGroupKey(workspaceID, appID)]...)
}

func (m *Model) WorkspaceSelectionState(workspaceID string) SelectionState {
	return m.selectionStateForKeys(m.itemsByWS[workspaceID])
}

func (m *Model) AppSelectionState(workspaceID string, appID string) SelectionState {
	return m.selectionStateForKeys(m.itemsByApp[m.appGroupKey(workspaceID, appID)])
}

func (m *Model) WindowSelectionState(windowKey string) SelectionState {
	if !m.IsSelectable(windowKey) {
		return SelectionUnavailable
	}
	if m.IsSelected(windowKey) {
		return SelectionAll
	}
	return SelectionNone
}

func (m *Model) selectionStateForKeys(keys []string) SelectionState {
	if len(keys) == 0 {
		return SelectionUnavailable
	}
	selectable := 0
	selected := 0
	for _, key := range keys {
		item, ok := m.itemByKey[key]
		if !ok || item.Status != restore.StatusReady {
			continue
		}
		selectable++
		if m.selected[key] {
			selected++
		}
	}
	if selectable == 0 {
		return SelectionUnavailable
	}
	if selected == 0 {
		return SelectionNone
	}
	if selected == selectable {
		return SelectionAll
	}
	return SelectionPartial
}

func (m *Model) Item(windowKey string) (restore.Item, bool) {
	item, ok := m.itemByKey[windowKey]
	return item, ok
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
	filtered := FilterPlan(m.plan, m.SelectedMap())
	for _, item := range filtered.Items {
		switch item.Status {
		case restore.StatusReady:
			lines = append(lines, fmt.Sprintf("%s ready: %s", item.WindowKey, item.Command))
		default:
			reason := strings.TrimSpace(item.Reason)
			if reason == "" {
				reason = "no reason"
			}
			lines = append(lines, fmt.Sprintf("%s %s: %s", item.WindowKey, item.Status, reason))
		}
	}
	sort.Strings(lines)
	return lines
}

func (m *Model) appGroupKey(workspaceID string, appID string) string {
	return workspaceID + "\x00" + appID
}
