---
title: Publish deploy and smoke-test headless slice support
priority: medium
---

## Goal

Publish the minimal headless-output contract, update both consumers to one package revision, and prove the daily Lattice-to-Overton workflow without relying on Lattice's monitor.

## Acceptance Criteria

- ADR 0004, protocol, operations, readiness guidance, contract JSON/schema, flake assertions, and consumer-contract tests describe inventory schema 2 and zero-output behavior consistently.
- Upgrade guidance states that mixed package revisions degrade safely and retain prior authority; no mismatched reader may authorize disappearance or spatial writes.
- Terminal Redeemer release validation passes with no unrelated feature expansion.
- Mono-nix pins the released revision on Lattice and Overton and updates exact contract assertions without activating either host automatically.
- Both host builds pass and result links are removed.
- After user-owned activation, Lattice inventory is complete with its monitor unavailable; `Mod+Ctrl+Return` lists sources; one selected source attaches; and selected `Mod+Return` routes into an existing named workspace.
- The smoke confirms no duplicate host session, no local fallback after remote intent, no unrelated focus/window mutation, and no proportional-size action while the host output is absent.

## Design Decisions

- Do not add virtual displays, arbitrary headless Zellij discovery, multi-monitor support, clipboard sync, or missing-workspace creation.
- Keep deployment declarative and activation user-owned.
- Preserve existing enrolled inventory/controller authority; never reinitialize over current state.

## Implementation Notes

Terminal Redeemer paths include `contracts/host-leech-slices/v1`, `docs`, `flake.nix`, and consumer-contract tests. Consumer work belongs in `/home/jmo/Development/mono/nix`; use `jj`, flakes via `path:.`, repository validation wrappers, and remove `result` symlinks.
