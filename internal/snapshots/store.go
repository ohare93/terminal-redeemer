package snapshots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrNoSnapshot = errors.New("no snapshot at or before timestamp")

type Snapshot struct {
	V               int            `json:"v"`
	CreatedAt       time.Time      `json:"created_at"`
	Host            string         `json:"host"`
	Profile         string         `json:"profile"`
	LastEventOffset int64          `json:"last_event_offset"`
	State           map[string]any `json:"state"`
	StateHash       string         `json:"state_hash"`
}

func (s Snapshot) Validate() error {
	if s.V != 1 {
		return fmt.Errorf("invalid version: %d", s.V)
	}
	if s.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	if strings.TrimSpace(s.Host) == "" {
		return errors.New("host is required")
	}
	if strings.TrimSpace(s.Profile) == "" {
		return errors.New("profile is required")
	}
	if strings.TrimSpace(s.StateHash) == "" {
		return errors.New("state_hash is required")
	}
	if s.LastEventOffset < 0 {
		return errors.New("last_event_offset must be >= 0")
	}
	return nil
}

type Store struct {
	dir string
}

func NewStore(root string) (*Store, error) {
	dir := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Write(snapshot Snapshot) (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}

	path := filepath.Join(s.dir, fmt.Sprintf("%d.json", snapshot.CreatedAt.Unix()))
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}

	return path, nil
}

func (s *Store) Read(path string) (Snapshot, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}

	return snapshot, nil
}

func (s *Store) LoadNearest(at time.Time) (Snapshot, string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return Snapshot{}, "", fmt.Errorf("read snapshots dir: %w", err)
	}

	var bestTS int64 = -1
	var bestPath string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		ts, err := parseSnapshotUnix(entry.Name())
		if err != nil {
			continue
		}
		if ts <= at.Unix() && ts > bestTS {
			bestTS = ts
			bestPath = filepath.Join(s.dir, entry.Name())
		}
	}

	if bestTS < 0 {
		return Snapshot{}, "", ErrNoSnapshot
	}

	snapshot, err := s.Read(bestPath)
	if err != nil {
		return Snapshot{}, "", err
	}

	return snapshot, bestPath, nil
}

func ShouldSnapshot(totalEvents int, snapshotEvery int) bool {
	if totalEvents <= 0 || snapshotEvery <= 0 {
		return false
	}
	return totalEvents%snapshotEvery == 0
}

func parseSnapshotUnix(name string) (int64, error) {
	base := strings.TrimSuffix(name, ".json")
	return strconv.ParseInt(base, 10, 64)
}
