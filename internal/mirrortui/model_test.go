package mirrortui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jmo/terminal-redeemer/internal/mirror"
)

func pickerWindows() []mirror.Window {
	return []mirror.Window{
		{Order: 0, SourceWindowID: 1, AppID: "kitty", WorkspaceName: "Dev", Title: "editor", ZellijSession: "alpha", Terminal: &mirror.Terminal{CWD: "/home/test/project-a"}},
		{Order: 1, SourceWindowID: 2, AppID: "kitty", WorkspaceName: "Chat", Title: "messages", ZellijSession: "beta", Terminal: &mirror.Terminal{CWD: "/tmp/chat"}},
		{Order: 2, SourceWindowID: 3, AppID: "kitty", WorkspaceName: "Dev", Title: "alpha", ZellijSession: "gamma", Terminal: &mirror.Terminal{CWD: "/srv/project-b"}},
		{Order: 3, SourceWindowID: 4, AppID: "kitty", Title: "loose", ZellijSession: "delta", Terminal: &mirror.Terminal{CWD: "/tmp/other"}},
	}
}

func key(value string) tea.KeyMsg {
	switch value {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "ctrl+a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func update(m *Model, value string) tea.Cmd {
	_, cmd := m.Update(key(value))
	return cmd
}

func selectedSessions(m *Model) []string {
	var sessions []string
	for _, window := range m.Selection() {
		sessions = append(sessions, mirror.SessionName(window))
	}
	return sessions
}

func TestNavigationSelectionAndDiscoveryOrder(t *testing.T) {
	m := NewModel(pickerWindows(), false)
	if got := m.currentIndex(); got != 0 {
		t.Fatalf("initial current index=%d", got)
	}
	update(m, "down")
	update(m, "space") // beta
	update(m, "up")
	update(m, "space") // alpha
	if cmd := update(m, "enter"); cmd == nil {
		t.Fatal("Enter did not quit")
	}
	if got := strings.Join(selectedSessions(m), ","); got != "alpha,beta" {
		t.Fatalf("selection was not returned in discovery order: %s", got)
	}

	fallback := NewModel(pickerWindows(), false)
	update(fallback, "j")
	update(fallback, "enter")
	if got := strings.Join(selectedSessions(fallback), ","); got != "beta" {
		t.Fatalf("Enter without checks selected %q", got)
	}
}

func TestFilteringCheckedPersistenceAndFilteredToggleAll(t *testing.T) {
	m := NewModel(pickerWindows(), false)
	update(m, "space") // alpha remains checked through filters
	update(m, "project-b")
	if len(m.visible) != 1 || m.visible[0] != 2 {
		t.Fatalf("CWD filter visible=%v", m.visible)
	}
	update(m, "ctrl+a") // gamma
	update(m, "esc")    // clear query, not cancel
	if m.Cancelled() || m.query != "" || len(m.visible) != 4 {
		t.Fatalf("first Esc did not only clear query: cancelled=%t query=%q visible=%v", m.Cancelled(), m.query, m.visible)
	}
	if got := strings.Join(selectedSessions(m), ","); got != "alpha,gamma" {
		t.Fatalf("checks did not survive filtering: %s", got)
	}

	update(m, "DEV")
	if got := m.visible; len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("case-insensitive workspace filter visible=%v", got)
	}
	update(m, "ctrl+a") // both are checked, so remove only filtered checks
	update(m, "esc")
	if m.checkedCount() != 0 {
		t.Fatalf("filtered toggle did not clear filtered checks: %#v", m.checked)
	}
}

func TestFilteringSearchesSessionTitleAndBackspace(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{query: "BETA", want: 1},
		{query: "messages", want: 1},
		{query: "loose", want: 3},
	} {
		m := NewModel(pickerWindows(), false)
		update(m, tc.query)
		if len(m.visible) != 1 || m.visible[0] != tc.want {
			t.Fatalf("filter %q visible=%v", tc.query, m.visible)
		}
		update(m, "backspace")
		if m.query == tc.query {
			t.Fatalf("Backspace did not edit %q", tc.query)
		}
	}
}

func TestGroupingOrderAndUnnamedFallback(t *testing.T) {
	m := NewModel(pickerWindows(), false)
	m.width, m.height = 100, 100
	view := m.View()
	dev := strings.Index(view, "Workspace: Dev")
	alpha := strings.Index(view, "alpha")
	gamma := strings.Index(view, "gamma")
	chat := strings.Index(view, "Workspace: Chat")
	unnamed := strings.Index(view, "Workspace: (unnamed)")
	if dev < 0 || alpha < dev || gamma < alpha || chat < gamma || unnamed < chat {
		t.Fatalf("groups or sessions not in first-discovery order:\n%s", view)
	}

	update(m, "messages")
	view = m.View()
	if !strings.Contains(view, "Workspace: Chat") || strings.Contains(view, "Workspace: Dev") || strings.Contains(view, "Workspace: (unnamed)") {
		t.Fatalf("empty filtered groups were not hidden:\n%s", view)
	}
}

func TestEscapeAndControlCCancel(t *testing.T) {
	m := NewModel(pickerWindows(), false)
	if cmd := update(m, "esc"); cmd == nil || !m.Cancelled() {
		t.Fatal("Esc with an empty query did not cancel")
	}
	m = NewModel(pickerWindows(), false)
	if cmd := update(m, "ctrl+c"); cmd == nil || !m.Cancelled() {
		t.Fatal("Ctrl+C did not cancel")
	}
}

func TestResponsiveRenderingColorNoColorAndViewport(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	windows := pickerWindows()
	windows[0].Title = windows[0].ZellijSession // duplicate title must be elided
	windows[3].ZellijSession = "非常に長いセッション名"

	plain := NewModel(windows, false)
	plain.width, plain.height = 32, 8
	for i := 0; i < 3; i++ {
		update(plain, "down")
	}
	view := plain.View()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR rendering contains ANSI: %q", view)
	}
	if strings.Count(view, "\n") > 8 || !strings.Contains(view, "> [ ]") {
		t.Fatalf("viewport overflowed or lost active row:\n%s", view)
	}
	for _, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if ansi.StringWidth(line) > 32 {
			t.Fatalf("line exceeds display width: width=%d line=%q", ansi.StringWidth(line), line)
		}
	}

	plain = NewModel(windows[:1], false)
	plain.width, plain.height = 40, 20
	view = plain.View()
	if strings.Contains(view, "title: alpha") || !strings.Contains(view, "cwd: ~/project-a") {
		t.Fatalf("duplicate title was not elided or home CWD not shortened:\n%s", view)
	}

	colored := NewModel(windows, true)
	colored.width, colored.height = 90, 20
	update(colored, "space")
	view = colored.View()
	for _, escape := range []string{"\x1b[38;2;138;190;183m", "\x1b[38;2;181;189;104m", "\x1b[48;2;58;58;74m", "\x1b[0m"} {
		if !strings.Contains(view, escape) {
			t.Fatalf("colored rendering missing %q:\n%s", escape, view)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if ansi.StringWidth(line) > 90 {
			t.Fatalf("styled line exceeds display width: width=%d line=%q", ansi.StringWidth(line), line)
		}
	}
}

func TestNoColorPresenceIncludesEmptyValue(t *testing.T) {
	old, existed := os.LookupEnv("NO_COLOR")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("NO_COLOR", old)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	})
	_ = os.Setenv("NO_COLOR", "")
	if colorEnabled() {
		t.Fatal("empty but present NO_COLOR did not disable color")
	}
}
