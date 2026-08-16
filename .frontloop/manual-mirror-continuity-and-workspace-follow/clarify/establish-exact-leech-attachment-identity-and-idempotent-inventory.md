---
title: Establish exact leech attachment identity and idempotent inventory
priority: critical
frontloop_approval_task: 1d9ea47a749f8fb696680b912ac3307d196af535948c907aad61612a6535ecfe-1
---

## Goal

Give every Redeem-owned Overton mirror window durable, exact source-host and Zellij-session identity so manual snapshots and repeated workspace reconciliation can safely detect what is already open. Reuse the existing safe SSH/Zellij launch path and owned-window boundary rather than reviving the deleted slice controller or general replay machinery.

## Acceptance Criteria

- Newly launched mirror windows can be correlated to an exact source host, exact case-sensitive Zellij session, remote CWD, local Niri window ID, and local workspace without treating mutable titles as authority.
- Attachment metadata is bounded, schema-versioned, stored with mode 0600 using lock/temp/fsync/rename/directory-fsync publication, and contains no arbitrary executable or shell command payloads.
- Local inventory verifies live Niri/PID/process evidence before considering a record open; stale records are ignored or pruned without closing any window.
- Existing pre-upgrade owned mirror windows are either adopted through exact process evidence or reported as untracked; they are never guessed from title text or destructively mutated.
- Repeated inventory and launch planning suppress exact-session duplicates while preserving existing safe quoting, attach-only behavior for known sessions, generated-name-only creation, environment clearing, and owned close boundaries.
- Focused tests cover hostile host/session/CWD values, stale/spoofed metadata, PID reuse, ambiguous windows, legacy-window handling, exact-case identities, and concurrent unrelated Kitty windows.

## Design Decisions

- Exact Zellij session identity remains the projection identity for this intentionally terminal-only workflow.
- Persist typed attachment facts, never arbitrary argv or shell strings.
- Titles remain presentation only; owned app ID alone is insufficient authority.
- Do not reintroduce RPC, source epochs, distributed authority, event history, or general GUI replay.

## Implementation Notes

Start at `internal/mirror/windows.go`, `internal/mirror/remote.go`, current PID-correlation patterns in `internal/resume/executor.go`, and atomic storage in `internal/checkpoints/store.go`. This task is the shared prerequisite for save/apply and follow.


## Blocked

Superseded before implementation by Claude Opus 5 plan review. The task's durable registry premise is unnecessary: Kitty's explicit --title is immutable, and live Niri PID plus exact SSH/Zellij descendant argv can inventory projected sessions without persistent registry state. Revised plan will fold the minimal live-evidence helper into save/apply.
