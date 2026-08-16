---
title: Reconcile existing windows and restore workspace and column order
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-4
---

## Goal

Refactor execution into a verified reconciliation pass that launches only missing sessions, moves both existing and new windows to saved workspaces, and restores deterministic left-to-right terminal column order.

## Acceptance Criteria

- The executor first maps every already-open session to exactly one verified Niri window; duplicate or ambiguous attachments fail safely without launching or moving another window.
- Missing sessions are launched sequentially with direct Kitty argv, exact PID correlation, and stable descendant attachment evidence as today.
- Both existing and newly launched windows are moved by exact window ID using workspace name, then output plus index, then index fallback; workspace moves use `--focus false` where Niri supports it.
- All target windows are available before a second ordering phase runs, and terminal columns are processed deterministically by workspace, captured column, and session-name tie-breaker.
- Column movement focuses the exact window ID, invokes Niri `move-column-to-index`, observes the same window at the requested column, and stops/degrades that workspace if verification fails.
- The previously focused window is restored best-effort after ordering; an order failure leaves successfully attached windows open and never causes duplicate relaunch on rerun.
- Floating and size restoration still run independently and report degradation without falsifying successful attachment/workspace placement.
- Captured stacked-row values other than the supported one-window-per-column case are preserved and reported as unsupported rather than guessed with consume/expel heuristics.
- The entire mutating resume operation shares the repository operation lock with capture so a timer cannot publish intermediate launch/order state.
- Executor/runtime tests cover full empty-workspace restore, partial restore, already-open relocation, concurrent unrelated windows, duplicate attachment, focus/order verification failure, lock contention, and rerun idempotency.
- `go test ./internal/resume ./internal/storelock ./cmd/redeem` passes.

## Design Decisions

- Preserve exact PID/window/attachment correlation and sequential launch; no app-ID or creation-order correlation.
- Restore absolute captured Niri columns for the current one-window-per-column workflow, with explicit degradation for stacked layouts.
- Use Niri's focus-only ordering actions only with exact focus and post-action observation, and restore prior focus afterward.
- Keep attached terminals alive when optional layout work fails.

## Implementation Notes

Depends on task 3. Relevant files: `internal/resume/executor.go`, `internal/resume/runtime.go`, planner item identity, Niri adapter/model placement, and tests. Current Niri exposes `focus-window --id` and `move-column-to-index` but no window-ID form of the move action, so verification is mandatory.
