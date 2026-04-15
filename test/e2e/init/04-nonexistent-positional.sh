# Regression lock for Fix 1: `vp init <path-that-does-not-exist>` must
# abort with ExitUser BEFORE any filesystem writes, with a stderr hint
# pointing at --vault-path.
#
# This case ran first in the orchestrator discovery order so a Phase-1
# regression is caught immediately.

cd "$CASE_DIR"

# Construct a path guaranteed not to exist under the sandboxed $HOME.
bogus="$CASE_DIR/does-not-exist/vault-ish"

run_vp init "$bogus"

assert_exit_code 1 "$LAST_EXIT_CODE"   # cli.ExitUser == 1
assert_grep 'does not exist'  "$CASE_LOGDIR/stderr.log"
assert_grep 'vault-path'      "$CASE_LOGDIR/stderr.log"

# Protective assertions: NO filesystem writes happened. If the existence
# check ever slides back into initProject, initGlobal will have already
# created these and these assertions will fail.
assert_file_absent "$XDG_CONFIG_HOME/vibe-palace/config.toml"
assert_dir_absent  "$HOME/vibe-palace-vault"
