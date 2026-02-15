# terminal-redeemer

`terminal-redeemer` provides a rewindable timeline for terminal and window session restore on Niri.

CLI command: `redeem`

## Status

V1 core flows are implemented and tested:

- capture (`once`, `run`)
- history (`list`, `inspect`)
- replay at timestamp
- restore (`apply`, `tui`)
- prune (`run`)
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

### Retention prune

```bash
go run ./cmd/redeem prune run --state-dir ~/.terminal-redeemer --days 30
```

## Flake Outputs

- `packages.<system>.terminal-redeemer`
- `apps.<system>.redeem`
- `homeManagerModules.terminal-redeemer`
