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
	AppAllowlist map[string]string `yaml:"appAllowlist"`
	Terminal     TerminalConfig    `yaml:"terminal"`
}

type TerminalConfig struct {
	Command              string `yaml:"command"`
	ZellijAttachOrCreate bool   `yaml:"zellijAttachOrCreate"`
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
			AppAllowlist: map[string]string{},
			Terminal: TerminalConfig{
				Command:              "kitty",
				ZellijAttachOrCreate: true,
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
	if cfg.ProcessMetadata.Whitelist == nil {
		cfg.ProcessMetadata.Whitelist = []string{}
	}
	if cfg.ProcessMetadata.WhitelistExtra == nil {
		cfg.ProcessMetadata.WhitelistExtra = []string{}
	}

	return cfg, nil
}
