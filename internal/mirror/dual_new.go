package mirror

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSourceReadyTimeout = 5 * time.Second
	DefaultSourcePollInterval = 250 * time.Millisecond
)

// ValidateWorkspaceReference accepts a bounded Niri workspace name or positive
// numeric index. It is passed as argv locally and shell-quoted across SSH.
func ValidateWorkspaceReference(reference string) error {
	if reference == "" {
		return nil
	}
	if strings.TrimSpace(reference) != reference || len(reference) > 128 || strings.HasPrefix(reference, "-") {
		return fmt.Errorf("invalid workspace reference %q", reference)
	}
	for _, r := range reference {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid workspace reference %q", reference)
		}
	}
	if index, err := strconv.Atoi(reference); err == nil && index <= 0 {
		return fmt.Errorf("workspace index must be positive")
	}
	return nil
}

type SourceAttachConfig struct {
	SourceHost    string
	SSHCommand    string
	SSHOptions    []string
	RemoteCommand string
	Session       string
	Workspace     string
}

// PlanSourceAttach invokes Redeem on the source. OpenSSH accepts the remote
// command as one string, so every argv item is deterministically shell-quoted.
func PlanSourceAttach(cfg SourceAttachConfig) (Command, error) {
	if err := ValidateDestination(cfg.SourceHost); err != nil {
		return Command{}, err
	}
	if !generatedSessionPattern.MatchString(cfg.Session) {
		return Command{}, fmt.Errorf("invalid generated mirror session name %q", cfg.Session)
	}
	if err := ValidateWorkspaceReference(cfg.Workspace); err != nil {
		return Command{}, err
	}
	if strings.TrimSpace(cfg.SSHCommand) == "" || strings.TrimSpace(cfg.RemoteCommand) == "" {
		return Command{}, fmt.Errorf("SSH and remote Redeem commands must not be empty")
	}

	remoteArgv := []string{cfg.RemoteCommand, "mirror", "attach-local", "--session", cfg.Session}
	if cfg.Workspace != "" {
		remoteArgv = append(remoteArgv, "--workspace", cfg.Workspace)
	}
	args := append([]string(nil), cfg.SSHOptions...)
	args = append(args, "--", cfg.SourceHost, QuoteCommand(remoteArgv))
	return Command{Name: cfg.SSHCommand, Args: args}, nil
}

type SnapshotAcquirer func(context.Context) (Snapshot, error)
type WaitFunc func(context.Context, time.Duration) error

type DualNewCoordinator struct {
	Runner       Runner
	Acquire      SnapshotAcquirer
	Wait         WaitFunc
	Timeout      time.Duration
	PollInterval time.Duration
}

type DualNewResult struct {
	SourceError error
}

// Run starts the sole creator exactly once. Source readiness and the source
// helper are best effort: their failure is returned in SourceError, never as a
// creator failure.
func (coordinator DualNewCoordinator) Run(ctx context.Context, creator LaunchPlan, sourceHelper Command) (DualNewResult, error) {
	runner := coordinator.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if err := runner.Run(ctx, creator.Command); err != nil {
		return DualNewResult{}, err
	}
	if coordinator.Acquire == nil {
		return DualNewResult{SourceError: fmt.Errorf("source readiness observer is unavailable")}, nil
	}

	timeout := coordinator.Timeout
	if timeout <= 0 {
		timeout = DefaultSourceReadyTimeout
	}
	interval := coordinator.PollInterval
	if interval <= 0 {
		interval = DefaultSourcePollInterval
	}
	wait := coordinator.Wait
	if wait == nil {
		wait = waitContext
	}
	attempts := int(timeout/interval) + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		snapshot, err := coordinator.Acquire(ctx)
		if err == nil && snapshotHasExactSession(snapshot, creator.Session) {
			if err := runner.Run(ctx, sourceHelper); err != nil {
				return DualNewResult{SourceError: fmt.Errorf("launch source Kitty: %w", err)}, nil
			}
			return DualNewResult{}, nil
		}
		if err != nil {
			lastErr = err
		}
		if attempt+1 < attempts {
			if err := wait(ctx, interval); err != nil {
				return DualNewResult{SourceError: err}, nil
			}
		}
	}
	if lastErr != nil {
		return DualNewResult{SourceError: fmt.Errorf("source session %q did not become ready: %w", creator.Session, lastErr)}, nil
	}
	return DualNewResult{SourceError: fmt.Errorf("source session %q did not become ready within %s", creator.Session, timeout)}, nil
}

func snapshotHasExactSession(snapshot Snapshot, session string) bool {
	for _, window := range snapshot.Windows {
		if SessionName(window) == session {
			return true
		}
	}
	return false
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
