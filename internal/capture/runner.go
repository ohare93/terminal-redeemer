package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jmo/terminal-redeemer/internal/diff"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

type Collector interface {
	Collect(ctx context.Context) (model.State, error)
}

type EventStore interface {
	AcquireWriter() (*events.Writer, error)
}

type SnapshotStore interface {
	Write(snapshot snapshots.Snapshot) (string, error)
}

type Config struct {
	Collector     Collector
	DiffEngine    *diff.Engine
	EventStore    EventStore
	SnapshotStore SnapshotStore
	SnapshotEvery int
	Host          string
	Profile       string
	Source        string
	Now           func() time.Time
	Logger        io.Writer
}

type Runner struct {
	collector     Collector
	diffEngine    *diff.Engine
	eventStore    EventStore
	snapshotStore SnapshotStore
	snapshotEvery int
	host          string
	profile       string
	source        string
	now           func() time.Time
	logger        io.Writer

	lastState  model.State
	hasLast    bool
	eventCount int
}

type Result struct {
	EventsWritten int
	SnapshotPath  string
	StateHash     string
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

	return &Runner{
		collector:     config.Collector,
		diffEngine:    config.DiffEngine,
		eventStore:    config.EventStore,
		snapshotStore: config.SnapshotStore,
		snapshotEvery: config.SnapshotEvery,
		host:          config.Host,
		profile:       config.Profile,
		source:        config.Source,
		now:           now,
		logger:        logger,
	}
}

func (r *Runner) CaptureOnce(ctx context.Context) (Result, error) {
	state, err := r.collector.Collect(ctx)
	if err != nil {
		return Result{}, err
	}

	before := model.State{}
	if r.hasLast {
		before = r.lastState
	}

	patches, changed, err := r.diffEngine.Diff(before, state)
	if err != nil {
		return Result{}, err
	}
	if !changed {
		r.lastState = state
		r.hasLast = true
		stateHash, hashErr := state.Hash()
		if hashErr != nil {
			return Result{}, hashErr
		}
		return Result{StateHash: stateHash}, nil
	}

	writer, err := r.eventStore.AcquireWriter()
	if err != nil {
		return Result{}, err
	}
	defer writer.Close()

	now := r.now().UTC()
	stateHash, err := state.Hash()
	if err != nil {
		return Result{}, err
	}

	var lastOffset int64
	for _, patch := range patches {
		lastOffset, err = writer.Append(events.Event{
			V:         1,
			TS:        now,
			Host:      r.host,
			Profile:   r.profile,
			EventType: "window_patch",
			WindowKey: patch.WindowKey,
			Patch:     patch.Fields,
			Source:    r.source,
			StateHash: stateHash,
		})
		if err != nil {
			return Result{}, err
		}
	}

	r.eventCount += len(patches)
	result := Result{EventsWritten: len(patches), StateHash: stateHash}
	if snapshots.ShouldSnapshot(r.eventCount, r.snapshotEvery) {
		snapshotPath, err := r.snapshotStore.Write(snapshots.Snapshot{
			V:               1,
			CreatedAt:       now,
			Host:            r.host,
			Profile:         r.profile,
			LastEventOffset: lastOffset,
			StateHash:       stateHash,
			State:           stateAsMap(state),
		})
		if err != nil {
			return Result{}, err
		}
		result.SnapshotPath = snapshotPath
	}

	r.lastState = state
	r.hasLast = true
	return result, nil
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

func stateAsMap(state model.State) map[string]any {
	payload, err := json.Marshal(state)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(payload, &out); err != nil {
		return map[string]any{}
	}
	return out
}
