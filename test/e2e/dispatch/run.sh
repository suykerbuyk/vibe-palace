#!/usr/bin/env bash
#
# Orchestrator for `vp` dispatch bash e2e cases. Each case verifies
# end-user CLI behavior for the parent-command dispatch gate:
# bare parent → help on stdout (exit 0); unknown subcommand → usage
# error on stderr (exit 1). See agentctx/tasks/cli-parent-bare-
# invocation-shows-help.md for context.
#
# Mirrors the contract of test/e2e/init/run.sh: build vp once from
# the real $HOME, sandbox HOME/XDG_CONFIG_HOME per case, retain
# $TMPROOT on failure, clean up on pass.
#
# Usage:
#   bash test/e2e/dispatch/run.sh                 # all cases
#   bash test/e2e/dispatch/run.sh 02              # only 02-*.sh
set -euo pipefail

_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$_SCRIPT_DIR/../../.." && pwd)"
CASES_DIR="$_SCRIPT_DIR"
LIB_SH="$_SCRIPT_DIR/../lib.sh"

if ! command -v go >/dev/null 2>&1; then
  echo "run.sh: 'go' not in PATH — cannot build vp" >&2
  exit 127
fi
if [[ ! -f "$LIB_SH" ]]; then
  echo "run.sh: missing $LIB_SH" >&2
  exit 1
fi

# shellcheck source=../lib.sh
source "$LIB_SH"

TMPROOT="$(mktemp -d -t vp-e2e-dispatch.XXXXXX)"
SUCCESS=0

cleanup() {
  if [[ "${SUCCESS:-0}" == "1" ]]; then
    rm -rf "$TMPROOT"
  else
    echo ""
    echo "retained tmpdir: $TMPROOT" >&2
  fi
}
trap cleanup EXIT

echo "building vp → $TMPROOT/bin/vp"
mkdir -p "$TMPROOT/bin"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/vp" ./cmd/vp)
export VP_BIN="$TMPROOT/bin/vp"
export PATH="$TMPROOT/bin:$PATH"

shopt -s nullglob
all_cases=( "$CASES_DIR"/[0-9][0-9]-*.sh )
if [[ ${#all_cases[@]} -eq 0 ]]; then
  echo "run.sh: no cases found under $CASES_DIR" >&2
  exit 1
fi

if [[ $# -gt 0 ]]; then
  selected=()
  for prefix in "$@"; do
    found=0
    for c in "${all_cases[@]}"; do
      if [[ "$(basename "$c")" == "${prefix}-"* ]]; then
        selected+=( "$c" )
        found=1
      fi
    done
    if [[ $found -eq 0 ]]; then
      echo "run.sh: no case matching prefix '$prefix'" >&2
      exit 1
    fi
  done
  all_cases=( "${selected[@]}" )
fi

PASSED=0
FAILED=0
FAILED_NAMES=()

for case_script in "${all_cases[@]}"; do
  case_name="$(basename "$case_script" .sh)"
  echo ""
  echo "=== $case_name ==="

  export CASE_DIR="$TMPROOT/cases/$case_name"
  export CASE_LOGDIR="$CASE_DIR/logs"
  mkdir -p "$CASE_DIR" "$CASE_LOGDIR"
  fresh_home

  if ( set -e; source "$LIB_SH"; source "$case_script" ); then
    echo "PASS: $case_name"
    PASSED=$((PASSED + 1))
  else
    rc=$?
    echo "FAIL: $case_name (exit $rc)"
    echo "  logs: $CASE_LOGDIR"
    if [[ -s "$CASE_LOGDIR/stderr.log" ]]; then
      echo "  --- stderr tail ---"
      tail -n 20 "$CASE_LOGDIR/stderr.log" | sed 's/^/  /'
    fi
    FAILED=$((FAILED + 1))
    FAILED_NAMES+=( "$case_name" )
  fi
done

echo ""
echo "================================"
if [[ $FAILED -eq 0 ]]; then
  echo "PASS: all $PASSED cases"
  SUCCESS=1
  exit 0
fi

echo "FAIL: $FAILED of $((PASSED + FAILED)) cases failed"
for n in "${FAILED_NAMES[@]}"; do
  echo "  - $n"
done
exit "$FAILED"
