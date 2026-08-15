# Host/leech slice readiness and consumer contract

## Authoritative references

The host/leech terminal-slice MVP is opt in and disabled by default. Installing
the package does not activate the controller, routed launch mode, or Niri
bindings.

Use the v1.3.0 machine-readable artifacts in
[`contracts/host-leech-slices/v1`](../contracts/host-leech-slices/v1/) for the
consumer surface. The const-rich JSON Schema owns strict structure and exact
semantic values; runtime-coupled Go tests own protocol constants, pinned
versions, and defaults.

The detailed behavior is intentionally not repeated here:

- [ADR 0002](adr/0002-host-leech-terminal-slice-domain-and-lifecycle.md)
  defines domain ownership and lifecycle.
- [ADR 0003](adr/0003-terminal-slice-workspace-sharing-and-persistence.md)
  defines workspace selection and persistence.
- [ADR 0004](adr/0004-single-monitor-niri-spatial-mapping-policy.md)
  defines host-authoritative spatial behavior and the live Niri proof.
- [ADR 0005](adr/0005-global-slice-selection-and-live-management.md)
  defines global all-eligible selection, unnamed sources, management, and schema-2 compatibility.
- [PROTOCOL.md](PROTOCOL.md) defines wire, identity, revision, attachment,
  recovery, and routed-launch rules.
- [CONFIG.md](CONFIG.md) lists exact YAML and module options and defaults.
- [OPERATIONS.md](OPERATIONS.md) owns generic deployment, service, security,
  state, and incident guidance.

The accepted limitations are one active output per leech and either one active output or a coherent zero-output source host, approximate
proportional placement, report-only live order drift, Niri 26.04, Zellij
0.44.3, shared
minimum-client-grid reflow, and no automatic local fallback after an ambiguous
remote launch.

Contract 1.3.0 updates the exact runtime compatibility pair and Zellij's
`contract_version_1` socket/cache layout. Inventory schema 2, RPC schema 1,
controller schema 2, artifact path `v1`, option names, defaults, and persisted
authority formats are unchanged. Consumers must update both host and leech to the same Terminal
Redeemer pin/NAR metadata; mixed inventory schema revisions reject safely and retain prior authority, but do not authorize disappearance or spatial writes; no new mono-nix option or authority migration is
required. Quiesce in-flight projection helpers before replacing 1.1.0 because
prepared 0.43.1 socket namespaces are not adopted by the 0.44.3 binary.

## Contract and repository verification

```bash
check-jsonschema \
  --schemafile contracts/host-leech-slices/v1/consumer-contract.schema.json \
  contracts/host-leech-slices/v1/consumer-contract.json
go test ./internal/consumercontract
nix build .#host-leech-consumer-contract
nix build .#checks.x86_64-linux.host-leech-consumer-contract -L
```

The flake check validates the JSON against the strict schema, runs compact
negative mutations across selection, downgrade, drop, exact command argv,
authority, revision, limitation, and no-fallback values, compares packaged source members
byte-for-byte, verifies the generated binding template, and checks the packaged
CLI surface. `internal/consumercontract` independently
compares contract defaults, protocol versions, normalization, and pinned
component versions with production Go constants.

The reviewed consumer outputs remain:

| Surface | Output |
| --- | --- |
| package/app | `packages.x86_64-linux.terminal-redeemer`, `apps.x86_64-linux.redeem` |
| contract artifacts | `packages.x86_64-linux.host-leech-consumer-contract` |
| flake metadata | `lib.sliceConsumerContract` |
| modules | `homeManagerModules.terminal-redeemer`, `nixosModules.terminal-redeemer` |
| helpers | read-only `slice.launchCommand`, `slice.closeFocusedCommand`, `slice.manageCommand` |
| Niri fragment | read-only `slice.niriIntegrationFragment` |

The generated fragment is an opt-in template. Merge it without replacing
unrelated bindings. `Mod+Return` runs `redeem slice launch`; `Mod+W` runs
`redeem slice close-focused`, which may close only a positively owned leech
projection and never host work. The fragment is unchanged by the manager:
`slice.manageCommand` is direct packaged Kitty/Redeem argv that a consumer may
bind under any locally selected key, but the contract reserves and installs no
management binding. Live proof that such a binding opens the TUI belongs to the
two-machine smoke, not repository evaluation.

## Upgrade and explicit controller re-enrolment

Do not hand-edit, overlay, generation-merge, or casually restore slice
authority. Host and leech namespaces are separate from capture/history and
legacy mirror state. Backups are forensic evidence, not ordinary rollback
points.

1. **Verify without activating.** Build the proposed package and contract,
   validate the schema/package checks, run `nix flake check`, inspect the
   consumer configuration diff, and select one package revision for both
   machines.
2. **Quiesce and preserve both authorities separately.** Keep Leech mode off and
   the controller disabled. Stop existing controller and host RPC launch
   writers. Copy the complete host `stateDir/slice/`, complete leech
   `stateDir/slice/`, and each YAML file into separate owner-only, read-only
   forensic locations. Record capture time and generation without logging
   tokens, sockets, identities, argv, credentials, or private state.
