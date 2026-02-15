# terminal-redeemer Milestones (V1)

This is a practical execution tracker aligned to `IMPLEMENTATION_PLAN.md`.

## Milestone 0: Bootstrap

- Scope: repo skeleton, CLI stub (`redeem --help`), flake outputs, HM module skeleton.
- Exit criteria: build/test/package scaffolding passes.
- Estimate: 0.5-1 day.

## Milestone 1: Event Store Foundation

- Scope: JSONL append/read, validation, snapshot write/read, locking.
- Exit criteria: deterministic event/snapshot integration tests green.
- Estimate: 1-2 days.

## Milestone 2: State + Diff

- Scope: normalized state model, sparse patch diff, hash-based change detect.
- Exit criteria: no-change emits nothing; sparse patch semantics verified.
- Estimate: 1-1.5 days.

## Milestone 3: Collector

- Scope: Niri adapters + terminal metadata enrichment (cwd, whitelist process tags, session tag).
- Exit criteria: fixture-driven collector tests pass with graceful degradation cases.
- Estimate: 1.5-2.5 days.

## Milestone 4: Capture Commands

- Scope: `redeem capture once` and `redeem capture run` wired end-to-end.
- Exit criteria: writes only on change, snapshot cadence honored, recoverable errors tolerated.
- Estimate: 1-1.5 days.

## Milestone 5: Replay + History CLI

- Scope: replay by timestamp, `history list`, `history inspect --at`.
- Exit criteria: accurate reconstruction from snapshot+tail and boundary timestamps.
- Estimate: 1-2 days.

## Milestone 6: Restore Apply (Non-TUI)

- Scope: restore planner/executor, additive mode, allowlist rules, kitty+zellij strategy.
- Exit criteria: restore summary correctness and continue-on-failure behavior verified.
- Estimate: 1.5-2.5 days.

## Milestone 7: Restore TUI (Bubble Tea)

- Scope: timestamp picker, workspace/app/window selection, preview + confirm apply.
- Exit criteria: full interactive flow maps correctly to restore plan.
- Estimate: 2-3 days.

## Milestone 8: Retention/Prune

- Scope: age-based pruning for events/snapshots, `redeem prune run`.
- Exit criteria: 30-day policy enforced without breaking reconstructability.
- Estimate: 0.5-1 day.

## Milestone 9: Home Manager Integration

- Scope: production HM module options, config rendering, user service/timer generation.
- Exit criteria: module evaluates cleanly; capture timer enabled by default path works.
- Estimate: 1-2 days.

## Milestone 10: Hardening + Docs

- Scope: regression tests, operational docs, troubleshooting, cleanup/refactors.
- Exit criteria: fresh user can install and use from docs alone.
- Estimate: 1-2 days.

## Total Estimate

- Optimistic: ~11 days.
- Likely: ~14-16 days.
- Conservative (with polish/unknowns): ~20 days.

## Tracking Format

Use this status legend in PRs/issues:
- `not_started`
- `in_progress`
- `blocked`
- `done`

Recommended branch sequence:
1. `feat/bootstrap`
2. `feat/event-store`
3. `feat/diff-engine`
4. `feat/collector`
5. `feat/capture-cli`
6. `feat/replay-history`
7. `feat/restore-apply`
8. `feat/restore-tui`
9. `feat/prune`
10. `feat/hm-module`
11. `feat/hardening-docs`
