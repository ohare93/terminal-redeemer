package collector

import (
	"context"

	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/niri"
)

type Snapshotter interface {
	Snapshot(ctx context.Context) ([]byte, error)
}

type Enricher interface {
	EnrichWindow(window model.Window) (model.Window, error)
}

type Collector struct {
	snapshotter Snapshotter
	enricher    Enricher
}

func New(snapshotter Snapshotter, enricher Enricher) *Collector {
	return &Collector{snapshotter: snapshotter, enricher: enricher}
}

func (c *Collector) Collect(ctx context.Context) (model.State, error) {
	raw, err := c.snapshotter.Snapshot(ctx)
	if err != nil {
		return model.State{}, err
	}

	state, err := niri.ParseSnapshot(raw)
	if err != nil {
		return model.State{}, err
	}

	if c.enricher == nil {
		return state, nil
	}

	for i := range state.Windows {
		enriched, err := c.enricher.EnrichWindow(state.Windows[i])
		if err != nil {
			continue
		}
		state.Windows[i] = enriched
	}

	return model.Normalize(state), nil
}
