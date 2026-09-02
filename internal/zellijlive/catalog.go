package zellijlive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/jmo/terminal-redeemer/internal/procrun"
)

const PinnedVersion = "0.44.3"
const SocketContractDir = "contract_version_1"
const MaxSocketPathBytes = 107
const MaxCatalogBytes = 1 << 20
const MaxCatalogEntries = 4096

var safeSessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type CommandCataloger struct {
	Command    string
	SocketBase string
	CacheHome  string
	BootID     string
	UID        int
	readDir    func(string) ([]os.DirEntry, error)
}

func (cataloger CommandCataloger) Observe(ctx context.Context) (Catalog, error) {
	readDir := cataloger.readDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	command := cataloger.Command
	if command == "" {
		command = "zellij"
	}
	uid := cataloger.UID
	if uid == 0 {
		uid = os.Getuid()
	}
	base := cataloger.SocketBase
	if base == "" {
		base = os.Getenv("ZELLIJ_SOCKET_DIR")
	}
	if base == "" {
		runtime := os.Getenv("XDG_RUNTIME_DIR")
		if runtime == "" {
			runtime = filepath.Join("/run/user", strconv.Itoa(uid))
		}
		base = filepath.Join(runtime, "zellij")
	}
	contractDir := filepath.Join(base, SocketContractDir)
	if err := verifyOwnedDirectory(base, uid); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Catalog{}, fmt.Errorf("validate Zellij socket base: %w", err)
	}
	if err := verifyOwnedDirectory(contractDir, uid); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Catalog{}, fmt.Errorf("validate Zellij contract socket directory: %w", err)
	}
	versionOut, err := runBounded(ctx, command, []string{"--version"}, nil)
	if err != nil || strings.TrimSpace(string(versionOut)) != "zellij "+PinnedVersion {
		return Catalog{}, fmt.Errorf("pinned Zellij %s is unavailable", PinnedVersion)
	}
	emptyCache, err := os.MkdirTemp("", "redeem-zellij-catalog-cache-*")
	if err != nil {
		return Catalog{}, err
	}
	defer os.RemoveAll(emptyCache)
	environment := scrubEnv(os.Environ(), map[string]string{"ZELLIJ_SOCKET_DIR": base, "XDG_CACHE_HOME": emptyCache})
	out, commandErr := runBounded(ctx, command, []string{"list-sessions", "--short"}, environment)
	if commandErr != nil {
		return Catalog{}, fmt.Errorf("list live Zellij sessions: %w", commandErr)
	}
	listed, err := parseLines(out)
	if err != nil {
		return Catalog{}, fmt.Errorf("parse live Zellij sessions: %w", err)
	}

	if err := verifyOwnedDirectory(base, uid); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Catalog{}, fmt.Errorf("validate Zellij socket base: %w", err)
	}
	if err := verifyOwnedDirectory(contractDir, uid); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Catalog{}, fmt.Errorf("validate Zellij contract socket directory: %w", err)
	}
	entries, readErr := readDir(contractDir)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Catalog{}, fmt.Errorf("read Zellij sockets: %w", readErr)
	}
	if len(entries) > MaxCatalogEntries || len(listed) > MaxCatalogEntries {
		return Catalog{}, fmt.Errorf("Zellij catalog exceeds entry bound")
	}
	byName := make(map[string]Session)
	counts := map[string]int{}
	for _, name := range listed {
		counts[name]++
	}
	for _, entry := range entries {
		name := entry.Name()
		if !SafeSessionName(name) || len(filepath.Join(contractDir, name)) > MaxSocketPathBytes {
			byName[name] = Session{Name: name, Status: StatusSocketInvalid}
			continue
		}
		info, err := os.Lstat(filepath.Join(contractDir, name))
		if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
			byName[name] = Session{Name: name, Status: StatusSocketInvalid}
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != uid {
			byName[name] = Session{Name: name, Status: StatusSocketInvalid}
			continue
		}
		if counts[name] != 1 {
			byName[name] = Session{Name: name, Status: StatusDuplicate}
			continue
		}
		byName[name] = Session{Name: name, ID: SessionID(cataloger.BootID, name, uint64(stat.Dev), stat.Ino), Status: StatusActive}
	}
	for name, count := range counts {
		if count > 1 {
			byName[name] = Session{Name: name, Status: StatusDuplicate}
			continue
		}
		if _, found := byName[name]; !found {
			byName[name] = Session{Name: name, Status: StatusMissing}
		}
	}
	cacheHome := cataloger.CacheHome
	if cacheHome == "" {
		cacheHome = os.Getenv("XDG_CACHE_HOME")
	}
	if cacheHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cacheHome = filepath.Join(home, ".cache")
		}
	}
	deadDir := filepath.Join(cacheHome, "zellij", SocketContractDir, "session_info")
	deadEntries, deadReadErr := readDir(deadDir)
	resurrectionCacheAvailable := deadReadErr == nil
	if deadReadErr != nil && !errors.Is(deadReadErr, os.ErrNotExist) {
		return Catalog{}, fmt.Errorf("read Zellij resurrection catalog: %w", deadReadErr)
	}
	if len(deadEntries) > MaxCatalogEntries {
		return Catalog{}, fmt.Errorf("Zellij resurrection catalog exceeds entry bound")
	}
	for _, entry := range deadEntries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, found := byName[name]; !found {
			byName[name] = Session{Name: name, Status: StatusDeadResurrectable}
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return Catalog{Sessions: byName, Names: names, ResurrectionCacheAvailable: resurrectionCacheAvailable}, nil
}

func verifyOwnedDirectory(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("directory is not a private direct directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return fmt.Errorf("directory is not owned by current user")
	}
	return nil
}

func SafeSessionName(name string) bool { return safeSessionName.MatchString(name) && len(name) <= 255 }

func SessionID(bootID, name string, device, inode uint64) string {
	payload := fmt.Sprintf("terminal-redeemer/session/v1\x00%d:%s\x00%d:%d", len(bootID), bootID, device, inode)
	sum := sha256.Sum256([]byte(payload + "\x00" + name))
	return "ses_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

func parseLines(payload []byte) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	out := make([]string, 0)
	for scanner.Scan() {
		value := strings.TrimSpace(scanner.Text())
		if value == "" || strings.HasPrefix(value, "No active zellij sessions") {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type boundedOutput struct {
	bytes.Buffer
	max int
}

func (output *boundedOutput) Write(payload []byte) (int, error) {
	if output.Len()+len(payload) > output.max {
		return 0, fmt.Errorf("command output exceeds bound")
	}
	return output.Buffer.Write(payload)
}

func runBounded(ctx context.Context, command string, args []string, environment []string) ([]byte, error) {
	output := &boundedOutput{max: MaxCatalogBytes}
	cmd := procrun.CommandContext(ctx, command, args...)
	if environment != nil {
		cmd.Env = environment
	}
	cmd.Stdout = output
	cmd.Stderr = output
	err := procrun.ContextError(ctx, cmd.Run())
	return append([]byte(nil), output.Bytes()...), err
}

func scrubEnv(values []string, set map[string]string) []string {
	out := make([]string, 0, len(values)+len(set))
	for _, value := range values {
		key := strings.SplitN(value, "=", 2)[0]
		if key == "ZELLIJ" || key == "ZELLIJ_SESSION_NAME" {
			continue
		}
		if _, replace := set[key]; !replace {
			out = append(out, value)
		}
	}
	for key, value := range set {
		out = append(out, key+"="+value)
	}
	return out
}
