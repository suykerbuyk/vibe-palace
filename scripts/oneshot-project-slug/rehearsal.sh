#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# ONE-SHOT rehearsal (H1-H12) for `vp migrate project-slug`, from the task
# migrate-quantum-ng-vault-history-to-qa-metabuild-system, section "Rehearsal
# on a remote-stripped copy". Deleted with the rest of scripts/oneshot-project-slug/.
#
#   rehearsal.sh --r DIR [--final] [--source VAULT] [--project-repo DIR]
#                [--plan TASKFILE] [--no-isolation] [--keep]
#
# --r            scratch root (tmpfs is fine); every copy lives under it
# --final        R-final: refuse a dirty copy instead of freezing copy dirt
# --source       vault to copy (default ~/vibe-palace-vault); NEVER written to
# --project-repo repo to clone for the capture proofs (default ~/code/qa-metabuild-system)
# --plan         task file to lift the *verbatim* Rollback A block from
#                (default: the task in the source vault)
# --no-isolation skip the H4 user-namespace read-only bind and use the plan's
#                recorded-state fallback instead
#
# It prints one line per step and ends with REHEARSAL PASS (exit 0) or
# REHEARSAL FAIL <step> (exit 1). R-final's recorded outputs land in DIR/out:
# counts.json, b1.json, census-B.json, vp.sha256.
#
# The live vault is only ever READ (H4 binds it read-only). Everything else
# happens inside DIR. VP names the binary (default: vp).
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export VP="${VP:-vp}"
FROM=quantum-ng
TO=qa-metabuild-system
N_A="${SLUG_DRILL_N_A:-20}"      # journal lines before the DRILL2a kill
N_B="${SLUG_DRILL_N_B:-1044}"    # ... and before the DRILL2b kill

# Kept for the H4 re-exec, which happens after this loop has consumed them.
ARGV=("$@")
SELF="$here/$(basename "${BASH_SOURCE[0]}")"

R="" SOURCE="$HOME/vibe-palace-vault" REPO="$HOME/code/qa-metabuild-system" PLAN="" FINAL=0 ISO=1 INSIDE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --r) R=${2:-}; shift ;;
    --final) FINAL=1 ;;
    --source) SOURCE=${2:-}; shift ;;
    --project-repo) REPO=${2:-}; shift ;;
    --plan) PLAN=${2:-}; shift ;;
    --no-isolation) ISO=0 ;;
    --inside) INSIDE=1 ;;
    -h|--help) sed -n '5,25p' "${BASH_SOURCE[0]}" >&2; exit 2 ;;
    *) echo "rehearsal.sh: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$R" ] || { echo "rehearsal.sh: --r DIR is required" >&2; exit 2; }
R=$(mkdir -p "$R" && cd "$R" && pwd)
SOURCE=$(cd "$SOURCE" && pwd)
: "${PLAN:=$SOURCE/Projects/vibe-palace/tasks/migrate-quantum-ng-vault-history-to-qa-metabuild-system.md}"

fail() { echo "REHEARSAL FAIL $1: $2" >&2; exit 1; }
step() { printf '%s %s\n' "$1" "$2"; }
vault_of() { echo "$R/$1"; }
xdg_of() { echo "$R/xdg-$1"; }
# Run vp against one copy. Never exports XDG_CONFIG_HOME into the shell.
vpc() { local c=$1; shift; XDG_CONFIG_HOME=$(xdg_of "$c") "$VP" "$@"; }
tree_hash() { (cd "$1" && find . -path ./.git -prune -o -path ./.vp-locks -prune -o -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum); }
head_of() { git -C "$1" rev-parse HEAD; }
clean_p() { [ "$(git -C "$1" --no-optional-locks status --porcelain=v1 -uall | wc -l)" = 0 ]; }

# --------------------------------------------------------------- H4 first
# The read-only bind must wrap every later step, so it re-execs this script
# inside a user+mount namespace before anything is copied.
if [ "$ISO" = 1 ] && [ "$INSIDE" = 0 ]; then
  if unshare --user --map-root-user --mount true 2>/dev/null; then
    exec unshare --user --map-root-user --mount bash -c '
      set -eu
      mount --bind "$1" "$1" && mount -o remount,bind,ro "$1"
      if touch "$1/.ro-probe" 2>/dev/null; then echo "REHEARSAL FAIL H4: the live vault is still writable" >&2; exit 1; fi
      shift
      exec "$@"' _ "$SOURCE" "$SELF" --inside "${ARGV[@]}"
  fi
  echo "H4 SKIP unshare is unavailable; falling back to recorded state" >&2
  ISO=0
