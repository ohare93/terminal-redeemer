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

	rows     []row
	cursor   int
	confirm  bool
	quitting bool
	loadPlan func(time.Time) (restore.Plan, error)
	runErr   error
}

type rowKind int

const (
	rowWorkspace rowKind = iota
	rowApp
	rowWindow
)

type row struct {
	kind        rowKind
	workspaceID string
	appID       string
	windowKey   string
}

func NewApp(plan restore.Plan, timestamps []time.Time) *App {
	return NewAppWithPlanLoader(plan, timestamps, nil)
}

func NewAppWithPlanLoader(plan restore.Plan, timestamps []time.Time, loadPlan func(time.Time) (restore.Plan, error)) *App {
	app := &App{model: NewModel(plan, timestamps), loadPlan: loadPlan}
	app.rebuildRows()
	return app
}

func Run(plan restore.Plan, timestamps []time.Time) (restore.Plan, bool, error) {
	return RunWithPlanLoader(plan, timestamps, nil)
}

func RunWithPlanLoader(plan restore.Plan, timestamps []time.Time, loadPlan func(time.Time) (restore.Plan, error)) (restore.Plan, bool, error) {
	app := NewAppWithPlanLoader(plan, timestamps, loadPlan)
	program := tea.NewProgram(app)
	finalModel, err := program.Run()
	if err != nil {
		return restore.Plan{}, false, err
	}
	final, ok := finalModel.(*App)
	if !ok {
		return restore.Plan{}, false, fmt.Errorf("unexpected tui final model type")
	}
	if final.runErr != nil {
		return restore.Plan{}, false, final.runErr
	}
	return FilterPlan(final.model.plan, final.model.SelectedMap()), final.confirm, nil
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
			} else if a.cursor < len(a.rows)-1 {
				a.cursor++
			}
		case "enter":
			switch a.model.mode {
			case ModeTimestamp:
				if err := a.loadPlanForSelection(); err != nil {
					a.runErr = err
					return a, tea.Quit
				}
				a.model.SetMode(ModeItems)
			case ModeItems:
				a.model.SetMode(ModeConfirm)
			case ModeConfirm:
				a.confirm = true
				return a, tea.Quit
			}
		case " ":
			if a.model.mode == ModeItems && len(a.rows) > 0 {
				a.toggleCurrentRow()
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
		fmt.Fprintln(b, "Select items (space=toggle):")
		for i, row := range a.rows {
			prefix := "  "
			if i == a.cursor {
				prefix = "> "
			}
			fmt.Fprintf(b, "%s%s %s\n", prefix, a.selectionMark(row), a.rowLabel(row))
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

func (a *App) rebuildRows() {
	a.rows = a.rows[:0]
	for _, workspaceID := range a.model.WorkspaceIDs() {
		a.rows = append(a.rows, row{kind: rowWorkspace, workspaceID: workspaceID})
		for _, appID := range a.model.AppIDs(workspaceID) {
			a.rows = append(a.rows, row{kind: rowApp, workspaceID: workspaceID, appID: appID})
			for _, windowKey := range a.model.WindowKeys(workspaceID, appID) {
				a.rows = append(a.rows, row{kind: rowWindow, workspaceID: workspaceID, appID: appID, windowKey: windowKey})
			}
		}
	}
	if a.cursor >= len(a.rows) {
		a.cursor = 0
	}
}

func (a *App) toggleCurrentRow() {
	if a.cursor < 0 || a.cursor >= len(a.rows) {
		return
	}
	current := a.rows[a.cursor]
	switch current.kind {
	case rowWorkspace:
		a.model.ToggleWorkspace(current.workspaceID)
	case rowApp:
		a.model.ToggleApp(current.workspaceID, current.appID)
	case rowWindow:
		a.model.ToggleWindow(current.windowKey)
	}
}

func (a *App) loadPlanForSelection() error {
	if a.loadPlan == nil {
		return nil
	}
	plan, err := a.loadPlan(a.model.SelectedTimestamp())
	if err != nil {
		return err
	}
	a.model.SetPlan(plan)
	a.rebuildRows()
	return nil
}

func (a *App) selectionMark(row row) string {
	var state SelectionState
	switch row.kind {
	case rowWorkspace:
		state = a.model.WorkspaceSelectionState(row.workspaceID)
	case rowApp:
		state = a.model.AppSelectionState(row.workspaceID, row.appID)
	default:
		state = a.model.WindowSelectionState(row.windowKey)
	}
	switch state {
	case SelectionAll:
		return "[x]"
	case SelectionPartial:
		return "[-]"
	case SelectionNone:
		return "[ ]"
	default:
		return "[~]"
	}
}

func (a *App) rowLabel(row row) string {
	switch row.kind {
	case rowWorkspace:
		return fmt.Sprintf("workspace %s", row.workspaceID)
	case rowApp:
		return fmt.Sprintf("  app %s", row.appID)
	default:
		item, ok := a.model.Item(row.windowKey)
		if !ok {
			return "    window " + row.windowKey
		}
		if item.Status == restore.StatusReady {
			return fmt.Sprintf("    window %s (%s)", item.WindowKey, item.Status)
		}
		reason := strings.TrimSpace(item.Reason)
		if reason == "" {
			reason = "no reason"
		}
		return fmt.Sprintf("    window %s (%s: %s)", item.WindowKey, item.Status, reason)
	}
}
