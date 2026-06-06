# ADR 002: Wrap-State Anchors

**Status:** Accepted (2026-06-06)
**Deciders:** Project owner
**Context:** Vibe-palace restore-mcp-vault-surface — Phase D (wrap-state machinery)

## Context

The `/wrap` flow needs to know how much has changed since the last time a
session was wrapped: which iteration we are on, what commits and files have
landed, and how the task set has shifted. Vibe-vault (`vv`) computed this
from per-project anchors and a snapshot diff. Vibe-palace inherits the need
so that an agent can drive the wrap end-to-end through MCP (see PRD §1.1,
§6.11) instead of eyeballing `git log` and the tasks directory.

The state must be:

- **Cheap and read-only to collect** — `vp_collect_wrap_state` is called
  speculatively at wrap time and must never mutate the repo or hang.
- **Anchored to a durable point** — "since the last wrap," not "since some
  fixed date," so the delta shrinks to zero immediately after a wrap.
- **Graceful on first run** — a brand-new project with no prior wrap, no
  anchor, and no snapshot must still produce a sensible record rather than
  an error.

MCP servers do not observe the repo directly; the wrap-state engine reads
the project working tree and the vault. The anchors are therefore stored on
the **project side**, next to the code being wrapped.

## Decision

### Project-root anchors under `.vibe-palace/`

Two files at the project repo root carry the wrap anchor. Unlike
vibe-vault's `.vibe-vault/`, vibe-palace stamps under `.vibe-palace/`:

```
<projectRoot>/.vibe-palace/
  last-iter                   # canonical iteration anchor (committed)
  last-tasks-snapshot.json    # task-set snapshot for delta computation
```

`vp_stamp_iter` writes both atomically at the end of a wrap. `last-iter`
is the canonical project-side anchor for the pipeline; once committed, the
commit that touched it becomes the reference point for the next cycle.
`last-tasks-snapshot.json` records the active / done / cancelled task slug
sets (plus the anchor SHA) as of that stamp.

### `task_deltas` via filesystem set-difference

`vp_collect_wrap_state` reads `last-tasks-snapshot.json` and diffs it
against the **live** vault tasks tree, as a pure set-difference on task
slugs:

- **added** — slugs present live but absent from the snapshot
- **retired** — slugs the snapshot had as active that are now done
- **cancelled** — slugs the snapshot had as active that are now cancelled

When the snapshot is absent (first run), the computation bootstraps
gracefully: everything live is reported as added, nothing as retired or
cancelled.

### `commits_since` via git walk from the anchor SHA

The commit list is `git log <anchorSHA>..HEAD` in the project repo, where
`anchorSHA` is the SHA of the most recent commit that touched
`.vibe-palace/last-iter`. Files changed use `git diff --name-only` over the
same range.

On the **first run** the iter stamp is not yet tracked, so the anchor SHA
is empty — the canonical "no prior wrap" signal. Rather than report zero
history, the engine falls back to the **oldest root commit**
(`git rev-list --max-parents=0 HEAD`, oldest entry) and walks from there,
so the inaugural wrap sees the whole project history. All git probes are
read-only, pin `GIT_TERMINAL_PROMPT=0` and `GIT_EDITOR=true` so they can
never hang on credential or editor prompts, and degrade to empty results
(never errors) when the directory is not a git repo.

### `iter_n` from the iteration narrative

The iteration number is parsed from the resume/iteration narrative with
`^### Iteration (\d+)\b` (multiline); `iter_n` is the maximum matched value
plus one. This keeps the iteration counter derived from the human-readable
record rather than a second source of truth.

### `render_wrap_text` is intentionally not ported

Vibe-vault exposed a `render_wrap_text` helper that formatted the wrap-state
record into prose. It was retired upstream and is **deliberately not** ported
here. `vp_collect_wrap_state` returns the structured record; rendering the
wrap narrative is the agent's job, which keeps the surface data-only and
avoids baking a presentation format into the tool contract.

## Consequences

**Positive:**

- The wrap delta is anchored to the last wrap and collapses to near-zero
  immediately after `vp_stamp_iter`, so each wrap sees only new work.
- First-run behavior is well defined: full history via the root-commit
  fallback, all-added task deltas, no errors.
- Collection is read-only and prompt-proof, safe to call speculatively.
- The anchors are plain files the project can commit, so the reference
  point travels with the repo across machines.

**Negative / trade-offs:**

- The anchor lives in the project tree, adding a `.vibe-palace/` directory
  alongside the single `.vibe-palace.toml` identity file. This is a small,
  deliberate expansion of the project-local footprint for the wrap path.
- `task_deltas` is a set-difference on slugs, so a task that is renamed
  reads as one retired + one added rather than a rename.
- Iteration parsing depends on the `### Iteration N` heading convention;
  narratives that drift from it will not advance `iter_n`.

## Alternatives considered

- **Date-based deltas ("since timestamp").** Rejected: the delta would not
  reset after a wrap, and clock skew across machines makes it fragile.
- **Store anchors in the vault instead of the project.** Rejected: the
  commits/files probe is inherently about the project repo's history; keeping
  the anchor beside the code keeps the reference point and the thing it
  references in the same git graph.
- **Track tasks via git diff of the tasks tree.** Rejected: the vault tasks
  directory and the project repo are different git repositories; a snapshot
  file is a simpler, repo-independent record of the task set.
- **Port `render_wrap_text`.** Rejected: it was retired upstream and would
  pin a presentation format into the tool; agents render the narrative.

## References

- PRD §1.1 (MCP-First write/wrap surface), §6.11 (wrap-state tools):
  `doc/PRD-vibe-palace.md`
- Wrap-state engine: `internal/wrapstate/` (`wrapstate.go`, `collect.go`,
  `gitprobe.go`, `stamp.go`)
- Wrap-state tools: `internal/tools/wrapstate_tools.go`
- The MCP-surface-version bump that advertises this expanded surface is
  deferred to the `mcp-surface-handshake` task.
