# Unknown subcommand (`vp config bogus`) prints a usage error to
# stderr and exits 1 (ExitUser). Regression guard: the pre-plan
# behavior was ExitOK for arbitrary typos.

run_vp config bogus

assert_exit_code 1 "$LAST_EXIT_CODE"

# Error message goes to stderr, not stdout.
if [[ -s "$CASE_LOGDIR/stdout.log" ]]; then
  echo "FAIL: stdout should be empty for error path, got:" >&2
  cat "$CASE_LOGDIR/stdout.log" >&2
  exit 1
fi

assert_grep 'unknown subcommand "bogus"' "$CASE_LOGDIR/stderr.log"
assert_grep "vp config" "$CASE_LOGDIR/stderr.log"
assert_grep "Commands:" "$CASE_LOGDIR/stderr.log"
