package procmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ProcReader struct {
	ProcRoot string
}

func (r ProcReader) Inspect(pid int) (ProcessInfo, error) {
	if pid <= 0 {
		return ProcessInfo{}, nil
	}

	root := r.ProcRoot
	if strings.TrimSpace(root) == "" {
		root = "/proc"
	}

	info := ProcessInfo{}

	if cwd, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "cwd")); err == nil {
		info.CWD = cwd
	}

	if payload, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline")); err == nil {
		info.Args = parseNullSeparated(payload)
	}

	if payload, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "environ")); err == nil {
		info.Env = parseEnv(payload)
	}

	info.ProcessChain = r.readProcessChain(root, pid)

	return info, nil
}

func (r ProcReader) readProcessChain(root string, pid int) []string {
	chain := make([]string, 0, 8)
	current := pid
	for range 8 {
		if current <= 0 {
			break
		}

		commPath := filepath.Join(root, strconv.Itoa(current), "comm")
		if payload, err := os.ReadFile(commPath); err == nil {
			name := strings.TrimSpace(string(payload))
			if name != "" {
				chain = append(chain, name)
			}
		}

		statPath := filepath.Join(root, strconv.Itoa(current), "stat")
		payload, err := os.ReadFile(statPath)
		if err != nil {
			break
		}

		next, err := parseParentPIDFromStat(string(payload))
		if err != nil || next == current {
			break
		}
		current = next
	}

	return chain
}

func parseNullSeparated(payload []byte) []string {
	raw := strings.Split(string(payload), "\x00")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseEnv(payload []byte) map[string]string {
	env := map[string]string{}
	for _, part := range parseNullSeparated(payload) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			env[kv[0]] = kv[1]
		}
	}
	return env
}

func parseParentPIDFromStat(stat string) (int, error) {
	idx := strings.LastIndex(stat, ")")
	if idx < 0 || idx+2 >= len(stat) {
		return 0, fmt.Errorf("unexpected stat format")
	}
	rest := strings.Fields(stat[idx+2:])
	if len(rest) < 2 {
		return 0, fmt.Errorf("unexpected stat fields")
	}
	return strconv.Atoi(rest[1])
}
