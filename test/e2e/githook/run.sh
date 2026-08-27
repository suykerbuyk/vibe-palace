#!/usr/bin/env bash
#
# Orchestrator for git post-commit hook (commit.msg reaper) bash e2e cases.
#
# Contract:
#   - Build `vp` ONCE from the real $HOME (before any env rewriting) so
#     Go's module cache is reachable. Then sandbox HOME/XDG_CONFIG_HOME
#     per case.
#   - Each case runs in its own $CASE_DIR with a fresh $HOME via
#     fresh_home (from lib.sh). Cases are fully independent.
#   - On all-pass, cleans up $TMPROOT and exits 0.
#   - On ANY failure, retains $TMPROOT for post-mortem and prints its
#     path. Exit code is non-zero (number of failed cases).
#
# Usage:
#   bash test/e2e/githook/run.sh                 # run all cases
#   bash test/e2e/githook/run.sh 03              # run only 03-*.sh
#   bash test/e2e/githook/run.sh 01 03        # run a subset
#
set -euo pipefail

# Resolve repo root from this script's location.
_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$_SCRIPT_DIR/../../.." && pwd)"
CASES_DIR="$_SCRIPT_DIR"
LIB_SH="$_SCRIPT_DIR/../lib.sh"

# Precondition checks before we touch anything.
if ! command -v go >/dev/null 2>&1; then
  echo "run.sh: 'go' not in PATH — cannot build vp" >&2
  exit 127
fi
if ! command -v git >/dev/null 2>&1; then
  echo "run.sh: 'git' not in PATH — cases drive real commits with it" >&2
  exit 127
fi
if [[ ! -f "$LIB_SH" ]]; then
  echo "run.sh: missing $LIB_SH" >&2
  exit 1
fi

# shellcheck source=../lib.sh
source "$LIB_SH"

TMPROOT="$(mktemp -d -t vp-e2e-githook.XXXXXX)"
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

# Build vp ONCE from the real $HOME (module cache still reachable).
echo "building vp → $TMPROOT/bin/vp"
mkdir -p "$TMPROOT/bin"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/vp" ./cmd/vp)
export VP_BIN="$TMPROOT/bin/vp"
export PATH="$TMPROOT/bin:$PATH"

# Resolve the list of cases to run. No args ⇒ everything; args ⇒ match by
# two-digit prefix.
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

  # Run each case in a subshell so `set -e` / env mutations don't
  # leak across cases.
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