fi
if [ "$ISO" = 1 ]; then
  step H4 "live vault bound read-only (touch probe refused), id=$(id -u) inside the namespace"
else
  SRC_HEAD_BEFORE=$(head_of "$SOURCE")
  SRC_DIRT_BEFORE=$(git -C "$SOURCE" --no-optional-locks status --porcelain=v1 -uall | sha256sum)
  touch "$R/marker"
  step H4 "FALLBACK: recorded source HEAD and dirt hash; checked again at the end"
fi

mkdir -p "$R/out"
command -v python3 >/dev/null || fail H0 "python3 is required"
"$VP" version >/dev/null || fail H0 "no usable vp binary (VP=$VP)"
VPPATH=$(command -v "$VP")
sha256sum "$VPPATH" | cut -d' ' -f1 > "$R/out/vp.sha256"
step H0 "vp $("$VP" version | head -1), sha256 $(cat "$R/out/vp.sha256")"

# --------------------------------------------------------------- H1 copies
COPIES="vault seq bfb bft drill drill2a drill2b"
for c in $COPIES; do
  [ -e "$(vault_of "$c")" ] || cp -a "$SOURCE" "$(vault_of "$c")"
done
step H1 "copies: $COPIES"

# --------------------------------------------------------------- H2 remotes
for c in $COPIES; do
  v=$(vault_of "$c")
  for r in $(git -C "$v" remote); do git -C "$v" remote remove "$r"; done
  git -C "$v" config --unset-all branch.main.remote 2>/dev/null || true
  git -C "$v" config --unset-all branch.main.merge 2>/dev/null || true
  printf '#!/bin/sh\necho "rehearsal copy: push refused" >&2\nexit 1\n' > "$v/.git/hooks/pre-push"
  chmod +x "$v/.git/hooks/pre-push"
  [ -z "$(git -C "$v" remote)" ] || fail H2 "$c still has a remote"
  [ -z "$(git -C "$v" config --get-regexp '^(remote|branch)\.' || true)" ] || fail H2 "$c still has remote config"
  mkdir -p "$(xdg_of "$c")/vibe-palace"
  printf 'vault_path = "%s"\n' "$v" > "$(xdg_of "$c")/vibe-palace/config.toml"
  if vpc "$c" vault push --vault "$v" >/dev/null 2>&1; then fail H2 "$c: vp vault push exited 0"; fi
  if git -C "$v" push origin main >/dev/null 2>&1; then fail H2 "$c: git push exited 0"; fi
done
step H2 "remotes stripped, pre-push hook installed, pushes refused on every copy"

# --------------------------------------------------------------- H3 config
[ ! -e /tmp/.vibe-palace.toml ] || fail H3 "/tmp/.vibe-palace.toml exists"
[ ! -e /.vibe-palace.toml ] || fail H3 "/.vibe-palace.toml exists"
for c in $COPIES; do
  v=$(vault_of "$c")
  if [ "$FINAL" = 1 ]; then
    clean_p "$v" || fail H3 "R-final: copy $c is dirty (redo P2b)"
  elif ! clean_p "$v"; then
    git -C "$v" add -A
    git -C "$v" -c user.email=rehearsal@localhost -c user.name=rehearsal commit -q --allow-empty -m 'R-dev: freeze copy dirt'
  fi
  # A command with no --vault must resolve to this copy, never to live.
  vpc "$c" status 2>/dev/null | grep -qF "$v" || fail H3 "$c: vp status did not name the copy"
done
step H3 "each copy has its own XDG config; no stray /tmp or / project toml"

# --------------------------------------------------------------- H5 clones
git clone -q "$REPO" "$R/qms-clone"
git clone -q "$REPO" "$R/qng-clone"
for cl in qms-clone qng-clone; do
  git -C "$R/$cl" remote remove origin
done
printf '[project]\nname = "%s"\n' "$TO" > "$R/qms-clone/.vibe-palace.toml"
printf '[project]\nname = "%s"\n' "$FROM" > "$R/qng-clone/.vibe-palace.toml"
step H5 "clones qms-clone ($TO) and qng-clone ($FROM), remotes removed"

