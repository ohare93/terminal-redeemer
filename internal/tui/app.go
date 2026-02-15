package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jmo/terminal-redeemer/internal/restore"
)

type App struct {
	model *Model

	keys     []string
	cursor   int
	confirm  bool
	quitting bool
}

func NewApp(plan restore.Plan, timestamps []time.Time) *App {
	app := &App{model: NewModel(plan, timestamps)}
	app.rebuildKeys()
	return app
}

func Run(plan restore.Plan, timestamps []time.Time) (restore.Plan, bool, error) {
	app := NewApp(plan, timestamps)
	program := tea.NewProgram(app)
	finalModel, err := program.Run()
	if err != nil {
		return restore.Plan{}, false, err
	}
	final, ok := finalModel.(*App)
	if !ok {
		return restore.Plan{}, false, fmt.Errorf("unexpected tui final model type")
	}
	return FilterPlan(plan, final.model.SelectedMap()), final.confirm, nil
}

func (a *App) Init() tea.Cmd { return nil }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "q":
			a.quitting = true
			return a, tea.Quit
		case "up", "k":
			if a.model.mode == ModeTimestamp {
				a.model.PrevTimestamp()
			} else if a.cursor > 0 {
				a.cursor--
			}
		case "down", "j":
			if a.model.mode == ModeTimestamp {
				a.model.NextTimestamp()
			} else if a.cursor < len(a.keys)-1 {
				a.cursor++
			}
		case "enter":
			switch a.model.mode {
			case ModeTimestamp:
				a.model.SetMode(ModeItems)
			case ModeItems:
				a.model.SetMode(ModeConfirm)
			case ModeConfirm:
				a.confirm = true
				return a, tea.Quit
			}
		case " ":
			if a.model.mode == ModeItems && len(a.keys) > 0 {
				a.model.ToggleWindow(a.keys[a.cursor])
			}
		case "W":
			if a.model.mode == ModeItems && len(a.keys) > 0 {
				ws := a.workspaceFor(a.keys[a.cursor])
				a.model.ToggleWorkspace(ws)
			}
		case "esc", "n":
			if a.model.mode == ModeConfirm {
				a.confirm = false
				return a, tea.Quit
			}
		case "y":
			if a.model.mode == ModeConfirm {
				a.confirm = true
				return a, tea.Quit
			}
		}
	}
	return a, nil
}

func (a *App) View() string {
	if a.quitting {
		return "cancelled\n"
	}

	b := &strings.Builder{}
	fmt.Fprintln(b, "Restore TUI")
	fmt.Fprintf(b, "Mode: %s\n", a.modeLabel())

	if a.model.mode == ModeTimestamp {
		fmt.Fprintln(b, "Select timestamp:")
		for i, ts := range a.model.timestamps {
			prefix := "  "
			if i == a.model.tsIndex {
				prefix = "> "
			}
			fmt.Fprintf(b, "%s%s\n", prefix, ts.Format(time.RFC3339))
		}
	} else {
		fmt.Fprintln(b, "Select items (space=toggle, W=workspace):")
		for i, key := range a.keys {
			prefix := "  "
			if i == a.cursor {
				prefix = "> "
			}
			mark := "[ ]"
			if a.model.IsSelected(key) {
				mark = "[x]"
			}
			fmt.Fprintf(b, "%s%s %s\n", prefix, mark, key)
		}
		fmt.Fprintln(b, "Preview:")
		for _, line := range a.model.PreviewLines() {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}

	if a.model.mode == ModeConfirm {
		fmt.Fprintln(b, "Confirm apply? (y/n)")
	}

	return b.String()
}

func (a *App) modeLabel() string {
	switch a.model.mode {
	case ModeTimestamp:
		return "timestamp"
	case ModeItems:
		return "items"
	case ModeConfirm:
		return "confirm"
	default:
		return "unknown"
	}
}

func (a *App) rebuildKeys() {
	a.keys = a.keys[:0]
	for _, item := range a.model.plan.Items {
		a.keys = append(a.keys, item.WindowKey)
	}
}

func (a *App) workspaceFor(key string) string {
	for _, item := range a.model.plan.Items {
		if item.WindowKey == key {
			return item.WorkspaceID
		}
	}
	return ""
}
