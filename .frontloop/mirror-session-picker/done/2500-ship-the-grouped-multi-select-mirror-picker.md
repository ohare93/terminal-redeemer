---
title: Ship the grouped multi-select mirror picker
priority: high
---

## Goal

Replace `mirror open`'s raw numeric prompt with one cohesive Bubble Tea picker change covering interaction, Niri-workspace grouping, responsive rendering, copied Pi semantic styling, `NO_COLOR`, tests, CLI integration, and documentation while preserving deterministic automation flags.

## Acceptance Criteria

- Running `redeem mirror open` without `--all`, `--session`, or `--select` opens the picker instead of printing numbered rows and reading a number from `os.Stdin`; cancellation launches nothing and exits cleanly.
- Up/down and j/k move only among session rows; workspace headings are not focusable; Space toggles the current session; Enter returns all checked sessions in original discovery order, or the current session when none are checked; Ctrl+A toggles all currently filtered sessions.
- Printable input performs case-insensitive substring filtering across session, title, Niri workspace name, and CWD; checked sessions survive filter changes; Backspace edits the query; Esc clears a non-empty query before a subsequent Esc cancels; Ctrl+C cancels.
- Visible sessions are grouped under Niri workspace headings in first-discovery order, with session order preserved inside each group, empty filtered groups hidden, and unnamed workspaces shown under a clear fallback heading.
- One responsive view pipeline provides textual cursor and checkbox markers, ANSI/display-cell-aware aligned columns, a readable narrow-terminal layout, shortened home-directory CWDs, duplicate session/title elision, Unicode-safe truncation, and a viewport that keeps the active row visible without exceeding terminal height.
- Styling uses compact semantic roles `accent`, `success`, `text`, `muted`, `dim`, `warning`, `error`, and `selectedBg` with values copied from Pi's current built-in dark theme; ANSI styling never changes width, truncation, or viewport calculations.
- When `NO_COLOR` is present, the picker emits no colour escapes and remains understandable and aligned through its text markers and labels.
- Existing `--all`, repeatable `--session`, and `--select N` behavior, validation, and launch ordering remain unchanged.
- `README.md` documents navigation, filtering, workspace grouping, Space multi-select, Ctrl+A filtered selection, Enter behavior, Esc clear/cancel behavior, and the unchanged noninteractive flags.
- Focused Bubble Tea model/view and CLI integration checks cover navigation, selection, filtering, grouping/order, cancellation, coloured and `NO_COLOR` responsive rendering, viewport bounds, and noninteractive paths; `go test ./...` passes.

## Design Decisions

- Reuse existing Bubble Tea and `github.com/charmbracelet/x/ansi`; reuse local patterns from `internal/slicetui` without extracting a generic TUI framework or adding fzf, Bubbles, or another dependency.
- Use simple case-insensitive substring filtering, not fuzzy ranking.
- Derive headings from `mirror.Window.WorkspaceName`; unnamed workspaces receive a textual fallback and headings are never selectable.
- Return selections in `mirror.Discover` order regardless of cursor movement or filtering.
- Copy Pi dark values locally for now: accent `#8abeb7`, success `#b5bd68`, text `#d4d4d4`, muted `#808080`, dim `#666666`, warning `#ffff00`, error `#cc6666`, selectedBg `#3a3a4a`; do not add shared-theme resolution or palette configuration.
- Colour supplements rather than replaces textual state.
- Do not add mouse support, preview panes, sort modes, persisted selections, picker configuration, or shared theme infrastructure.

## Implementation Notes

Relevant paths: `cmd/redeem/main.go` (`runMirrorOpen`), `cmd/redeem/main_test.go`, `internal/mirror/remote.go`, `internal/mirror/snapshot.go`, `README.md`, and existing display-cell/viewport patterns in `internal/slicetui` and `internal/tui`. Add only the smallest picker package and chooser injection seam needed to keep terminal interaction testable. This task supersedes both clarify tasks in this epic; they were moved out of ready before implementation following the Grok 4.5 Ponytail review.


## Completion Summary

- Replaced the numeric stdin prompt with a grouped Bubble Tea multi-select picker and clean cancellation path.
- Added discovery-order selection, workspace grouping, case-insensitive filtering, keyboard controls, responsive ANSI/display-cell-safe rendering, Pi dark semantic colours, and NO_COLOR support.
- Preserved and tested --all, repeatable --session, and --select behavior and ordering; documented picker controls.
- Independently verified focused picker/CLI tests and the full Go suite: 803 tests passed across 37 packages.

### Files Changed

- README.md
- cmd/redeem/main.go
- cmd/redeem/main_test.go
- internal/mirrortui/app.go
- internal/mirrortui/model.go
- internal/mirrortui/model_test.go
