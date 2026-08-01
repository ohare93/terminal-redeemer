# ADR 0004: Single-monitor Niri spatial mapping policy

- **Status:** Accepted for v1 (disabled by default)
- **Date:** 2026-07-25
- **Decision owners:** Terminal Redeemer maintainers

## Context

[ADR 0002](0002-host-leech-terminal-slice-domain-and-lifecycle.md) establishes semantic spatial projection. [ADR 0003](0003-terminal-slice-workspace-sharing-and-persistence.md) establishes canonical static-workspace identity, exact source identity, atomic controller authority, and safe failure semantics. The passing [Niri direct-IPC spike](../spikes/0002-niri-direct-ipc.md) proves the exact inventory and mutation shapes available in pinned Niri 26.04.

This ADR fixes the deterministic spatial policy for one host output and one leech output. It also introduces the pure `internal/slicelayout` proposal model used by reconciliation; the policy model itself performs no machine enablement.

## Decision

### 1. Topology and identity

MVP accepts exactly one active output on the host and one active output on the leech. Mapping is implicit and one-to-one. A second active output, a source window outside the active output, an incomplete output join, or invalid logical geometry is a typed unsupported/degraded outcome and authorizes no spatial write. Docked, multi-monitor, rotated-topology, and output-name mapping are future work.

A static named Niri workspace is the only cross-machine workspace identity. Names use protocol normalization `unicode-nfkc-fold-v1`: trim Unicode whitespace, NFKC, Unicode case fold, then NFKC. The controller carries the display spelling but compares the canonical key. Unnamed workspaces are not cross-machine identities. Duplicate runtime workspaces with one key and distinct spellings that normalize to one key are explicit `workspace_normalization_collision`; duplicate exact names at distinct runtime IDs are `workspace_duplicate`. Neither permits an arbitrary target.

Source IDs remain opaque and epoch-scoped. Host and leech Niri window/workspace IDs are same-instance, same-epoch mutation targets only. They are never compared across machines or persisted as cross-epoch identity. Every exact-window proposal names its opaque source, exact target side, exact target compositor epoch, and exact current runtime window ID. Positive projection ownership must bind that source to both current host and leech compositor epochs and runtime IDs before any existing window is moved or resized. A compositor restart invalidates that ownership even if the new instance reuses the same numeric ID; ownership must be re-established against the new epoch. Titles, app IDs, workspace positions, and session-name similarity are not ownership proof.

### 2. Complete spatial observation

For each eligible source, a complete observation carries:

- canonical and display workspace name;
- tiled or floating mode;
- exact one-based source `(column,tile)` for tiled windows;
- observed window width and height;
- active output logical x/y/width/height, scale, and transform; and
- the source epoch and same-epoch runtime window ID.

The underlying Niri observation uses direct EventStream replay through explicit `ConfigLoaded.failed:false`, then a separate Outputs request and complete-reference validation. A malformed, dangling, timed-out, topology-incompatible, or otherwise degraded observation preserves the prior accepted spatial authority. It proposes no ensure, move, float/tile, resize, order, focus, close, or ownership action.

### 3. Workspace creation and membership

When a desired canonical workspace already exists exactly once on the target, a move proposal carries that workspace's current runtime ID. When it is absent, reconciliation first requests `workspace_ensure` and performs no dependent window mutation in that proposal generation.

Workspace creation follows the proven exact contract:

1. validate one active output and all joins;
2. reject duplicate and normalized-colliding named workspaces;
3. select the only highest-index unnamed workspace with no windows on that output;
4. send direct Niri `SetWorkspaceName` with that exact workspace ID;
5. do not focus any workspace or window;
6. on every bounded verification poll, rescan the full named catalog for exact duplicates and canonical collisions, revalidate the one-active-output topology, and require the same candidate ID, output, and index;
7. accept only when that same ID has the exact requested display name and the unique highest-index replacement is a later unnamed empty workspace on the unchanged output; and
8. treat timeout, silent no-op, candidate/output/topology change, duplicate, or collision as degraded/failed rather than completion.

