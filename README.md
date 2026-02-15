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

### Enter dev environment

```bash
use_dev_env
```

Or use devbox directly:

```bash
devbox shell
```

### Run tests

```bash
go test ./...
```

### Try CLI

```bash
go run ./cmd/redeem --help
```

### Capture once (fixture)

```bash
go run ./cmd/redeem capture once \
  --fixture ./testdata/niri-snapshot.json \
  --state-dir ~/.terminal-redeemer
```

### Capture once (live command)

```bash
go run ./cmd/redeem capture once \
  --niri-cmd 'niri msg -j workspaces windows' \
  --state-dir ~/.terminal-redeemer
```

### Inspect and restore

```bash
go run ./cmd/redeem history list --state-dir ~/.terminal-redeemer
go run ./cmd/redeem history inspect --state-dir ~/.terminal-redeemer --at 2026-02-15T10:00:00Z
go run ./cmd/redeem restore tui --state-dir ~/.terminal-redeemer
go run ./cmd/redeem restore apply --state-dir ~/.terminal-redeemer --at 2026-02-15T10:00:00Z --yes
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
go run ./cmd/redeem prune run --state-dir ~/.terminal-redeemer --days 30
```

`prune run` prints:

- `prune_summary events_pruned=<n> snapshots_pruned=<n>`

### Doctor checks

```bash
go run ./cmd/redeem doctor
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
