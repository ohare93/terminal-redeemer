---
title: Route launches into existing headless host workspaces
priority: medium
---

## Goal

Let selected `Mod+Return` launches complete against an existing named Lattice workspace while Lattice has no active output.

## Acceptance Criteria

- Workspace lookup returns an existing exact uniquely-normalized named workspace without requiring host output geometry.
- The routed transaction launches one exact host Kitty/Zellij source, places it by exact existing workspace ID, and proves the committed source through complete headless inventory.
- Missing, duplicate, or normalization-colliding headless workspaces fail closed without inventing a workspace or launching a local fallback.
- Replay, pending-token, exact process/window/session ownership, and no-duplicate guarantees remain unchanged.
- Focused RPC, routed-launch, transport, and crash-boundary tests pass.

## Design Decisions

- Support only existing named workspaces while headless.
- Do not implement headless trailing-workspace creation until a real use case proves it necessary.
- Preserve the rule that ambiguous remote intent never falls back to local Kitty.

## Implementation Notes

Primary paths: `internal/slicerpc/server.go`, routed-launch tests, and applicable subprocess acceptance fixtures. Perform exact workspace-name resolution before output-dependent workspace-creation validation.
