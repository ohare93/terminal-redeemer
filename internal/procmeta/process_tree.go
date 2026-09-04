package procmeta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxProcessNodes         = 256
	maxProcessDepth         = 8
	maxProcessTasks         = 256
	maxProcessMetadataBytes = 64 << 10
)

type processIdentity struct {
	parentPID int
	startTime string
}

type processNode struct {
	pid    int
	parent int
	depth  int
}

// ZellijSessionEvidence is a complete, point-in-time observation of the
// process tree below a Kitty window. Candidates contains the distinct session
// names from exact descendant argv of the form zellij attach -- <session>.
// Callers must reject evidence unless Complete and KittyVerified are both true.
type ZellijSessionEvidence struct {
	KittyVerified bool
	Candidates    []string
	Complete      bool
}

// ExactZellijAttachSession parses the one attach form used by resume and by
// visible-session observation. It deliberately accepts no optional flags,
// omitted separator, environment inference, or shell text.
func ExactZellijAttachSession(args []string) (string, bool) {
	if len(args) != 4 || filepath.Base(args[0]) != "zellij" || args[1] != "attach" || args[2] != "--" || args[3] == "" {
		return "", false
	}
	return args[3], true
}

// ObserveZellijSessionEvidence returns exact and complete session evidence for
// a Kitty process. A nil error with Complete=false means the process tree could
// not be observed without a PID replacement, disappearance, unreadable
// metadata, or an exceeded resource bound; no candidate is trustworthy.
func ObserveZellijSessionEvidence(ctx context.Context, procRoot string, rootPID int) (ZellijSessionEvidence, error) {
	return observeZellijSessionEvidence(ctx, procRoot, rootPID, nil)
}

// SessionEvidenceObserver is shared by capture and mirror enrichment.
type SessionEvidenceObserver interface {
	ObserveZellijSessions(pid int) (ZellijSessionEvidence, error)
}

// ProcSessionEvidenceObserver observes the live procfs process tree.
type ProcSessionEvidenceObserver struct{ ProcRoot string }

func (observer ProcSessionEvidenceObserver) ObserveZellijSessions(pid int) (ZellijSessionEvidence, error) {
	return ObserveZellijSessionEvidence(context.Background(), observer.ProcRoot, pid)
}

func observeZellijSessionEvidence(ctx context.Context, procRoot string, rootPID int, afterSnapshot func()) (ZellijSessionEvidence, error) {
	evidence := ZellijSessionEvidence{}
	if rootPID <= 0 {
		return evidence, nil
	}
	root := normalizedProcRoot(procRoot)
	table, children, complete, err := processTable(ctx, root, rootPID)
	if err != nil {
		return evidence, err
	}
	if !complete {
		return evidence, nil
	}
	if afterSnapshot != nil {
		afterSnapshot()
	}
	rootIdentity, ok := table[rootPID]
	if !ok {
		return evidence, nil
	}
	if current, err := readProcessIdentity(root, rootPID); err != nil || current != rootIdentity {
		return evidence, nil
	}
	verified, metadataComplete := verifyKittyProcess(root, rootPID)
	if !metadataComplete {
		return evidence, nil
	}
	evidence.KittyVerified = verified
	if !verified {
		evidence.Complete = true
		return evidence, nil
	}

	candidates := make(map[string]struct{})
	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]struct{}{}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return ZellijSessionEvidence{}, err
		}
		pid := queue[0]
		queue = queue[1:]
		if _, duplicate := seen[pid]; duplicate {
			return evidence, nil
		}
		seen[pid] = struct{}{}
		identity, ok := table[pid]
		if !ok {
			return evidence, nil
		}
		current, err := readProcessIdentity(root, pid)
		if err != nil || current != identity {
			return evidence, nil
		}
		args, err := readProcArgs(root, pid)
		if err != nil {
			return evidence, nil
		}
		after, err := readProcessIdentity(root, pid)
		if err != nil || after != identity {
			return evidence, nil
		}
		if session, exact := ExactZellijAttachSession(args); exact {
			candidates[session] = struct{}{}
		}
		queue = append(queue, children[pid]...)
	}
	if current, err := readProcessIdentity(root, rootPID); err != nil || current != rootIdentity {
		return evidence, nil
	}
	for candidate := range candidates {
		evidence.Candidates = append(evidence.Candidates, candidate)
	}
	sort.Strings(evidence.Candidates)
	evidence.Complete = true
	return evidence, nil
}

