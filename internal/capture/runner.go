package capture

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/bootid"
	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/storelock"
)

type Collector interface {
	Collect(ctx context.Context) (model.State, error)
}

type CheckpointStore interface {
	Write(checkpoint checkpoints.Checkpoint) (string, error)
}

type Config struct {
	Collector       Collector
	CheckpointStore CheckpointStore
	StateDir        string
	BootIDSource    bootid.Source
	Host            string
	Profile         string
	Now             func() time.Time
	Logger          io.Writer
}

type Runner struct {
	collector       Collector
	checkpointStore CheckpointStore
	stateDir        string
	bootIDSource    bootid.Source
	host            string
	profile         string
	now             func() time.Time
	logger          io.Writer
}

type Result struct {
	CheckpointPath string
	StateHash      string
}

func NewRunner(config Config) *Runner {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	logger := config.Logger
	if logger == nil {
		logger = io.Discard
	}
	bootIDSource := config.BootIDSource
	if bootIDSource == nil {
		bootIDSource = bootid.Current
	}
	return &Runner{
		collector:       config.Collector,
		checkpointStore: config.CheckpointStore,
		stateDir:        strings.TrimSpace(config.StateDir),
		bootIDSource:    bootIDSource,
		host:            strings.TrimSpace(config.Host),
		profile:         strings.TrimSpace(config.Profile),
		now:             now,
		logger:          logger,
	}
}

// CaptureOnce performs one complete query and atomically replaces this boot's
// rolling checkpoint while holding the repository's single-writer lock.
func (r *Runner) CaptureOnce(ctx context.Context) (Result, error) {
	if r.checkpointStore == nil {
		return Result{}, fmt.Errorf("rolling checkpoint store is unavailable")
	}
	if r.stateDir == "" {
		return Result{}, fmt.Errorf("state directory is required")
	}

	// Collection is part of the serialized write transaction. Otherwise a slow
	// capture can observe old state, wait behind a newer capture, and then
	// overwrite the newer checkpoint with a later publication timestamp.
	lock, err := storelock.Acquire(r.stateDir)
	if err != nil {
		return Result{}, fmt.Errorf("acquire checkpoint writer lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	state, err := r.collector.Collect(ctx)
	if err != nil {
		return Result{}, err
	}
	state = model.Normalize(state)
	stateHash, err := state.Hash()
	if err != nil {
		return Result{}, err
	}

	bootID, err := r.bootIDSource()
	if err != nil {
		return Result{}, fmt.Errorf("read boot ID: %w", err)
	}
	bootID = strings.TrimSpace(bootID)
	if bootID == "" {
		return Result{}, fmt.Errorf("boot ID is empty")
	}

	path, err := r.checkpointStore.Write(checkpoints.Checkpoint{
		V:          checkpoints.SchemaVersion,
		BootID:     bootID,
		Host:       r.host,
		Profile:    r.profile,
		ObservedAt: r.now().UTC(),
		State:      state,
		StateHash:  stateHash,
	})
	if err != nil {
		return Result{}, fmt.Errorf("publish rolling checkpoint: %w", err)
	}
	return Result{CheckpointPath: path, StateHash: stateHash}, nil
}

func (r *Runner) CaptureRun(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
			if _, err := r.CaptureOnce(ctx); err != nil {
				_, _ = fmt.Fprintf(r.logger, "capture_once_error err=%q\n", err.Error())
			}
		}
	}
}
