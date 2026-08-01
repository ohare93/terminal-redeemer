---
title: Prepare mono-nix for Terminal Redeemer v1.1 without activation
priority: medium
---

## Goal

Pin mono-nix to the published Terminal Redeemer v1.1 revision and update its provider-free consumer contracts/documentation while leaving controller, Leech mode, bindings, and machine activation disabled.

## Acceptance Criteria

- mono-nix pins Terminal Redeemer commit `05413a3e737bc02bbad9ee99023391cd1a417870` with an updated lock/narHash and no local path override.
- Focused consumer and Lattice/Overton contracts assert contract 1.1.0, unchanged inventory/RPC/controller schemas 1/1/2, all-eligible semantics, routed-launch separation, manage/all/pickup-remove surfaces, and exact read-only manageCommand availability.
- Lattice and Overton retain their existing public roles/source labels and exact upstream package identity; Hatch remains excluded.
- Capture, startup restore, controller, Leech mode, automatic bindings, and machine activation remain disabled; no Niri keybinding is installed.
- mono-nix documentation records the new immutable pin, contract 1.1.0, prepared management surfaces, compatibility limits, and that physical activation/smoke remains a separate approved step.
- Focused Terminal Redeemer consumer checks, Lattice/Overton contract checks, host evaluations/builds appropriate for a no-activation preparation, formatting/linting, and mono-nix's required validation pass.
- No `nixos-rebuild switch`, Home Manager activation, live SSH/Niri/Zellij command, credential access, or running-session mutation occurs.

## Design Decisions

- Use the published upstream commit, not a local path override.
- Prepare contract/package consumption only; keep every runtime gate off.
- Do not reserve or install a management key until the physical rollout is separately approved.

## Implementation Notes

Target repository: `/home/jmo/Development/mono-nix` (clean jj working copy). Relevant files include flake.nix/flake.lock, features/terminal-redeemer, modules/flake/lattice-overton-terminal-redeemer-contract.nix, profiles/personal-development, and host role declarations. User explicitly selected 'Prepare mono-nix only' and separately authorized publishing Terminal Redeemer.


## Completion Summary

- Pinned mono-nix to published Terminal Redeemer commit 05413a3e737bc02bbad9ee99023391cd1a417870 with a one-node lock delta.
- Updated focused and Lattice/Overton consumer contracts for v1.1 all/manage/pickup-remove surfaces and exact read-only manage argv.
- Preserved host roles/source labels/package counts, Hatch exclusion, and every disabled runtime/activation/binding gate.
- Updated mono-nix architecture, validation, feature, migration, handoff, cutover, and host documentation for the immutable build-only preparation.
- Passed formatting, Statix, deadnix, architecture, focused contracts, all host evaluations, warning-permissive host builds, and `sd all check`; no activation occurred.
- Independent review found and verified one dossier wording correction; final review is clean.

### Files Changed

- /home/jmo/Development/mono-nix/flake.nix
- /home/jmo/Development/mono-nix/flake.lock
- /home/jmo/Development/mono-nix/features/terminal-redeemer/modules/flake/integration.nix
- /home/jmo/Development/mono-nix/modules/flake/lattice-overton-terminal-redeemer-contract.nix
- /home/jmo/Development/mono-nix/tools/validation/check_architecture.py
- /home/jmo/Development/mono-nix/features/terminal-redeemer/README.md
- /home/jmo/Development/mono-nix/docs/architecture.md
- /home/jmo/Development/mono-nix/docs/copy-inventory.md
- /home/jmo/Development/mono-nix/docs/cutover-dossiers/lattice.md
- /home/jmo/Development/mono-nix/docs/migration-map.md
- /home/jmo/Development/mono-nix/docs/session-handoff.md
- /home/jmo/Development/mono-nix/docs/validation.md
- /home/jmo/Development/mono-nix/hosts/lattice/README.md
- /home/jmo/Development/mono-nix/hosts/overton/README.md
