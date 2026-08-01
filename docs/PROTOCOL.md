# Live source inventory protocol

Terminal Redeemer exposes a versioned, host-authoritative inventory for the opt-in host-leech slice controller. The controller and host-location-only v1 protocol are disabled by default. This protocol is additive to the legacy one-shot `redeem mirror snapshot` payload; it does not replace or reinterpret that unversioned payload.

The inventory protocol discovers eligible **open** host Kitty windows backed one-to-one by verified currently live Zellij sessions. It does not inventory headless or merely resurrectable sessions and never attaches, creates, resurrects, terminates, or prefix-matches a session.

## Operator commands

Initialize source authority once:

```bash
redeem slice inventory init
```

Initialization first durably creates an independent enrollment marker, then writes a random source-host UUID into a separate crash-safe state generation. It may run only when both the marker and current authority are absent. A crash after marker creation remains fail-safe used evidence: existing, corrupt, or missing-after-use authority is refused rather than silently replacing identity.

Request a full snapshot:

```bash
redeem slice inventory snapshot --accept-schema-version 1
```

`--accept-schema-version` is repeatable. With no flag the command accepts the currently supported set. If there is no common version, it returns nonzero before contacting Niri:

```json
{
  "code": "unsupported_schema_version",
  "supported_schema_versions": [1]
}
```

The source state is stored under `stateDir/slice/source-inventory/`, separately from capture checkpoints and `events.jsonl`.

## Compatibility

The [version-1 consumer contract](../contracts/host-leech-slices/v1/consumer-contract.json) records the supported protocol, compatibility, defaults, configuration, helper argv, module, binding, legacy, watch, and fallback surfaces. Its strict schema is packaged alongside identical contract bytes. See [HOST_LEECH_READINESS.md](HOST_LEECH_READINESS.md) for deployment, rollback, and proof requirements.

Version 1 requires all documented required fields and stable enum values. Decoders accept unknown additive object fields. They reject:

- unsupported schema versions;
- duplicate JSON object keys;
- missing required fields or malformed types;
- unknown enum values;
- oversized payloads and strings; and
- semantic invariant violations.

Breaking field or enum changes require a new integer schema version. The legacy mirror snapshot remains unversioned and cannot be decoded as authoritative version 1 inventory. Existing `mirror snapshot`, `mirror list`, and `mirror open` consumers retain their current behavior.

## Complete envelope

```json
{
  "schema_version": 1,
  "source_host_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "observation": {
    "quality": "complete",
    "attempted_at": "2026-07-25T12:00:00Z"
  },
  "authoritative": {
    "source_epoch": "11111111-1111-4111-8111-111111111111",
    "revision": 4,
    "observed_at": "2026-07-25T12:00:00Z",
    "workspace_normalization": "unicode-nfkc-fold-v1",
    "live_session_ids": [],
    "sources": [],
    "conflicts": []
  }
}
```

A complete observation is built from Niri's local direct Unix-socket event replay through successful `ConfigLoaded`, a separate Outputs request, bounded process evidence, and a pinned Zellij 0.44.3 live-socket catalog. `live_session_ids` is a required (possibly empty), sorted, duplicate-free list of every directly verified active session in that catalog, including live headless sessions that are not eligible window-bound sources. Every eligible source session must occur in the list. Catalog-only changes participate in the semantic hash. All workspace/window/output references and the one-active-output MVP topology validate before publication. A join or catalog failure degrades the whole attempt and retains prior authority; it never publishes a partial or empty live list as absence.

`attempted_at` is the completed attempt time. Every successfully completed authoritative poll advances revision and refreshes `observed_at`, even when no semantic inventory changed.

## Eligible sources

Each source contains:

- `source_id`: opaque digest of source epoch and runtime Niri window ID;
- `runtime_window_id`: positive same-epoch evidence only;
- `session`: a distinct opaque verified live-session ID, bounded safe name, and `active` status;
- `workspace`: runtime workspace evidence plus optional static display name and canonical key;
- `output`: output name, logical position/dimensions, scale, and transform; and
- `layout`: `tiled` or `floating`, tile/window size, and exact one-based `(column,tile)` position for tiled windows.

Source IDs are not titles, PIDs, CWDs, timestamps, orders, or raw runtime IDs. A random public source epoch rotates only when a successful complete poll observes a changed private Linux boot identity or Niri socket filesystem identity (device/inode). The private path is used only to locate and `lstat` the socket; another path to the same socket inode does not rotate the epoch. Runtime ID reuse after rotation therefore yields a different source ID.