A `Handled` reply proves request acceptance only. It never proves the mutation occurred.

Existing-window membership uses the exact pinned shape and preserves focus:

```json
{"Action":{"MoveWindowToWorkspace":{"window_id":42,"reference":{"Id":9},"focus":false}}}
```

Verification requires the same exact window ID on the exact target workspace and no unrelated focus change. The controller never moves a window absent positive ownership.

### 4. Proportional size and layout state

The shared size is a percentage of the source output's logical dimensions:

```text
width_percent  = clamp(1, 100, source_window_width  / source_logical_width  * 100)
height_percent = clamp(1, 100, source_window_height / source_logical_height * 100)
```

The pure policy rounds to four decimal places. The same percentages are requested on the target, not raw pixels. The pinned direct actions are:

```json
{"Action":{"MoveWindowToFloating":{"id":42}}}
{"Action":{"MoveWindowToTiling":{"id":42}}}
{"Action":{"SetWindowWidth":{"id":42,"change":{"SetProportion":50}}}}
{"Action":{"SetWindowHeight":{"id":42,"change":{"SetProportion":50}}}}
```

Each action is ID-targeted and every requested property is verified from bounded complete follow-up observation. Batches never include a focus action. A failure can leave a partially applied placement, which is reported as degraded with per-property evidence; it never changes Zellij/session/source ownership and never authorizes destructive cleanup.

Niri does not expose working-area dimensions. Bars and exclusive zones can therefore change the effective percentage. Kitty font metrics, padding, decorations, and concurrent Zellij clients can also produce different terminal cell grids. Consequently every cross-machine mapping reports `approximate` with `niri_working_area_unobservable` and `terminal_cell_grid_may_differ`; logical dimension differences add an explicit reason, and any exact difference between the validated decoded positive scale values (including `1.0` versus `1.005`) adds `output_scale_differs`. There is no hidden scale tolerance. Approximate is a successful fidelity class only after all requested MVP properties verify. It is not a degraded observation.

### 5. Initial order and drift

The host's exact one-based `(column,tile)` is observation, not a durable cross-machine ID. Initial projection launch intents are sorted by `(column,tile,source_id)`, with floating/no-position sources after tiled sources. The controller preserves this initial launch order where practical and records the resulting observed order.

Later host/leech order differences are reported deterministically by source ID as order drift. No correction proposal is emitted. Pinned Niri has no ID-targeted action for moving an existing column; `MoveColumnToIndex` acts on focus. The visible, racy focus dance needed for exact reorder is excluded from MVP.

### 6. Host-location mode

Host-location is the only shipped v1 mode. The latest accepted complete host workspace, layout mode, proportional width, and proportional height seed the initial projection and remain authoritative. Every observation proposes convergence of divergence on only the positively owned matching leech projection, so local leech changes in those four supported properties are reverted. They are never written back to the host. Exact order remains diagnostics-only.

Every host-to-leech proposal carries:

- target `leech`;
- opaque source identity, exact current leech compositor epoch, and exact current leech runtime window ID;
- origin controller ID, durable generation, `from=host`, fixed compatibility `mode=host_location`, and cause;
- only workspace/layout/width/height intents;
- `focus=false`; and
- `verify_after_write=true`.

### 7. Pure proposal boundary

`internal/slicelayout` contains the pure host-authoritative spatial policy. It:

- validates workspace catalogs and spatial observations;
- computes bounded proportions and fidelity reasons;
- creates typed non-focus proposals bound to the exact target compositor epoch/runtime ID, origin, and verification requirements;
- gates every existing-window proposal on epoch-bound ownership;
- sorts initial order and reports drift without creating order corrections.

It performs no Niri I/O, process execution, attachment, window creation/closure, persistence, transport, retry scheduling, or focus operation. Production translates only host-authoritative proposals targeting the owned leech projection into typed `internal/niriipc` actions. Host-target proposals are rejected.

