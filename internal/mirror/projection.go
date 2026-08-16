package mirror

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
)

// Projection is an owned Niri window whose live descendants prove the exact
// SSH destination and case-sensitive Zellij session. Titles are presentation
// only and are deliberately not consulted.
type Projection struct {
	Window     OwnedWindow
	SourceHost string
	Session    string
}

type ProjectionInventory struct {
	Exact     []Projection
	Untracked []OwnedWindow
}

type ProjectionInspector interface {
	Inspect(context.Context, []OwnedWindow) (ProjectionInventory, error)
}

type ProcProjectionInspector struct {
	ProcRoot   string
	SSHCommand string
}

func (p ProcProjectionInspector) Inspect(ctx context.Context, windows []OwnedWindow) (ProjectionInventory, error) {
	inventory := ProjectionInventory{}
	for _, window := range windows {
		projection, ok, err := p.inspectWindow(ctx, window)
		if err != nil {
			return ProjectionInventory{}, fmt.Errorf("inspect owned window %d: %w", window.ID, err)
		}
		if !ok {
			inventory.Untracked = append(inventory.Untracked, window)
			continue
		}
		inventory.Exact = append(inventory.Exact, projection)
	}
	return inventory, nil
}

func (p ProcProjectionInspector) inspectWindow(ctx context.Context, window OwnedWindow) (Projection, bool, error) {
	if window.ID <= 0 || window.PID <= 0 {
		return Projection{}, false, nil
	}
	matches := make([]Projection, 0, 1)
	_, err := procmeta.DescendantArgvMatchContext(ctx, p.ProcRoot, window.PID, func(argv []string) bool {
		host, session, ok := ParseProjectionSSHArgv(argv, p.SSHCommand)
		if ok {
			matches = append(matches, Projection{Window: window, SourceHost: host, Session: session})
		}
		// Walk the complete descendant set so multiple candidates fail closed.
		return false
	})
	if err != nil {
		return Projection{}, false, err
	}
	if len(matches) != 1 {
		return Projection{}, false, nil
	}
	return matches[0], true, nil
}

// ParseProjectionSSHArgv accepts only the deterministic argv emitted by
// PlanLaunch/PlanNew. Arbitrary SSH commands and shell fragments fail closed.
func ParseProjectionSSHArgv(argv []string, sshCommand string) (host string, session string, ok bool) {
	if len(argv) < 5 {
		return "", "", false
	}
	want := strings.TrimSpace(sshCommand)
	if want == "" {
		want = "ssh"
	}
	if filepath.Base(argv[0]) != filepath.Base(want) {
		return "", "", false
	}
	n := len(argv)
	if argv[n-4] != "-tt" || argv[n-3] != "--" {
		return "", "", false
	}
	host = argv[n-2]
	if ValidateDestination(host) != nil {
		return "", "", false
	}
	remote := argv[n-1]
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
	prefix = append(prefix, "zellij", "attach")
	if len(words) < len(prefix)+4 || !equalStrings(words[:len(prefix)], prefix) {
		return "", "", false
	}
	rest := words[len(prefix):]
	if len(rest) == 5 && rest[0] == "--create" {
		rest = rest[1:]
	}
	if len(rest) != 4 || rest[1] != "options" || rest[2] != "--on-force-close" || rest[3] != "detach" {
		return "", "", false
	}
	if ValidateSession(rest[0]) != nil {
		return "", "", false
	}
	return host, rest[0], true
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
