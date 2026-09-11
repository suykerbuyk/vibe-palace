# The same-binary-twice property: running `vp init` a second time over an
# already-initialized project must converge — exit 0, no [FAIL] row, and the
# project config file byte-identical to what the first run left.
#
# This case used to assert `assert_grep "already"` and lock cmd_init.go's
# marker gate (the early return that fired the moment .vibe-palace.toml
# existed). That gate is gone: it made `vp init` a permanent no-op over any
# project whose marker was written by something other than `vp init`. The
# global-config gate still prints "already", so the old assertion would now
# pass by luck rather than by property — hence the explicit convergence
# assertions below.

cd "$CASE_DIR"
mkdir -p proj
cd proj
git init -q

run_vp init --name reinit-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/proj/.vibe-palace.toml"
assert_not_grep "\[FAIL\]" "$CASE_LOGDIR/stdout.log"

# The vault-side artifacts a re-init must NOT lose.
assert_file_exists "$(vault_project_config_path reinit-case)"
assert_file_exists "$HOME/vibe-palace-vault/Projects/reinit-case/commands/README.md"
assert_file_exists "$HOME/vibe-palace-vault/Projects/reinit-case/skills/README.md"

cp "$CASE_DIR/proj/.vibe-palace.toml" "$CASE_DIR/marker-after-run1.toml"

# Rotate log dir so the second run has its own files.
export CASE_LOGDIR="$CASE_DIR/logs-run2"
mkdir -p "$CASE_LOGDIR"

run_vp init --name reinit-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_not_grep "\[FAIL\]" "$CASE_LOGDIR/stdout.log"

if ! cmp -s "$CASE_DIR/marker-after-run1.toml" "$CASE_DIR/proj/.vibe-palace.toml"; then
  echo "FAIL: ${BASH_SOURCE[0]}: second vp init rewrote .vibe-palace.toml" >&2
  diff -u "$CASE_DIR/marker-after-run1.toml" "$CASE_DIR/proj/.vibe-palace.toml" >&2 || true
  exit 1
fi

# The vault artifacts survive the second run too.
assert_file_exists "$(vault_project_config_path reinit-case)"
assert_file_exists "$HOME/vibe-palace-vault/Projects/reinit-case/commands/README.md"
assert_file_exists "$HOME/vibe-palace-vault/Projects/reinit-case/skills/README.md"

# The re-init now says out loud what it deliberately does NOT do, and names
# the owners: `vp commands upgrade` for stale shims, and the explicit reset
# verbs for a vault Templates/ override (no upgrade resets one).
assert_grep "vp commands upgrade" "$CASE_LOGDIR/stdout.log"
assert_grep "vp commands reset" "$CASE_LOGDIR/stdout.log"
assert_grep "vp skills reset" "$CASE_LOGDIR/stdout.log"
