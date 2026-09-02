# ADR 0001: Resume exact Zellij terminals into Niri workspaces

- Status: accepted

## Context

After a reboot, Niri runtime workspace/window IDs and process IDs are different. A safe resume mechanism must recover exact live Zellij sessions without guessing which new Kitty window belongs to which captured terminal or creating a missing session under a stale name.

The product needs the latest successful terminal placement from each boot.

## Decision

### Rolling boot checkpoints

Periodic capture performs a complete Niri query and terminal enrichment, then atomically replaces one checkpoint for the current boot/host/profile identity. Checkpoints contain the normalized state, observation time, semantic hash, and Linux boot ID.

Capture and prune share an advisory repository writer lock. Checkpoint publication uses temporary-file write, file fsync, atomic rename, and checkpoint-directory fsync. A failed query or pre-rename publish leaves the prior successful file usable.

Checkpoints also bind an authoritative ACTIVE-session allow-list and sticky per-session placement into their integrity hash. Plain manual resume excludes the current boot and remains the narrower prior-visible workflow. `resume --all` instead uses the current exact ACTIVE catalog after a same-boot compositor failure; after reboot it filters host/profile, selects the newest prior recovery point, and permits only sessions in that prior-active allow-list which are currently exact active or dead-resurrectable. It does not admit unrelated resurrection-cache history or fall back to an older boot when the authoritative candidate is empty or stale.

Maximum age blocks stale prior dead-session resurrection and warns on old sticky placement, but does not block attachment of a currently exact ACTIVE session. Sticky placement prefers named workspaces, then output plus index, then index; index-dependent tracked placement remains visible as an operational warning rather than causing automatic workspace creation or renaming.

### Exact attach-only recovery

For every eligible terminal, resume:

1. verifies the case-sensitive exact Zellij status and maps any already-open attachment to exactly one Niri window;
2. launches only missing sessions directly with `zellij attach -- <session>` and captured CWD;
3. correlates the launcher PID to exactly one Niri `client_pid`;
4. verifies an exact live descendant attach argv and revalidates the all-session candidate before launch;
5. moves both existing and new windows by exact ID to the resolved workspace;
6. waits until all possible targets exist, then restores deterministic captured terminal-column order with verified focus/actions;
7. observes required placement before reporting success and restores prior focus best effort.

Missing or ambiguous sessions are unavailable and never recreated. Attach-or-create, create, and force-run flags are prohibited. A launcher that forks or daemonizes beyond exact PID correlation fails rather than triggering an app-ID/order heuristic.

Workspace targets resolve by exact name, then output plus index, then index. Unresolved targets follow explicit `current`, `skip`, or `fail` policy. Floating and supported sizes remain best effort. Column ordering is limited to terminal targets with one window per captured column; stacked rows are explicitly unsupported rather than guessed with consume/expel, and unrelated-window ordering is not reconstructed.

### Startup policy

Manual `redeem resume` is the default. When enabled, Home Manager's graphical-session oneshot invokes `redeem resume --all` with bounded readiness/restart policy and the shared capture/resume operation lock; `onStartup` alone installs this initial graphical service. Consumers include the generated Niri fragment exactly once to supply repeated-launch behavior. Its single native `spawn-at-startup` command synchronously imports the new compositor's `NIRI_SOCKET` and graphical environment into the systemd user manager before restarting that same unit on every compositor launch, including same-boot restarts; it does not add another service, recovery command, or event subscriber. Periodic complete capture is ordered after initial recovery and may leave sticky placement stale by at most the capture interval. Repeated recovery invocation is idempotent.

## Consequences

- The last complete capture survives abrupt failure within the filesystem durability contract.
- Current-boot sticky state supports same-boot compositor recovery while prior-active allow-listing bounds reboot resurrection.
- Session attachment and workspace mutation cannot target a window chosen by prefix, app ID, creation order, or proximity.
- Ordering degrades explicitly for stacks instead of performing unsafe layout guesses.
- The state model is intentionally terminal-only and one checkpoint per boot; mirror snapshots and pins remain separate state.

## Rejected alternatives

- **Attach-or-create during resume:** may recreate a dead session and falsely report recovery.
- **Application ID or launch-order correlation:** concurrent windows can be assigned to the wrong workspace.
- **Current-boot latest state:** startup capture could overwrite the state needed for recovery.
- **Continuous compositor subscription:** adds lifecycle/reconnect complexity without evidence that periodic complete capture is insufficient.
