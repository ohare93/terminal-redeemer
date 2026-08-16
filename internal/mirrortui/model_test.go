package mirrortui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/projectidentity"
)

func pickerWindows() []mirror.Window {
	return []mirror.Window{
		{Order: 0, SourceWindowID: 1, AppID: "kitty", WorkspaceID: "ws-dev", WorkspaceIndex: 1, WorkspaceName: "Dev", Output: "DP-1", Title: "editor", ZellijSession: "alpha", Terminal: &mirror.Terminal{CWD: "/home/test/project-a"}},
		{Order: 1, SourceWindowID: 2, AppID: "kitty", WorkspaceID: "ws-chat", WorkspaceIndex: 2, WorkspaceName: "Chat", Output: "DP-1", Title: "messages", ZellijSession: "beta", Terminal: &mirror.Terminal{CWD: "/tmp/chat"}},
		{Order: 2, SourceWindowID: 3, AppID: "kitty", WorkspaceID: "ws-dev", WorkspaceIndex: 1, WorkspaceName: "Dev", Output: "DP-1", Title: "alpha", ZellijSession: "gamma", Terminal: &mirror.Terminal{CWD: "/srv/project-b"}},
		{Order: 3, SourceWindowID: 4, AppID: "kitty", WorkspaceID: "ws-loose", WorkspaceIndex: 3, Output: "HDMI-A-1", Title: "loose", ZellijSession: "delta", Terminal: &mirror.Terminal{CWD: "/tmp/other"}},
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
	update(m, "space") // gamma, next in the rendered Dev group
	update(m, "down")
	update(m, "space") // beta, first session in the next group
	if cmd := update(m, "enter"); cmd == nil {
		t.Fatal("Enter did not quit")
	}
	if got := strings.Join(selectedSessions(m), ","); got != "beta,gamma" {
		t.Fatalf("selection was not returned in discovery order: %s", got)
	}

	fallback := NewModel(pickerWindows(), false)
	update(fallback, "down")
	update(fallback, "down")
	update(fallback, "enter")
	if got := strings.Join(selectedSessions(fallback), ","); got != "beta" {
		t.Fatalf("Enter without checks selected %q", got)
	}
}

func TestJKTypeIntoFilterAndOnlyArrowsNavigate(t *testing.T) {
	windows := []mirror.Window{
		{SourceWindowID: 1, AppID: "kitty", WorkspaceID: "one", Title: "jupiter", ZellijSession: "jupiter"},
		{SourceWindowID: 2, AppID: "kitty", WorkspaceID: "two", Title: "kite", ZellijSession: "kite"},
	}

	jModel := NewModel(windows, false)
	update(jModel, "j")
	if jModel.query != "j" || len(jModel.visible) != 1 || jModel.visible[0] != 0 || jModel.cursor != 0 {
		t.Fatalf("j did not filter normally: query=%q visible=%v cursor=%d", jModel.query, jModel.visible, jModel.cursor)
	}

	kModel := NewModel(windows, false)
	update(kModel, "k")
	if kModel.query != "k" || len(kModel.visible) != 1 || kModel.visible[0] != 1 || kModel.cursor != 0 {
		t.Fatalf("k did not filter normally: query=%q visible=%v cursor=%d", kModel.query, kModel.visible, kModel.cursor)
	}
	update(kModel, "esc")
	update(kModel, "down")
	if kModel.currentIndex() != 1 {
		t.Fatalf("down arrow did not navigate: current=%d", kModel.currentIndex())
	}
	update(kModel, "up")
	if kModel.currentIndex() != 0 {
		t.Fatalf("up arrow did not navigate: current=%d", kModel.currentIndex())
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

func TestFilteringIncludesSourceProjectIdentity(t *testing.T) {
	windows := pickerWindows()
	windows[0].Terminal.Project = []projectidentity.Segment{
		{Label: "mono/agent"},
		{Label: "agentleman-real"},
	}
	m := NewModel(windows, false)
	update(m, "mono/agent")
	if len(m.visible) != 1 || m.visible[0] != 0 {
		t.Fatalf("repository chip label filter visible=%v", m.visible)
	}
	m = NewModel(windows, false)
	update(m, "agentleman-real")
	if len(m.visible) != 1 || m.visible[0] != 0 {
		t.Fatalf("workspace chip label filter visible=%v", m.visible)
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

func TestGroupingUsesExactWorkspaceIdentityAndPreservesOrder(t *testing.T) {
	m := NewModel(pickerWindows(), false)
	m.width, m.height = 100, 100
	view := m.View()
	dev := strings.Index(view, "Workspace: Dev · DP-1 (2)")
	alpha := strings.Index(view, "alpha")
	gamma := strings.Index(view, "gamma")
	chat := strings.Index(view, "Workspace: Chat · DP-1 (1)")
	loose := strings.Index(view, "Workspace: Workspace 3 · HDMI-A-1 (1)")
	if dev < 0 || alpha < dev || gamma < alpha || chat < gamma || loose < chat {
		t.Fatalf("groups or sessions not in first-discovery order:\n%s", view)
	}

	update(m, "messages")
	view = m.View()
	if !strings.Contains(view, "Workspace: Chat · DP-1 (1)") || strings.Contains(view, "Workspace: Dev") || strings.Contains(view, "Workspace 3") {
		t.Fatalf("empty filtered groups were not hidden:\n%s", view)
	}

	windows := []mirror.Window{
		{SourceWindowID: 10, AppID: "kitty", WorkspaceID: "ws-10", WorkspaceIndex: 10, Title: "first", ZellijSession: "first"},
		{SourceWindowID: 11, AppID: "kitty", WorkspaceID: "ws-11", WorkspaceIndex: 11, Title: "second", ZellijSession: "second"},
		{SourceWindowID: 12, AppID: "kitty", WorkspaceID: "ws-12", WorkspaceName: "Duplicate", Title: "third", ZellijSession: "third"},
		{SourceWindowID: 13, AppID: "kitty", WorkspaceID: "ws-13", WorkspaceName: "Duplicate", Title: "fourth", ZellijSession: "fourth"},
	}
	m = NewModel(windows, false)
	m.width, m.height = 100, 30
	view = m.View()
	if strings.Count(view, "Workspace: Workspace 10 (1)") != 1 || strings.Count(view, "Workspace: Workspace 11 (1)") != 1 {
		t.Fatalf("distinct unnamed workspace IDs collapsed:\n%s", view)
	}
	if strings.Count(view, "Workspace: Duplicate (1)") != 2 {
		t.Fatalf("duplicate workspace display names collapsed despite distinct IDs:\n%s", view)
	}
}

func TestHeadlessSessionsRenderLastAndRemainSelectable(t *testing.T) {
	headless := mirror.Window{Headless: true, AppID: "zellij", Title: "redeem-detached", ZellijSession: "redeem-detached", Terminal: &mirror.Terminal{ZellijSession: "redeem-detached"}}
	visible := mirror.Window{SourceWindowID: 1, AppID: "kitty", WorkspaceID: "ws-1", WorkspaceIndex: 1, Title: "visible", ZellijSession: "visible", Terminal: &mirror.Terminal{CWD: "/tmp/visible"}}
	model := NewModel([]mirror.Window{headless, visible}, false)
	model.width, model.height = 80, 20
	view := model.View()
	workspaceHeading := strings.Index(view, "Workspace: Workspace 1 (1)")
	headlessHeading := strings.Index(view, "Headless Zellij (1)")
	if workspaceHeading < 0 || headlessHeading < workspaceHeading || !strings.Contains(view, "(unknown)") || !strings.Contains(view, "redeem-detached") {
		t.Fatalf("mixed visible/headless grouping was not useful:\n%s", view)
	}
	if model.currentIndex() != 1 {
		t.Fatalf("initial cursor did not follow rendered workspace-first order: current=%d visible=%v", model.currentIndex(), model.visible)
	}
	update(model, "down")
	if model.currentIndex() != 0 {
		t.Fatalf("down arrow did not move visually into headless group: current=%d visible=%v", model.currentIndex(), model.visible)
	}

	update(model, "redeem-detached")
	if len(model.visible) != 1 || model.visible[0] != 0 || !strings.Contains(model.View(), "Headless Zellij (1)") {
		t.Fatalf("headless session was not filterable: visible=%v\n%s", model.visible, model.View())
	}
	update(model, "enter")
	if got := selectedSessions(model); len(got) != 1 || got[0] != "redeem-detached" {
		t.Fatalf("headless selection = %v", got)
	}
}

func TestProjectFirstWideAndNarrowRendering(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	window := mirror.Window{
		Title:         "sensible-bee | OC | Fix picker",
		ZellijSession: "sensible-bee",
		IsFocused:     true,
		Terminal:      &mirror.Terminal{CWD: "/home/test/Development/projects/terminal-redeemer"},
	}

	wide := NewModel([]mirror.Window{window}, false)
	wide.width, wide.height = 84, 20
	update(wide, "space")
	view := wide.View()
	projectHeading := strings.Index(view, "PROJECT / DIRECTORY")
	activityHeading := strings.Index(view, "ACTIVITY")
	sessionHeading := strings.Index(view, "SESSION")
	project := strings.Index(view, "terminal-redeemer")
	activity := strings.Index(view, "OC | Fix picker")
	session := strings.LastIndex(view, "sensible-bee")
	if projectHeading < 0 || activityHeading < projectHeading || sessionHeading < activityHeading || project < sessionHeading || activity < project || session < activity {
		t.Fatalf("wide view is not project/activity/session ordered:\n%s", view)
	}
	if !strings.Contains(view, "> [x] * ") || strings.Contains(view, "Workspace: (unnamed)") || strings.Contains(view, "sensible-bee | OC") {
		t.Fatalf("wide focused, checked, grouping, or activity rendering regressed:\n%s", view)
	}
	if !strings.Contains(view, "Selected: 1  Matching: 1/1") || !strings.Contains(view, "Ctrl+A matches") {
		t.Fatalf("wide header or footer is not concise:\n%s", view)
	}

	narrow := NewModel([]mirror.Window{window}, false)
	narrow.width, narrow.height = 36, 20
	view = narrow.View()
	project = strings.Index(view, "terminal-redeemer")
	activity = strings.Index(view, "activity: OC | Fix picker")
	session = strings.Index(view, "session: sensible-bee")
	if project < 0 || activity < project || session < activity || !strings.Contains(view, "> [ ] * ") || !strings.Contains(view, "Selected: 0  Matching: 1/1") {
		t.Fatalf("narrow view did not retain clear totals and a project-first stacked fallback:\n%s", view)
	}
	for _, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if ansi.StringWidth(line) > narrow.width {
			t.Fatalf("narrow line exceeds display width: width=%d line=%q", ansi.StringWidth(line), line)
		}
	}
}

func TestProjectChipsMatchMonoTreatmentAndTruncateSafely(t *testing.T) {
	segments := []projectidentity.Segment{
		{Label: "mono/agent", Background: projectidentity.RGB{R: 192, G: 175, B: 22}, Foreground: projectidentity.RGB{R: 255, G: 255, B: 255}},
		{Label: "agentleman-real", Background: projectidentity.RGB{R: 177, G: 73, B: 32}, Foreground: projectidentity.RGB{R: 255, G: 255, B: 255}},
	}
	window := mirror.Window{SourceWindowID: 1, AppID: "kitty", WorkspaceID: "ws", ZellijSession: "session", Terminal: &mirror.Terminal{CWD: "/remote", Project: segments}}
	colored := NewModel([]mirror.Window{window}, true)
	colored.width, colored.height = 100, 10
	view := colored.View()
	for _, want := range []string{
		"\x1b[48;2;192;175;22m\x1b[38;2;255;255;255m\x1b[1m mono/agent \x1b[0m",
		"/\x1b[48;2;177;73;32m\x1b[38;2;255;255;255m\x1b[1m agentleman-real \x1b[0m",
		"\x1b[0m" + foregroundANSI(textColor) + backgroundANSI(selectedBgColor) + "/",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("colored project chip missing %q:\n%q", want, view)
		}
	}

	plain := NewModel([]mirror.Window{window}, false)
	plain.width, plain.height = 32, 10
	view = plain.View()
	if strings.Contains(view, "\x1b[") || !strings.Contains(view, "agentleman-real") {
		t.Fatalf("NO_COLOR chip fallback is not readable: %q", view)
	}

	colored.width, colored.height = 32, 10
	view = colored.View()
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 32 {
			t.Fatalf("narrow chip line width=%d: %q", ansi.StringWidth(line), line)
		}
	}
	if !strings.Contains(view, "\x1b[0m") {
		t.Fatalf("truncated colored chips do not close SGR state: %q", view)
	}
}

func TestDisplayedActivityCleanupIsConservative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		title   string
		session string
		want    string
	}{
		{name: "known separator", title: "alpha | OC | task", session: "alpha", want: "OC | task"},
		{name: "duplicate", title: "alpha", session: "alpha", want: ""},
		{name: "different separator", title: "alpha - task", session: "alpha", want: "alpha - task"},
		{name: "different case", title: "Alpha | task", session: "alpha", want: "Alpha | task"},
		{name: "not leading", title: "task | alpha", session: "alpha", want: "task | alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := mirror.Window{Title: tc.title, ZellijSession: tc.session}
			if got := displayActivity(window); got != tc.want {
				t.Fatalf("displayActivity()=%q, want %q", got, tc.want)
			}
		})
	}

	model := NewModel([]mirror.Window{{
		Title:         "alpha | OC | task",
		ZellijSession: "alpha",
		Terminal:      &mirror.Terminal{CWD: "/tmp/project"},
	}}, false)
	update(model, "alpha | OC")
	if len(model.visible) != 1 {
		t.Fatalf("display cleanup changed filtering data: visible=%v", model.visible)
	}
}