3. **Upgrade host first.** Deploy without activation. Verify exact RPC liveness,
   schema negotiation, and pinned Niri/Zellij versions. Initialize source
   inventory/token authority only on a machine that has never been enrolled;
   missing state after enrolment is an error.
4. **Upgrade leech and handle controller authority explicitly.** Current
   schema-2 authority upgrades in place with optional `all_eligible` defaulting
   false; do not re-enrol it. If older experimental authority exists, stop and
   disable the controller, preserve its entire directory, and only with explicit
   operator approval rename/remove that controller directory and run
   `redeem slice controller init`. Never remove host inventory/token state, Kitty/Zellij work, legacy mirror state, or
   unrelated configuration, and never overlay an old backup into new authority.
5. **Prove host-location projection.** Run all hermetic checks and applicable
   physical smoke rows below with mode off and disposable named workspaces.
6. **Enable the controller service.** Enable only the reviewed Home Manager
   service and verify singleton/journal status while routed launch remains off.
7. **Enable routed launch last.** First prove mode-off and unselected launches
   remain local. Then enable Leech mode. Install `Mod+Return` only after the
   response-loss/no-fallback smoke and `Mod+W` only after ownership-safe
   close/reopen smoke.

Controller schema 2 intentionally rejects old schema-1 source-keyed drops and
experimental leech authority; there is no in-place translation for those old
representations. V1.1's optional omitted-false `all_eligible` field is instead
an additive schema-2 extension. Upgrade never rewrites legacy mirror payloads, capture events, resume checkpoints, host
inventory/token authority, or host Kitty/Zellij work.

## Downgrade and rollback

Rollback stops new control; it is not a cleanup operation:

1. remove or disable consumer `Mod+Return`, `Mod+W`, and management bindings;
2. while the v1.1 controller is still running, run `redeem slice controller all-disable` and verify success;
3. run `redeem slice mode disable` while the current binary is available;
4. stop the controller and set both controller and Leech mode options false;
5. preserve all `stateDir/slice/` authority, especially token journals, routed
   intents, exclusions, cleanup/successor gates, and source identity;
6. select the previous package only after proving the optional `all_eligible`
   field is absent; and
7. use explicit same-token reconnect, reopen, or legacy exact attach before
   downgrade when known host work must remain accessible.

Global toggles are audited but create no undo records, so successful
`all-disable` removes the only v1.1-only controller-state shape. A prior binary
encountering active `all_eligible` rejects it as an unknown field and fails
closed; its restarting user service may repeat that invalid-state failure until
stopped. Never delete or reinitialize authority to bypass this check.

Do not delete projections as rollback, clear token journals, rerun init over
used state, terminate host Kitty/Zellij, issue broad Niri closes, or replace a
newer namespace with an older backup. Disabling services leaves host sessions,
pending launches, and unrelated windows untouched.

A same-generation forensic replacement is exceptional recovery. Quiesce every
writer, preserve fresh complete host and leech copies, and use a version-matched
read-only tool to prove exact enrollment identity, epoch/revision/tombstones,
generation, and every non-prunable record. It must also prove there is no
pending/disconnected intent, replay-relevant host journal, handoff, cleanup or
successor gate, unresolved lineage/recovery, pickup, drop, selection, or spatial
origin that would be removed. Any difference, corruption, missing file, newer
generation, or uncertainty blocks restore; keep services off and recover work
non-destructively instead.

## Automated pre-smoke gate

```bash
scripts/tests/host-leech-layer-smoke.sh --require
scripts/tests/host-leech-hermetic-matrix.sh
scripts/tests/host-leech-soak.sh --iterations 2000
nix flake check
```

The matrix uses fixtures, fakes, deterministic clocks, subprocess helpers, and
private temporary state. The soak keeps cap, effect-cardinality, retry-budget,
and resource-leak assertions in process; it emits ordinary Go test output, not
a separate status protocol. A longer pre-release run is:

```bash
scripts/tests/host-leech-soak.sh --iterations 10000
```

Optional coverage reporting uses native Go coverage and is not an exact
acceptance baseline:

```bash
go test -coverprofile=/tmp/host-leech.cover ./internal/...
go tool cover -func=/tmp/host-leech.cover
```

These evidence layers remain distinct: focused unit/integration tests,
deterministic controller model histories, native fuzz targets, packaged
subprocess/crash tests, pinned executable checks, bounded soak, and the mandatory
credentialed physical smoke. No aggregate percentage substitutes for a stronger
boundary.

## Physical operator smoke

Run on disposable named workspaces with an unrelated sentinel window. Record
pass/fail and sanitized timestamps, never credentials, tokens, socket paths,
private state, environment values, titles, or argv dumps.

