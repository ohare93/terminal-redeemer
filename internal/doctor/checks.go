package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/checkpoints"
	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/niri"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
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
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("schema=%d integrity=invalid checkpoint=%s error=%v", checkpoints.SchemaVersion, filepath.Base(issue.Path), issue.Err)}
	}
	schemaCounts := make(map[int]int)
	for _, checkpoint := range valid {
		schemaCounts[checkpoint.V]++
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: fmt.Sprintf("schema=%d integrity=valid checkpoints=%d schema_1=%d schema_2=%d schema_3=%d", checkpoints.SchemaVersion, len(valid), schemaCounts[1], schemaCounts[2], schemaCounts[checkpoints.SchemaVersion])}
}

type RecoveryInventoryCheck struct {
	StateDir       string
	Host           string
	Profile        string
	CurrentBootID  func() (string, error)
	ObserveCatalog func(context.Context, string) (zellijlive.Catalog, error)
}

func (c RecoveryInventoryCheck) Name() string { return "recovery_inventory" }

func (c RecoveryInventoryCheck) Run(ctx context.Context) Result {
	if c.CurrentBootID == nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: "current boot ID source is unavailable"}
	}
	bootID, err := c.CurrentBootID()
	if err != nil || strings.TrimSpace(bootID) == "" {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("current boot ID unavailable: %v", err)}
	}
	valid, issues, err := checkpoints.List(c.StateDir)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: err.Error()}
	}
	if len(issues) > 0 {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("invalid checkpoint %s: %v", filepath.Base(issues[0].Path), issues[0].Err)}
	}

	var current, prior *checkpoints.Checkpoint
	for i := range valid {
		checkpoint := valid[i]
		if checkpoint.V != checkpoints.SchemaVersion || (c.Host != "" && checkpoint.Host != c.Host) || (c.Profile != "" && checkpoint.Profile != c.Profile) {
			continue
		}
		if checkpoint.BootID == strings.TrimSpace(bootID) {
			if current == nil || checkpoint.ObservedAt.After(current.ObservedAt) {
				copy := checkpoint
				current = &copy
			}
		} else if prior == nil || checkpoint.ObservedAt.After(prior.ObservedAt) {
			copy := checkpoint
			prior = &copy
		}
	}

	observe := c.ObserveCatalog
	if observe == nil {
		observe = func(ctx context.Context, bootID string) (zellijlive.Catalog, error) {
			return (zellijlive.CommandCataloger{BootID: bootID}).Observe(ctx)
		}
	}
	catalog, err := observe(ctx, strings.TrimSpace(bootID))
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Detail: fmt.Sprintf("exact Zellij recovery catalog unavailable: %v", err)}
	}
	catalogActiveTotal, catalogDeadTotal := 0, 0
	for _, session := range catalog.Sessions {
		switch session.Status {
		case zellijlive.StatusActive:
			catalogActiveTotal++
		case zellijlive.StatusDeadResurrectable:
			catalogDeadTotal++
		}
	}
	currentInventoryTotal, sameBootEligibleActive := 0, 0
	if current != nil {
		currentInventoryTotal = len(current.Recovery.ActiveSessions)
		// Same-boot --all deliberately uses every exact ACTIVE catalog entry;
		// the current checkpoint supplies sticky placement, not an allow-list.
		sameBootEligibleActive = catalogActiveTotal
	}
	priorInventoryTotal := 0
	priorEligibleActive, priorEligibleDead := 0, 0
	priorExcluded := make(map[string]int)
	if prior != nil {
		priorInventoryTotal = len(prior.Recovery.ActiveSessions)
		seen := make(map[string]struct{}, priorInventoryTotal)
		for _, rawName := range prior.Recovery.ActiveSessions {
			name := strings.TrimSpace(rawName)
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			switch status := catalog.Exact(name).Status; status {
			case zellijlive.StatusActive:
				priorEligibleActive++
			case zellijlive.StatusDeadResurrectable:
				priorEligibleDead++
			default:
				label := string(status)
				if label == "" {
					label = "unknown"
				}
				priorExcluded[label]++
			}
		}
	}
	selected := prior
	candidateSource := "prior_active"
	if current != nil {
		selected = current
		candidateSource = "current_active"
	}
	incompleteIdentity, unnamedIndexPlacement := recoveryWarnings(selected)
	detail := fmt.Sprintf("catalog_total=%d catalog_active_total=%d catalog_dead_resurrectable_total=%d current_inventory_active_total=%d prior_inventory_active_total=%d same_boot_eligible_active=%d prior_active_eligible_active=%d prior_active_eligible_dead_resurrectable=%d prior_active_excluded_unsafe=%d prior_active_excluded_statuses=%s candidate_source=%s resurrection_cache_available=%t incomplete_identity_evidence=%d unnamed_index_dependent_placements=%d", len(catalog.Sessions), catalogActiveTotal, catalogDeadTotal, currentInventoryTotal, priorInventoryTotal, sameBootEligibleActive, priorEligibleActive, priorEligibleDead, statusCountTotal(priorExcluded), formatStatusCounts(priorExcluded), candidateSource, catalog.ResurrectionCacheAvailable, incompleteIdentity, unnamedIndexPlacement)
	if incompleteIdentity > 0 || unnamedIndexPlacement > 0 {
		detail += "; warning=prefer exact session identity and named Niri workspaces before automatic recovery"
	}
	return Result{Name: c.Name(), Status: StatusPass, Detail: detail}
}

func statusCountTotal(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func formatStatusCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	labels := make([]string, 0, len(counts))
	for label := range counts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf("%s:%d", label, counts[label]))
	}
	return strings.Join(parts, ",")
}

func recoveryWarnings(checkpoint *checkpoints.Checkpoint) (incompleteIdentity, unnamedIndexPlacement int) {
	if checkpoint == nil {
		return 0, 0
	}
	visible := make(map[string]int, len(checkpoint.Recovery.Sessions))
	for _, session := range checkpoint.Recovery.Sessions {
		if session.Visible {
			visible[strings.TrimSpace(session.Name)]++
		}
		if session.PlacementObservedAt != nil && session.WorkspaceRef != nil && strings.TrimSpace(session.WorkspaceRef.Name) == "" && session.WorkspaceRef.Index > 0 {
			unnamedIndexPlacement++
		}
	}
	tagCounts := make(map[string]int)
	for _, window := range checkpoint.State.Windows {
		if recoveryTerminal(window.AppID) && window.Terminal != nil {
			tag := strings.TrimSpace(window.Terminal.SessionTag)
			if tag != "" {
				tagCounts[tag]++
			}
		}
	}
	for _, window := range checkpoint.State.Windows {
		if !recoveryTerminal(window.AppID) {
			continue
		}
		tag := ""
		if window.Terminal != nil {
			tag = strings.TrimSpace(window.Terminal.SessionTag)
		}
		if tag == "" || tagCounts[tag] != 1 || visible[tag] != 1 {
			incompleteIdentity++
		}
	}
	return incompleteIdentity, unnamedIndexPlacement
}

func recoveryTerminal(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "kitty", "alacritty", "foot", "wezterm":
		return true
	default:
		return false
	}
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