Workspace normalization version 1 trims Unicode whitespace, applies NFKC, Unicode case fold, then NFKC again. Empty, control-containing, invalid, or oversized keys are rejected. Normalization collisions are explicit conflicts. The pinned v1 tables deterministically map U+02DA to `SPACE + COMBINING RING ABOVE` and Cherokee U+13D7 to U+ABA7; these forms are not generally idempotent, so an already persisted canonical key is compared as authority rather than silently normalized again as a migration.

Live session IDs sort lexically. Sources are ordered deterministically by workspace key (unnamed last), runtime workspace ID, column, tile, then source ID. Conflicts sort by code, source ID, then session ID. Titles never affect identity or ordering.

## Conflicts

A complete inventory can contain per-window conflicts without making an unsafe binding eligible. Version 1 conflict codes are:

- `kitty_process_unverified`
- `process_metadata_invalid`
- `session_candidate_missing`
- `session_candidate_ambiguous`
- `session_name_invalid`
- `session_missing`
- `session_dead_resurrectable`
- `session_prefix_only`
- `session_catalog_duplicate`
- `session_duplicate_binding`
- `session_socket_invalid`
- `workspace_name_collision`

A session catalog alone never creates a source. Two windows cannot share one eligible session, and one window cannot choose among multiple candidates.

## Degraded observations

A degraded attempt is informative only:

```json
{
  "schema_version": 1,
  "source_host_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "observation": {
    "quality": "degraded",
    "attempted_at": "2026-07-25T12:00:01Z",
    "degraded_reasons": [{"code": "niri_replay_timeout"}]
  },
  "authoritative": {
    "source_epoch": "11111111-1111-4111-8111-111111111111",
    "revision": 4,
    "observed_at": "2026-07-25T12:00:00Z",
    "workspace_normalization": "unicode-nfkc-fold-v1",
    "live_session_ids": [],
    "sources": [],
    "conflicts": []
  }
}
```

If prior authority exists, it is retained unchanged. Before the first successful complete observation, `authoritative` is absent. Degraded responses never invent an empty inventory and never authorize disappearance or projection close.

Version 1 degraded reason codes are:

- `niri_socket_unavailable`, `niri_replay_timeout`, `niri_replay_eof`, `niri_config_failed`, `niri_malformed`, `niri_reply_too_large`
- `niri_missing_workspace`, `niri_missing_output`, `niri_invalid_geometry`, `niri_unsupported_topology`
- `source_identity_changed`, `process_observation_incomplete`, `zellij_catalog_unavailable`, `authority_unavailable`, `revision_overflow`

Wire reasons contain stable codes, not raw replies, socket paths, boot IDs, device/inode values, process arguments, environment values, or unsanitized errors.

## Revision and epoch acceptance

Within one source epoch:

- the first successfully completed authoritative poll has revision 1;
- every later successfully completed authoritative poll advances revision by one, including a poll with an unchanged semantic hash;
- a degraded or otherwise unsuccessful poll retains the last committed revision;
- a lower revision is stale;
- the same revision and same semantic hash is an exact idempotent replay; and
- the same revision with different semantics is conflict.

A previously unseen epoch is a full resync. The leech retires the prior epoch and does not compare revision numbers across epochs. A later payload from a retired epoch is replay and is rejected.

A degraded response updates response-health time only and preserves accepted authority. Freshness is based on accepted `{source_host_id, source_epoch, revision}` plus the leech's local receive time. Source `observed_at` is diagnostic, so backward or future host clock skew cannot make a valid received revision fresh or stale.

## Crash and privacy boundaries

The enrollment marker is created exclusively and fsynced before the initial authority generation. Initialization, private fingerprint, public epoch, revision, semantic hash, and committed authority then use one atomic current-state generation. Complete publication occurs only after temp write, file fsync, atomic rename, and directory fsync. The inventory holds its own advisory lock for initialization and the bounded poll/commit transaction. A corrupt marker, corrupt authority, or current state missing after enrollment fails safe.

The public payload never serializes Niri socket paths, socket device/inode values, boot IDs, Zellij socket/cache paths, raw process metadata, raw Niri replies, or private fingerprint inputs.

## Host RPC envelope

