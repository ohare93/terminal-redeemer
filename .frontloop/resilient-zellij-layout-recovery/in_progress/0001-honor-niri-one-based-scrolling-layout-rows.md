---
title: Honor Niri one-based scrolling-layout rows
priority: critical
---

## Goal

Correct stack detection and persisted occupancy validation to use Niri's documented one-based row coordinates.

## Acceptance Criteria

- A normal one-window column at row 1 is eligible for column ordering and is never classified as stacked solely because its row is nonzero.
- Rows greater than 1 and multiple captured windows occupying the same column remain conservatively classified as stacked/unsupported.
- Persisted `CapturedColumnOccupied` evidence is valid only for a complete row-1 target placement and survives sticky carry-forward with integrity protection.
- Tests use realistic one-based Niri row/column fixtures and cover normal row 1, stacked row 2, same-column occupancy, checkpoint validation, capture, planner, runtime, and executor behavior.
- `go test ./...`, `go vet ./...`, and `nix flake check` pass; no live resume or compositor mutation occurs.

## Design Decisions

- Use Niri's native one-based coordinates end-to-end; do not normalize them to zero-based values.
- Change only row-base assumptions; preserve existing absolute one-based column ordering.

## Implementation Notes

Live `niri msg` and `move-column-to-index --help` confirm scrolling-layout coordinates are one-based. Current code incorrectly checks row 0 in capture/planner/checkpoint validation and treats row >0 as stacked in executor.
