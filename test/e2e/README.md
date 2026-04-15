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

## `walkthrough` tier

A single-case harness whose **stdout is the documentation**. On pass,
the full annotated transcript is cat'd to the terminal; on fail, the
retained tmpdir plus the transcript tell you exactly what drifted.

The single case (`happy-path.sh`) mirrors the narrative of
`doc/TUTORIAL.md` **Part 2: Project Setup** — each step's banner names
the tutorial subsection it corresponds to (e.g. *Initialize a
Project*, *Understanding the Vault*). If this harness and Part 2
disagree, one of them is wrong.

One subtle point: the walkthrough runs `git init` in the project
directory **before** `vp init`. This is load-bearing, not decorative
— `vp init` keys several decisions (project-name auto-detection,
git-detected status in the summary, and the `.gitignore` handling
path) off the presence of `.git/`. Removing the `git init` would
silently test a different code path from the one the tutorial
documents.

How to run:

```bash
make walkthrough-e2e
# or:
bash test/e2e/walkthrough/run.sh
```

## `workflows` tier

A multi-iteration **measurement rig**. Each case exercises a realistic
user-facing workflow (seed drawers → tune → apply → idempotency
re-apply, etc.) across several iterations and emits a JSONL metrics
stream that the orchestrator renders as a summary table.

Three Go helpers are built once alongside `vp` at the top of `run.sh`:

- `mockllm` — a stub HTTP server speaking the subset of the LLM API
  that `vp tune` needs. Built as a Go binary so it ships with the
  repo and has no Python-runner footgun (no venv, no version skew, no
  network).
- `seeddrawer` — writes drawer rows directly to the palace JSONL
  (`source_type="seed"`, `added_by="e2e-rig"`) because `vp capture`
  is MCP-only and cannot be driven from bash.
- `tomleq` — decode-then-compare TOML equality, so the idempotency
  assertion is structural rather than textual.

### Hard-assert vs. tracked-metric rule

This is the contract for what a case is allowed to fail on:

> **If the value depends on the LLM mock's response file, it is a
> METRIC (tracked via `emit_metric`, never asserted). If it is part
> of the binary's contract with its caller — exit code, JSON shape,
> structural idempotency — it is an ASSERTION (`assert_*`).**

Concretely:

| What | Kind |
|---|---|
| exit code == 0 | assertion |
| `report.json` parses | assertion |
| `.project == "proj"` | assertion |
| `.samples_total >= 4` | assertion |
| `.proposals \| type == "array"` | assertion |
| `(.unmatched_flags // []) \| type == "array"` | assertion |
| second `--apply` → decoded-TOML-struct equality | assertion |
| `ms` per command | metric |
| `.samples_total` value | metric |
| `.proposals \| length` | metric |
| `.agreements`, `.disagreements` | metric |
| `.judgments_total` | metric |
| `total_tokens` (from mock log) | metric |

This split is why the rig stays green when the mock's response
distribution drifts, but will go red the instant `vp tune` breaks
its callable contract.

### Adding a new case

1. Create `test/e2e/workflows/NN-<slug>.sh` (two-digit prefix, next
   unused).
2. The orchestrator sources the case; `lib.sh` is already available
   and `CASE_DIR`, `CASE_HOME`, `HOME`, `XDG_CONFIG_HOME`,
   `METRIC_FILE="$CASE_DIR/metrics.jsonl"` are pre-set.
3. Use the convention:
   - Hard assertions via `assert_shape`, `assert_exit_code`,
     `assert_toml_struct_equal`.
   - Tracked values via `emit_metric <iter> <cmd> <key> <value>`.
4. Build on the provided helpers (`seed_drawer`, `mockllm`,
   `tomleq`) rather than reimplementing.

### Known limitations

- `vp capture` is MCP-only, so the rig uses `seed_drawer` which
  writes drawers with `source_type="seed"` and `added_by="e2e-rig"`.
  Any test that needs the real capture path has to wait for an MCP
  harness.
- Metrics are never consumed by any gate. They exist for trend
  inspection — CI uploads them as an artifact
  (`workflows-metrics-<sha>`, 14-day retention) on success.

How to run:

```bash
make workflows-e2e
# or:
bash test/e2e/workflows/run.sh
```
