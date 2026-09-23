#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# ONE-SHOT self-test for this directory's harness. It builds a FIXTURE vault
# (selftest_fixture.py) that uses the real slugs and exercises every rewrite
# class, runs the real pipeline against it, and then injects one defect per
# check to prove that the check FAILS. It never reads or writes the live
# vault, except to copy the plan's task file (read-only) so that H9 lifts the
# Rollback A block that will really run.
#
#   selftest.sh [--dir DIR] [--vp PATH] [--plan TASKFILE] [--quick]
#
# --quick skips the full rehearsal (H1-H12) and runs only the census, cache
# and parity checks with their failure injections.
#
# Exit 0 when every expectation held: SELFTEST PASS.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
DIR="" VPBIN="" QUICK=0
PLAN="$HOME/vibe-palace-vault/Projects/vibe-palace/tasks/migrate-quantum-ng-vault-history-to-qa-metabuild-system.md"
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) DIR=${2:-}; shift ;;
    --vp) VPBIN=${2:-}; shift ;;
    --plan) PLAN=${2:-}; shift ;;
    --quick) QUICK=1 ;;
    -h|--help) sed -n '5,18p' "${BASH_SOURCE[0]}" >&2; exit 2 ;;
    *) echo "selftest.sh: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$DIR" ] || DIR=$(mktemp -d -t slug-selftest-XXXXXX)
mkdir -p "$DIR"
DIR=$(cd "$DIR" && pwd)

if [ -z "$VPBIN" ]; then
  repo=$(cd "$here/../.." && pwd)
  VPBIN="$DIR/vp"
  (cd "$repo" && go build -o "$VPBIN" ./cmd/vp) || { echo "selftest.sh: cannot build vp from $repo" >&2; exit 2; }
fi
export VP="$VPBIN"
FROM=quantum-ng
TO=qa-metabuild-system

# Fixture-sized floors. The defaults are the live figures, so they must be
# lowered here; every other rule is exactly the one that runs live.
export SLUG_FLOOR_FROM_TRACKED=1 SLUG_FLOOR_DRAWERS=1 SLUG_FLOOR_KG_FILES=1 SLUG_FLOOR_CACHE_FROM=1 \
       SLUG_FLOOR_TOTAL=1 SLUG_FLOOR_AUDIT_LINES=1 SLUG_FLOOR_CLASS=1

ok=0 bad=0
pass() { ok=$((ok + 1)); printf 'SELFTEST ok   %s\n' "$1"; }
oops() { bad=$((bad + 1)); printf 'SELFTEST FAIL %s: %s\n' "$1" "$2"; }
# expect NAME EXIT-WANTED TEXT-WANTED -- command...
expect() {
  local name=$1 want=$2 text=$3 out rc
  shift 3
  [ "$1" = -- ] && shift
  set +e
  out=$("$@" 2>&1)
  rc=$?
  set -e
  if [ "$rc" != "$want" ]; then
    oops "$name" "exit $rc, want $want: $(echo "$out" | tail -3 | tr '\n' '|')"
  elif [ -n "$text" ] && ! echo "$out" | grep -qF "$text"; then
    oops "$name" "output lacks '$text': $(echo "$out" | tail -3 | tr '\n' '|')"
  else
    pass "$name"
  fi
}

# ------------------------------------------------------------- the fixture
build() {  # $1 destination; prints the vault path
  rm -rf "$1"
  mkdir -p "$1"
  python3 "$here/selftest_fixture.py" "$1" >/dev/null
  echo "$1/vault"
}

