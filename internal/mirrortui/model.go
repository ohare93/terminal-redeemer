package mirrortui

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jmo/terminal-redeemer/internal/mirror"
)

const (
	accentColor     = "#8abeb7"
	successColor    = "#b5bd68"
	textColor       = "#d4d4d4"
	mutedColor      = "#808080"
	dimColor        = "#666666"
	warningColor    = "#ffff00"
	errorColor      = "#cc6666"
	selectedBgColor = "#3a3a4a"
)

type semanticRole int

const (
	roleAccent semanticRole = iota
	roleSuccess
	roleText
	roleMuted
	roleDim
	roleWarning
	roleError
)

type renderedLine struct {
	text     string
	role     semanticRole
	selected bool
	active   bool
}

// Model is the Bubble Tea model for choosing one or more discovered sessions.
type Model struct {
	windows   []mirror.Window
	visible   []int
	checked   map[int]bool
	cursor    int
	query     string
	width     int
	height    int
	color     bool
	done      bool
	cancelled bool
}

// NewModel builds a picker model. The color argument is explicit so rendering
// behavior can be verified independently of the process environment.
func NewModel(windows []mirror.Window, color bool) *Model {
	m := &Model{
		windows: append([]mirror.Window(nil), windows...),
		checked: make(map[int]bool),
		width:   80,
		height:  24,
		color:   color,
	}
	m.refilter(-1)
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
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
				current := m.currentIndex()
				m.query = ""
				m.refilter(current)
				return m, nil
			}
			m.cancelled, m.done = true, true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor+1 < len(m.visible) {
				m.cursor++
			}
		case " ":
			if index := m.currentIndex(); index >= 0 {
				m.checked[index] = !m.checked[index]
			}
		case "ctrl+a":
			m.toggleFiltered()
		case "enter":
			if len(m.visible) == 0 {
				return m, nil
			}
			m.done = true
			return m, tea.Quit
		case "backspace":
			if m.query != "" {
				current := m.currentIndex()
				runes := []rune(m.query)
				m.query = string(runes[:len(runes)-1])
				m.refilter(current)
			}
		default:
			if msg.Type == tea.KeyRunes {
				var printable []rune
				for _, r := range msg.Runes {
					if unicode.IsPrint(r) && !unicode.IsControl(r) {
						printable = append(printable, r)
					}
				}
				if len(printable) > 0 {
					current := m.currentIndex()
					m.query += string(printable)
					m.refilter(current)
				}
			}
		}
	}
	return m, nil
}

func (m *Model) currentIndex() int {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return -1
	}
	return m.visible[m.cursor]
}

func (m *Model) refilter(preferred int) {
	needle := strings.ToLower(m.query)
	m.visible = m.visible[:0]
	for i, window := range m.windows {
		cwd := ""
		if window.Terminal != nil {
			cwd = window.Terminal.CWD
		}
		haystack := strings.ToLower(strings.Join([]string{
			mirror.SessionName(window), window.Title, window.WorkspaceName, cwd,
		}, "\x00"))
		if needle == "" || strings.Contains(haystack, needle) {
			m.visible = append(m.visible, i)
		}
	}
	m.cursor = 0
	if preferred >= 0 {
		for i, index := range m.visible {
			if index == preferred {
				m.cursor = i
				break
			}
		}
	}
}

func (m *Model) toggleFiltered() {
	if len(m.visible) == 0 {
		return
	}
	all := true
	for _, index := range m.visible {
		if !m.checked[index] {
			all = false
			break
		}
	}
	for _, index := range m.visible {
		m.checked[index] = !all
	}
}

// Selection returns checked windows in discovery order. If none are checked,
// it returns the focused window, matching the picker's Enter contract.
func (m *Model) Selection() []mirror.Window {
	selected := make([]mirror.Window, 0, len(m.checked))
	for i, window := range m.windows {
		if m.checked[i] {
			selected = append(selected, window)
		}
	}
	if len(selected) == 0 {
		if current := m.currentIndex(); current >= 0 {
			selected = append(selected, m.windows[current])
		}
	}
	return selected
}

func (m *Model) Cancelled() bool { return m.cancelled }

