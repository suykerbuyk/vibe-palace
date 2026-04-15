#!/usr/bin/env bash
#
# Walkthrough happy-path — a single annotated case that mirrors
# doc/TUTORIAL.md Part 2 ("Project Setup"). The transcript this emits is
# the documentation artifact; keep the banners readable and let real `vp`
# output narrate itself.
#
# Sourced by test/e2e/walkthrough/run.sh inside a subshell that already
# sourced lib.sh and set $CASE_DIR / $CASE_LOGDIR / $TMPROOT / $VP_BIN.

set -euo pipefail

export CASE_NAME="walkthrough"
fresh_home

# ---------------------------------------------------------------------------
# STEP 1 — create a project directory (mirrors TUTORIAL Part 2: "Initialize
# a Project"). Nothing vibe-palace-specific yet; this is just the user
# landing in a project they want to memorialize.
# ---------------------------------------------------------------------------
echo ""
echo "=== STEP 1: create project directory ==="
echo "(mirrors TUTORIAL Part 2 — Initialize a Project)"
mkdir -p "$CASE_DIR/proj-a"
cd "$CASE_DIR/proj-a"
echo "cwd: $PWD"

# ---------------------------------------------------------------------------
# STEP 2 — LOAD-BEARING `git init`. Not decorative.
#
# `vp init` only writes .vibe-palace.toml when project.DetectSignal(dir) is
# non-None. With no .git (and no recognized manifest like go.mod /
# package.json / Cargo.toml), step 3's per-project assertion would
# silently pass because the config would NEVER be written. Commit 92338b1
# hardened the positional-arg validator against exactly this class of
# silent regression; this step LOCKS that guarantee in at e2e scope.
# ---------------------------------------------------------------------------
echo ""
echo "=== STEP 2: git init (LOAD-BEARING) ==="
echo "git init writes .git/ which is the project signal vp init detects —"
echo "without it, vp init would NOT write .vibe-palace.toml (silent"
echo "regression risk, see commit 92338b1)."
git init -q
assert_dir_exists ".git"

# ---------------------------------------------------------------------------
# STEP 3 — `vp init` with defaults. Asserts all three configuration
# artifacts land where the tutorial promises: global config, default
# vault, and per-project stub.
# ---------------------------------------------------------------------------
echo ""
echo "=== STEP 3: vp init (default config + default vault) ==="
echo "(mirrors TUTORIAL Part 2 — Initialize a Project / Understanding the Vault)"
run_vp init
assert_exit_code 0 "$LAST_EXIT_CODE"
cat "$CASE_LOGDIR/stdout.log"

assert_file_exists "$HOME/.config/vibe-palace/config.toml"
assert_dir_exists  "$HOME/vibe-palace-vault"
assert_file_exists ".vibe-palace.toml"

# ---------------------------------------------------------------------------
# STEP 4 — hydrate the project space and inspect it.
#
# End-users normally trigger capture through their editor's MCP client
# (vp_capture_session); we use the rig's seed_drawer helper here to
# produce the same on-disk artifact without an MCP roundtrip. Then `vp
# status` and `vp inject --max-tokens 2000` demonstrate that the
# bootstrap JSON the AI sees contains a non-empty .project.
# ---------------------------------------------------------------------------
echo ""
echo "=== STEP 4: hydrate + inspect ==="
echo "(End-users normally trigger capture through their editor's MCP client"
echo " (vp_capture_session); we use the rig's seed_drawer helper here to"
echo " produce the same on-disk artifact without an MCP roundtrip.)"
seed_drawer proj-a facts "Walkthrough seeds a fact drawer for demo purposes."

echo "--- vp status ---"
run_vp status
assert_exit_code 0 "$LAST_EXIT_CODE"
cat "$CASE_LOGDIR/stdout.log"

echo "--- vp inject --max-tokens 2000 (truncated) ---"
run_vp inject --max-tokens 2000
assert_exit_code 0 "$LAST_EXIT_CODE"
# Show a readable slice of the bootstrap JSON rather than the whole blob.
head -c 400 "$CASE_LOGDIR/stdout.log"
echo ""
echo "… (bootstrap JSON truncated for transcript readability)"

# Shape check: bootstrap JSON must carry a non-empty .project. stdout.log
# from `vp inject` is pure JSON (verified empirically against 0.1.0-dev).
assert_shape "$CASE_LOGDIR/stdout.log" '.project | length > 0' 'true'

# ---------------------------------------------------------------------------
# STEP 5 — attach a SECOND project to the SAME vault. `vp init
# --vault-path` must be idempotent at the vault level: proj-b gets its
# own .vibe-palace.toml but the shared vault .git/HEAD must not move.
# ---------------------------------------------------------------------------
echo ""
echo "=== STEP 5: second project, same vault ==="
echo "(mirrors TUTORIAL Part 2 — Understanding the Vault: one vault, many projects)"
vault_git_before=$(cat "$HOME/vibe-palace-vault/.git/HEAD")

mkdir -p "$CASE_DIR/proj-b"
cd "$CASE_DIR/proj-b"
git init -q
run_vp init --vault-path "$HOME/vibe-palace-vault"
assert_exit_code 0 "$LAST_EXIT_CODE"
cat "$CASE_LOGDIR/stdout.log"
assert_file_exists ".vibe-palace.toml"

vault_git_after=$(cat "$HOME/vibe-palace-vault/.git/HEAD")
if [[ "$vault_git_before" != "$vault_git_after" ]]; then
  _e2e_fail "${BASH_SOURCE[0]}" "$LINENO" \
    "vault .git/HEAD changed — proj-b must not reinit the vault"
  exit 1
fi
echo "vault .git/HEAD unchanged ($vault_git_after) — proj-b attached, not reinitialized."

echo ""
echo "=== walkthrough: all steps passed ==="
