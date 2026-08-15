# Operations

## Dependencies and platform boundary

History restore and live mirroring are separate paths. Live mirroring currently requires:

- `ssh` and source-side `redeem mirror snapshot`
- local Kitty and Niri for opening/listing/closing mirror windows
- source-side Zellij
- `wl-paste`, `scp`, and Kitty remote control when the image bridge is enabled

Commands are configurable. `redeem doctor` is read-only: it does not create the state directory or write probe files. It reports config validity; Linux boot ID; exact state/event/rolling-checkpoint/timestamped-snapshot paths and integrity; live Niri socket/query readiness (or an offline `REDEEM_NIRI_FIXTURE`); direct Kitty launcher availability and PID-correlation assumptions; Zellij executable/listing behavior; configured checkpoint-age/workspace/startup/capture policy; startup-service enablement when requested; and local-install shadowing. A corrupt rolling checkpoint fails its doctor check, but resume still falls back to valid boot-aware event evidence. When `mirror.sourceHost` is configured doctor additionally checks mirror SSH, launcher, Niri, and enabled clipboard/SCP executables. Doctor does not connect to the source.

A failed required check makes doctor exit 1. Disabled optional startup automation is a passing informational result, not a prerequisite failure. Use a valid Niri fixture to test doctor without a compositor; live mode requires `NIRI_SOCKET` and a successful windows/workspaces query.

A non-Niri compositor cannot provide owned-window status/close. A non-Kitty launcher must implement the Kitty flags/control behavior used by the planner. Failures are reported rather than silently operating on unrelated windows.

## Home Manager and NixOS

Enable `programs.terminal-redeemer.enable = true`. Home Manager writes `~/.config/terminal-redeemer/config.yaml`, installs the selected package, and optionally manages capture/prune timers. The capture timer starts and stops with `graphical-session.target`, waits one configured interval before its first activation, and repeats every configured interval while the graphical session is active. Its default interval is 60 seconds (`capture.interval = "60s"`). Each activation runs the same `redeem capture once` full reconciliation available to operators. A failed Niri windows/workspaces query exits the oneshot visibly in the user journal without appending a partial checkpoint; the next timer activation retries from a fresh full query.

A successful activation always runs the complete Niri windows/workspaces query and terminal metadata enrichment. History is change-only: the first success in a boot appends one `state_full`, and later same-boot successes append only when semantic normalized state changes. Window titles are excluded from that hash because shell commands, browser titles, and progress spinners are volatile presentation metadata; the latest title is still retained in the rolling checkpoint. Session identity, CWD, PID, application identity, window lifecycle, workspace, and placement changes remain history-worthy. Regardless of whether an event is appended, each success replaces that boot/host/profile's rolling file under `stateDir/checkpoints/`, so a quiet desktop still gets a fresh observation every 60 seconds by default. `events.jsonl` remains append-only between prune runs; `snapshots/` remains the older timestamped replay optimization.

Event append and rolling replacement share the repository's advisory writer lock. Rolling checkpoints created before title-insensitive hashing remain readable; the first subsequent capture emits at most one transition event and rewrites the current boot's checkpoint in the new format. A changed event is flushed before checkpoint publication. Checkpoint replacement writes and fsyncs a mode-0600 temporary file, atomically renames it, then fsyncs `checkpoints/`. If power fails after the event flush but before publication, the next unchanged capture recognizes the newer event, repairs the checkpoint, and does not append a duplicate.

The NixOS wrapper requires the Home Manager NixOS module and forwards `programs.terminal-redeemer.users.<name>`. Home Manager, not a system service, owns graphical startup resume.

Startup resume remains off by default. With `restore.onStartup = true`, Home Manager enables `terminal-redeemer-resume.service` as a `graphical-session.target` user oneshot. Its `ExecStart` is the same packaged executable, generated config path, and canonical `resume` subcommand used manually—there is no wrapper algorithm. The service completes only after `redeem resume` completes. It is ordered before capture when both are starting, while the capture timer still delays its first run by one interval. Resume itself only reads history, and capture/prune retain their single-writer lock.

The canonical command first polls Niri IPC in-process, bounded by `restore.resumeTimeout` at `restore.resumePollInterval`, before it selects a checkpoint. Manual and startup invocations therefore share the same readiness contract. The successful readiness snapshot is reused for initial reconciliation. A missing/not-ready `NIRI_SOCKET`, timed-out/failed Niri query, or other applying failure is actionable and journal-visible. systemd additionally retries the whole idempotent command after 3 seconds, at most five starts within 30 seconds, then stops; there is no unbounded or persistent loop. Ensure the Niri session imports `NIRI_SOCKET` into the user manager and Kitty/Zellij are installed in the user or system profile:

```bash
systemctl --user show-environment | grep '^NIRI_SOCKET='
systemctl --user status terminal-redeemer-resume.service
journalctl --user -u terminal-redeemer-resume.service -b
```

Use build/evaluation before activation:

```bash
nix flake check 'path:.'
```

## Source setup and smoke checks

