# Operations

The opt-in slice deployment, migration/backup/downgrade order, Super+Enter/Super+W consumer template, full operator smoke matrix, rollback invariants, and legacy retirement gate are documented in [HOST_LEECH_READINESS.md](HOST_LEECH_READINESS.md).

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

## Versioned live source inventory

The shipped opt-in host-leech controller uses a separate, authoritative inventory rather than the legacy `mirror snapshot` payload. It remains disabled by default. Initialize its crash-safe source identity once, then inspect snapshots locally:

```bash
redeem slice inventory init
redeem slice inventory snapshot --accept-schema-version 2
```

Initialization durably writes an enrollment marker before current authority and refuses to overwrite existing, corrupt, or missing-after-use state under `stateDir/slice/source-inventory/`. Deleting only `current.json` never makes the namespace fresh again. Snapshot collection uses the direct `NIRI_SOCKET` event replay through an explicit `ConfigLoaded.failed:false`, a separate Outputs query, current-user Kitty process evidence, and pinned Zellij 0.44.3 live sockets. It never attaches, creates, resurrects, or terminates a session.

A coherent zero-output Niri replay (empty outputs and null workspace outputs) is complete authority: existing verified Kitty/Zellij window sources remain selectable, while output geometry is omitted and arbitrary windowless sessions remain in live-session evidence only. Every successfully completed authoritative poll advances revision state and refreshes `observed_at`, even when its semantic inventory hash is unchanged. An exact replay of the same committed revision remains an idempotent duplicate. A `degraded` response retains prior authority and revision when available and must never be treated as source disappearance or permission to close a projection. Inspect `observation.degraded_reasons` rather than raw private errors.

The inventory protocol does not start a reconciliation service or modify host/leech configuration. See [PROTOCOL.md](PROTOCOL.md) for version negotiation, wire fields, conflict/reason enums, ordering, epoch/full-resync behavior, freshness, and privacy boundaries.

## Packaged slice RPC and attachment

The Home Manager module renders store paths for `redeem`, Kitty, OpenSSH, Zellij 0.44.3, Niri 26.04, and `systemctl`. Slice execution uses direct argv throughout: it never invokes `sh -lc`, a login shell, or an interactive profile. Niri inventory/actions remain official newline-delimited direct socket IPC; the packaged Niri executable is used only for the exact `niri 26.04 (Nixpkgs)` compatibility gate.

A source-side noninteractive probe sends exactly one bounded request on stdin:

```bash
printf '%s\n' '{"schema_version":1,"accept_schema_versions":[1],"request_id":"smoke-1","verb":"liveness","payload":{}}' \
  | redeem slice rpc
```

`liveness`, `snapshot`, and `workspace_ensure` fail typed/unavailable if the Niri compatibility or graphical context gate fails. Configuration must contain the exact order-insensitive set `NIRI_SOCKET`, `WAYLAND_DISPLAY`, and `XDG_RUNTIME_DIR`; subsets and extras are rejected. All three values are read from the process environment or immediately filtered from bounded output of the fixed `systemctl --user show-environment` command. Import them into the graphical user manager; do not add profile-sourcing wrappers. Socket values are never serialized.

Host launches use Kitty `--config NONE` with direct fixed argv, so control-plane success does not depend on user Kitty configuration or shell/profile wrappers. The terminal's eventual interactive contents remain user-owned. Launches use a caller-generated idempotency token. The source fully writes/fsyncs a temporary pending record, atomically links it without replacement to `stateDir/slice/rpc-tokens/<digest>.json`, then fsyncs the direct private directory before starting Kitty. Token directory components are rechecked for owner, private mode, and symlinks on every operation. A repeated launch or query returns the same opaque host-terminal ID and never repeats the side effect. Only an executable error proven before process start becomes `failed`; every post-start error, cancellation, or transport loss stays `pending`; a controller may query/replay that same token but must not launch locally. Exhausted bounded query retries become stable `disconnected` until explicit reconnect.

Exact attachment is host-side and interactive:

