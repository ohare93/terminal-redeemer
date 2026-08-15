# ADR 0001: Resume exact Zellij terminals into Niri workspaces

- Status: accepted

## Context

After a reboot, Niri runtime workspace/window IDs and process IDs are different. A safe resume mechanism must recover exact live Zellij sessions without guessing which new Kitty window belongs to which captured terminal or creating a missing session under a stale name.

The product needs the latest successful placement from each boot, not a time-travel event timeline or arbitrary GUI application recreation.

## Decision

### Rolling boot checkpoints

Periodic capture performs a complete Niri query and terminal enrichment, then atomically replaces one checkpoint for the current boot/host/profile identity. Checkpoints contain the normalized state, observation time, semantic hash, and Linux boot ID.

Capture and prune share an advisory repository writer lock. Checkpoint publication uses temporary-file write, file fsync, atomic rename, and checkpoint-directory fsync. A failed query or pre-rename publish leaves the prior successful file usable.

Resume reads checkpoint files directly. It excludes the current boot, filters host/profile, and selects the newest prior boot before evaluating age or eligible terminal contents. It does not fall back to an older boot when that authoritative candidate is empty or stale.

### Exact attach-only recovery

For every captured terminal, resume:

1. verifies the case-sensitive Zellij session exists;
2. skips it if the same verified session is already open;
3. launches Kitty directly with `zellij attach -- <session>` and captured CWD;
4. correlates the launcher PID to exactly one Niri `client_pid`;
5. verifies an exact live descendant attach argv;
6. moves only that window to the resolved workspace;
7. observes required placement before reporting success.

Missing sessions are unavailable and never recreated. A launcher that forks or daemonizes beyond exact PID correlation fails rather than triggering an app-ID/order heuristic.

Workspace targets resolve by exact name, then output plus index, then index. Unresolved targets follow explicit `current`, `skip`, or `fail` policy. Optional layout details remain best effort and are reported separately from required placement.

### Startup policy

Manual `redeem resume` is the default. Home Manager may invoke the same command from a graphical-session user service after competing startup restorers have been disabled. Repeated invocation is idempotent.

## Consequences

- The last complete capture survives abrupt failure within the filesystem durability contract.
- Current-boot capture cannot hide the newest prior-boot recovery candidate.
- Session attachment and workspace mutation cannot target a window chosen by prefix, app ID, creation order, or proximity.
- The state model is intentionally terminal-only and one checkpoint per boot.
- Old event logs and timestamped snapshots are ignored and left untouched rather than migrated destructively.

## Rejected alternatives

- **Attach-or-create during resume:** may recreate a dead session and falsely report recovery.
- **Application ID or launch-order correlation:** concurrent windows can be assigned to the wrong workspace.
- **Current-boot latest state:** startup capture could overwrite the state needed for recovery.
- **Continuous compositor subscription:** adds lifecycle/reconnect complexity without evidence that periodic complete capture is insufficient.
- **General GUI application recreation:** application identity and duplicate behavior are not strong enough for the required safety boundary.
