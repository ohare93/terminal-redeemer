---
title: Converge contracts, documentation, publication, and mono/nix integration
priority: medium
frontloop_approval_task: 1d9ea47a749f8fb696680b912ac3307d196af535948c907aad61612a6535ecfe-5
---

## Goal

Validate and publish the bounded workflows, update mono/nix to consume them on Lattice and Overton without activation, and leave a precise physical smoke checklist for the user.

## Acceptance Criteria

- CLI help, README, operations/config documentation, and ADR language distinguish manual picker, pinned save/apply, temporary foreground follow, rolling local resume, and best-effort dual-visible new sessions.
- Home Manager/module contracts expose only the required `mirror new` source-workspace argument and command surfaces; they add no follower timer/service, automatic workspace following, or hardcoded workspace in Redeem.
- Mono/nix passes `agentleman` explicitly as Overton's `mirror new` source workspace while workspace-follow selection remains runtime TUI-driven.
- Go tests, race tests, vet, flake checks, packaged CLI checks, focused Lattice/Overton contracts, and synthetic build-only host builds pass or clearly isolate an unchanged unrelated aggregate failure.
- A read-only acceptance harness covers exact duplicate suppression, save/apply after same-boot connection loss, temporary follow start/stop/reconnect, source-session exit, and dual-visible new partial failure.
- The reviewed Terminal Redeemer revision is published to GitHub main and mono/nix is pinned to its exact revision/NAR hash in a committed local revision.
- No activation, deployment, switch/test/boot/dry-activate, destructive migration, or physical window manipulation is performed; result links are removed.
- The final user-owned smoke checklist verifies Overton save/apply, source workspace TUI selection by `agentleman` and by number, source-order batch opening, stopping follow, zero-output Lattice source Kitty behavior, session exit closing both views, and reboot/connection-loss recovery.

## Design Decisions

- Keep activation and physical dual-host validation user-owned.
- Do not delete historical state; new code ignores obsolete slice/history artifacts.
- Publish only after independent correctness and Ponytail review.
- The current committed mono/nix Agentleman workspace rule is acknowledged but is not a prerequisite for runtime workspace selection.

## Implementation Notes

Terminal Redeemer currently publishes from GitHub main; mono/nix current pin is `ec2dcb7e8dfc4c920b55e28c51c1fcdc8535f6d0`. Live Lattice has mixed/stale activated package units, so build evidence must not be represented as activation evidence.


## Blocked

Superseded before implementation by Claude Opus 5 review. Revised convergence task removes the impossible read-only two-host harness, lists all exact Nix contract sites, requires byte-identical baseline evidence for unrelated failures, and leaves physical disconnect/source-exit scenarios solely in the user-owned smoke checklist.
