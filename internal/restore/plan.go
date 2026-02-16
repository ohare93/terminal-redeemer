package restore

import (
	"fmt"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/model"
)

type Status string

const (
	StatusReady    Status = "ready"
	StatusSkipped  Status = "skipped"
	StatusDegraded Status = "degraded"
	StatusFailed   Status = "failed"
)

type PlannerConfig struct {
	AppAllowlist map[string]string
	AppMode      map[string]AppMode
	Terminal     TerminalConfig
}

type AppMode string

const (
	AppModePerWindow AppMode = "per_window"
	AppModeOneShot   AppMode = "oneshot"
)

type TerminalConfig struct {
	Command              string
	ZellijAttachOrCreate bool
}

type Planner struct {
	config PlannerConfig
}

func NewPlanner(config PlannerConfig) *Planner {
	if strings.TrimSpace(config.Terminal.Command) == "" {
		config.Terminal.Command = "kitty"
	}
	normalizedAllowlist := make(map[string]string, len(config.AppAllowlist))
	for appID, command := range config.AppAllowlist {
		normalizedAllowlist[normalizeAppID(appID)] = strings.TrimSpace(command)
	}
	config.AppAllowlist = normalizedAllowlist
	normalizedAppModes := make(map[string]AppMode, len(config.AppMode))
	for appID, mode := range config.AppMode {
		normalized := normalizeAppID(appID)
		if mode == AppModeOneShot {
			normalizedAppModes[normalized] = AppModeOneShot
			continue
		}
		normalizedAppModes[normalized] = AppModePerWindow
	}
	config.AppMode = normalizedAppModes
	return &Planner{config: config}
}

type Plan struct {
	Items []Item
}

type Item struct {
	WindowKey   string
	WorkspaceID string
	AppID       string
	Status      Status
	Reason      string
	Command     string
}

func (p *Planner) Build(state model.State) Plan {
	plan := Plan{Items: make([]Item, 0, len(state.Windows))}
	oneshootSeen := make(map[string]bool)
	for _, window := range state.Windows {
		var item Item
		if isTerminal(window.AppID) {
			item = p.planTerminal(window)
		} else {
			item = p.planApp(window)
			mode := p.appMode(window.AppID)
			if mode == AppModeOneShot && item.Status == StatusReady {
				appID := normalizeAppID(window.AppID)
				if oneshootSeen[appID] {
					item.Status = StatusSkipped
					item.Reason = "oneshot app already scheduled"
					item.Command = ""
				} else {
					oneshootSeen[appID] = true
				}
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan
}

func (p *Planner) appMode(appID string) AppMode {
	mode, ok := p.config.AppMode[normalizeAppID(appID)]
	if !ok {
		return AppModePerWindow
	}
	if mode == AppModeOneShot {
		return AppModeOneShot
	}
	return AppModePerWindow
}

func (p *Planner) planTerminal(window model.Window) Item {
	item := Item{WindowKey: window.Key, WorkspaceID: window.WorkspaceID, AppID: window.AppID}
	if window.Terminal == nil {
		item.Status = StatusSkipped
		item.Reason = "missing terminal metadata"
		return item
	}

	cwd := strings.TrimSpace(window.Terminal.CWD)
	sessionTag := strings.TrimSpace(window.Terminal.SessionTag)
	if cwd == "" && sessionTag == "" {
		item.Status = StatusSkipped
		item.Reason = "missing terminal metadata"
		return item
	}

	command := strings.TrimSpace(p.config.Terminal.Command)
	if cwd != "" {
		command = fmt.Sprintf("%s --directory %q", command, cwd)
	}
	if p.config.Terminal.ZellijAttachOrCreate && sessionTag != "" {
		command = fmt.Sprintf("%s -e sh -lc %q", strings.TrimSpace(p.config.Terminal.Command), fmt.Sprintf("zellij attach %s || zellij -s %s", sessionTag, sessionTag))
	}

	if cwd == "" {
		item.Status = StatusDegraded
		item.Reason = "missing terminal cwd"
		item.Command = command
		return item
	}
	if p.config.Terminal.ZellijAttachOrCreate && sessionTag == "" {
		item.Status = StatusDegraded
		item.Reason = "missing terminal session tag"
		item.Command = command
		return item
	}

	item.Status = StatusReady
	item.Command = command
	return item
}

func (p *Planner) planApp(window model.Window) Item {
	item := Item{WindowKey: window.Key, WorkspaceID: window.WorkspaceID, AppID: window.AppID}
	command, ok := p.config.AppAllowlist[normalizeAppID(window.AppID)]
	if !ok {
		item.Status = StatusSkipped
		item.Reason = "app not allowlisted"
		return item
	}
	if command == "" {
		item.Status = StatusSkipped
		item.Reason = "allowlist command is empty"
		return item
	}
	item.Status = StatusReady
	item.Command = command
	return item
}

func normalizeAppID(appID string) string {
	return strings.ToLower(strings.TrimSpace(appID))
}

func isTerminal(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "kitty", "alacritty", "foot", "wezterm":
		return true
	default:
		return false
	}
}