# --------------------------------------------------------------- H6 baselines
V=$(vault_of vault)
X=$(xdg_of vault)
vpc vault search -p "$FROM" -n 1 warm >/dev/null 2>&1 || true
vpc vault search -p "$TO" -n 1 warm >/dev/null 2>&1 || true
XDG_CONFIG_HOME=$X "$here/census.sh" --cache-warm-check --vault "$V" --snapshot "$R/cache-pre.list" || fail H6 "cache is cold"
XDG_CONFIG_HOME=$X "$here/census.sh" --profile rehearsal --phase B --vault "$V" --out "$R/census-B-full.json" >/dev/null || fail H6 "census B (rehearsal profile)"
XDG_CONFIG_HOME=$X "$here/census.sh" --profile live --phase B --vault "$V" --out "$R/out/census-B.json" >/dev/null || fail H6 "census B (live profile)"
XDG_CONFIG_HOME=$X "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$V" --json > "$R/out/counts.json" 2>/dev/null || fail H6 "report"
XDG_CONFIG_HOME=$X "$here/parity.sh" --baseline --vault "$V" --counts "$R/out/counts.json" --out "$R/out/b1.json" || fail H6 "parity baseline"
clean_p "$V" || fail H6 "the baselines dirtied the copy"
step H6 "baselines recorded; COPY still clean"

# --------------------------------------------------------------- H7 apply
COPY_HEAD=$(head_of "$V")
XDG_CONFIG_HOME=$X "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$V" --apply \
  --expect "$R/out/counts.json" --expect-head "$COPY_HEAD" --journal "$R/journal.tsv" > "$R/apply.log" 2>&1 || {
  cat "$R/apply.log" >&2; fail H7 "apply"; }
XDG_CONFIG_HOME=$X "$here/census.sh" --cache-report "$R/apply.log" --pre "$R/cache-pre.list" --stray "$R/journal.d/stray.list" --vault "$V" || fail H7 "cache report"
XDG_CONFIG_HOME=$X "$here/census.sh" --profile rehearsal --phase A --vault "$V" --b "$R/census-B-full.json" --counts "$R/out/counts.json" --out "$R/census-A.json" || fail H7 "census A"
XDG_CONFIG_HOME=$X "$here/parity.sh" --vault "$V" --b1 "$R/out/b1.json" --runs 1 --out "$R/parity-A.json" || fail H7 "parity"
step H7 "applied, census A and parity pass"

# --------------------------------------------------------------- H8 idempotency
H8_HEAD=$(head_of "$V")
H8_TREE=$(tree_hash "$V")
out=$(XDG_CONFIG_HOME=$X "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$V" --apply \
  --expect "$R/out/counts.json" --expect-head "$COPY_HEAD" --journal "$R/journal-h8.tsv" 2>/dev/null || true)
case "$out" in *"already applied"*) : ;; *) fail H8 "re-apply said: $out" ;; esac
out=$(XDG_CONFIG_HOME=$X "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$V" --phase cache --apply \
  --journal "$R/journal-h8c.tsv" 2>/dev/null || true)
case "$out" in *"nothing to do (marker present)"*) : ;; *) fail H8 "re-run of the cache phase said: $out" ;; esac
[ "$(head_of "$V")" = "$H8_HEAD" ] || fail H8 "HEAD moved"
[ "$(tree_hash "$V")" = "$H8_TREE" ] || fail H8 "the tree changed"
step H8 "re-apply and re-run of the cache phase are no-ops"

# --------------------------------------------------------------- H9 rollback drills
# The drill runs the plan's Rollback A block VERBATIM: it is lifted from the
# task file, never retyped here.
python3 - "$PLAN" > "$R/rollback-a.sh" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
i = text.index("**Rollback A: before the vault push.**")
m = re.search(r"```\n(.*?)```", text[i:], re.S)
if not m:
    sys.exit("no fenced block after the Rollback A heading in %s" % sys.argv[1])
sys.stdout.write(m.group(1))
PY
grep -q 'ROLLBACK A DONE' "$R/rollback-a.sh" || fail H9 "the lifted Rollback A block looks wrong"

