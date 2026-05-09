# Regression guard: a known two-word subcommand (`vp hook install
# --help`) still routes through the framework help path with exit 0,
# exercising the two-word lookup that runs BEFORE the parent gate.

run_vp hook install --help

assert_exit_code 0 "$LAST_EXIT_CODE"
assert_grep "hook install" "$CASE_LOGDIR/stdout.log"

if [[ -s "$CASE_LOGDIR/stderr.log" ]]; then
  echo "FAIL: stderr should be empty for --help, got:" >&2
  cat "$CASE_LOGDIR/stderr.log" >&2
  exit 1
fi
