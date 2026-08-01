# ADR 0002: Define the host-leech terminal-slice domain and lifecycle

- **Status:** Accepted for v1 (disabled by default)
- **Date:** 2026-07-25
- **Decision owners:** Terminal Redeemer maintainers

## Context

When this ADR was proposed, Terminal Redeemer had an explicit, one-shot live-mirror workflow and a separate prior-boot resume workflow, but neither defined the continuous selection, ownership, recovery, or spatial semantics required to project selected live host terminals onto another machine safely. The common domain contract below preceded the protocol, persistence, controller, and routed-launch implementation.

This ADR originated as the contract rather than a claim about the legacy mirror implementation. The final v1 amendments below now describe the opt-in controller, revisioned protocol, routed launch implementation, and host-authoritative spatial convergence. [ADR 0005](0005-global-slice-selection-and-live-management.md) adds the v1.1 global inclusion reason and live manager without changing ownership or attachment semantics.

## Decision drivers

- Keep the machine running the work authoritative for windows, sessions, and processes.
- Attach only to an exact, verified live Zellij session; never create or resurrect one as a side effect of projection.
- Discover eligible open terminals without an operator publication step while keeping selection explicit and understandable.
- Make a local projection close safe and persistent without affecting host work.
- Bound automatic recovery and expose stable outcomes.
- Preserve one host creation intent across uncertain routed-launch responses and retries.
- Recreate useful Niri semantics across different client and output sizes without promising pixel identity.
- Admit incomplete observations without treating them as evidence that work disappeared.

## Decision

### 1. Domain vocabulary and authority

For the terminal-slice domain, these terms have one meaning:

- The **host (workhorse)** is the source and execution authority. Niri, source Kitty OS windows, Zellij servers and sessions, and agent processes execute there. Lattice currently fills this role.
- The **leech (workstation)** renders locally owned Kitty projections and supplies terminal interaction. It does not acquire ownership of host execution, windows, sessions, or processes. Overton currently fills this role.
- Lattice and Overton are deployment examples, not durable identities. The word “host” here is a slice-domain role, not an existing configuration/history label and not, by itself, a protocol identity.
- A **source terminal** is the association between one open host Kitty OS window and one Zellij session. Source-window identity and Zellij-session identity remain distinct even when an eligible association binds them one-to-one.
- A **projection** is a Terminal Redeemer-owned leech Kitty window, an exact live-only interactive Zellij attachment within it, and its semantic Niri placement. It is not a copy or restoration of the session.
- **Leech mode** is an implemented opt-in policy that enables automatic projection from selected workspaces and routing of launches selected by workspace policy to the host. It and the controller remain disabled by default; when Leech mode is off, existing local launch and window behavior is unchanged.
- **Ownership** is deliberately split: the host always owns source/session/process lifecycle; Terminal Redeemer owns only the leech projection it created and its slice-selection state. Spatial authority may be delegated as described below without transferring work ownership.

An **interactive attachment** is a terminal-protocol Zellij client connected to one exact, already-live session. It carries terminal rendering and input. Projection is semantic terminal attachment and compositor placement, never screen, pixel, or video streaming.

### 2. Eligible resources, the share, and the live slice

An **eligible source terminal** is a currently open host Kitty OS window bound one-to-one to exactly one verified active/live Zellij session. Every eligible source is discoverable automatically. There is no host publish step or per-window publish allowlist.

A missing, dead-but-resurrectable, ambiguous, duplicated, incompatible, or otherwise unsafe Kitty-to-Zellij binding is a conflict or ineligible source, not an alternate inventory path. Headless Zellij sessions are not initial resources. Eligibility never grants the leech permission to create, resurrect, or terminate a session.

The **share** is the discoverable set of all eligible sources. “Share” means eligibility and discoverability; it does not mean screen sharing, publication, ownership transfer, or a complete authorization design.

