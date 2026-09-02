# terminal-redeemer

`terminal-redeemer` provides separate, bounded terminal workflows for Niri, Kitty, and Zellij:

1. **`Mod+Return` — local terminal.** Niri launches Kitty directly; Redeem is not involved.
2. **`Mod+Shift+Return` — new dual-visible Lattice terminal.** Overton alone creates the persistent session, then Redeem best-effort opens an attach-only source Kitty.
3. **`Mod+Ctrl+Return` — manual picker.** Browse or reopen visible and headless live Lattice sessions without persistent selection.
4. **Pinned save/apply.** Manually replace and later apply one exact projection set; it is independent of rolling checkpoints.
5. **Foreground workspace follow.** Temporarily add missing visible projections from one runtime-selected workspace until the command exits.
6. **Same-boot and reboot recovery.** Rolling local checkpoints let `resume --all` reconcile current exact ACTIVE sessions after Niri restarts and restrict reboot resurrection to the newest prior-active allow-list.

The Home Manager module exports only the three shortcuts as an opt-in Niri fragment. It also exports direct argv for new/open/save/apply/follow, but installs no follow service, timer, rule, or saved selection.

## Remote sessions

```bash
redeem mirror new --host lattice --source-workspace agentleman  # create once, view on both hosts
redeem mirror open --host lattice                               # project-first picker
redeem mirror list --host lattice      # non-interactive live inventory
redeem mirror save --host lattice      # replace the pinned live projection set
redeem mirror apply --host lattice     # manually reopen that pinned set
redeem mirror follow --host lattice    # temporarily follow one selected workspace
```

Closing an Overton mirror window detaches it; the Zellij session continues on Lattice and remains discoverable. Existing sessions are exact attach-only. `mirror new` uses attach-or-create only for its newly generated safe identity.

`mirror save` always refreshes the remote snapshot and trusts only complete configured SSH argv plus exact static Zellij evidence below owned Niri window PIDs; titles are presentation, so ambiguous or untracked windows are reported and excluded. The persisted host is the exact SSH destination token, not a canonical-host claim for aliases. Save atomically replaces one mode-0600 pin per host/profile under `STATE_DIR/mirror/pins/`, outside rolling checkpoints. `mirror apply` refreshes and preflights exact ACTIVE sessions, skips already projected sessions, never creates missing sessions, and opens the rest in locally captured order. Each new window is moved once and receives supported floating/tiled size actions; exact Niri column/stack reconstruction is intentionally unsupported. Both commands support side-effect-free `--dry-run`.

`mirror follow` is a foreground-only temporary additions loop. Select a current source workspace with arrow keys; printable `j` and `k` filter normally. Redeem freezes the matching local workspace identity, polls complete source snapshots no faster than two seconds, opens only missing visible exact ACTIVE sessions, and moves each new projection once. Defaults are four detached launch attempts per poll and 64 for the run; attempts are charged before invocation so an uncorrelated launch cannot bypass `--max-total`. `--max-per-poll`, `--max-total`, and `--interval` are explicit overrides. It never closes, reorders, resizes, or continually repositions windows. Press `q` or Ctrl+C to stop.

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

## Terminal recovery

```bash
redeem capture once              # atomically refresh this boot's rolling checkpoint
redeem resume --all --dry-run    # inspect same-boot ACTIVE or reboot prior-active recovery
redeem resume --all              # idempotently reconcile the full eligible set
redeem resume                    # narrower, prior-visible manual resume remains available
redeem prune run                 # bound retained boot checkpoints
redeem doctor                    # read-only recovery and mirror diagnostics
```

Capture stores one complete schema-3 checkpoint per boot, host, and profile, including an exact ACTIVE-session allow-list and sticky placement. Publication holds the shared writer/operation lock and uses a mode-0600 temporary file, file fsync, atomic rename, and directory fsync. On the same boot, `--all` uses the current exact ACTIVE catalog and sticky placement; after reboot it selects the newest matching prior recovery point and permits only names in that prior-active allow-list. Unrelated resurrection-cache entries are never candidates.

`--max-age` blocks stale prior dead-session resurrection and warns about stale sticky placement, but does not block attaching a session that is currently exact ACTIVE. Named workspaces are preferred over output/index and index-only fallbacks; `doctor` warns about tracked placement that depends on unnamed indices. Recovery launches Kitty directly with `zellij attach -- <session>`: it never creates, uses attach-or-create, or passes force-run flags. It reconciles existing windows before missing ones, then restores deterministic terminal column order when each target occupies its own column. Stacked rows are reported unsupported rather than guessed, and unrelated window ordering is not reconstructed.

## Setup

- [Configuration](docs/CONFIG.md)
- [Operations and physical smoke checklist](docs/OPERATIONS.md)
- [Prior-boot resume decision](docs/adr/0001-resume-zellij-terminals-in-niri.md)
- [Bounded mirror continuity decision](docs/adr/0002-bounded-mirror-continuity.md)

Home Manager can schedule capture and optionally run the same `redeem resume --all` oneshot at graphical-session startup. When enabled, add the generated `resume.niriIntegrationFragment` once to Niri's configuration; its native `spawn-at-startup` hook restarts that service on every compositor start, while periodic capture remains ordered afterward. Startup recovery is disabled by default. Disable any competing startup terminal restorer before enabling it.

Physical deployment, activation, dual-host validation, and reboot testing remain user-owned.
