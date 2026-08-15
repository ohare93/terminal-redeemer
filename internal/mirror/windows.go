package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

var zellijEnvironment = []string{
	"ZELLIJ",
	"ZELLIJ_SESSION_NAME",
	"ZELLIJ_PANE_ID",
	"ZELLIJ_TAB_INDEX",
	"ZELLIJ_TAB_NAME",
}

type LaunchConfig struct {
	SourceHost      string
	SSHCommand      string
	SSHOptions      []string
	LauncherCommand string
	SelfCommand     string
	AppID           string
	Mode            string
	Socket          string
	Clipboard       bool
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
	if cfg.Mode != "attach" && cfg.Mode != "watch" {
		return LaunchPlan{}, fmt.Errorf("invalid mirror mode %q (expected attach or watch)", cfg.Mode)
	}
	if cfg.Mode == "watch" {
		return LaunchPlan{}, fmt.Errorf("mirror watch is unsupported by pinned Zellij 0.44.3")
	}
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
	remoteArgv = append(remoteArgv, "zellij", "attach")
	if create {
		remoteArgv = append(remoteArgv, "--create")
	}
	remoteArgv = append(remoteArgv, session, "options", "--on-force-close", "detach")
	remoteCommand := "exec " + QuoteCommand(remoteArgv)
	if cwd != "" {
		remoteCommand = "cd -- " + ShellQuote(cwd) + " 2>/dev/null || true; " + remoteCommand
	}

	titlePart := strings.TrimSpace(window.Title)
	if titlePart == "" {
		titlePart = session
	}
	titlePart = strings.NewReplacer("\n", " ", "\r", " ").Replace(titlePart)
	title := fmt.Sprintf("%s[%d]: %s", cfg.SourceHost, window.Order, titlePart)

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
	ID          int    `json:"id"`
	Title       string `json:"title"`
	WorkspaceID any    `json:"workspace_id,omitempty"`
	AppID       string `json:"app_id"`
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
