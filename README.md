# terminal-redeemer

`terminal-redeemer` provides a rewindable timeline for terminal and window session restore on Niri.

CLI command: `redeem`

## Status

Current CLI behavior is implemented and covered by tests:

- capture (`once`, `run`)
- history (`list`, `inspect`)
- restore (`apply`, `tui`)
- prune (`run`)
- doctor (`doctor`)
- Home Manager module scaffolding and eval checks

## Quick Start

### Try CLI

```bash
redeem --help
```

### Capture once (fixture)

```bash
redeem capture once \
  --fixture ./testdata/niri-snapshot.json
```

### Capture once (live command)

```bash
redeem capture once \
  --niri-cmd 'niri msg -j windows'
```

### Inspect and restore

```bash
redeem history list
redeem history inspect --at 10m
redeem restore tui
redeem restore apply --at 10m --dry-run
redeem restore apply --at 10m --yes
```

`restore apply` behavior:

- Without `--yes`, it prints a preview summary and exits without executing commands:
  - `restore_plan ready=<n> skipped=<n> degraded=<n>`
  - `pass --yes to execute`
- With `--yes`, it executes ready items and prints:
  - `restore_item ...` lines only for non-ready outcomes (`skipped`, `degraded`, `failed`)
  - `restore_summary restored=<n> skipped=<n> failed=<n>`
- `--at` is required.

`restore tui` behavior:

- Starts interactive selection over timestamps and plan items.
- If cancelled, prints `restore cancelled`.
- If confirmed, executes the filtered plan and prints the same execution output format as `restore apply --yes` (`restore_item ...`, `restore_summary ...`).

### Retention prune

```bash
redeem prune run --days 30
```

`prune run` prints:

- `prune_summary events_pruned=<n> snapshots_pruned=<n>`

### Doctor checks

```bash
redeem doctor
```

`doctor` prints one line per check and then a summary:

- `doctor_check name=<check> status=<pass|fail> detail=<text>`
- `doctor_summary total=<n> passed=<n> failed=<n>`

Current checks:

- `state_dir_writable`
- `config_load`
- `niri_source`
- `kitty_available`
- `zellij_available`
- `events_integrity`
- `snapshots_integrity`

## Flake Outputs

- `packages.<system>.terminal-redeemer`
- `apps.<system>.redeem`
- `homeManagerModules.terminal-redeemer`
