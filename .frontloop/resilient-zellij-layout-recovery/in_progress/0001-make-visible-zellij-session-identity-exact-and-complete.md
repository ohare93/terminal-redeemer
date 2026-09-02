---
title: Make visible Zellij session identity exact and complete
priority: critical
frontloop_approval_task: c515dd1646daffafafffd4524e70c79af6d10509b2d85d51e35ba565116ff15b-1
---

## Goal

Repair the observation boundary so capture, mirror inventory, and resume identify Kitty windows launched with the real `zellij attach -- <session>` argv even when their title remains `zellij`. Reuse the existing bounded descendant-process evidence instead of adding another title or launch-order heuristic.

## Acceptance Criteria

- A Kitty process with an exact live descendant argv `zellij attach -- <session>` is associated with that case-sensitive session even when no session appears in the window title.
- Session evidence distinguishes one exact candidate, multiple distinct candidates, and incomplete observation; identity is rejected when the Kitty root cannot be verified, descendant observation is incomplete, or candidates are ambiguous.
- Capture and mirror consume the same evidence API; title parsing remains only a corroborated fallback and cannot invent a missing session.
- Regression tests extend the existing process-tree and resume-probe coverage for literal `zellij attach -- <session>`, multiple distinct candidates, root replacement, unreadable or disappearing descendants, title-only fallback, and `title=zellij`; no second attach parser is introduced.
- `go test ./internal/procmeta ./internal/zellijlive ./internal/collector ./internal/mirror` passes.

## Design Decisions

- Extend the existing `procmeta.DescendantArgvMatchContext` traversal rather than adding another process walker or parser; it already validates root and descendant PID/start-time identity and is used by resume attachment verification.
- Add a complete evidence result capable of distinguishing one exact candidate, multiple candidates, and incomplete observation; the current boolean matcher cannot prove uniqueness.
- Make collector and mirror consume the same evidence API. Keep title parsing only as corroborated fallback; it must not manufacture a session when process evidence is absent or incomplete.

## Implementation Notes

First because later inventory depends on complete identity. Start at `internal/procmeta/process_tree.go` and the existing verifier/enricher call sites in collector and mirror. Do not add active-session inventory here: `internal/mirror/snapshot.go` already obtains authoritative active names through `zellijlive.Cataloger`.
