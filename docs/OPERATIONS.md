# Operations

## Service Setup (Home Manager)

- Enable `programs.terminal-redeemer.enable = true`.
- Keep `capture.enable = true` for timer-managed capture.
- Verify unit/timer:
  - `systemctl --user status terminal-redeemer-capture.service`
  - `systemctl --user status terminal-redeemer-capture.timer`

## Capture Troubleshooting

- Run manual capture once:
  - `redeem capture once --state-dir ~/.terminal-redeemer --niri-cmd 'niri msg -j workspaces windows'`
- Check output for `events_written=...`.
- If Niri command fails, test command directly and inspect stderr.

## Replay and Restore Troubleshooting

- List timeline:
  - `redeem history list --state-dir ~/.terminal-redeemer`
- Inspect state at timestamp:
  - `redeem history inspect --state-dir ~/.terminal-redeemer --at <RFC3339>`
- Preview restore plan:
  - `redeem restore apply --state-dir ~/.terminal-redeemer --at <RFC3339>`
- Interactive restore:
  - `redeem restore tui --state-dir ~/.terminal-redeemer`

## Retention and Pruning

- Run prune:
  - `redeem prune run --state-dir ~/.terminal-redeemer --days 30`
- If prune reports active lock, ensure capture is not writing concurrently.

## Integrity and Recovery

- Replay skips malformed lines and continues with valid events.
- Snapshots are optional optimization; replay works from events alone.
- Keep regular backups of `events.jsonl` and `snapshots/` for disaster recovery.
