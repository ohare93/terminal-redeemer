---
title: Build and integrate the grouped multi-select mirror picker
priority: high
frontloop_approval_task: 3b48c2390b4f6a0533f0b70b6893153cd339b9aed23d48a7e45b7f6e427bffbc-1
---

## Goal

Replace the no-flag numeric selection branch of `mirror open` with a small Bubble Tea picker that selects one or more discovered sessions while preserving all existing noninteractive selection paths. Keep selection, filtering, grouping, and rendering inside the minimum package boundary needed for direct model tests and CLI injection.

## Acceptance Criteria

- Running `redeem mirror open` without `--all`, `--session`, or `--select` opens the interactive picker instead of printing numbered rows and reading a number from `os.Stdin`; cancellation launches nothing and exits cleanly.
- Up/down and j/k move only among session rows; workspace headings are not focusable; Space toggles the current session; Enter returns all checked sessions in original discovery order, or the current session when none are checked; Ctrl+A toggles all currently filtered sessions.
- Printable input applies case-insensitive substring filtering across session name, title, Niri workspace name, and CWD; checked sessions survive filter changes; Backspace edits the query; Esc clears a non-empty query before a subsequent Esc cancels; Ctrl+C cancels.
- Visible sessions are grouped under Niri workspace headings in first-discovery order, with session order preserved inside each group and a clear fallback heading for unnamed workspaces; groups with no filtered matches are hidden.
- The view shows cursor and checkbox text markers, selected/visible/total counts, aligned display-cell-aware session and context columns at normal widths, a readable narrow-terminal layout, shortened home-directory CWDs, and no redundant title text when it merely repeats the session name.
- The viewport keeps the active session visible and never exceeds the reported terminal height; long Unicode session, workspace, title, and CWD values are truncated without breaking column alignment.
- Existing `--all`, repeatable `--session`, and `--select N` behavior and launch ordering remain unchanged.
- Focused Bubble Tea model/view tests and CLI integration tests cover navigation, multi-selection, filter persistence, group visibility/order, cancellation, responsive rendering, viewport bounds, and preservation of noninteractive paths; `go test ./...` passes.

## Design Decisions

- Reuse the existing Bubble Tea and `github.com/charmbracelet/x/ansi` dependencies; do not add fzf, Bubbles widgets, or a second TUI framework.
- Use simple case-insensitive substring filtering, not fuzzy ranking.
- Headings are derived from `mirror.Window.WorkspaceName`; unnamed workspaces use a textual fallback rather than becoming selectable rows.
- Selections are returned in `mirror.Discover` order, independent of cursor movement or filtering.
- Do not add mouse support, preview panes, sort modes, picker configuration, or persisted selections.

## Implementation Notes

Relevant paths: `cmd/redeem/main.go` (`runMirrorOpen`), `cmd/redeem/main_test.go`, `internal/mirror/remote.go`, `internal/mirror/snapshot.go`, and the existing rendering/viewport examples in `internal/tui` and `internal/slicetui`. Introduce only the smallest picker package or injected chooser seam needed to keep terminal interaction out of CLI logic and tests. This task precedes the semantic styling/documentation task.


## Blocked

Superseded before implementation by the user-approved Ponytail merge: interaction, rendering, semantic palette, NO_COLOR, tests, and documentation should ship as one task to avoid duplicate render and validation passes.