The **live slice** is the derived desired set of eligible source identities and their projection intents. It is not the set of already-existing projection windows. In v1.1, an eligible source is desired when global all-eligible selection is active, its case-normalized static workspace name is selected, **or** it has an exact per-source **pickup** inclusion, unless a manual **close** exclusion keyed by that source's exact verified Zellij session identity removes it:

```text
(all eligible OR selected static workspace OR pickup inclusion) AND NOT close exclusion
```

A live slice is initially a computed policy and result, not a required named, persisted, or reusable collection. Detailed persistence and conflict rules remain for later decisions.

The selection actions are:

- **All eligible:** include every current and future eligible source, including sources on unnamed host workspaces. It controls projection desire only and does not broaden routed-launch workspace selection.
- **Pickup:** explicitly include an eligible source outside the selected workspaces. Pickup removal removes only that exact positive reason.
- **Close** (sometimes described as “drop”): detach and remove only the local projection and retain a `closed_by_user` exclusion for that exact verified Zellij session. It survives source replacement and source epochs while that session remains live. It does not remove a source from the share or erase its underlying global/workspace/pickup selection.
- **Reopen:** resolve a current source to its verified session and clear that session’s manual-close exclusion so its underlying global, selected-workspace, or pickup reason can resume requesting projection. Reopen is not a second inclusion reason. It requests exact live attachment only when that source is still eligible and still selected by global, workspace, or pickup policy; it never means Zellij resurrection.
- **Undo:** reverse the most recent eligible local slice-selection action, such as close to reopen or pickup to removal of that override. Undo never reverses a host window, process, or session effect. The original decision deferred history depth, retention, persistence format, and command shape; the v1 controller implements bounded choices without making them part of this domain ADR.

### 3. Projection ownership and exact interactive attachment

The host remains authoritative while host and leech clients are attached concurrently. The leech projection is another interactive client, not a transferred session. Concurrent use and different client sizes are accepted for MVP, including Zellij’s shared minimum-grid behavior. Therefore a smaller client may constrain the shared terminal grid; independent grids and pixel-identical presentation are not promised.

Super+W, or any other local projection close, acts only on the Terminal Redeemer-owned leech Kitty window and detaches that leech Zellij client. It must never close the host Kitty window and must never terminate, create, or resurrect the host Zellij session. The eligible source remains in the discoverable share. Polling or reconciliation must not automatically request projection while the exact session remains live and carries a `closed_by_user` exclusion. Explicit reopen or applicable undo clears that exclusion early; otherwise it expires automatically only after bounded consecutive accepted complete session-absence evidence plus the committed grace deadline.

An attachment must select the exact verified live session. A missing session, a stale socket, a source-epoch change, or attachment failure cannot fall through to a similarly prefixed session and cannot use resurrection metadata. Reconnecting rebuilds exact-session verification; it does not relax identity.

### 4. Lifecycle and connection-state axes

Host source lifecycle, leech desire, attachment connection, and routed launch intent are separate axes. They must not be collapsed into one enum:

1. **Host source lifecycle:** a source is eligible/discoverable while its host Kitty window remains open and is bound exactly to a verified live Zellij session. Host source close or source-epoch replacement is host-authoritative. A conflict or source-gone result is not a connection state.
2. **Leech desire/override:** a source is selected by workspace or explicitly included by pickup unless it is explicitly excluded as `closed_by_user`. Reopen only clears that close exclusion; it does not add another inclusion reason.
3. **Attachment connection:** an intended projection’s connection is `connected`, bounded `reconnecting`, or stable `disconnected`.
4. **Routed launch intent:** an uncertain host creation result is `launch_pending` for the same idempotency token. `launch_pending` is neither an attachment connection state nor an unqualified generic pending state.

The required operator-visible labels retain their axis meanings; they are not members of one state enum:

