package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/events"
	"github.com/jmo/terminal-redeemer/internal/niri"
	"github.com/jmo/terminal-redeemer/internal/snapshots"
)

type StateDirWritableCheck struct {
	StateDir  string
	MkdirAll  func(path string, perm os.FileMode) error
	WriteFile func(name string, data []byte, perm os.FileMode) error
	Remove    func(name string) error
}

func (c StateDirWritableCheck) Name() string {
	return "state_dir_writable"
}

func (c StateDirWritableCheck) Run(_ context.Context) Result {
	mkdirAll := c.MkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	writeFile := c.WriteFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	remove := c.Remove
	if remove == nil {
		remove = os.Remove
	}

	if err := mkdirAll(c.StateDir, 0o755); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("mkdir failed: %v", err)}
	}

	probePath := filepath.Join(c.StateDir, ".doctor-write-check")
	if err := writeFile(probePath, []byte("ok"), 0o600); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("write failed: %v", err)}
	}
	_ = remove(probePath)

	return Result{Name: c.Name(), Status: StatusPass, Detail: "writable"}
}

type ConfigLoadCheck struct {
	Path     string
	Explicit bool
	Load     func(path string, explicitPath bool) (config.Config, error)
}

func (c ConfigLoadCheck) Name() string {
	return "config_load"
}

func (c ConfigLoadCheck) Run(_ context.Context) Result {
	load := c.Load
	if load == nil {
		load = config.Load
	}
	_, err := load(c.Path, c.Explicit)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: err.Error()}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: "valid"}
}

type NiriSourceCheck struct {
	FixturePath string
	Command     string
	ReadFile    func(name string) ([]byte, error)
	LookPath    func(file string) (string, error)
	Parse       func(raw []byte) error
}

func (c NiriSourceCheck) Name() string {
	return "niri_source"
}

func (c NiriSourceCheck) Run(_ context.Context) Result {
	readFile := c.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	lookPath := c.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	parse := c.Parse
	if parse == nil {
		parse = func(raw []byte) error {
			_, err := niri.ParseSnapshot(raw)
			return err
		}
	}

	fixture := strings.TrimSpace(c.FixturePath)
	if fixture != "" {
		payload, err := readFile(fixture)
		if err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("fixture unreadable: %v", err)}
		}
		if err := parse(payload); err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("fixture invalid: %v", err)}
		}
		return Result{Name: c.Name(), Status: StatusPass, Detail: "fixture readable and valid"}
	}

	binary, err := firstCommandToken(c.Command)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: err.Error()}
	}
	if _, err := lookPath(binary); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("command unavailable: %s", binary)}
	}

	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("command available: %s", binary)}
}

type CommandAvailableCheck struct {
	CheckName string
	Command   string
	LookPath  func(file string) (string, error)
}

func (c CommandAvailableCheck) Name() string {
	return c.CheckName
}

func (c CommandAvailableCheck) Run(_ context.Context) Result {
	lookPath := c.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	binary, err := firstCommandToken(c.Command)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: err.Error()}
	}
	if _, err := lookPath(binary); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("missing: %s", binary)}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("available: %s", binary)}
}

type EventsIntegrityCheck struct {
	StateDir string
	OpenFile func(name string) (*os.File, error)
}

func (c EventsIntegrityCheck) Name() string {
	return "events_integrity"
}

func (c EventsIntegrityCheck) Run(_ context.Context) Result {
	openFile := c.OpenFile
	if openFile == nil {
		openFile = os.Open
	}

	path := filepath.Join(c.StateDir, "events.jsonl")
	f, err := openFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: c.Name(), Status: StatusPass, Detail: "events file missing (no captures yet)"}
		}
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("open failed: %v", err)}
	}
	defer func() {
		_ = f.Close()
	}()

	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("line %d decode failed: %v", line, err)}
		}
		if err := event.Validate(); err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("line %d invalid: %v", line, err)}
		}
	}
	if err := scanner.Err(); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("scan failed: %v", err)}
	}

	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("readable and valid (%d events)", line)}
}

type SnapshotsIntegrityCheck struct {
	StateDir string
	ReadDir  func(name string) ([]os.DirEntry, error)
	ReadFile func(name string) ([]byte, error)
}

func (c SnapshotsIntegrityCheck) Name() string {
	return "snapshots_integrity"
}

func (c SnapshotsIntegrityCheck) Run(_ context.Context) Result {
	readDir := c.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	readFile := c.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}

	dir := filepath.Join(c.StateDir, "snapshots")
	entries, err := readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: c.Name(), Status: StatusPass, Detail: "snapshots dir missing (no snapshots yet)"}
		}
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("read dir failed: %v", err)}
	}

	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		checked++
		path := filepath.Join(dir, entry.Name())
		payload, err := readFile(path)
		if err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("read %s failed: %v", entry.Name(), err)}
		}
		var snapshot snapshots.Snapshot
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("decode %s failed: %v", entry.Name(), err)}
		}
		if err := snapshot.Validate(); err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("invalid %s: %v", entry.Name(), err)}
		}
	}

	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("readable and valid (%d snapshots)", checked)}
}

type LocalInstallCheck struct {
	Path string
	Stat func(name string) (os.FileInfo, error)
}

func (c LocalInstallCheck) Name() string {
	return "local_install"
}

func (c LocalInstallCheck) Run(_ context.Context) Result {
	stat := c.Stat
	if stat == nil {
		stat = os.Stat
	}

	path := c.Path
	if path == "" {
		return Result{Name: c.Name(), Status: StatusPass, Detail: "no local install path resolved"}
	}
	if _, err := stat(path); err != nil {
		return Result{Name: c.Name(), Status: StatusPass, Detail: "no local install found"}
	}
	return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("%s exists and may shadow the Nix-managed version; run `devbox run uninstall-local` to remove it", path)}
}

func firstCommandToken(command string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(command))
	if len(parts) == 0 {
		return "", fmt.Errorf("command is empty")
	}
	return parts[0], nil
}
