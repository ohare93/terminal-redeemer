---
title: Accept and publish coherent zero-output Niri inventory
priority: high
---

## Goal

Treat a coherent zero-output Niri replay as complete source authority and publish existing Kitty/Zellij window sources without inventing output geometry.

## Acceptance Criteria

- A Lattice-shaped fixture with `outputs: {}`, null workspace outputs, and valid window/workspace/layout references reproduces the current failure before the change and passes afterward.
- Niri validation accepts either one complete active output or a coherent zero-output state, while partial joins, dangling references, malformed layout, and unsupported multi-output topology remain degraded.
- Inventory schema 2 makes source output geometry optional while retaining required source, session, workspace, layout mode, window dimensions, and tiled order fields.
- The source builder publishes eligible open Kitty windows backed by exact live Zellij sessions when output geometry is absent.
- Complete headless observations advance revision and live-session evidence normally; no arbitrary windowless Zellij session becomes a source.
- Focused protocol, inventory, fuzz, and bounded-codec tests pass.

## Design Decisions

- Bump inventory schema from 1 to 2 rather than silently changing schema-1 required-field semantics.
- Keep RPC schema 1 and controller state schema 2.
- Represent only output geometry as optional; do not add a generic capability framework or synthetic geometry.
- Preserve the exact open-Kitty plus live-Zellij source boundary.

## Implementation Notes

Primary paths: `internal/niriipc`, `internal/sourceinventory`, `internal/sliceprotocol`, and their fixtures/tests. The observed Lattice state has no outputs and all workspace `output` fields null, but retains workspace IDs, names, indexes, and window layout data.
