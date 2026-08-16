package mirror

import (
	"crypto/rand"
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

	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/storelock"
	"golang.org/x/sys/unix"
)

const (
	PinSchemaVersion = 1
	maxPinBytes      = 2 << 20
	pinsRelative     = "mirror/pins"
)

var (
	ErrPinNotFound = errors.New("mirror pin not found")
	ErrPinInvalid  = errors.New("invalid mirror pin")
)

type PinnedProjection struct {
	Session   string             `json:"session"`
	RemoteCWD string             `json:"remote_cwd,omitempty"`
	Workspace model.WorkspaceRef `json:"workspace"`
	Order     int                `json:"order"`
	Placement model.Placement    `json:"placement"`
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
		if err := validateText("workspace name", item.Workspace.Name, 256, true); err != nil || item.Workspace.Output != "" || item.Workspace.Index < 0 || item.Workspace.Index > 1_000_000 || (item.Workspace.Name == "" && item.Workspace.Index == 0) {
			return fmt.Errorf("projection %d has invalid workspace selector", i)
		}
		if item.Placement.Column != nil || item.Placement.IsFloating == nil {
			return fmt.Errorf("projection %d has invalid bounded placement", i)
		}
		if err := validateFloatSize(item.Placement.TileSize); err != nil {
			return fmt.Errorf("projection %d tile size: %w", i, err)
		}
		if err := validateIntSize(item.Placement.WindowSize); err != nil {
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
	pin.Projections = append([]PinnedProjection(nil), pin.Projections...)
	if pin.Projections == nil {
		pin.Projections = []PinnedProjection{}
	}
	for i := range pin.Projections {
		pin.Projections[i].Placement.TileSize = append([]float64(nil), pin.Projections[i].Placement.TileSize...)
		pin.Projections[i].Placement.WindowSize = append([]int(nil), pin.Projections[i].Placement.WindowSize...)
	}
	sort.SliceStable(pin.Projections, func(i, j int) bool { return pin.Projections[i].Order < pin.Projections[j].Order })
	return pin
}

type PinStore struct{ stateDir string }

func OpenPinStore(stateDir string) (*PinStore, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("state directory is empty")
	}
	return &PinStore{stateDir: stateDir}, nil
}

func pinName(host, profile string) string {
	identity, _ := json.Marshal([]string{host, profile})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *PinStore) Path(host, profile string) string {
	return filepath.Join(s.stateDir, pinsRelative, pinName(host, profile))
}
func (s *PinStore) lockRoot(host, profile string) string {
	return filepath.Join(s.stateDir, "mirror", "pin-locks", strings.TrimSuffix(pinName(host, profile), ".json"))
}