`redeem slice rpc` is an additive, one-request JSON-stdin/JSON-stdout host boundary. It is separate from both inventory JSON and legacy mirror commands. Requests are capped at 1 MiB, must be valid UTF-8 with no duplicate/unknown fields, and negotiate schema version 1. The configured request timeout begins before stdin ingestion; timeout closes the one-shot input and cannot be held open by a partial request:

```json
{"schema_version":1,"accept_schema_versions":[1],"request_id":"req-01","verb":"liveness","payload":{}}
```

Every valid request receives a typed response with the same request ID:

```json
{"schema_version":1,"request_id":"req-01","outcome":{"status":"ok"},"result":{"alive":true,"schema_versions":[1]}}
```

Version 1 verbs are `liveness`, `snapshot`, `workspace_ensure`, `launch`, `token_query`, and `token_replay`. `snapshot` returns the complete/degraded inventory envelope above. `workspace_ensure` uses direct Niri IPC, exact runtime workspace IDs, and verify-after-write for routed host launch placement; it is not a general spatial-writeback capability. It rejects normalized-name collisions and does not trust `Handled` as completion.

`launch` requires a bounded safe idempotency token. Routed launches additionally require the exact deterministic `tr-<base32(sha256(token))>` session name and canonical static workspace name. Before any host side effect, the source first durably enrolls a sentinel outside the journal root, then fully writes and file-fsyncs a mode-0600 temporary `pending` transaction containing the stable opaque `host_terminal_id`, transaction source epoch/private fingerprint, session, and workspace, atomically hard-links it to the final digest path without replacement, and fsyncs the private token directory. A sentinel with a missing journal root is missing-after-use authority and returns `token_state_unavailable`; only a valid enrolled journal can prove `token_not_found`. A crash can leave an ignorable temporary name or a complete pending final transaction, never a torn final record.

The routed transaction advances durably through `pending`, `session_starting`, `session_created`, `kitty_starting`, `kitty_started`, `placed`, and `committed`. A fresh pending transaction treats an already-live exact deterministic name as collision; only durable `session_starting` recovery may adopt exact existence. Before any resumed stage performs session, Kitty, workspace, or placement work, the loaded record's transaction epoch/fingerprint must still exactly match the current server epoch/fingerprint; mismatch returns unchanged pending state with zero effects. It creates only the exact deterministic session with pinned `zellij attach --create-background`, using a private empty creation cache, and refuses nonempty/dead cache evidence. It starts one Kitty with a token-derived exact app ID and direct exact live `zellij attach SESSION options --on-force-close detach` argv under an empty shim cache. Replay first adopts at most one exact app-ID/PID Niri window; ambiguity fails closed. It correlates Kitty PID to one Niri window, ensures the exact host workspace, moves that exact window with `focus:false`, and verifies placement. Immediately before every placed-to-committed transition (including replay), it re-resolves graphical context, re-fingerprints boot/Niri socket, collects complete authoritative inventory, and requires the original transaction epoch/fingerprint plus exact window/PID/app/executable/full argv/workspace/session/source tuple. Rotation, incomplete inventory, or mismatch remains pending. Only then does it atomically commit `{token,session,host_terminal_id,source_epoch,source_id,runtime_window_id}`. A private no-follow interprocess transaction lock serializes journal creation/replay. The host journal has a fixed exact-record capacity and fails novel creation explicitly at exhaustion rather than pruning replay authority. A crash or lost response resumes the same stage/session/window proof and cannot create a second session or Kitty.

Only a proven executable pre-start error becomes `failed`; every error after successful process start, cancellation, process death, or unknown transport outcome remains `pending`. `token_query` is observation-only; routed `token_replay` repeats the exact token/session/workspace metadata and resumes the same idempotent transaction. The state directory, `slice`, and token root are owner/mode checked as direct non-symlink directories before every operation. Ordinary RPC cannot erase or replace token state.

RPC outcome statuses are `ok`, `invalid`, `unavailable`, `pending`, `disconnected`, and `failed`. A transport that loses a launch response reports `pending`; exhausted read/query retries report stable `disconnected`. Neither status authorizes a local fallback.

The transport runs packaged SSH directly with fixed, validated, shell-inert RPC command tokens and JSON on stdin. It accepts a response only when the nonempty request ID exactly matches, the status/code combination is known for that verb, and the result has the required verb-specific shape and token identity. Stale, missing-ID, invented-status, and malformed-result responses are ambiguous failures, not authority. Keepalive and bounded retry options are additive. Terminal Redeemer does not set host-key bypasses, known-hosts replacements, authentication material, authorization, agents, or `ProxyCommand`; operator-supplied SSH options remain an explicit trusted boundary.

