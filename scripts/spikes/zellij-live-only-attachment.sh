#!/usr/bin/env bash
set -euo pipefail

zellij_bin=${ZELLIJ_BIN:-zellij}
script_bin=${SCRIPT_BIN:-script}
timeout_bin=${TIMEOUT_BIN:-timeout}
expected_version=${EXPECTED_ZELLIJ_VERSION:-0.44.3}
contract_dir=${ZELLIJ_CONTRACT_DIR:-contract_version_1}

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

expect_attached() {
  local command=$1
  local output rc
  set +e
  output=$("$timeout_bin" --signal=TERM --kill-after=1 2 "$script_bin" -qefc "$command" /dev/null 2>&1)
  rc=$?
  set -e
  case "$rc" in
    124|137) ;;
    *)
      printf '%s\n' "$output" >&2
      fail "expected an attached client to remain alive until timeout, got exit $rc"
      ;;
  esac
}

expect_attach_failure() {
  local command=$1
  local output rc
  set +e
  output=$("$timeout_bin" 5 "$script_bin" -qefc "$command" /dev/null 2>&1)
  rc=$?
  set -e
  case "$rc" in
    124|137)
      printf '%s\n' "$output" >&2
      fail "expected attachment failure, but the client remained attached"
      ;;
    0) fail "expected attachment failure, got success" ;;
  esac
  printf '%s' "$output"
}

version_output=$($zellij_bin --version)
[[ $version_output == "zellij $expected_version" ]] || fail "expected zellij $expected_version, got: $version_output"

root=$(mktemp -d "${TMPDIR:-/tmp}/terminal-redeemer-zellij-spike.XXXXXX")
real_base=$root/real
real_cache=$root/cache
shim_cache=$root/shim-cache
private_root=$root/private
mkdir -m 700 -p "$real_base/$contract_dir" "$real_cache" "$shim_cache" "$private_root"

host_config=$root/host.kdl
hostile_config=$root/hostile.kdl
cat >"$host_config" <<'EOF'
show_release_notes false
EOF
cat >"$hostile_config" <<'EOF'
on_force_close "quit"
show_release_notes false
EOF

real_zellij() {
  env -u ZELLIJ -u ZELLIJ_SESSION_NAME \
    ZELLIJ_SOCKET_DIR="$real_base" \
    XDG_CACHE_HOME="$real_cache" \
    ZELLIJ_CONFIG_FILE="$host_config" \
    "$zellij_bin" "$@"
}

cleanup() {
  if [[ -d $real_base/$contract_dir ]]; then
    for socket in "$real_base/$contract_dir"/*; do
      [[ -S $socket ]] || continue
      real_zellij kill-session "${socket##*/}" >/dev/null 2>&1 || true
    done
  fi
  rm -rf "$root"
}
trap cleanup EXIT

attach_command() {
  local socket_base=$1 cache=$2 session=$3 config=$4 force_close=${5:-detach}
  printf "env -u ZELLIJ -u ZELLIJ_SESSION_NAME ZELLIJ_SOCKET_DIR='%s' XDG_CACHE_HOME='%s' ZELLIJ_CONFIG_FILE='%s' '%s' attach '%s' options --on-force-close '%s'" \
    "$socket_base" "$cache" "$config" "$zellij_bin" "$session" "$force_close"
}

make_private_exact_socket() {
  local attachment=$1 session=$2
  local dir=$private_root/$attachment/$contract_dir
  mkdir -p "$dir"
  chmod 700 "$private_root/$attachment" "$dir"
  ln "$real_base/$contract_dir/$session" "$dir/$session"
  printf '%s\n' "$private_root/$attachment"
}

# A hard link remains a socket entry and exposes only the exact requested session.
real_zellij attach --create-background exact-session
exact_private=$(make_private_exact_socket att-exact exact-session)
real_inode=$(stat -c '%d:%i' "$real_base/$contract_dir/exact-session")
private_inode=$(stat -c '%d:%i' "$exact_private/$contract_dir/exact-session")
[[ $real_inode == "$private_inode" ]] || fail "private socket is not a hard link to the live socket"
[[ $(stat -c '%a' "$private_root/att-exact") == 700 ]] || fail "private attachment directory is not mode 0700"

# Nested-session variables are deliberately present in the parent and scrubbed by the command.
export ZELLIJ=host-nested ZELLIJ_SESSION_NAME=host-nested
expect_attached "$(attach_command "$exact_private" "$shim_cache" exact-session "$host_config")"
unset ZELLIJ ZELLIJ_SESSION_NAME
real_zellij list-sessions --short | grep -Fx exact-session >/dev/null || fail "closing isolated client terminated the host session"