func (s *PinStore) acquire(host, profile string) (*storelock.Lock, error) {
	if err := ValidateDestination(host); err != nil {
		return nil, err
	}
	if err := validateText("source profile", profile, 128, false); err != nil {
		return nil, err
	}
	root, err := s.openRoot(true)
	if err != nil {
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	return storelock.Acquire(s.lockRoot(host, profile))
}

func (s *PinStore) Read(host, profile string) (Pin, error) {
	if err := ValidateDestination(host); err != nil {
		return Pin{}, err
	}
	if err := validateText("source profile", profile, 128, false); err != nil {
		return Pin{}, err
	}
	root, err := s.openRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return Pin{}, ErrPinNotFound
	}
	if err != nil {
		return Pin{}, err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Join(pinsRelative, pinName(host, profile))
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Pin{}, ErrPinNotFound
	}
	if err != nil {
		return Pin{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Pin{}, fmt.Errorf("%w: pin must be a non-symlink 0600 regular file", ErrPinInvalid)
	}
	file, err := root.Open(name)
	if err != nil {
		return Pin{}, err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Pin{}, fmt.Errorf("%w: pin changed during safe open", ErrPinInvalid)
	}
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
		return Pin{}, fmt.Errorf("%w: decode pin: %v", ErrPinInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Pin{}, fmt.Errorf("%w: trailing JSON", ErrPinInvalid)
	}
	pin = normalizePin(pin)
	if err := pin.Validate(); err != nil || pin.SourceHost != host || pin.SourceProfile != profile {
		return Pin{}, fmt.Errorf("%w: validation or deterministic identity mismatch: %v", ErrPinInvalid, err)
	}
	return pin, nil
}

func (s *PinStore) Write(pin Pin) (path string, returnErr error) {
	pin = normalizePin(pin)
	if err := pin.Validate(); err != nil {
		return "", fmt.Errorf("validate mirror pin: %w", err)
	}
	lock, err := s.acquire(pin.SourceHost, pin.SourceProfile)
	if err != nil {
		return "", fmt.Errorf("lock mirror pin: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	payload, err := json.Marshal(pin)
	if err != nil {
		return "", fmt.Errorf("encode mirror pin: %w", err)
	}
	payload = append(payload, '\n')
	root, err := s.openRoot(true)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	name := filepath.Join(pinsRelative, pinName(pin.SourceHost, pin.SourceProfile))
	if info, statErr := root.Lstat(name); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
		return "", fmt.Errorf("%w: existing pin must be a non-symlink 0600 regular file", ErrPinInvalid)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	tmpName, tmp, err := createRootTemp(root)
	if err != nil {
		return "", fmt.Errorf("create mirror pin temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		if returnErr != nil {
			_ = root.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", err
	}
	if err := writeFull(tmp, payload); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync mirror pin temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	pinsDir, err := root.Open(pinsRelative)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, pinsDir.Close()) }()
	if err := unix.Renameat(int(pinsDir.Fd()), filepath.Base(tmpName), int(pinsDir.Fd()), filepath.Base(name)); err != nil {
		return "", fmt.Errorf("replace mirror pin: %w", err)
	}
	if err := pinsDir.Sync(); err != nil {
		return "", fmt.Errorf("sync mirror pin directory: %w", err)
	}
	return s.Path(pin.SourceHost, pin.SourceProfile), nil
}

func writeFull(file *os.File, payload []byte) error {
	for len(payload) > 0 {
		n, err := file.Write(payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}

func createRootTemp(root *os.Root) (string, *os.File, error) {
	for range 32 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := filepath.Join(pinsRelative, ".pin-"+hex.EncodeToString(random[:])+".tmp")
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return name, file, err
	}
	return "", nil, errors.New("cannot allocate unique pin temp file")
}

func (s *PinStore) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := mkdirAllDurable(s.stateDir, 0o700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(s.stateDir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("state directory is not a safe directory")
	}
	root, err := os.OpenRoot(s.stateDir)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"mirror", pinsRelative} {
		info, err := root.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := root.Mkdir(dir, 0o700); err != nil {
				_ = root.Close()
				return nil, err
			}
			if err := syncRootDirectory(root, filepath.Dir(dir)); err != nil {
				_ = root.Close()
				return nil, err
			}
			info, err = root.Lstat(dir)
		}
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
			_ = root.Close()
			return nil, fmt.Errorf("mirror pin directory %q is not a 0700 directory", dir)
		}
	}
	return root, nil
}

func syncRootDirectory(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// mkdirAllDurable fsyncs the parent entry for every state-directory component
// it creates. Directories below the state root are synced by openRoot.
func mkdirAllDurable(path string, mode os.FileMode) error {
	clean := filepath.Clean(path)
	missing := make([]string, 0, 4)
	for cursor := clean; ; cursor = filepath.Dir(cursor) {
		info, err := os.Lstat(cursor)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%s is not a safe directory", cursor)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, cursor)
		if filepath.Dir(cursor) == cursor {
			return err
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], mode); err != nil {
			return err
		}
		parent, err := os.Open(filepath.Dir(missing[i]))
		if err != nil {
			return err
		}
		if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
			return err
		}
	}
	return nil
}
