---
title: Perform destructive Niri and reboot recovery drills
priority: medium
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-6
---

## Goal

Validate the shipped recovery contract on lattice with controlled compositor-failure, partial-restore, and machine-reboot scenarios before enabling it as the normal startup path.

## Acceptance Criteria

- Before each drill, a normalized expected mapping of session to workspace, column, and CWD is captured and the relevant Redeem state directory is backed up.
- A three-session/two-named-workspace drill proves same-boot Niri restart recovery, exact session attachment, workspace placement, column order, no duplicates, and idempotent rerun.
- A partial failure drill proves already-open windows are reconciled and only missing sessions are launched.
- A controlled reboot proves only the prior-active allow-list is resurrected and historical exited Zellij cache entries are excluded.
- A final full-inventory drill compares every intended live session against the pre-failure normalized layout and records any degraded unnamed-workspace or unsupported stacked-layout result.
- Service logs, dry-run/apply summaries, `niri msg -j windows`, Zellij catalog evidence, and rollback steps are retained in the completion record.

## Design Decisions

- Run small drills before attempting the full session inventory.
- Do not use attach-or-create or force-run commands during validation.
- Treat arbitrary process revival after reboot as outside Redeem's guarantee; validate Zellij serialization separately.

## Implementation Notes

Depends on task 5 and likely requires deploying the resulting package/configuration through the user's external Nix setup. No source implementation should be added solely to automate destructive physical testing.

## Questions

### Q1: When the implementation and hermetic checks are complete, when may the agent intentionally restart Niri and later reboot lattice, given that both actions will interrupt the active desktop and development sessions?
