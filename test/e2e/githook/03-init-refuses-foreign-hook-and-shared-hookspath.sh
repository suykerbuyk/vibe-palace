# The two refusals, through the real binary.
#
# 1. A pre-existing post-commit hook this project did not write is never
#    clobbered and never appended to.
# 2. A repo with core.hooksPath set is never written to at all — installing a
#    vibe-palace hook into a directory shared by every repo the operator owns is
#    worse than a missing hook, and `git rev-parse --git-path hooks` would hand
#    that shared directory over without complaint if it were asked first.
#
# Both refusals must leave `vp init` at exit 0: a hook that could not be
# installed is a row, never an abort.

cd "$CASE_DIR"
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null

# --- 1. foreign hook ---
mkdir -p foreign
cd foreign
git init -q
mkdir -p .git/hooks
printf '#!/bin/sh\n# somebody else wrote this\necho hi\n' > .git/hooks/post-commit
chmod +x .git/hooks/post-commit
FOREIGN_SHA="$(sha256sum .git/hooks/post-commit | cut -d' ' -f1)"

run_vp init --name githook-foreign-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_not_grep "vibe-palace:post-commit-reap" "$CASE_DIR/foreign/.git/hooks/post-commit"

AFTER_SHA="$(sha256sum "$CASE_DIR/foreign/.git/hooks/post-commit" | cut -d' ' -f1)"
if [[ "$FOREIGN_SHA" != "$AFTER_SHA" ]]; then
  echo "FAIL: a foreign post-commit hook was modified" >&2
  exit 1
fi

# --- 2. shared core.hooksPath ---
cd "$CASE_DIR"
mkdir -p shared-hooks
mkdir -p shared
cd shared
git init -q
git config core.hooksPath "$CASE_DIR/shared-hooks"

export CASE_LOGDIR="$CASE_DIR/logs-shared"
mkdir -p "$CASE_LOGDIR"
run_vp init --name githook-shared-case
assert_exit_code 0 "$LAST_EXIT_CODE"

assert_file_absent "$CASE_DIR/shared-hooks/post-commit"
assert_file_absent "$CASE_DIR/shared/.git/hooks/post-commit"
assert_grep "core.hooksPath" "$CASE_LOGDIR/stdout.log"
