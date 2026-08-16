package mirror

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
)

// Projection is an owned Niri window whose live descendants prove the exact
// configured SSH destination and case-sensitive Zellij session. Titles are
// presentation only and are deliberately not consulted.
type Projection struct {
	Window           OwnedWindow
	SourceHost       string
	Session          string
	CorrelationToken string
}

type ProjectionInventory struct {
	Exact               []Projection
	Untracked           []OwnedWindow
	Ambiguous           []OwnedWindow
	AmbiguousCandidates map[int][]Projection
}

type ProjectionEvidenceConfig struct {
	ProcRoot   string
	SSHCommand string
	SSHOptions []string
}

// InspectProjections evaluates each owned window exactly once. Vanished or
// unreadable process evidence is untracked; multiple qualifying descendants
// are ambiguous. Neither category is authoritative.
func InspectProjections(ctx context.Context, windows []OwnedWindow, cfg ProjectionEvidenceConfig) (ProjectionInventory, error) {
	if err := ValidateSSHOptions(cfg.SSHOptions); err != nil {
		return ProjectionInventory{}, err
	}
	inventory := ProjectionInventory{AmbiguousCandidates: make(map[int][]Projection)}
	for _, window := range windows {
		matches, err := inspectProjectionWindow(ctx, window, cfg)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ProjectionInventory{}, err
			}
			inventory.Untracked = append(inventory.Untracked, window)
			continue
		}
		switch len(matches) {
		case 0:
			inventory.Untracked = append(inventory.Untracked, window)
		case 1:
			inventory.Exact = append(inventory.Exact, matches[0])
		default:
			inventory.Ambiguous = append(inventory.Ambiguous, window)
			inventory.AmbiguousCandidates[window.ID] = append([]Projection(nil), matches...)
		}
	}
	return inventory, nil
}

func inspectProjectionWindow(ctx context.Context, window OwnedWindow, cfg ProjectionEvidenceConfig) ([]Projection, error) {
	if window.ID <= 0 || window.PID <= 0 {
		return nil, nil
	}
	matches := make([]Projection, 0, 1)
	_, err := procmeta.DescendantArgvMatchContext(ctx, cfg.ProcRoot, window.PID, func(argv []string) bool {
		host, session, token, ok := parseProjectionSSHArgv(argv, cfg.SSHCommand, cfg.SSHOptions)
		if ok {
			matches = append(matches, Projection{Window: window, SourceHost: host, Session: session, CorrelationToken: token})
		}
		// Walk the complete descendant set so multiple candidates fail closed.
		return false
	})
	return matches, err
}

// parseProjectionSSHArgv accepts only the complete deterministic SSH argv
// emitted by PlanLaunch/PlanNew: exact executable identity as represented in
// argv[0], exact configured options, -tt, --, destination, and one static
// remote command. It does not interpret arbitrary SSH argv.
func parseProjectionSSHArgv(argv []string, sshCommand string, sshOptions []string) (host string, session string, token string, ok bool) {
	want := strings.TrimSpace(sshCommand)
	if want == "" {
		want = "ssh"
	}
	if len(argv) < 3 || !sameExecutableArgv0(argv[0], want) {
		return "", "", "", false
	}
	host, remote := argv[len(argv)-2], argv[len(argv)-1]
	expected, err := buildSSHArgs(sshOptions, []string{"-tt"}, host, remote)
	if err != nil || !equalStrings(argv[1:], expected) {
		return "", "", "", false
	}
	session, token, ok = parseProjectionRemoteCommand(remote)
	if !ok {
		return "", "", "", false
	}
	return host, session, token, true
}

func sameExecutableArgv0(observed, configured string) bool {
	if filepath.IsAbs(configured) || strings.ContainsRune(configured, filepath.Separator) {
		return filepath.Clean(observed) == filepath.Clean(configured)
	}
	return observed == configured
}

func parseProjectionRemoteCommand(remote string) (string, string, bool) {
	if strings.HasPrefix(remote, "cd -- ") {
		_, remainder, valid := consumeGeneratedShellWord(strings.TrimPrefix(remote, "cd -- "))
		if !valid || !strings.HasPrefix(remainder, " 2>/dev/null || true; exec ") {
			return "", "", false
		}
		remote = strings.TrimPrefix(remainder, " 2>/dev/null || true; ")
	}
	if !strings.HasPrefix(remote, "exec ") {
		return "", "", false
	}
	words, valid := parseGeneratedShellWords(strings.TrimPrefix(remote, "exec "))
	if !valid {
		return "", "", false
	}
	prefix := []string{"env"}
	for _, name := range zellijEnvironment {
		prefix = append(prefix, "-u", name)
	}
	if len(words) < len(prefix)+4 || !equalStrings(words[:len(prefix)], prefix) {
		return "", "", false
	}
	rest := words[len(prefix):]
	token := ""
	if strings.HasPrefix(rest[0], projectionTokenEnvironment+"=") {
		token = strings.TrimPrefix(rest[0], projectionTokenEnvironment+"=")
		if !correlationTokenPattern.MatchString(token) {
			return "", "", false
		}
		rest = rest[1:]
	}
	if len(rest) < 4 || rest[0] != "zellij" || rest[1] != "attach" {
		return "", "", false
	}
	rest = rest[2:]
	var session string
	switch {
	case len(rest) == 5 && rest[0] == "--create" && rest[2] == "options" && rest[3] == "--on-force-close" && rest[4] == "detach":
		session = rest[1]
	case len(rest) == 4 && rest[1] == "options" && rest[2] == "--on-force-close" && rest[3] == "detach":
		session = rest[0]
	case len(rest) == 2 && rest[0] == "--" && strings.HasPrefix(rest[1], "-"):
		// Zellij's clap grammar accepts a leading-dash session only through
		// `attach -- SESSION`; its trailing options subcommand cannot then be
		// combined. This remains attach-only and never creates a session.
		session = rest[1]
	default:
		return "", "", false
	}
	if ValidateSession(session) != nil {
		return "", "", false
	}
	return session, token, true
}

func parseGeneratedShellWords(input string) ([]string, bool) {
	if input == "" {
		return nil, false
	}
	words := make([]string, 0, 16)
	for len(input) > 0 {
		word, remainder, valid := consumeGeneratedShellWord(input)
		if !valid {
			return nil, false
		}
		words = append(words, word)
		input = remainder
		if input == "" {
			break
		}
		if input[0] != ' ' || len(input) == 1 || input[1] == ' ' {
			return nil, false
		}
		input = input[1:]
	}
	return words, true
}

func consumeGeneratedShellWord(input string) (string, string, bool) {
	if input == "" || input[0] != '\'' {
		return "", input, false
	}
	input = input[1:]
	var word strings.Builder
	for {
		index := strings.IndexByte(input, '\'')
		if index < 0 {
			return "", input, false
		}
		word.WriteString(input[:index])
		input = input[index+1:]
		if strings.HasPrefix(input, "\"'\"'") {
			word.WriteByte('\'')
			input = input[4:]
			continue
		}
		return word.String(), input, true
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
