# Operations

## Dependencies

Local capture/resume requires Linux boot IDs, Niri, Kitty, and Zellij. Remote access additionally requires SSH and source-side `redeem mirror snapshot`. The optional image bridge uses `wl-paste`, SCP, and Kitty remote control.

`redeem doctor` is read-only. It reports configuration validity, state/checkpoint integrity, boot ID, Niri readiness, direct Kitty launcher availability, Zellij listing behavior, resume policy, startup-service policy, and configured mirror commands.

## Capture lifecycle

Home Manager's capture timer starts with `graphical-session.target`, waits one configured interval, and runs the same packaged command as manual capture:

```bash
redeem capture once
```

Each activation performs a complete Niri windows/workspaces query and refreshes process-derived terminal metadata. It holds the repository advisory lock while publishing a deterministic checkpoint under `checkpoints/` for the current boot, host, and profile.

Publication writes a mode-0600 temporary file, fsyncs it, atomically renames it over the identity's checkpoint, and fsyncs the directory. A failure before rename leaves the previously published checkpoint usable. A failure or partial query never overwrites a prior successful checkpoint. The recovery point objective is one capture interval plus scheduling delay.

## Resume lifecycle

```bash
redeem resume --dry-run
redeem resume
```

Resume waits for a valid Niri snapshot, reads rolling checkpoints directly, rejects malformed checkpoint files, excludes the current Linux boot ID, filters host/profile, and selects the newest prior boot. The newest candidate remains authoritative when empty or stale; resume does not silently choose an older state.

Per-item states include `ready`, `already_open`, `unavailable`, `degraded`, `stale`, `failed`, `skipped`, and `restored`. Applying returns nonzero when an item fails.

Resume launches Kitty directly with exact attach-only argv ending in `zellij attach -- <session>`. It accepts success only after:

1. the launcher supplies a usable PID;
2. exactly one Niri window reports that PID as `client_pid`;
3. a live descendant argv exactly matches the requested attach;
4. required workspace movement is observed for that exact window.

There is no app-ID, launch-order, prefix-session, attach-or-create, or nearest-window fallback. Failed pre-attachment launches are cleaned up. A successfully attached terminal is left open if a later placement action fails; rerunning detects it as already open rather than creating a duplicate.

## Optional startup resume

`resume.onStartup = true` installs `terminal-redeemer-resume.service`, a bounded-retry graphical-session user oneshot that invokes the canonical `redeem resume` command. Startup resume is off by default.

Before enabling it, disable every Niri, Kitty, Zellij, autostart, or host-local service that also restores terminals. Only one component may own startup restoration.

```bash
systemctl --user status terminal-redeemer-resume.service
journalctl --user -u terminal-redeemer-resume.service
```

## Remote sessions

```bash
redeem mirror new --host lattice
redeem mirror list --host lattice
redeem mirror open --host lattice
```

A physical smoke should:

1. create one new remote terminal;
2. confirm its shell/processes run on Lattice;
3. close the local Kitty window;
4. confirm the exact Zellij session remains in `mirror list` as headless;
5. reopen it through the picker;
6. confirm tabs, panes, and CWD remain intact.

Existing sessions are attach-only. Hosts, session names, CWDs, and remote argv are validated and quoted. Local processes use explicit argv. Only windows with the configured mirror app ID are eligible for bulk close.

## Retention and rollback

```bash
redeem prune run --days 30
```

Prune holds the same writer lock as capture, removes checkpoints older than the cutoff, and fsyncs `checkpoints/`. Keep retention longer than the maximum acceptable resume age.

The reduced product does not rewrite or delete old `events.jsonl` or `snapshots/` data. Rollback is by an earlier package revision; user-owned cleanup of orphaned historical state is separate.

## Physical reboot smoke checklist

Deployment and activation are user-owned. After activating both hosts:

1. run `redeem capture once` and `redeem doctor`;
2. inspect `redeem resume --dry-run`;
3. reboot or perform the approved failure simulation;
4. run `redeem resume` and verify exact session/workspace placement;
5. run it again and verify no duplicate Kitty windows;
6. exercise the remote-session detach/reopen smoke above.
