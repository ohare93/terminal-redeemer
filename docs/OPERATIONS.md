# Operations

## Service Setup (Home Manager)

- Enable `programs.terminal-redeemer.enable = true`.
- Keep `capture.enable = true` for timer-managed capture.
- Verify unit/timer:
  - `systemctl --user status terminal-redeemer-capture.service`
  - `systemctl --user status terminal-redeemer-capture.timer`

## Service Setup (NixOS)

- Import both modules in your NixOS flake/module list:
  - `home-manager.nixosModules.home-manager`
  - `terminal-redeemer.nixosModules.terminal-redeemer`
- Enable and configure per-user settings under `programs.terminal-redeemer.users.<name>`.
- The NixOS wrapper forwards each user block to Home Manager and writes:
  - `~/.config/terminal-redeemer/config.yaml`
  - user `systemd` capture/prune services and timers.

## Capture Troubleshooting

- Run manual capture once:
  - `redeem capture once --state-dir ~/.terminal-redeemer --niri-cmd 'niri msg -j windows'`
- Check output for `events_written=...`.
- If command mode fails, run `redeem doctor` and check `niri_source`.
- If fixture mode is intended, verify `REDEEM_NIRI_FIXTURE` points to readable valid JSON.
- If both `--fixture` and `--niri-cmd` are empty, capture exits with usage error.

## Replay and Restore Troubleshooting

- List timeline:
  - `redeem history list --state-dir ~/.terminal-redeemer`
- Inspect state at timestamp:
  - `redeem history inspect --state-dir ~/.terminal-redeemer --at <RFC3339>`
- Preview restore plan:
  - `redeem restore apply --state-dir ~/.terminal-redeemer --at <RFC3339>`
- Interactive restore:
  - `redeem restore tui --state-dir ~/.terminal-redeemer`

Restore output behavior:

- `restore apply` preview (no `--yes`) prints:
  - `restore_plan ready=<n> skipped=<n> degraded=<n>`
  - `pass --yes to execute`
- `restore apply --yes` and confirmed `restore tui` print:
  - `restore_item ...` for `skipped`, `degraded`, and `failed` items
  - `restore_summary restored=<n> skipped=<n> failed=<n>`
- `restore tui` cancellation prints `restore cancelled`.
- `--at` is required for `history inspect` and `restore apply`.

## Retention and Pruning

- Run prune:
  - `redeem prune run --state-dir ~/.terminal-redeemer --days 30`
- Successful prune prints `prune_summary events_pruned=<n> snapshots_pruned=<n>`.
- If prune reports `active writer lock present`, stop capture and retry.

Prune retention behavior:

- Events older than cutoff are pruned, but one pre-cutoff anchor event is retained for replay continuity.
- Snapshots keep newest overall and newest snapshot at/before cutoff; older redundant snapshots are removed.

## Doctor

- Run checks:
  - `redeem doctor`
- Output format:
  - `doctor_check name=<check> status=<pass|fail> detail=<text>`
  - `doctor_summary total=<n> passed=<n> failed=<n>`
- Current checks: `state_dir_writable`, `config_load`, `niri_source`, `kitty_available`, `zellij_available`, `events_integrity`, `snapshots_integrity`.

## Integrity and Recovery

- Replay skips malformed lines and continues with valid events.
- Snapshots are optional optimization; replay works from events alone.
- Keep regular backups of `events.jsonl` and `snapshots/` for disaster recovery.

## Quick Troubleshooting Matrix

- `config load failed: ...` on most commands: fix YAML or path; run `redeem --config <path> doctor` to see `config_load` detail.
- `invalid --at`: pass RFC3339/RFC3339Nano timestamp (example: `2026-02-15T10:00:00Z`).
- `history list` returns nothing: verify `--state-dir`, and ensure at least one successful capture wrote `events.jsonl`.
- restore mostly skipped: inspect `restore.appAllowlist` and terminal metadata availability via `history inspect`.
- prune does nothing: verify retention window (`--days`) and event/snapshot timestamps are older than cutoff.
