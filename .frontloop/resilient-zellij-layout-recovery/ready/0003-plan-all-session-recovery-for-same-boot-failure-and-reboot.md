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
- Current active sessions are not blocked solely because their placement is old; placement age is reported as a warning/degradation.
- `resume.maxCheckpointAge` continues to prevent automatic resurrection from an excessively old prior recovery point, but does not block attachment of a currently active session.
- Missing, prefix-only, duplicate, or socket-invalid catalog entries are never launched or recreated, and no path adds Zellij create or force-run flags.
- Already-open exact sessions remain actionable reconciliation items carrying their current Niri window ID rather than being discarded as complete.
- Dry-run output explains candidate source, Zellij status, placement source/age, target workspace and column, and every exclusion or degradation.
- Existing plain `redeem resume` behavior remains available for restoring only the previously visible recovery set; `--all` is the explicit all-active/prior-active mode used by automatic recovery.
- Planner and CLI tests cover same boot, reboot, mixed active/resurrectable/missing catalogs, stale placement, stale prior recovery point, active sessions without placement, duplicates, and idempotent reruns.
- `go test ./internal/resume ./cmd/redeem` passes.

## Design Decisions

- Use the authoritative `zellijlive.Catalog` statuses instead of parsing `zellij list-sessions --short` as an undifferentiated availability list.
- Never resurrect every cache entry; prior active inventory is the allow-list across boot.
- Keep `--all` explicit and preserve the narrower plain-resume workflow.
- Normal Zellij resurrection is allowed for an eligible prior-active session; attach-or-create and `--force-run-commands` remain forbidden.

## Implementation Notes

Depends on task 2. Main surfaces: `internal/resume/planner.go`, `cmd/redeem/main.go`, `internal/zellijlive/*`, config validation, output tests, and the recovery ADR.
