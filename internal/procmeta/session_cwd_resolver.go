package procmeta

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type SessionCWDResolver interface {
	Resolve(session string) (string, error)
}

type ZellijSessionCWDResolver struct {
	ProcRoot string
}

func NewZellijSessionCWDResolver(procRoot string) ZellijSessionCWDResolver {
	return ZellijSessionCWDResolver{ProcRoot: procRoot}
}

func (r ZellijSessionCWDResolver) Resolve(session string) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", nil
	}

	root := strings.TrimSpace(r.ProcRoot)
	if root == "" {
		root = "/proc"
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}

	type procMeta struct {
		pid     int
		ppid    int
		comm    string
		cmdline string
	}
	metas := make(map[int]procMeta, len(entries))
	children := make(map[int][]int, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		statPayload, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, err := parseParentPIDFromStat(string(statPayload))
		if err != nil {
			continue
		}

		comm := readComm(root, pid)
		cmdline := ""
		if payload, err := os.ReadFile(filepath.Join(root, entry.Name(), "cmdline")); err == nil {
			cmdline = strings.Join(parseNullSeparated(payload), " ")
		}

		meta := procMeta{pid: pid, ppid: ppid, comm: comm, cmdline: cmdline}
		metas[pid] = meta
		children[ppid] = append(children[ppid], pid)
	}

	servers := make([]int, 0, 4)
	needle := "/" + session
	for pid, meta := range metas {
		if strings.ToLower(meta.comm) != "zellij" {
			continue
		}
		if strings.Contains(meta.cmdline, "--server") && strings.Contains(meta.cmdline, needle) {
			servers = append(servers, pid)
		}
	}

	if len(servers) == 0 {
		return "", nil
	}

	home, _ := os.UserHomeDir()
	best := ""
	bestScore := -1
	for _, serverPID := range servers {
		candidates := bfsChildren(children, serverPID, 4)
		for _, c := range candidates {
			cwd, err := os.Readlink(filepath.Join(root, strconv.Itoa(c.pid), "cwd"))
			if err != nil || strings.TrimSpace(cwd) == "" {
				continue
			}
			score := c.depth * 10
			if isInteractiveComm(metas[c.pid].comm) {
				score += 50
			}
			if home != "" && cwd != home {
				score += 20
			}
			if score > bestScore {
				bestScore = score
				best = cwd
			}
		}
	}

	if bestScore < 0 {
		return "", nil
	}

	return best, nil
}

type childCandidate struct {
	pid   int
	depth int
}

func bfsChildren(children map[int][]int, rootPID int, maxDepth int) []childCandidate {
	queue := []childCandidate{{pid: rootPID, depth: 0}}
	out := make([]childCandidate, 0, 16)
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if curr.depth >= maxDepth {
			continue
		}
		for _, child := range children[curr.pid] {
			next := childCandidate{pid: child, depth: curr.depth + 1}
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