On the source host:

```bash
redeem mirror snapshot
```

The command emits one JSON object containing `host`, `profile`, `generated_at`, and ordered `windows`. Terminal windows may contain top-level and nested `zellij_session`, plus `terminal.cwd`. The consumer rejects malformed JSON and incomplete required envelope/window metadata.

On the consuming host:

```bash
redeem mirror list --host source.example
redeem mirror open --host source.example --all --dry-run
redeem mirror new --host source.example --dry-run
```

`--dry-run` validates and prints launch plans without running Kitty. For fully offline `list`/`open` checks, add `--snapshot-file PATH`. A physical smoke should create one remote terminal, close its local Kitty window, confirm the Zellij session remains in `mirror list`, and reopen that exact session through the picker.

## Owned-window lifecycle

Terminal Redeemer marks mirror Kitty windows with `mirror.appID`. Status and close first decode `niri msg -j windows`, then filter by exact app ID. With `--host`, they additionally require the generated title prefix `<host>[`; `--all-hosts` removes only that host filter, never the app-ID ownership filter.

```bash
redeem mirror status --host source.example
redeem mirror close --host source.example --dry-run
redeem mirror close --host source.example
```

Always inspect dry-run output before destructive close. Closing a local mirror window does not kill its remote Zellij session.

## Image bridge

Each launched window gets a unique Kitty control socket and Ctrl+V mapping. `paste-image`:

1. reads advertised Wayland MIME types;
2. chooses the first configured supported image MIME;
3. reads binary clipboard bytes into a mode-0600 uniquely named local file;
4. creates the quoted remote directory through SSH and copies with SCP;
5. injects the identical remote path into that Kitty instance;
6. removes the local temporary file.

The remote file is intentionally retained for the remote consumer. Arrange separate `/tmp` cleanup according to source policy. If clipboard inspection/data is unavailable or not an image, raw Ctrl+V is forwarded. SSH/SCP failures are errors and do not inject a nonexistent path.

## Security assumptions

- Hosts and snapshot metadata are validated/quoted. Local process execution uses explicit argv rather than `sh -c`.
- SSH necessarily sends a remote shell command. Snapshot argv, CWD, session name, and remote mkdir path use POSIX single-quote escaping, covered by tests.
- SSH/SCP option lists and executable paths are operator-controlled configuration. Treat the YAML as trusted: SSH options such as `ProxyCommand` can intentionally execute local programs.
- The app ID is the ownership boundary for close. Do not assign it to unrelated applications.
- SSH host keys, authentication, authorization, remote command availability, and remote temp-file confidentiality remain the operator's responsibility.
- The image bridge copies clipboard data to the configured source host. Disable it for sensitive workflows or untrusted hosts.

## Troubleshooting

- `source host is required`: set `mirror.sourceHost` or pass `--host`.
- `decode/malformed remote mirror snapshot`: verify the remote `redeem` version and run its snapshot command directly.
- SSH failure: test normal non-mutating SSH separately; inspect `sshCommand`, `sshOptions`, and `snapshotCommand`.
- no Kitty/Zellij windows: source snapshot windows need `app_id: kitty` and Zellij session metadata.
- Niri/Wayland error: run from the graphical user session and verify `NIRI_SOCKET`; status/close do not support other compositors yet.
- launcher failure: verify Kitty accepts `--detach`, `--class`, `--listen-on`, `--override`, and `-e`.
- image fallback only: inspect `wl-paste --list-types`, configured MIME preference, Kitty remote-control socket, and SCP command/options.
- nested key interception: use the default fresh-Kitty launcher; attach clears Zellij environment variables. Watch is unsupported by pinned Zellij.

## Prior-boot resume

```bash
redeem resume --dry-run  # selection and reconciliation only
redeem resume            # apply the same plan
```

The dry run is non-mutating: it reads rolling checkpoints plus boot-aware full-state event fallback, current Niri workspaces/windows and process metadata, and `zellij list-sessions --short`; it never attaches, creates, launches, or moves anything. Per prior boot, rolling `observed_at` normally controls freshness; a newer event covers an event-to-checkpoint crash boundary. Corrupt/missing rolling files fall back safely. Output starts with the selected prior boot ID and latest successful observation time, followed by stable `resume_item` records and a status-count summary.

Candidate statuses are:

- `ready`: a valid prior-boot checkpoint can be reconciled;
- `empty`: the authoritative prior-boot checkpoint has no eligible terminals;
- `stale`: it exceeds `restore.maxCheckpointAge`; and
- `not_found`: no complete boot-aware checkpoint matches host/profile.

Item statuses are:

- `ready`: dry-run would attempt the item;
- `already_open`: the exact verified Zellij session already has a current terminal;
- `unavailable`: Zellij cannot list/attach/resurrect the captured session, which is never recreated;
- `degraded`: the terminal is/would be available but required workspace resolution or optional layout is incomplete;
- `stale`: the candidate age policy prevented the item;
- `failed`: launch, PID correlation, attachment evidence, required move, or policy failed;
- `skipped`: policy intentionally omitted the item; and
- `restored`: attachment and required workspace placement were evidenced.

