// Package checkpoints stores one crash-durable rolling full-state checkpoint
// for each boot, host, and profile identity.
package checkpoints

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
)

var (
	ErrNotFound = errors.New("rolling checkpoint not found")
	ErrInvalid  = errors.New("invalid rolling checkpoint")
)

// Checkpoint is the latest complete state for one boot/host/profile identity.
type Checkpoint struct {
	BootID        string                  `json:"boot_id"`
	Host          string                  `json:"host"`
	Profile       string                  `json:"profile"`
	ObservedAt    time.Time               `json:"observed_at"`
	State         model.State             `json:"state"`
	StateHash     string                  `json:"state_hash"`
	Recovery      model.RecoveryInventory `json:"recovery"`
	IntegrityHash string                  `json:"integrity_hash"`
}

func (c Checkpoint) Validate() error {
	if strings.TrimSpace(c.BootID) == "" {
		return errors.New("boot_id is required")
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("host is required")
	}
	if strings.TrimSpace(c.Profile) == "" {
		return errors.New("profile is required")
	}
	if c.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	if strings.TrimSpace(c.StateHash) == "" {
		return errors.New("state_hash is required")
	}
	hash, err := c.State.Hash()
	if err != nil {
		return fmt.Errorf("hash state: %w", err)
	}
	if hash != c.StateHash {
		return fmt.Errorf("state_hash mismatch: got %q want %q", c.StateHash, hash)
	}
	if err := validateRecovery(c.Recovery); err != nil {
		return fmt.Errorf("recovery inventory: %w", err)
	}
	want, err := RecoveryIntegrityHash(c.State, c.Recovery)
	if err != nil {
		return fmt.Errorf("hash recovery payload: %w", err)
	}
	if c.IntegrityHash != want {
		return fmt.Errorf("integrity_hash mismatch: got %q want %q", c.IntegrityHash, want)
	}
	return nil
}

func validateRecovery(recovery model.RecoveryInventory) error {
	if len(recovery.ActiveSessions) != len(recovery.Sessions) {
		return errors.New("active allow-list and session metadata differ in length")
	}
	active := make(map[string]bool, len(recovery.ActiveSessions))
	for _, name := range recovery.ActiveSessions {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || active[name] {
			return fmt.Errorf("invalid or duplicate active session %q", name)
		}
		active[name] = true
	}
	seen := make(map[string]bool, len(recovery.Sessions))
	for _, session := range recovery.Sessions {
		if !active[session.Name] || seen[session.Name] {
			return fmt.Errorf("metadata session %q is not unique in the active allow-list", session.Name)
		}
		seen[session.Name] = true
		if session.PlacementObservedAt != nil && session.PlacementObservedAt.IsZero() {
			return fmt.Errorf("session %q has an empty placement observation time", session.Name)
		}
		if session.CapturedColumnOccupied {
			if session.PlacementObservedAt == nil || session.WorkspaceRef == nil || session.Placement == nil || session.Placement.Column == nil || session.Placement.Row == nil || *session.Placement.Row != 0 {
				return fmt.Errorf("session %q has column occupancy without a complete row-zero placement observation", session.Name)
			}
		}
	}
	return nil
}

// RecoveryIntegrityHash binds normalized semantic compositor state and every
// field in the normalized active recovery inventory.
func RecoveryIntegrityHash(state model.State, recovery model.RecoveryInventory) (string, error) {
	normalizedState := model.Normalize(state)
	for i := range normalizedState.Windows {
		normalizedState.Windows[i].Title = ""
	}
	payload, err := json.Marshal(struct {
		State    model.State             `json:"state"`
		Recovery model.RecoveryInventory `json:"recovery"`
	}{State: normalizedState, Recovery: model.NormalizeRecovery(recovery)})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type Issue struct {
	Path string
	Err  error
}

type Store struct {
	root     string
	dir      string
	syncFile func(*os.File) error
	syncDir  func(string) error
	rename   func(string, string) error
}

func NewStore(root string) (*Store, error) {
	dir := filepath.Join(root, "checkpoints")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create rolling checkpoints dir: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		return nil, fmt.Errorf("sync state dir: %w", err)
	}
	return &Store{
		root:     root,
		dir:      dir,
		syncFile: func(file *os.File) error { return file.Sync() },
		syncDir:  syncDirectory,
		rename:   os.Rename,
	}, nil
}

func pathName(bootID, host, profile string) string {
	identity, _ := json.Marshal([]string{bootID, host, profile})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *Store) Path(bootID, host, profile string) string {
	return filepath.Join(s.dir, pathName(bootID, host, profile))
}

// History returns valid checkpoints for a host/profile in oldest-first order.
// Invalid files do not displace the newest valid recovery source.
func (s *Store) History(host, profile string) ([]Checkpoint, error) {
	all, _, err := List(s.root)
	if err != nil {
		return nil, err
	}
	out := make([]Checkpoint, 0, len(all))
	for _, checkpoint := range all {
		if checkpoint.Host == host && checkpoint.Profile == profile {
			out = append(out, checkpoint)
		}
	}
	return out, nil
}

func (s *Store) Read(bootID, host, profile string) (Checkpoint, error) {
	path := s.Path(bootID, host, profile)
	checkpoint, err := readPath(path)
	if errors.Is(err, os.ErrNotExist) {
		return Checkpoint{}, ErrNotFound
	}
	if err != nil {
		return Checkpoint{}, err
	}
	if checkpoint.BootID != bootID || checkpoint.Host != host || checkpoint.Profile != profile {
		return Checkpoint{}, fmt.Errorf("%w: identity does not match deterministic path", ErrInvalid)
	}
	return checkpoint, nil
}

