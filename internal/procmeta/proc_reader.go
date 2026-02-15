package procmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type ProcReader struct {
	ProcRoot string
}

const maxDescendantDepth = 3

var interactiveCommands = map[string]struct{}{
	"zsh":    {},
	"bash":   {},
	"fish":   {},
	"sh":     {},
	"nu":     {},
	"zellij": {},
	"tmux":   {},
	"nvim":   {},
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
	windowCWD := info.CWD

	if payload, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline")); err == nil {
		info.Args = parseNullSeparated(payload)
	}

	if payload, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "environ")); err == nil {
		info.Env = parseEnv(payload)
	}

	if preferred, ok := r.detectPreferredCWD(root, pid, windowCWD); ok {
		info.CWD = preferred
	}

	info.ProcessChain = r.readProcessChain(root, pid)

	return info, nil
}

func (r ProcReader) detectPreferredCWD(root string, rootPID int, windowCWD string) (string, bool) {
	descendants := collectDescendants(root, rootPID, maxDescendantDepth)
	if len(descendants) == 0 {
		return "", false
	}

	home, _ := os.UserHomeDir()
	bestScore := -1
	bestCWD := ""
	for _, candidate := range descendants {
		cwd, err := os.Readlink(filepath.Join(root, strconv.Itoa(candidate.pid), "cwd"))
		if err != nil || strings.TrimSpace(cwd) == "" {
			continue
		}

		score := candidate.depth * 10
		if isInteractiveComm(candidate.comm) {
			score += 50
		}
		if windowCWD != "" && cwd != windowCWD {
			score += 20
		}
		if home != "" && cwd != home {
			score += 10
		}

		if score > bestScore {
			bestScore = score
			bestCWD = cwd
		}
	}

	if bestScore < 0 {
		return "", false
	}
	return bestCWD, true
}

type descendantCandidate struct {
	pid   int
	depth int
	comm  string
}

func collectDescendants(root string, rootPID int, maxDepth int) []descendantCandidate {
	children, _ := buildChildrenIndex(root)
	out := make([]descendantCandidate, 0, 16)
	queue := []descendantCandidate{{pid: rootPID, depth: 0}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= maxDepth {
			continue
		}

		for _, child := range children[current.pid] {
			next := descendantCandidate{pid: child, depth: current.depth + 1, comm: readComm(root, child)}
			out = append(out, next)
			queue = append(queue, next)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].depth != out[j].depth {
			return out[i].depth < out[j].depth
		}
		return out[i].pid < out[j].pid
	})
	return out
}

func buildChildrenIndex(root string) (map[int][]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return map[int][]int{}, err
	}

	children := make(map[int][]int, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, err := parseParentPIDFromStat(string(payload))
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return children, nil
}

func readComm(root string, pid int) string {
	payload, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(payload))
}

func isInteractiveComm(comm string) bool {
	comm = strings.ToLower(strings.TrimSpace(comm))
	_, ok := interactiveCommands[comm]
	return ok
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
