---
title: Persist active recovery inventory with sticky last-known placement
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-2
---

## Goal

Evolve rolling boot checkpoints so each recovery point distinguishes the current active Zellij inventory from the current Niri window observation and carries forward the last verified placement of active-but-headless sessions.

## Acceptance Criteria

- A new checkpoint schema records the exact active Zellij session set and per-session recovery metadata: session name, CWD, workspace reference, column, row, floating state, dimensions, placement observation time, and whether the session was visible in that capture.
- Capture updates placement only from an exact visible session/window association; an active headless session retains its newest valid prior placement instead of losing it.
- A successful capture with zero Kitty windows but still-active Zellij sessions preserves those sessions and their previous placements.
- The first capture of a new boot can carry placement forward from the newest valid matching prior checkpoint, while active inventory remains specific to the new observation.
- Dead-resurrectable cache entries are not recorded as currently active; an unavailable, ambiguous, or failed active-session catalog prevents publication and leaves the prior checkpoint usable.
- Checkpoint integrity covers every recovery-relevant field and publication keeps the existing writer lock, temp-write, file fsync, atomic rename, and directory fsync guarantees.
- Schema-1 and schema-2 checkpoints remain readable and can supply legacy visible placement where available; new writes use the new schema without importing the unbounded legacy event log.
- Tests cover same-boot headless carry-forward, new-boot carry-forward, empty Niri state, active-session removal, catalog failure, legacy reads, tampering, concurrent capture, and prune retention.
- `go test ./internal/model ./internal/checkpoints ./internal/capture ./internal/prune` passes.

## Design Decisions

- Keep one rolling checkpoint per boot/host/profile; enrich its recovery payload rather than restoring an append-only event timeline.
- Treat absence from Niri as detachment, not deletion of placement.
- Drop a session from the current active inventory only after a complete authoritative Zellij catalog says it is not active; retained prior checkpoints remain the reboot recovery source.
- Persist the second `pos_in_scrolling_layout` component now, but reconstruction of stacked multi-window columns may remain explicitly degraded until a real use case requires it.

## Implementation Notes

Depends on exact identity from task 1. Relevant files: `internal/model/state.go`, `internal/checkpoints/store.go`, `internal/capture/runner.go`, `internal/niri/adapter.go`, store/capture/prune tests, and a new ADR superseding the replacement-only assumptions in `docs/adr/0001-resume-zellij-terminals-in-niri.md`.
