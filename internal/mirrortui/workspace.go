package mirrortui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jmo/terminal-redeemer/internal/mirror"
)

type WorkspaceModel struct {
	choices   []mirror.WorkspaceChoice
	visible   []int
	cursor    int
	query     string
	width     int
	height    int
	color     bool
	done      bool
	cancelled bool
}

func NewWorkspaceModel(choices []mirror.WorkspaceChoice, color bool) *WorkspaceModel {
	model := &WorkspaceModel{choices: append([]mirror.WorkspaceChoice(nil), choices...), width: 80, height: 24, color: color}
	model.refilter()
	return model
}

func (m *WorkspaceModel) Init() tea.Cmd { return nil }

func (m *WorkspaceModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancelled, m.done = true, true
			return m, tea.Quit
		case "esc":
			if m.query != "" {
				m.query = ""
				m.refilter()
				return m, nil
			}
			m.cancelled, m.done = true, true
			return m, tea.Quit
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor+1 < len(m.visible) {
				m.cursor++
			}
		case "enter":
			if len(m.visible) > 0 {
				m.done = true
				return m, tea.Quit
			}
		case "backspace":
			if m.query != "" {
				runes := []rune(m.query)
				m.query = string(runes[:len(runes)-1])
				m.refilter()
			}
		default:
			if msg.Type == tea.KeyRunes {
				for _, r := range msg.Runes {
					if unicode.IsPrint(r) && !unicode.IsControl(r) {
						m.query += string(r)
					}
				}
				m.refilter()
			}
		}
	}
	return m, nil
}

func (m *WorkspaceModel) refilter() {
	needle := strings.ToLower(m.query)
	m.visible = m.visible[:0]
	for i, choice := range m.choices {
		label := workspaceChoiceLabel(choice.Workspace)
		haystack := strings.ToLower(fmt.Sprintf("%s %d %s", label, choice.Workspace.Index, choice.Workspace.Output))
		if needle == "" || strings.Contains(haystack, needle) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *WorkspaceModel) Selection() (mirror.WorkspaceChoice, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return mirror.WorkspaceChoice{}, false
	}
	return m.choices[m.visible[m.cursor]], true
}

func (m *WorkspaceModel) Cancelled() bool { return m.cancelled }