## Exact attachment boundary

`redeem slice attach` implements the passed Zellij 0.44.3 live-only spike contract. Live sockets and resurrection metadata are under the upstream client/server compatibility directory `contract_version_1`, not the binary version. The wrapper accepts one safe exact session and attachment token, checks the exact executable version and current-user Unix socket, then creates a dedicated, durably marked mode-0700 same-filesystem root and a separately marked `att-*` tree containing a verified hard link to only that socket. It uses an empty shim cache, scrubs all nested-Zellij variables, and executes with the caller's stdin, stdout, stderr, foreground terminal, and cancellation context:

```text
zellij attach SESSION options --on-force-close detach
```

There is no outer shell, prefix selection, attach-or-create, resurrection, title evidence, or normal cache fallback. Private paths are bounded by the 107-byte socket-path budget. Normal exit removes only the freshly created attachment directory. Startup garbage collection first proves the direct root's durable ownership marker, then removes only old, current-user, mode-0700 `att-*` directories carrying their own valid ownership marker; an unrelated prefix-matching directory is retained. A client failure followed by a failed exact Unix-socket connect is typed `unavailable`; otherwise it remains `attach_failed`. Typed statuses are `invalid`, `unavailable`, `setup_failed`, `attach_failed`, `detached`, and `cancelled`.

## Graphical context and privacy

Slice RPC requires the exact order-insensitive set `NIRI_SOCKET`, `WAYLAND_DISPLAY`, and `XDG_RUNTIME_DIR`; subsets, duplicates, and extras are invalid. It obtains those values from the current process or the bounded output of the fixed `systemctl --user show-environment` command, immediately filtering to that immutable set. All three values are bounded and validated before the exact three-key graphical map is passed to Kitty. It does not source login/interactive profiles or inspect logs, credentials, agents, shell history, or arbitrary private files. These values remain process-private and never appear in RPC responses, token state, inventory payloads, or errors.

## Spatial proposal boundary

The [single-monitor Niri spatial policy](adr/0004-single-monitor-niri-spatial-mapping-policy.md) consumes complete inventory spatial fields through the pure `internal/slicelayout` package. It maps canonical static workspace identity, tiled/floating state, and logical-output-relative width/height to typed exact-window proposals. Every exact-window proposal carries target side, opaque source ownership, exact target compositor epoch and same-epoch runtime target ID, controller origin/generation, `focus:false`, and verify-after-write. Ownership binds both host and leech epoch/ID pairs, so a compositor restart invalidates stale ownership even when a numeric ID is reused. Missing workspaces produce an ensure-only generation before any dependent window mutation; source-side ensure scans the whole catalog before action and on every verification poll revalidates exact/canonical uniqueness, unchanged one-output topology, candidate ID/output/index, and a unique later trailing empty replacement.

Production v1 uses only host-location proposals flowing from host authority to the owned leech projection. Column/tile order is used only for initial launch ordering and later drift reporting; it never creates a correction or writeback action. Cross-machine size is always marked approximate because Niri working areas and terminal cell grids are not fully observable. Logical dimension differences and every exact decoded scale difference are reported explicitly; scale has no tolerance.

## Leech controller and local control protocol

The opt-in controller is enrolled explicitly with `redeem slice controller init` and runs in the foreground with `redeem slice controller run`. It holds a singleton advisory lock for its entire lifetime and exposes one owner-only mode-0600 Unix socket under `stateDir/slice/controller/`. State-changing requests and reconciliation are serialized through one strict controller schema-v2 JSON boundary with 1 MiB requests and 8 MiB responses. Supported operations are status, static workspace add/remove, global all-eligible enable/disable, exact-source pickup/pickup removal, close/drop, reopen, undo, explicit reconnect, and routed-launch handoff. `redeem slice manage` polls this same bounded socket and sends only these operations; it is not another authority or effect executor. Internal projection helpers use the same boundary to report exact token-correlated attachment readiness and loss.

