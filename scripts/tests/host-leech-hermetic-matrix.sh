#!/usr/bin/env bash
set -euo pipefail

# This matrix is intentionally hermetic: all compositor, transport, process,
# clock, and filesystem inputs are fixtures/fakes or temporary state.
export GOTOOLCHAIN=local
if [[ -d vendor && -z "${GOFLAGS:-}" ]]; then
  export GOFLAGS=-mod=vendor
fi
matrix_home="$(mktemp -d)"
trap 'rm -rf -- "$matrix_home"' EXIT
export HOME="$matrix_home"

# Cache reuse is opt-in rather than inherited. Explicit paths must be absolute,
# direct, current-user-owned directories; otherwise the hermetic run refuses.
use_explicit_cache() {
  local value="$1" variable="$2"
  [[ "$value" == /* && -d "$value" && ! -L "$value" && -O "$value" ]] || {
    printf 'unsafe explicit %s path: %s\n' "$variable" "$value" >&2
    exit 2
  }
  printf -v "$variable" '%s' "$value"
  export "$variable"
}
unset GOMODCACHE GOCACHE
if [[ -n "${HOST_LEECH_GOMODCACHE:-}" ]]; then
  use_explicit_cache "$HOST_LEECH_GOMODCACHE" GOMODCACHE
fi
if [[ -n "${HOST_LEECH_GOCACHE:-}" ]]; then
  use_explicit_cache "$HOST_LEECH_GOCACHE" GOCACHE
fi
export GOPROXY=off
export GOSUMDB=off
unset XDG_CACHE_HOME XDG_CONFIG_HOME XDG_STATE_HOME XDG_RUNTIME_DIR
unset NIRI_SOCKET WAYLAND_DISPLAY ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_SOCKET_DIR ZELLIJ_CACHE_DIR SSH_AUTH_SOCK

packages=(
  ./internal/consumercontract
  ./internal/niri
  ./internal/niriipc
  ./internal/zellijlive
  ./internal/sourceinventory
  ./internal/sliceprotocol
  ./internal/sliceenv
  ./internal/sliceattach
  ./internal/slicetransport
  ./internal/slicerpc
  ./internal/slicelayout
  ./internal/slicecontroller
  ./internal/slicetui
  ./internal/slicelaunch
  ./internal/hostleechsoak
  ./internal/mirror
  ./internal/resume
  ./internal/config
  ./cmd/redeem
)

export TERMINAL_REDEEMER_SOAK_ITERATIONS="${TERMINAL_REDEEMER_SOAK_ITERATIONS:-2000}"
go test -count=1 "${packages[@]}"

bash scripts/tests/host-leech-layer-smoke.sh --require

if [[ "${RUN_LOCKED_NIRI_VERSION_CHECK:-0}" == 1 ]]; then
  : "${NIRI_BIN:?set by the locked flake check}"
  : "${EXPECTED_NIRI_VERSION:?set by the locked flake check}"
  niri_version_output=$($NIRI_BIN --version)
  [[ $niri_version_output == "niri $EXPECTED_NIRI_VERSION (Nixpkgs)" ]] || {
    printf 'locked Niri version mismatch: %s\n' "$niri_version_output" >&2
    exit 1
  }
fi

if [[ "${RUN_LOCKED_ZELLIJ_SPIKE:-0}" == 1 ]]; then
  : "${ZELLIJ_BIN:?set by the locked flake check}"
  : "${SCRIPT_BIN:?set by the locked flake check}"
  : "${TIMEOUT_BIN:?set by the locked flake check}"
  : "${EXPECTED_ZELLIJ_VERSION:?set by the locked flake check}"
  bash scripts/spikes/zellij-live-only-attachment.sh
fi