drill_apply() {  # $1 copy name -> applies, leaving $R/<c>-w/journal.tsv
  local c=$1 v x w
  v=$(vault_of "$c"); x=$(xdg_of "$c"); w="$R/$c-w"
  mkdir -p "$w/xdg/vibe-palace"
  cp "$x/vibe-palace/config.toml" "$w/xdg/vibe-palace/config.toml"
  XDG_CONFIG_HOME=$x "$VP" search -p "$FROM" -n 1 warm >/dev/null 2>&1 || true
  XDG_CONFIG_HOME=$x "$VP" search -p "$TO" -n 1 warm >/dev/null 2>&1 || true
  python3 "$here/slugcheck.py" snapshot --vault "$v" --out "$w/cache-pre.list" "$FROM" "$TO"
  XDG_CONFIG_HOME=$x "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$v" --json > "$w/counts.json" 2>/dev/null
  {
    printf 'V=%s\nW=%s\nNEW=%s\nOLD=%s\n' "$v" "$w" "$R/qms-clone" "$R/qng-clone"
    printf 'ROLLBACK=%s\n' "$(head_of "$v")"
  } > "$w/state.env"
}

drill_rollback() {  # $1 copy name
  local w="$R/$1-w"
  ( set +e; STATE="$w/state.env" DRILL=1 PATH="$(dirname "$VPPATH"):$PATH" bash "$R/rollback-a.sh" ) > "$w/rollback.log" 2>&1
  grep -q 'ROLLBACK A DONE' "$w/rollback.log" || { cat "$w/rollback.log" >&2; return 1; }
}

drill_assert() {  # $1 copy, $2 expected HEAD, $3 max not-done
  local c=$1 want=$2 maxnd=$3 v w
  v=$(vault_of "$c"); w="$R/$c-w"
  [ "$(head_of "$v")" = "$want" ] || fail H9 "$c: HEAD is $(head_of "$v"), want $want"
  clean_p "$v" || fail H9 "$c: tree is dirty after the rollback"
  local line nd cf
  line=$(grep -oE 'lines=[0-9]+ done=[0-9]+ previously-replayed=[0-9]+ not-done=[0-9]+ conflict=[0-9]+' "$w/rollback.log" | tail -1)
  [ -n "$line" ] || fail H9 "$c: no replay result line"
  nd=$(echo "$line" | sed -n 's/.*not-done=\([0-9]*\).*/\1/p')
  cf=$(echo "$line" | sed -n 's/.*conflict=\([0-9]*\).*/\1/p')
  [ "$cf" = 0 ] || fail H9 "$c: replay reported conflict=$cf"
  [ "$nd" -le "$maxnd" ] || fail H9 "$c: replay reported not-done=$nd (max $maxnd)"
  python3 - "$v" "$w/cache-pre.list" "$c" <<'PY' || fail H9 "$c: restored state"
import os, sys, hashlib
v, pre, name = sys.argv[1], sys.argv[2], sys.argv[3]
bad = []
for line in open(pre):
    slug, n, size, sha = line.rstrip("\n").split("\t")
    p = os.path.join(v, "palace", ".local", "embed-cache", slug, n)
    if not os.path.exists(p):
        bad.append("missing %s/%s" % (slug, n))
    elif hashlib.sha256(open(p, "rb").read()).hexdigest() != sha:
        bad.append("changed %s/%s" % (slug, n))
for marker in (".project-slug-cache-done", ".project-slug-stray.list"):
    for slug in os.listdir(os.path.join(v, "palace", ".local", "embed-cache")):
        if os.path.exists(os.path.join(v, "palace", ".local", "embed-cache", slug, marker)):
            bad.append("leftover %s under %s" % (marker, slug))
if bad:
    sys.exit("%s: %s" % (name, "; ".join(bad[:6])))
PY
  [ -d "$w/attempt-1" ] || fail H9 "$c: the journal was not archived to attempt-1/"
}

drill_apply drill
DRILL_HEAD=$(head_of "$(vault_of drill)")
XDG_CONFIG_HOME=$(xdg_of drill) "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$(vault_of drill)" --apply \
  --expect "$R/drill-w/counts.json" --expect-head "$DRILL_HEAD" --journal "$R/drill-w/journal.tsv" > "$R/drill-w/apply.log" 2>&1 || fail H9a "apply on DRILL"
drill_rollback drill || fail H9a "the Rollback A block failed"
drill_assert drill "$DRILL_HEAD" 0
drill_rollback drill || fail H9a "the second Rollback A run failed"
grep -q 'done=0' "$R/drill-w/rollback.log" || fail H9a "the re-run replayed something (expected done=0)"
step H9a "clean rollback on DRILL, and a re-run is a no-op"

