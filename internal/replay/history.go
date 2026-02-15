package replay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmo/terminal-redeemer/internal/events"
)

func ListEvents(root string, from *time.Time, to *time.Time) ([]events.Event, error) {
	path := filepath.Join(root, "events.jsonl")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open events file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	out := make([]events.Event, 0)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if err := event.Validate(); err != nil {
			continue
		}
		if from != nil && event.TS.Before(*from) {
			continue
		}
		if to != nil && event.TS.After(*to) {
			continue
		}
		out = append(out, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return out, nil
}
