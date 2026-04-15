# `--vault-path` is honored: the vault lands at the requested path,
# NOT under $HOME.

cd "$CASE_DIR"
mkdir -p proj
cd proj
git init -q

custom_vault="$CASE_DIR/custom-vault"

run_vp init --vault-path "$custom_vault" --name explicit-vault

assert_exit_code 0 "$LAST_EXIT_CODE"
assert_dir_exists  "$custom_vault"
assert_dir_exists  "$custom_vault/.git"
assert_dir_absent  "$HOME/vibe-palace-vault"
assert_file_exists "$XDG_CONFIG_HOME/vibe-palace/config.toml"
assert_grep "$custom_vault" "$XDG_CONFIG_HOME/vibe-palace/config.toml"