func verifyKittyProcess(root string, pid int) (verified bool, complete bool) {
	processDir := filepath.Join(root, strconv.Itoa(pid))
	executable, executableErr := os.Readlink(filepath.Join(processDir, "exe"))
	if executableErr == nil && isKittyBasename(filepath.Base(executable)) {
		return true, true
	}
	payload, commErr := readBoundedFile(filepath.Join(processDir, "comm"))
	if commErr == nil {
		if !utf8.Valid(payload) {
			return false, false
		}
		comm := strings.TrimSpace(string(payload))
		if strings.ContainsAny(comm, "/\\\x00") {
			return false, true
		}
		return isKittyBasename(comm), true
	}
	if executableErr != nil {
		return false, false
	}
	return false, true
}

func isKittyBasename(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kitty", "kitty.bin", ".kitty-wrapped":
		return true
	default:
		return false
	}
}

// DescendantArgvMatch walks a bounded process tree below rootPID and reports
// whether any live descendant argv satisfies match. A process must retain the
// parent and start-time identity observed with the tree before its cmdline is
// trusted, preventing PID-reuse and parentage TOCTOU matches.
func DescendantArgvMatch(procRoot string, rootPID int, match func([]string) bool) (bool, error) {
	return DescendantArgvMatchContext(context.Background(), procRoot, rootPID, match)
}

func DescendantArgvMatchContext(ctx context.Context, procRoot string, rootPID int, match func([]string) bool) (bool, error) {
	return descendantArgvMatch(ctx, procRoot, rootPID, match, nil)
}

func descendantArgvMatch(ctx context.Context, procRoot string, rootPID int, match func([]string) bool, afterSnapshot func()) (bool, error) {
	if rootPID <= 0 || match == nil {
		return false, nil
	}
	root := normalizedProcRoot(procRoot)
	table, children, complete, err := processTable(ctx, root, rootPID)
	if err != nil || !complete {
		return false, err
	}
	if afterSnapshot != nil {
		afterSnapshot()
	}
	identity, ok := table[rootPID]
	if !ok {
		return false, nil
	}
	current, err := readProcessIdentity(root, rootPID)
	if err != nil || current != identity {
		return false, nil
	}
	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]struct{}{}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		pid := queue[0]
		queue = queue[1:]
		if _, ok := seen[pid]; ok {
			return false, nil
		}
		seen[pid] = struct{}{}
		identity, ok := table[pid]
		if !ok {
			return false, nil
		}
		current, err := readProcessIdentity(root, pid)
		if err != nil || current != identity {
			return false, nil
		}
		args, err := readProcArgs(root, pid)
		if err != nil {
			return false, nil
		}
		after, statErr := readProcessIdentity(root, pid)
		if statErr != nil || after != identity {
			return false, nil
		}
		if match(args) {
			return true, nil
		}
		queue = append(queue, children[pid]...)
	}
	return false, nil
}

// DescendantPIDs returns descendants with leaves before parents, suitable for
// bounded process-tree cleanup.
func DescendantPIDs(procRoot string, rootPID int) ([]int, error) {
	_, children, complete, err := processTable(context.Background(), normalizedProcRoot(procRoot), rootPID)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, errors.New("process tree observation incomplete")
	}
	queue := append([]int(nil), children[rootPID]...)
	seen := map[int]struct{}{}
	out := make([]int, 0, len(queue))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, ok := seen[pid]; ok {
			return nil, errors.New("process tree contains duplicate or cyclic edge")
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out, nil
}

func normalizedProcRoot(root string) string {
	if root = strings.TrimSpace(root); root == "" {
		return "/proc"
	}
	return root
}

