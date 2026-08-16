---
title: Add an on-demand foreground workspace-follow TUI
priority: high
frontloop_approval_task: 1d9ea47a749f8fb696680b912ac3307d196af535948c907aad61612a6535ecfe-4
---

## Goal

Provide a command run on Overton that lists Lattice's current Niri workspaces by name or number, lets the user select one, and follows it only while that foreground command remains open by periodically attaching missing exact sessions in source order.

## Acceptance Criteria

- Running `redeem mirror follow --host lattice` fetches current Lattice workspaces and presents a keyboard-driven TUI showing workspace name or number plus eligible terminal/session counts; no workspace name is hardcoded.
- Selecting a source workspace maps it to the matching Overton workspace by exact name when named, otherwise by numeric index; absence or ambiguity is reported rather than guessed from titles or projects.
- Only visible Kitty windows in the selected source workspace with one exact verified active Zellij session are eligible; headless sessions and non-terminal GUI windows remain available only through the existing manual picker where applicable.
- The foreground loop periodically reacquires source state and opens only exact sessions not currently projected on Overton, launching each newly observed batch in source order.
- The loop is additions-only: it does not reconcile moves, sizes, or order after launch and issues no explicit close. When the Zellij session exits, existing SSH/Zellij clients close naturally.
- SSH failure, incomplete source workspace data, or malformed inventory opens nothing, retains existing windows, reports disconnected state, and retries with bounded backoff.
- `q`, Ctrl+C, process exit, or closing the containing Kitty stops following immediately. No systemd service, timer, daemon, persistent follower configuration, or automatic startup is added.
- Repeated polls, reconnects, local manual close/reopen behavior, duplicate sessions, changing numeric workspace IDs, named workspaces, j/k text filtering, arrows, narrow terminals, and NO_COLOR have deterministic tests.

## Design Decisions

- Following is explicitly foreground and temporary, never always-on.
- Workspace selection is command/TUI driven; support names and numbers without requiring a Redeem config or Niri rule.
- Use periodic complete snapshots and exact-session reconciliation; event streaming and a distributed controller are unnecessary for additions-only semantics.
- Mirror to the matching local name/index and preserve source order only when opening a batch.
- Manual subset picker remains unchanged and separate.

## Implementation Notes

Build on `internal/mirror/snapshot.go`, `internal/mirrortui`, and the exact attachment inventory from task 1. A modest default poll interval with an optional CLI interval override is sufficient; no background unit should be introduced.


## Blocked

Superseded before implementation by Claude Opus 5 review. Revised task must say new projections are moved exactly once to the resolved destination, destination identity is frozen at selection, manual local close causes reopen on the next healthy poll, empty workspaces are represented, and per-poll/total launch bounds are explicit.
