package prune

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
)

var ErrActiveWriter = errors.New("active writer lock present")

type Runner struct {
	root string
	days int
	now  func() time.Time
}

type Summary struct {
	EventsPruned    int
	SnapshotsPruned int
}

func NewRunner(root string, days int, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	return &Runner{root: root, days: days, now: now}
}

func (r *Runner) Run() (Summary, error) {
	if _, err := os.Stat(filepath.Join(r.root, "meta", "lock")); err == nil {
		return Summary{}, ErrActiveWriter
	}

	cutoff := r.now().UTC().AddDate(0, 0, -r.days)
	eventsPruned, err := r.pruneEvents(cutoff)
	if err != nil {
		return Summary{}, err
	}
	snapshotsPruned, err := r.pruneSnapshots(cutoff)
	if err != nil {
		return Summary{}, err
	}

	return Summary{EventsPruned: eventsPruned, SnapshotsPruned: snapshotsPruned}, nil
}

func (r *Runner) pruneEvents(cutoff time.Time) (int, error) {
	eventsPath := filepath.Join(r.root, "events.jsonl")
	f, err := os.Open(eventsPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open events: %w", err)
	}
	defer f.Close()

	kept := make([]events.Event, 0)
	var anchor *events.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if err := event.Validate(); err != nil {
			continue
		}
		if event.TS.Before(cutoff) {
			e := event
			anchor = &e
			continue
		}
		kept = append(kept, event)
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan events: %w", err)
	}

	if anchor != nil {
		kept = append([]events.Event{*anchor}, kept...)
	}

	originalCount := countValidEvents(filepath.Join(r.root, "events.jsonl"))
	if err := rewriteEvents(eventsPath, kept); err != nil {
		return 0, err
	}

	return max(0, originalCount-len(kept)), nil
}

func (r *Runner) pruneSnapshots(cutoff time.Time) (int, error) {
	dir := filepath.Join(r.root, "snapshots")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read snapshots dir: %w", err)
	}

	type snapshotFile struct {
		path string
		ts   time.Time
	}
	all := make([]snapshotFile, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		base := entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]
		unix, parseErr := strconv.ParseInt(base, 10, 64)
		if parseErr != nil {
			continue
		}
		all = append(all, snapshotFile{path: filepath.Join(dir, entry.Name()), ts: time.Unix(unix, 0).UTC()})
	}

	if len(all) == 0 {
		return 0, nil
	}

	sort.Slice(all, func(i, j int) bool { return all[i].ts.Before(all[j].ts) })

	keep := map[string]struct{}{all[len(all)-1].path: {}}
	for i := len(all) - 1; i >= 0; i-- {
		if !all[i].ts.After(cutoff) {
			keep[all[i].path] = struct{}{}
			break
		}
	}

	pruned := 0
	for _, snap := range all {
		if _, ok := keep[snap.path]; ok {
			continue
		}
		if err := os.Remove(snap.path); err != nil {
			return 0, err
		}
		pruned++
	}

	return pruned, nil
}

func rewriteEvents(path string, kept []events.Event) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, event := range kept {
		payload, err := json.Marshal(event)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write(append(payload, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func countValidEvents(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if err := event.Validate(); err != nil {
			continue
		}
		count++
	}
	return count
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