func TestPathTruncationAndEmptyStates(t *testing.T) {
	got := fitPath("/one/two/project-a", 10)
	if got != "…project-a" || ansi.StringWidth(got) != 10 {
		t.Fatalf("fitPath()=%q width=%d", got, ansi.StringWidth(got))
	}

	empty := NewModel(nil, false)
	empty.width, empty.height = 80, 10
	if view := empty.View(); !strings.Contains(view, "Matching: 0/0") || !strings.Contains(view, "No sessions are available.") || strings.Contains(view, "PROJECT / DIRECTORY") {
		t.Fatalf("empty state rendering regressed:\n%s", view)
	}

	filtered := NewModel(pickerWindows(), false)
	filtered.width, filtered.height = 80, 10
	update(filtered, "not-a-match")
	if view := filtered.View(); !strings.Contains(view, "Matching: 0/4") || !strings.Contains(view, "No sessions match the current filter.") || strings.Contains(view, "PROJECT / DIRECTORY") {
		t.Fatalf("filtered-empty state rendering regressed:\n%s", view)
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

func TestExactHeightRenderingKeepsTitleAndHasNoTrailingNewline(t *testing.T) {
	model := NewModel(pickerWindows(), false)
	model.width, model.height = 100, 7
	view := model.View()
	if strings.HasSuffix(view, "\n") {
		t.Fatalf("view has a trailing newline that Bubble Tea treats as an extra row: %q", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != model.height {
		t.Fatalf("rendered lines=%d, want exact terminal height %d:\n%s", len(lines), model.height, view)
	}
	if !strings.HasPrefix(lines[0], "Mirror sessions  Selected:") {
		t.Fatalf("title row was clipped from full-height render:\n%s", view)
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
	if strings.Contains(view, "activity:") || !strings.Contains(view, "~/project-a") || !strings.Contains(view, "session: alpha") {
		t.Fatalf("duplicate activity was not elided or home CWD not shortened:\n%s", view)
	}

	windows[0].IsFocused = true
	colored := NewModel(windows, true)
	colored.width, colored.height = 90, 20
	update(colored, "space")
	view = colored.View()
	for description, sequence := range map[string]string{
		"accent current marker":  foregroundANSI(accentColor) + "> ",
		"success checked marker": foregroundANSI(successColor) + "[x] ",
		"warning focus marker":   foregroundANSI(warningColor) + "* ",
		"accent project":         foregroundANSI(accentColor) + "~/project-a",
		"dim session":            foregroundANSI(dimColor) + "alpha",
		"selected background":    backgroundANSI(selectedBgColor),
		"reset":                  "\x1b[0m",
	} {
		if !strings.Contains(view, sequence) {
			t.Fatalf("colored rendering missing %s %q:\n%s", description, sequence, view)
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