| Axis | Label | Meaning and next behavior |
|---|---|---|
| Attachment connection | `connected` | The projection has a confirmed exact live interactive attachment. Unexpected loss changes only this axis to `reconnecting`. |
| Attachment connection | `reconnecting` | Automatic recovery attempts for the same source/session identity are active within a bounded retry window. Recovery returns this axis to `connected`; exhaustion changes it to stable `disconnected`. |
| Attachment connection | `disconnected` | The bounded automatic retry window was exhausted. No automatic connection attempts continue. Explicit reconnect restarts bounded attempts for the same identity only when it remains desired and eligible. |
| Leech desire/override | `closed_by_user` | The user intentionally closed the local projection and the exact-session close exclusion remains in effect while that session is live. Explicit reopen or applicable undo clears it early; otherwise bounded confirmed session absence plus grace expires it automatically. Reconnect alone must not clear it. |
| Routed launch intent | `launch_pending` | The host creation response is uncertain. Resolution and connection attempts remain tied to the same token and must not create duplicate or fallback-local work. |

An intentional local close sets only the leech-desire axis to `closed_by_user` and detaches/removes the projection. It does not transition the connection axis to `closed_by_user`; after detachment there is no active attachment connection for that projection intent. The axes can otherwise be reported together: for example, `launch_pending` may coexist with `disconnected` after bounded automatic attempts are exhausted. The original ADR deferred representation; the implementation uses inventory/RPC schema 1 and controller schema 2 boundaries.

Retry durations, schedules, and counts are intentionally not selected here, but they must be finite. Attachment retry exhaustion stops automatic attempts until explicit reconnect. Reconnect addresses attachment loss; reopen addresses a manual close. Neither may create or resurrect a session.

Source-gone, conflict/ineligible, and **degraded** remain outcomes on their respective source/observation/operation dimensions, not connection or desire labels. A source disappearance becomes authoritative only under the implemented controller's complete-snapshot grace semantics. A degraded or incomplete inventory preserves the last authoritative source result and never authorizes projection closure.

### 5. Spatial projection, fidelity, and location authority

A **spatial projection** semantically maps a source’s named workspace, tiled/floating state, proportional width and height, and best-effort initial order onto the leech. MVP maps one active host output to one active leech output. Multi-monitor topology is not inferred.

**Fidelity** describes how completely those semantic properties match. Different logical dimensions, bars, exclusive zones, client grids, and compositor state can make an exact geometric match impossible. Proportional cross-machine approximation is a normal successful fidelity outcome when every requested MVP semantic property is applied. A **degraded outcome** is instead a typed and visible result in which an observation or attachment is incomplete or failed, or a requested semantic property is incomplete, failed, or unapplied. Degraded is not success and is not proof that the source disappeared.

MVP spatial fidelity includes:

- named workspace membership;
- tiled or floating state;
- proportional width and height;
- initial `(column, tile)` order where practical; and
- observation and reporting of later order drift.

Exact continuous order correction is stretch-only. The system must not use visible, racy focus-dance operations to imply exact live order synchronization in MVP.

Location authority belongs to the host. Host workspace, tiled/floating state, proportional width, and proportional height continuously converge the owned leech projection. Leech divergence in those supported properties is reverted and never written back to the host; order drift remains report-only. Host-target spatial mutation is outside the controller model.

### 6. Routed-launch idempotency invariant

When Leech mode and workspace policy route a terminal launch to the host, one persisted idempotency token denotes one host-terminal creation intent and exactly one host-terminal creation. After creation, repeated connections target that same host Kitty window and Zellij session. A retry or reconnect must query for or attach to the result of that same intent. It must not mint another creation merely because a response was lost.

An uncertain host outcome sets the separate launch-intent axis to `launch_pending`. Bounded resolution or connection attempts may exhaust while that launch intent remains tied to the same token, with the attachment-connection axis then stable `disconnected`. `launch_pending` never triggers automatic local fallback. Automatic fallback could duplicate work after a host creation whose response was lost. A normal local launch remains appropriate only when Leech mode is off or the workspace policy does not route that launch.

