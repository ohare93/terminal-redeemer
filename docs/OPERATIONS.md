# Operations

## Four workflows

| Shortcut or command | Result |
|---|---|
| `Mod+Return` | Niri launches a local Kitty directly. |
| `Mod+Shift+Return` | Create a persistent Zellij session on Lattice and attach from an owned Kitty on Overton. |
| `Mod+Ctrl+Return` | Browse and reopen visible or headless live Lattice sessions. |
| `redeem resume` | Restore exact prior-boot terminal placement. |

The Home Manager module exports the shortcuts as an opt-in Niri fragment. `mirror new` creates no visible Kitty on Lattice; only the persistent shell and Zellij session run there.

## Dependencies and diagnostics

Local capture/resume requires a Linux boot ID, Niri, Kitty, and Zellij. Remote access additionally requires SSH and source-side `redeem mirror snapshot`. The optional image bridge requires `wl-paste`, SCP, and Kitty remote control.

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

## User-owned physical smoke checklist

Do not activate hosts from repository automation. After the user activates the approved package and configuration on Lattice and Overton:

1. **Local shortcut:** press `Mod+Return` on Overton; verify a local Kitty opens without SSH or Redeem routing.
2. **Remote creation:** press `Mod+Shift+Return`; verify the shell hostname/processes are on Lattice and no visible Kitty was created on Lattice.
3. **Detach persistence:** note the exact Zellij session name, close the Overton Kitty normally, and verify `redeem mirror list --host lattice` still reports that live headless session.
4. **Picker reopen:** press `Mod+Ctrl+Return`, choose the exact session, and verify its tabs, panes, processes, and CWD are intact.
5. **Checkpoint:** run `redeem capture once` and `redeem doctor`; confirm both succeed.
6. **Prior-boot plan:** after the approved reboot or failure simulation, run `redeem resume --dry-run`; verify it selects the expected prior boot and exact sessions.
7. **Placement:** run `redeem resume`; verify each terminal appears in its captured workspace, then run it again and verify no duplicate Kitty windows.
