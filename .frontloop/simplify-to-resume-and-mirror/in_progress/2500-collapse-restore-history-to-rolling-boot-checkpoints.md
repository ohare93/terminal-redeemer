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
