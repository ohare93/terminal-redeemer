package events

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrLocked = errors.New("event store is locked")

type Event struct {
	V         int            `json:"v"`
	TS        time.Time      `json:"ts"`
	Host      string         `json:"host"`
	Profile   string         `json:"profile"`
	EventType string         `json:"event_type"`
	WindowKey string         `json:"window_key,omitempty"`
	Patch     map[string]any `json:"patch,omitempty"`
	Source    string         `json:"source,omitempty"`
	StateHash string         `json:"state_hash"`
}

func (e Event) Validate() error {
	if e.V != 1 {
		return fmt.Errorf("invalid version: %d", e.V)
	}
	if e.TS.IsZero() {
		return errors.New("ts is required")
	}
	if strings.TrimSpace(e.Host) == "" {
		return errors.New("host is required")
	}
	if strings.TrimSpace(e.Profile) == "" {
		return errors.New("profile is required")
	}
	if strings.TrimSpace(e.EventType) == "" {
		return errors.New("event_type is required")
	}
	if strings.TrimSpace(e.StateHash) == "" {
		return errors.New("state_hash is required")
	}
	return nil
}

type Store struct {
	eventsPath string
	lockPath   string
}

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		return nil, fmt.Errorf("create meta dir: %w", err)
	}

	eventsPath := filepath.Join(root, "events.jsonl")
	if _, err := os.Stat(eventsPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
			return nil, fmt.Errorf("create events file: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat events file: %w", err)
	}

	return &Store{
		eventsPath: eventsPath,
		lockPath:   filepath.Join(root, "meta", "lock"),
	}, nil
}

type Writer struct {
	lockPath string
	file     *os.File
}

func (s *Store) AcquireWriter() (*Writer, error) {
	lockFile, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, fmt.Errorf("create lock file: %w", err)
	}
	if _, err := fmt.Fprintf(lockFile, "%d\n", os.Getpid()); err != nil {
		_ = lockFile.Close()
		_ = os.Remove(s.lockPath)
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	_ = lockFile.Close()

	eventsFile, err := os.OpenFile(s.eventsPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.Remove(s.lockPath)
		return nil, fmt.Errorf("open events file: %w", err)
	}

	return &Writer{lockPath: s.lockPath, file: eventsFile}, nil
}

func (w *Writer) Append(event Event) (int64, error) {
	if err := event.Validate(); err != nil {
		return 0, err
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return 0, fmt.Errorf("marshal event: %w", err)
	}
	payload = append(payload, '\n')

	if _, err := w.file.Write(payload); err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}

	offset, err := w.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fmt.Errorf("seek current: %w", err)
	}

	return offset, nil
}

func (w *Writer) Close() error {
	errFile := w.file.Close()
	errLock := os.Remove(w.lockPath)
	if errFile != nil {
		return errFile
	}
	if errLock != nil && !errors.Is(errLock, os.ErrNotExist) {
		return errLock
	}
	return nil
}

func (s *Store) ReadSince(cursor int64) ([]Event, int64, error) {
	f, err := os.Open(s.eventsPath)
	if err != nil {
		return nil, cursor, fmt.Errorf("open events file: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(cursor, io.SeekStart); err != nil {
		return nil, cursor, fmt.Errorf("seek to cursor: %w", err)
	}

	var out []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, cursor, fmt.Errorf("decode event: %w", err)
		}
		if err := event.Validate(); err != nil {
			return nil, cursor, fmt.Errorf("validate event: %w", err)
		}
		out = append(out, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, cursor, fmt.Errorf("scan events: %w", err)
	}

	nextCursor, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, cursor, fmt.Errorf("read current cursor: %w", err)
	}

	return out, nextCursor, nil
}
