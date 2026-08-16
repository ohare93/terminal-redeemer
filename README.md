# terminal-redeemer

`terminal-redeemer` provides four terminal workflows for Niri, Kitty, and Zellij:

1. **`Mod+Return` — local terminal.** Niri launches Kitty directly; Redeem is not involved.
2. **`Mod+Shift+Return` — new Lattice terminal.** Redeem creates a persistent Zellij session on Lattice and attaches to it in an owned Kitty on Overton.
3. **`Mod+Ctrl+Return` — browse or reopen Lattice terminals.** The project-first picker includes both visible and headless live Zellij sessions.
4. **Resume after reboot.** Rolling boot checkpoints restore exact live terminal sessions to their captured Niri workspaces.

The Home Manager module exports these bindings as an opt-in Niri fragment. It does not install the fragment automatically.

## Remote sessions

```bash
redeem mirror new --host lattice       # create on Lattice and attach from Overton
redeem mirror open --host lattice      # project-first picker
redeem mirror list --host lattice      # non-interactive live inventory
```

Closing an Overton mirror window detaches it; the Zellij session continues on Lattice and remains discoverable. Existing sessions are exact attach-only. `mirror new` uses attach-or-create only for its newly generated safe identity.

### Picker controls

The picker groups window-backed sessions by exact Niri workspace identity, using a readable fallback label for unnamed workspaces, and keeps live headless sessions visible in a separate **Headless Zellij** section. Project and JJ-workspace identities use the same stable, path-derived coloured chip treatment as Mono/Auto's project footer; identity is resolved on the source host so canonical repository/workspace labels remain accurate. Type any printable character—including `j` and `k`—to filter by project, activity, session, workspace, or CWD. Only `↑` and `↓` move the current row.

- `Space` toggles the current session.
- `Ctrl+A` toggles all sessions matching the current filter.
- `Enter` opens checked sessions in discovery order, or the current session when none are checked.
- `Esc` clears a non-empty filter; press it again to cancel.

Automation can continue to bypass the picker with `--all`, repeatable `--session NAME`, or one-based `--select N`.

`mirror new` does **not** create a visible Kitty window on Lattice. The persistent shell and Zellij session run there, while the visible Kitty projection is on Overton.

Source-side and owned-window support commands are:

```text
redeem mirror snapshot
redeem mirror status
redeem mirror close --host lattice
redeem mirror paste-image
```

## Reboot resume

```bash
redeem capture once        # atomically refresh this boot's rolling checkpoint
redeem resume --dry-run    # inspect the newest eligible prior-boot plan
redeem resume              # idempotently apply that plan
redeem prune run           # bound retained boot checkpoints
redeem doctor              # read-only capture/resume/mirror diagnostics
```

Capture stores one complete checkpoint per boot, host, and profile. Publication holds the writer lock and uses a mode-0600 temporary file, file fsync, atomic rename, and directory fsync. Resume selects the newest matching prior boot before evaluating age or contents.

Resume is terminal-only and attach-only. It never recreates a missing captured session. It correlates each launched Kitty PID to exactly one Niri window, requires exact descendant `zellij attach -- <session>` evidence, moves only that window, and is idempotent when the session is already open.

## Setup

- [Configuration](docs/CONFIG.md)
- [Operations and physical smoke checklist](docs/OPERATIONS.md)
- [Prior-boot resume decision](docs/adr/0001-resume-zellij-terminals-in-niri.md)

Home Manager can schedule capture and optionally run resume at graphical-session startup. Startup resume is disabled by default. Disable any competing startup terminal restorer before enabling it.

Physical deployment, activation, dual-host validation, and reboot testing remain user-owned.
