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
	if got, want := DefaultStateDir(), filepath.Join(home, ".terminal-redeemer"); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestLoadMissingDefaultPathUsesSafeDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := Load("", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != filepath.Join(home, ".terminal-redeemer") || cfg.Host != "local" || cfg.Capture.Interval != time.Minute {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.Resume.OnStartup || cfg.Resume.MaxCheckpointAge != 24*time.Hour || cfg.Resume.UnresolvedWorkspace != "current" || cfg.Resume.Timeout != 10*time.Second || cfg.Resume.PollInterval != 100*time.Millisecond || cfg.Resume.TerminalCommand != "kitty" {
		t.Fatalf("unexpected safe resume defaults: %#v", cfg.Resume)
	}
}

func TestLoadMissingExplicitPathReturnsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), true); err == nil {
		t.Fatal("expected error for explicit missing config")
	}
}

func TestLoadYAMLMergesOverDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	payload := `stateDir: /tmp/redeem
host: host-a
capture:
  interval: 15s
processMetadata:
  whitelist: [zellij]
  whitelistExtra: [tmux]
  includeSessionTag: false
retention:
  days: 14
resume:
  onStartup: true
  maxCheckpointAge: 12h
  unresolvedWorkspace: skip
  timeout: 8s
  pollInterval: 25ms
  terminalCommand: foot
mirror:
  sourceHost: source-a
  sshCommand: custom-ssh
  sshOptions: ["-p", "2222"]
  snapshotCommand: [remote-redeem, mirror, snapshot]
  launcherCommand: custom-kitty
  selfCommand: /bin/redeem
  appID: redeem-owned
  openDelay: 25ms
  niriCommand: custom-niri
  clipboard:
    enabled: true
    command: custom-paste
    scpCommand: custom-scp
    scpOptions: ["-P", "2222"]
    kittyCommand: custom-kitty
    tempDir: /var/tmp
    mimeTypes: [image/webp]
`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != "/tmp/redeem" || cfg.Host != "host-a" || cfg.Capture.Interval != 15*time.Second || cfg.Retention.Days != 14 {
		t.Fatalf("unexpected core config: %#v", cfg)
	}
	if !cfg.Resume.OnStartup || cfg.Resume.MaxCheckpointAge != 12*time.Hour || cfg.Resume.UnresolvedWorkspace != "skip" || cfg.Resume.Timeout != 8*time.Second || cfg.Resume.PollInterval != 25*time.Millisecond || cfg.Resume.TerminalCommand != "foot" {
		t.Fatalf("unexpected resume config: %#v", cfg.Resume)
	}
	if cfg.Mirror.SourceHost != "source-a" || cfg.Mirror.SSHCommand != "custom-ssh" || cfg.Mirror.OpenDelay != 25*time.Millisecond || cfg.Mirror.Clipboard.TempDir != "/var/tmp" {
		t.Fatalf("unexpected mirror config: %#v", cfg.Mirror)
	}
}

func TestLoadRejectsInvalidResumeConfig(t *testing.T) {
	for name, payload := range map[string]string{
		"non-positive age":     "resume:\n  maxCheckpointAge: 0s\n",
		"non-positive timeout": "resume:\n  timeout: 0s\n",
		"poll exceeds timeout": "resume:\n  timeout: 1s\n  pollInterval: 2s\n",
		"unknown workspace":    "resume:\n  unresolvedWorkspace: anywhere\n",
		"empty terminal":       "resume:\n  terminalCommand: ''\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, true); err == nil {
				t.Fatal("expected invalid resume config error")
			}
		})
	}
}

func TestLoadRejectsIncompleteClipboardCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mirror:\n  selfCommand: ''\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("expected enabled clipboard bridge to require self command")
	}
}

func TestLoadRejectsUnknownConfigField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("unknownField: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("expected unknown config field to be rejected")
	}
}
