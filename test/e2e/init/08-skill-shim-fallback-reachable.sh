# The persona shims' MCP-less fallback, run exactly as an agent's shell would.
#
# Every persona shim (Cursor rule, Grok and Claude skill) ends with a fallback
# for a session where the vp_skill tool cannot be loaded: run
# `vp skills show <name>` from the project directory. Until it named that
# command, the fallback told the agent to read <vault>/Templates/skills/<name>/
# SKILL.md, a file override-only Templates/ never creates, and it wrote the
# operator's vault root into .cursor/rules/, which vp does not gitignore.
#
# This case proves the literal text: it pulls the command out of the rendered
# .mdc with sed, runs it with the harness binary first on PATH, and checks that
# it serves the built-in, then the project override once one exists.

cd "$CASE_DIR"
mkdir -p proj/.cursor/rules proj/.grok
cd proj
git init -q

run_vp init --name fallback-case
assert_exit_code 0 "$LAST_EXIT_CODE"
rule="$CASE_DIR/proj/.cursor/rules/vps-chair.mdc"
assert_file_exists "$rule"

# The plain command only: the closing backtick must follow the skill name, so
# the `vp skills show chair --section <ref>` span on another line never matches.
cmd="$(sed -n 's/.*`\(vp skills show [a-z0-9-]*\)`.*/\1/p' "$rule")"
if [[ "$cmd" != "vp skills show chair" ]]; then
  echo "FAIL: ${BASH_SOURCE[0]}: fallback command in vps-chair.mdc is '$cmd', want 'vp skills show chair'" >&2
  cat "$rule" >&2
  exit 1
fi
if [[ "$(command -v vp)" != "$VP_BIN" ]]; then
  echo "FAIL: ${BASH_SOURCE[0]}: 'vp' on PATH is '$(command -v vp)', not the harness binary $VP_BIN" >&2
  exit 1
fi

# $cmd is split into words on purpose: that is how a shell runs the text.
set +e
$cmd >"$CASE_LOGDIR/fallback.out" 2>"$CASE_LOGDIR/fallback.err"
rc=$?
set -e
assert_exit_code 0 "$rc"
assert_grep "^# skill: chair | source: embedded$" "$CASE_LOGDIR/fallback.out"

# With a project override in the vault, the same command run from the project
# directory serves it — the tier vp_skill serves there.
override="$HOME/vibe-palace-vault/Projects/fallback-case/skills/chair/SKILL.md"
mkdir -p "$(dirname "$override")"
printf '# Chair override\n\nfallback-case chair body\n' >"$override"
set +e
$cmd >"$CASE_LOGDIR/fallback-project.out" 2>"$CASE_LOGDIR/fallback-project.err"
rc=$?
set -e
assert_exit_code 0 "$rc"
assert_grep "^# skill: chair | source: project$" "$CASE_LOGDIR/fallback-project.out"
assert_grep "fallback-case chair body" "$CASE_LOGDIR/fallback-project.out"

# No shim names a vault path. `! grep -rl`, never `grep -L`: grep -L's exit
# status changed in GNU grep 3.5 and asserts nothing. Every directory must
# exist first — grep exits 2 on a missing one, which `!` would turn into a pass.
for d in .cursor .grok .claude; do
  assert_dir_exists "$CASE_DIR/proj/$d"
done
if grep -rl -e 'Templates/' -e "$HOME/vibe-palace-vault" .cursor .grok .claude; then
  echo "FAIL: ${BASH_SOURCE[0]}: the shim files above name a vault path" >&2
  exit 1
fi
