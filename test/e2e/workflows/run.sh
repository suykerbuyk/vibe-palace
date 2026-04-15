#!/usr/bin/env bash
#
# Orchestrator for vp workflow measurement e2e cases.
#
# Contract mirrors test/e2e/init/run.sh with these deltas:
#   - Builds vp + mockllm + seeddrawer + tomleq ONCE at top.
#   - Pre-creates $CASE_DIR/metrics.jsonl before each case so emit_metric
#     has a sink.
#   - After all cases PASS, walks every cases/*/metrics.jsonl and prints a
#     summary table to STDERR. Summary is informational — it never changes
#     exit code.
#   - Retain-on-fail semantics identical to init harness.
#
# Exports WORKFLOWS_DIR so cases can reference $WORKFLOWS_DIR/mockllm/...
# without re-computing $_SCRIPT_DIR.
#
# Usage:
#   bash test/e2e/workflows/run.sh                 # run all cases
#   bash test/e2e/workflows/run.sh 01              # run only 01-*.sh
#   bash test/e2e/workflows/run.sh 01 02           # run a subset

set -euo pipefail

_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$_SCRIPT_DIR/../../.." && pwd)"
CASES_DIR="$_SCRIPT_DIR"
LIB_SH="$_SCRIPT_DIR/../lib.sh"

# Precondition checks.
if ! command -v go >/dev/null 2>&1; then
  echo "run.sh: 'go' not in PATH — cannot build vp/mockllm" >&2
  exit 127
fi
if ! command -v git >/dev/null 2>&1; then
  echo "run.sh: 'git' not in PATH — cases need it to mark project dirs" >&2
  exit 127
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "run.sh: 'jq' not in PATH — workflow shape-checks and summary require it" >&2
  exit 127
fi
if [[ ! -f "$LIB_SH" ]]; then
  echo "run.sh: missing $LIB_SH" >&2
  exit 1
fi

# shellcheck source=../lib.sh
source "$LIB_SH"

TMPROOT="$(mktemp -d -t vp-e2e-workflows.XXXXXX)"
export TMPROOT
export WORKFLOWS_DIR="$_SCRIPT_DIR"
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

# Build helper binaries ONCE from the real $HOME (Go module cache reachable).
mkdir -p "$TMPROOT/bin"
echo "building vp         → $TMPROOT/bin/vp"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/vp"         ./cmd/vp)
echo "building mockllm    → $TMPROOT/bin/mockllm"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/mockllm"    ./test/e2e/workflows/mockllm)
echo "building seeddrawer → $TMPROOT/bin/seeddrawer"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/seeddrawer" ./test/e2e/internal/seeddrawer)
echo "building tomleq     → $TMPROOT/bin/tomleq"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/tomleq"     ./test/e2e/internal/tomleq)
export VP_BIN="$TMPROOT/bin/vp"
export PATH="$TMPROOT/bin:$PATH"

# Resolve case list.
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
  # Pre-create the metrics sink so emit_metric always has a file to append to.
  : >"$CASE_DIR/metrics.jsonl"
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
if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: $FAILED of $((PASSED + FAILED)) cases failed"
  for n in "${FAILED_NAMES[@]}"; do
    echo "  - $n"
  done
  exit "$FAILED"
fi

echo "PASS: all $PASSED cases"

# ---------------------------------------------------------------------------
# Informational metrics summary. Never changes the exit code.
# ---------------------------------------------------------------------------
{
  echo ""
  echo "=== metrics summary ==="
  for mfile in "$TMPROOT"/cases/*/metrics.jsonl; do
    [[ -s "$mfile" ]] || continue
    case_name="$(basename "$(dirname "$mfile")")"
    echo "case: $case_name"
    printf '%-5s %-22s %-7s %-7s %-8s %-9s\n' "iter" "cmd" "ms" "tokens" "samples" "proposals"
    jq -r '
      . as $r
      | [
          ( $r.iter        // "-" | tostring ),
          ( $r.cmd         // "-" | tostring ),
          ( $r.ms          // "-" | tostring ),
          ( $r.tokens      // "-" | tostring ),
          ( $r.samples     // "-" | tostring ),
          ( $r.proposals   // "-" | tostring )
        ]
      | @tsv
    ' "$mfile" 2>/dev/null | while IFS=$'\t' read -r iter cmd ms tokens samples proposals; do
      printf '%-5s %-22s %-7s %-7s %-8s %-9s\n' \
        "$iter" "$cmd" "$ms" "$tokens" "$samples" "$proposals"
    done
    echo ""
  done
} >&2

SUCCESS=1
exit 0
