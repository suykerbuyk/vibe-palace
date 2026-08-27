# An existing clone never re-runs `vp init`, so a missing hook has to be
# REPORTED by a path the operator already runs. `vp check` is that path.
#
# Deleting the hook simulates the clone that predates the feature. `vp check`
# must name it; re-running `vp init` must repair it; and the repaired state must
# report clean.

cd "$CASE_DIR"
mkdir -p proj
cd proj

export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
git init -q
git config user.name githook-e2e
git config user.email githook@test.invalid

run_vp init --name githook-check-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/proj/.git/hooks/post-commit"

# The pre-hook clone.
rm -f "$CASE_DIR/proj/.git/hooks/post-commit"

export CASE_LOGDIR="$CASE_DIR/logs-check-missing"
mkdir -p "$CASE_LOGDIR"
run_vp check
assert_grep "Git commit.msg hook" "$CASE_LOGDIR/stdout.log"
assert_grep "no post-commit hook" "$CASE_LOGDIR/stdout.log"

# Repair through the path the row names.
export CASE_LOGDIR="$CASE_DIR/logs-reinit"
mkdir -p "$CASE_LOGDIR"
run_vp init --name githook-check-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/proj/.git/hooks/post-commit"

export CASE_LOGDIR="$CASE_DIR/logs-check-clean"
mkdir -p "$CASE_LOGDIR"
run_vp check
assert_grep "commit.msg reaper installed" "$CASE_LOGDIR/stdout.log"
assert_not_grep "no post-commit hook" "$CASE_LOGDIR/stdout.log"
