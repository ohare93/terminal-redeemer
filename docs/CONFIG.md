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

`redeem mirror save --host HOST` and `redeem mirror apply --host HOST` always acquire a fresh source snapshot over the configured SSH command; state-mutating commands do not accept local snapshot fixtures. Live evidence must match Redeem's complete configured SSH executable/options argv and static generated Zellij command. To keep the destination position provable, `mirror.sshOptions` accepts only flags `-4`, `-6`, `-C`, `-a`, `-n`, `-q`, `-T`, `-t`, `-v`, and `-x`, plus argument options `-F`, `-J`, `-i`, `-l`, `-o`, and `-p` (separate or attached values); operands, unknown options, and a configured `--` are rejected. Redeem appends its own option boundary, destination, and one remote command. `HOST` is persisted and compared as the exact configured SSH destination token (including an alias or `user@` prefix); Redeem cannot attest the canonical machine behind SSH config. The single replaceable pin for the fresh source snapshot's profile is stored under `stateDir/mirror/pins/`, not `checkpoints/`, and is therefore outside rolling capture and retention pruning. No command argv or shell payload is persisted.

`redeem mirror follow --host HOST` has no persistent configuration or state. It requires a complete source-side workspace and ACTIVE-session inventory, freezes the matching local workspace runtime identity at selection, and runs only while its foreground TUI remains open. Defaults are `--interval 5s`, `--max-per-poll 4`, and `--max-total 64`; intervals below two seconds are rejected and disconnected polls use capped backoff. These safeguards can be increased only with explicit command-line flags.

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
