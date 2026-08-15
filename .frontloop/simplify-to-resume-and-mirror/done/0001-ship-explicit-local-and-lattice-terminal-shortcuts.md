---
title: Ship explicit local and Lattice terminal shortcuts
priority: critical
frontloop_approval_task: bb1f5140ea3f97ba3060ab67219d59f37ae4035c722e0e1536b1f3d88d757f10-1
---

## Goal

Make Super+Enter unconditionally local and add the smallest remote workflow: create a persistent Zellij session on Lattice, attach in a local owned Kitty, and reopen all live Lattice sessions—including headless sessions—from the project-first picker.

## Acceptance Criteria

- The generated Niri integration binds Mod+Return directly to Kitty, a distinct binding to `redeem mirror new --host lattice`, and a distinct binding to the mirror picker
- `redeem mirror new` generates a safe unique session identity and launches one local owned Kitty that SSHes to an exact newly created Lattice Zellij session
- Remote creation never falls back to a local shell; an ambiguous/lost connection leaves any created session discoverable rather than requiring controller recovery state
- Mirror inventory includes exact live Zellij sessions not currently represented by a Niri terminal window, without duplicate picker rows
- Closing the local Kitty detaches without intentionally terminating the remote Zellij session
- Focused tests cover unique creation argv/quoting, headless discovery/deduplication, and binding behavior
- `go test ./...` and relevant Nix contract checks pass

## Design Decisions

- No visible Kitty is created on Lattice; the persistent terminal runs there and is viewed from Overton
- No continuous workspace/layout synchronization
- Existing-session paths remain attach-only; attach-or-create is only for newly generated session names
- Normal Niri window close remains the local mirror close UX

## Implementation Notes

Reuse internal/mirror quoting, launch planning, session CWD resolver, owned app ID, and picker. Prefer one SSH/Kitty flow over a durable transaction. Relevant paths: cmd/redeem/main.go, internal/mirror, internal/mirrortui, modules/home-manager, contracts/host-leech-slices/v1/niri-bindings.kdl.in.


## Completion Summary

- Added `mirror new` with safe generated session identities, remote-only create/attach argv, and detach-on-close behavior
- Added active-only headless Zellij discovery with exact deduplication and dead-cache exclusion
- Bound Mod+Return to local Kitty and added distinct Lattice new/picker bindings
- Added focused CLI, launch, inventory, picker, Home Manager, and contract coverage; full Go and Nix checks passed

### Files Changed

- cmd/redeem/main.go
- cmd/redeem/main_test.go
- internal/mirror/new.go
- internal/mirror/windows.go
- internal/mirror/remote.go
- internal/mirror/snapshot.go
- internal/mirror/snapshot_test.go
- internal/mirror/orchestration_test.go
- internal/mirrortui/model_test.go
- internal/procmeta/session_verifier.go
- internal/procmeta/session_verifier_test.go
- contracts/host-leech-slices/v1/niri-bindings.kdl.in
- modules/home-manager/terminal-redeemer.nix
- flake.nix