Applying returns nonzero when any item is `failed`. Empty/stale/not-found candidates and unavailable/degraded/skipped items remain explicit results rather than silently selecting older history.

The newest prior-boot candidate is authoritative even when it is empty. `empty`, `stale`, and `not_found` candidate statuses are visible no-ops rather than reasons to select older history. Their output includes actionable guidance to use `redeem restore tui` or `redeem restore apply --at ...` for explicit forensic selection, including legacy records without boot IDs.

Apply launches each Kitty process directly, with no outer shell, and passes exact attach-only argv: `zellij attach -- <session>`. Zellij environment variables are removed to avoid accidental nested-session behavior. Resume accepts a launch only when all required evidence is present:

1. the launched process supplies a positive PID;
2. exactly one Niri window with that client PID appears before the configured timeout;
3. a live descendant process has exact argv `zellij attach -- <session>` on two consecutive polls while the launched Kitty process remains alive; and
4. after the Niri move succeeds, the same window ID and PID is observed on the resolved runtime workspace.

There is no app-ID, creation-order, or nearest-window fallback. A launcher that forks/daemonizes or a Kitty/Niri combination that does not preserve client PID identity is reported as `failed`. Correlation or attachment timeout kills the launched process so it cannot leak an unowned terminal. A failed required workspace move is also `failed`, but the successfully attached terminal is deliberately left open; a rerun detects it as `already_open` and does not create a duplicate.

Workspace resolution uses captured durable metadata in this order: exact name, output plus index, then index. See `restore.unresolvedWorkspace` in `docs/CONFIG.md` for unresolved-target behavior. With the `current` policy, an attached session whose target cannot be resolved remains `degraded`, never `restored`. Floating state and supported width/height actions are attempted only after required placement; column ordering is reported as unsupported because Niri cannot target that action by window ID safely. Optional layout failures do not change a required `restored` result.

An `already_open` result comes from a current terminal with the same verified Zellij session, or from the exact `/proc` attachment evidence checked immediately before each launch. Mere presence in Zellij's session list means available, not open. Missing sessions and attach exits are `unavailable` and are never recreated.

## Retention, migration, and rollback

Resume can only select history that capture retained. `retention.days` and prune therefore bound the reboot recovery horizon independently of `restore.maxCheckpointAge`; choose retention longer than the maximum acceptable resume age. Prune holds the same writer lock, crash-safely removes rolling checkpoints whose `observed_at` is older than its cutoff, and applies the existing event/timestamped-snapshot retention rules. A short retention window may produce `not_found`, while an old retained event fallback may produce `stale`. An `empty` newest prior boot is authoritative and does not fall back. Before reboot testing, run `redeem capture once`, then verify `redeem resume --dry-run` after the boot.

Migrate one owner at a time:

1. Deploy Terminal Redeemer capture with `restore.onStartup = false`.
2. Verify periodic/current capture, boot IDs, history paths, `redeem doctor`, manual `redeem resume --dry-run`, and manual idempotence (a second apply opens nothing).
3. **Disable every host-local Niri/Kitty/Zellij startup restoration script, service, timer, autostart entry, or compositor startup command. This is required before the next step.** Capture-only legacy tooling must also not write Terminal Redeemer's state directory.
4. Enable `programs.terminal-redeemer.restore.onStartup = true`, rebuild, reboot, and inspect the user journal.
5. Remove obsolete host-local restoration code from the consumer configuration repository as a separate consumer-owned follow-up; this repository does not remove it.

To disable or roll back automation, set `restore.onStartup = false` and rebuild. This removes the generated startup unit while preserving history and manual `redeem resume`. If immediate containment is needed before rebuild, stop/disable the user unit and keep all other startup restorers off until ownership is deliberately reassigned:

```bash
systemctl --user disable --now terminal-redeemer-resume.service
redeem resume --dry-run
```

Do not re-enable a host-local restorer concurrently. Rollback of startup automation does not delete history; use the configured prune policy if deletion is intended.

## Existing capture/restore operations

```bash
redeem capture once
redeem history list
redeem history inspect --at <RFC3339>
redeem restore apply --at <RFC3339> --dry-run
redeem restore tui
redeem prune run --days 30
```

Replay and `doctor` both ignore one malformed trailing event after a crash (doctor emits a non-failing note), but report corruption if a malformed record appears before a later record. Timestamped snapshots remain an optional explicit-history optimization and are not the process-independent rolling resume mechanism. Bootless events and existing snapshots are never migrated or rewritten merely by capture and remain available to explicit historical restore. Capture and prune coordinate through a crash-recoverable advisory lock; a leftover `meta/lock` file is harmless, while prune still reports an active writer when the lock is held.

## Deferred work

Terminal Redeemer intentionally does not add continuous cross-host reconciliation, duplicate-window suppression across repeated `mirror open` calls, or a pane-rich full-screen mirror TUI.
