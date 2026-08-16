# Operations

## Four workflows

| Shortcut or command | Result |
|---|---|
| `Mod+Return` | Niri launches a local Kitty directly. |
| `Mod+Shift+Return` | Create one persistent Zellij session on Lattice, attach from Overton, and best-effort open an attach-only Lattice Kitty. |
| `Mod+Ctrl+Return` | Browse and reopen visible or headless live Lattice sessions. |
| `redeem resume` | Restore exact prior-boot terminal placement. |

The Home Manager module exports the shortcuts as an opt-in Niri fragment. `mirror new` always preserves the Overton-created session even when the bounded source-local Kitty helper is unavailable. An optional `--source-workspace` name or number moves the source Kitty once without installing a Niri rule or service.

## Dependencies and diagnostics

Local capture/resume requires a Linux boot ID, Niri, Kitty, and Zellij. Remote access additionally requires SSH and source-side `redeem mirror snapshot`. Dual-visible creation requires a compatible source-side `redeem mirror attach-local`; deployment/version skew is tolerated as a post-creation warning. The optional image bridge requires `wl-paste`, SCP, and Kitty remote control.

`redeem doctor` is read-only. It validates configuration and existing checkpoints, reads the boot ID, queries Niri readiness, lists Zellij sessions, checks direct launchers, reports resume/service policy, and checks configured mirror executables. It does not create or repair state.

## Capture and resume

Home Manager's capture timer runs:

```bash
redeem capture once
```

Capture holds the writer lock while performing one complete Niri windows/workspaces query and publishing the current boot/host/profile checkpoint. Publication writes a mode-0600 temporary file, fsyncs it, atomically renames it, and fsyncs `checkpoints/`. A failed query or failure before rename leaves the previous checkpoint usable.

```bash
redeem resume --dry-run
redeem resume
```

Resume waits for a valid Niri query, rejects malformed checkpoint files, excludes the current Linux boot ID, filters host/profile, and selects the newest prior boot. It does not silently choose an older candidate when the newest is empty or stale.

For each terminal, resume requires an exact live Zellij session, direct Kitty launcher PID, exactly one matching Niri `client_pid`, exact descendant attach argv, and observed workspace placement. It never uses session prefixes, app-ID ordering, attach-or-create, or nearest-window fallback. Rerunning detects an already open session instead of creating a duplicate.

## Startup and retention

`resume.onStartup = true` installs `terminal-redeemer-resume.service`, a bounded-retry graphical-session user oneshot. It is disabled by default. Before enabling it, disable every Niri, Kitty, Zellij, autostart, or host-local service that also resumes terminals.

```bash
systemctl --user status terminal-redeemer-resume.service
journalctl --user -u terminal-redeemer-resume.service
redeem prune run --days 30
```

Prune shares the writer lock, preserves the current boot and newest usable prior checkpoint for each host/profile, removes eligible older checkpoints, and fsyncs the checkpoint directory.

## Manual mirror continuity

Run `redeem mirror save --host lattice` while the desired Overton projections are open. Save acquires a fresh remote snapshot, then uses each current Niri PID plus the complete configured SSH argv and exact static Zellij command evidence. Title-only windows are untracked and multiple matching descendants are ambiguous; neither is pinned. The host identity is the exact SSH destination token, not a claim about the canonical machine behind an alias. Save atomically replaces the host/profile pin outside `checkpoints/`. Run `redeem mirror apply --host lattice` after a disconnect or restart. Apply refreshes the source snapshot, reports missing sessions without creating them, skips exact already-open projections, and attaches the rest in locally captured order before one-time workspace/floating/size actions. Placement degradation leaves the attached window open. Use `--dry-run` on either command for fresh read-only observation without pin, launch, or Niri mutation.

## Temporary workspace following

Run `redeem mirror follow --host lattice`, filter and select one current Lattice workspace with printable input and arrow navigation, and leave the foreground status TUI open. Each healthy complete poll opens only missing visible exact ACTIVE sessions in source order, with at most four detached launch attempts per poll and 64 attempts total by default, then moves each newly verified projection once to the frozen matching Overton workspace. Attempts are charged before invocation, and the status separates attempted, confirmed, and uncorrelated totals. Closing a projection causes the next healthy poll to reopen it while the source remains eligible. Source session exit causes no Redeem close action. Disconnection, malformed inventory, or ambiguous local evidence opens nothing and retries with bounded backoff. Press `q` or Ctrl+C to stop; no service, timer, rule, or saved selection exists.

## User-owned physical smoke checklist

Do not activate hosts from repository automation. After the user activates the approved package and configuration on Lattice and Overton:

1. **Local shortcut:** press `Mod+Return` on Overton; verify a local Kitty opens without SSH or Redeem routing.
2. **Remote creation:** press `Mod+Shift+Return`; verify the Overton shell runs on Lattice and a second attach-only Kitty opens in the requested Lattice workspace. Confirm only the Overton command contains `--create`.
3. **Zero-output/partial success:** with no Lattice monitor, confirm creation still succeeds on Overton; accept either a retained non-visible Lattice Kitty or a clear source-helper warning. Verify an older/unavailable source helper never removes the session.
4. **Detach persistence:** note the exact Zellij session name, close either Kitty normally, and verify `redeem mirror list --host lattice` still reports the live session.
5. **Picker reopen:** press `Mod+Ctrl+Return`, choose the exact session, and verify its tabs, panes, processes, and CWD are intact.
6. **Checkpoint:** run `redeem capture once` and `redeem doctor`; confirm both succeed.
7. **Prior-boot plan:** after the approved reboot or failure simulation, run `redeem resume --dry-run`; verify it selects the expected prior boot and exact sessions.
8. **Placement:** run `redeem resume`; verify each terminal appears in its captured workspace, then run it again and verify no duplicate Kitty windows.
