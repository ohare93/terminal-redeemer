package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const projectionTokenEnvironment = "REDEEM_PROJECTION_TOKEN"

var zellijEnvironment = []string{
	"ZELLIJ",
	"ZELLIJ_SESSION_NAME",
	"ZELLIJ_PANE_ID",
	"ZELLIJ_TAB_INDEX",
	"ZELLIJ_TAB_NAME",
}

type LaunchConfig struct {
	SourceHost       string
	CorrelationToken string
	SSHCommand       string
	SSHOptions       []string
	LauncherCommand  string
	SelfCommand      string
	AppID            string
	Socket           string
	Clipboard        bool
}

type LaunchPlan struct {
	SourceHost string
	Session    string
	Title      string
	Order      int
	RemoteCWD  string
	Command    Command
}

func PlanLaunch(window Window, cfg LaunchConfig) (LaunchPlan, error) {
	session := SessionName(window)
	if session == "" {
		return LaunchPlan{}, fmt.Errorf("source window %d has no zellij session", window.SourceWindowID)
	}
	return planZellijLaunch(window, cfg, session, false)
}

// PlanNew launches a deliberately new, generated Zellij session. Existing
// session launches use PlanLaunch and never receive --create.
func PlanNew(session string, cfg LaunchConfig) (LaunchPlan, error) {
	if !generatedSessionPattern.MatchString(session) {
		return LaunchPlan{}, fmt.Errorf("invalid generated mirror session name %q", session)
	}
	window := Window{Title: session, ZellijSession: session}
	return planZellijLaunch(window, cfg, session, true)
}

func planZellijLaunch(window Window, cfg LaunchConfig, session string, create bool) (LaunchPlan, error) {
	if err := ValidateDestination(cfg.SourceHost); err != nil {
		return LaunchPlan{}, err
	}
	if err := ValidateSession(session); err != nil {
		return LaunchPlan{}, err
	}
	if strings.TrimSpace(cfg.LauncherCommand) == "" || strings.TrimSpace(cfg.SSHCommand) == "" || strings.TrimSpace(cfg.AppID) == "" {
		return LaunchPlan{}, fmt.Errorf("launcher, SSH command, and app ID must not be empty")
	}

	cwd := ""
	if window.Terminal != nil {
		cwd = strings.TrimSpace(window.Terminal.CWD)
	}
	remoteArgv := []string{"env"}
	for _, name := range zellijEnvironment {
		remoteArgv = append(remoteArgv, "-u", name)
	}
	if cfg.CorrelationToken != "" {
		if !correlationTokenPattern.MatchString(cfg.CorrelationToken) {
			return LaunchPlan{}, fmt.Errorf("invalid projection correlation token")
		}
		remoteArgv = append(remoteArgv, projectionTokenEnvironment+"="+cfg.CorrelationToken)
	}
	remoteArgv = append(remoteArgv, "zellij", "attach")
	switch {
	case create:
		remoteArgv = append(remoteArgv, "--create", session, "options", "--on-force-close", "detach")
	case strings.HasPrefix(session, "-"):
		// Zellij requires the option boundary for leading-dash session names,
		// and its trailing options subcommand cannot be combined after it.
		remoteArgv = append(remoteArgv, "--", session)
	default:
		remoteArgv = append(remoteArgv, session, "options", "--on-force-close", "detach")
	}
	remoteCommand := "exec " + QuoteCommand(remoteArgv)
	if cwd != "" {
		remoteCommand = "cd -- " + ShellQuote(cwd) + " 2>/dev/null || true; " + remoteCommand
	}

	titlePart := strings.TrimSpace(window.Title)
	if titlePart == "" {
		titlePart = session
	}
	titlePart = strings.NewReplacer("\n", " ", "\r", " ").Replace(titlePart)
	// The exact session is immutable launch-time presentation metadata. Live
	// process evidence, never this title, remains projection authority.
	title := fmt.Sprintf("%s[%d|%s]: %s", cfg.SourceHost, window.Order, session, titlePart)

	args := []string{"--detach", "--class", cfg.AppID, "--override", "confirm_os_window_close=0", "--title", title}
	if cfg.Clipboard {
		if strings.TrimSpace(cfg.Socket) == "" || strings.TrimSpace(cfg.SelfCommand) == "" {
			return LaunchPlan{}, fmt.Errorf("clipboard bridge requires a socket and self command")
		}
		mapping := "map=ctrl+v launch --type=background " + QuoteCommand([]string{cfg.SelfCommand, "mirror", "paste-image", "--host", cfg.SourceHost, "--kitty-to", cfg.Socket})
		args = append(args, "--listen-on", cfg.Socket, "--override", mapping)
	}
	sshArgs := append([]string(nil), cfg.SSHOptions...)
	sshArgs = append(sshArgs, "-tt", "--", cfg.SourceHost, remoteCommand)
	args = append(args, "-e", cfg.SSHCommand)
	args = append(args, sshArgs...)
	return LaunchPlan{
		SourceHost: cfg.SourceHost,
		Session:    session,
		Title:      title,
		Order:      window.Order,
		RemoteCWD:  cwd,
		Command:    Command{Name: cfg.LauncherCommand, Args: args},
	}, nil
}

