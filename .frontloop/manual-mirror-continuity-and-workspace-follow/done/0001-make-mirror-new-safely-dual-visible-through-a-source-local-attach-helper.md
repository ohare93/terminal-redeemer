---
title: Make mirror new safely dual-visible through a source-local attach helper
priority: critical
frontloop_approval_task: 8131f8be65f97e7fee6decd7c97db1f7d29347b3c33d545062d159efc8657b88-1
---

## Goal

Keep Overton as the sole creator of a generated persistent Lattice Zellij session, then always make a best-effort request to a source-local Redeem helper that launches an attach-only Lattice Kitty and optionally places it in an explicitly supplied source workspace.

## Acceptance Criteria

- `mirror new` still creates exactly one generated `redeem-<32 hex>` session through the Overton Kitty's `zellij attach --create`; no other path receives `--create`.
- After the exact generated session is observed active on Lattice, Overton invokes a source-local `redeem mirror attach-local`-style helper using the existing validated SSH destination and deterministic shell quoting; the helper accepts only a validated exact session and optional validated Niri workspace name/number.
- The source-local helper recovers Lattice's graphical environment from its user manager, launches a detached ordinary Kitty with attach-only Zellij argv and detach-on-close semantics, then identifies its exact session-bearing Niri window from live PID/process evidence and moves that window once with `--window-id` and `--focus false` when a workspace was supplied.
- The detached source Kitty outlives the short SSH helper invocation and connection; closing either Kitty detaches, while exiting the Zellij session naturally closes both clients.
- `--source-workspace` is optional for backward-compatible CLI/module contracts: omission still attempts a source Kitty with normal Niri placement, while mono/nix may explicitly pass `agentleman`. Redeem itself contains no hardcoded workspace.
- No connected-monitor/output check is made. Source readiness timeout, absent graphical environment, launch/correlation/placement failure, or unavailable older source helper is reported as a tolerated partial failure and never closes the Overton viewer, kills the session, retries creation, or duplicates the source Kitty.
- Planner, injected-runner, fake-proc/Niri, and dry-run tests prove exact command ordering, session/workspace validation, hostile argv safety, daemonized lifetime boundary, zero-output behavior, partial failures, optional selector compatibility, and no duplicate source attach.

## Design Decisions

- This task is independent and ships first.
- Source-side graphical launch and Niri placement belong in a source-local Redeem subcommand, not a long-lived remote shell.
- Use detached Kitty plus exact live process evidence after launch; do not require the detached launcher PID to equal the Niri window PID.
- Always attempt dual visibility; source placement is optional and best effort.
- Do not add a Niri rule, monitor gate, daemon, timer, or hardcoded workspace.

## Implementation Notes

Current behavior is in `internal/mirror/windows.go` and `cmd/redeem/main.go`. Reuse `envWithGraphicalSession`, `NiriActions.MoveToWorkspace`, safe `QuoteCommand`, and injectable `/proc` readers. Keep the current byte-compatible no-workspace CLI valid; later integration updates exact Nix assertions in root `flake.nix`, mono/nix feature integration, and the Lattice/Overton contract.


## Completion Summary

- Kept Overton as the sole generated-session creator and added one bounded best-effort source-helper invocation.
- Added source-local ACTIVE-session readiness, per-session interprocess duplicate suppression, detached attach-only Kitty launch, exact process correlation, and one-time Niri placement.
- Added hard subprocess/process-tree cancellation bounds and stable regression coverage.
- Updated mirror-new documentation for dual-visible partial-success and version-skew behavior.
- Passed independent correctness/Ponytail reviews plus full tests, race tests, vet, Nix evaluation, and targeted timeout stress.

### Files Changed

- README.md
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- docs/CONFIG.md
- docs/OPERATIONS.md
- internal/mirror/dual_new.go
- internal/mirror/dual_new_test.go
- internal/mirror/local_attach.go
- internal/mirror/orchestration_test.go
- internal/mirror/runtime.go
- internal/mirror/snapshot.go
- internal/mirror/windows.go
- internal/procmeta/process_tree.go
- internal/procmeta/process_tree_test.go
- internal/procrun/command.go
- internal/procrun/command_test.go
- internal/resume/executor.go
- internal/resume/runtime.go
- internal/zellijlive/catalog.go
- internal/zellijlive/zellijlive_test.go
