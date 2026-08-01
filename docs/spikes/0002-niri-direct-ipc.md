# Spike 0002: Niri direct IPC inventory and safe MVP mutations

- **Status:** Automated compatibility verified; live nested re-smoke pending
- **Pinned contract:** Niri 26.04 from the locked Nixpkgs input
- **Live nested proof:** `scripts/spikes/niri-direct-ipc.sh`
- **Direct socket probe:** `scripts/spikes/niri-direct-ipc-probe.py`

## Decision

Terminal Redeemer can consume Niri directly over its Unix socket and safely implement the MVP inventory and spatial mutations without shell commands or focus-stealing fallbacks.

The host-side inventory should use Niri's event-stream initial replay as the workspace/window snapshot barrier, query Outputs on a separate connection, validate all cross-references, and publish only complete joined observations as authoritative revisions.

MVP mutations can target workspace and window runtime IDs for workspace creation, workspace movement, tiled/floating state, and proportional size. Exact existing-column reorder has no ID-targeted action in Niri 26.04 and remains a stretch-only focus-dance operation.

## Compatibility evidence

The pinned Niri 26.04 `niri-ipc` source retains the request, response, event,
workspace, window, output, and action structures below, and production fixtures
exercise those serialized forms. The nested two-instance rerun on this exact
release remains part of the operator smoke and was deliberately not run during
the non-activating compatibility update.

The live harness starts two bounded nested Niri instances under the current Wayland session. The first instance launches a pinned Kitty window and exercises inventory and mutations; the second proves source-instance rotation after compositor restart. Neither instance uses `--session`, changes the parent Niri configuration, or mutates parent workspaces.

The original live proof plus the exact-release source and fixture evidence establish:

- The pinned `niri-ipc` protocol remains direct newline-delimited JSON over `NIRI_SOCKET`; the exact-release nested runtime re-smoke is still pending.
- `"EventStream"` first returns `{"Ok":"Handled"}`.
- The initial replay includes `WorkspacesChanged` and `WindowsChanged` and ends at `ConfigLoaded`.
- Outputs require a separate `"Outputs"` request.
- A complete Outputs/workspaces/windows join has no dangling references.
- A fingerprint derived privately from Linux boot ID plus socket device/inode is stable for one Niri instance and changes across a nested Niri restart in the same boot.
- Naming the trailing unnamed empty workspace by runtime ID creates the named workspace and a replacement trailing empty workspace.
- Attempting the same name with different case returns `Handled` but silently leaves the target unnamed, proving that duplicate/case-colliding names require verify-after-write.
- `MoveWindowToWorkspace` with exact window/workspace IDs and `focus:false` moves the window without changing the focused workspace.
- `MoveWindowToFloating`, `SetWindowWidth`, and `SetWindowHeight` accept exact window IDs.
- `SetProportion:45.0` and `SetProportion:40.0` produced approximately 45% output width and 40% output height in the nested single-output fixture.
- Every mutation became observable through bounded follow-up queries rather than being assumed from the `Handled` response.

The harness also captures exact requests and observations into a caller-selected output directory.

## Initial inventory protocol

### Connection 1: event-stream replay

Send:

```json
"EventStream"
```

Read:

```json
{"Ok":"Handled"}
{"WorkspacesChanged":{"workspaces":[...]}}
{"WindowsChanged":{"windows":[...]}}
...
{"ConfigLoaded":{"failed":false}}
```

`ConfigLoaded` is the end-of-initial-replay barrier. `KeyboardLayoutsChanged` is optional when no layouts are configured and must not be used as the barrier.

After the barrier, the same connection may remain open for local controller updates. A one-shot host snapshot helper may close it after constructing the initial state.

### Connection 2: Outputs

Send:

```json
"Outputs"
```

Expect an `Ok.Outputs` map containing output names and logical dimensions, scale, transform, and modes.

### Completeness validation

Before an observation may advance the authoritative host revision:

- every non-null workspace output exists in Outputs;
- every non-null window workspace ID exists in Workspaces;
- required output logical geometry is present for the one-monitor policy;
- exactly one active output satisfies the MVP topology;
- the event replay reached `ConfigLoaded` without timeout, malformed JSON, or EOF.

Niri documents temporary inconsistency across state parts. A dangling reference is therefore a degraded observation to retry, not protocol corruption and not evidence that a source disappeared. Degraded observations never authorize projection closure.

## Source-instance contract

The host process privately reads:

- `/proc/sys/kernel/random/boot_id`;
- `lstat(NIRI_SOCKET).st_dev`;
- `lstat(NIRI_SOCKET).st_ino`.

A private fingerprint such as:

```text
SHA-256(boot-id || device || inode)
```

is compared with persisted host state. A changed fingerprint rotates a persisted random public source-epoch UUID. The public protocol carries only that random epoch UUID; it never carries the socket path, device, inode, or graphical-session environment values.

A production implementation should additionally retain the resolved socket path privately so an unexpected path change with reused inode metadata also rotates the epoch.

