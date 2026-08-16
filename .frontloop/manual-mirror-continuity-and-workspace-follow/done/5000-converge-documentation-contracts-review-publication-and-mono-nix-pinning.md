---
title: Converge documentation, contracts, review, publication, and mono/nix pinning
priority: medium
frontloop_approval_task: 8131f8be65f97e7fee6decd7c97db1f7d29347b3c33d545062d159efc8657b88-4
---

## Goal

Validate and document the bounded workflows, update declarative integration without activation, independently review the result, and—only after explicit user confirmation—publish Terminal Redeemer and pin mono/nix.

## Acceptance Criteria

- CLI help, README, operations/config docs, and ADR language clearly distinguish manual picker, pinned save/apply, temporary foreground follow, rolling local resume, and best-effort dual-visible new sessions; obsolete statements that `mirror new` never creates a source Kitty are removed.
- Home Manager/module contracts expose the optional `mirror new` source-workspace argument and new command surfaces but add no follower timer/service, automatic workspace following, or hardcoded workspace in Redeem.
- Mono/nix passes `agentleman` explicitly only for Overton's `mirror new` source placement; `mirror follow` remains runtime TUI-driven with no persistent selection or always-on unit.
- The exact existing command assertions in Terminal Redeemer `flake.nix`, mono/nix Terminal Redeemer feature integration, and mono/nix Lattice/Overton contract are updated in lockstep while preserving the no-selector compatibility path.
- Go tests, full race tests, vet, flake/package checks, focused Lattice/Overton contracts, and synthetic build-only host builds pass. Any unrelated aggregate failure is named and shown byte-identical against the pre-change parent/baseline rather than merely described as pre-existing.
- Independent correctness/security and Ponytail reviews find no blockers, including no unnecessary registry/controller/history machinery and no unsafe process/argv parsing or owned-window mutation.
- Physical two-host scenarios—same-boot save/apply after loss, follow start/stop/reconnect, source-session exit, zero-output dual-visible partial failure, and activation/version skew—remain solely in a precise user-owned smoke checklist; no impossible read-only live harness is claimed.
- After implementation/review evidence is complete, obtain explicit user confirmation before outward publication. If approved, publish the reviewed Terminal Redeemer revision without force, then update mono/nix to the exact revision/NAR hash in a committed local revision.
- No activation, deployment, switch/test/boot/dry-activate, destructive migration, physical window manipulation, or deletion of historical state is performed; build result links are removed.

## Design Decisions

- Keep activation and physical dual-host validation user-owned.
- Do not delete historical state; new code ignores obsolete slice/history artifacts.
- Do not publish merely because implementation passed; ask at the irreversible boundary.
- The restored mono/nix `agentleman` workspace rule is compatible but not a prerequisite for runtime follow selection.
- Build evidence must never be represented as activation evidence.

## Implementation Notes

Current Terminal Redeemer main is `ec2dcb7e8dfc4c920b55e28c51c1fcdc8535f6d0`; mono/nix currently pins it but live Lattice has mixed/stale activated units. Relevant docs include README.md and docs/OPERATIONS.md. Confirm dead `_compat` restore blocks before touching them; prefer active host adapters/contracts only.


## Completion Summary

- Converged CLI help, workflow documentation, bounded-continuity ADR, Home Manager command surfaces, and packaged/module assertions without follower automation.
- Published reviewed Terminal Redeemer main non-force at 9ebc121791dae2b0739e401e176df50c54cf455f, including the clean consumer vendorHash fix; published NAR is sha256-D9ELoE5xDd1Mr+h6UVY5znAfE1ngem4GpUCYCe+g2nk=.
- Pinned mono/nix to the exact published revision/NAR and passed `agentleman` only to Overton mirror-new while preserving Lattice/no-selector compatibility and runtime-only follow.
- Updated focused active contracts and architecture guards without `_compat` edits, follower services/timers/rules, or weakened readFile boundaries.
- Passed Go tests/race/vet, upstream flake/package checks, mono consumer/host/personal-apps/Niri contracts, and build-only synthetic Lattice/Overton builds.
- Proved unrelated personal-app architecture and root networking failures byte-identical to baseline; completed independent correctness/Ponytail review with no blockers.
- Left activation and the documented physical two-host smoke checklist entirely user-owned; no deployment, switch, boot, dry-activate, state migration, or physical window mutation occurred.

### Files Changed

- Terminal Redeemer: README.md
- Terminal Redeemer: cmd/redeem/main.go
- Terminal Redeemer: cmd/redeem/main_test.go
- Terminal Redeemer: docs/CONFIG.md
- Terminal Redeemer: docs/OPERATIONS.md
- Terminal Redeemer: docs/adr/0002-bounded-mirror-continuity.md
- Terminal Redeemer: flake.nix
- Terminal Redeemer: internal/mirror/pin_active_preflight_test.go
- Terminal Redeemer: internal/mirror/pin_apply_hardening_test.go
- Terminal Redeemer: internal/mirror/pin_test.go
- Terminal Redeemer: internal/mirror/pin_workflow.go
- Terminal Redeemer: modules/home-manager/terminal-redeemer.nix
- mono/nix: features/terminal-redeemer/README.md
- mono/nix: features/terminal-redeemer/modules/flake/integration.nix
- mono/nix: flake.lock
- mono/nix: flake.nix
- mono/nix: hosts/overton/default.nix
- mono/nix: modules/flake/lattice-overton-terminal-redeemer-contract.nix
- mono/nix: modules/flake/personal-apps-contract.nix
- mono/nix: profiles/personal-development/default.nix
- mono/nix: tools/validation/check_architecture.py
