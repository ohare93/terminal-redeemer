# Configuration

`redeem` reads defaults, then optional YAML config, then command flags.

## Sources and precedence

Applied in this order (later wins):

1. Built-in defaults.
2. YAML config file.
   - Default lookup path: `${XDG_CONFIG_HOME}/terminal-redeemer/config.yaml`
   - Fallback when `XDG_CONFIG_HOME` is unset: `~/.config/terminal-redeemer/config.yaml`
3. Per-command CLI flags.

Global config flag:

- `--config <path>` selects an explicit config file.
- For most commands, an explicit missing/invalid config is a startup error.
- `doctor` is special: it still runs and reports config failure in `config_load`.

## Keys used by current CLI

Core:

- `stateDir`
- `host`
- `profile`

Capture:

- `capture.interval`
- `capture.snapshotEvery`
- `capture.niriCommand`

Process metadata:

- `processMetadata.whitelist`
- `processMetadata.whitelistExtra`
- `processMetadata.includeSessionTag`

Retention:

- `retention.days`

Restore:

- `restore.appAllowlist`
- `restore.terminal.command`
- `restore.terminal.zellijAttachOrCreate`

Note: `capture.enabled` is not consumed by the CLI binary; scheduling enablement is handled by service/module wiring.

## Defaults

- `stateDir`: `~/.terminal-redeemer`
- `host`: `local`
- `profile`: `default`
- `capture.interval`: `60s`
- `capture.snapshotEvery`: `100`
- `capture.niriCommand`: `niri msg -j workspaces windows`
- `retention.days`: `30`
- `restore.terminal.command`: `kitty`
- `restore.terminal.zellijAttachOrCreate`: `true`
- `restore.appAllowlist`: empty map

## Env vars currently used by capture/doctor

- `REDEEM_NIRI_FIXTURE`
  - Default value for `capture once/run --fixture`.
  - If set and `--fixture` is not provided, capture uses file snapshot mode.
- `REDEEM_NIRI_CMD`
  - Used as fallback default for `capture once/run --niri-cmd` and doctor niri source checks.
  - Applied when `capture.niriCommand` is still at the built-in default.

CLI flags always override environment-derived defaults.

## Minimal YAML example

```yaml
stateDir: /home/user/.terminal-redeemer
host: workstation-a
profile: default

capture:
  interval: 60s
  snapshotEvery: 100
  niriCommand: niri msg -j workspaces windows

processMetadata:
  whitelist: []
  whitelistExtra: []
  includeSessionTag: true

retention:
  days: 30

restore:
  appAllowlist:
    firefox: firefox --new-window
  terminal:
    command: kitty
    zellijAttachOrCreate: true
```
