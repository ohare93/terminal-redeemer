---
title: Plan all-session recovery for same-boot failure and reboot
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-3
---

## Goal

Add the `redeem resume --all` planning contract that selects the right sessions and placements from current evidence and recovery points instead of globally requiring one fresh prior-boot terminal snapshot.

## Acceptance Criteria

- For a same-boot Niri failure, `resume --all` targets every currently active exact Zellij session, using current-boot sticky placement when available and a documented degraded fallback when not.
- After a boot change, `resume --all` considers only sessions recorded active in the newest eligible prior recovery point and currently classified as active or dead-resurrectable.
- Unrelated historical dead-resurrectable sessions are never selected, even when the Zellij cache contains hundreds of them.
- Current active sessions are not blocked solely because their placement is old; placement age is reported as a warning or degradation.
- `resume.maxCheckpointAge` continues to prevent automatic resurrection from an excessively old prior recovery point, but does not block attachment of a currently active session.
- Reuse `zellijlive.Catalog` exact statuses and safety checks, including pinned-version, owned socket, duplicate, missing, socket-invalid, and dead-resurrectable classification. Reuse mirror pin/apply’s fail-closed active-session preflight pattern where useful; mirror pins are never the recovery allow-list.
- Preserve the existing direct attach-only contract: `KittyLaunchSpec` remains `zellij attach -- <session>` with no shell, create, attach-or-create, or force-run flags.
- Planner output retains exactly observed open-window identity, including Niri window ID, for later reconciliation. This supersedes the executor’s current behavior that marks a matching session `already_open` without relocation.
- Dry-run output explains candidate source, Zellij status, placement source and age, target workspace and column, and every exclusion or degradation.
- Existing plain `redeem resume` behavior remains available for restoring only the previously visible recovery set; `--all` is the explicit all-active or prior-active mode used by automatic recovery.
- Planner and CLI tests cover same boot, reboot, mixed active/resurrectable/missing catalogs, stale placement, stale prior recovery point, active sessions without placement, duplicates, and idempotent reruns.
- `go test ./internal/resume ./cmd/redeem` passes.

## Design Decisions

- Use the authoritative `zellijlive.Catalog` statuses instead of parsing `zellij list-sessions --short` as an undifferentiated availability list.
- Never resurrect every cache entry; prior active inventory is the allow-list across boot.
- Keep `--all` explicit and preserve the narrower plain-resume workflow.
- Normal Zellij resurrection is allowed for an eligible prior-active session; attach-or-create and `--force-run-commands` remain forbidden.

## Implementation Notes

Depends on task 2. Extend the existing planner and CLI rather than introducing a parallel resume pipeline. `--all` changes candidate selection and explanation, then feeds the existing plan/executor types. Preserve plain resume behavior and the existing max-checkpoint-age option, changing only its documented treatment of currently active sessions.


## Completion Summary

- Added `redeem resume --all` planning through the existing resume pipeline for same-boot active sessions and reboot-time prior-active allow-listed sessions.
- Used authoritative Zellij catalog states to exclude unrelated historical, missing, duplicate, prefix-only, and socket-invalid sessions while permitting bounded eligible resurrection.
- Retained exact open-window identity, degraded missing/old placement per session, and detailed dry-run source/status/age/target reporting.
- Added per-launch catalog revalidation to close resurrection TOCTOU, rejected unaged placement, and preserved plain resume plus direct attach-only safety; tests and independent review passed.

### Files Changed

- cmd/redeem/main.go
- cmd/redeem/main_test.go
- internal/resume/planner.go
- internal/resume/planner_all.go
- internal/resume/planner_all_test.go
- internal/resume/executor.go
- internal/resume/executor_test.go
