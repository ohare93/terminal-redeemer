# ADR 0002: Keep remote mirror continuity manual and bounded

- Status: accepted

## Context

Persistent Zellij sessions survive a viewer disconnect, but reopening several projections should not require reconstructing them individually. A temporary workspace-following view is also useful while actively working across Lattice and Overton. Neither need a distributed controller, durable projection registry, or general GUI replay system.

## Decision

Terminal Redeemer provides four separate remote workflows:

1. `mirror open` is a manual project/session picker over a fresh source snapshot. It can show visible and headless live sessions and opens only the user's explicit selection.
2. `mirror save` replaces one host/profile pin from fresh source metadata and exact live local PID/SSH/Zellij evidence. `mirror apply` preflights active sessions and reopens the available missing projections attach-only in captured order. Pins contain typed placement metadata only, live outside `checkpoints/`, and are not retention history.
3. `mirror follow` is a foreground TUI. It freezes one source and destination workspace identity, periodically reacquires complete snapshots, and adds bounded batches of missing visible exact sessions. It moves each new projection once, never closes or continually reconciles it, and stops with the foreground process.
4. `mirror new` gives Overton sole creation authority for one generated persistent session. After creation it best-effort invokes a bounded source-local attach helper. Optional source placement is an explicit argument; source-view failure cannot roll back the session or Overton view.

Projection identity comes from a current Niri window PID and unambiguous descendant process evidence matching Redeem's deterministic SSH and case-sensitive Zellij attach argv. Titles are presentation only. Mutation is limited to positively verified owned windows and exact Niri window IDs.

Rolling local capture/resume remains a fifth, independent workflow. Its boot checkpoints do not read or write mirror pins.

## Consequences

- There is no projection registry, event history, source epoch, RPC protocol, controller, daemon, follower service, timer, Niri rule, or persisted follow selection.
- Save/apply can restore sessions, captured opening order, destination workspaces, floating/tiled state, and supported sizes, but not exact columns or stacks.
- Follow reopens a manually closed eligible projection on a later healthy poll, while source-session exit needs no Redeem close action.
- Finite per-poll and lifetime attempt limits bound detached launch ambiguity. Disconnected or degraded observations retain existing windows and retry with capped backoff.
- SSH aliases are identified by their exact configured destination token, not claimed as canonical host identities.
- Activation, zero-output behavior, connection-loss recovery, version skew, and physical dual-host placement remain user-owned smoke evidence.

## Rejected alternatives

- **Durable attachment registry:** duplicates live process authority and introduces stale adoption and pruning problems.
- **Controller, RPC, or event stream:** unnecessary lifecycle and compatibility machinery for manual apply and additions-only temporary following.
- **Automatic close or continuous placement reconciliation:** risks mutating user-arranged or unrelated windows.
- **Always-on follow service or Niri rule:** turns an explicit temporary view into hidden persistent policy.
- **Exact column/stack replay:** unsupported by the bounded native Niri actions used here.
