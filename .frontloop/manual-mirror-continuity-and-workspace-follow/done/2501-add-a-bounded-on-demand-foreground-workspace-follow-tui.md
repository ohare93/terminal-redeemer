---
title: Add a bounded on-demand foreground workspace-follow TUI
priority: high
frontloop_approval_task: 8131f8be65f97e7fee6decd7c97db1f7d29347b3c33d545062d159efc8657b88-3
---

## Goal

Let the user run one foreground command on Overton, select a current Lattice workspace by name or number, and periodically open only its missing exact Zellij projections in source order until that command exits.

## Acceptance Criteria

- `redeem mirror follow --host lattice` acquires a source snapshot with a backward-compatible top-level workspace inventory, including empty workspaces, and presents a filterable arrow-navigated TUI showing workspace name or number plus eligible visible-terminal/session counts; no workspace is hardcoded.
- Only visible source Kitty windows in the selected workspace with one exact verified active Zellij session are eligible; headless sessions and non-terminal GUI windows remain outside follow mode.
- At selection time, the destination is resolved exactly once to the matching Overton workspace by unique exact name when named, otherwise by numeric index; absence/ambiguity fails visibly, and later local renumbering cannot silently retarget the running follower.
- Each healthy poll reacquires complete source state, computes an exact-session set difference against live Overton projection evidence, and opens a bounded batch of missing sessions in source order through the existing `PlanLaunch` path.
- Every newly opened projection is identified by exact live session/process evidence and moved exactly once by Niri window ID to the frozen destination. After initial placement the loop never re-moves, resizes, reorders, or explicitly closes it.
- The loop is additions-only. If the user manually closes a projection while its source window/session remains eligible, the next healthy poll reopens it. If the Zellij session exits, attached SSH/Zellij clients close naturally without a Redeem close action.
- Default per-poll and total-open safeguards are finite, visible in the TUI/status, and overrideable by explicit CLI flags so the user can intentionally mirror a larger workspace; deferred sessions are reported rather than silently dropped.
- SSH failure, incomplete/malformed workspace inventory, session ambiguity, or local evidence failure opens nothing for that poll, retains existing windows, reports disconnected/degraded state, and retries no faster than a documented minimum interval with bounded backoff.
- `q`, Ctrl+C, SIGHUP/process exit, or closing the containing Kitty stops following immediately. No systemd service, timer, daemon, persistent selection/configuration, Niri rule, source epoch, event stream, or distributed controller is added.
- Tests cover empty/named/numbered workspaces, j/k printable filtering, arrows, narrow terminals, NO_COLOR, frozen destination identity, source-order batches, launch bounds, repeated polls, reconnect/backoff, manual-close reopen, session exit, duplicate/ambiguous sessions, and clean cancellation.

## Design Decisions

- Following is explicitly foreground and temporary, never always-on.
- Workspace selection is command/TUI driven and requires no Redeem config or new Niri rule.
- Use periodic complete snapshots and exact-session reconciliation; event streaming and controller machinery are unnecessary for additions-only semantics.
- Move each new projection once to the matching local workspace, then leave user placement alone.
- Manual subset picker and pinned save/apply remain separate workflows.

## Implementation Notes

Build on `internal/mirror/snapshot.go`, `internal/mirrortui`, `PlanLaunch`, and the live projection helper from the previous task. Niri dynamic workspaces can renumber, so freeze the destination's resolved ID/name/index at selection and warn that named workspaces such as `agentleman` are the durable path.


## Completion Summary

- Added backward-compatible complete workspace and ACTIVE-session inventories, including empty workspaces.
- Added foreground `mirror follow` workspace picker with arrow-only navigation, printable filtering, narrow/NO_COLOR rendering, and no persistent selection.
- Added source/destination runtime-ID freezing, global exact-session validation, bounded source-order additions-only reconciliation, conservative lifetime launch accounting, exact token correlation, and one-time exact-ID placement.
- Added capped degraded/disconnected backoff and joined q/Ctrl+C/signal cancellation so no poll survives lock release.
- Passed independent correctness/Ponytail reviews, full tests/race/vet, Nix evaluation, and repeated hostile/cancellation stress.

### Files Changed

- README.md
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- docs/CONFIG.md
- docs/OPERATIONS.md
- internal/mirror/follow.go
- internal/mirror/follow_test.go
- internal/mirror/pin_workflow.go
- internal/mirror/snapshot.go
- internal/mirror/snapshot_test.go
- internal/mirrortui/workspace.go
- internal/mirrortui/workspace_test.go
