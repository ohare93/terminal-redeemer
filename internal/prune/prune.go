package prune

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/bootid"
	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/storelock"
)

var ErrActiveWriter = errors.New("active writer lock present")

type Runner struct {
	root         string
	days         int
	now          func() time.Time
	bootIDSource bootid.Source
}

type Summary struct {
	CheckpointsPruned int
}

func NewRunner(root string, days int, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	return &Runner{root: root, days: days, now: now, bootIDSource: bootid.Current}
}

func (r *Runner) Run() (Summary, error) {
	lock, err := storelock.Acquire(r.root)
	if errors.Is(err, storelock.ErrLocked) {
		return Summary{}, ErrActiveWriter
	}
	if err != nil {
		return Summary{}, fmt.Errorf("acquire prune lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	currentBootID, err := r.bootIDSource()
	if err != nil {
		return Summary{}, fmt.Errorf("read current boot ID: %w", err)
	}
	currentBootID = strings.TrimSpace(currentBootID)
	if currentBootID == "" {
		return Summary{}, errors.New("current boot ID is empty")
	}

	cutoff := r.now().UTC().AddDate(0, 0, -r.days)
	removed, err := checkpoints.Prune(r.root, cutoff, currentBootID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{CheckpointsPruned: removed}, nil
}