func readPath(path string) (Checkpoint, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Checkpoint{}, err
	}
	var checkpoint Checkpoint
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: decode %s: %v", ErrInvalid, filepath.Base(path), err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Checkpoint{}, fmt.Errorf("%w: decode %s: trailing JSON value", ErrInvalid, filepath.Base(path))
	}
	checkpoint.State = model.Normalize(checkpoint.State)
	checkpoint.Recovery = model.NormalizeRecovery(checkpoint.Recovery)
	if err := checkpoint.Validate(); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: validate %s: %v", ErrInvalid, filepath.Base(path), err)
	}
	return checkpoint, nil
}

// Write replaces the identity's rolling checkpoint using temp-write, file
// fsync, atomic rename, and directory fsync. Callers hold the repository's
// single-writer lock for the checkpoint mutation.
func (s *Store) Write(checkpoint Checkpoint) (path string, err error) {
	checkpoint.ObservedAt = checkpoint.ObservedAt.UTC()
	checkpoint.State = model.Normalize(checkpoint.State)
	checkpoint.Recovery = model.NormalizeRecovery(checkpoint.Recovery)
	if err := checkpoint.Validate(); err != nil {
		return "", fmt.Errorf("validate rolling checkpoint: %w", err)
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return "", fmt.Errorf("marshal rolling checkpoint: %w", err)
	}
	payload = append(payload, '\n')

	path = s.Path(checkpoint.BootID, checkpoint.Host, checkpoint.Profile)
	if _, readErr := s.Read(checkpoint.BootID, checkpoint.Host, checkpoint.Profile); readErr != nil && !errors.Is(readErr, ErrNotFound) {
		return "", fmt.Errorf("refuse to replace invalid rolling checkpoint: %w", readErr)
	}

	tmp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create rolling checkpoint temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	written, err := tmp.Write(payload)
	if err != nil {
		return "", fmt.Errorf("write rolling checkpoint temp file: %w", err)
	}
	if written != len(payload) {
		return "", io.ErrShortWrite
	}
	if err := s.syncFile(tmp); err != nil {
		return "", fmt.Errorf("sync rolling checkpoint temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close rolling checkpoint temp file: %w", err)
	}
	if err := s.rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("rename rolling checkpoint: %w", err)
	}
	if err := s.syncDir(s.dir); err != nil {
		return "", fmt.Errorf("sync rolling checkpoints dir: %w", err)
	}
	return path, nil
}

// List reads every valid rolling checkpoint. A malformed checkpoint is
// reported as an issue without hiding other valid rolling checkpoints.
func List(root string) ([]Checkpoint, []Issue, error) {
	dir := filepath.Join(root, "checkpoints")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read rolling checkpoints dir: %w", err)
	}
	out := make([]Checkpoint, 0, len(entries))
	issues := make([]Issue, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		checkpoint, readErr := readPath(path)
		if readErr != nil {
			issues = append(issues, Issue{Path: path, Err: readErr})
			continue
		}
		if entry.Name() != pathName(checkpoint.BootID, checkpoint.Host, checkpoint.Profile) {
			issues = append(issues, Issue{Path: path, Err: fmt.Errorf("%w: identity does not match filename", ErrInvalid)})
			continue
		}
		out = append(out, checkpoint)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ObservedAt.Before(out[j].ObservedAt)
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].BootID < out[j].BootID
	})
	return out, issues, nil
}

// Prune removes expired rolling checkpoints while retaining the current boot
// and the newest usable prior-boot checkpoint for every host/profile identity.
// The caller holds the repository's single-writer lock.
func Prune(root string, cutoff time.Time, currentBootID string) (int, error) {
	currentBootID = strings.TrimSpace(currentBootID)
	if currentBootID == "" {
		return 0, errors.New("current boot ID is required to prune rolling checkpoints")
	}

	dir := filepath.Join(root, "checkpoints")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read rolling checkpoints dir: %w", err)
	}

	type identity struct {
		host    string
		profile string
	}
	type candidate struct {
		path       string
		checkpoint Checkpoint
	}
	candidates := make([]candidate, 0, len(entries))
	newestPrior := make(map[identity]candidate)
	protected := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		checkpoint, readErr := readPath(path)
		if readErr != nil || entry.Name() != pathName(checkpoint.BootID, checkpoint.Host, checkpoint.Profile) {
			continue
		}
		entryCandidate := candidate{path: path, checkpoint: checkpoint}
		candidates = append(candidates, entryCandidate)
		if checkpoint.BootID == currentBootID {
			protected[path] = true
			continue
		}
		key := identity{host: checkpoint.Host, profile: checkpoint.Profile}
		previous, ok := newestPrior[key]
		if !ok || checkpoint.ObservedAt.After(previous.checkpoint.ObservedAt) ||
			(checkpoint.ObservedAt.Equal(previous.checkpoint.ObservedAt) && checkpoint.BootID > previous.checkpoint.BootID) {
			newestPrior[key] = entryCandidate
		}
	}
	for _, entry := range newestPrior {
		protected[entry.path] = true
	}

	removed := 0
	for _, entry := range candidates {
		if protected[entry.path] || !entry.checkpoint.ObservedAt.Before(cutoff) {
			continue
		}
		if err := os.Remove(entry.path); err != nil {
			return removed, fmt.Errorf("remove expired rolling checkpoint %s: %w", filepath.Base(entry.path), err)
		}
		removed++
	}
	if removed > 0 {
		if err := syncDirectory(dir); err != nil {
			return removed, fmt.Errorf("sync rolling checkpoints dir after prune: %w", err)
		}
	}
	return removed, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
