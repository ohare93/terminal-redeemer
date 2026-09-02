---
title: Reconcile existing windows and restore workspace and column order
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-4
---

## Goal

Refactor execution into a verified reconciliation pass that launches only missing sessions, moves both existing and new windows to saved workspaces, and restores deterministic left-to-right terminal column order.

## Acceptance Criteria

- The executor first maps every already-open session to exactly one verified Niri window; duplicate or ambiguous attachments fail safely without launching or moving another window.
- Preserve rather than reimplement the current sequential launch path: direct Kitty argv, exact launched-PID correlation, two stable exact descendant-attachment observations, and leave-attached-open behavior on optional placement failure.
- Both existing and newly launched windows use the existing exact-window-ID workspace action. Retain `--focus false` and current name/index movement; add output-plus-index fallback only in the workspace resolver, not as a second mover implementation.
- All target windows are available before a second ordering phase runs, and terminal columns are processed deterministically by workspace, captured column, and session-name tie-breaker.
- Replace the current deliberate column-order-unsupported result with a second all-windows-ready ordering phase. Focus and verify the exact window before and after `move-column-to-index`, and stop or degrade only the affected workspace on failure.
- The previously focused window is restored best-effort after ordering; an order failure leaves successfully attached windows open and never causes duplicate relaunch on rerun.
- Retain the existing independent floating and size actions and their degradation reporting. Detect captured stacked rows before ordering and report unsupported rather than guessing with consume or expel actions.
- The entire mutating resume operation shares the repository operation lock with capture so a timer cannot publish intermediate launch or ordering state.
- Executor and runtime tests cover full empty-workspace restore, partial restore, already-open relocation, concurrent unrelated windows, duplicate attachment, focus/order verification failure, lock contention, and rerun idempotency.
- `go test ./internal/resume ./internal/storelock ./cmd/redeem` passes.

## Design Decisions

- Preserve exact PID/window/attachment correlation and sequential launch; no app-ID or creation-order correlation.
- Restore absolute captured Niri columns for the current one-window-per-column workflow, with explicit degradation for stacked layouts.
- Use Niri's focus-only ordering actions only with exact focus and post-action observation, and restore prior focus afterward.
- Keep attached terminals alive when optional layout work fails.

## Implementation Notes

Depends on task 3. Refactor around the existing executor launch/correlation loop, `SnapshotObserver`, `ProcAttachmentProbe`, and `NiriActions`; do not replace them. The actual delta is existing-window reconciliation, two-phase execution, verified ordering and focus restoration, and operation-lock coverage.


## Completion Summary

- Refactored resume into exact preflight reconciliation, sequential missing-session launch/relocation, and a second verified per-workspace column-ordering phase.
- Added complete workspace transition checks, descending absolute ordering with final all-target verification, affected-workspace degradation, and best-effort focus restoration.
- Closed duplicate-attachment and focus/order races while preserving exact PID correlation, stable attachment checks, attach-only launches, and independent floating/size handling.
- Persisted conservative stacked-column evidence through sticky headless/new-boot captures and rejected partial layout evidence; shared capture/resume operation locking and all required tests passed independent review.

### Files Changed

- cmd/redeem/main.go
- cmd/redeem/main_test.go
- internal/model/state.go
- internal/niri/adapter.go
- internal/capture/runner.go
- internal/capture/runner_test.go
- internal/checkpoints/store.go
- internal/checkpoints/store_test.go
- internal/resume/executor.go
- internal/resume/executor_test.go
- internal/resume/planner.go
- internal/resume/planner_test.go
- internal/resume/planner_all.go
- internal/resume/planner_all_test.go
- internal/resume/runtime.go
- internal/resume/runtime_test.go
