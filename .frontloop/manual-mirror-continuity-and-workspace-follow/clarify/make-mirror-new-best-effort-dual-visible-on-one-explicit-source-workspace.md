---
title: Make mirror new best-effort dual-visible on one explicit source workspace
priority: critical
frontloop_approval_task: 1d9ea47a749f8fb696680b912ac3307d196af535948c907aad61612a6535ecfe-2
---

## Goal

Change `redeem mirror new` so Overton still creates and views the unique persistent Lattice Zellij session, then always attempts to open a second Kitty on Lattice attached to that exact session in an explicitly supplied source workspace.

## Acceptance Criteria

- The Overton path remains the sole creator: it uses a generated `redeem-<32 hex>` identity and `zellij attach --create`; the Lattice Kitty uses attach-only after exact live-session readiness is proven.
- `mirror new` accepts an explicit source workspace selector by Niri name or number; Redeem contains no hardcoded `agentleman` value.
- The remote source launcher obtains the Lattice graphical environment from the user manager, launches a normal Kitty containing the exact Zellij session, and moves it to the requested source workspace without shell interpolation of untrusted values.
- Lattice output/monitor absence is not used as a veto. Source-Kitty failure is clearly reported but never closes the Overton viewer, kills the Zellij session, retries creation, or spawns duplicates.
- Closing either Kitty detaches without killing the session; exiting the Zellij session naturally closes both clients.
- Dry-run and injected-runner tests prove command ordering, readiness timeout, partial failures, zero-output behavior, exact workspace selection, safe argv, and no source launch duplication.

## Design Decisions

- Always attempt both views, but source visibility is best effort.
- One explicit source workspace is supplied by command/config integration; no Niri app rule is required by Redeem.
- No monitor-connected check; Niri/graphical launch capability is the relevant boundary.
- The source client never receives `--create`.

## Implementation Notes

The current root cause is in `internal/mirror/windows.go` and `cmd/redeem/main.go`: only one local Kitty→SSH→Zellij command exists. Reuse `envWithGraphicalSession` behavior on Lattice. Mono/nix will later pass `agentleman` explicitly for Overton's binding.


## Blocked

Superseded before implementation by Claude Opus 5 review. Revised task must specify a source-local `redeem` attach helper that survives the SSH launcher, correlates the detached Kitty by exact session/process evidence, moves it by exact Niri window ID, and keeps `--source-workspace` optional for backward-compatible CLI/contracts.
