package procmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DescendantArgvMatch walks a bounded-by-/proc process tree below rootPID and
// reports whether any live descendant argv satisfies match. Unreadable or
// exited individual processes are skipped; failure to read the process table
// itself is returned.
func DescendantArgvMatch(procRoot string, rootPID int, match func([]string) bool) (bool, error) {
	if rootPID <= 0 || match == nil {
		return false, nil
	}
	root := strings.TrimSpace(procRoot)
	if root == "" {
		root = "/proc"
	}
	children, err := processChildren(root)
	if err != nil {
		return false, err
	}
	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]struct{}{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		args, err := readProcArgs(root, pid)
		if err == nil && match(args) {
			return true, nil
		}
		queue = append(queue, children[pid]...)
	}
	return false, nil
}

// DescendantPIDs returns descendants with leaves before parents, suitable for
// bounded process-tree cleanup.
func DescendantPIDs(procRoot string, rootPID int) ([]int, error) {
	root := strings.TrimSpace(procRoot)
	if root == "" {
		root = "/proc"
	}
	children, err := processChildren(root)
	if err != nil {
		return nil, err
	}
	queue := append([]int(nil), children[rootPID]...)
	out := make([]int, 0, len(queue))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out, nil
}

func processChildren(root string) (map[int][]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read process table: %w", err)
	}
	children := make(map[int][]int)
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
		ppid, err := parentPID(string(payload))
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	for ppid := range children {
		sort.Ints(children[ppid])
	}
	return children, nil
}

func parentPID(stat string) (int, error) {
	idx := strings.LastIndex(stat, ")")
	if idx < 0 || idx+2 >= len(stat) {
		return 0, fmt.Errorf("unexpected stat format")
	}
	fields := strings.Fields(stat[idx+2:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected stat fields")
	}
	return strconv.Atoi(fields[1])
}

func readProcArgs(root string, pid int) ([]string, error) {
	payload, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(payload), "\x00")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out, nil
}
