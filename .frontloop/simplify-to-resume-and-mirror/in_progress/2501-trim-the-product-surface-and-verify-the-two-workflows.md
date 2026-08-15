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
