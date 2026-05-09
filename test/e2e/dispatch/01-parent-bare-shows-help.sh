# Bare parent (`vp config`) renders help on stdout and exits 0, instead
# of the old stubby "Run --help for details" on stderr.

run_vp config

assert_exit_code 0 "$LAST_EXIT_CODE"

# Help text lands on stdout.
assert_grep "Usage: vp config" "$CASE_LOGDIR/stdout.log"
assert_grep "Commands:" "$CASE_LOGDIR/stdout.log"
assert_grep "config sync" "$CASE_LOGDIR/stdout.log"

# Stderr stays empty for a bare parent invocation.
if [[ -s "$CASE_LOGDIR/stderr.log" ]]; then
  echo "FAIL: stderr should be empty for bare parent, got:" >&2
  cat "$CASE_LOGDIR/stderr.log" >&2
  exit 1
fi