# Run the pre-migration half: warm, census B, counts, parity baseline.
before() {  # $1 fixture dir
  local d=$1
  local v=$d/vault
  XDG_CONFIG_HOME=$d/xdg "$VP" search -p "$FROM" -n 1 warm >/dev/null 2>&1
  XDG_CONFIG_HOME=$d/xdg "$VP" search -p "$TO" -n 1 warm >/dev/null 2>&1
  XDG_CONFIG_HOME=$d/xdg "$here/census.sh" --cache-warm-check --vault "$v" --snapshot "$d/cache-pre.list" >/dev/null
  XDG_CONFIG_HOME=$d/xdg "$here/census.sh" --profile rehearsal --phase B --vault "$v" --out "$d/census-B.json" >/dev/null
  XDG_CONFIG_HOME=$d/xdg "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$v" --json > "$d/counts.json" 2>/dev/null
  XDG_CONFIG_HOME=$d/xdg "$here/parity.sh" --baseline --vault "$v" --counts "$d/counts.json" --out "$d/b1.json" >/dev/null
}

apply() {  # $1 fixture dir
  local d=$1
  local v=$d/vault
  XDG_CONFIG_HOME=$d/xdg "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$v" --apply \
    --expect "$d/counts.json" --expect-head "$(git -C "$v" rev-parse HEAD)" --journal "$d/journal.tsv" > "$d/apply.log" 2>&1
}

census_a() { XDG_CONFIG_HOME=$1/xdg "$here/census.sh" --profile rehearsal --phase A --vault "$1/vault" --b "$1/census-B.json" --counts "$1/counts.json" --out "$1/census-A.json"; }
cache_report() { XDG_CONFIG_HOME=$1/xdg "$here/census.sh" --cache-report "$1/apply.log" --pre "$1/cache-pre.list" --stray "$1/journal.d/stray.list" --vault "$1/vault"; }
parity_check() { XDG_CONFIG_HOME=$1/xdg "$here/parity.sh" --vault "$1/vault" --b1 "$1/b1.json" --runs "${2:-1}"; }
warm_check() { XDG_CONFIG_HOME=$1/xdg "$here/census.sh" --cache-warm-check --vault "$1/vault"; }

# ------------------------------------------------------------- 1. the happy path
G="$DIR/good"
build "$G" >/dev/null
before "$G"
expect "warm check passes on a warm cache" 0 "CACHE WARM" -- warm_check "$G"
apply "$G"
expect "apply makes three commits" 0 "3" -- grep -c "^K[012] " "$G/apply.log"
expect "cache report passes" 0 "CACHE-REPORT PASS" -- cache_report "$G"
expect "census A passes" 0 "CENSUS PASS" -- census_a "$G"
expect "parity passes, twice over" 0 "PARITY PASS" -- parity_check "$G" 2

# A second fixture, used as a "wrong vault" and a "foreign census B".
B2="$DIR/other"
build "$B2" >/dev/null
before "$B2"

# ------------------------------------------------------------- 2. each check can fail
# Each injection starts from a fresh fixture, so one defect is the only difference.
inject() {  # $1 name, $2 stage (before|after), $3 shell snippet, $4 checker, $5 text
  local d="$DIR/inj-$1"
  build "$d" >/dev/null
  before "$d"
  if [ "$2" = after ]; then apply "$d"; fi
  ( cd "$d" && eval "$3" )
  expect "$1" 1 "$5" -- "$4" "$d"
}