```bash
redeem slice attach \
  --session exact-safe-name \
  --token controller-token \
  --real-socket-dir "$ZELLIJ_SOCKET_DIR"
```

The wrapper proxies the caller's real stdin/stdout/stderr and terminal cancellation while exposing only the exact verified current-user socket through a private same-filesystem hard link, using an empty cache, scrubbing nested-Zellij variables, and pinning `options --on-force-close detach`. The dedicated mode-0700 private root and each collectible `att-*` directory carry separate durable ownership markers; stale GC retains every unmarked or otherwise unproven directory. Exit statuses are JSON `detached`, `invalid`, `unavailable`, `setup_failed`, `attach_failed`, or `cancelled`; process exit codes are respectively 0, 3, 4, 5, 6, and 130. It never creates, resurrects, prefix-matches, or terminates the host session. Keep the derived private path short enough for the 107-byte Unix socket limit.

SSH host-key verification, known-hosts policy, authentication, authorization, agents, and account provisioning remain operator-owned. The slice transport adds only `-T` and configured keepalive values; it does not set `StrictHostKeyChecking`, replace known-hosts files, install credentials, or choose an agent. Test the fixed packaged remote `redeem slice rpc` command with ordinary SSH before enabling a controller. Operator-supplied SSH argv remains trusted configuration and can deliberately name an identity, agent, jump host, or `ProxyCommand`.

Clipboard transfer for the new slice controller is hard-disabled in configuration for the first rollout. The legacy `mirror.clipboard.enabled` setting remains independent and retains its existing default/behavior.

## Slice spatial-policy smoke boundary

The pure [single-monitor spatial policy](adr/0004-single-monitor-niri-spatial-mapping-policy.md) is not itself a controller or activation surface. Before the v1 controller is enabled, run its documented host-location live smoke on exactly one active output per machine with disposable named workspaces and an unrelated sentinel window. The gate requires exact workspace-ID ensure/move verification, `focus:false`, floating/tiled and proportional-size verification, equal/differing-resolution fidelity reporting, initial order plus drift-only reporting, and failure injection that never moves/closes/focuses unrelated work or affects Zellij ownership. Host writeback is not part of the controller. Multi-monitor output is a typed unsupported topology, not an inferred mapping.

## Opt-in slice controller

Keep `slice.controller.enabled = false` while enrolling and inspecting authority:

```bash
redeem slice controller init --host-id host --leech-id leech
redeem slice controller run --allow-disabled   # foreground development smoke only
redeem slice controller status
redeem slice controller workspace-add --workspace Work
redeem slice controller all-enable
redeem slice controller all-disable
redeem slice controller pickup --source-id src_...
redeem slice controller pickup-remove --source-id src_...
redeem slice controller close --source-id src_...
redeem slice controller reopen --source-id src_...
redeem slice controller reconnect --source-id src_...
redeem slice controller undo
redeem slice manage
```

Only after the foreground smoke passes should a Home Manager consumer opt in with `programs.terminal-redeemer.slice.controller.enable = true`. The service is a foreground singleton, exits on missing/corrupt/uninitialized authority, polls bounded revisioned snapshots, and keeps its mode-0600 control socket in the private controller directory. Check `systemctl --user status terminal-redeemer-slice-controller.service` and its journal. A second controller is refused by the store lock.

Workspace selection automatically includes current and future eligible sources. `all-enable` adds every eligible current and future source—including sources on unnamed host workspaces—to projection desire without changing routed-launch workspace selection; `all-disable` removes only that global reason. Unnamed sources attach exactly but have no cross-machine workspace identity, so they receive no spatial proposal, produce no spatial conflict, and appear under `(unnamed)` in the manager. `redeem slice manage` presents these policies and independent source/projection/connection facts through the running controller's private socket. `pickup` is exact-source inclusion, and `pickup-remove` removes only that inclusion; `drop` is an alias for manual `close`; `close`/the focused-close helper resolves the source once and persists an exclusion keyed by its exact verified Zellij session ID before closing only a positively owned local projection. The drop survives source replacement and source epochs, including headless intervals while the session remains live. `reopen` through any current source for that session or applicable undo clears it early; otherwise consecutive accepted complete session absence plus committed grace expires it automatically. `reconnect` restarts only an exhausted connection budget. `redeem slice close-focused` carries a focus-required marker through the serialized socket close and freshly re-proves that the exact positively owned Niri window is still focused immediately before the local close action. Its close intent is durable before the effect, but any failed focused effect atomically rolls back only that newly created exclusion, closing mapping transition, connection change, and undo entry before serialized reconciliation resumes; an exclusion that already existed is never rolled back. It falls back only when it can lock the store, reload current mapping, and re-prove the same focused Niri window after locking and again after committing close by exact app ID, PID, resolved configured Kitty executable, and byte-for-byte full command argv. Titles never count.

