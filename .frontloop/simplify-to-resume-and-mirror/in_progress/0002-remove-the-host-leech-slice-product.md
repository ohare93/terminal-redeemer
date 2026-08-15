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
