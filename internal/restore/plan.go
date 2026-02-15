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
)

type PlannerConfig struct {
	AppAllowlist map[string]string
	Terminal     TerminalConfig
}

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
	return &Planner{config: config}
}

type Plan struct {
	Items []Item
}

type Item struct {
	WindowKey string
	Status    Status
	Reason    string
	Command   string
}

func (p *Planner) Build(state model.State) Plan {
	plan := Plan{Items: make([]Item, 0, len(state.Windows))}
	for _, window := range state.Windows {
		item := Item{WindowKey: window.Key}
		if isTerminal(window.AppID) {
			item = p.planTerminal(window)
		} else {
			item = p.planApp(window)
		}
		plan.Items = append(plan.Items, item)
	}
	return plan
}

func (p *Planner) planTerminal(window model.Window) Item {
	item := Item{WindowKey: window.Key}
	if window.Terminal == nil || strings.TrimSpace(window.Terminal.CWD) == "" {
		item.Status = StatusSkipped
		item.Reason = "missing terminal metadata"
		return item
	}

	command := fmt.Sprintf("%s --directory %q", p.config.Terminal.Command, window.Terminal.CWD)
	if p.config.Terminal.ZellijAttachOrCreate && strings.TrimSpace(window.Terminal.SessionTag) != "" {
		command = fmt.Sprintf("%s -e sh -lc %q", p.config.Terminal.Command, fmt.Sprintf("zellij attach %s || zellij -s %s", window.Terminal.SessionTag, window.Terminal.SessionTag))
	}

	item.Status = StatusReady
	item.Command = command
	return item
}

func (p *Planner) planApp(window model.Window) Item {
	item := Item{WindowKey: window.Key}
	command, ok := p.config.AppAllowlist[window.AppID]
	if !ok {
		item.Status = StatusSkipped
		item.Reason = "app not allowlisted"
		return item
	}
	item.Status = StatusReady
	item.Command = command
	return item
}

func isTerminal(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "kitty", "alacritty", "foot", "wezterm":
		return true
	default:
		return false
	}
}