func RenderCommand(command Command) string {
	return QuoteCommand(append([]string{command.Name}, command.Args...))
}

type OwnedWindow struct {
	ID          int         `json:"id"`
	PID         int         `json:"pid,omitempty"`
	Title       string      `json:"title"`
	WorkspaceID any         `json:"workspace_id,omitempty"`
	AppID       string      `json:"app_id"`
	IsFloating  bool        `json:"is_floating,omitempty"`
	Layout      OwnedLayout `json:"layout,omitempty"`
}

type OwnedLayout struct {
	TileSize   []float64 `json:"tile_size,omitempty"`
	WindowSize []int     `json:"window_size,omitempty"`
}

type OwnedWorkspace struct {
	ID    any `json:"id"`
	Index int `json:"idx"`
	Name  any `json:"name"`
}

type niriWindowsPayload struct {
	Windows []OwnedWindow `json:"windows"`
}

func DecodeOwnedWindows(raw []byte, appID string, sourceHost string) ([]OwnedWindow, error) {
	var windows []OwnedWindow
	if err := json.Unmarshal(raw, &windows); err != nil {
		var payload niriWindowsPayload
		if objectErr := json.Unmarshal(raw, &payload); objectErr != nil {
			return nil, fmt.Errorf("decode Niri windows JSON: %w", err)
		}
		windows = payload.Windows
	}
	prefix := ""
	if sourceHost != "" {
		prefix = sourceHost + "["
	}
	owned := make([]OwnedWindow, 0)
	for _, window := range windows {
		if window.ID <= 0 || window.AppID != appID {
			continue
		}
		if prefix != "" && !strings.HasPrefix(window.Title, prefix) {
			continue
		}
		owned = append(owned, window)
	}
	return owned, nil
}

type WindowManager struct {
	Runner      Runner
	NiriCommand string
}

func (manager WindowManager) List(ctx context.Context, appID string, sourceHost string) ([]OwnedWindow, error) {
	runner := manager.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if strings.TrimSpace(manager.NiriCommand) == "" {
		return nil, fmt.Errorf("niri command is empty")
	}
	raw, err := runner.Output(ctx, Command{Name: manager.NiriCommand, Args: []string{"msg", "-j", "windows"}})
	if err != nil {
		return nil, fmt.Errorf("list Niri windows (Niri/Wayland session required): %w", err)
	}
	return DecodeOwnedWindows(raw, appID, sourceHost)
}

func (manager WindowManager) Workspaces(ctx context.Context) ([]OwnedWorkspace, error) {
	runner := manager.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if strings.TrimSpace(manager.NiriCommand) == "" {
		return nil, fmt.Errorf("niri command is empty")
	}
	raw, err := runner.Output(ctx, Command{Name: manager.NiriCommand, Args: []string{"msg", "-j", "workspaces"}})
	if err != nil {
		return nil, fmt.Errorf("list Niri workspaces: %w", err)
	}
	var workspaces []OwnedWorkspace
	if err := json.Unmarshal(raw, &workspaces); err != nil {
		return nil, fmt.Errorf("decode Niri workspaces JSON: %w", err)
	}
	return workspaces, nil
}

func (manager WindowManager) Close(ctx context.Context, windows []OwnedWindow, dryRun bool) error {
	if dryRun {
		return nil
	}
	runner := manager.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	for _, window := range windows {
		request := Command{Name: manager.NiriCommand, Args: []string{"msg", "action", "close-window", "--id", strconv.Itoa(window.ID)}}
		if err := runner.Run(ctx, request); err != nil {
			return fmt.Errorf("close owned Niri window %d: %w", window.ID, err)
		}
	}
	return nil
}
