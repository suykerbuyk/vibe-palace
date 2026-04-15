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

# ---------------------------------------------------------------------------
# Phase 1 — workflow measurement rig helpers.
#
# Contract additions for these helpers:
#   - Metric helpers (emit_metric, time_ms_*) MUST NOT exit on failure.
#     Hard assertions still use the assert_* helpers above.
#   - Every workflow case that emits metrics MUST set
#     METRIC_FILE="$CASE_DIR/metrics.jsonl" before calling emit_metric.
#   - Helpers that depend on built binaries expect $TMPROOT/bin/<name>
#     to exist (the workflows orchestrator builds these once up front).
# ---------------------------------------------------------------------------

emit_metric() {
  # usage: emit_metric <jsonl-file> key=val [key=val ...]
  # Appends one JSONL row. Auto-injects ts (epoch ms), case ($CASE_NAME),
  # and iter ($METRIC_ITER if set). Numeric-looking values are emitted as
  # JSON numbers; everything else as JSON strings. Never fails the case.
  local file="${1:-}"; shift || true
  if [[ -z "$file" ]]; then
    echo "emit_metric: missing file arg (ignored)" >&2
    return 0
  fi
  local ts; ts=$(date +%s%3N 2>/dev/null || echo 0)
  local out="{\"ts\":$ts"
  if [[ -n "${CASE_NAME:-}" ]]; then
    out+=",\"case\":\"${CASE_NAME//\"/\\\"}\""
  fi
  if [[ -n "${METRIC_ITER:-}" ]]; then
    out+=",\"iter\":${METRIC_ITER}"
  fi
  local kv k v
  for kv in "$@"; do
    k="${kv%%=*}"
    v="${kv#*=}"
    if [[ "$v" =~ ^-?[0-9]+(\.[0-9]+)?$ ]]; then
      out+=",\"${k}\":${v}"
    else
      # Escape backslashes then double-quotes for JSON string literal.
      local esc="${v//\\/\\\\}"
      esc="${esc//\"/\\\"}"
      out+=",\"${k}\":\"${esc}\""
    fi
  done
  out+="}"
  if ! printf '%s\n' "$out" >>"$file" 2>/dev/null; then
    echo "emit_metric: append to $file failed (ignored)" >&2
  fi
  return 0
}

