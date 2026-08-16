---
title: Add a pinned manual leech mirror snapshot and apply workflow
priority: high
frontloop_approval_task: 1d9ea47a749f8fb696680b912ac3307d196af535948c907aad61612a6535ecfe-3
---

## Goal

Let the user manually save the currently open Redeem-owned Lattice projections on Overton into one pinned snapshot and manually reapply that same subset after connection loss or restart.

## Acceptance Criteria

- A manual save command records only exactly verified Redeem-owned remote attachments, including exact source host/session, remote CWD, destination workspace selector, captured order, floating/tiled state, and available size data.
- There is one replaceable pinned snapshot per source host/profile; later rolling captures, same-boot disconnects, and boot changes do not overwrite or invalidate it.
- Manual apply preflights the source's active exact Zellij catalog, reports missing sessions without creating them, skips already-open exact attachments, and launches the rest attach-only in captured order.
- Applied windows are correlated by exact launcher PID/Niri window evidence, moved to their captured destination workspace, and have floating/tiled sizes restored where Niri supports exact-ID actions.
- Apply intentionally does not reconstruct exact column/stack relations or perform focus-based reorder choreography; its result reports any degraded workspace/size action.
- Save/apply is idempotent and never closes, moves, or resizes ordinary Kitty, another host's mirrors, untracked legacy windows, or unrelated Redeem windows.
- Store corruption, permissions, atomic replacement, hostile values, missing sessions, partial launch failure, duplicate apply, multi-workspace order, and NOOP apply all have focused regression coverage.

## Design Decisions

- Use a separate pinned manual artifact, not rolling boot checkpoints or historical replay.
- Ship one snapshot slot per source host/profile; named snapshot libraries are YAGNI.
- Restore workspace, captured/source order, and sizes, but not exact columns/stacks.
- Manual apply is attach-only and non-destructive outside positively verified ownership.

## Implementation Notes

Suggested CLI surface is the smallest clear pair, such as `redeem mirror save --host lattice` and `redeem mirror apply --host lattice`; final spelling may follow existing CLI conventions. Reuse placement types/actions without teaching generic local resume to launch remote commands.


## Blocked

Superseded before implementation by Claude Opus 5 review. Revised task will absorb the minimal live projection inventory, use exact session/process evidence rather than launcher PID correlation, explicitly store outside checkpoints/prune scope, and move each newly applied window once by verified Niri ID.
