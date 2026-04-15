# Shared helpers for vp e2e bash cases.
#
# Cases source this file. It must be idempotent and have no side effects
# beyond defining functions.
#
# Contract:
#   - All assertion helpers print `FAIL: <file>:<LINENO>: <msg>` on
#     failure and return 1. Cases set `set -e` so returning 1 aborts.
#   - run_vp captures stdout+stderr to $CASE_LOGDIR and sets
#     LAST_EXIT_CODE. It never aborts on non-zero exit — callers
#     inspect LAST_EXIT_CODE themselves.
#   - fresh_home mints a clean HOME + XDG_CONFIG_HOME under $CASE_DIR
#     and exports both. Call this once at the top of every case
#     BEFORE invoking run_vp.

_e2e_fail() {
  # $1 = caller source, $2 = caller line, $3 = message
  echo "FAIL: ${1}:${2}: ${3}" >&2
  return 1
}

assert_file_exists() {
  # usage: assert_file_exists PATH
  if [[ ! -f "$1" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "expected file to exist: $1"
    exit 1
  fi
}

assert_file_absent() {
  if [[ -e "$1" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "expected file absent, but exists: $1"
    exit 1
  fi
}

assert_dir_exists() {
  if [[ ! -d "$1" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "expected directory to exist: $1"
    exit 1
  fi
}

assert_dir_absent() {
  if [[ -e "$1" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "expected directory absent, but exists: $1"
    exit 1
  fi
}

assert_grep() {
  # usage: assert_grep PATTERN FILE
  if ! grep -qE "$1" "$2" 2>/dev/null; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "pattern '$1' not found in $2"
    exit 1
  fi
}

assert_not_grep() {
  if grep -qE "$1" "$2" 2>/dev/null; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "pattern '$1' unexpectedly found in $2"
    exit 1
  fi
}

assert_exit_code() {
  # usage: assert_exit_code EXPECTED ACTUAL
  if [[ "$1" != "$2" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" "exit code = $2, want $1"
    exit 1
  fi
}

run_vp() {
  # Invokes $VP_BIN with the given args, capturing stdout+stderr.
  # Sets LAST_EXIT_CODE. Does NOT abort on non-zero exit.
  : "${VP_BIN:?VP_BIN not set}"
  : "${CASE_LOGDIR:?CASE_LOGDIR not set}"
  mkdir -p "$CASE_LOGDIR"
  set +e
  "$VP_BIN" "$@" >"$CASE_LOGDIR/stdout.log" 2>"$CASE_LOGDIR/stderr.log"
  LAST_EXIT_CODE=$?
  set -e
  export LAST_EXIT_CODE
}

fresh_home() {
  # Mints a clean HOME + XDG_CONFIG_HOME under $CASE_DIR. Every case
  # calls this before run_vp so $HOME/vibe-palace-vault and
  # $XDG_CONFIG_HOME/vibe-palace/config.toml start empty.
  : "${CASE_DIR:?CASE_DIR not set}"
  export CASE_HOME="$CASE_DIR/home"
  export HOME="$CASE_HOME"
  export XDG_CONFIG_HOME="$CASE_HOME/.config"
  mkdir -p "$CASE_HOME" "$XDG_CONFIG_HOME"
}