crash_drill() {  # $1 copy, $2 journal lines to wait for
  local c=$1 n=$2 v x w p
  drill_apply "$c"
  v=$(vault_of "$c"); x=$(xdg_of "$c"); w="$R/$c-w"
  DRILL2_HEAD=$(head_of "$v")
  XDG_CONFIG_HOME=$x "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$v" --apply \
    --expect "$w/counts.json" --expect-head "$DRILL2_HEAD" --journal "$w/journal.tsv" > "$w/apply.log" 2>&1 &
  p=$!
  jlines() { if [ -e "$w/journal.tsv" ]; then wc -l < "$w/journal.tsv"; else echo 0; fi; }
  while kill -0 "$p" 2>/dev/null && [ "$(jlines)" -lt "$n" ]; do sleep 0.02; done
  if kill -0 "$p" 2>/dev/null; then kill -9 "$p" 2>/dev/null || true; fi
  wait "$p" 2>/dev/null || true
  # Recorded now: the rollback block archives apply.log into attempt-<n>/.
  KILLED_AT="killed at journal=$(jlines) commits=$(git -C "$v" rev-list --count "$DRILL2_HEAD"..HEAD)"
  echo "$KILLED_AT" >> "$w/apply.log"
  while pgrep -f "git.*$v" >/dev/null 2>&1; do sleep 0.1; done
  if [ -e "$v/.git/index.lock" ]; then rm -f "$v/.git/index.lock"; echo "stale index.lock removed" >> "$w/apply.log"; fi
  drill_rollback "$c" || fail H9b "$c: the Rollback A block failed"
  drill_assert "$c" "$DRILL2_HEAD" 1
  step H9b "$c: $KILLED_AT, rolled back clean"
}
crash_drill drill2a "$N_A"
crash_drill drill2b "$N_B"

# BFA is COPY as it stands after H8, before the capture probe writes to it.
cp -a "$V" "$(vault_of bfa)"
mkdir -p "$(xdg_of bfa)/vibe-palace"
printf 'vault_path = "%s"\n' "$(vault_of bfa)" > "$(xdg_of bfa)/vibe-palace/config.toml"

# --------------------------------------------------------------- H10 capture
mkdir -p "$R/transcripts"
SID="2026-09-22-cccc0000-0000-0000-0000-00000000000a"
printf '%s\n' \
  "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"rehearsal capture probe\"},\"uuid\":\"u1\",\"sessionId\":\"$SID\"}" \
  "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Captured during the rehearsal.\"}]},\"uuid\":\"a1\",\"parentUuid\":\"u1\",\"sessionId\":\"$SID\"}" \
  > "$R/transcripts/h10.jsonl"
cap() {  # $1 copy, $2 clone, $3 transcript, $4 session id -> prints the note id
  XDG_CONFIG_HOME=$(xdg_of "$1") "$VP" hook <<EOF | python3 -c 'import json,sys; print(json.load(sys.stdin).get("session_note_id",""))'
{"session_id":"$4","transcript_path":"$3","cwd":"$R/$2","hook_event_name":"SessionEnd"}
EOF
}
NOTE=$(cap vault qms-clone "$R/transcripts/h10.jsonl" "$SID")
[ -n "$NOTE" ] || fail H10 "the capture returned no session_note_id"
[ -f "$V/Projects/$TO/sessions/$NOTE.md" ] || fail H10 "no note at Projects/$TO/sessions/$NOTE.md"
[ ! -d "$V/Projects/$FROM" ] || fail H10 "Projects/$FROM came back"
XDG_CONFIG_HOME=$X python3 "$here/slugcheck.py" mcp-call vp_list_projects '{}' --vault "$V" --cwd "$R/qms-clone" > "$R/projects.json" 2>/dev/null || fail H10 "vp_list_projects"
grep -q "\"$TO\"" "$R/projects.json" || fail H10 "$TO missing from vp_list_projects"
if grep -q "\"$FROM\"" "$R/projects.json"; then fail H10 "$FROM is still listed"; fi
step H10 "capture landed under $TO as $NOTE; C5 holds"

