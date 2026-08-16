# Configuration

Configuration precedence is:

1. built-in defaults;
2. YAML from `--config PATH` or the XDG default path;
3. per-command flags.

The YAML decoder rejects unknown fields.

```yaml
stateDir: ~/.terminal-redeemer
host: local
profile: default

capture:
  interval: 60s
  niriCommand: niri msg -j windows

processMetadata:
  whitelist: []
  whitelistExtra: []
  includeSessionTag: true

retention:
  days: 30

resume:
  onStartup: false
  maxCheckpointAge: 24h
  unresolvedWorkspace: current # current, skip, or fail
  timeout: 10s
  pollInterval: 100ms
  terminalCommand: kitty

mirror:
  sourceHost: ""
  sshCommand: ssh
  sshOptions: []
  snapshotCommand: [redeem, mirror, snapshot]
  launcherCommand: kitty
  selfCommand: redeem
  appID: terminal-redeemer-mirror
  openDelay: 150ms
  niriCommand: niri
  clipboard:
    enabled: true
    command: wl-paste
    scpCommand: scp
    scpOptions: []
    kittyCommand: kitty
    tempDir: /tmp
    mimeTypes: [image/png, image/jpeg, image/webp, image/gif]
```

## Capture and checkpoints

`capture.interval` controls the Home Manager timer and `capture.niriCommand` controls the complete Niri windows/workspaces query. Every successful capture refreshes one deterministic checkpoint for the current boot/host/profile identity. Window titles remain in the checkpoint, while the state hash excludes volatile titles.

`host` partitions local checkpoints. `mirror.sourceHost` is instead the SSH destination for remote terminal access. `mirror new` creates the persistent session only through the local viewer, then best-effort asks the source-side Redeem to open one attach-only Kitty. Pass `--source-workspace NAME_OR_NUMBER` to place that source window once; there is no persistent workspace setting or hardcoded workspace in Redeem. Source-helper failure is a warning and never rolls back the created session.

## Resume policy

`resume.onStartup` is consumed by the Home Manager module. Setting it in a hand-written YAML file does not install a service.

`resume.maxCheckpointAge` rejects a stale newest prior-boot candidate without falling back to an older boot. `resume.unresolvedWorkspace` controls terminals whose captured workspace cannot be resolved by name, output plus index, or index:

- `current`: attach on the current workspace and report degraded;
- `skip`: do not launch that item;
- `fail`: report it as failed.

`resume.timeout` and `resume.pollInterval` bound Niri readiness, exact Kitty PID correlation, Zellij attachment evidence, and placement verification. `resume.terminalCommand` must be a direct Kitty-compatible executable, not a shell pipeline or daemonizing wrapper.

CLI `--max-age`, `--unresolved-workspace`, `--timeout`, `--poll-interval`, and `--launcher-command` override these values for one invocation.

## Retention

`retention.days` bounds rolling boot checkpoints when `redeem prune run` executes. Prune shares capture's advisory writer lock and fsyncs the checkpoint directory after removals.

## Home Manager and NixOS

The Home Manager module exposes typed `capture`, `resume`, `retention`, and `mirror` options. The NixOS wrapper forwards per-user settings to Home Manager; it does not create a system-level graphical service.

```nix
programs.terminal-redeemer = {
  enable = true;
  resume.onStartup = true;
  mirror.sourceHost = "lattice";
};
```

`mirror.snapshotCommand` is also the trusted source-helper prefix. For dual-visible `mirror new`, it must end with the exact argv suffix `[mirror, snapshot]`; Redeem preserves any preceding wrapper argv and replaces only that suffix with `mirror attach-local`. Unsupported wrappers degrade after creation with a warning. Lattice and Overton should run compatible Redeem and pinned Zellij versions; version skew leaves the persistent Overton-created session intact but may prevent the source view.

Disable competing startup terminal restorers before enabling `resume.onStartup`.
