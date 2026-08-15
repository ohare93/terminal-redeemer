package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	StateDir        string                `yaml:"stateDir"`
	Host            string                `yaml:"host"`
	Profile         string                `yaml:"profile"`
	Capture         CaptureConfig         `yaml:"capture"`
	ProcessMetadata ProcessMetadataConfig `yaml:"processMetadata"`
	Retention       RetentionConfig       `yaml:"retention"`
	Restore         RestoreConfig         `yaml:"restore"`
	Mirror          MirrorConfig          `yaml:"mirror"`
}

type CaptureConfig struct {
	Interval      time.Duration `yaml:"interval"`
	SnapshotEvery int           `yaml:"snapshotEvery"`
	NiriCommand   string        `yaml:"niriCommand"`
}

type ProcessMetadataConfig struct {
	Whitelist         []string `yaml:"whitelist"`
	WhitelistExtra    []string `yaml:"whitelistExtra"`
	IncludeSessionTag bool     `yaml:"includeSessionTag"`
}

type RetentionConfig struct {
	Days int `yaml:"days"`
}

type RestoreConfig struct {
	OnStartup               bool              `yaml:"onStartup"`
	AppAllowlist            map[string]string `yaml:"appAllowlist"`
	AppMode                 map[string]string `yaml:"appMode"`
	ReconcileWorkspaceMoves bool              `yaml:"reconcileWorkspaceMoves"`
	WorkspaceReconcileDelay time.Duration     `yaml:"workspaceReconcileDelay"`
	MaxCheckpointAge        time.Duration     `yaml:"maxCheckpointAge"`
	UnresolvedWorkspace     string            `yaml:"unresolvedWorkspace"`
	ResumeTimeout           time.Duration     `yaml:"resumeTimeout"`
	ResumePollInterval      time.Duration     `yaml:"resumePollInterval"`
	Terminal                TerminalConfig    `yaml:"terminal"`
}

type TerminalConfig struct {
	Command              string `yaml:"command"`
	ZellijAttachOrCreate bool   `yaml:"zellijAttachOrCreate"`
}

type MirrorConfig struct {
	SourceHost      string                `yaml:"sourceHost"`
	SSHCommand      string                `yaml:"sshCommand"`
	SSHOptions      []string              `yaml:"sshOptions"`
	SnapshotCommand []string              `yaml:"snapshotCommand"`
	LauncherCommand string                `yaml:"launcherCommand"`
	SelfCommand     string                `yaml:"selfCommand"`
	AppID           string                `yaml:"appID"`
	DefaultMode     string                `yaml:"defaultMode"`
	OpenDelay       time.Duration         `yaml:"openDelay"`
	NiriCommand     string                `yaml:"niriCommand"`
	Clipboard       MirrorClipboardConfig `yaml:"clipboard"`
}

type MirrorClipboardConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Command      string   `yaml:"command"`
	SCPCommand   string   `yaml:"scpCommand"`
	SCPOptions   []string `yaml:"scpOptions"`
	KittyCommand string   `yaml:"kittyCommand"`
	TempDir      string   `yaml:"tempDir"`
	MIMETypes    []string `yaml:"mimeTypes"`
}

func DefaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".terminal-redeemer"
	}

	return filepath.Join(home, ".terminal-redeemer")
}

func DefaultConfigPath() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "terminal-redeemer", "config.yaml")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "terminal-redeemer", "config.yaml")
	}

	return filepath.Join(home, ".config", "terminal-redeemer", "config.yaml")
}

func Defaults() Config {
	return Config{
		StateDir: DefaultStateDir(),
		Host:     "local",
		Profile:  "default",
		Capture: CaptureConfig{
			Interval:      60 * time.Second,
			SnapshotEvery: 100,
			NiriCommand:   "niri msg -j windows",
		},
		ProcessMetadata: ProcessMetadataConfig{
			Whitelist:         []string{},
			WhitelistExtra:    []string{},
			IncludeSessionTag: true,
		},
		Retention: RetentionConfig{Days: 30},
		Restore: RestoreConfig{
			OnStartup:               false,
			AppAllowlist:            map[string]string{},
			AppMode:                 map[string]string{},
			ReconcileWorkspaceMoves: true,
			WorkspaceReconcileDelay: 1200 * time.Millisecond,
			MaxCheckpointAge:        24 * time.Hour,
			UnresolvedWorkspace:     "current",
			ResumeTimeout:           10 * time.Second,
			ResumePollInterval:      100 * time.Millisecond,
			Terminal: TerminalConfig{
				Command:              "kitty",
				ZellijAttachOrCreate: true,
			},
		},
		Mirror: MirrorConfig{
			SSHCommand:      "ssh",
			SSHOptions:      []string{},
			SnapshotCommand: []string{"redeem", "mirror", "snapshot"},
			LauncherCommand: "kitty",
			SelfCommand:     "redeem",
			AppID:           "terminal-redeemer-mirror",
			DefaultMode:     "attach",
			OpenDelay:       150 * time.Millisecond,
			NiriCommand:     "niri",
			Clipboard: MirrorClipboardConfig{
				Enabled:      true,
				Command:      "wl-paste",
				SCPCommand:   "scp",
				SCPOptions:   []string{},
				KittyCommand: "kitty",
				TempDir:      "/tmp",
				MIMETypes:    []string{"image/png", "image/jpeg", "image/webp", "image/gif"},
			},
		},
	}
}

