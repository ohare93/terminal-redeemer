---
title: Remove the host-leech slice product
priority: critical
frontloop_approval_task: bb1f5140ea3f97ba3060ab67219d59f37ae4035c722e0e1536b1f3d88d757f10-2
---

## Goal

Delete the distributed slice controller/RPC/selection/routed-launch product after the mirror workflow replaces its useful user-facing behavior.

## Acceptance Criteria

- All slice CLI commands, services, config keys, controller/RPC/protocol/inventory/layout/TUI code, dedicated acceptance/soak/consumer-contract tests, generated host-leech contract, and slice-specific documentation are removed
- No retained code or generated binding references `redeem slice`
- Mirror, capture, resume, prune, doctor, and owned-window safety remain functional
- Dependencies used only by slice are removed from go.mod/go.sum and Nix packaging inputs
- Existing slice state is not destructively deleted by migration code
- `go test ./...`, package build, and `nix flake check 'path:.' --no-build` pass

## Design Decisions

- Delete slice rather than retaining a smaller controller or compatibility command
- Do not add migration aliases for removed slice commands
- Rollback is by prior Jujutsu revision; old state directories may remain orphaned

## Implementation Notes

Likely deletion surface: internal/slice*, internal/sourceinventory, internal/zellijlive unless reused by minimal mirror discovery, internal/niriipc if unused, internal/subprocessacceptance, internal/hostleechsoak, internal/consumercontract, contracts/host-leech-slices, and slice ADR/readiness/protocol docs. Verify every package caller before deletion.


## Completion Summary

- Deleted the host/leech slice CLI, controller/RPC/protocol/inventory/layout/TUI implementation, services, configuration, contracts, tests, scripts, and dedicated docs
- Kept mirror-required active-only Zellij catalog, capture/resume/prune/doctor, and exact owned-window close safety
- Replaced removed generated contract with direct Home Manager local Kitty, mirror-new, and mirror-picker bindings
- Removed stale package/config dependencies and verified no retained `redeem slice` or host/leech product references
- Passed full Go tests/vet/race checks, package and module builds, and full Nix flake checks

### Files Changed

- README.md
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- docs/CONFIG.md
- docs/OPERATIONS.md
- flake.nix
- go.mod
- internal/config/config.go
- internal/config/config_test.go
- internal/mirror/snapshot_test.go
- modules/home-manager/terminal-redeemer.nix
- contracts/host-leech-slices/ (deleted)
- internal/slice*/ (deleted)
- internal/sourceinventory/ (deleted)
- internal/niriipc/ (deleted)
- internal/subprocessacceptance/ (deleted)
- internal/consumercontract/ (deleted)
- internal/hostleechsoak/ (deleted)
- slice-specific docs and scripts (deleted)
