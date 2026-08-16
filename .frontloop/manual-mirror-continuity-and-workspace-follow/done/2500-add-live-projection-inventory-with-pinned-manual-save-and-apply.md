---
title: Add live projection inventory with pinned manual save and apply
priority: high
frontloop_approval_task: 8131f8be65f97e7fee6decd7c97db1f7d29347b3c33d545062d159efc8657b88-2
---

## Goal

Use fresh Niri/PID/SSH/Zellij evidence—not a persistent attachment registry—to identify exact Overton projections, then let the user save one pinned subset and manually reapply it after same-boot connection loss or restart.

## Acceptance Criteria

- `mirror.OwnedWindow` retains the Niri PID, and a bounded injectable live-evidence helper walks only owned-window descendants to identify the exact SSH destination and exact case-sensitive Zellij attach session from Redeem's deterministic argv shape; substring matches, title-only matches, ambiguous evidence, and app-ID-only matches fail closed.
- New mirror titles include the exact session as an immutable presentation/index value, but live process evidence remains required before a window is considered an exact projection; pre-upgrade windows are evaluated by the same evidence and are reported untracked when exact extraction is impossible.
- A manual save command joins exact local projections to a fresh source snapshot by exact session and writes one replaceable pinned artifact per source host/profile under a dedicated mirror state path outside `checkpoints/`, so rolling capture and `prune run` never touch it.
- The pinned artifact contains only bounded typed source host/session, remote CWD, destination workspace selector, captured order, floating/tiled state, and available size data; it contains no arbitrary executable/shell payload and is stored mode 0600 with lock, temp file, file fsync, rename, and directory fsync.
- Manual apply validates the pinned artifact, preflights the exact active remote Zellij catalog, reports missing sessions without creating them, skips sessions already projected by exact live evidence, and launches the rest attach-only in captured order.
- Each newly applied window is found from its unique exact-session process evidence, moved exactly once by Niri window ID to the captured destination workspace, and receives supported floating/tiled size actions; no launcher-PID equality is assumed because Kitty remains detached.
- Apply intentionally does not reconstruct exact column/stack relations or perform focus-based reorder choreography; degraded workspace/size actions are reported.
- Save/apply is idempotent and never closes, moves, or resizes ordinary Kitty, another host's projections, ambiguous/untracked windows, picker windows, or unrelated Redeem windows.
- Focused tests cover deterministic SSH evidence extraction, hostile/leading-dash/case-distinct sessions, fake `/proc`, ambiguous descendants, immutable title formatting, pre-upgrade windows, atomic corruption/permissions, prune isolation, missing sessions, partial launches, duplicate apply, multi-workspace order, and no-op apply.

## Design Decisions

- Do not create a durable attachment registry; fresh process evidence eliminates staleness, PID-reuse, adoption, and pruning machinery.
- Exact session is the projection identity for this bounded terminal-only workflow.
- Use one pinned snapshot slot per source host/profile; named snapshot libraries are YAGNI.
- Store the pin outside rolling checkpoints and history.
- Restore workspace, captured/source order, and sizes, but not exact columns/stacks.
- Manual apply is attach-only and non-destructive outside positively verified ownership.

## Implementation Notes

Fold the minimal projection helper into this task. Reuse `/proc` traversal seams from `internal/resume/runtime.go`, source metadata from `internal/mirror/snapshot.go`, launch planning from `internal/mirror/windows.go`, Niri actions from `internal/resume/runtime.go`, and atomic publication patterns from `internal/checkpoints/store.go`. A small shared atomic-write helper is acceptable if it reduces duplication.


## Completion Summary

- Added fresh PID/SSH/Zellij process-evidence inventory that fails closed on deceptive, ambiguous, title-only, and untracked windows.
- Added one replaceable typed manual mirror pin per source host/profile outside checkpoint/prune scope with descriptor-relative no-follow locking and atomic durable 0600 publication.
- Added fresh-only `mirror save` and idempotent attach-only `mirror apply` with ACTIVE preflight, missing-session reporting, captured-order launch, detached token correlation, exact-ID one-time workspace/layout actions, and structured degradation.
- Added strict shared SSH option grammar, transport-token identity, leading-dash session support, immutable session titles, and hostile/concurrency/durability regression coverage.
- Passed independent correctness/Ponytail reviews, full tests and race tests, vet, Nix evaluation, repeated hostile tests, and final focused approval.

### Files Changed

- README.md
- cmd/redeem/apply_output_test.go
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- docs/CONFIG.md
- docs/OPERATIONS.md
- internal/mirror/dual_new.go
- internal/mirror/new.go
- internal/mirror/orchestration_test.go
- internal/mirror/pin.go
- internal/mirror/pin_apply_hardening_test.go
- internal/mirror/pin_hardening_test.go
- internal/mirror/pin_test.go
- internal/mirror/pin_workflow.go
- internal/mirror/projection.go
- internal/mirror/projection_test.go
- internal/mirror/remote.go
- internal/mirror/ssh_options.go
- internal/mirror/ssh_options_test.go
- internal/mirror/windows.go
- internal/resume/runtime.go
- internal/resume/runtime_test.go