Projection Kitty windows run the packaged `slice projection-run` helper, which invokes packaged SSH directly and then only the host's exact live-only `slice attach` wrapper. Do not interpret SSH survival, an authentication prompt, a banner, or a stalled remote command as connection: `connected` requires the random framed readiness nonce emitted only after the exact isolated Zellij client starts and survives the bounded interactive confirmation interval, plus fresh positive local ownership. The retry deadline is absolute and persisted, so restarting the service first downgrades old `connected` state to re-observation, preserves any existing absolute budget, and creates one bounded episode only when ordinary connected state has no recovery, so local observation failure cannot leave it reconnecting indefinitely. Stable `disconnected` requires explicit reconnect. Degraded or disconnected inventory never closes work; source disappearance requires accepted complete revisions/grace. Manual projection close never terminates host Kitty/Zellij/process state.

Every owned leech projection is converged to the authoritative host workspace and floating/tiled state. Proportional width and height converge only while the host source carries output geometry; a headless revision emits no size action and the next complete outputful revision resumes sizing using exact-ID, non-focus, verify-after-write actions. Local divergence in those four properties is therefore reverted; order drift remains report-only. The controller has no host spatial-writeback path. Epoch replacement without continuity blocks all distinct desired, explicit-reconnect, and timed-retry launches behind proof that old owned cleanup completed; unresolved zero-successor lineage and exhausted gates remain inspectable. Controller state uses fixed caps plus deterministic exact retired-epoch and retired-token tombstones so terminal churn cannot grow the current file without bound while active gates and pending intents are never pruned. Tombstone exhaustion fails a novel transition explicitly and requires maintenance/re-enrollment; it never probabilistically rejects an unrelated value. Exact projection argv and transport-option counts/bytes are bounded, and an oversized marshaled state is rejected before it can replace readable current authority. The controller accepts monotonic routed-launch handoff records for one existing token. Routed creation is owned by the packaged command below; do not install a consumer Super+Enter binding until the complete readiness/rollback gate is satisfied.

### Controller schema compatibility and re-enrolment

Contract v1.1 retains controller schema 2 and adds only optional omitted-false `all_eligible`. Existing schema-2 authority loads with global selection disabled. Global toggles are audited but create no undo entries. To downgrade, run `redeem slice controller all-disable` successfully while the v1.1 controller is still running, disable Leech mode, stop the controller, preserve authority, then deploy the prior package. If `all_eligible` remains true, the prior reader rejects the unknown field and its service fails closed; do not delete authority to work around that result.

Old experimental controller authority is deliberately not migrated in place. On a schema mismatch:

1. stop and disable `terminal-redeemer-slice-controller.service`;
2. copy the entire owner-only `stateDir/slice/controller/` directory to a separate owner-only forensic backup and verify that backup exists and is readable;
3. only after explicit operator approval, rename or remove **only** `stateDir/slice/controller/`;
4. run `redeem slice controller init` to enrol fresh schema-2 authority;
5. explicitly re-add workspace selections, pickups, and manual drops, then start the controller.

Never remove source-inventory or routed-launch token state, host Kitty windows, Zellij sessions, unrelated configuration, or legacy mirror state. The backup is forensic evidence only: never overlay or merge it into the new controller directory.

## Routed Leech terminal launch

