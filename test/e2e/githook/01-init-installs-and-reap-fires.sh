# THE DoD, end to end through the real binary: `vp init` installs the
# post-commit hook, and a real `git commit -F commit.msg` typed WITHOUT the
# trailing `&& rm commit.msg` leaves NO commit.msg on disk.
#
# The message is multi-paragraph with trailing whitespace and a double blank
# line — the shape `git commit -F` rewrites under --cleanup=whitespace. A hook
# comparing raw bytes would silently no-op on it, so this case also proves the
# positive path fires on this project's real message shape.

cd "$CASE_DIR"
mkdir -p proj
cd proj

export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
git init -q
git config user.name githook-e2e
git config user.email githook@test.invalid

echo seed > f.txt
git add f.txt
git commit -q -m seed

run_vp init --name githook-case
assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/proj/.git/hooks/post-commit"
assert_grep "vibe-palace:post-commit-reap" "$CASE_DIR/proj/.git/hooks/post-commit"

if [[ ! -x "$CASE_DIR/proj/.git/hooks/post-commit" ]]; then
  echo "FAIL: hook is not executable — git silently ignores a non-executable hook" >&2
  exit 1
fi

printf 'feat(e2e): a subject line\n\na body line with trailing spaces   \nanother body line\n\n\na paragraph after two blank lines\n' > commit.msg
echo changed > f.txt
git add f.txt

# No `&& rm` — that omission is the whole hole this hook closes.
git commit -q -F commit.msg

assert_file_absent "$CASE_DIR/proj/commit.msg"
