# Spike 0001: exact live-only Zellij attachment

- **Status:** Passed
- **Pinned contract:** Zellij 0.44.3 from this repository's locked nixpkgs input
- **Executable proof:** `scripts/spikes/zellij-live-only-attachment.sh`

## Decision

A host-side `redeem slice attach` wrapper can attach a leech client to exactly one active Zellij session without allowing resurrection or unique-prefix fallback.

The wrapper must expose the exact live session through a private `ZELLIJ_SOCKET_DIR`, isolate resurrection metadata with an empty cache, and override force-close behavior to detach.

A symbolic link is not suitable. Zellij enumerates the socket directory and accepts only entries whose own file type is a socket. A hard link on the same filesystem retains the socket file type and inode.

## Proven behavior

The executable harness proves against Zellij 0.44.3 that:

- a hard-linked exact socket appears as the same socket inode and supports an interactive client;
- a symbolic-link socket is ignored by `list-sessions`;
- an empty `XDG_CACHE_HOME` prevents a serialized dead session from being resurrected;
- the normal cache resurrects the same dead fixture, proving that cache isolation is the controlling difference;
- the normal socket directory can attach a unique-prefix sibling after the exact session disappears;
- a private directory containing only a stale exact hard link fails instead of falling through to that sibling;
- nested-Zellij variables can be scrubbed before attachment;
- `options --on-force-close detach` overrides a hostile `on_force_close "quit"` user configuration;
- terminating the leech client with the detach override leaves the host session alive;
- removing stale private hard links does not remove or terminate the real session;
- version and Unix socket path bounds can be checked before attachment.

## Required wrapper contract

### Inputs

- pinned Zellij executable and expected version;
- exact verified active session name;
- real Zellij socket directory;
- private runtime root on the same filesystem as the real socket;
- dedicated shim cache that is never used to create a Zellij server.

Session names accepted by the initial slice implementation should use a bounded safe form such as:

```text
[A-Za-z0-9][A-Za-z0-9._-]*
```

Other active sessions remain visible as conflicts/incompatible resources rather than being passed ambiguously through clap or filesystem paths.

### Filesystem layout

For an attachment token `TOKEN`, socket/cache contract directory `contract_version_1`, and session `SESSION`:

```text
REAL_BASE/contract_version_1/SESSION                  # server-owned Unix socket
PRIVATE_ROOT/att-TOKEN/contract_version_1/SESSION    # hard link to the exact socket
SHIM_CACHE/zellij/contract_version_1/session_info/   # empty; no resurrection layouts
```

Requirements:

- `PRIVATE_ROOT` and every `att-*` directory are owned by the current user and mode `0700`;
- the real entry is a socket owned by the current user;
- the private root is on the same filesystem as the real socket;
- `link(2)` creates the private entry and inode equality is verified;
- the full private socket pathname is at most 107 bytes (Linux `sun_path` stores 108 bytes including the terminating NUL);
- each attachment gets a separate directory;
- startup and normal exit remove only owned `att-*` directories.

### Exact attachment argv

Conceptually, without an outer shell:

```text
env -u ZELLIJ \
    -u ZELLIJ_SESSION_NAME \
    -u ZELLIJ_PANE_ID \
    -u ZELLIJ_TAB_INDEX \
    -u ZELLIJ_TAB_NAME \
    ZELLIJ_SOCKET_DIR=PRIVATE_ROOT/att-TOKEN \
    XDG_CACHE_HOME=SHIM_CACHE \
    ZELLIJ_CONFIG_FILE=<optional validated config> \
    <pinned-zellij> attach SESSION options --on-force-close detach
```

The production Go wrapper should construct `exec.Cmd` argv and environment directly. The shell string in the spike exists only to drive a PTY through `script(1)`.

### Validation order

1. Validate the exact pinned version.
2. Validate the session name and complete private path length.
3. Resolve and `lstat` the exact real socket; reject missing, non-socket, or wrong-owner entries.
4. Create a fresh mode-0700 private directory on the same filesystem.
5. Hard-link the exact socket and verify socket type plus device/inode equality.
6. Launch Zellij with the isolated environment and explicit detach override.
7. Wait for the interactive client.
8. Remove the private directory after exit; garbage-collect stale owned directories on later startup.

If the original session dies and is recreated, the old hard link remains bound to the old socket inode. Attachment fails rather than switching to the replacement. A reconnect attempt must rebuild the private directory from a newly verified source socket.

## Outcome categories for implementation

The wrapper should map setup failures into typed slice outcomes rather than exposing raw client text:

- `invalid`: unsupported version, invalid session, or path over budget;
- `unavailable`: exact live socket is absent or fails its handshake;
- `setup_failed`: ownership, directory, or hard-link validation failed;
- `attach_failed`: the isolated Zellij client exited before a confirmed interactive lifetime;
- `detached`: the client ended without terminating host work;
- `cancelled`: controller cancellation ended the client.

A missing or dead session is never converted into a create/resurrect request.

## Packaging and test contract

The flake check provides the pinned Zellij executable, `script(1)`, and `timeout(1)` to the harness. The proof is Linux-specific because it relies on Unix-domain socket filesystem entries and hard links.

The production packaged wrapper includes only the Zellij executable at runtime; `script` and `timeout` remain test dependencies, not production dependencies.

## Residual risks

- Hard links require the private root and real socket to be on the same filesystem.
- A SIGKILL can leave a stale private directory; bounded owner/prefix-checked GC is required.
- Zellij 0.44.3 uses `contract_version_1` in socket and resurrection-cache paths; Terminal Redeemer still requires the exact 0.44.3 executable on both sides.
- The Unix socket path limit constrains runtime-root and session-name lengths.
- The empty shim cache must never be reused to start a server, or it could acquire resurrection layouts.
- Session death after the hard link is created still causes a client failure, but cannot select or resurrect another session; controller retry policy handles that failure.

## Result

The attachment mechanism is viable and unblocks downstream protocol/controller planning, subject to implementing this contract rather than calling plain `zellij attach` against the normal socket/cache environment.
