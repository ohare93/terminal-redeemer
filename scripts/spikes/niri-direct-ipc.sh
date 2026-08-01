#!/usr/bin/env bash
set -euo pipefail

[[ $# == 0 ]] || { printf 'usage: %s\n' "$0" >&2; exit 2; }
niri_bin=${NIRI_BIN:-niri}
kitty_bin=${KITTY_BIN:-kitty}
python_bin=${PYTHON_BIN:-python3}
expected_version=${EXPECTED_NIRI_VERSION:-26.04}
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
probe=${NIRI_PROBE:-$script_dir/niri-direct-ipc-probe.py}

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

version_output=$($niri_bin --version)
[[ $version_output == "niri $expected_version (Nixpkgs)" ]] || fail "expected Niri $expected_version (Nixpkgs), got: $version_output"

[[ -n ${WAYLAND_DISPLAY:-} || -n ${WAYLAND_SOCKET:-} ]] || fail "a parent Wayland session is required for the nested Niri spike"
root=$(mktemp -d "${TMPDIR:-/tmp}/terminal-redeemer-niri-spike.XXXXXX")
results=${SPIKE_OUTPUT_DIR:-$root/results}
mkdir -p "$results"
pids=()

cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  done
  if [[ ${KEEP_SPIKE_OUTPUT:-0} != 1 ]]; then
    rm -rf "$root"
  else
    printf 'kept nested Niri spike workspace: %s\n' "$root"
  fi
}
trap cleanup EXIT

start_nested() {
  local label=$1 mutate=$2
  local run_dir=$root/$label
  local output_dir=$results/$label
  mkdir -p "$run_dir" "$output_dir"
  : >"$run_dir/config.kdl"
  cat >"$run_dir/child.sh" <<EOF
#!/bin/sh
printf 'NIRI_SOCKET=%s\nWAYLAND_DISPLAY=%s\n' "\$NIRI_SOCKET" "\$WAYLAND_DISPLAY" > '$run_dir/endpoint.env'
exec '$kitty_bin' --config NONE --class tr-niri-spike-$label --title tr-niri-spike-$label sh -c 'sleep 120'
EOF
  chmod +x "$run_dir/child.sh"

  "$niri_bin" -c "$run_dir/config.kdl" -- "$run_dir/child.sh" >"$run_dir/niri.log" 2>&1 &
  local pid=$!
  pids+=("$pid")
  for _ in $(seq 1 150); do
    [[ -s $run_dir/endpoint.env ]] && break
    kill -0 "$pid" >/dev/null 2>&1 || {
      tail -100 "$run_dir/niri.log" >&2
      fail "nested Niri $label exited before publishing its endpoint"
    }
    sleep 0.1
  done
  [[ -s $run_dir/endpoint.env ]] || fail "nested Niri $label did not publish its endpoint"

  local inner_socket
  inner_socket=$(while IFS='=' read -r key value; do
    if [[ $key == NIRI_SOCKET ]]; then
      printf '%s' "$value"
      break
    fi
  done <"$run_dir/endpoint.env")
  for _ in $(seq 1 100); do
    [[ -S $inner_socket ]] && break
    kill -0 "$pid" >/dev/null 2>&1 || break
    sleep 0.05
  done
  [[ -S $inner_socket ]] || fail "nested Niri $label socket is unavailable"

  local args=("$python_bin" "$probe" --socket "$inner_socket" --output-dir "$output_dir" --timeout 8)
  [[ $mutate == yes ]] && args+=(--mutate)
  "${args[@]}"

  kill "$pid" >/dev/null 2>&1 || true
  wait "$pid" >/dev/null 2>&1 || true
  pids=("${pids[@]:0:${#pids[@]}-1}")
}

start_nested first yes
start_nested second no

"$python_bin" - "$results/first/result.json" "$results/second/result.json" <<'PY'
import json,sys
first=json.load(open(sys.argv[1]))
second=json.load(open(sys.argv[2]))
assert first["source_identity"]["boot_digest"] == second["source_identity"]["boot_digest"], "boot identity changed during spike"
assert first["source_identity"]["instance_digest"] != second["source_identity"]["instance_digest"], "Niri restart did not rotate source-instance identity"
assert first["mutations_verified"] is True
assert second["mutations_verified"] is False
print("PASS: Niri direct-IPC inventory, source-instance rotation, and safe MVP mutations verified")
PY
