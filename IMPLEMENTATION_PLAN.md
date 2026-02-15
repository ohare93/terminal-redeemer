# terminal-redeemer Implementation Plan (TDD, Red/Green/Refactor)

## 1) Delivery Strategy

This plan implements `terminal-redeemer` in thin vertical slices. Every slice follows strict TDD:

1. **Red**: write a failing test for one observable behavior.
2. **Green**: implement the minimum code to pass the test.
3. **Refactor**: improve design while keeping tests green.

Rules for all phases:
- No feature code without a failing test first.
- Keep commits small and behavior-focused.
- Prefer deterministic unit tests; isolate integration tests behind fixtures.
- Maintain a passing `go test ./...` at all times before moving to next slice.

## 2) Definition of Done (Global)

- All acceptance criteria from the slice are covered by automated tests.
- Unit + integration test suites pass locally.
- CLI behavior documented in `README.md` and `docs/CONFIG.md`.
- No TODO stubs in merged code paths.
- Refactor pass completed (naming, duplication removal, interfaces cleaned).

## 3) Testing Pyramid

- **Unit tests (~70%)**: diffing, replay, parsing, retention, config validation.
- **Integration tests (~25%)**: event log + snapshot + replay with fixtures.
- **E2E smoke (~5%)**: CLI commands against temporary state dirs.

Tooling:
- `go test ./...`
- `go test -race ./...` for concurrency-sensitive packages.
- Golden fixtures for Niri JSON and replay results.

## 4) Iteration 0: Project Bootstrap

Goal: establish build/test/package scaffolding.

### Red
- Add tests that assert:
  - binary starts and prints help (`redeem --help`).
  - config loader returns sensible defaults (non-personal).
  - state directory resolver uses `~/.terminal-redeemer` if unset.

### Green
- Create repo skeleton:
  - `cmd/redeem/main.go`
  - `internal/config`
  - minimal CLI command tree.
- Add `flake.nix` package and app outputs.
- Add Home Manager module skeleton with capture timer only.

### Refactor
- Introduce internal package boundaries (`config`, `cli`, `paths`).
- Normalize error handling and exit codes.

Acceptance:
- `redeem --help` works.
- `go test ./...` passes.
- flake evaluates for package + app + HM module outputs.

## 5) Iteration 1: Event Store Core (JSONL + Snapshots)

Goal: append-only storage and snapshot persistence.

### Red
- Failing tests for:
  - appending valid events in order.
  - rejecting malformed events.
  - replay cursor tracking.
  - snapshot write/read round-trip.
  - lock behavior preventing concurrent writers.

### Green
- Implement `internal/events`:
  - event struct + validation.
  - JSONL append/read.
  - lock file management.
- Implement `internal/snapshots`:
  - write snapshot every N events.
  - load nearest snapshot <= timestamp.

### Refactor
- Extract storage interfaces for easier mocking.
- Consolidate serialization and validation utilities.

Acceptance:
- Event append/replay deterministic with fixtures.
- Snapshot cadence works (`snapshotEvery`).

## 6) Iteration 2: State Model + Sparse Diff Engine

Goal: compute sparse patch events from normalized state.

### Red
- Tests for:
  - unchanged state emits no events.
  - single-field change emits one sparse patch.
  - optional-field add/remove behavior.
  - workspace/window ordering invariants.

### Green
- Implement `internal/model` normalized state.
- Implement `internal/diff` producing sparse patches.

### Refactor
- Remove duplicate field comparison logic via generic patch helpers.
- Improve test readability with builders/fixtures.

Acceptance:
- Diff output minimal and schema-compliant.
- Hash-based change detection is stable.

## 7) Iteration 3: Collector (Niri + Terminal Metadata)

Goal: periodic state collection with enrichment.

### Red
- Tests for:
  - Niri fixture parsing into normalized model.
  - cwd extraction behavior.
  - process whitelist capture (`opencode`, `claude`, plus extras).
  - session tag extraction best-effort behavior.

### Green
- Implement `internal/niri` adapters.
- Implement `internal/procmeta` with whitelist and session tag extraction.
- Implement collector orchestration returning full normalized state.

### Refactor
- Introduce adapter interfaces for process and IPC readers.
- Isolate OS-specific code paths for future portability.

Acceptance:
- Collector returns expected model from fixtures.
- Missing metadata degrades gracefully, never crashes capture.

## 8) Iteration 4: Capture Pipeline Command

Goal: `redeem capture once` and `redeem capture run` end-to-end.

### Red
- Tests for:
  - `capture once` writes events on change only.
  - `capture run` loops at configured interval.
  - snapshot emitted every N events.
  - recoverable errors logged and loop continues.

### Green
- Wire collector + diff + event store into CLI capture commands.
- Add structured logging and summary counts.

### Refactor
- Split loop control, business logic, and output formatting.
- Add context cancellation and graceful shutdown.

