package procmeta

import (
	"bytes"
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

	out, err := v.exec.Output("zellij", "list-sessions", "--short")
	if err != nil {
		return false, err
	}

	for _, line := range bytes.Split(out, []byte("\n")) {
		name := strings.TrimSpace(string(line))
		if name == "" {
			continue
		}
		fields := strings.Fields(name)
		if len(fields) > 0 {
			name = fields[0]
		}
		if name == session {
			return true, nil
		}
	}

	return false, nil
}
