# terminal-redeemer Design Spec (V1)

## 1) Purpose

`terminal-redeemer` is a CLI-first session history and restore tool for Wayland/Niri workflows, focused on preserving and reconstructing terminal context and workspace ordering over time.

It records sparse, append-only state changes (JSONL) plus periodic snapshots, enabling rewindable restore points and interactive selection at login or on demand.

Primary goal:
- recover terminal and app layout context after reboot, crash, or session loss.

## 2) Scope

### In Scope (V1)
- Append-only event log of window/session changes.
- Periodic snapshots for fast state reconstruction.
- Timer-based capture with change detection.
- Interactive restore TUI (Bubble Tea).
- Restore to any timestamp.
- Additive restore mode only (do not close existing windows).
- Terminal restore via kitty + zellij attach-or-create.
- Optional terminal process metadata capture from whitelist.
- Home Manager module + flake packaging.
- Host/profile-partitioned state.
- Retention pruning (30 days).

### Out of Scope (V1)
- Bottles implementation (designed for V2 only).
- Event-driven IPC capture.
- NixOS module (system-level).
- Full process graph capture.
- Auto-restore enabled by default.

## 3) Product Principles

- CLI-first, scriptable, composable.
- Append-only history for auditability and rewind.
- Sparse patch events to reduce churn.
- Best-effort restore with explicit summary.
- Safe defaults: no personal app mappings baked in.
- All behavior configurable via flake/Home Manager module.

## 4) High-Level Architecture

Components:
1. Collector
   - Polls Niri state (windows/workspaces).
   - Enriches terminal metadata (cwd + whitelist process tags).
   - Produces normalized in-memory state.
2. Diff Engine
   - Computes sparse patch events against last materialized state.
   - Emits only changed properties.
3. Event Store
   - Appends JSONL events.
   - Rotates/writes snapshots every N events.
4. Replay Engine
   - Reconstructs state at timestamp from nearest snapshot + event replay.
5. Restore Planner
   - Builds additive execution plan from selected restore point.
6. Restore Executor
   - Applies plan via Niri spawn/focus actions and command templates.
7. TUI
   - Interactive restore selection, filtering, and action preview.
8. Pruner
   - Retention-based cleanup for events/snapshots.

## 5) Repository Layout

```text
terminal-redeemer/
  cmd/redeem/
    main.go
  internal/
    collector/
    diff/
    events/
    replay/
    restore/
    tui/
    config/
    prune/
    niri/
    procmeta/
  modules/
    home-manager/
      terminal-redeemer.nix
  docs/
    DESIGN.md
    CONFIG.md
    OPERATIONS.md
  flake.nix
  flake.lock
  go.mod
  go.sum
  README.md
```

## 6) Storage Model

Default root:
- `~/.terminal-redeemer`

Partition:
- `<stateDir>/<hostname>/<profile>/`

Files:
- `events.jsonl` (append-only)
- `snapshots/<unix-ts>.json`
- `meta/state-index.json` (cursor/checkpoint)
- `meta/lock` (process lock)

### Event Envelope (JSONL)

```json
{
  "v": 1,
  "ts": "2026-02-15T10:12:34.123Z",
  "host": "overton",
  "profile": "default",
  "event_type": "window_patch",
  "window_key": "w:kitty:abc123",
  "patch": {
    "workspace_idx": 2,
    "terminal": {
      "cwd": "/home/jmo/Development/active/tools",
      "mux": {
        "kind": "zellij",
        "session": "redeemer"
      }
    }
  },
  "source": "capture.timer",
  "state_hash": "sha256:..."
}
```

Rules:
- `patch` is sparse: only changed keys.
- all payload properties optional.
- event ordering is append order.
- schema versioned with `v`.

### Snapshot Schema

```json
{
  "v": 1,
  "created_at": "2026-02-15T10:20:00Z",
  "host": "overton",
  "profile": "default",
  "last_event_offset": 12345,
  "state": {
    "workspaces": { "...": "..." },
    "windows": { "...": "..." }
  },
  "state_hash": "sha256:..."
}
```

## 7) Identity Model

Window identity in V1:
- Use current strategy compatible with existing setup behavior.
- Include compositor ID-derived key + app identity.
- For terminals, enrich with:
  - terminal class/app id
  - zellij session (if detected)
  - process tags from whitelist
- Keys are stable enough for additive restore planning, not guaranteed global permanence across all lifecycle edge cases (improved in V2).

## 8) Capture Pipeline

1. Poll Niri JSON (windows/workspaces) every `capture.interval` (default `60s`).
2. Build normalized model.
3. Enrich terminals:
   - cwd best effort from pid/cwd.
   - detect whitelisted foreground/child process names.
   - extract `session_tag` best effort (args/title/env parse).
4. Hash normalized state.
5. If hash unchanged from previous, no write.
6. If changed:
   - diff -> sparse patch events.
   - append events.
   - every `snapshotEvery` events (default 100): write snapshot.
7. Periodic prune by retention policy (default 30 days).

Failure handling:
- collector errors logged; loop continues.
- partial enrichment failure does not block event write.

## 9) Restore Semantics

### Restore Point
- Any timestamp selected in TUI.
- Replay from nearest snapshot <= timestamp + events up to timestamp.

### Plan Generation
- Group by workspace then app.
- For each planned item:
  - determine whether restorable under allowlist/config.
  - mark status: `ready`, `skipped`, `degraded`.
