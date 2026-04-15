# Meta-safety assertion: $HOME must resolve entirely under the harness
# tmpdir. If this ever fails, the whole harness is suspect — cases
# could be mutating the real user environment.

cd "$CASE_DIR"

# $TMPROOT isn't directly exported to cases (run.sh uses it in the
# orchestrator) but $CASE_DIR is its descendant and $HOME is under
# $CASE_DIR. Verify that chain.

case "$HOME" in
  "$CASE_DIR"/*)
    # good — $HOME is inside this case's sandbox
    ;;
  *)
    echo "ISOLATION FAILURE: HOME='$HOME' is not inside CASE_DIR='$CASE_DIR'" >&2
    exit 1
    ;;
esac

case "$XDG_CONFIG_HOME" in
  "$CASE_DIR"/*)
    ;;
  *)
    echo "ISOLATION FAILURE: XDG_CONFIG_HOME='$XDG_CONFIG_HOME' is not inside CASE_DIR='$CASE_DIR'" >&2
    exit 1
    ;;
esac

# Also assert that common "real" paths are not accidentally set.
# /home/<user>/vibe-palace-vault would indicate the harness didn't redirect HOME.
real_home="$(getent passwd "$(id -un)" 2>/dev/null | cut -d: -f6)"
if [[ -n "$real_home" && "$HOME" == "$real_home" ]]; then
  echo "ISOLATION FAILURE: HOME equals the real user home ($real_home)" >&2
  exit 1
fi
