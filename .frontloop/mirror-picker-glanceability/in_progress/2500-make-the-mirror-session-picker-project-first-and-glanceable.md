---
title: Make the mirror session picker project-first and glanceable
priority: high
frontloop_approval_task: c08eb29f09b37dee04fa585232c4db1ccf8efd503aeb9b474aa9d5d089f534f5-1
---

## Goal

Rework the picker’s presentation so project/directory and cleaned activity are the primary scan targets, with generated session names retained as secondary metadata. Keep the change within the existing TUI model and avoid adding dependencies or new interaction modes.

## Acceptance Criteria

- At wide terminal widths, rows have headings and display project/directory first, cleaned activity second, and session name last.
- Project labels are derived from existing CWD data, and truncated paths preserve their distinguishing tail rather than hiding the final directory.
- A leading session-name prefix is removed from the displayed title/activity when redundant, without changing the underlying title or filter data.
- The sole `Workspace: (unnamed)` heading is omitted; meaningful workspace grouping remains available when it distinguishes rows.
- The header presents selected and matching totals clearly, and the footer uses concise action labels.
- Existing keyboard navigation, filtering across session/title/workspace/CWD, filtered multi-select, Enter behavior, color/NO_COLOR behavior, and narrow responsive rendering continue to work.
- Focused, checked, empty, filtered-empty, wide, and narrow render states have concise regression coverage, and the mirrortui Go tests pass.

## Design Decisions

- Prefer a single focused implementation task rather than splitting visual cleanup into speculative phases.
- Reuse the current Bubble Tea model and existing captured Window fields; do not add dependencies or collect new recency data.
- Treat generated session names as secondary metadata, not the primary row label.
- Preserve the existing narrow stacked layout as the responsive fallback.

## Implementation Notes

Primary paths: internal/mirrortui/model.go and internal/mirrortui/model_test.go. Current data already includes Title, Terminal.CWD, WorkspaceName, IsFocused, and ZellijSession. Keep title cleanup conservative, such as stripping only an exact leading session plus known separator.