# --------------------------------------------------------------- H11 C12
DEC_B="palace/$FROM/drawers/$FROM/decisions/drawers.jsonl"
DEC_A="palace/$TO/drawers/$TO/decisions/drawers.jsonl"
cp "$(vault_of bfb)/$DEC_B" "$R/bfb-before.jsonl"
cp "$(vault_of bft)/$DEC_A" "$R/bft-before.jsonl"
cp "$(vault_of bfa)/$DEC_A" "$R/bfa-before.jsonl"
XDG_CONFIG_HOME=$(xdg_of bfb) python3 "$here/slugcheck.py" mcp-call vp_palace_backfill_decisions "{\"project\":\"$FROM\",\"apply\":true,\"all\":true}" --vault "$(vault_of bfb)" >/dev/null || fail H11 "backfill on BFB"
XDG_CONFIG_HOME=$(xdg_of bft) python3 "$here/slugcheck.py" mcp-call vp_palace_backfill_decisions "{\"project\":\"$TO\",\"apply\":true,\"all\":true}" --vault "$(vault_of bft)" >/dev/null || fail H11 "backfill on BFT"
XDG_CONFIG_HOME=$(xdg_of bfa) python3 "$here/slugcheck.py" mcp-call vp_palace_backfill_decisions "{\"project\":\"$TO\",\"apply\":true,\"all\":true}" --vault "$(vault_of bfa)" >/dev/null || fail H11 "backfill on BFA"
python3 "$here/slugcheck.py" c12 --counts "$R/out/counts.json" --to "$TO" \
  --bfb-before "$R/bfb-before.jsonl" --bfb-after "$(vault_of bfb)/$DEC_B" \
  --bft-before "$R/bft-before.jsonl" --bft-after "$(vault_of bft)/$DEC_A" \
  --bfa-before "$R/bfa-before.jsonl" --bfa-after "$(vault_of bfa)/$DEC_A" || fail H11 "C12 set comparison"
step H11 "C12: after the migration the backfill appends exactly the source set plus the target set"

# --------------------------------------------------------------- H12 SEQ
SQ=$(vault_of seq)
SX=$(xdg_of seq)
printf '%s\n' "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"seq probe\"},\"uuid\":\"u1\",\"sessionId\":\"2026-09-22-dddd0000-0000-0000-0000-00000000000b\"}" > "$R/transcripts/h12a.jsonl"
printf '%s\n' "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"seq probe two\"},\"uuid\":\"u2\",\"sessionId\":\"2026-09-22-dddd0000-0000-0000-0000-00000000000c\"}" > "$R/transcripts/h12b.jsonl"
N1=$(cap seq qng-clone "$R/transcripts/h12a.jsonl" "2026-09-22-dddd0000-0000-0000-0000-00000000000b")
case "$N1" in *-01) : ;; *) fail H12 "the first capture minted $N1, want -01" ;; esac
git -C "$SQ" add -A
git -C "$SQ" -c user.email=rehearsal@localhost -c user.name=rehearsal commit -q -m 'H12: seq capture'
SEQ_HEAD=$(head_of "$SQ")
XDG_CONFIG_HOME=$SX "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$SQ" --json > "$R/seq-counts.json" 2>/dev/null
XDG_CONFIG_HOME=$SX "$VP" migrate project-slug --from "$FROM" --to "$TO" --vault "$SQ" --apply \
  --expect "$R/seq-counts.json" --expect-head "$SEQ_HEAD" --journal "$R/seq-journal.tsv" > "$R/seq-apply.log" 2>&1 || { cat "$R/seq-apply.log" >&2; fail H12 "apply on SEQ"; }
N2=$(cap seq qms-clone "$R/transcripts/h12b.jsonl" "2026-09-22-dddd0000-0000-0000-0000-00000000000c")
[ "${N1%-01}" = "${N2%-02}" ] || fail H12 "second capture is $N2; want ${N1%-01}-02 (same date and writer)"
case "$N2" in *-02) : ;; *) fail H12 "the second capture minted $N2, want -02" ;; esac
step H12 "session counter continues: $N1 then $N2"

# --------------------------------------------------------------- H4 fallback close
if [ "$ISO" = 0 ]; then
  [ "$(head_of "$SOURCE")" = "$SRC_HEAD_BEFORE" ] || fail H4 "the source vault's HEAD moved"
  [ "$(git -C "$SOURCE" --no-optional-locks status --porcelain=v1 -uall | sha256sum)" = "$SRC_DIRT_BEFORE" ] || fail H4 "the source vault's dirt changed"
  touched=$(find "$SOURCE" -newer "$R/marker" -type f -not -path '*/.git/*' -not -path '*/palace/.local/*' | head -5)
  [ -z "$touched" ] || fail H4 "the source vault was written: $touched"
  step H4 "FALLBACK closed: source HEAD, dirt and mtimes unchanged"
fi

echo "outputs: $R/out/{counts.json,b1.json,census-B.json,vp.sha256}"
echo "REHEARSAL PASS"
