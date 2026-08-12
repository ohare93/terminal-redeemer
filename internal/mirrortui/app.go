package mirrortui

import (
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jmo/terminal-redeemer/internal/mirror"
)

// Run opens the interactive picker. Cancellation is a successful outcome with
// cancelled set, allowing callers to launch nothing and exit cleanly.
func Run(windows []mirror.Window) ([]mirror.Window, bool, error) {
	if len(windows) == 0 {
		return nil, false, errors.New("mirror picker requires at least one session")
	}
	final, err := tea.NewProgram(NewModel(windows, colorEnabled()), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, false, err
	}
	model, ok := final.(*Model)
	if !ok {
		return nil, false, errors.New("mirror picker returned an unexpected model")
	}
	if model.Cancelled() {
		return nil, true, nil
	}
	return model.Selection(), false, nil
}

func colorEnabled() bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	return !noColor
}
