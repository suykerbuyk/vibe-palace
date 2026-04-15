# `--no-git` disables vault git-init. The vault dir still gets created,
# but without a .git subdir.

cd "$CASE_DIR"
mkdir -p proj
cd proj
git init -q

run_vp init --no-git --name no-git-case

assert_exit_code 0 "$LAST_EXIT_CODE"
assert_dir_exists "$HOME/vibe-palace-vault"
assert_dir_absent "$HOME/vibe-palace-vault/.git"
