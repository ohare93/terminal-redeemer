---
title: Trim the product surface and verify the two workflows
priority: high
frontloop_approval_task: bb1f5140ea3f97ba3060ab67219d59f37ae4035c722e0e1536b1f3d88d757f10-4
---

## Goal

Make CLI, configuration, documentation, and packaging describe only local restoration and Lattice session access, with final end-to-end evidence suitable for user-owned deployment.

## Acceptance Criteria

- README and operational documentation lead with the four shortcuts/workflows: local terminal, new Lattice terminal, browse/reopen Lattice session, resume after reboot
- CLI help and configuration contain no removed slice, historical restore, or arbitrary app-restore surface
- Doctor checks only dependencies required by capture/resume/mirror and remains read-only
- Repository tests, packaged binary tests, consumer configuration build, and `nix flake check 'path:.' --no-build` pass
- A concise user-owned physical smoke checklist covers Overton local launch, Lattice remote creation/reopen, detach persistence, and reboot resume
- Final LOC/command/config reduction is measured and reported

## Design Decisions

- Physical activation remains user-owned
- Do not add a new umbrella daemon or compatibility layer
- Document source-host visible Kitty as explicitly out of scope

## Implementation Notes

This is the final convergence and acceptance task; remove stale names and examples found by repository-wide search.


## Completion Summary

- Converged README, operations, configuration, CLI help, modules, and packaging on local terminal, new Lattice terminal, Lattice picker/reopen, and reboot resume
- Documented that remote creation runs on Lattice without opening a visible source-host Kitty
- Removed unused capture-loop, unsupported mirror mode, raw extra config, scaffold command, and duplicate doctor surface
- Kept resilient periodic capture through the Home Manager systemd timer and read-only required-dependency doctor checks
- Added packaged installed-binary and Home Manager/NixOS binding checks plus a user-owned physical smoke checklist
- Verified full Go tests/race/vet, full Nix flake check, packaged CLI, and consumer module builds
- Measured 44,652 to 11,336 total Go LOC, 34 to 16 packages, 79 to 37 YAML fields, and 42 to 11 supported CLI leaves from revision 25bbcd76ef2d

### Files Changed

- README.md
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- docs/CONFIG.md
- docs/OPERATIONS.md
- docs/adr/0001-resume-zellij-terminals-in-niri.md
- flake.nix
- internal/capture/runner.go
- internal/capture/runner_test.go
- internal/config/config.go
- internal/config/config_test.go
- internal/doctor/checks.go
- internal/doctor/checks_test.go
- internal/mirror/orchestration_test.go
- internal/mirror/windows.go
- internal/model/state.go
- internal/resume/executor.go
- modules/home-manager/terminal-redeemer.nix
