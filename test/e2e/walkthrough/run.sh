#!/usr/bin/env bash
#
# Orchestrator for the vibe-palace end-to-end walkthrough.
#
# Unlike test/e2e/init/run.sh, the walkthrough is intentionally a SINGLE
# case: a narrated happy-path that mirrors TUTORIAL Part 2. The transcript
# printed to stdout on success IS the documentation — if a future beat
# needs covering, add STEPS to the case, not new cases. (A scenario grid
# belongs in test/e2e/workflows/.)
#
# Contract:
#   - Build vp AND seeddrawer ONCE from the real $HOME (module cache
#     reachable) BEFORE any case-time env rewriting.
#   - Exactly one *.sh file (other than run.sh) must live under this dir.
#     Zero or more-than-one is a structural error.
#   - The case's subshell output is teed to $CASE_LOGDIR/transcript.log.
#     On success the transcript is cat'd to stdout. On failure the
#     retained tmpdir is printed and the transcript-so-far is emitted for
#     post-mortem.
#
# Usage:
#   bash test/e2e/walkthrough/run.sh

set -euo pipefail

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
  echo "run.sh: 'git' not in PATH — cases need it to mark project dirs" >&2
  exit 127
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "run.sh: 'jq' not in PATH — walkthrough shape-checks require it" >&2
  exit 127
fi
if [[ ! -f "$LIB_SH" ]]; then
  echo "run.sh: missing $LIB_SH" >&2
  exit 1
fi

# shellcheck source=../lib.sh
source "$LIB_SH"

TMPROOT="$(mktemp -d -t vp-e2e-walkthrough.XXXXXX)"
export TMPROOT
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

# Build vp + seeddrawer ONCE from the real $HOME (module cache reachable).
echo "building vp → $TMPROOT/bin/vp"
mkdir -p "$TMPROOT/bin"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/vp" ./cmd/vp)
echo "building seeddrawer → $TMPROOT/bin/seeddrawer"
(cd "$REPO_ROOT" && go build -o "$TMPROOT/bin/seeddrawer" ./test/e2e/internal/seeddrawer)
export VP_BIN="$TMPROOT/bin/vp"
export PATH="$TMPROOT/bin:$PATH"

# Locate the single case. Walkthrough is deliberately singular.
shopt -s nullglob
all_cases=()
for f in "$CASES_DIR"/*.sh; do
  [[ "$(basename "$f")" == "run.sh" ]] && continue
  all_cases+=( "$f" )
done
if [[ ${#all_cases[@]} -eq 0 ]]; then
  echo "run.sh: no case found under $CASES_DIR (expected exactly one *.sh besides run.sh)" >&2
  exit 1
fi
if [[ ${#all_cases[@]} -gt 1 ]]; then
  echo "run.sh: multiple cases found under $CASES_DIR — walkthrough must be a single case:" >&2
  for c in "${all_cases[@]}"; do
    echo "  - $(basename "$c")" >&2
  done
  echo "run.sh: add STEPS to the existing case, not new cases. Scenario grids belong in workflows/." >&2
  exit 1
fi

case_script="${all_cases[0]}"
case_name="$(basename "$case_script" .sh)"

export CASE_DIR="$TMPROOT/cases/$case_name"
export CASE_LOGDIR="$CASE_DIR/logs"
mkdir -p "$CASE_DIR" "$CASE_LOGDIR"

echo ""
echo "=== $case_name ==="

# Run the case in a subshell so `set -e` and env mutations don't leak.
# PIPING into tee DEFEATS `set -e` propagation out of the subshell — the
# exit status of the pipeline is tee's. Capture the subshell's real exit
# via PIPESTATUS[0].
set +e
( set -e; source "$LIB_SH"; source "$case_script" ) 2>&1 | tee "$CASE_LOGDIR/transcript.log"
rc="${PIPESTATUS[0]}"
set -e

if [[ "$rc" -eq 0 ]]; then
  echo ""
  echo "=== transcript ==="
  cat "$CASE_LOGDIR/transcript.log"
  echo ""
  echo "PASS: $case_name"
  SUCCESS=1
  exit 0
fi

echo ""
echo "FAIL: $case_name (exit $rc)"
echo "  logs: $CASE_LOGDIR"
echo "  retained tmpdir: $TMPROOT"
if [[ -s "$CASE_LOGDIR/transcript.log" ]]; then
  echo "  --- transcript so far ---"
  sed 's/^/  /' "$CASE_LOGDIR/transcript.log"
fi
if [[ -s "$CASE_LOGDIR/stderr.log" ]]; then
  echo "  --- last run_vp stderr ---"
  tail -n 20 "$CASE_LOGDIR/stderr.log" | sed 's/^/  /'
fi
exit "$rc"
