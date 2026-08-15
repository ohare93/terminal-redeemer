# terminal-redeemer

`terminal-redeemer` owns terminal/window-session capture, history, restore, and live cross-host mirroring for Niri, Kitty, and Zellij. The CLI is `redeem`.

## Product model

- **Restore dead sessions:** query complete Niri and terminal state on every capture, append history only when restore-relevant normalized state changes (not volatile window titles), and recreate local applications and Zellij terminals. A crash-durable rolling checkpoint per boot tracks the latest successful observation; restore preserves the captured terminal CWD.
- **Use live remote sessions:** `mirror new` creates a persistent Zellij terminal on a configured SSH host, while `mirror open` discovers and attaches to visible or headless live sessions.

Mirroring is an explicit action, not a continuous synchronization daemon. Host names are configuration values; no host identity is built in.

## Quick start

```bash
redeem capture once
redeem resume --dry-run  # inspect the prior-boot terminal plan; no mutation
redeem resume            # idempotently apply that same plan
redeem history list
redeem restore tui
redeem restore apply --at 10m --dry-run
```

Manual `redeem resume` is the distributed default. It restores attachable Zellij terminals only; arbitrary GUI applications remain outside its default scope. Resume merges rolling prior-boot checkpoints with boot-aware event fallback, while `restore apply`/`restore tui` retain access to legacy and timestamped history. `restore apply` requires `--at`. Without `--yes` it previews; with `--yes` it executes.

## Optional startup resume

Home Manager exposes `programs.terminal-redeemer.restore.onStartup`, defaulting to `false`. When explicitly enabled it installs a bounded-retry graphical-session user oneshot whose exact applying command is `redeem --config …/terminal-redeemer/config.yaml resume`; it does not contain another restore implementation. **Before enabling it, disable every host-local Niri/Kitty/Zellij startup restoration hook** to prevent duplicate ownership. Removing those old hooks from a consumer repository is a follow-up in that repository, not a change made here.

See [docs/OPERATIONS.md](docs/OPERATIONS.md) for deployment, security, and smoke checks.

## Live remote terminals

Configure `mirror.sourceHost`, or pass `--host`:

```bash
# Source-side JSON contract (backward compatible)
redeem mirror snapshot

# Create a persistent remote terminal and attach locally
redeem mirror new --host workstation.example

# Consumer-side discovery
redeem mirror list --host workstation.example
redeem mirror list --host workstation.example --json

# Interactive chooser (when no selection flag is supplied)
redeem mirror open --host workstation.example

# Deterministic automation
redeem mirror open --host workstation.example --session project-a --mode attach
redeem mirror open --host workstation.example --all --mode watch --dry-run
redeem mirror open --snapshot-file fixture.json --host source --select 2 --dry-run

# Only Terminal Redeemer-owned windows are listed or closed
redeem mirror status --host workstation.example
redeem mirror status --all-hosts --json
redeem mirror close --host workstation.example --dry-run
redeem mirror close --host workstation.example
```

`open` accepts exactly one selection strategy: `--all`, repeatable `--session`, `--select N`, or its interactive picker. The picker groups sessions under their Niri workspace (with an `(unnamed)` fallback) while preserving discovery order. Use Up/Down or j/k to move, type to filter by session, title, workspace, or CWD, Backspace to edit the filter, Space to check multiple sessions, and Ctrl+A to toggle all sessions in the current filter. Enter opens checked sessions in discovery order, or the focused session when none are checked. Esc first clears a non-empty filter and then cancels; Ctrl+C also cancels. `NO_COLOR` disables picker colours without changing its markers or layout.

The noninteractive `--all`, repeatable `--session`, and `--select N` flags are unchanged. `open` preserves source order, host, title, session, and CWD in the launch plan. Interactive `attach` remains supported. Pinned Zellij 0.44.3 has no `watch` command, so `--mode watch` returns an explicit unsupported result without constructing a command. Attach commands clear nested-Zellij environment variables.

Mirrored Kitty windows map Ctrl+V to `redeem mirror paste-image`. Supported local image clipboard data is written to a unique temporary path, copied to the same path on the source with SCP, and that path is injected through the window's private Kitty control socket. Non-image or unreadable clipboard data forwards Ctrl+V unchanged. Use `mirror.clipboard.enabled: false` or `open --no-clipboard` to disable this mapping.

## Architecture and constraints

Application logic and process planning live in Go under `internal/mirror`. SSH, Niri, Kitty, SCP, and clipboard calls cross a small argv-based runner interface, allowing tests to use fakes without Wayland or network access. Remote shell fragments are limited to explicitly quoted snapshot/attach and remote-directory commands.

Current live-mirror constraints:

- local compositor: Niri (JSON window listing and close action)
- local terminal launcher: Kitty-compatible command and remote-control behavior
- remote multiplexer: Zellij
- source host: `redeem mirror snapshot` available through SSH

Architecture decision: [prior-boot resume](docs/adr/0001-resume-zellij-terminals-in-niri.md).

See [docs/CONFIG.md](docs/CONFIG.md) for precedence/schema and [docs/OPERATIONS.md](docs/OPERATIONS.md) for dependencies, security, and troubleshooting.

## Other commands

```bash
redeem capture once|run
redeem history list|inspect
redeem resume [--dry-run]
redeem mirror new|open|list|snapshot|status|close|paste-image
redeem restore apply|tui
redeem prune run
redeem doctor
```

## Flake outputs

- `packages.<system>.terminal-redeemer`
- `apps.<system>.redeem`
- `homeManagerModules.terminal-redeemer`
- `nixosModules.terminal-redeemer`
