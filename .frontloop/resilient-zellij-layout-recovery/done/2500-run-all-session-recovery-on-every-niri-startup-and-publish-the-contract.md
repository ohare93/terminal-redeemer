---
title: Run all-session recovery on every Niri startup and publish the contract
priority: high
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-5
---

## Goal

Wire the verified `resume --all` path into the Home Manager/Niri lifecycle so it runs after login and after a compositor restart, then expose diagnostics and documentation needed to operate it safely.

## Acceptance Criteria

- When `resume.onStartup` is enabled, the generated startup integration invokes the same `redeem resume --all` systemd oneshot every time Niri starts, not only when the long-lived graphical-session target first activates.
- Preserve the existing Home Manager oneshot, bounded readiness policy, diagnostics, and shared-lock behavior. Do not create a second recovery command or service.
- Periodic capture remains ordered after the initial recovery attempt, and regression coverage proves an empty post-restart Niri observation retains task-2 sticky placement.
- `redeem doctor` reports checkpoint schema and integrity, active versus prior-active candidate counts, resurrection-cache availability, incomplete session identity, and tracked placements that rely on unnamed workspace indices.
- The generated Niri integration and systemd service have evaluation tests, including repeated compositor startup and disabled-startup configurations.
- Update only the recovery delta in README, CLI help, configuration, operations guidance, and the recovery ADR: same-boot versus reboot selection, prior-active allow-list, `--all`, max-age semantics, named-workspace preference, ordering limitations, and create/force-run prohibition. Preserve current mirror pin/apply/follow documentation and never present mirror state as recovery state.
- `go test ./...` and `nix flake check` pass.

## Design Decisions

- Use the existing Home Manager-owned systemd oneshot for logging/retry policy and expose/invoke it from the generated Niri startup integration.
- Automatic startup uses `resume --all`; plain manual resume remains narrower.
- Keep periodic complete capture rather than adding a continuous Niri event subscriber; placement may be stale by at most the configured capture interval.
- Prefer named workspaces and warn, rather than inventing or renaming workspaces automatically.

## Implementation Notes

Depends on task 4. Primary integration is `modules/home-manager/terminal-redeemer.nix`, its evaluation tests, `cmd/redeem/main.go`, doctor/config surfaces, and recovery documentation. Touch the NixOS module only if a shared option or assertion actually requires it; the startup user service is Home Manager-owned.


## Completion Summary

- Reused the Home Manager recovery oneshot for canonical `redeem resume --all` and generated one Niri spawn-at-startup hook that imports the current compositor environment before restarting it.
- Kept periodic capture ordered after startup recovery and added enabled, composed, repeated-start-path, and disabled Nix evaluation coverage.
- Expanded `redeem doctor` with checkpoint integrity/schema, exact current/prior eligibility, exclusion/cache, identity-completeness, and unnamed-workspace diagnostics.
- Published the same-boot/reboot recovery and safety contract while preserving mirror documentation; full Go tests, vet, Nix flake checks, and independent review passed.

### Files Changed

- README.md
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- docs/CONFIG.md
- docs/OPERATIONS.md
- docs/adr/0001-resume-zellij-terminals-in-niri.md
- flake.nix
- internal/capture/runner_test.go
- internal/doctor/checks.go
- internal/doctor/checks_test.go
- internal/zellijlive/catalog.go
- internal/zellijlive/types.go
- modules/home-manager/terminal-redeemer.nix
