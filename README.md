# terminal-redeemer

`terminal-redeemer` does two jobs for Niri, Kitty, and Zellij:

1. capture terminal placement and resume exact live Zellij sessions after a reboot;
2. create, browse, and attach to persistent terminal sessions on another host.

The CLI is `redeem`.

## Local reboot resume

```bash
redeem capture once        # atomically refresh this boot's rolling checkpoint
redeem resume --dry-run    # inspect the newest eligible prior-boot plan
redeem resume              # idempotently apply that plan
redeem prune run           # bound retained boot checkpoints
redeem doctor              # read-only dependency and state diagnostics
```

Capture stores one complete checkpoint for each boot, host, and profile. Every successful capture replaces that boot's file with temp-write, file fsync, atomic rename, and directory fsync while holding the repository writer lock. Resume reads checkpoints directly, excludes the current boot, and selects the newest matching prior boot before evaluating its terminal contents or age.

Resume is terminal-only and attach-only. It never recreates a missing captured session. It correlates the launched Kitty PID to exactly one Niri window, requires exact descendant `zellij attach -- <session>` evidence, moves only that correlated window, and treats a second run as an idempotent no-op when the session is already open.

## Remote terminal workflow

```bash
redeem mirror new --host lattice       # create a persistent Lattice session and attach locally
redeem mirror open --host lattice      # project-first picker for visible and headless sessions
redeem mirror list --host lattice      # non-interactive inventory
redeem mirror snapshot                 # source-side JSON inventory used over SSH
redeem mirror status                   # list locally owned mirror windows
redeem mirror close --host lattice     # close only owned local projections
```

Closing a local mirror window detaches it; the Zellij session continues on the remote host and remains discoverable. Existing-session operations are exact attach-only. `mirror new` uses attach-or-create only for its newly generated safe session identity.

The Home Manager module exports an opt-in Niri fragment with separate shortcuts:

- `Mod+Return`: local Kitty;
- `Mod+Shift+Return`: new remote session;
- `Mod+Ctrl+Return`: remote session picker.

The module does not install the fragment automatically.

## Configuration and operations

- [Configuration](docs/CONFIG.md)
- [Operations and smoke checks](docs/OPERATIONS.md)
- [Prior-boot resume decision](docs/adr/0001-resume-zellij-terminals-in-niri.md)

Home Manager can run periodic capture and optional startup resume. Startup resume is off by default; disable every competing host-local startup terminal restorer before enabling it.

Old event logs and timestamped snapshot directories are not migrated or deleted. They are ignored by the reduced product and can be removed later under user-owned retention policy.

## Supported CLI

```text
redeem capture once|run
redeem resume [--dry-run]
redeem mirror new|open|list|snapshot|status|close|paste-image
redeem prune run
redeem doctor
```

Physical deployment, activation, dual-host validation, and reboot smoke testing remain user-owned.
