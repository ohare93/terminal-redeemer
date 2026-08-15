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
	if !cfg.Restore.ReconcileWorkspaceMoves {
		t.Fatalf("expected reconcile workspace moves default true")
	}
	if cfg.Restore.WorkspaceReconcileDelay <= 0 {
		t.Fatalf("expected positive workspace reconcile delay, got %s", cfg.Restore.WorkspaceReconcileDelay)
	}
	if cfg.Restore.OnStartup {
		t.Fatal("expected manual resume default (restore.onStartup false)")
	}
	if cfg.Restore.MaxCheckpointAge != 24*time.Hour || cfg.Restore.UnresolvedWorkspace != "current" || cfg.Restore.ResumeTimeout != 10*time.Second || cfg.Restore.ResumePollInterval != 100*time.Millisecond {
		t.Fatalf("unexpected safe resume defaults: %#v", cfg.Restore)
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
  onStartup: true
  appAllowlist:
    firefox: firefox --new-window
  appMode:
    firefox: oneshot
  reconcileWorkspaceMoves: false
  workspaceReconcileDelay: 3s
  maxCheckpointAge: 12h
  unresolvedWorkspace: current
  resumeTimeout: 8s
  resumePollInterval: 25ms
  terminal:
    command: foot
    zellijAttachOrCreate: false
mirror:
  sourceHost: source-a
  sshCommand: custom-ssh
  sshOptions: ["-p", "2222"]
  snapshotCommand: [remote-redeem, mirror, snapshot]
  launcherCommand: custom-kitty
  selfCommand: /bin/redeem
  appID: redeem-owned
  defaultMode: watch
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
	if !cfg.Restore.OnStartup {
		t.Fatal("expected onStartup true from YAML")
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
	if cfg.Restore.AppMode["firefox"] != "oneshot" {
		t.Fatalf("unexpected app mode: %#v", cfg.Restore.AppMode)
	}
	if cfg.Restore.ReconcileWorkspaceMoves {
		t.Fatalf("expected reconcileWorkspaceMoves false, got true")
	}
	if cfg.Restore.WorkspaceReconcileDelay != 3*time.Second {
		t.Fatalf("expected workspaceReconcileDelay 3s, got %s", cfg.Restore.WorkspaceReconcileDelay)
	}
	if cfg.Restore.MaxCheckpointAge != 12*time.Hour || cfg.Restore.UnresolvedWorkspace != "current" || cfg.Restore.ResumeTimeout != 8*time.Second || cfg.Restore.ResumePollInterval != 25*time.Millisecond {
		t.Fatalf("unexpected resume policy: %#v", cfg.Restore)
	}
	if cfg.Mirror.SourceHost != "source-a" || cfg.Mirror.SSHCommand != "custom-ssh" || cfg.Mirror.DefaultMode != "watch" {
		t.Fatalf("unexpected mirror config: %#v", cfg.Mirror)
	}
	if cfg.Mirror.OpenDelay != 25*time.Millisecond || cfg.Mirror.Clipboard.TempDir != "/var/tmp" || cfg.Mirror.Clipboard.MIMETypes[0] != "image/webp" {
		t.Fatalf("unexpected mirror timing/clipboard config: %#v", cfg.Mirror)
	}
}

func TestLoadRejectsInvalidResumeConfig(t *testing.T) {
	for name, payload := range map[string]string{
		"non-positive age":     "restore:\n  maxCheckpointAge: 0s\n",
		"non-positive timeout": "restore:\n  resumeTimeout: 0s\n",
		"poll exceeds timeout": "restore:\n  resumeTimeout: 1s\n  resumePollInterval: 2s\n",
		"unknown workspace":    "restore:\n  unresolvedWorkspace: anywhere\n",
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

func TestLoadRejectsInvalidMirrorConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mirror:\n  defaultMode: edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("expected invalid mirror mode error")
	}
}

func TestLoadRejectsRemovedSliceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("slice: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("expected removed slice config to be rejected")
	}
}
