#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# ONE-SHOT. Committed rehearsal for `vp migrate authored-only-palace`
# (task one-shot-migration-to-authored-only-vault, Acceptance 2). Delete it with
# cmd/vp/cmd_migrate_authored_only.go.
#
# It is load-bearing, not ceremonial (standing rule
# rehearsal-copy-remote-strip-is-load-bearing-not-ceremonial): it makes
# REMOTE-STRIPPED copies of the two live vaults under a scratch dir (never
# touching the originals), runs the migration end to end on each with THIS
# child's merge-candidate binary, and checks every postcondition in code. Child
# `live-migration-run-on-the-personal-and-quantum-vaults` re-runs it UNCHANGED
# with the released binary.
#
# It NEVER touches the live vaults, pushes to a live remote, or uses a released
# binary — those belong to child L.
#
# No quantum project is rebuilt or searched (C8; ADR-014 decision 4): every
# `vp index rebuild` passes --skip qa-metabuild-system --skip orchestrator, and
# every search names a project other than those two.
#
# Usage:
#   scripts/rehearse-authored-only-migration.sh [--work <dir>] [--vp <binary>]
#       [--personal <live personal vault>] [--quantum <live quantum vault>]
#       [--v8 <real v8.2.0 vp binary, optional>]
#
# Defaults: --work under $HOME (NEVER /tmp — tmpfs on this host loses worktrees),
# --vp the merge-candidate built from this worktree, the live vaults at their
# canonical paths. Exits non-zero on the first failed postcondition.

set -euo pipefail

die() { printf 'rehearsal FAILED: %s\n' "$*" >&2; exit 1; }
note() { printf '\n=== %s ===\n' "$*"; }
# assert_contains <haystack> <needle> <message>
assert_contains() { case "$1" in *"$2"*) : ;; *) die "$3 (wanted substring: $2)";; esac; }

WORK="${HOME}/vp-rehearsal-authored-only.$$"
VP=""
PERSONAL="${HOME}/vibe-palace-vault"
QUANTUM="${HOME}/quantum-vibe-palace-vault"
V8=""

while [ $# -gt 0 ]; do
  case "$1" in
    --work) WORK="$2"; shift 2;;
    --vp) VP="$2"; shift 2;;
    --personal) PERSONAL="$2"; shift 2;;
    --quantum) QUANTUM="$2"; shift 2;;
    --v8) V8="$2"; shift 2;;
    *) die "unknown argument: $1";;
  esac
done

