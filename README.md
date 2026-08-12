# terminal-redeemer

`terminal-redeemer` owns terminal/window-session capture, history, restore, and live cross-host mirroring for Niri, Kitty, and Zellij. The CLI is `redeem`.

## Product model

- **Restore dead sessions:** query complete Niri and terminal state on every capture, append history only when restore-relevant normalized state changes (not volatile window titles), and recreate local applications and Zellij terminals. A crash-durable rolling checkpoint per boot tracks the latest successful observation; restore preserves the captured terminal CWD.
- **Project live sessions:** the opt-in `slice` controller continuously projects selected eligible host Kitty/live-Zellij terminals; the legacy `mirror` commands remain one-shot interactive attachment.

Legacy mirroring is an explicit CLI action, not a continuous synchronization daemon. Host names are configuration values; no host identity is built in.

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

See [docs/OPERATIONS.md](docs/OPERATIONS.md) for operations and the [host/leech readiness and consumer contract](docs/HOST_LEECH_READINESS.md) for deployment order, rollback invariants, the generated Niri binding template, and the operator smoke gate.

## Continuous terminal slices

The slice controller and routed Leech mode are separate opt-ins and remain disabled by default. After the host inventory and leech controller are enrolled, manage current policy from a terminal:

```bash
redeem slice manage
redeem slice controller workspace-add --workspace Work  # current/future eligible terminals in Work
redeem slice controller all-enable                       # all current/future eligible host terminals
redeem slice controller pickup --source-id src_...      # exact-source inclusion
redeem slice controller pickup-remove --source-id src_...
redeem slice controller close --source-id src_...       # local projection only; host work survives
redeem slice controller reopen --source-id src_...
```

All-eligible is an additive projection reason: it includes named and unnamed host workspaces but does not broaden routed Super+Enter, which still requires an explicitly selected named workspace and separate Leech mode. Unnamed sources attach without cross-machine spatial placement and appear under `(unnamed)` in the manager.

`redeem slice close-focused` is the ownership-checked command for a consumer keybinding. Home Manager also exports `slice.manageCommand`, direct Kitty argv for opening the live manager; consumers choose any binding and the module installs none. See [docs/OPERATIONS.md](docs/OPERATIONS.md) before enabling the controller or routed launch mode.

## Legacy one-shot mirroring

Configure `mirror.sourceHost`, or pass `--host`:

```bash
# Source-side JSON contract (backward compatible)
redeem mirror snapshot

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

The noninteractive `--all`, repeatable `--session`, and `--select N` flags are unchanged. `open` preserves source order, host, title, session, and CWD in the launch plan. Interactive `attach` remains supported. Pinned Zellij 0.44.3 has no `watch` command, so `--mode watch` returns an explicit unsupported result without constructing a command. The legacy attach command clears nested-Zellij environment variables.

Mirrored Kitty windows map Ctrl+V to `redeem mirror paste-image`. Supported local image clipboard data is written to a unique temporary path, copied to the same path on the source with SCP, and that path is injected through the window's private Kitty control socket. Non-image or unreadable clipboard data forwards Ctrl+V unchanged. Use `mirror.clipboard.enabled: false` or `open --no-clipboard` to disable this mapping.

## Architecture and constraints

Application logic and process planning live in Go under `internal/mirror`. SSH, Niri, Kitty, SCP, and clipboard calls cross a small argv-based runner interface, allowing tests to use fakes without Wayland or network access. Remote shell fragments are limited to explicitly quoted snapshot/attach and remote-directory commands.

Current live-mirror constraints:

- local compositor: Niri (JSON window listing and close action)
- local terminal launcher: Kitty-compatible command and remote-control behavior
- remote multiplexer: Zellij
- source host: `redeem mirror snapshot` available through SSH
- legacy mirror remains one-shot; the separate slice controller is opt-in and disabled by default

Architecture decisions: [prior-boot resume](docs/adr/0001-resume-zellij-terminals-in-niri.md), [host-leech terminal slices](docs/adr/0002-host-leech-terminal-slice-domain-and-lifecycle.md), controller [workspace sharing and persistence semantics](docs/adr/0003-terminal-slice-workspace-sharing-and-persistence.md), the [single-monitor Niri spatial mapping policy](docs/adr/0004-single-monitor-niri-spatial-mapping-policy.md), and [global selection with live management](docs/adr/0005-global-slice-selection-and-live-management.md).

The additive [live source inventory, RPC, and controller protocol](docs/PROTOCOL.md) provides explicit initialization, revisioned complete/degraded snapshots, bounded typed RPC, crash-safe launch tokens, exact live-only attachment, an opt-in foreground reconciler, and disabled-by-default routed Leech launches. `redeem slice launch` routes selected static workspaces to exactly one host Kitty/Zellij transaction and never falls back locally after remote intent; the host owns execution/work and the leech window is only a projection. The module exports packaged launch argv but installs no consumer keybinding. These surfaces do not replace the legacy one-shot mirror JSON contract.

See [docs/CONFIG.md](docs/CONFIG.md) for precedence/schema, [docs/OPERATIONS.md](docs/OPERATIONS.md) for dependencies, security, and troubleshooting, and the [hermetic host/leech acceptance matrix](docs/testing/host-leech-hermetic-matrix.md) for executable test traceability and the separate live operator smoke gate.

## Other commands

```bash
redeem capture once|run
redeem history list|inspect
redeem resume [--dry-run]
redeem slice inventory init|snapshot
redeem slice rpc
redeem slice attach --session NAME --token TOKEN
redeem slice controller init|run|status|workspace-add|workspace-remove|all-enable|all-disable|pickup|pickup-remove|drop|close|reopen|undo|reconnect
redeem slice mode enable|disable|status
redeem slice launch [--reconnect-token TOKEN]
redeem slice manage
redeem slice close-focused
redeem restore apply|tui
redeem prune run
redeem doctor
```

## Flake outputs

- `packages.<system>.terminal-redeemer`
- `packages.<system>.host-leech-consumer-contract`
- `apps.<system>.redeem`
- `homeManagerModules.terminal-redeemer`
- `nixosModules.terminal-redeemer`
- `lib.sliceConsumerContract`

The contract package contains the versioned technical contract, strict schema, source Niri binding template, and rendered store-path binding fragment. Repository checks validate the schema and require packaged source artifacts to match the repository bytes.
