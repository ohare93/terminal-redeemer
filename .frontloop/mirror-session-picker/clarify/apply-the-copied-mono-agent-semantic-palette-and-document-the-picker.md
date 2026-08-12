---
title: Apply the copied Mono/Agent semantic palette and document the picker
priority: medium
frontloop_approval_task: 3b48c2390b4f6a0533f0b70b6893153cd339b9aed23d48a7e45b7f6e427bffbc-2
---

## Goal

Give the picker the same semantic colour vocabulary and current dark values used by Mono/Agent's Pi UI, without introducing shared-theme configuration yet, and update operator-facing documentation for the new interaction.

## Acceptance Criteria

- Picker styling is expressed through the semantic roles `accent`, `success`, `text`, `muted`, `dim`, `warning`, `error`, and `selectedBg`, using copied values from Pi's current built-in dark theme rather than scattered per-element colour literals.
- The cursor/current row, checked marker, workspace headings, primary text, secondary context, help/count text, empty-result warning, and errors use consistent semantic roles; cursor and checkbox text remain sufficient when colour is unavailable.
- When `NO_COLOR` is present, the picker emits no colour escape sequences and remains fully understandable and aligned.
- ANSI styling does not affect display-cell width calculations, truncation, viewport height, or responsive layouts; focused tests cover coloured and `NO_COLOR` rendering.
- `README.md` describes arrow/j/k navigation, filtering, workspace grouping, Space multi-select, Ctrl+A filtered selection, Enter behavior, Esc clear/cancel behavior, and the unchanged noninteractive flags.
- No shared-theme loader, theme-path option, palette configuration, runtime Mono/Agent dependency, or new picker feature is added; `go test ./...` passes.

## Design Decisions

- Per the user's decision, copy the active Pi dark semantic values for now; exact shared-file resolution is deferred.
- Reuse an already-installed Charmbracelet styling facility if it makes the implementation smaller; do not add a new styling dependency solely for this picker.
- Colour is supplementary: every state retains a textual cursor, checkbox, label, or message.
- Documentation should replace the old implication that the interactive chooser expects a numeric response.

## Implementation Notes

Depends on the grouped picker task. Source palette reference reviewed during planning: Pi built-in `dark.json` uses accent `#8abeb7`, success `#b5bd68`, text `#d4d4d4`, muted `#808080`, dim `#666666`, warning `#ffff00`, error `#cc6666`, and selectedBg `#3a3a4a`. Keep the copied mapping local and compact so replacing it with shared theme resolution later is straightforward only when actually requested.


## Blocked

Superseded before implementation by the user-approved Ponytail merge: palette, NO_COLOR, and README changes belong in the same picker task and should not force a second rendering/test pass.
