# Host/leech hermetic acceptance matrix

The repository matrix runs without a desktop, network, credentials, or existing
private state. Compositor, transport, process, clock, and filesystem inputs are
fixtures, fakes, subprocess helpers, or owner-only temporary directories.

## Native commands

Run the focused matrix with explicit current-user caches, or from a Nix build:

```console
HOST_LEECH_GOMODCACHE="$(go env GOMODCACHE)" \
HOST_LEECH_GOCACHE="$(go env GOCACHE)" \
  scripts/tests/host-leech-hermetic-matrix.sh

scripts/tests/host-leech-layer-smoke.sh --require
scripts/tests/host-leech-soak.sh --iterations 2000
nix flake check
```

The runner rejects unsafe explicit caches, creates and removes a private HOME,
and unsets XDG, graphical, Zellij, and SSH-agent inputs. A longer soak remains a
plain Go test invocation through the wrapper:

```console
scripts/tests/host-leech-soak.sh --iterations 10000
```

The soak keeps all state-cap, tombstone, retry-budget, duplicate-effect,
host-target-effect, helper-process, descriptor, goroutine, namespace, and cache
assertions inside `TestBoundedHostLeechSoak`. It does not emit or round-trip a
custom JSON status artifact.

Native Go coverage is optional reporting, not an exact baseline gate:

```console
go test -coverprofile=/tmp/host-leech.cover ./internal/...
go tool cover -func=/tmp/host-leech.cover
```

## Layer ownership

| Layer | Owns |
| --- | --- |
| Focused Go tests | Protocol validation, inventory authority, exact attachment, controller lifecycle, live management TUI state/actions, spatial policy, routing, process ownership, and failure regressions. |
| Controller model | Long generated lifecycle histories compared with an independent oracle, including restart, cleanup, cap, handoff, and recovery witnesses. |
| Native fuzz | Hostile wire, persisted state, argv/environment, journal, transport, and bounded-decoder inputs. |
| Packaged subprocess/crash | Real process, socket, fsync, cancellation, response-loss, restart, and replay boundaries. |
| Locked components | Production Niri IPC unit tests plus the actual locked exact-commit executable/version gate; live nested-Niri operator proof; Zellij 0.44.3 executable attach proof using `contract_version_1` socket/cache paths. |
| Bounded soak | Thousands of deterministic churn events, state/effect caps, durable reconstruction, and resource leak checks. |
| Physical smoke | Credentials, real machines, visible placement/reflow/focus, and activation decisions; never replaced by repository evidence. |

Ordinary `go test` executes every checked-in fuzz seed deterministically. The
layer smoke uses native `go test -list '^Fuzz'` discovery to require an exact
match with all 17 explicitly reviewed targets, then runs exactly 100
single-worker fuzz iterations per target with a temporary Go cache. This
consumes every current seed corpus before generated cases without relying on
wall-clock timing or retries. Longer campaigns should also keep a temporary
cache and run one target at a time, for example:

```console
cache="$(mktemp -d)"; trap 'rm -rf "$cache"' EXIT
GOCACHE="$cache" GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  go test -run '^$' -fuzz '^FuzzInventoryEnvelope$' -fuzztime=30s \
  ./internal/sliceprotocol
```

Promote a useful reproducer to a named `f.Add` seed and focused regression test;
do not commit generated fuzz cache/corpus output.

## Packaged process boundary

The packaged two-node check is intentionally separate:

```console
nix build .#checks.x86_64-linux.host-leech-subprocess-acceptance -L
```

For a local run both binaries must be supplied explicitly. The tests skip rather
than substituting `go run` or ambient executables:

```console
REDEEM_BIN=/absolute/path/to/packaged/redeem \
ZELLIJ_BIN=/absolute/path/to/zellij-0.44.3 \
  go test -count=1 -v ./internal/subprocessacceptance
```

This layer owns isolated roots, CLI enrolment/refusal, complete real-Zellij
inventory, a bounded in-process Niri Unix-socket peer, shell-inert transport,
controller restart/status, additive all-eligible enable/disable across restart,
exact routed attachment, response loss, same-token replay, cancellation,
responsible-process cleanup, and every durable routed-launch crash partition.
It must never read ambient graphical or credential state.

## Traceability anchors

- `internal/sliceprotocol`, `internal/sourceinventory`, and `internal/niriipc`
  own complete authority, revision, topology, production IPC behavior, cheap
  inventory cardinality/evidence-loss rows, and hostile input checks.
- `internal/sliceattach`, `internal/zellijlive`, and `internal/slicetransport`
  own exact live-only attachment, process evidence, environment isolation, and
  typed transport failures.
- `internal/slicecontroller` and `internal/slicelayout` own the additive
  all/workspace/pickup-minus-close desire formula, pickup removal, shared-effect
  recovery of an interrupted 32-source poll fanout across restart, restart/epoch
  recovery, ownership-safe close, generated host-authoritative convergence,
  report-only order drift, and caps.
  `TestAllEligibleSelectionComposesWithWorkspacePickupAndClose`,
  `TestInterruptedAllEnableRecoversUnstartedFanout`, the generated model, and
  the frozen v1.0 reader test are the focused v1.1 anchors. `cmd/redeem` tests
  additionally cross the real serialized control socket, require fresh focus at
  the destructive focused-close boundary, and prove failed focus or other
  effect execution rolls back all newly durable close/undo intent before later
  observation and reconciliation while generic close remains unchanged.
- `internal/slicetui` owns controller-socket-only management derivation and
  actions, independent status axes, orphan close visibility, bounded polling,
  stable selection, and display-cell-bounded rendering. Its tests use fake
  clients and Bubble Tea messages, never a TTY or live controller.
- `internal/slicelaunch` and `internal/slicerpc` own durable same-token routing,
  no fallback, journals, crash replay, and host effect cardinality.
- `internal/hostleechsoak` composes those production paths under sustained
  deterministic churn.
- `internal/subprocessacceptance` crosses packaged process/socket/fsync seams,
  including all-eligible as the sole desired reason across controller restart,
  exact projection cardinality, focused-close ownership, and same-token host
  creation/replay without local fallback.
- `internal/mirror` retains legacy one-shot exact attach compatibility.

Focused regression, fuzz, security/process ownership, crash/subprocess harness,
runtime/auth/lifecycle, and cap/tombstone tests remain authoritative in their
native packages. The matrix lists packages rather than duplicating every test
name, so adding or renaming focused tests does not create a second maintenance
protocol.

## Non-hermetic operator gate

The live Niri proof remains distinct. From a disposable parent Wayland session,
run the locked development shell with every script binding explicit:

```console
nix develop .#niri-spike --command env NIRI_BIN=niri KITTY_BIN=kitty PYTHON_BIN=python3 NIRI_PROBE="$PWD/scripts/spikes/niri-direct-ipc-probe.py" EXPECTED_NIRI_VERSION='26.04' bash scripts/spikes/niri-direct-ipc.sh
```

The script requires exact packaged output `niri 26.04 (Nixpkgs)`. The visible
operator checks remain as described in
[ADR 0004](../adr/0004-single-monitor-niri-spatial-mapping-policy.md). Then use
the physical checklist and upgrade/rollback sequence in
[HOST_LEECH_READINESS.md](../HOST_LEECH_READINESS.md). Repository validation
must not weaken host-key checking, inspect credentials, activate bindings, or
turn ambiguity into local fallback.