func Load(path string, explicitPath bool) (Config, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigPath()
	}

	cfg := Defaults()
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if explicitPath {
			return Config{}, fmt.Errorf("config file not found: %s", path)
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file: %w", err)
	}

	if cfg.Restore.AppAllowlist == nil {
		cfg.Restore.AppAllowlist = map[string]string{}
	}
	if cfg.Restore.AppMode == nil {
		cfg.Restore.AppMode = map[string]string{}
	}
	if cfg.ProcessMetadata.Whitelist == nil {
		cfg.ProcessMetadata.Whitelist = []string{}
	}
	if cfg.ProcessMetadata.WhitelistExtra == nil {
		cfg.ProcessMetadata.WhitelistExtra = []string{}
	}
	if cfg.Mirror.SSHOptions == nil {
		cfg.Mirror.SSHOptions = []string{}
	}
	if cfg.Mirror.SnapshotCommand == nil {
		cfg.Mirror.SnapshotCommand = []string{}
	}
	if cfg.Mirror.Clipboard.SCPOptions == nil {
		cfg.Mirror.Clipboard.SCPOptions = []string{}
	}
	if cfg.Mirror.Clipboard.MIMETypes == nil {
		cfg.Mirror.Clipboard.MIMETypes = []string{}
	}
	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func Validate(cfg Config) error {
	if cfg.Restore.MaxCheckpointAge <= 0 {
		return fmt.Errorf("restore.maxCheckpointAge must be positive")
	}
	if cfg.Restore.ResumeTimeout <= 0 || cfg.Restore.ResumePollInterval <= 0 {
		return fmt.Errorf("restore.resumeTimeout and restore.resumePollInterval must be positive")
	}
	if cfg.Restore.ResumePollInterval > cfg.Restore.ResumeTimeout {
		return fmt.Errorf("restore.resumePollInterval must not exceed restore.resumeTimeout")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Restore.UnresolvedWorkspace)) {
	case "skip", "current", "fail":
	default:
		return fmt.Errorf("restore.unresolvedWorkspace must be current, skip, or fail")
	}
	if cfg.Mirror.DefaultMode != "attach" && cfg.Mirror.DefaultMode != "watch" {
		return fmt.Errorf("mirror.defaultMode must be attach or watch")
	}
	if strings.TrimSpace(cfg.Mirror.SSHCommand) == "" {
		return fmt.Errorf("mirror.sshCommand must not be empty")
	}
	if len(cfg.Mirror.SnapshotCommand) == 0 || strings.TrimSpace(cfg.Mirror.SnapshotCommand[0]) == "" {
		return fmt.Errorf("mirror.snapshotCommand must not be empty")
	}
	if strings.TrimSpace(cfg.Mirror.LauncherCommand) == "" || strings.TrimSpace(cfg.Mirror.AppID) == "" {
		return fmt.Errorf("mirror.launcherCommand and mirror.appID must not be empty")
	}
	if strings.TrimSpace(cfg.Mirror.NiriCommand) == "" {
		return fmt.Errorf("mirror.niriCommand must not be empty")
	}
	if cfg.Mirror.OpenDelay < 0 {
		return fmt.Errorf("mirror.openDelay must not be negative")
	}
	if cfg.Mirror.Clipboard.Enabled {
		if strings.TrimSpace(cfg.Mirror.Clipboard.Command) == "" || strings.TrimSpace(cfg.Mirror.Clipboard.SCPCommand) == "" || strings.TrimSpace(cfg.Mirror.Clipboard.KittyCommand) == "" {
			return fmt.Errorf("enabled mirror.clipboard commands must not be empty")
		}
		if !filepath.IsAbs(cfg.Mirror.Clipboard.TempDir) {
			return fmt.Errorf("mirror.clipboard.tempDir must be absolute")
		}
	}
	return nil
}