func (m *Model) View() string {
	if m.done {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}

	top := []renderedLine{
		{text: fmt.Sprintf("Mirror sessions  Selected: %d  Matching: %d/%d", m.checkedCount(), len(m.visible), len(m.windows)), role: roleAccent},
		{text: "Filter: " + m.query, role: roleText},
	}
	if width < 72 {
		top = []renderedLine{
			{text: "Mirror sessions", role: roleAccent},
			{text: fmt.Sprintf("Selected: %d  Matching: %d/%d", m.checkedCount(), len(m.visible), len(m.windows)), role: roleMuted},
			{text: "Filter: " + m.query, role: roleText},
		}
	} else if len(m.visible) > 0 {
		top = append(top, renderedLine{text: columnHeading(width), role: roleMuted})
	}
	body := m.bodyLines(width)
	footer := renderedLine{text: "↑/↓ move  Space select  Ctrl+A matches  Enter open  Esc clear/cancel  * source focus", role: roleDim}

	bodyCapacity := len(body)
	showFooter := true
	if m.height > 0 {
		if len(body) > 0 {
			maxTop := m.height - 1
			if maxTop < 0 {
				maxTop = 0
			}
			if len(top) > maxTop {
				top = top[:maxTop]
			}
		}
		bodyCapacity = m.height - len(top) - 1
		if bodyCapacity < 1 && len(body) > 0 {
			showFooter = false
			bodyCapacity = m.height - len(top)
		}
		if bodyCapacity < 0 {
			bodyCapacity = 0
		}
	}
	body = viewport(body, bodyCapacity)

	lines := append([]renderedLine(nil), top...)
	lines = append(lines, body...)
	if showFooter && (m.height <= 0 || len(lines) < m.height) {
		lines = append(lines, footer)
	}
	if m.height > 0 && len(lines) > m.height {
		lines = lines[:m.height]
	}

	var out strings.Builder
	for _, line := range lines {
		plain := fit(line.text, width)
		out.WriteString(m.paint(plain, line.role, line.selected))
		out.WriteByte('\n')
	}
	return out.String()
}

func (m *Model) bodyLines(width int) []renderedLine {
	if len(m.visible) == 0 {
		if len(m.windows) == 0 {
			return []renderedLine{{text: "No sessions are available.", role: roleError, active: true}}
		}
		return []renderedLine{{text: "No sessions match the current filter.", role: roleWarning, active: true}}
	}

	visible := make(map[int]bool, len(m.visible))
	for _, index := range m.visible {
		visible[index] = true
	}
	groupOrder := make([]string, 0)
	seenGroup := make(map[string]bool)
	for i, window := range m.windows {
		if !visible[i] {
			continue
		}
		key := strings.TrimSpace(window.WorkspaceName)
		if !seenGroup[key] {
			seenGroup[key] = true
			groupOrder = append(groupOrder, key)
		}
	}

	active := m.currentIndex()
	lines := make([]renderedLine, 0, len(m.visible)*2+len(groupOrder))
	for _, group := range groupOrder {
		if group != "" || len(groupOrder) > 1 {
			heading := group
			if heading == "" {
				heading = "(unnamed)"
			}
			lines = append(lines, renderedLine{text: "  Workspace: " + heading, role: roleAccent})
		}
		for i, window := range m.windows {
			if !visible[i] || strings.TrimSpace(window.WorkspaceName) != group {
				continue
			}
			lines = append(lines, m.windowLines(i, window, width, i == active)...)
		}
	}
	return lines
}

func (m *Model) windowLines(index int, window mirror.Window, width int, active bool) []renderedLine {
	cursor := "  "
	if active {
		cursor = "> "
	}
	check := "[ ] "
	role := roleText
	if m.checked[index] {
		check = "[x] "
		role = roleSuccess
	}
	focus := "  "
	if window.IsFocused {
		focus = "* "
	}
	prefix := cursor + check + focus
	session := mirror.SessionName(window)
	activity := displayActivity(window)
	project := projectDirectory(window)

	if width >= 72 {
		projectWidth, activityWidth, sessionWidth := columnWidths(width)
		line := prefix + padPath(project, projectWidth) + "  " + padCell(activity, activityWidth) + "  " + fit(session, sessionWidth)
		return []renderedLine{{text: line, role: role, selected: active, active: active}}
	}

	projectWidth := width - ansi.StringWidth(prefix)
	lines := []renderedLine{{text: prefix + fitPath(project, projectWidth), role: role, selected: active, active: active}}
	indent := strings.Repeat(" ", ansi.StringWidth(prefix))
	if activity != "" {
		lines = append(lines, renderedLine{text: indent + "activity: " + activity, role: roleDim, selected: active, active: active})
	}
	if strings.TrimSpace(session) != "" {
		lines = append(lines, renderedLine{text: indent + "session: " + session, role: roleDim, selected: active, active: active})
	}
	return lines
}