| Area | Required observation |
| --- | --- |
| Compatibility | Both machines use one package revision; inventory schema 2, RPC schema 1, controller schema 2, Niri 26.04, and Zellij 0.44.3 pass. |
| Local boundary | Mode-off and unselected `Mod+Return` remain ordinary local launch. |
| Workspace selection | Selected current and newly opened eligible sources project once; unselection touches no unrelated window. |
| All-eligible fanout | With `all-enable`, every current eligible source projects exactly once and each later eligible source projects once without another selection action; `all-disable` removes only that reason and preserves independent workspace/pickup projections and the unrelated sentinel. |
| Close subtraction/non-undoability | While all-eligible is active, an exact close subtracts only that session and it remains closed as other sources fan out. All-enable/disable audit entries are present, no undo entry is added, and undo does not reverse either global toggle. Explicit reopen restores the source only while another positive reason remains. |
| Unnamed source | An eligible unnamed-workspace source receives one exact live-only attachment and appears under the manager's synthetic `(unnamed)` group, while no spatial proposal/effect, spatial conflict/retry, or repeated poll/restart state churn is produced; a real named `(unnamed)` workspace remains distinct. |
| Management binding/TUI | The reviewed consumer-owned management binding directly spawns the exported `manageCommand` argv and opens the live terminal TUI. Polling and all-enable/disable, workspace add/remove, pickup/remove, close/reopen, undo, and reconnect actions work through the controller while preserving cursor/viewport usability, host Kitty/Zellij work, and unrelated windows; no management binding is installed by the module itself. |
| Exact attach | Concurrent host/leech clients use the exact live session; dead, cache-only, and prefix names fail; detach and resize preserve host work while documenting shared-grid reflow. |
| Routed launch separation/replay | With Leech mode and all-eligible active, `Mod+Return` on an unselected named workspace remains an ordinary local launch; only an explicitly selected named workspace routes. Selected mode creates one token/session/host Kitty and exact projection; a lost first response remains inspectable and same-token replay creates no duplicate or local fallback. |
| Close/drop/reopen | `Mod+W` closes only exact owned leech state; the session-keyed drop survives source/epoch replacement and live headless intervals; only explicit reopen/undo or confirmed complete absence plus grace clears it. |
| Headless source | With the source monitor unavailable, inventory is complete schema 2, existing Kitty/Zellij sources list and attach, workspace/layout/order remain authoritative, no proportional-size action is emitted, and no windowless Zellij session becomes a source. |
| Headless routed launch | Selected `Mod+Return` routes once into an existing exact uniquely normalized named source workspace; missing, duplicate, and colliding names create no workspace, no host Kitty/session duplicate, and no local fallback. |
| Output restoration | The next complete outputful revision resumes proportional sizing for the same source/session identity without focus or unrelated mutation. |
| Spatial | Host workspace, floating/tiled mode, and proportional size converge on the leech; supported leech drift reverts, initial order is best effort, later order drift is report-only, and approximation is recorded. |
| Recovery | In-window recovery retains its original budget; exhaustion becomes stable disconnected; explicit reconnect uses the same source/token. |
| Disappearance/revision | Degraded, stale, duplicate, conflicting, and retired observations close nothing; accepted complete revisions alone advance absence; restart/epoch rotation never reuses raw identity. |
| Ownership/process races | PID/helper/inventory delay and ambiguous or reused candidates remain bounded and cannot authorize app-ID/title/order fallback, host mutation, duplicate creation, or unrelated close. |
| V1.1 downgrade compatibility | With the v1.1 controller still running, successful `all-disable` removes `all_eligible`; then mode is disabled and the controller stopped before the prior package is selected. The prior reader accepts that preserved authority with existing audit/undo records intact. Separately, against an owner-only disposable copy with active `all_eligible`, the prior reader rejects the unknown field, rewrites nothing, and a supervised restart repeats the invalid-state failure until stopped. Authority is never deleted or reinitialized to force a downgrade. |
| Rollback | Removing consumer bindings and disabling mode/controller preserves sessions, journals, state, sentinel windows, and legacy exact attach. |
| Isolation | Repository validation does not connect to live SSH, inspect credentials/agents/private sessions, install bindings, or activate machines. |

Any failed row blocks consumer activation. Do not weaken host-key, complete
snapshot, exact identity, process ownership, or no-fallback rules to make a
smoke pass.

## Legacy coexistence and retirement

Legacy one-shot `mirror snapshot/list/open/status/close` remains independent.
Interactive attach still scrubs nested Zellij state, detaches without killing
remote work, and provides the manual recovery path. Pinned Zellij 0.44.3 has no
watch command; watch remains explicitly unsupported. Legacy clipboard behavior
is separate and slice clipboard remains off.

Do not retire legacy attach until routed response-loss replay, ownership-safe
close/reopen, concurrent-client host survival, controller restart, reconnect
exhaustion/recovery, rollback preservation, and the full physical smoke have
been recorded for the selected immutable package revision. Retirement is a
separate approved consumer change, never a side effect of package installation.
