# Happy path: `vp init` in a git-inited project directory (using the
# default vault path under $HOME) creates .vibe-palace.toml in the cwd
# and materializes the vault under $HOME.

cd "$CASE_DIR"
mkdir -p proj
cd proj
git init -q

run_vp init --name positional-project-dir

assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/proj/.vibe-palace.toml"
assert_dir_exists  "$HOME/vibe-palace-vault"
assert_dir_exists  "$HOME/vibe-palace-vault/.git"
assert_file_exists "$XDG_CONFIG_HOME/vibe-palace/config.toml"
assert_grep "vault_path" "$XDG_CONFIG_HOME/vibe-palace/config.toml"
