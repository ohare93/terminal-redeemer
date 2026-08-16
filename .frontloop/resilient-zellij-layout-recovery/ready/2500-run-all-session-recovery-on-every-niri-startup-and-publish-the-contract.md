---
title: Run all-session recovery on every Niri startup and publish the contract
priority: high
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-5
---

## Goal

Wire the verified `resume --all` path into the Home Manager/Niri lifecycle so it runs after login and after a compositor restart, then expose diagnostics and documentation needed to operate it safely.

## Acceptance Criteria

- When `resume.onStartup` is enabled, the generated startup integration invokes the same `redeem resume --all` systemd oneshot every time Niri starts, not only when the long-lived graphical-session target first activates.
- The startup oneshot retains bounded readiness retries, uses the shared operation lock, and cannot race periodic capture into duplicate windows.
- Periodic capture starts after the initial recovery attempt and later empty-window captures cannot erase sticky placement.
- `redeem doctor` reports checkpoint schema/integrity, active versus prior-active candidate counts, resurrection-cache availability, incomplete session identity, and tracked placements that rely on unnamed workspace indices.
- The generated Niri integration and systemd service have evaluation tests, including repeated compositor startup and disabled-startup configurations.
- README, configuration, operations guidance, CLI help, and a new ADR document same-boot recovery, reboot resurrection boundaries, `--all`, max-age semantics, named-workspace preference, ordering limits, failure reporting, and the prohibition on create/force-run behavior.
- `go test ./...` and `nix flake check` pass.

## Design Decisions

- Use the existing Home Manager-owned systemd oneshot for logging/retry policy and expose/invoke it from the generated Niri startup integration.
- Automatic startup uses `resume --all`; plain manual resume remains narrower.
- Keep periodic complete capture rather than adding a continuous Niri event subscriber; placement may be stale by at most the configured capture interval.
- Prefer named workspaces and warn, rather than inventing or renaming workspaces automatically.

## Implementation Notes

Depends on task 4. Relevant files: `modules/home-manager/terminal-redeemer.nix`, `modules/nixos/terminal-redeemer.nix`, `internal/doctor/*`, `internal/config/*`, `flake.nix`, `README.md`, `docs/CONFIG.md`, `docs/OPERATIONS.md`, and `docs/adr/`.
