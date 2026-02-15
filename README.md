# terminal-redeemer

`terminal-redeemer` provides a rewindable timeline for terminal and window session restore on Niri.

CLI command: `redeem`

## Status

This repository is scaffolded for V1 implementation. See:

- `DESIGN.md`
- `IMPLEMENTATION_PLAN.md`
- `MILESTONES.md`

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

## Flake Outputs

- `packages.<system>.terminal-redeemer`
- `apps.<system>.redeem`
- `homeManagerModules.terminal-redeemer`