This domain ADR deliberately does not choose a token format, storage representation, request/response fields, command name, or timeout. The v1 implementation supplies those details and routed launch while preserving this invariant; it remains disabled unless an operator separately enables Leech mode and the controller.

### 7. Observation boundary

A host-local controller may consume Niri’s event stream to maintain timely local state. The leech-facing MVP protocol remains bounded polling of revisioned full snapshots, not forwarding a raw event stream.

Only a complete joined observation may advance authoritative source inventory: initial event-stream replay must pass its barrier, Outputs must be joined, references and required geometry must validate, and the one-active-output topology must hold. An incomplete, dangling, timed-out, or malformed observation is degraded and retryable. It can be reported, but it cannot authorize destructive disappearance or local projection close.

The original ADR deferred the protocol schema, revision fields, polling interval, disappearance grace, and persistence layout. They are implemented by the v1 protocol/controller and documented in [the protocol](../PROTOCOL.md), while remaining outside this domain ADR's normative scope.

### 8. Prior-boot resume boundary and legacy compatibility

Prior-boot resume and live host-leech projection are separate domains:

- `redeem resume`, governed by [ADR 0001](0001-resume-zellij-terminals-in-niri.md), reconciles durable dead or prior-boot captured state and may use Zellij’s permitted prior-boot recovery behavior.
- A live slice attaches only to currently open host Kitty windows backed by verified currently live Zellij sessions.

Neither domain is fallback behavior for the other. Live projection never resurrects prior-boot state, and resume does not supply connection recovery or local fallback for a live projection.

The proven legacy behavior preserved during migration is one-shot **interactive attach** to an exact live session. Pinned Zellij 0.44.3 has no supported `watch` subcommand. Read-only/watch projection is unsupported, and the implemented legacy command now rejects `--mode watch` before acquiring a snapshot or constructing an attachment command.

## Failure behavior

- Unsafe, ambiguous, duplicated, dead, or missing source bindings are visible conflicts/ineligible resources, not attach targets.
- Exact-attachment setup or client failures produce typed outcomes; they cannot fall through to creation, resurrection, or a prefix match.
- Unexpected connection loss enters bounded `reconnecting`. Exhaustion enters stable `disconnected` and automatic attempts stop until explicit reconnect.
- Intentional local close sets the leech-desire exclusion to `closed_by_user`, detaches/removes only the local projection, and suppresses automatic reopen. It does not transition the attachment-connection axis.
- A degraded observation retains the last authoritative inventory and cannot authorize closure. Implemented confirmed source-gone handling remains distinct from transport disconnect and requires complete-snapshot grace semantics.
- A routed launch with an uncertain response remains tied to the same idempotency token and sets launch intent to `launch_pending`; bounded attempt exhaustion is reported separately as attachment `disconnected`. It never duplicates the host intent or automatically launches locally.
- Spatial mutation success must be verified from bounded subsequent observation rather than inferred from a handled response. A verified proportional approximation is successful when every requested MVP semantic property was applied; an incomplete, failed, or unapplied requested property is degraded.

## Consequences

### Positive

- Host work remains safe under leech close, failure, retry, and reconnection.
- Eligibility and live-slice selection have distinct, inspectable meanings without a publish ceremony.
- Exact live-only attachment prevents resurrection and prefix ambiguity.
- Separate lifecycle axes prevent incomplete inventory from masquerading as transport or user intent.
- Revisioned full snapshots give the remote boundary a recoverable, inspectable model while permitting host-local event use.
- Routed launch has a no-duplicate invariant even under uncertain responses.

### Negative