// processTable builds one bounded snapshot from explicit per-task child edges.
// complete is false for volatile/malformed proc metadata or exceeded bounds.
func processTable(ctx context.Context, root string, rootPID int) (map[int]processIdentity, map[int][]int, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	table := make(map[int]processIdentity)
	children := make(map[int][]int)
	queue := []processNode{{pid: rootPID, depth: 0}}
	seen := make(map[int]struct{})
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, err
		}
		if len(seen) >= maxProcessNodes {
			return table, children, false, nil
		}
		current := queue[0]
		queue = queue[1:]
		if _, duplicate := seen[current.pid]; duplicate {
			return table, children, false, nil
		}
		seen[current.pid] = struct{}{}
		identity, err := readProcessIdentity(root, current.pid)
		if err != nil || (current.parent > 0 && identity.parentPID != current.parent) {
			return table, children, false, nil
		}
		table[current.pid] = identity
		childPIDs, err := readProcessChildren(root, current.pid)
		if err != nil {
			return table, children, false, nil
		}
		children[current.pid] = childPIDs
		if current.depth >= maxProcessDepth {
			if len(childPIDs) > 0 {
				return table, children, false, nil
			}
			continue
		}
		if len(seen)+len(queue)+len(childPIDs) > maxProcessNodes {
			return table, children, false, nil
		}
		for _, child := range childPIDs {
			queue = append(queue, processNode{pid: child, parent: current.pid, depth: current.depth + 1})
		}
	}
	return table, children, true, nil
}

func readProcessChildren(root string, pid int) ([]int, error) {
	taskRoot := filepath.Join(root, strconv.Itoa(pid), "task")
	tasks, err := readDirBounded(taskRoot, maxProcessTasks)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{})
	for _, task := range tasks {
		if !task.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(task.Name()); err != nil {
			return nil, fmt.Errorf("invalid task ID %q", task.Name())
		}
		payload, err := readBoundedFile(filepath.Join(taskRoot, task.Name(), "children"))
		if err != nil {
			return nil, err
		}
		taskSeen := make(map[int]struct{})
		for _, field := range strings.Fields(string(payload)) {
			child, err := strconv.Atoi(field)
			if err != nil || child <= 0 {
				return nil, fmt.Errorf("invalid child PID %q", field)
			}
			if _, duplicate := taskSeen[child]; duplicate {
				return nil, fmt.Errorf("duplicate child PID %d", child)
			}
			taskSeen[child] = struct{}{}
			seen[child] = struct{}{}
		}
	}
	children := make([]int, 0, len(seen))
	for child := range seen {
		children = append(children, child)
	}
	sort.Ints(children)
	return children, nil
}

func readDirBounded(path string, limit int) ([]os.DirEntry, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("task bound exceeded")
	}
	return entries, nil
}

func readProcessIdentity(root string, pid int) (processIdentity, error) {
	payload, err := readBoundedFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return processIdentity{}, err
	}
	return parseProcessIdentity(string(payload))
}

func parseProcessIdentity(stat string) (processIdentity, error) {
	idx := strings.LastIndex(stat, ")")
	if idx < 0 || idx+2 >= len(stat) {
		return processIdentity{}, fmt.Errorf("unexpected stat format")
	}
	fields := strings.Fields(stat[idx+2:])
	if len(fields) < 2 {
		return processIdentity{}, fmt.Errorf("unexpected stat fields")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return processIdentity{}, fmt.Errorf("invalid parent PID")
	}
	// Linux starttime is field 22, or index 19 when fields begin at state (3).
	// Minimal fixture stat rows use the entire suffix as a stable identity.
	startTime := strings.Join(fields, " ")
	if len(fields) > 19 {
		startTime = fields[19]
	}
	return processIdentity{parentPID: ppid, startTime: startTime}, nil
}

func readProcArgs(root string, pid int) ([]string, error) {
	payload, err := readBoundedFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(payload) {
		return nil, errors.New("process metadata is not valid UTF-8")
	}
	if len(payload) == 0 {
		return nil, nil
	}
	parts := bytes.Split(payload, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	out := make([]string, len(parts))
	for i, part := range parts {
		out[i] = string(part)
	}
	return out, nil
}

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxProcessMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxProcessMetadataBytes {
		return nil, errors.New("process metadata exceeds bound")
	}
	return payload, nil
}
