---
title: Reconcile headless sources with available layout authority
priority: high
---

## Goal

Allow the controller and manager to select and attach headless Lattice sources, applying only the workspace and layout authority that remains observable.

## Acceptance Criteria

- A complete zero-output inventory populates controller sources and appears in the existing Slice Manager instead of showing `niri_missing_output` with no sources.
- Workspace selection, exact pickup, all-eligible selection, projection launch, and exact live-only attachment work without host output geometry.
- Existing `(column,tile)` data continues to determine initial projection order.
- Named workspace and tiled/floating reconciliation remain available when safely observable on the host and leech.
- Width and height reconciliation emits no action while host output geometry is absent and never reuses stale cached geometry.
- When the host output returns, the next complete revision resumes ordinary proportional sizing without changing source/session ownership.
- Output loss or restoration never closes host work, duplicates a projection, mutates an unrelated window, or changes focus.

## Design Decisions

- Reuse the existing controller, manager, ownership proof, and spatial planner rather than introduce a parallel headless controller mode.
- Skip only output-dependent sizing; keep workspace, layout mode, and initial order.
- Do not add TUI redesign unless the existing source presentation is genuinely ambiguous after inventory becomes complete.

## Implementation Notes

Primary paths: `internal/slicelayout`, `internal/slicecontroller`, `cmd/redeem/main.go`, and focused controller/TUI tests. Keep spatial effects exact-ID, leech-targeted, non-focus, and verify-after-write.