- Concurrent attachments can expose Zellij’s shared minimum-grid constraint, so either client may affect the usable grid.
- Cross-machine spatial fidelity is approximate when output geometry or working areas differ.
- A manual close requires explicit reopen or undo while its exact session remains live and selected; only bounded confirmed session absence plus grace expires it automatically.
- Bounded recovery can leave stable disconnected projection intents requiring operator action.
- Exact live order correction, read-only watch, headless inventory, and multi-monitor mapping remain unavailable in MVP.
- Read-only/watch projection remains unsupported; the legacy command fails it explicitly rather than constructing unsafe behavior.

## Alternatives considered

### Require host publication or named slices

Rejected for MVP. Every safe open Kitty/live-Zellij binding is discoverable, and the live slice is computed from selected static workspaces plus per-source overrides. Named reusable slices may be reconsidered later.

### Inventory every headless or resurrectable Zellij session

Rejected. The initial resource is an open host Kitty window with one verified live session. Expanding inventory would weaken window identity and live-only safety.

### Stream the host screen or video

Rejected. Terminal-protocol attachment plus semantic Niri projection preserves interactive terminal behavior without pixel streaming.

### Retry indefinitely or reopen immediately after local close

Rejected. Infinite retry has no stable failure state, and reopening a still-live session on the next poll defeats explicit user intent. Recovery is bounded; reopen/undo clears a close early, while bounded confirmed session absence plus committed grace expires it automatically.

### Fall back to a local launch after an uncertain host response

Rejected. The host may already have created the terminal, so local fallback can duplicate work.

### Continuously force exact order using focus dance

Rejected for MVP. Pinned Niri lacks an ID-targeted existing-column move, making the workaround visible and racy. Observe and report drift instead.

### Claim Zellij watch as the projection mechanism

Rejected. Pinned Zellij 0.44.3 has no supported `watch` subcommand, and the executable attachment proof establishes interactive attach only.

## Compatibility and sequencing

Downstream implementation covers persistence and override conflict semantics, source identity and the revisioned protocol, transport and packaging, spatial policy details, controller reconciliation, routed launch, and adversarial tests. The host-location-only v1 model preserves these invariants and remains disabled by default.

Existing one-shot interactive attach behavior remains available during opt-in deployment. Unsupported watch behavior is not part of that compatibility promise. Consumer configuration and enablement remain explicit operator actions.

## Appendix A: Executable spike evidence

This appendix records the spike evidence that constrained the decision. The spike alone does not prove the protocol or controller; the implementation and hermetic matrix provide that separate evidence.

### A.1 Exact live-only Zellij attachment

[Spike 0001](../spikes/0001-zellij-live-only-attachment.md) passed against repository-pinned Zellij 0.44.3 using [`scripts/spikes/zellij-live-only-attachment.sh`](../../scripts/spikes/zellij-live-only-attachment.sh).

The executable proof establishes that Zellij 0.44.3 discovers live sockets and resurrection metadata under `contract_version_1`, and that an exact interactive attachment requires a private, per-attachment socket directory on the same filesystem; a hard link to the exact live socket with device/inode verification; an isolated empty cache; scrubbed nested-Zellij environment variables; and explicit `options --on-force-close detach`. It demonstrates that:

- a symlink is ignored because the enumerated entry is not itself a socket;
- the empty cache blocks resurrection while the normal cache can resurrect the dead control fixture;
- private exact-socket isolation prevents unique-prefix fallback after the exact session disappears;
- the detach override defeats hostile `on_force_close "quit"` configuration; and
- terminating the leech client and cleaning its private hard link leaves the host session alive.

The implemented wrapper outcome categories derived from this proof are `invalid`, `unavailable`, `setup_failed`, `attach_failed`, `detached`, and `cancelled`. They classify attachment-wrapper results and do not replace the attachment-connection states `connected`, `reconnecting`, and `disconnected`; they also do not replace the separate leech-desire label `closed_by_user` or launch-intent label `launch_pending`. A missing or dead session must never become a create or resurrect request.

Residual limitations retained from the proof are:

- hard links require the private root and real socket to share a filesystem;
- SIGKILL can leave stale attachment directories, requiring bounded owner/prefix-checked garbage collection;
- server and client must match the exact pinned Zellij version;
- the complete Unix socket pathname must fit the 107-byte budget (`sun_path` has 108-byte storage including the terminating NUL);
- the isolated shim cache must never be used to start a server; and
- session death after link creation can still fail attachment, although it cannot select or resurrect another session.

This is evidence for one-shot **interactive attach only**. It does not test concurrent-client minimum-grid behavior, which is an accepted product constraint rather than a spike-proven result. It does not prove read-only behavior, and pinned Zellij 0.44.3 has no supported `watch` subcommand.

### A.2 Niri inventory and safe spatial mutations

[Spike 0002](../spikes/0002-niri-direct-ipc.md) passed against repository-pinned Niri 26.04 and Kitty 0.45.0 using [`scripts/spikes/niri-direct-ipc.sh`](../../scripts/spikes/niri-direct-ipc.sh) and [`scripts/spikes/niri-direct-ipc-probe.py`](../../scripts/spikes/niri-direct-ipc-probe.py); production IPC behavior is covered in `internal/niriipc`.

The executable proof establishes that:

- initial inventory uses event-stream replay through `ConfigLoaded` as its barrier, while Outputs is requested on a separate connection;
- workspace/window/output cross-references, required geometry, and exactly one active output must validate before authority advances;
- dangling or incomplete joined state is degraded and retryable and never authorizes projection close;
- a private fingerprint over Linux boot ID and Niri socket device/inode detects instance rotation and supports rotating a persisted random public source epoch without exposing raw socket metadata;
- exact-ID workspace moves can preserve focus, trailing-empty workspaces can be named, and tiled/floating plus percentage-size actions are viable only with bounded verify-after-write; and
- Niri exposes `(column, tile)` for observation, but exact movement to an existing column has no ID target and would require visible, racy focus dance, so exact live order is outside MVP.

The source-epoch experiment supports an identity boundary but does not itself define protocol fields or schemas. The implemented v1 protocol separately defines the host-local event/replay boundary and leech-facing revisioned full snapshots fetched by bounded polling.

Residual limitations retained from the proof are:

- Outputs and event-stream state can race across their separate requests;
- handled action replies can be silent no-ops;
- Niri does not expose working-area dimensions directly;
- exact order mutation is focus-dependent;
- Niri IPC remains version-coupled even when additive fields are tolerated; and
- nested-winit proof cannot reproduce every physical-output or exclusive-zone behavior.

## Validation criteria

Acceptance review confirms that:

- every defined term is used consistently and projection is never described as screen/video streaming;
- source eligibility requires one open Kitty window and exactly one verified live Zellij session;
- host ownership and local-close safety are unconditional;
- source lifecycle, local desire (`closed_by_user`), attachment connection (`connected`, `reconnecting`, `disconnected`), degraded observation, and routed launch intent (`launch_pending`) remain distinct;
- recovery is bounded and exhaustion requires explicit reconnect;
- routed launch retains one identity and never automatically falls back locally;
- the MVP fidelity boundary and exact-order stretch goal are explicit;
- the spike evidence and every residual limitation above remain linked and accurately scoped; and
- historical deferrals are marked as such, and the host-authoritative protocol/controller/routed launch are implemented but disabled by default.

## Non-goals

- Screen or video streaming.
- Initial inventory or projection of headless Zellij sessions.
- Projection of arbitrary GUI applications.
- Clipboard synchronization in the first rollout.
- Multi-monitor topology or mapping.
- Named, reusable slices in the initial model.
- Exact live order synchronization.
- Read-only/watch projection with pinned Zellij.
- Defining protocol fields, retry timings, persistence formats, command names, or undo-history depth in this ADR.
- Editing mono-nix or consumer configuration.
- Activating the feature on the host or leech.
