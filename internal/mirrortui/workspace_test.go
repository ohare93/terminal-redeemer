package mirrortui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jmo/terminal-redeemer/internal/mirror"
)

func workspaceChoices() []mirror.WorkspaceChoice {
	return []mirror.WorkspaceChoice{
		{Workspace: mirror.Workspace{ID: "one", Index: 1, Name: "jupiter"}, EligibleSessions: 2, VisibleTerminals: 3},
		{Workspace: mirror.Workspace{ID: "two", Index: 2, Name: "kite"}, EligibleSessions: 0, VisibleTerminals: 0},
	}
}

func workspaceUpdate(model *WorkspaceModel, key tea.KeyMsg) tea.Cmd {
	_, command := model.Update(key)
	return command
}

func TestWorkspacePickerJKFilterAndArrowNavigation(t *testing.T) {
	model := NewWorkspaceModel(workspaceChoices(), false)
	workspaceUpdate(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if model.query != "j" || len(model.visible) != 1 || model.visible[0] != 0 {
		t.Fatalf("j did not filter: query=%q visible=%v", model.query, model.visible)
	}
	workspaceUpdate(model, tea.KeyMsg{Type: tea.KeyEsc})
	workspaceUpdate(model, tea.KeyMsg{Type: tea.KeyDown})
	choice, ok := model.Selection()
	if !ok || choice.Workspace.ID != "two" {
		t.Fatalf("down did not navigate: %#v ok=%t", choice, ok)
	}
	workspaceUpdate(model, tea.KeyMsg{Type: tea.KeyUp})
	workspaceUpdate(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if model.query != "k" || len(model.visible) != 1 || model.visible[0] != 1 {
		t.Fatalf("k did not filter: query=%q visible=%v", model.query, model.visible)
	}
}

func TestWorkspacePickerNarrowAndNoColorRendering(t *testing.T) {
	model := NewWorkspaceModel(workspaceChoices(), false)
	model.width, model.height = 24, 5
	view := model.View()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR rendering contains ANSI: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 24 {
			t.Fatalf("line width=%d: %q", ansi.StringWidth(line), line)
		}
	}
	if !strings.Contains(view, "jupiter") || strings.Count(view, "\n") >= 5 {
		t.Fatalf("narrow view=%q", view)
	}
}

func TestFollowStatusQAndCtrlCCancelImmediately(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyRunes, tea.KeyCtrlC} {
		ctx, cancel := context.WithCancel(context.Background())
		model := NewFollowStatusModel(ctx, cancel, "Dev", "max=4", func(context.Context) (mirror.FollowPollResult, time.Duration) {
			return mirror.FollowPollResult{Healthy: true}, mirror.MinimumFollowInterval
		})
		message := tea.KeyMsg{Type: key}
		if key == tea.KeyRunes {
			message.Runes = []rune("q")
		}
		_, command := model.Update(message)
		if command == nil || ctx.Err() == nil {
			t.Fatalf("key=%v did not cancel/quit", key)
		}
	}
}

func TestFollowStatusShowsBoundsDeferredAndDegraded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := NewFollowStatusModel(ctx, cancel, "Dev", "interval=5s max-per-poll=4 max-total=64", nil)
	model.width, model.height = 120, 10
	model.last = mirror.FollowPollResult{Healthy: false, Eligible: 9, Existing: 2, Deferred: 7, Total: 4, Reason: "SSH unavailable"}
	view := model.View()
	for _, want := range []string{"degraded/disconnected", "deferred=7", "max-total=64", "SSH unavailable", "q/Ctrl+C"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
}