inject c1t-missing-session after 'rm vault/Projects/'"$TO"'/sessions/2026-08-22-0f0d1eb5-02.md && git -C vault commit -q -am drop' census_a "CENSUS FAIL"
inject c3-old-identifier after 'printf "\nproject: '"$FROM"'\n" >> vault/Projects/'"$TO"'/resume.md && git -C vault commit -q -am leak' census_a "CENSUS FAIL"
# The C3 ruling: the accepted[] entries must be rewritten and the reason prose
# must NOT be, so a prose edit after the migration has to fail the census.
inject c3-baseline-prose-edited after 'python3 - <<EOF
import json,pathlib
p=pathlib.Path("vault/Audits/baseline.json")
d=json.loads(p.read_text())
r=d["dimensions"]["archive-roundtrip"]
r["reason"]=r["reason"].replace("Projects/'"$FROM"'/","Projects/'"$TO"'/")
p.write_text(json.dumps(d,indent=2)+"\n")
EOF
git -C vault commit -q -am prose' census_a "CENSUS FAIL"
inject c6-kg-changed after 'printf "{}\n" >> vault/palace/'"$TO"'/kg/entities.jsonl && git -C vault commit -q -am kg' census_a "CENSUS FAIL"
inject c2-extra-commit after 'git -C vault commit -q --allow-empty -m interloper' census_a "CENSUS FAIL"
inject warm-missing-vector before 'rm "$(ls vault/palace/.local/embed-cache/'"$FROM"'/*.vec | head -1)"' warm_check "CACHE COLD"
inject cache-report-lost-vector after 'rm "$(ls vault/palace/.local/embed-cache/'"$TO"'/iter.*.vec | head -1)"' cache_report "CACHE-REPORT FAIL"
inject cache-report-stray-alive after 'cp journal.d/deleted/* "vault/palace/.local/embed-cache/'"$TO"'/$(head -1 journal.d/stray.list | cut -d" " -f2)"' cache_report "CACHE-REPORT FAIL"
inject parity-content-drift after 'python3 - <<EOF
import json,pathlib
p=pathlib.Path("vault/palace/'"$TO"'/drawers/'"$TO"'/general/drawers.jsonl")
rows=[json.loads(x) for x in p.read_text().splitlines() if x.strip()]
rows[0]["content"]="a completely different fact about nothing at all"
p.write_text("".join(json.dumps(r,separators=(",",":"))+"\n" for r in rows))
EOF
git -C vault commit -q -am drift' parity_check "PARITY FAIL"
# A vector the migration should have carried across is missing, so run A
# re-embeds it: the run creates a vector that is not the sanctioned stray one.
inject parity-unexpected-embed after 'rm "$(ls vault/palace/.local/embed-cache/'"$TO"'/note.*-03.c0.vec | head -1)"' parity_check "PARITY FAIL"

# imp2 round-4 SHOULD-FIX 2: the two checks added in the last rounds need
# their own injections, or "every check can fail" is a claim about the others.
# C4's second direction: a finding that DISAPPEARS between B and A.
inject c4-audit-finding-disappeared after 'python3 - <<EOF
import json,pathlib
p=pathlib.Path("census-B.json"); d=json.loads(p.read_text())
d["audit"]["lines"].append("- `Projects/'"$FROM"'/tasks/ghost.md` \u2014 a finding that will vanish")
p.write_text(json.dumps(d,indent=1))
EOF' census_a "CENSUS FAIL"
# F2: an A query that comes back shallower than its own B1 baseline.
expect "parity fails when a query returns fewer rows than B1" 1 "after dropping strays, B1 measured" -- \
  env SLUG_SELFTEST_TRUNCATE_A=1 XDG_CONFIG_HOME="$G/xdg" "$here/parity.sh" --vault "$G/vault" --b1 "$G/b1.json"

# The script-layer pins: a check that cannot see the right vault must refuse,
# and a census measured without its MCP inputs must FAIL, not skip (C7).
expect "parity refuses a vault vp does not resolve" 2 "not the vault under test" -- \
  env XDG_CONFIG_HOME="$DIR/other/xdg" "$here/parity.sh" --vault "$G/vault" --b1 "$G/b1.json"
expect "census A refuses when the MCP inputs were not measured" 1 "were not measured" -- \
  env XDG_CONFIG_HOME="$G/xdg" bash -c 'python3 "$0" measure --vault "$1/vault" --profile rehearsal --phase A --out "$1/census-A-nomcp.json" --no-mcp --no-audit >/dev/null && python3 "$0" verify --b "$1/census-B.json" --a "$1/census-A-nomcp.json" --counts "$1/counts.json"' "$here/slugcheck.py" "$G"