Leech mode is separately disabled by default. Inspect and change its owner-only durable state without changing controller selection:

```bash
redeem slice mode status
redeem slice mode enable
redeem slice mode disable
```

The module exports the shell-inert argv `programs.terminal-redeemer.slice.launchCommand = [ <store-redeem> "slice" "launch" ]` for a consumer-owned Niri Super+Enter binding. It also exports `programs.terminal-redeemer.slice.manageCommand`, exact packaged Kitty argv ending in `<store-redeem> --config <generated-config> slice manage`. A consumer may expand that list into a Niri `spawn` under any locally chosen unused key; for example, after substituting its evaluated store/config paths:

```kdl
// Consumer-owned example only: Mod+Shift+R is not reserved by the contract.
Mod+Shift+R { spawn "/nix/store/…-kitty/bin/kitty" "--config" "NONE" "--class" "terminal-redeemer-slice-manager" "--override" "confirm_os_window_close=0" "--title" "Terminal Redeemer Slices" "-e" "/nix/store/…-terminal-redeemer/bin/redeem" "--config" "/home/USER/.config/terminal-redeemer/config.yaml" "slice" "manage"; }
```

The module never installs this example or any management binding, and the generated Mod+Return/Mod+W fragment is unchanged. When mode is disabled or the exact current static workspace is not selected, `redeem slice launch` directly starts the configured packaged Kitty with empty argv, matching the ordinary local launcher. That decision is completed before any token or remote intent exists.

On a selected workspace with mode enabled, `redeem slice launch` persists one token/session intent before SSH, creates or resumes exactly one host transaction, and hands a committed source identity to the running controller. Lost responses stay on that token. Exhaustion is durable and visible; continue explicitly with:

```bash
redeem slice launch --reconnect-token <64-hex-token>
```

Never compensate for `pending`, `disconnected`, cancellation, or an unavailable handoff by launching Kitty locally: host work may already exist. A definitive host-proven failed/non-created result also requires explicit operator action and terminally resolves controller handoff as `not_created` when no host identity exists. A locally empty/missing/misconfigured SSH destination never impersonates host `token_not_found`; it remains pending/disconnected. The host owns execution, Kitty, and Zellij work; the leech window is only an interactive projection.

For inventory schema 2, deploy one package revision to both ends before activation; a mixed revision must fail negotiation while retaining prior authority and authorizing no disappearance or spatial write. Before any binding rollout, perform a disposable two-machine smoke: verify mode-off and unselected local launch; selected first success; exact source handoff before the next inventory poll; response loss after host creation; repeated/reordered response; delayed projection; controller restart; bounded exhaustion; explicit same-token reconnect; cancellation; host absence; no local process after remote intent; exact workspace placement without focus change; projection close preserving the host session; and rejection of dead/cache-only or prefix sessions. Keep a sentinel unrelated window and confirm no close/move/focus action targets it.

Pinned Zellij 0.44.3 does not support watch. `mirror open --mode watch` returns an explicit unsupported error without constructing or running a command. Legacy attach and mirror snapshot/list/open/status/close remain separate.

The repository-wide [host/leech hermetic acceptance matrix](testing/host-leech-hermetic-matrix.md) maps every safety contract to named tests and runs in the Nix sandbox without a live desktop or network. Its separately listed operator smoke is a deployment gate, not a substitute for the hermetic checks. The complete consumer-facing smoke checklist and rollback evidence requirements are in [HOST_LEECH_READINESS.md](HOST_LEECH_READINESS.md). Keep automated repository validation isolated from live sessions, credentials, socket values, and machine activation.

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
```

`--dry-run` on `open` still acquires/validates the snapshot but does not run Kitty. For fully offline checks, add `--snapshot-file PATH`.

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

The legacy capture/restore milestone intentionally does not add continuous reconciliation, an always-running capture/restore daemon, duplicate-window suppression across repeated legacy `mirror open` calls, or a pane-rich full-screen mirror TUI. The separately shipped opt-in slice controller provides its own foreground reconciliation service and remains disabled by default; it does not change those legacy capture/restore boundaries.
