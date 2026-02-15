package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := DefaultStateDir()
	want := filepath.Join(home, ".terminal-redeemer")

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestLoadMissingDefaultPathUsesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load("", false)
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}

	if cfg.StateDir != filepath.Join(home, ".terminal-redeemer") {
		t.Fatalf("expected default state dir, got %q", cfg.StateDir)
	}
	if cfg.Host != "local" {
		t.Fatalf("expected default host local, got %q", cfg.Host)
	}
	if cfg.Capture.Interval != 60*time.Second {
		t.Fatalf("expected default interval 60s, got %s", cfg.Capture.Interval)
	}
}

func TestLoadMissingExplicitPathReturnsError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), true)
	if err == nil {
		t.Fatal("expected error for explicit missing config")
	}
}

func TestLoadYAMLMergesOverDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`stateDir: /tmp/redeem
host: host-a
capture:
  interval: 15s
  snapshotEvery: 5
processMetadata:
  whitelist:
    - zellij
  whitelistExtra:
    - tmux
  includeSessionTag: false
retention:
  days: 14
restore:
  appAllowlist:
    firefox: firefox --new-window
  terminal:
    command: foot
    zellijAttachOrCreate: false
`), 0o600)
	if err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := Load(configPath, true)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.StateDir != "/tmp/redeem" {
		t.Fatalf("expected stateDir from YAML, got %q", cfg.StateDir)
	}
	if cfg.Host != "host-a" {
		t.Fatalf("expected host from YAML, got %q", cfg.Host)
	}
	if cfg.Profile != "default" {
		t.Fatalf("expected default profile, got %q", cfg.Profile)
	}
	if cfg.Capture.Interval != 15*time.Second {
		t.Fatalf("expected interval 15s, got %s", cfg.Capture.Interval)
	}
	if cfg.Capture.SnapshotEvery != 5 {
		t.Fatalf("expected snapshotEvery 5, got %d", cfg.Capture.SnapshotEvery)
	}
	if len(cfg.ProcessMetadata.Whitelist) != 1 || cfg.ProcessMetadata.Whitelist[0] != "zellij" {
		t.Fatalf("unexpected whitelist: %#v", cfg.ProcessMetadata.Whitelist)
	}
	if len(cfg.ProcessMetadata.WhitelistExtra) != 1 || cfg.ProcessMetadata.WhitelistExtra[0] != "tmux" {
		t.Fatalf("unexpected whitelistExtra: %#v", cfg.ProcessMetadata.WhitelistExtra)
	}
	if cfg.ProcessMetadata.IncludeSessionTag != false {
		t.Fatalf("expected includeSessionTag false, got %v", cfg.ProcessMetadata.IncludeSessionTag)
	}
	if cfg.Retention.Days != 14 {
		t.Fatalf("expected retention days 14, got %d", cfg.Retention.Days)
	}
	if cfg.Restore.Terminal.Command != "foot" {
		t.Fatalf("expected terminal command foot, got %q", cfg.Restore.Terminal.Command)
	}
	if cfg.Restore.Terminal.ZellijAttachOrCreate != false {
		t.Fatalf("expected zellijAttachOrCreate false, got %v", cfg.Restore.Terminal.ZellijAttachOrCreate)
	}
	if cfg.Restore.AppAllowlist["firefox"] != "firefox --new-window" {
		t.Fatalf("unexpected app allowlist: %#v", cfg.Restore.AppAllowlist)
	}
}
