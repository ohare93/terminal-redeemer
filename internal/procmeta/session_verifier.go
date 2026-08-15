package procmeta

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

type SessionVerifier interface {
	Exists(session string) (bool, error)
}

type commandExecutor interface {
	Output(name string, args ...string) ([]byte, error)
}

type osCommandExecutor struct{}

func (osCommandExecutor) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type ZellijSessionVerifier struct {
	exec commandExecutor
}

func NewZellijSessionVerifier(exec commandExecutor) ZellijSessionVerifier {
	if exec == nil {
		exec = osCommandExecutor{}
	}
	return ZellijSessionVerifier{exec: exec}
}

func (v ZellijSessionVerifier) Exists(session string) (bool, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return false, nil
	}

	sessions, err := v.List()
	if err != nil {
		return false, err
	}
	for _, name := range sessions {
		if name == session {
			return true, nil
		}
	}
	return false, nil
}

// List returns the current session names reported by Zellij. Keeping this on
// the existing verifier gives resume one snapshot of availability instead of
// invoking list-sessions once for every captured item.
func (v ZellijSessionVerifier) List() ([]string, error) {
	out, err := v.exec.Output("zellij", "list-sessions", "--short")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && bytes.Contains(exitErr.Stderr, []byte("No active zellij sessions found")) {
			return []string{}, nil
		}
		return nil, err
	}
	return ParseZellijSessions(out), nil
}

func ParseZellijSessions(out []byte) []string {
	sessions := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range bytes.Split(out, []byte("\n")) {
		name := strings.TrimSpace(string(line))
		if name == "" || strings.HasPrefix(name, "No active zellij sessions") {
			continue
		}
		fields := strings.Fields(name)
		if len(fields) > 0 {
			name = fields[0]
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		sessions = append(sessions, name)
	}
	return sessions
}
