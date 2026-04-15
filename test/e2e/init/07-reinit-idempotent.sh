# Re-running `vp init` in an already-initialized environment must be a
# no-op: exit 0 and surface the "already exists / already configured"
# info rows. Locks cmd_init.go:104-111.

cd "$CASE_DIR"
mkdir -p proj
cd proj
git init -q

run_vp init --name reinit-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/proj/.vibe-palace.toml"

# Rotate log dir so the second run has its own files.
export CASE_LOGDIR="$CASE_DIR/logs-run2"
mkdir -p "$CASE_LOGDIR"

run_vp init --name reinit-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_grep "already" "$CASE_LOGDIR/stdout.log"
