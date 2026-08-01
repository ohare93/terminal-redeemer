package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/slicecontroller"
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
	Slice           SliceConfig           `yaml:"slice"`
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

// These variables are replaced with store paths by the Nix package. Plain
// names keep source builds usable while execution still uses direct argv.
var (
	DefaultSelfExecutable      = "redeem"
	DefaultKittyExecutable     = "kitty"
	DefaultTransportExecutable = "ssh"
	DefaultZellijExecutable    = "zellij"
	DefaultNiriExecutable      = "niri"
	DefaultSystemctlExecutable = "systemctl"
)

type SliceConfig struct {
	LeechModeEnabled     bool          `yaml:"leechModeEnabled"`
	SourceHost           string        `yaml:"sourceHost"`
	SelfCommand          string        `yaml:"selfCommand"`
	KittyCommand         string        `yaml:"kittyCommand"`
	TransportCommand     string        `yaml:"transportCommand"`
	TransportOptions     []string      `yaml:"transportOptions"`
	RPCCommand           []string      `yaml:"rpcCommand"`
	ZellijCommand        string        `yaml:"zellijCommand"`
	NiriCommand          string        `yaml:"niriCommand"`
	SystemctlCommand     string        `yaml:"systemctlCommand"`
	ExpectedNiriVersion  string        `yaml:"expectedNiriVersion"`
	RequestTimeout       time.Duration `yaml:"requestTimeout"`
	KeepaliveInterval    time.Duration `yaml:"keepaliveInterval"`
	KeepaliveCount       int           `yaml:"keepaliveCount"`
	RetryMaxAttempts     int           `yaml:"retryMaxAttempts"`
	RetryInitialBackoff  time.Duration `yaml:"retryInitialBackoff"`
	RetryMaxBackoff      time.Duration `yaml:"retryMaxBackoff"`
	AttachPrivateRoot    string        `yaml:"attachPrivateRoot"`
	AttachShimCache      string        `yaml:"attachShimCache"`
	GraphicalContextKeys []string      `yaml:"graphicalContextKeys"`
	Clipboard            struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"clipboard"`
	Controller SliceControllerConfig `yaml:"controller"`
}

type SliceControllerConfig struct {
	Enabled                 bool          `yaml:"enabled"`
	HostID                  string        `yaml:"hostID"`
	LeechID                 string        `yaml:"leechID"`
	PollInterval            time.Duration `yaml:"pollInterval"`
	ControlTimeout          time.Duration `yaml:"controlTimeout"`
	RetryWindow             time.Duration `yaml:"retryWindow"`
	SourceGoneGrace         time.Duration `yaml:"sourceGoneGrace"`
	SourceGoneConfirmations int           `yaml:"sourceGoneConfirmations"`
	AuthorityMode           string        `yaml:"authorityMode"`
	LeechWriteAuthorized    bool          `yaml:"leechWriteAuthorized"`
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
		Slice: SliceConfig{
			SelfCommand:          DefaultSelfExecutable,
			KittyCommand:         DefaultKittyExecutable,
			TransportCommand:     DefaultTransportExecutable,
			TransportOptions:     []string{},
			RPCCommand:           []string{DefaultSelfExecutable, "slice", "rpc"},
			ZellijCommand:        DefaultZellijExecutable,
			NiriCommand:          DefaultNiriExecutable,
			SystemctlCommand:     DefaultSystemctlExecutable,
			ExpectedNiriVersion:  niriipc.SupportedVersion,
			RequestTimeout:       15 * time.Second,
			KeepaliveInterval:    15 * time.Second,
			KeepaliveCount:       3,
			RetryMaxAttempts:     3,
			RetryInitialBackoff:  200 * time.Millisecond,
			RetryMaxBackoff:      2 * time.Second,
			GraphicalContextKeys: []string{"NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"},
			Controller: SliceControllerConfig{
				Enabled: false, HostID: "host", LeechID: "leech", PollInterval: 2 * time.Second,
				ControlTimeout: 5 * time.Second, RetryWindow: 30 * time.Second,
				SourceGoneGrace: 5 * time.Second, SourceGoneConfirmations: 2, AuthorityMode: "host_location",
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
	if cfg.Slice.TransportOptions == nil {
		cfg.Slice.TransportOptions = []string{}
	}
	if cfg.Slice.RPCCommand == nil {
		cfg.Slice.RPCCommand = []string{}
	}
	if cfg.Slice.GraphicalContextKeys == nil {
		cfg.Slice.GraphicalContextKeys = []string{}
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
	if err := validateSlice(cfg.Slice); err != nil {
		return err
	}
	return nil
}

func validateSlice(cfg SliceConfig) error {
	for name, value := range map[string]string{
		"selfCommand": cfg.SelfCommand, "kittyCommand": cfg.KittyCommand,
		"transportCommand": cfg.TransportCommand, "zellijCommand": cfg.ZellijCommand,
		"niriCommand": cfg.NiriCommand, "systemctlCommand": cfg.SystemctlCommand,
	} {
		if strings.TrimSpace(value) == "" || len(value) > slicecontroller.MaxProjectionArgvEntryBytes || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("slice.%s must be a bounded non-empty executable without control characters", name)
		}
	}
	if cfg.SourceHost != "" && (strings.TrimSpace(cfg.SourceHost) == "" || len(cfg.SourceHost) > slicecontroller.MaxProjectionArgvEntryBytes || strings.ContainsAny(cfg.SourceHost, "\x00\r\n")) {
		return fmt.Errorf("slice.sourceHost must be bounded and contain no control characters when set")
	}
	if len(cfg.RPCCommand) == 0 || len(cfg.RPCCommand) > 16 {
		return fmt.Errorf("slice.rpcCommand must contain 1 to 16 argv entries")
	}
	for _, value := range cfg.RPCCommand {
		if value == "" || len(value) > slicecontroller.MaxProjectionArgvEntryBytes || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("slice rpc argv entries must be bounded and contain no control characters")
		}
	}
	if err := slicecontroller.ValidateProjectionTransportOptions(cfg.TransportOptions); err != nil {
		return fmt.Errorf("slice.transportOptions: %w", err)
	}
	if cfg.ExpectedNiriVersion != niriipc.SupportedVersion {
		return fmt.Errorf("slice.expectedNiriVersion must be %s", niriipc.SupportedVersion)
	}
	if cfg.RequestTimeout <= 0 || cfg.KeepaliveInterval <= 0 || cfg.RetryInitialBackoff <= 0 || cfg.RetryMaxBackoff <= 0 {
		return fmt.Errorf("slice timeout, keepalive, and retry durations must be positive")
	}
	if cfg.KeepaliveCount < 1 || cfg.KeepaliveCount > 10 || cfg.RetryMaxAttempts < 1 || cfg.RetryMaxAttempts > 10 || cfg.RetryInitialBackoff > cfg.RetryMaxBackoff {
		return fmt.Errorf("slice keepalive/retry bounds are invalid")
	}
	allowed := map[string]bool{"NIRI_SOCKET": true, "WAYLAND_DISPLAY": true, "XDG_RUNTIME_DIR": true}
	if len(cfg.GraphicalContextKeys) != len(allowed) {
		return fmt.Errorf("slice.graphicalContextKeys must contain exactly NIRI_SOCKET, WAYLAND_DISPLAY, and XDG_RUNTIME_DIR")
	}
	seen := map[string]bool{}
	for _, key := range cfg.GraphicalContextKeys {
		if !allowed[key] || seen[key] {
			return fmt.Errorf("slice.graphicalContextKeys contains unsupported or duplicate key %q", key)
		}
		seen[key] = true
	}
	if cfg.Clipboard.Enabled {
		return fmt.Errorf("slice.clipboard.enabled must remain false for the first rollout")
	}
	if cfg.AttachPrivateRoot != "" && !filepath.IsAbs(cfg.AttachPrivateRoot) {
		return fmt.Errorf("slice.attachPrivateRoot must be absolute when set")
	}
	if cfg.AttachShimCache != "" && !filepath.IsAbs(cfg.AttachShimCache) {
		return fmt.Errorf("slice.attachShimCache must be absolute when set")
	}
	controller := cfg.Controller
	for name, value := range map[string]string{"hostID": controller.HostID, "leechID": controller.LeechID} {
		if strings.TrimSpace(value) == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n/") {
			return fmt.Errorf("slice.controller.%s must be a bounded namespace identity", name)
		}
	}
	if controller.PollInterval <= 0 || controller.ControlTimeout <= 0 || controller.RetryWindow <= 0 || controller.SourceGoneGrace <= 0 {
		return fmt.Errorf("slice.controller timing values must be positive")
	}
	if controller.SourceGoneConfirmations < 2 || controller.SourceGoneConfirmations > 20 {
		return fmt.Errorf("slice.controller.sourceGoneConfirmations must be between 2 and 20")
	}
	if controller.AuthorityMode != "host_location" {
		return fmt.Errorf("slice.controller.authorityMode must be host_location in v1")
	}
	if controller.LeechWriteAuthorized {
		return fmt.Errorf("slice.controller.leechWriteAuthorized must be false in v1")
	}
	return nil
}