Acceptance:
- Running capture in temp env produces deterministic log timeline.
- Timer-driven capture suitable for HM service.

## 9) Iteration 5: Replay Engine (Timestamp Reconstruction)

Goal: reconstruct state at arbitrary timestamp quickly.

### Red
- Tests for:
  - replay from empty log.
  - replay with snapshot + tail events.
  - corrupted line skip behavior.
  - exact timestamp boundary semantics.

### Green
- Implement `internal/replay` to materialize state at `--at`.
- Add `redeem history list` and `redeem history inspect --at`.

### Refactor
- Optimize replay path and reduce allocations.
- Add replay profiling benchmarks for large logs.

Acceptance:
- Any timestamp resolve is correct and deterministic.

## 10) Iteration 6: Restore Planner + Executor (Non-TUI)

Goal: reliable additive restore with summary reporting.

### Red
- Tests for:
  - plan status classification (`ready/skipped/degraded`).
  - terminal restore skips missing metadata.
  - app restore obeys allowlist only.
  - continue-on-failure summary behavior.

### Green
- Implement `internal/restore/plan` and `internal/restore/exec`.
- Add `redeem restore apply --at <timestamp> [--yes]`.
- Implement kitty+zellij attach-or-create strategy.

### Refactor
- Separate command templating from execution logic.
- Consolidate retry/error aggregation helpers.

Acceptance:
- Additive restore works without killing existing windows.
- Exit code policy matches spec (fatal init only).

## 11) Iteration 7: Bubble Tea Restore TUI

Goal: interactive restore selection UX.

### Red
- Model-level tests for:
  - timestamp selection.
  - item toggling by workspace/window.
  - action preview generation.
- Integration tests for mapping TUI selections to restore plan.

### Green
- Implement `redeem restore tui` with Bubble Tea.
- Provide:
  - timeline selector
  - workspace/app/window tree views
  - include/exclude toggles
  - apply confirmation

### Refactor
- Extract reusable UI components and keymap config.
- Improve rendering performance and state updates.

Acceptance:
- User can pick timestamp, edit selection, and apply restore from TUI.

## 12) Iteration 8: Retention + Pruning

Goal: enforce 30-day retention by default.

### Red
- Tests for:
  - age-based pruning of events and snapshots.
  - safety around active files.
  - no data loss for current replay window.

### Green
- Implement `internal/prune`.
- Add `redeem prune run` command.
- Integrate optional prune in capture loop cadence.

### Refactor
- Clean prune policy interfaces for future size-cap mode.

Acceptance:
- Old data pruned safely; current state remains reconstructable.

## 13) Iteration 9: Home Manager Module Integration

Goal: production-ready install + service management.

### Red
- Nix tests/evaluations for:
  - module option schema.
  - generated user service/timer units.
  - config file rendering from options.

### Green
- Implement HM module:
  - capture timer enabled path.
  - configurable state dir/profile/interval/snapshotEvery/retention.
  - process whitelist and app allowlist config wiring.

### Refactor
- Reduce module duplication; improve option docs.
- Ensure generated unit naming and descriptions are consistent.

Acceptance:
- `homeManagerModules.terminal-redeemer` works with capture timer only by default.

## 14) Iteration 10: Hardening, Docs, and Operational Playbook

Goal: make V1 robust and operator-friendly.

### Red
- Add regression tests for all known bug classes discovered in earlier phases.

### Green
- Finalize docs:
  - `README.md`
  - `docs/CONFIG.md`
  - `docs/OPERATIONS.md`
  - troubleshooting and recovery workflows.

### Refactor
- package-level API cleanup.
- remove dead code and tighten lint/static checks.

Acceptance:
- New user can install, capture, inspect history, and restore via TUI from docs only.

## 15) CI and Quality Gates

Per PR:
- `go test ./...`
- `go test -race ./...`
- flake eval for package/app/module outputs.
- format/lint checks.

Merge blocked if:
- any failing tests.
- uncovered acceptance criteria in current slice.

## 16) Suggested Commit Rhythm (Red/Green/Refactor)

For each feature slice:
1. `test: add failing coverage for <behavior>`
2. `feat: minimal implementation for <behavior>`
3. `refactor: clean <area> with tests green`

This keeps history aligned with TDD discipline and simplifies rollback/review.

## 17) Risk Register and Mitigations

- **Niri schema drift**
  - mitigate with adapter layer + fixture updates.
- **Process metadata fragility**
  - keep best-effort semantics and explicit skip paths.
- **Log growth/perf**
  - snapshot cadence + retention + replay benchmarks.
- **Restore side effects**
  - additive-only mode and explicit allowlists.

## 18) V2 Entry Criteria (Bottles)

Do not start V2 until V1 has:
- stable replay correctness.
- robust TUI restore UX.
- clean module packaging.
- no open P1 bugs for capture/restore data integrity.

Then begin mutable bottles as timeline pointers:
- `redeem bottle create/use/update/open`.