assert_shape() {
  # usage: assert_shape <json-file> <jq-expr> <expected-stringified>
  local file="$1" expr="$2" expected="$3"
  local actual
  if ! actual=$(jq -ec "$expr" "$file" 2>/dev/null); then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "jq '$expr' failed on $file"
    exit 1
  fi
  if [[ "$actual" != "$expected" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "shape mismatch $file '$expr': got $actual, want $expected"
    exit 1
  fi
}

time_ms_start() {
  # usage: time_ms_start <label>
  local label="$1"
  local ns; ns=$(date +%s%N 2>/dev/null || echo 0)
  eval "__TIMER_${label}=$(( ns / 1000000 ))"
}

time_ms_end() {
  # usage: time_ms_end <label>
  # Prints elapsed ms to stdout and clears the timer.
  local label="$1"
  local start_var="__TIMER_${label}"
  local start="${!start_var:-0}"
  local ns; ns=$(date +%s%N 2>/dev/null || echo 0)
  local now=$(( ns / 1000000 ))
  local elapsed=$(( now - start ))
  unset "$start_var"
  printf '%d\n' "$elapsed"
}

json_field() {
  # usage: json_field <file> <jq-expr>
  jq -r "$2" "$1"
}

seed_drawer() {
  # usage: seed_drawer <project> <room> <content>
  # Thin wrapper around the seeddrawer Go binary. Fails hard on error —
  # seeding is load-bearing for workflow cases.
  : "${TMPROOT:?TMPROOT not set}"
  local bin="$TMPROOT/bin/seeddrawer"
  if [[ ! -x "$bin" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "seed_drawer: missing binary $bin (build it first)"
    exit 1
  fi
  if ! "$bin" "$@"; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "seed_drawer: seeddrawer $* exited non-zero"
    exit 1
  fi
}

vault_project_config_path() {
  # usage: vault_project_config_path <project>
  # Mirrors storage.Vault.ProjectConfigFile:
  #   {vault}/Projects/{project}/config.toml
  printf '%s/vibe-palace-vault/Projects/%s/config.toml\n' "$HOME" "$1"
}

assert_toml_struct_equal() {
  # usage: assert_toml_struct_equal <fileA> <fileB>
  # Decoded-TOML-struct equality via the tomleq Go helper (sha256 is not
  # safe — toml.NewEncoder is not byte-stable across round-trips).
  : "${TMPROOT:?TMPROOT not set}"
  local bin="$TMPROOT/bin/tomleq"
  if [[ ! -x "$bin" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "assert_toml_struct_equal: missing binary $bin"
    exit 1
  fi
  if ! "$bin" "$1" "$2"; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "TOML struct differs: $1 vs $2"
    exit 1
  fi
}

start_mock_llm() {
  # usage: start_mock_llm [--script FILE]
  # Starts $TMPROOT/bin/mockllm on an ephemeral port. Exports:
  #   VP_MOCK_LLM_URL  — http://127.0.0.1:PORT (base URL, no path suffix)
  #   VP_MOCK_LLM_LOG  — JSONL request log path
  #   VP_MOCK_LLM_PID  — background PID (used by stop_mock_llm)
  # On EXIT, installs a trap that kills the server.
  : "${TMPROOT:?TMPROOT not set}"
  : "${CASE_DIR:?CASE_DIR not set}"
  local bin="$TMPROOT/bin/mockllm"
  if [[ ! -x "$bin" ]]; then
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "start_mock_llm: missing binary $bin"
    exit 1
  fi
  local script=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --script) script="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  if [[ -n "$script" ]]; then
    export MOCK_RESPONSE_FILE="$script"
  fi
  export MOCK_LOG="$CASE_DIR/mockllm.log"
  : >"$MOCK_LOG"
  local outfile="$CASE_DIR/mockllm.stdout"
  local errfile="$CASE_DIR/mockllm.stderr"
  "$bin" --port 0 >"$outfile" 2>"$errfile" &
  local pid=$!
  # Read the first line of stdout (the chosen port).
  local port=""
  local i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if [[ -s "$outfile" ]]; then
      port=$(head -n 1 "$outfile")
      [[ -n "$port" ]] && break
    fi
    sleep 0.05
  done
  if [[ -z "$port" ]]; then
    kill "$pid" 2>/dev/null || true
    _e2e_fail "${BASH_SOURCE[1]}" "${BASH_LINENO[0]}" \
      "start_mock_llm: timed out reading port from $outfile"
    exit 1
  fi
  export VP_MOCK_LLM_URL="http://127.0.0.1:$port"
  export VP_MOCK_LLM_LOG="$MOCK_LOG"
  export VP_MOCK_LLM_PID="$pid"
  trap 'stop_mock_llm' EXIT
}

stop_mock_llm() {
  # Idempotent teardown. Safe to call multiple times.
  if [[ -n "${VP_MOCK_LLM_PID:-}" ]]; then
    kill -TERM "$VP_MOCK_LLM_PID" 2>/dev/null || true
    unset VP_MOCK_LLM_PID
  fi
}

write_project_llm_config() {
  # usage: write_project_llm_config <project> <url>
  # Appends a [palace.llm] stanza to the project's vault config.toml.
  # The internal/llm client treats Endpoint as a BASE URL and appends
  # "/chat/completions" itself, so we write the base URL as-is.
  # Also exports VP_MOCK_KEY=mock-key for api_key_env resolution.
  local project="$1" url="$2"
  local cfg; cfg=$(vault_project_config_path "$project")
  mkdir -p "$(dirname "$cfg")"
  {
    echo ""
    echo "[palace.llm]"
    echo "endpoint = \"$url\""
    echo "model = \"mock-model\""
    echo "api_key_env = \"VP_MOCK_KEY\""
    echo "max_tokens = 256"
  } >>"$cfg"
  export VP_MOCK_KEY="mock-key"
}
