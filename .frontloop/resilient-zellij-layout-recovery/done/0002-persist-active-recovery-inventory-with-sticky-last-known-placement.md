---
title: Persist active recovery inventory with sticky last-known placement
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-2
---

## Goal

Evolve rolling boot checkpoints so each recovery point distinguishes the current active Zellij inventory from the current Niri window observation and carries forward the last verified placement of active-but-headless sessions.

## Acceptance Criteria

- A new checkpoint schema records an authoritative active-session allow-list plus per-session recovery metadata: name, CWD, workspace reference, column, row, floating state, dimensions, placement observation time, and visibility in the capture. It reuses the active-only `zellijlive.Cataloger` semantics instead of independently parsing `list-sessions`.
- Capture updates placement only from an exact visible session/window association; an active headless session retains its newest valid prior placement instead of losing it.
- A successful capture with zero Kitty windows but still-active Zellij sessions preserves those sessions and their previous placements.
- The first capture of a new boot can carry placement forward from the newest valid matching prior checkpoint, while active inventory remains specific to the new observation.
- Dead-resurrectable cache entries are never marked active. Catalog ambiguity, socket-invalid entries, duplicate names, or catalog failure prevents checkpoint publication and leaves the prior checkpoint usable. Mirror’s existing `ActiveSessions` and headless-window representation may inform conversion tests, but mirror snapshots and pins are not checkpoint storage.
- Checkpoint integrity covers every recovery-relevant field and publication keeps the existing writer lock, temp-write, file fsync, atomic rename, and directory fsync guarantees.
- Schema-1 and schema-2 checkpoints remain readable and can supply legacy visible placement where available; new writes use the new schema without importing the unbounded legacy event log.
- Tests cover same-boot headless carry-forward, new-boot carry-forward, empty Niri state, active-session removal, catalog failure and ambiguity, legacy reads, tampering, concurrent capture, and prune retention.
- `go test ./internal/model ./internal/checkpoints ./internal/capture ./internal/prune` passes.

## Design Decisions

- Keep one rolling checkpoint per boot/host/profile; enrich its recovery payload rather than restoring an append-only event timeline.
- Treat absence from Niri as detachment, not deletion of placement.
- Drop a session from the current active inventory only after a complete authoritative Zellij catalog says it is not active; retained prior checkpoints remain the reboot recovery source.
- Persist the second `pos_in_scrolling_layout` component now, but reconstruction of stacked multi-window columns may remain explicitly degraded until a real use case requires it.

## Implementation Notes

Depends on task 1. The new work belongs in checkpoint/model/capture persistence and sticky merge logic. Reuse or extract the active-catalog projection from `internal/mirror/snapshot.go`; do not build a second Zellij command workflow. Preserve current mirror pin/apply schemas and behavior. Add the recovery ADR only for checkpoint/recovery semantics.


## Completion Summary

- Added checkpoint schema v3 with authoritative active-session inventory and integrity-protected per-session recovery metadata.
- Preserved last verified workspace, column, row, sizes, floating state, CWD, and placement time for active headless sessions across same-boot and new-boot captures.
- Restricted placement refresh to exact unambiguous process evidence and safely merged partial workspace/layout observations.
- Kept v1/v2 reads, rolling checkpoint durability, prune behavior, and fail-closed catalog publication; focused/full tests and independent review passed.

### Files Changed

- internal/model/state.go
- internal/niri/adapter.go
- internal/niri/adapter_test.go
- internal/procmeta/enricher.go
- internal/procmeta/enricher_test.go
- internal/capture/runner.go
- internal/capture/runner_test.go
- internal/checkpoints/store.go
- internal/checkpoints/store_test.go
- internal/prune/prune_test.go
- cmd/redeem/main_test.go
- docs/adr/0003-sticky-active-recovery-inventory.md
