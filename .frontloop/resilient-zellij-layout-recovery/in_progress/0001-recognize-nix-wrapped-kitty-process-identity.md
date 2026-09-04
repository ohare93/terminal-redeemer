---
title: Recognize Nix-wrapped Kitty process identity
priority: critical
---

## Goal

Allow exact session evidence collection from the deployed Nix Kitty wrapper without weakening executable verification.

## Acceptance Criteria

- Kitty process verification accepts the exact basename `.kitty-wrapped` from `/proc/<pid>/exe` or comm in addition to existing `kitty` and `kitty.bin` names.
- No suffix, argv-only, title-only, or arbitrary wrapper matching is introduced.
- Regression coverage proves a complete `.kitty-wrapped` process tree with exact `zellij attach -- <session>` evidence is verified and a near-match remains rejected.
- Focused procmeta/capture tests and `go test ./...` pass.

## Design Decisions

- Fix Redeem rather than altering the Nix Kitty wrapper.
- Use one exact basename addition; do not generalize wrapper discovery.

## Implementation Notes

Root cause reproduced live: all visible Kitty processes have exe/comm `.kitty-wrapped`; 17 observed trees already contain exact `zellij attach -- <name>` argv and should become position-trackable after deployment and capture.
