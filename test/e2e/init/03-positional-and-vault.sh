# Both a positional (project dir) AND --vault-path (vault) must land
# independently. This was the mental model the original user reporter
# was attempting; Fix 1 keeps the semantics, we just verify them end-to-end.

cd "$CASE_DIR"
mkdir -p myproj
cd myproj
git init -q
cd "$CASE_DIR"

custom_vault="$CASE_DIR/custom-vault"

run_vp init "$CASE_DIR/myproj" --vault-path "$custom_vault" --name both-args

assert_exit_code 0 "$LAST_EXIT_CODE"
assert_file_exists "$CASE_DIR/myproj/.vibe-palace.toml"
assert_dir_exists  "$custom_vault/.git"
assert_dir_absent  "$HOME/vibe-palace-vault"
