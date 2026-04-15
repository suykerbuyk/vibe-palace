# vp end-to-end bash harness

A hermetic, shell-based smoke/integration harness for the `vp` CLI.
Each harness lives under `test/e2e/<command>/` with its own `run.sh`
orchestrator and numbered case scripts (`NN-<slug>.sh`). Shared
assertion helpers live in `test/e2e/lib.sh`.

Currently implemented: `test/e2e/init/` — covers `vp init`.

## Why bash and not Go?

The project already has Go-level integration tests (`make
integration`). This harness covers a different layer: it exercises the
real built `vp` binary as a shell subprocess, inside a sandboxed
`HOME` and `XDG_CONFIG_HOME`. That catches regressions Go tests can
miss — like a command-line parse mistake that silently falls through
to a default path the user didn't want.

## Isolation contract

Every case runs inside a tmpdir tree rooted at
`$TMPDIR/vp-e2e-<cmd>.XXXXXX` (typically `/tmp/vp-e2e-init.XXXXXX`).
Inside that root each case gets its own `cases/<NN-name>/home/` that
acts as both `$HOME` and the parent of `$XDG_CONFIG_HOME`. No case
writes outside its own sandbox.

**The harness cannot touch your real `~/.config/vibe-palace/` or
`~/vibe-palace-vault/`.** Case `06-cleanup-isolation.sh` asserts this
as a meta-safety check — if it ever fails, treat every other case as
suspect.

## Prerequisites

- `go` in `PATH` (to build `vp`)
- `git` in `PATH` (cases use `git init` to mark project dirs)
- Bash 4+

## Running

From the repo root:

```bash
# All cases (builds vp once, runs every NN-*.sh in order)
make init-e2e

# Or directly:
bash test/e2e/init/run.sh

# Run a single case by two-digit prefix:
bash test/e2e/init/run.sh 04

# Run a subset:
bash test/e2e/init/run.sh 01 04 07
```

On success the tmpdir is deleted. On any failure the tmpdir is
retained and its path is printed at the end of the run — inspect
`$TMPROOT/cases/<NN-name>/logs/stdout.log` and `stderr.log` for the
post-mortem.

## Adding a case

1. Create `test/e2e/init/NN-short-slug.sh` where `NN` is the next
   unused two-digit prefix.
2. Structure:
   ```bash
   # Headline comment describing what the case proves.
   # $CASE_DIR, $CASE_HOME, $HOME, $XDG_CONFIG_HOME already set by run.sh.
   # `lib.sh` is already sourced.
   cd "$CASE_DIR"

   # ... set up the scenario (git init, etc.) ...

   run_vp init ...
   assert_exit_code 0 "$LAST_EXIT_CODE"
   assert_file_exists ...
   ```
3. No shebang needed — the orchestrator sources cases, it does not
   exec them.
4. Cases should NOT cleanup — the orchestrator owns `$CASE_DIR`.

## Extending to other commands

Sibling harnesses go in `test/e2e/<command>/run.sh`. They `source
../lib.sh` for the shared helpers. Reserve `test/e2e/check/`,
`test/e2e/sync/`, and `test/e2e/absorb/` for future work.