# A symbolic link is ignored because Zellij requires the directory entry itself to be a socket.
mkdir -m 700 -p "$private_root/att-symlink/$contract_dir"
ln -s "$real_base/$contract_dir/exact-session" "$private_root/att-symlink/$contract_dir/exact-session"
set +e
symlink_output=$(env ZELLIJ_SOCKET_DIR="$private_root/att-symlink" XDG_CACHE_HOME="$shim_cache" "$zellij_bin" list-sessions --short 2>&1)
symlink_rc=$?
set -e
[[ $symlink_rc -ne 0 ]] || fail "Zellij unexpectedly accepted a symbolic-link socket entry"
[[ $symlink_output == *"No active zellij sessions found"* ]] || fail "unexpected symbolic-link result: $symlink_output"

# An empty cache prevents a serialized dead session from being resurrected.
dead_session=dead-session
mkdir -p "$real_cache/zellij/$contract_dir/session_info/$dead_session"
printf 'layout {\n    pane\n}\n' >"$real_cache/zellij/$contract_dir/session_info/$dead_session/session-layout.kdl"
real_zellij list-sessions --no-formatting | grep -E "^${dead_session} .+EXITED - attach to resurrect" >/dev/null || fail "dead fixture is not resurrectable with the normal cache"
mkdir -m 700 -p "$private_root/att-dead/$contract_dir"
dead_output=$(expect_attach_failure "$(attach_command "$private_root/att-dead" "$shim_cache" "$dead_session" "$host_config")")
[[ $dead_output == *"No session with the name '$dead_session' found"* ]] || fail "unexpected dead-session failure: $dead_output"
[[ ! -S $real_base/$contract_dir/$dead_session ]] || fail "isolated attach resurrected the dead session"
expect_attached "$(attach_command "$real_base" "$real_cache" "$dead_session" "$host_config")"
[[ -S $real_base/$contract_dir/$dead_session ]] || fail "normal-cache control did not resurrect the dead session"
real_zellij kill-session "$dead_session"

# The normal socket directory can unique-prefix match. A stale exact hard link cannot fall through
# because the private directory contains no sibling sessions.
real_zellij attach --create-background prefix
real_zellij attach --create-background prefix-long
prefix_private=$(make_private_exact_socket att-prefix prefix)
real_zellij kill-session prefix
for _ in $(seq 1 50); do
  [[ ! -S $real_base/$contract_dir/prefix ]] && break
  sleep 0.05
done
expect_attached "$(attach_command "$real_base" "$shim_cache" prefix "$host_config")"
prefix_output=$(expect_attach_failure "$(attach_command "$prefix_private" "$shim_cache" prefix "$host_config")")
[[ $prefix_output == *"No session with the name 'prefix' found"* ]] || fail "unexpected exact-prefix failure: $prefix_output"
real_zellij kill-session prefix-long

# Explicit detach overrides a hostile user config that would otherwise quit the host session.
real_zellij attach --create-background quit-control
expect_attached "$(attach_command "$real_base" "$real_cache" quit-control "$hostile_config" quit)"
for _ in $(seq 1 50); do
  [[ ! -S $real_base/$contract_dir/quit-control ]] && break
  sleep 0.05
done
[[ ! -S $real_base/$contract_dir/quit-control ]] || fail "hostile quit control did not terminate its test session"

real_zellij attach --create-background detach-proof
detach_private=$(make_private_exact_socket att-detach detach-proof)
expect_attached "$(attach_command "$detach_private" "$shim_cache" detach-proof "$hostile_config" detach)"
real_zellij list-sessions --short | grep -Fx detach-proof >/dev/null || fail "explicit detach did not override hostile quit config"
real_zellij kill-session detach-proof

# Linux sun_path stores 108 bytes including the terminating NUL, leaving a 107-byte
# pathname budget. The production wrapper must reject before launch at that boundary.
max_socket_path_bytes=107
exact_private_path=$exact_private/$contract_dir/exact-session
(( ${#exact_private_path} <= max_socket_path_bytes )) || fail "test private socket path exceeds the pinned limit"
long_session=$(printf 'x%.0s' $(seq 1 160))
long_candidate=$private_root/att-long/$contract_dir/$long_session
(( ${#long_candidate} > max_socket_path_bytes )) || fail "long-path fixture did not exceed the pinned limit"

# GC is deliberately scoped to owned att-* directories; removing stale hard links cannot remove
# or terminate the real session socket.
mkdir -p "$private_root/keep" "$private_root/att-stale/$contract_dir"
ln "$real_base/$contract_dir/exact-session" "$private_root/att-stale/$contract_dir/exact-session"
for stale in "$private_root"/att-*; do
  [[ -d $stale ]] || continue
  rm -rf -- "$stale"
done
[[ -d $private_root/keep ]] || fail "GC removed an unowned directory"
real_zellij list-sessions --short | grep -Fx exact-session >/dev/null || fail "GC of hard links damaged the host session"

printf 'PASS: exact live-only Zellij attachment contract verified with %s\n' "$version_output"