## Failure behavior and non-disruptive invariant

For any validation, transport, action, timeout, silent no-op, or verification failure:

- Zellij ownership and exact attachment identity are unchanged;
- host Kitty/Zellij/process lifecycle is unchanged;
- no source or projection is closed;
- no unrelated Niri window or workspace is mutated;
- no focus action is issued;
- raw IDs are not reused across epochs;
- prior complete spatial authority is retained; and
- the outcome identifies failed/unapplied properties or conflicts rather than claiming convergence.

Spatial placement failure is never evidence that a source disappeared and never authorizes local fallback or duplicate creation.

## Hermetic fixtures

`internal/slicelayout/testdata/equal-resolution.json` proves a 960x540 source window on 1920x1080 maps to 50% by 50% on an equal logical output while still reporting the unknown working-area/cell-grid approximation.

`internal/slicelayout/testdata/differing-resolution.json` proves the same source maps to 50% by 50% on a 1440x900 scale-1.5 target and reports dimension and scale differences. Unit tests cover production v1 normalized collisions, exact workspace ensure sequencing, floating/tiled changes, initial order, later drift, unrelated IDs, degraded observations, and non-focus proposal invariants.

## Live operator smoke criteria

Before enabling the feature, run the locked spike and a bounded controller smoke in disposable named workspaces on one physical/nested output per machine:

1. Verify packaged Niri prints exactly `niri 26.04 (Nixpkgs)`; build `checks.x86_64-linux.host-leech-hermetic-matrix` for the locked version and production Go IPC checks, then use the documented spike script for a live existing-Wayland smoke.
2. Capture complete host and leech observations and confirm exactly one active output on each, valid logical dimensions/scale, and no canonical workspace collision.
3. Request a missing hostile-but-valid static workspace name; inspect the exact ID-targeted `SetWorkspaceName`, unchanged focus, exact-name verification, and replacement trailing empty workspace.
4. Project one owned tiled terminal, then one owned floating terminal. Verify exact workspace IDs, `focus:false`, mode, width and height after each action. Keep an unrelated sentinel window focused and prove it never moves or closes.
5. Repeat with equal logical dimensions, then differing logical dimensions/scale or bars. Confirm percentage intent and visible approximation reasons rather than pixel-equality claims.
6. Launch multiple projections in source `(column,tile)` order, rearrange one afterward, and confirm drift is reported without a focus dance or correction.
7. Rearrange only the leech and prove no host action occurs. Change the host and prove only the owned matching leech window receives a verified proposal.
8. Prove configuration and controller state expose no alternate location authority, and every host-target proposal is rejected without a Niri mutation.
9. Inject Niri timeout, silent no-op, topology change, and lost ownership. Confirm degraded/conflict outcomes, no unrelated focus/move/close, no Zellij lifecycle effect, and deterministic continued host authority.

Record exact requests and sanitized outcomes. Never record Niri socket values, credentials, raw process metadata, or private graphical context.

## Consequences

### Positive

- Spatial intent is deterministic across differing logical sizes without pretending to be pixel-identical.
- Host-to-leech convergence uses one testable, non-focus pure proposal model without exposing host writeback.
- Exact order remains observable without adopting a visible/racy mutation mechanism.

### Negative

- Even equal-resolution projection remains approximate because working areas and cell grids are not fully observable.
- Exact live order, multi-monitor mapping, and independent concurrent-client terminal grids remain unavailable.

## Non-goals

- Multi-monitor/docked topology mapping.
- Exact pixels, working-area identity, font-cell identity, or independent Zellij client grids.
- Exact live column/tile correction or focus-dance actions.
- Arbitrary GUI windows, headless sessions, session creation/resurrection, or lifecycle transfer.
- Controller persistence, continuous reconciliation, projection creation/closure, routed launch, or consumer configuration.