- Additive only: no closing/killing existing windows.

### Execution
- Workspaces: focus when needed.
- Terminal windows:
  - spawn kitty command template.
  - run zellij attach-or-create:
    - `zellij attach <session> || zellij -s <session>`
  - if required terminal metadata missing => skip window.
- Non-terminal apps:
  - only if app ID exists in user allowlist map.

### Exit Behavior
- continue on non-fatal failures.
- final summary with counts:
  - restored / skipped / failed
- non-zero exit only on fatal init/replay/config errors.

## 10) CLI Contract (V1)

```bash
redeem capture run
redeem capture once

redeem history list [--from ...] [--to ...]
redeem history inspect --at <timestamp>

redeem restore tui
redeem restore apply --at <timestamp> [--yes]

redeem prune run
redeem doctor
```

Global flags:
- `--config <path>`
- `--state-dir <path>`
- `--host <name>`
- `--profile <name>`
- `--log-level <debug|info|warn|error>`

## 11) Config Model

Config file format: YAML (JSON/TOML support optional).

Example:

```yaml
stateDir: "~/.terminal-redeemer"
host: "auto"
profile: "default"

capture:
  enabled: true
  interval: "60s"
  snapshotEvery: 100

retention:
  days: 30

processMetadata:
  whitelist:
    - opencode
    - claude
  whitelistExtra: []
  includeSessionTag: true

restore:
  interactive: true
  additiveOnly: true
  terminal:
    command: "kitty"
    zellijAttachOrCreate: true
  appAllowlist: {}
```

No personal defaults:
- `appAllowlist` is empty by default.
- user-specific mappings only from config/module options.

## 12) Home Manager Module

Module output:
- `homeManagerModules.terminal-redeemer`

Core options:
- `programs.terminal-redeemer.enable`
- `programs.terminal-redeemer.package`
- `programs.terminal-redeemer.stateDir`
- `programs.terminal-redeemer.profile`
- `programs.terminal-redeemer.capture.enable`
- `programs.terminal-redeemer.capture.interval`
- `programs.terminal-redeemer.capture.snapshotEvery`
- `programs.terminal-redeemer.retention.days`
- `programs.terminal-redeemer.processWhitelist`
- `programs.terminal-redeemer.processWhitelistExtra`
- `programs.terminal-redeemer.restore.appAllowlist`
- `programs.terminal-redeemer.terminal.command`
- `programs.terminal-redeemer.extraConfig`

Generated artifacts:
- config file under XDG config.
- systemd user service/timer for capture only by default:
  - `terminal-redeemer-capture.service`
  - `terminal-redeemer-capture.timer`

Not enabled by default:
- auto-restore service.

## 13) Flake Outputs

- `packages.<system>.terminal-redeemer`
- `apps.<system>.redeem`
- `homeManagerModules.terminal-redeemer`

## 14) Security & Privacy

- No full argv/env dump by default.
- Capture only whitelisted process names + pid + session tag.
- Session tag best effort and may be redacted/truncated if configured.
- File permissions:
  - state/config `0600` where applicable.
- No command execution from event log directly.
- Non-terminal restore restricted by explicit allowlist map.

## 15) Observability

Logs:
- structured logs with component and window key context.
- summary after each capture write and restore run.

Doctor command checks:
- niri IPC reachable.
- state dir writable.
- zellij/kitty presence.
- config validity.
- event/snapshot integrity.

## 16) Failure Modes & Mitigations

- Corrupt JSONL line:
  - skip with warning; continue replay when possible.
- Snapshot mismatch/hash failure:
  - fallback to earlier snapshot/full replay.
- Missing app mapping:
  - mark skipped in plan.
- Missing terminal metadata:
  - skip terminal by policy.
- Concurrent writer:
  - lock file and single-writer guarantee.

## 17) Performance Targets (Initial)

- Capture cycle p95 < 150ms under typical window counts (<100).
- Restore plan generation p95 < 300ms for 30-day history with snapshots.
- Replay worst-case bounded by snapshot cadence.

## 18) Test Strategy

Unit:
- sparse patch generation.
- replay correctness at arbitrary timestamp.
- retention pruning.
- process whitelist/session tag parsing.

Integration:
- fixture-based niri JSON inputs.
- event corruption handling.
- restore plan generation with missing metadata.
- allowlist behavior.

E2E (manual/CI):
- `redeem capture run` writes events on change only.
- `redeem restore apply` additive behavior.
- HM module creates timer/service correctly.

## 19) Rollout Plan

Phase 1:
- scaffold + flake + HM module skeleton + config.
Phase 2:
- capture/diff/events/snapshots/prune.
Phase 3:
- replay + CLI restore apply.
Phase 4:
- Bubble Tea restore TUI.
Phase 5:
- hardening + docs + migration and operations notes.

## 20) V2 Design Direction (Bottles)

- Mutable named workspaces/sessions built as named pointers over timeline.
- Commands:
  - `redeem bottle create`
  - `redeem bottle use`
  - `redeem bottle update`
  - `redeem bottle open`
- Reuse event/replay core with profile-like logical namespaces.

## 21) Open Questions (for implementation kickoff)

1. Preferred config file format priority (YAML only vs multi-format)?
2. Exact strategy for terminal window key derivation in edge cases.
3. Session tag extraction precedence (args vs title vs env).
4. Whether to support optional event-driven capture as experimental flag in late V1.