func projectDirectory(window mirror.Window) string {
	if window.Terminal == nil || strings.TrimSpace(window.Terminal.CWD) == "" {
		return "(unknown)"
	}
	return shortenHome(window.Terminal.CWD)
}

func displayActivity(window mirror.Window) string {
	title := strings.TrimSpace(window.Title)
	session := strings.TrimSpace(mirror.SessionName(window))
	if title == session {
		return ""
	}
	if session != "" {
		title = strings.TrimPrefix(title, session+" | ")
	}
	return strings.TrimSpace(title)
}

func columnWidths(width int) (project, activity, session int) {
	available := width - rowPrefixWidth - 4
	session = available / 4
	if session > 24 {
		session = 24
	}
	project = (available - session) * 55 / 100
	activity = available - session - project
	return project, activity, session
}

const rowPrefixWidth = 8

func columnHeading(width int) string {
	project, activity, session := columnWidths(width)
	return strings.Repeat(" ", rowPrefixWidth) + padCell("PROJECT / DIRECTORY", project) + "  " + padCell("ACTIVITY", activity) + "  " + fit("SESSION", session)
}

func (m *Model) checkedCount() int {
	count := 0
	for _, checked := range m.checked {
		if checked {
			count++
		}
	}
	return count
}

func viewport(lines []renderedLine, capacity int) []renderedLine {
	if capacity <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= capacity {
		return lines
	}
	activeStart, activeEnd := 0, 1
	found := false
	for i, line := range lines {
		if line.active {
			if !found {
				activeStart = i
				found = true
			}
			activeEnd = i + 1
		} else if found {
			break
		}
	}
	if activeEnd-activeStart > capacity {
		return lines[activeStart : activeStart+capacity]
	}
	start := activeEnd - capacity
	if start < 0 {
		start = 0
	}
	if start > activeStart {
		start = activeStart
	}
	end := start + capacity
	if end < activeEnd {
		end = activeEnd
		start = end - capacity
	}
	if end > len(lines) {
		end = len(lines)
		start = end - capacity
	}
	return lines[start:end]
}

func shortenHome(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return cwd
	}
	if cwd == home {
		return "~"
	}
	if strings.HasPrefix(cwd, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(cwd, home)
	}
	return cwd
}

func fit(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func fitPath(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}

	available := width - ansi.StringWidth("…")
	start := len(value)
	for index := range value {
		if ansi.StringWidth(value[index:]) <= available {
			start = index
			break
		}
	}
	return "…" + value[start:]
}

func padCell(value string, width int) string {
	value = fit(value, width)
	if padding := width - ansi.StringWidth(value); padding > 0 {
		value += strings.Repeat(" ", padding)
	}
	return value
}

func padPath(value string, width int) string {
	value = fitPath(value, width)
	if padding := width - ansi.StringWidth(value); padding > 0 {
		value += strings.Repeat(" ", padding)
	}
	return value
}

func (m *Model) paint(value string, role semanticRole, selected bool) string {
	if !m.color || value == "" {
		return value
	}
	foreground := textColor
	switch role {
	case roleAccent:
		foreground = accentColor
	case roleSuccess:
		foreground = successColor
	case roleMuted:
		foreground = mutedColor
	case roleDim:
		foreground = dimColor
	case roleWarning:
		foreground = warningColor
	case roleError:
		foreground = errorColor
	}
	sequence := foregroundANSI(foreground)
	if selected {
		sequence += backgroundANSI(selectedBgColor)
	}
	return sequence + value + "\x1b[0m"
}

func foregroundANSI(hex string) string { return colorANSI("38", hex) }
func backgroundANSI(hex string) string { return colorANSI("48", hex) }

func colorANSI(layer, hex string) string {
	var red, green, blue int
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &red, &green, &blue)
	return fmt.Sprintf("\x1b[%s;2;%d;%d;%dm", layer, red, green, blue)
}
