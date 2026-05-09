# Regression guard that the exit-code contract holds across several
# parent commands — not just `vp config`. Every parent with
# Subcommands exits 0 on bare invocation (help emitted) and exits 1
# when given a non-flag unknown token.

for parent in vault commands migrate skills archive; do
  run_vp "$parent"
  assert_exit_code 0 "$LAST_EXIT_CODE"
  assert_grep "Commands:" "$CASE_LOGDIR/stdout.log"

  run_vp "$parent" a-token-that-will-never-be-a-subcommand
  assert_exit_code 1 "$LAST_EXIT_CODE"
  assert_grep "unknown subcommand" "$CASE_LOGDIR/stderr.log"
done
