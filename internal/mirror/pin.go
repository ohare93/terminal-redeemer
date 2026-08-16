package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/jmo/terminal-redeemer/internal/storelock"
)

const (
	PinSchemaVersion = 1
	maxPinBytes      = 2 << 20
)

var (
	ErrPinNotFound = errors.New("mirror pin not found")
	ErrPinInvalid  = errors.New("invalid mirror pin")
)

type WorkspaceSelector struct {
	Name  string `json:"name,omitempty"`
	Index int    `json:"index,omitempty"`
}

type PinnedProjection struct {
	Session    string            `json:"session"`
	RemoteCWD  string            `json:"remote_cwd,omitempty"`
	Workspace  WorkspaceSelector `json:"workspace"`
	Order      int               `json:"order"`
	IsFloating bool              `json:"is_floating"`
	TileSize   []float64         `json:"tile_size,omitempty"`
	WindowSize []int             `json:"window_size,omitempty"`
}

type Pin struct {
	V             int                `json:"v"`
	SourceHost    string             `json:"source_host"`
	SourceProfile string             `json:"source_profile"`
	Projections   []PinnedProjection `json:"projections"`
}

func (pin Pin) Validate() error {
	if pin.V != PinSchemaVersion {
		return fmt.Errorf("schema version is %d, want %d", pin.V, PinSchemaVersion)
	}
	if err := ValidateDestination(pin.SourceHost); err != nil {
		return err
	}
	if err := validateText("source profile", pin.SourceProfile, 128, false); err != nil {
		return err
	}
	if pin.Projections == nil || len(pin.Projections) > 256 {
		return fmt.Errorf("projection count must be between 0 and 256")
	}
	seenSessions := make(map[string]struct{}, len(pin.Projections))
	seenOrders := make(map[int]struct{}, len(pin.Projections))
	for i, item := range pin.Projections {
		if err := ValidateSession(item.Session); err != nil {
			return fmt.Errorf("projection %d: %w", i, err)
		}
		if _, exists := seenSessions[item.Session]; exists {
			return fmt.Errorf("projection %d duplicates exact session %q", i, item.Session)
		}
		seenSessions[item.Session] = struct{}{}
		if item.Order < 0 || item.Order > 1_000_000 {
			return fmt.Errorf("projection %d has invalid order", i)
		}
		if _, exists := seenOrders[item.Order]; exists {
			return fmt.Errorf("projection %d duplicates order %d", i, item.Order)
		}
		seenOrders[item.Order] = struct{}{}
		if err := validateText("remote cwd", item.RemoteCWD, 4096, true); err != nil {
			return fmt.Errorf("projection %d: %w", i, err)
		}
		if err := validateText("workspace name", item.Workspace.Name, 256, true); err != nil {
			return fmt.Errorf("projection %d: %w", i, err)
		}
		if item.Workspace.Index < 0 || item.Workspace.Index > 1_000_000 || (item.Workspace.Name == "" && item.Workspace.Index == 0) {
			return fmt.Errorf("projection %d has no valid workspace selector", i)
		}
		if err := validateFloatSize(item.TileSize); err != nil {
			return fmt.Errorf("projection %d tile size: %w", i, err)
		}
		if err := validateIntSize(item.WindowSize); err != nil {
			return fmt.Errorf("projection %d window size: %w", i, err)
		}
	}
	return nil
}

func validateText(name, value string, limit int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || len(value) > limit || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}

func validateFloatSize(values []float64) error {
	if len(values) != 0 && len(values) != 2 {
		return errors.New("must contain exactly two values")
	}
	for _, value := range values {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > 1_000_000 {
			return errors.New("contains an invalid value")
		}
	}
	return nil
}

func validateIntSize(values []int) error {
	if len(values) != 0 && len(values) != 2 {
		return errors.New("must contain exactly two values")
	}
	for _, value := range values {
		if value <= 0 || value > 1_000_000 {
			return errors.New("contains an invalid value")
		}
	}
	return nil
}