Current controller authority uses enrollment-before-current and temp-write/file-fsync/atomic-rename/directory-fsync. Contract v1.1 keeps controller schema 2 and adds optional omitted-false `all_eligible`; old schema-2 authority therefore reads as disabled. Global enable/disable is idempotent and audited but deliberately creates no undo entry. A prior reader rejects active `all_eligible` as an unknown field, so downgrade must disable it through the running v1.1 controller before the service is stopped and the package is replaced. Missing current state after enrollment is not fresh state. It retains accepted epoch/revision/replay protection, selections and exact overrides, epoch-bound source/session facts, projection mappings, original reconnect deadlines, stable disconnected states, source-gone evidence, successor gates, pending spatial origins, unresolved launch handoffs, and bounded audit/undo boundaries. Schema-v2 fixed authority and legacy baseline fields continue to round-trip for compatibility, but the host-authoritative planner does not branch on or consult them. History pruning never removes current gates or intents. Retired epochs and compacted resolved handoff tokens use deterministic exact bounded tombstone sets, never probabilistic filters. When an exact tombstone capacity is exhausted, the novel transition fails closed with an explicit maintenance/re-enrollment requirement rather than rejecting an unrelated valid value by false positive or forgetting replay authority. Terminal source/spatial/lineage records and resolved launch handoffs otherwise compact under fixed caps. Non-prunable active overrides, recoveries, cleanup/successor gates, unresolved lineage, pending launches, or spatial authority fail the transition safely rather than growing state without bound. Projection argv has shared fixed entry-count, per-entry, and 64 KiB aggregate bounds. Transport options have a shared 64-entry/32 KiB aggregate bound that reserves the remaining argv budget for fixed generated flags and bounded executable/host/socket/identity values; the exact built argv is revalidated before preparation. A marshaled current generation larger than the read limit is rejected before rename.

Only accepted higher complete revisions and full resyncs advance manual-drop session absence from `live_session_ids`. A drop is keyed by exact verified Zellij session ID, survives ordinary source replacement and source epochs, and remains effective while the session is live but headless. Explicit reopen or applicable undo clears it early. Otherwise, presence resets its absence count/since/deadline, while absence commits a start and grace deadline on the first accepted missing observation; automatic expiry requires both the configured consecutive accepted complete absences and elapsed committed grace. Tick may expire only after the count threshold is already persisted. Duplicate, stale, conflicting, retired-epoch, degraded, transport-disconnected, per-source conflict, and isolated local-query outcomes cannot advance or clear this evidence. Manual disappearance commits the same session-keyed record only when accepted complete healthy authority still contains the eligible source. Expiry removes only the leech desire override and never closes host work. Source-window retirement and bounded recovery remain separate lifecycles. Reopening never resets an exhausted reconnect budget.

Before launching Kitty the controller atomically persists exactly one mapping for the epoch-scoped source. The mapping contains a unique derived app ID and attachment token. Positive ownership then requires that mapping, exact Niri app ID/window PID, exact persisted PID, the resolved configured packaged Kitty executable identity, and byte-for-byte full persisted argv in its original order with no extra or conflicting arguments; titles are ignored. Close effects re-observe this proof immediately before an exact direct-Niri `CloseWindow`. No action terminates remote Kitty or Zellij work.

Each projection runs packaged Kitty directly with a clean graphical environment and the packaged `redeem slice projection-run` helper. That helper runs packaged OpenSSH directly and requests the remote proven `redeem slice attach` wrapper for only the exact verified session. SSH survival, authentication prompts, banners, and stalls are never readiness. A random nonce is accepted only from the framed readiness marker emitted by the exact packaged wrapper only after the isolated exact-socket Zellij client successfully starts and remains running through the bounded interactive confirmation interval; the marker is removed from terminal output. Positive local ownership and that marker together establish `connected`. It reports attachment loss to the controller, which spends the already-persisted bounded retry episode. Exhaustion is stable `disconnected` until explicit reconnect. There is no attach-or-create, resurrection, prefix, watch, local fallback, or session-termination path.

Epoch replacement permits only ADR 0003's unique same-session lineage during an already active unexhausted recovery. Exhausted evidence creates a durable successor gate; matching new-epoch sources remain wanted but unattachable until explicit reconnect proves uniqueness. Zero-successor active recovery records durable unresolved lineage and retires the old epoch-scoped ID from launch eligibility; later unique exact-session evidence may resolve it without ever relaunching the old ID. Replacement outside recovery installs a durable cleanup barrier: distinct new sources are not evaluated for launch until old owned-window absence or exact close is freshly proven. Incomplete cleanup becomes a visible conflict and leaves the barrier in place across desired reconciliation, explicit reconnect, and every timed retry-launch path, with no override inheritance or duplicate window.

