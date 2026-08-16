---
title: Make visible Zellij session identity exact and complete
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-1
---

## Goal

Repair the observation boundary so capture, mirror inventory, and resume identify Kitty windows launched with the real `zellij attach -- <session>` argv even when their title remains `zellij`. Reuse the existing bounded descendant-process evidence instead of adding another title or launch-order heuristic.

## Acceptance Criteria

- A Kitty process with an exact live descendant argv `zellij attach -- <session>` is associated with that case-sensitive session even when no session appears in the window title.
- Session identity is rejected when the Kitty root cannot be verified, descendant observation is incomplete, or multiple distinct session candidates make the window ambiguous.
- Capture and mirror use the same exact session-evidence semantics; title parsing remains only a verified fallback and cannot invent a missing session.
- Regression tests cover the literal separator form, ambiguous descendants, disappearing process metadata, title-only fallback, and the currently observed `title=zellij` case.
- `go test ./internal/procmeta ./internal/zellijlive ./internal/collector ./internal/mirror` passes.

## Design Decisions

- Reuse `internal/zellijlive.ProcObserver` or its existing parser/traversal contract rather than teaching multiple independent parsers.
- Keep exact session matching, bounded `/proc` traversal, and attach-only safety; do not infer identity from app ID, PID proximity, or creation order.

## Implementation Notes

First task because every later recovery invariant depends on complete session identity. Relevant files: `internal/procmeta/*`, `internal/zellijlive/*`, `internal/collector/collector.go`, `internal/mirror/snapshot.go` and their tests.
