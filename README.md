# terminal-redeemer

`terminal-redeemer` provides four terminal workflows for Niri, Kitty, and Zellij:

1. **`Mod+Return` — local terminal.** Niri launches Kitty directly; Redeem is not involved.
2. **`Mod+Shift+Return` — new Lattice terminal.** Redeem creates a persistent Zellij session on Lattice, attaches from an owned Kitty on Overton, and best-effort opens an attach-only Kitty on Lattice.
3. **`Mod+Ctrl+Return` — browse or reopen Lattice terminals.** The project-first picker includes both visible and headless live Zellij sessions.
4. **Resume after reboot.** Rolling boot checkpoints restore exact live terminal sessions to their captured Niri workspaces.

The Home Manager module exports these bindings as an opt-in Niri fragment. It does not install the fragment automatically.

## Remote sessions

```bash
redeem mirror new --host lattice --source-workspace agentleman  # create once, view on both hosts
redeem mirror open --host lattice                               # project-first picker
redeem mirror list --host lattice      # non-interactive live inventory
redeem mirror save --host lattice      # replace the pinned live projection set
redeem mirror apply --host lattice     # manually reopen that pinned set
```

Closing an Overton mirror window detaches it; the Zellij session continues on Lattice and remains discoverable. Existing sessions are exact attach-only. `mirror new` uses attach-or-create only for its newly generated safe identity.

`mirror save` always refreshes the remote snapshot and trusts only complete configured SSH argv plus exact static Zellij evidence below owned Niri window PIDs; titles are presentation, so ambiguous or untracked windows are reported and excluded. The persisted host is the exact SSH destination token, not a canonical-host claim for aliases. Save atomically replaces one mode-0600 pin per host/profile under `STATE_DIR/mirror/pins/`, outside rolling checkpoints. `mirror apply` refreshes and preflights exact ACTIVE sessions, skips already projected sessions, never creates missing sessions, and opens the rest in locally captured order. Each new window is moved once and receives supported floating/tiled size actions; exact Niri column/stack reconstruction is intentionally unsupported. Both commands support side-effect-free `--dry-run`.

### Picker controls

The picker groups window-backed sessions by exact Niri workspace identity, using a readable fallback label for unnamed workspaces, and keeps live headless sessions visible in a separate **Headless Zellij** section. Project and JJ-workspace identities use the same stable, path-derived coloured chip treatment as Mono/Auto's project footer; identity is resolved on the source host so canonical repository/workspace labels remain accurate. Type any printable character—including `j` and `k`—to filter by project, activity, session, workspace, or CWD. Only `↑` and `↓` move the current row.

- `Space` toggles the current session.
- `Ctrl+A` toggles all sessions matching the current filter.
- `Enter` opens checked sessions in discovery order, or the current session when none are checked.
- `Esc` clears a non-empty filter; press it again to cancel.

Automation can continue to bypass the picker with `--all`, repeatable `--session NAME`, or one-based `--select N`.

`mirror new` remains safe under partial failure: the Overton viewer is the sole creator, then one bounded source-local helper waits for the exact session to become actively verified and opens an attach-only Lattice Kitty. `--source-workspace NAME_OR_NUMBER` optionally moves that source window once; omission uses normal Niri placement. A missing display, older source-side Redeem, version skew, or source placement failure is reported as a warning without killing the session or Overton view. With no connected monitor, Niri may retain the source Kitty without making it physically visible.

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