All-eligible projection desire includes eligible unnamed-workspace sources. Those sources can attach but have no cross-machine workspace identity, so the controller skips spatial proposal construction without recording a conflict or retrying a spatial write. The live manager displays them under a synthetic `(unnamed)` group that is not a workspace key.

The v1 controller consumes host-authoritative `internal/slicelayout` proposals. Origins are persisted before effects, every local leech action fresh-reloads controller authority and re-proves the exact mapped epoch/window/PID/app/configured-executable/full-argv ownership immediately before mutation, and local actions use exact direct Niri IDs and verification. Host workspace, tiled/floating state, proportional width, and proportional height converge the owned leech projection; order drift is recorded only. Routed-launch handoff persists an existing token/status/identity monotonically: identities cannot be erased and pending may resolve once to launched or failed but terminal outcomes cannot regress. It performs no new host creation itself; the routed launch command below owns creation and hands the committed identity into this boundary.

Pinned Zellij 0.44.3 has no `watch` command. Legacy `mirror open --mode watch` now returns an explicit unsupported result before acquiring a snapshot or constructing a command; existing attach, snapshot, list, open, status, and close behavior otherwise remains separate.

## Routed Leech launch boundary

`redeem slice mode enable|disable|status` controls and inspects owner-only durable Leech mode state; its distributed and module default is disabled. A separate enrolled marker and atomic mode generation make missing-after-use or symlinked authority fail closed rather than silently re-enabling/reinitializing it. `redeem slice launch` is the packaged argv intended for a future leech Niri Super+Enter binding. This repository exports the command contract but installs no consumer binding.

Before persisting remote intent, the command obtains a direct pinned-Niri snapshot, requires exactly one focused static named workspace with no normalization collision, and checks that workspace against explicit selected-workspace authority. Global `all_eligible` is projection policy only and is not consulted for routed launch. Mode-off and unselected decisions invoke the packaged local Kitty command directly with its unchanged empty argv. Those are the only local-launch branches and occur before any token is minted or remote call attempted.

For a selected workspace in Leech mode, the command first persists one random 256-bit token, its deterministic collision-resistant path-bounded session, workspace, absolute retry deadline, and status. Local intent files have a fixed cap: only oldest terminal launched/failed records compact, while pending/disconnected records are non-prunable and capacity exhaustion fails explicitly. It sends `launch` once, then only `token_replay` for the same token/session during the bounded exponential window. A routed successful response is accepted only with all eight exact fields and a positive runtime ID whose derived `source_id` matches `{source_epoch,runtime_window_id}`; legacy three/five-field success is incomplete and remains pending. Committed host/source/runtime identity is sent directly through the serialized controller handoff. The controller keeps it pending until complete retained authority matches the exact epoch/source/runtime/session/workspace tuple and never reconnects a mismatched source. Lost/duplicate responses, delayed source publication, cancellation, and transport ambiguity retain the same intent; exhaustion becomes durable `disconnected`. `redeem slice launch --reconnect-token TOKEN` starts a new bounded episode for that exact intent without minting another token or session; for an already committed source its repeated handoff restarts the controller's exhausted projection recovery for that exact eligible desired source. A proven host `failed`/missing-token outcome requires explicit user action. Local missing/empty/misconfigured SSH destination is not host proof and remains pending/disconnected. Definitive pre-start noncreation is handed to the controller as terminal `not_created` without a host/source identity or source action, so no stale pending handoff remains. No path after intent persistence invokes local Kitty.

The host owns the Kitty, Zellij session, execution, and work. The leech Kitty is only an interactive exact-live projection. There is no attach-or-create fallback, dead-session resurrection, prefix matching, shell/profile evaluation, remote termination, or automatic local fallback.

## Scope

V1.1 includes the opt-in single-pair controller, additive all-eligible/workspace/pickup selection, controller-backed live manager, strict local control protocol, crash-safe lifecycle/retry/absence/successor authority, exact projection ownership and live-only attachment launch, focused-close helper, and verified host-authoritative projection execution. It remains disabled by default and does not install a consumer routed-launch keybinding. Raw event forwarding, watch mode, clipboard synchronization, multi-monitor mapping, and host spatial writeback are unsupported.
