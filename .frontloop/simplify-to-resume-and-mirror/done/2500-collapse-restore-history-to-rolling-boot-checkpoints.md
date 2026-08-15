---
title: Collapse restore history to rolling boot checkpoints
priority: high
frontloop_approval_task: bb1f5140ea3f97ba3060ab67219d59f37ae4035c722e0e1536b1f3d88d757f10-3
---

## Goal

Keep the safe reboot-resume product while removing timestamped snapshot, replay-at-time, general restore, and restore-TUI paths that are not required to restore terminal placement after restart.

## Acceptance Criteria

- Periodic capture atomically maintains the minimal rolling checkpoint state needed for prior-boot resume
- Resume remains boot-aware, exact-session attach-only, idempotent, and supports dry-run
- Checkpoint lock and temp-write/fsync/rename/directory-fsync guarantees remain covered by tests
- Timestamped snapshots, replay-at-time, general restore/app allowlist, restore TUI, and their unused configuration/CLI/docs/tests are removed
- Pruning still bounds retained boot checkpoints
- Crash/power-loss tests demonstrate that a valid prior checkpoint remains usable after an interrupted publish
- `go test ./...`, package build, and `nix flake check 'path:.' --no-build` pass

## Design Decisions

- The product restores terminals, not arbitrary GUI applications
- Keep one rolling checkpoint per boot rather than a user-visible historical timeline
- Do not weaken resume correlation or exact Zellij identity checks

## Implementation Notes

Relevant paths: internal/capture, checkpoints, resume, prune, events, replay, snapshots, restore, tui, config, cmd/redeem/main.go. Reuse safefile/storelock primitives; prefer direct checkpoint reads over a replay abstraction.


## Completion Summary

- Replaced event/snapshot/replay history with direct atomic rolling checkpoint capture and direct prior-boot resume reads
- Removed general restore, arbitrary app allowlists, restore TUI, history CLI/config/docs, and unused packages
- Preserved boot-aware exact attach-only resume, dry-run, PID/window correlation, idempotence, and placement verification
- Serialized observation through publication under the writer lock to prevent stale concurrent overwrite
- Made prune preserve current checkpoints and newest usable prior checkpoint per host/profile while bounding older checkpoints
- Kept temp-write, 0600 permissions, file fsync, rename, directory fsync, and interrupted-publish preservation with focused tests
- Passed full Go tests/vet/race checks, package/module builds, and Nix flake evaluation

### Files Changed

- cmd/redeem/main.go
- cmd/redeem/main_test.go
- internal/capture/runner.go
- internal/capture/runner_test.go
- internal/checkpoints/store.go
- internal/checkpoints/store_test.go
- internal/prune/prune.go
- internal/prune/prune_test.go
- internal/resume/planner.go
- internal/resume/planner_test.go
- internal/config/config.go
- internal/config/config_test.go
- internal/doctor/checks.go
- internal/doctor/checks_test.go
- internal/model/state.go
- modules/home-manager/terminal-redeemer.nix
- modules/nixos/terminal-redeemer.nix
- README.md
- docs/CONFIG.md
- docs/OPERATIONS.md
- docs/adr/0001-resume-zellij-terminals-in-niri.md
- flake.nix
- internal/diff/ (deleted)
- internal/events/ (deleted)
- internal/replay/ (deleted)
- internal/restore/ (deleted)
- internal/safefile/ (deleted)
- internal/snapshots/ (deleted)
- internal/tui/ (deleted)