## Proven mutation requests

### Name the trailing empty workspace

```json
{
  "Action": {
    "SetWorkspaceName": {
      "name": "tr-spike",
      "workspace": {"Id": 2}
    }
  }
}
```

Selection rules:

1. restrict to the one active MVP output;
2. join windows to workspaces;
3. choose the highest-index unnamed workspace with no windows;
4. send the ID-targeted action;
5. poll until the same ID has the requested name and a new trailing empty workspace exists;
6. reject case-insensitive name collisions before mutation and still verify afterward.

### Move without following focus

```json
{
  "Action": {
    "MoveWindowToWorkspace": {
      "window_id": 42,
      "reference": {"Id": 2},
      "focus": false
    }
  }
}
```

Verification requires the same window ID on the target workspace and the previously focused workspace still focused.

### Floating and proportional size

```json
{"Action":{"MoveWindowToFloating":{"id":42}}}
{"Action":{"SetWindowWidth":{"id":42,"change":{"SetProportion":45.0}}}}
{"Action":{"SetWindowHeight":{"id":42,"change":{"SetProportion":40.0}}}}
```

Niri's proportion value is a percentage number, not a fraction: `45.0` means 45%, while `0.45` means 0.45%. Verify the resulting floating state and observed logical size after each action batch.

## Order boundary

Niri reports exact one-based `(column, tile)` position in `Window.layout.pos_in_scrolling_layout`, so Terminal Redeemer can observe initial order and drift.

`MoveColumnToIndex` contains only an index and always targets the focused column. There is no window/column ID argument in Niri 26.04. Exact live reorder would require:

1. record current focus;
2. focus the target window by ID;
3. move the focused column;
4. restore focus;
5. detect concurrent user changes.

That sequence is visible and racy. It is explicitly outside MVP.

## Recommended Go adapter contract

Create a dedicated package rather than extending shell-oriented snapshot runners:

```text
internal/niriipc
  Client.Dial(ctx, socketPath)
  Client.InitialState(ctx)        # event replay through ConfigLoaded
  Client.Outputs(ctx)             # separate request
  Client.Action(ctx, request)
  Snapshot.Validate()
  Observer.WaitFor(ctx, predicate)
```

Requirements:

- use `net.Dialer.DialContext("unix", path)`;
- set connection read/write deadlines from context;
- send one JSON request plus newline per request connection;
- bound individual reply/event line size and total initial replay size;
- ignore unknown additive event fields and variants where safe;
- treat malformed JSON, timeout, EOF before `ConfigLoaded`, failed config, and dangling references as typed degraded outcomes;
- keep raw Niri IDs internal to one source epoch;
- never treat `{"Ok":"Handled"}` as mutation completion;
- verify every mutation with bounded state observation;
- expose exact request structs so tests do not build JSON through string concatenation.

The controller may keep a local event-stream connection open. The remote/leech-facing protocol remains revisioned full snapshots with bounded polling for MVP.

## Automated and live checks

Production tests in `internal/niriipc` verify strict production-client reply
handling, replay barriers, validation failures, bounded input, version parsing,
and exact action encoding without maintaining a synthetic Niri state oracle.
The hermetic matrix runs the production version gate against the actual locked
Niri binary and requires the complete output `niri 26.04 (Nixpkgs)` exactly. The pinned
upstream IPC
types retain the EventStream, Outputs, ConfigLoaded, workspace/window/output,
and ID-targeted action structures used by production. Run:

```bash
go test ./internal/niriipc
nix build .#checks.x86_64-linux.host-leech-hermetic-matrix
```

The nested live harness remains the distinct operator proof for actual
compositor mutation behavior. From an existing disposable parent Wayland
session, run exactly:

```console
nix develop .#niri-spike --command env NIRI_BIN=niri KITTY_BIN=kitty PYTHON_BIN=python3 NIRI_PROBE="$PWD/scripts/spikes/niri-direct-ipc-probe.py" EXPECTED_NIRI_VERSION='26.04' bash scripts/spikes/niri-direct-ipc.sh
```

The locked shell supplies Niri, Kitty, and Python without restoring a package or
app output. The script itself verifies exact output `niri 26.04 (Nixpkgs)` before starting its bounded
nested instances.

## Residual risks

- Outputs and event-stream state come from separate requests; topology can change between them. Validation and retry bound the inconsistency.
- Some successful action replies represent silent no-ops, especially duplicate workspace names.
- Working-area dimensions are not exposed directly. Proportional sizing can differ when exclusive zones or bars differ between machines.
- Exact order mutation remains focus-dependent.
- Niri IPC is additive but version-coupled; unknown fields/variants must be tolerated and pinned-version smoke tests remain required.
- The live harness uses nested winit Niri and cannot exactly reproduce every physical-output or exclusive-zone behavior.

## Result

Direct Niri IPC and the targeted MVP spatial mutations remain viable from the automated exact-commit evidence. The exact-commit nested runtime re-smoke remains an operator gate.