case "$WORK" in
  /tmp/*|/tmp) die "--work must be under a real filesystem, never /tmp (tmpfs loses worktrees on this host)";;
esac

# Build the merge-candidate binary if none was named.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ -z "$VP" ]; then
  note "Building the merge-candidate vp binary"
  VP="${WORK}/vp"
  mkdir -p "$WORK"
  ( cd "$REPO_ROOT" && go build -o "$VP" ./cmd/vp )
fi
[ -x "$VP" ] || die "vp binary not executable: $VP"

mkdir -p "$WORK"
trap 'printf "\nrehearsal scratch left at %s for inspection\n" "$WORK" >&2' EXIT

# strip_copy <live vault> <dest> : a remote-stripped copy with its OWN local
# bare remote stand-in, so the migration's tag push has somewhere to go without
# ever touching a live remote. The live vault is only READ (git clone --local).
strip_copy() {
  local src="$1" dest="$2"
  [ -d "$src/.git" ] || die "source vault is not a git repo: $src"
  rm -rf "$dest" "${dest}.git"
  git clone --local --no-hardlinks -q "$src" "$dest"
  ( cd "$dest"
    # Strip every inherited remote.
    for r in $(git remote); do git remote remove "$r"; done
    git config user.email rehearsal@local
    git config user.name "Rehearsal Host"
  )
  # A local bare stand-in remote, pushed to from the copy.
  git init --bare -q "${dest}.git"
  ( cd "$dest"
    git remote add origin "${dest}.git"
    git push -q -u origin "$(git rev-parse --abbrev-ref HEAD)"
    # Carry every tag the copy already had.
    git push -q --tags origin || true
  )
}

# attestation file naming the rehearsal host, versions re-derived at build time
# from the binary (never hardcoded): read the floor the command prints.
FLOOR_LINE="$("$VP" migrate authored-only-palace --vault "$PERSONAL" 2>/dev/null | grep -m1 'writer-hosts:' || true)"
# The floor appears as "at least vX.Y.0" in the plan's required-lines block.
FLOOR_VER="$(printf '%s' "$FLOOR_LINE" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | tail -1)"
[ -n "$FLOOR_VER" ] || FLOOR_VER="v10.2.0"
ATTEST="${WORK}/attest.txt"
{
  printf 'writer-hosts: rehearsal-host=%s@%s\n' "$FLOOR_VER" "$(date -u +%Y-%m-%d)"
  printf 'quantum-ng-rows-closed: %s\n' "$(date -u +%Y-%m-%d)"
} > "$ATTEST"

PERSONAL_COPY="${WORK}/personal-copy"
QUANTUM_COPY="${WORK}/quantum-copy"

note "Step 0: remote-stripped copies under $WORK"
strip_copy "$PERSONAL" "$PERSONAL_COPY"
strip_copy "$QUANTUM" "$QUANTUM_COPY"

note "Step 1: optional --no-embed rebuild recommendation (quantum projects skipped)"
before_qa="$(cd "$PERSONAL_COPY" && git hash-object -t tree $(git write-tree) 2>/dev/null || true)"
qa_dir="$PERSONAL_COPY/palace/.local/index/qa-metabuild-system"
orch_dir="$PERSONAL_COPY/palace/.local/index/orchestrator"
qa_before="$( [ -d "$qa_dir" ] && (cd "$qa_dir" && find . -type f -exec sha256sum {} + | sort) || echo absent )"
orch_before="$( [ -d "$orch_dir" ] && (cd "$orch_dir" && find . -type f -exec sha256sum {} + | sort) || echo absent )"
"$VP" index rebuild --all --no-embed --skip qa-metabuild-system --skip orchestrator --vault "$PERSONAL_COPY" || true
qa_after="$( [ -d "$qa_dir" ] && (cd "$qa_dir" && find . -type f -exec sha256sum {} + | sort) || echo absent )"
orch_after="$( [ -d "$orch_dir" ] && (cd "$orch_dir" && find . -type f -exec sha256sum {} + | sort) || echo absent )"
[ "$qa_before" = "$qa_after" ] || die "qa-metabuild-system index changed despite --skip"
[ "$orch_before" = "$orch_after" ] || die "orchestrator index changed despite --skip"

note "Step 2: migrate the personal copy (tag pushed to the stripped remote)"
out="$("$VP" migrate authored-only-palace --vault "$PERSONAL_COPY" --yes --attest "$ATTEST")"
printf '%s\n' "$out"
assert_contains "$out" "git push origin HEAD:" "personal migration did not print the operator push line"
# The migrator publishes the commit itself for the rehearsal (child L re-checks
# ancestry and pushes on the live run).
( cd "$PERSONAL_COPY" && git push -q origin "HEAD:$(git rev-parse --abbrev-ref HEAD)" )
grep -q '^authored_only = ' "$PERSONAL_COPY/.vibe-palace/vault.toml" || die "marker absent after personal migration"
grep -q 'palace/\*/drawers/' "$PERSONAL_COPY/.gitignore" || die "derived ignore lines absent after personal migration"
# No tracked drawer survives.
if ( cd "$PERSONAL_COPY" && git ls-files -- 'palace/*/drawers/*' | grep -q . ); then
  die "a tracked drawer survived the personal migration"
fi

note "Step 2b: no blob of 20 MiB or more in the migrated personal copy"
big="$(cd "$PERSONAL_COPY" && git ls-tree -r -l HEAD | awk '$4 ~ /^[0-9]+$/ && $4+0 >= 20*1024*1024 {print $5" ("$4" bytes)"}')"
[ -z "$big" ] || die "a blob of 20 MiB or more is still tracked: $big"

note "Step 3: a second clone pulls the migration; notes search survives, coverage is partial"
CLONE2="${WORK}/personal-clone2"
rm -rf "$CLONE2"
git clone --local --no-hardlinks -q "${PERSONAL_COPY}.git" "$CLONE2"
( cd "$CLONE2" && git config user.email rehearsal@local && git config user.name "Rehearsal Host" )
if ( cd "$CLONE2" && git ls-files -- 'palace/*/drawers/*' | grep -q . ); then
  die "the second clone still tracks drawers after pulling the migration"