# F3: a query that returns nothing is broken, and the baseline must say so.
# It runs against the UNMIGRATED second fixture, which still has both slugs.
expect "parity baseline refuses a query that returns 0 rows" 1 "the query is broken" -- \
  env SLUG_SELFTEST_EMPTY_QUERY=1 XDG_CONFIG_HOME="$B2/xdg" "$here/parity.sh" --baseline --vault "$B2/vault" --counts "$B2/counts.json" --out "$B2/b1-broken.json"

# A census A measured against the wrong B must fail, not pass quietly.
expect "census A against a foreign B fails" 1 "CENSUS FAIL" -- env XDG_CONFIG_HOME="$G/xdg" "$here/census.sh" --profile rehearsal --phase A --vault "$G/vault" --b "$B2/census-B.json" --counts "$G/counts.json" --out "$G/census-A2.json"
expect "compare reports SAME for one file against itself" 0 SAME -- "$here/census.sh" --compare "$G/census-B.json" "$G/census-B.json"
expect "compare reports DIFF between two fixtures" 1 DIFF -- "$here/census.sh" --compare "$G/census-B.json" "$B2/census-B.json"
expect "census refuses a vault vp does not resolve" 2 "not the vault vp resolves" -- env XDG_CONFIG_HOME="$B2/xdg" "$here/census.sh" --profile rehearsal --phase B --vault "$G/vault" --out "$DIR/never.json"

# ------------------------------------------------------------- 3. the rehearsal
if [ "$QUICK" = 0 ]; then
  RF="$DIR/fixture-source"
  build "$RF" >/dev/null
  cp "$PLAN" "$DIR/plan.md"
  # The rehearsal refuses before it copies anything: a RAM-backed root, and a
  # space requirement it cannot meet. Both were learned the hard way — the
  # first R-dev attempt filled a tmpfs with 21 GB of copies (E1, E2).
  expect "rehearsal refuses a tmpfs copy root" 1 "RAM-backed" -- \
    "$here/rehearsal.sh" --r /dev/shm/slug-selftest-tmpfs-probe --source "$RF/vault" --project-repo "$RF/project-repo" --plan "$DIR/plan.md"
  # The space refusal needs a root on a REAL filesystem, or the tmpfs refusal
  # above fires first; $HOME is the one place we know is not RAM-backed.
  SPACE_ROOT=$(mktemp -d "$HOME/.slug-selftest-space-XXXXXX")
  expect "rehearsal refuses when the space is not there" 1 "free and this run needs" -- \
    env SLUG_REHEARSAL_EXTRA_KB=999999999 "$here/rehearsal.sh" --r "$SPACE_ROOT" --source "$RF/vault" --project-repo "$RF/project-repo" --plan "$DIR/plan.md"
  rm -rf "$SPACE_ROOT"
  expect "rehearsal H1-H12 passes on the fixture" 0 "REHEARSAL PASS" -- \
    env SLUG_DRILL_N_A=1 SLUG_DRILL_N_B=3 "$here/rehearsal.sh" --r "$DIR/R" --source "$RF/vault" \
        --project-repo "$RF/project-repo" --plan "$DIR/plan.md"
  sed 's/ROLLBACK A DONE/NOT THE BLOCK/' "$DIR/plan.md" > "$DIR/plan-bad.md"
  expect "rehearsal refuses a plan whose Rollback A block is wrong" 1 "REHEARSAL FAIL H9" -- \
    env SLUG_DRILL_N_A=1 SLUG_DRILL_N_B=3 "$here/rehearsal.sh" --r "$DIR/R-bad" --source "$RF/vault" \
        --project-repo "$RF/project-repo" --plan "$DIR/plan-bad.md"
fi

echo
echo "fixtures under $DIR"
if [ "$bad" = 0 ]; then
  echo "SELFTEST PASS ($ok expectations)"
else
  echo "SELFTEST FAIL ($bad of $((ok + bad)) expectations)"
  exit 1
fi
