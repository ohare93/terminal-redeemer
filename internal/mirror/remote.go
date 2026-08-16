package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var sshDestinationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:\[\]-]*$`)

type RemoteConfig struct {
	Host            string
	SSHCommand      string
	SSHOptions      []string
	SnapshotCommand []string
}

func ValidateDestination(host string) error {
	if !sshDestinationPattern.MatchString(host) {
		return fmt.Errorf("invalid SSH host %q", host)
	}
	return nil
}

func AcquireRemote(ctx context.Context, runner Runner, cfg RemoteConfig) (Snapshot, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	if err := ValidateDestination(cfg.Host); err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(cfg.SSHCommand) == "" {
		return Snapshot{}, fmt.Errorf("SSH command is empty")
	}
	if len(cfg.SnapshotCommand) == 0 || strings.TrimSpace(cfg.SnapshotCommand[0]) == "" {
		return Snapshot{}, fmt.Errorf("remote snapshot command is empty")
	}
	args := append([]string(nil), cfg.SSHOptions...)
	args = append(args, "--", cfg.Host, QuoteCommand(cfg.SnapshotCommand))
	payload, err := runner.Output(ctx, Command{Name: cfg.SSHCommand, Args: args})
	if err != nil {
		return Snapshot{}, fmt.Errorf("acquire mirror snapshot from %s: %w", cfg.Host, err)
	}
	return DecodeSnapshot(payload)
}

func ReadSnapshot(path string) (Snapshot, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read mirror snapshot: %w", err)
	}
	return DecodeSnapshot(payload)
}

func DecodeSnapshot(raw []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode remote mirror snapshot: %w", err)
	}
	if strings.TrimSpace(snapshot.Host) == "" {
		return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: host is empty")
	}
	if snapshot.GeneratedAt.IsZero() {
		return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: generated_at is empty")
	}
	if snapshot.Windows == nil {
		return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: windows is missing")
	}
	for i, window := range snapshot.Windows {
		if window.Terminal != nil {
			if len(window.Terminal.Project) > 2 {
				return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: window %d has too many project segments", i)
			}
			for _, segment := range window.Terminal.Project {
				label := strings.TrimSpace(segment.Label)
				if label == "" || len(label) > 4096 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
					return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: window %d has unsafe project label", i)
				}
			}
		}
		if window.Headless {
			if window.SourceWindowID != 0 || strings.TrimSpace(window.AppID) != "zellij" || SessionName(window) == "" {
				return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: headless session %d is invalid", i)
			}
			continue
		}
		if window.SourceWindowID <= 0 || strings.TrimSpace(window.AppID) == "" {
			return Snapshot{}, fmt.Errorf("malformed remote mirror snapshot: window %d lacks source_window_id or app_id", i)
		}
	}
	return snapshot, nil
}

// Discover returns live Kitty-backed and headless Zellij sessions. Snapshot
// order is authoritative; an exact session appears at most once, preferring
// the first (normally window-backed) entry.
func Discover(snapshot Snapshot) []Window {
	windows := make([]Window, 0, len(snapshot.Windows))
	seen := make(map[string]struct{})
	for _, window := range snapshot.Windows {
		if !window.Headless && !strings.EqualFold(strings.TrimSpace(window.AppID), "kitty") {
			continue
		}
		session := SessionName(window)
		if session == "" {
			continue
		}
		if _, found := seen[session]; found {
			continue
		}
		seen[session] = struct{}{}
		windows = append(windows, window)
	}
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].Order != windows[j].Order {
			return windows[i].Order < windows[j].Order
		}
		if windows[i].WorkspaceIndex != windows[j].WorkspaceIndex {
			return windows[i].WorkspaceIndex < windows[j].WorkspaceIndex
		}
		if windows[i].Title != windows[j].Title {
			return windows[i].Title < windows[j].Title
		}
		return windows[i].SourceWindowID < windows[j].SourceWindowID
	})
	return windows
}

func SessionName(window Window) string {
	if value := strings.TrimSpace(window.ZellijSession); value != "" {
		return value
	}
	if window.Terminal != nil {
		return strings.TrimSpace(window.Terminal.ZellijSession)
	}
	return ""
}

func FilterSessions(windows []Window, sessions []string) ([]Window, error) {
	if len(sessions) == 0 {
		return nil, fmt.Errorf("at least one --session is required when --all is not set")
	}
	wanted := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		value := strings.TrimSpace(session)
		if value == "" {
			return nil, fmt.Errorf("session name must not be empty")
		}
		wanted[value] = true
	}
	selected := make([]Window, 0, len(wanted))
	found := make(map[string]bool, len(wanted))
	for _, window := range windows {
		session := SessionName(window)
		if wanted[session] {
			selected = append(selected, window)
			found[session] = true
		}
	}
	missing := make([]string, 0)
	for session := range wanted {
		if !found[session] {
			missing = append(missing, session)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("sessions not found: %s", strings.Join(missing, ", "))
	}
	return selected, nil
}

func QuoteCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func ShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