func normalizePin(pin Pin) Pin {
	if pin.Projections != nil {
		projections := make([]PinnedProjection, len(pin.Projections))
		copy(projections, pin.Projections)
		pin.Projections = projections
	}
	for i := range pin.Projections {
		pin.Projections[i].TileSize = append([]float64(nil), pin.Projections[i].TileSize...)
		pin.Projections[i].WindowSize = append([]int(nil), pin.Projections[i].WindowSize...)
	}
	sort.SliceStable(pin.Projections, func(i, j int) bool { return pin.Projections[i].Order < pin.Projections[j].Order })
	return pin
}

type PinStore struct {
	root string
	dir  string
}

func OpenPinStore(stateDir string) (*PinStore, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("state directory is empty")
	}
	root := filepath.Join(stateDir, "mirror")
	return &PinStore{root: root, dir: filepath.Join(root, "pins")}, nil
}

func NewPinStore(stateDir string) (*PinStore, error) {
	store, err := OpenPinStore(stateDir)
	if err != nil {
		return nil, err
	}
	dir := store.dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create mirror pin directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure mirror pin directory: %w", err)
	}
	return store, nil
}

func pinName(host, profile string) string {
	identity, _ := json.Marshal([]string{host, profile})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *PinStore) Path(host, profile string) string {
	return filepath.Join(s.dir, pinName(host, profile))
}

func (s *PinStore) lockRoot(host, profile string) string {
	return filepath.Join(s.root, "pin-locks", strings.TrimSuffix(pinName(host, profile), ".json"))
}

func (s *PinStore) Read(host, profile string) (Pin, error) {
	path := s.Path(host, profile)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Pin{}, ErrPinNotFound
	}
	if err != nil {
		return Pin{}, err
	}
	defer func() { _ = file.Close() }()
	payload, err := io.ReadAll(io.LimitReader(file, maxPinBytes+1))
	if err != nil {
		return Pin{}, err
	}
	if len(payload) > maxPinBytes {
		return Pin{}, fmt.Errorf("%w: pin exceeds size limit", ErrPinInvalid)
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var pin Pin
	if err := decoder.Decode(&pin); err != nil {
		return Pin{}, fmt.Errorf("%w: decode %s: %v", ErrPinInvalid, filepath.Base(path), err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Pin{}, fmt.Errorf("%w: trailing JSON", ErrPinInvalid)
	}
	pin = normalizePin(pin)
	if err := pin.Validate(); err != nil {
		return Pin{}, fmt.Errorf("%w: validate %s: %v", ErrPinInvalid, filepath.Base(path), err)
	}
	if pin.SourceHost != host || pin.SourceProfile != profile {
		return Pin{}, fmt.Errorf("%w: identity does not match deterministic path", ErrPinInvalid)
	}
	return pin, nil
}

func (s *PinStore) Write(pin Pin) (path string, returnErr error) {
	pin = normalizePin(pin)
	if err := pin.Validate(); err != nil {
		return "", fmt.Errorf("validate mirror pin: %w", err)
	}
	lock, err := storelock.Acquire(s.lockRoot(pin.SourceHost, pin.SourceProfile))
	if err != nil {
		return "", fmt.Errorf("lock mirror pin: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()

	payload, err := json.Marshal(pin)
	if err != nil {
		return "", fmt.Errorf("encode mirror pin: %w", err)
	}
	payload = append(payload, '\n')
	path = s.Path(pin.SourceHost, pin.SourceProfile)
	tmp, err := os.CreateTemp(s.dir, ".pin-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create mirror pin temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if returnErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure mirror pin temp file: %w", err)
	}
	written, err := tmp.Write(payload)
	if err != nil {
		return "", fmt.Errorf("write mirror pin temp file: %w", err)
	}
	if written != len(payload) {
		return "", io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync mirror pin temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close mirror pin temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("replace mirror pin: %w", err)
	}
	if err := syncMirrorDirectory(s.dir); err != nil {
		return "", fmt.Errorf("sync mirror pin directory: %w", err)
	}
	return path, nil
}

func syncMirrorDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
