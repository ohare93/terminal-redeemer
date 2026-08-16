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
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	return &PinStore{stateDir: filepath.Clean(absolute)}, nil
}

func pinName(host, profile string) string {
	identity, _ := json.Marshal([]string{host, profile})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *PinStore) Path(host, profile string) string {
	return filepath.Join(s.stateDir, pinsRelative, pinName(host, profile))
}

func (s *PinStore) acquire(host, profile string) (*pinLock, error) {
	if err := validatePinIdentity(host, profile); err != nil {
		return nil, err
	}
	lockDir, err := s.openSecureChild("pin-locks", true)
	if err != nil {
		return nil, err
	}
	defer unix.Close(lockDir)
	name := strings.TrimSuffix(pinName(host, profile), ".json") + ".lock"
	fd, err := unix.Openat(lockDir, name, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open mirror pin lock safely: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if err := requireRegularFile(fd, 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("unsafe mirror pin lock: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: mirror pin", storelock.ErrLocked)
		}
		return nil, fmt.Errorf("lock mirror pin: %w", err)
	}
	return &pinLock{file: file}, nil
}

type pinLock struct{ file *os.File }

func (lock *pinLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	return errors.Join(err, lock.file.Close())
}

func validatePinIdentity(host, profile string) error {
	if err := ValidateDestination(host); err != nil {
		return err
	}
	return validateText("source profile", profile, 128, false)
}

func (s *PinStore) Read(host, profile string) (Pin, error) {
	if err := validatePinIdentity(host, profile); err != nil {
		return Pin{}, err
	}
	pinsDir, err := s.openSecureChild("pins", false)
	if errors.Is(err, os.ErrNotExist) {
		return Pin{}, ErrPinNotFound
	}
	if err != nil {
		return Pin{}, err
	}
	defer unix.Close(pinsDir)
	fd, err := unix.Openat(pinsDir, pinName(host, profile), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Pin{}, ErrPinNotFound
	}
	if err != nil {
		return Pin{}, fmt.Errorf("%w: open mirror pin safely: %v", ErrPinInvalid, err)
	}
	file := os.NewFile(uintptr(fd), pinName(host, profile))
	defer file.Close()
	if err := requireRegularFile(fd, 0o600); err != nil {
		return Pin{}, fmt.Errorf("%w: unsafe pin: %v", ErrPinInvalid, err)
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
	// Validate before normalization so a missing/null projections member is
	// distinguishable from the explicit empty array used for a no-op pin.
	if err := pin.Validate(); err != nil || pin.SourceHost != host || pin.SourceProfile != profile {
		return Pin{}, fmt.Errorf("%w: validation or deterministic identity mismatch: %v", ErrPinInvalid, err)
	}
	return normalizePin(pin), nil
}

func (s *PinStore) Write(pin Pin) (path string, returnErr error) {
	if err := pin.Validate(); err != nil {
		return "", fmt.Errorf("validate mirror pin: %w", err)
	}
	pin = normalizePin(pin)
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
	pinsDir, err := s.openSecureChild("pins", true)
	if err != nil {
		return "", err
	}
	defer unix.Close(pinsDir)
	finalName := pinName(pin.SourceHost, pin.SourceProfile)
	if fd, openErr := unix.Openat(pinsDir, finalName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0); openErr == nil {
		regularErr := requireRegularFile(fd, 0o600)
		_ = unix.Close(fd)
		if regularErr != nil {
			return "", fmt.Errorf("%w: unsafe existing pin: %v", ErrPinInvalid, regularErr)
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return "", fmt.Errorf("%w: open existing mirror pin safely: %v", ErrPinInvalid, openErr)
	}
	tmpName, tmp, err := createPinTemp(pinsDir)
	if err != nil {
		return "", fmt.Errorf("create mirror pin temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		if returnErr != nil {
			_ = unix.Unlinkat(pinsDir, tmpName, 0)
		}
	}()
	if _, err := tmp.Write(payload); err != nil {
		return "", fmt.Errorf("write mirror pin temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync mirror pin temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := unix.Renameat(pinsDir, tmpName, pinsDir, finalName); err != nil {
		return "", fmt.Errorf("replace mirror pin: %w", err)
	}
	if err := unix.Fsync(pinsDir); err != nil {
		return "", fmt.Errorf("sync mirror pin directory: %w", err)
	}
	return s.Path(pin.SourceHost, pin.SourceProfile), nil
}

func createPinTemp(pinsDir int) (string, *os.File, error) {
	for range 32 {
		id, err := RandomID()
		if err != nil {
			return "", nil, err
		}
		name := ".pin-" + id + ".tmp"
		fd, err := unix.Openat(pinsDir, name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if err := requireRegularFile(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(pinsDir, name, 0)
			return "", nil, err
		}
		return name, os.NewFile(uintptr(fd), name), nil
	}
	return "", nil, errors.New("cannot allocate unique pin temp file")
}

// openSecureChild anchors every lookup at a directory descriptor reached from
// `/` with O_NOFOLLOW. An attacker cannot swap any pathname component and
// redirect pin I/O after it has been opened.
func (s *PinStore) openSecureChild(name string, create bool) (int, error) {
	stateDir, err := openAbsoluteDirectory(s.stateDir, create)
	if err != nil {
		return -1, err
	}
	defer unix.Close(stateDir)
	mirrorDir, err := openChildDirectory(stateDir, "mirror", create, 0o700, false)
	if err != nil {
		return -1, err
	}
	defer unix.Close(mirrorDir)
	return openChildDirectory(mirrorDir, name, create, 0o700, true)
}

func openAbsoluteDirectory(path string, create bool) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("state directory anchor is not absolute")
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	if len(components) == 1 && components[0] == "" {
		return current, nil
	}
	for _, component := range components {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, os.ErrNotExist) && create {
			if err := unix.Mkdirat(current, component, 0o700); err != nil {
				_ = unix.Close(current)
				return -1, fmt.Errorf("create state directory component %q: %w", component, err)
			}
			if err := unix.Fsync(current); err != nil {
				_ = unix.Close(current)
				return -1, fmt.Errorf("sync state directory parent: %w", err)
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			return -1, fmt.Errorf("open state directory component %q safely: %w", component, openErr)
		}
		_ = unix.Close(current)
		current = next
	}
	return current, nil
}

func openChildDirectory(parent int, name string, create bool, mode uint32, requireSecure bool) (int, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := unix.Mkdirat(parent, name, mode); err != nil {
			return -1, err
		}
		if err := unix.Fsync(parent); err != nil {
			return -1, err
		}
		fd, err = unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open mirror %s directory safely: %w", name, err)
	}
	if requireSecure {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != mode || stat.Uid != uint32(os.Geteuid()) {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("mirror %s directory must be an owned %04o directory", name, mode)
		}
	}
	return fd, nil
}

func requireRegularFile(fd int, mode uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != mode || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("must be an owned %04o regular file", mode)
	}
	return nil
}