func (m *WorkspaceModel) View() string {
	if m.done {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	lines := []string{
		styleText("Mirror workspace follow", accentColor, m.color),
		fit("Filter: "+m.query, width),
	}
	capacity := m.height - 3
	if m.height <= 0 {
		capacity = len(m.visible)
	}
	if capacity < 0 {
		capacity = 0
	}
	start := 0
	if m.cursor >= capacity && capacity > 0 {
		start = m.cursor - capacity + 1
	}
	end := start + capacity
	if end > len(m.visible) {
		end = len(m.visible)
	}
	if len(m.visible) == 0 {
		lines = append(lines, fit("No workspaces match the current filter.", width))
	} else {
		for row := start; row < end; row++ {
			choice := m.choices[m.visible[row]]
			cursor := "  "
			if row == m.cursor {
				cursor = "> "
			}
			label := workspaceChoiceLabel(choice.Workspace)
			text := fmt.Sprintf("%s%s  %d eligible / %d terminals", cursor, label, choice.EligibleSessions, choice.VisibleTerminals)
			text = fit(text, width)
			if row == m.cursor && m.color {
				text = backgroundANSI(selectedBgColor) + foregroundANSI(textColor) + padCell(text, width) + "\x1b[0m"
			}
			lines = append(lines, text)
		}
	}
	if m.height <= 0 || len(lines) < m.height {
		lines = append(lines, fit("↑/↓ move  Enter follow  Esc clear/cancel  j/k filter", width))
	}
	if m.height > 0 && len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func workspaceChoiceLabel(workspace mirror.Workspace) string {
	if strings.TrimSpace(workspace.Name) != "" {
		return fmt.Sprintf("%s (#%d)", workspace.Name, workspace.Index)
	}
	return fmt.Sprintf("Workspace %d", workspace.Index)
}

func styleText(value, color string, enabled bool) string {
	if !enabled {
		return value
	}
	return foregroundANSI(color) + value + "\x1b[0m"
}

func RunWorkspace(choices []mirror.WorkspaceChoice) (mirror.WorkspaceChoice, bool, error) {
	return RunWorkspaceContext(context.Background(), choices)
}

func RunWorkspaceContext(ctx context.Context, choices []mirror.WorkspaceChoice) (mirror.WorkspaceChoice, bool, error) {
	if len(choices) == 0 {
		return mirror.WorkspaceChoice{}, false, errors.New("source has no selectable workspaces")
	}
	final, err := tea.NewProgram(NewWorkspaceModel(choices, colorEnabled()), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return mirror.WorkspaceChoice{}, false, err
	}
	model, ok := final.(*WorkspaceModel)
	if !ok {
		return mirror.WorkspaceChoice{}, false, errors.New("workspace picker returned an unexpected model")
	}
	if model.Cancelled() {
		return mirror.WorkspaceChoice{}, true, nil
	}
	choice, ok := model.Selection()
	if !ok {
		return mirror.WorkspaceChoice{}, false, errors.New("workspace picker returned no selection")
	}
	return choice, false, nil
}

type FollowPollFunc func(context.Context) (mirror.FollowPollResult, time.Duration)

type followPollMsg struct {
	result mirror.FollowPollResult
	delay  time.Duration
}

type followTickMsg struct{}

type FollowStatusModel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	poll      FollowPollFunc
	label     string
	limits    string
	width     int
	height    int
	last      mirror.FollowPollResult
	polling   bool
	cancelled bool
}

func NewFollowStatusModel(ctx context.Context, cancel context.CancelFunc, label, limits string, poll FollowPollFunc) *FollowStatusModel {
	return &FollowStatusModel{ctx: ctx, cancel: cancel, poll: poll, label: label, limits: limits, width: 80, height: 12, polling: true}
}

func (m *FollowStatusModel) Init() tea.Cmd { return m.pollCommand() }

func (m *FollowStatusModel) pollCommand() tea.Cmd {
	return func() tea.Msg {
		result, delay := m.poll(m.ctx)
		return followPollMsg{result: result, delay: delay}
	}
}

func (m *FollowStatusModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.cancelled = true
			m.cancel()
			return m, tea.Quit
		}
	case followPollMsg:
		m.last = msg.result
		m.polling = false
		if m.ctx.Err() != nil {
			return m, tea.Quit
		}
		delay := msg.delay
		if delay < mirror.MinimumFollowInterval {
			delay = mirror.MinimumFollowInterval
		}
		return m, tea.Tick(delay, func(time.Time) tea.Msg { return followTickMsg{} })
	case followTickMsg:
		if m.ctx.Err() != nil {
			return m, tea.Quit
		}
		m.polling = true
		return m, m.pollCommand()
	}
	return m, nil
}

func (m *FollowStatusModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	state := "connected"
	if !m.last.Healthy {
		state = "degraded/disconnected"
	}
	if m.polling {
		state += " · polling"
	}
	lines := []string{
		fit("Following "+m.label, width),
		fit("Status: "+state, width),
		fit(fmt.Sprintf("eligible=%d existing=%d opened=%d deferred=%d total_opened=%d", m.last.Eligible, m.last.Existing, m.last.Opened, m.last.Deferred, m.last.Total), width),
		fit("Safeguards: "+m.limits, width),
	}
	if m.last.Reason != "" {
		lines = append(lines, fit("Detail: "+m.last.Reason, width))
	}
	for _, item := range m.last.Items {
		line := fmt.Sprintf("%s: %s", item.Session, item.Status)
		if item.Reason != "" {
			line += " · " + item.Reason
		}
		lines = append(lines, fit(line, width))
	}
	lines = append(lines, fit("q/Ctrl+C stop · additions only; existing windows are never moved or closed", width))
	if m.height > 0 && len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		if ansi.StringWidth(lines[i]) > width {
			lines[i] = fit(lines[i], width)
		}
	}
	return strings.Join(lines, "\n")
}

func RunFollowStatus(ctx context.Context, label, limits string, poll FollowPollFunc) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := NewFollowStatusModel(runCtx, cancel, label, limits, poll)
	_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(runCtx)).Run()
	if err != nil && runCtx.Err() == nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
