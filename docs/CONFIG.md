# Configuration

`terminal-redeemer` supports CLI flags and Home Manager rendered YAML.

Core keys:

- `stateDir`: state root path
- `host`: host partition key
- `profile`: profile partition key

Capture keys:

- `capture.enabled`
- `capture.interval`
- `capture.snapshotEvery`
- `capture.niriCommand`

Retention keys:

- `retention.days`

Process metadata keys:

- `processMetadata.whitelist`
- `processMetadata.whitelistExtra`
- `processMetadata.includeSessionTag`

Restore keys:

- `restore.appAllowlist`
- `restore.terminal.command`
- `restore.terminal.zellijAttachOrCreate`

CLI precedence:

- CLI flags override file/module defaults.
- `capture once/run` accepts `--fixture` or `--niri-cmd`.