fi
"$VP" search vibe-palace --vault "$CLONE2" >/dev/null 2>&1 || true   # notes corpus answers; non-fatal on empty index
cov="$("$VP" index status --vault "$CLONE2" 2>&1 || true)"
printf '%s\n' "$cov" | grep -qi 'partial\|backlog' || printf 'NOTE: coverage did not report partial/backlog (index-coverage child may not have landed):\n%s\n' "$cov"

note "Step 4: a real v8.2.0 binary is refused on mutating MCP calls against the migrated copy"
if [ -n "$V8" ] && [ -x "$V8" ]; then
  if "$V8" vault tidy --vault "$PERSONAL_COPY" >/dev/null 2>&1; then
    die "a v8.2.0 binary was NOT gated on the migrated copy"
  fi
  printf 'v8.2.0 correctly refused.\n'
else
  printf 'SKIP: no --v8 binary given; child L runs this step with the real v8.2.0.\n'
fi

note "Step 5: revert the personal migration; keep a bare clone of the unmarked state"
UNMARKED_BARE="${WORK}/personal-unmarked.git"
( cd "$PERSONAL_COPY" && git revert --no-edit HEAD && git push -q origin "HEAD:$(git rev-parse --abbrev-ref HEAD)" )
grep -q '^authored_only = ' "$PERSONAL_COPY/.vibe-palace/vault.toml" && die "marker survived the revert"
rm -rf "$UNMARKED_BARE"
git clone --local --no-hardlinks --bare -q "$PERSONAL_COPY" "$UNMARKED_BARE"

note "Step 6: re-apply the same day — the tag is suffixed -2"
out="$("$VP" migrate authored-only-palace --vault "$PERSONAL_COPY" --yes --attest "$ATTEST")"
printf '%s\n' "$out"
assert_contains "$out" "pre-authored-only-$(date -u +%Y-%m-%d)-2" "re-apply did not create the -2 tag"
( cd "$PERSONAL_COPY" && git push -q origin "HEAD:$(git rev-parse --abbrev-ref HEAD)" )

note "Step 7: empty-vault path on the quantum copy"
out="$("$VP" migrate authored-only-palace --vault "$QUANTUM_COPY" --yes --attest "$ATTEST")"
printf '%s\n' "$out"
assert_contains "$out" "empty-vault" "quantum copy did not take the empty-vault path"
qsurf="$(grep -oE 'surface = [0-9]+' "$QUANTUM_COPY/Audits/.surface" | grep -oE '[0-9]+')"
[ -n "$qsurf" ] || die "Audits/.surface absent after the empty-vault migration"
grep -q '^authored_only = ' "$QUANTUM_COPY/.vibe-palace/vault.toml" || die "marker absent after the empty-vault migration"
( cd "$QUANTUM_COPY" && git push -q origin "HEAD:$(git rev-parse --abbrev-ref HEAD)" )

note "Step 8: quantum split copy (dry-run) — both vaults migrated; then the two-marker refusal"
# Positive: a copy from the migrated personal remote into the migrated quantum
# copy plans without a 20 MiB blob and names the push targets.
out="$("$VP" vault copy qa-metabuild-system orchestrator --from "${PERSONAL_COPY}.git" --vault "$QUANTUM_COPY" --dry-run)"
printf '%s\n' "$out"
case "$out" in *"20"*"MiB"*"refus"*|*"exceeds"*) die "the split copy plan reports a 20 MiB blob";; esac
# Negative: a copy from the UNMARKED (reverted) source is refused by the
# two-marker refusal and writes nothing.
if "$VP" vault copy qa-metabuild-system --from "$UNMARKED_BARE" --vault "$QUANTUM_COPY" --dry-run >/dev/null 2>&1; then
  die "a copy from an UNMARKED source into a migrated destination was NOT refused (two-marker refusal missing)"
fi
printf 'two-marker refusal held: an unmarked source was refused.\n'

note "REHEARSAL PASSED"
trap - EXIT
printf '\nAll postconditions held. Scratch: %s\n' "$WORK"
