package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/niri"
)

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

type CheckpointsIntegrityCheck struct {
	StateDir string
}

func (c CheckpointsIntegrityCheck) Name() string { return "checkpoints_integrity" }

func (c CheckpointsIntegrityCheck) Run(_ context.Context) Result {
	valid, issues, err := checkpoints.List(c.StateDir)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: err.Error()}
	}
	if len(issues) > 0 {
		issue := issues[0]
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("invalid %s: %v", filepath.Base(issue.Path), issue.Err)}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("readable and valid (%d rolling checkpoints)", len(valid))}
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
	return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("%s exists and may shadow the Nix-managed version; run `devenv shell uninstall-local` to remove it", path)}
}

// StatePathsCheck reports the configured checkpoint location without creating or
// modifying them. Integrity checks below inspect files that already exist.
type StatePathsCheck struct {
	StateDir string
	Stat     func(name string) (os.FileInfo, error)
}

func (c StatePathsCheck) Name() string { return "state_paths" }

func (c StatePathsCheck) Run(_ context.Context) Result {
	stat := c.Stat
	if stat == nil {
		stat = os.Stat
	}
	stateDir := strings.TrimSpace(c.StateDir)
	if stateDir == "" {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "stateDir is empty; configure a checkpoint directory"}
	}
	info, err := stat(stateDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("cannot inspect stateDir %s: %v", stateDir, err)}
		}
		parent := filepath.Dir(stateDir)
		if _, parentErr := stat(parent); parentErr != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("stateDir %s is absent and parent %s is unavailable: %v", stateDir, parent, parentErr)}
		}
		return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("state_dir=%s checkpoints=%s (no captures yet)", stateDir, filepath.Join(stateDir, "checkpoints"))}
	}
	if !info.IsDir() {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("stateDir %s is not a directory", stateDir)}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("state_dir=%s checkpoints=%s", stateDir, filepath.Join(stateDir, "checkpoints"))}
}

type BootIDCheck struct {
	Current func() (string, error)
}

func (c BootIDCheck) Name() string { return "boot_id" }

func (c BootIDCheck) Run(_ context.Context) Result {
	if c.Current == nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "boot ID source is not configured"}
	}
	id, err := c.Current()
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("Linux boot ID unavailable; resume cannot select a prior boot: %v", err)}
	}
	if strings.TrimSpace(id) == "" {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "Linux boot ID is empty; verify /proc/sys/kernel/random/boot_id"}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("available: %s", strings.TrimSpace(id))}
}

type NiriReadinessCheck struct {
	FixturePath string
	Command     string
	Socket      string
	ReadFile    func(name string) ([]byte, error)
	LookPath    func(file string) (string, error)
	Snapshot    func(context.Context) ([]byte, error)
	Parse       func(raw []byte) error
	Timeout     time.Duration
}

func (c NiriReadinessCheck) Name() string { return "niri_readiness" }

func (c NiriReadinessCheck) Run(ctx context.Context) Result {
	readFile := c.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	parse := c.Parse
	if parse == nil {
		parse = func(raw []byte) error {
			_, err := niri.ParseSnapshot(raw)
			return err
		}
	}
	if fixture := strings.TrimSpace(c.FixturePath); fixture != "" {
		raw, err := readFile(fixture)
		if err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("offline fixture unreadable: %v", err)}
		}
		if err := parse(raw); err != nil {
			return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("offline fixture invalid: %v", err)}
		}
		return Result{Name: c.Name(), Status: StatusPass, Detail: "offline fixture query is readable and valid (live Niri IPC bypassed)"}
	}

	if strings.TrimSpace(c.Socket) == "" {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "NIRI_SOCKET is unset; run doctor in the Niri graphical session and import NIRI_SOCKET into the systemd user manager"}
	}
	command := strings.TrimSpace(c.Command)
	if command == "" {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "Niri query command is empty"}
	}
	binary := "sh"
	if command == niri.DefaultSnapshotCommand {
		binary = "niri"
	}
	lookPath := c.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(binary); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("Niri query executable unavailable: %s", binary)}
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshot := c.Snapshot
	if snapshot == nil {
		snapshot = func(ctx context.Context) ([]byte, error) {
			return (niri.CommandSnapshotter{Command: c.Command}).Snapshot(ctx)
		}
	}
	raw, err := snapshot(queryCtx)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("Niri IPC query failed via %s: %v; verify NIRI_SOCKET and graphical-session readiness", c.Socket, err)}
	}
	if err := parse(raw); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("Niri IPC returned invalid windows/workspaces JSON: %v", err)}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("IPC query ready via %s", c.Socket)}
}

type ResumeLauncherCheck struct {
	Command  string
	LookPath func(file string) (string, error)
}

func (c ResumeLauncherCheck) Name() string { return "resume_launcher" }

func (c ResumeLauncherCheck) Run(_ context.Context) Result {
	command := strings.TrimSpace(c.Command)
	if command == "" {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "resume.terminalCommand is empty"}
	}
	lookPath := c.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	resolved, err := lookPath(command)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("Kitty launcher unavailable: %s; configure a direct executable, not a shell command", command)}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("available: %s; resume requires this direct launcher PID to appear as Niri client_pid (daemonizing wrappers are unsupported)", resolved)}
}

type ZellijListingCheck struct {
	LookPath   func(file string) (string, error)
	RunCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (c ZellijListingCheck) Name() string { return "zellij_listing" }

func (c ZellijListingCheck) Run(ctx context.Context) Result {
	lookPath := c.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("zellij"); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "zellij executable unavailable; install it in the graphical user service PATH"}
	}
	run := c.RunCommand
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, "zellij", "list-sessions", "--short")
	if err != nil {
		detail := strings.TrimSpace(string(out))
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("zellij list-sessions --short failed: %v output=%q", err, detail)}
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("listing succeeded; sessions=%d", count)}
}

type ResumePolicyCheck struct {
	MaxCheckpointAge    time.Duration
	UnresolvedWorkspace string
	OnStartup           bool
	CaptureInterval     time.Duration
}

func (c ResumePolicyCheck) Name() string { return "resume_policy" }

func (c ResumePolicyCheck) Run(_ context.Context) Result {
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("max_checkpoint_age=%s unresolved_workspace=%s on_startup=%t capture_interval=%s; inspect capture with systemctl --user status terminal-redeemer-capture.timer", c.MaxCheckpointAge, c.UnresolvedWorkspace, c.OnStartup, c.CaptureInterval)}
}

type StartupServiceCheck struct {
	Enabled    bool
	RunCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (c StartupServiceCheck) Name() string { return "startup_service" }

func (c StartupServiceCheck) Run(ctx context.Context) Result {
	if !c.Enabled {
		return Result{Name: c.Name(), Status: StatusPass, Detail: "disabled (manual resume default); no startup service is required"}
	}
	run := c.RunCommand
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, "systemctl", "--user", "is-enabled", "terminal-redeemer-resume.service")
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("resume.onStartup is true but terminal-redeemer-resume.service is not enabled: %v output=%q; inspect with journalctl --user -u terminal-redeemer-resume.service", err, strings.TrimSpace(string(out)))}
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: "enabled; inspect the last bounded resume attempt with journalctl --user -u terminal-redeemer-resume.service"}
}

func firstCommandToken(command string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(command))
	if len(parts) == 0 {
		return "", fmt.Errorf("command is empty")
	}
	return parts[0], nil
}
