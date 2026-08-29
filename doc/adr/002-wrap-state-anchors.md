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
  last-iter                   # canonical iteration anchor (host-local)
  last-tasks-snapshot.json    # task-set snapshot for delta computation
```

`vp_stamp_iter` writes both atomically at the end of a wrap. `last-iter`
is the canonical project-side anchor for the pipeline.
`last-tasks-snapshot.json` records the active / done / cancelled task slug
sets (plus the anchor SHA) as of that stamp. Both files are host-local and
never committed — see the Amendment below for how the reference point is
carried without them.

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
same range. **Superseded by the Amendment below**: on a host-local anchor that
`git log` is always empty, so the SHA comes from the snapshot instead.

On the **first run** the iter stamp is not yet tracked, so the anchor SHA
is empty — the canonical "no prior wrap" signal. Rather than report zero
history, the engine falls back to the **oldest root commit**
(`git rev-list --max-parents=0 HEAD`, oldest entry) and walks from there,
so the inaugural wrap sees the whole project history. All git probes are
read-only, pin `GIT_TERMINAL_PROMPT=0` and `GIT_EDITOR=true` so they can
never hang on credential or editor prompts, and degrade to empty results
(never errors) when the directory is not a git repo.

### `iter_n` from the iteration narrative

`vp_collect_wrap_state` derives a PREVIEW iteration number by scanning the
iteration narrative with `^(#{2,3})[ \t]+Iteration (\d+)\b` (canonical H2, with
legacy H3 tolerated so the reader never goes blind on history already on disk);
`iter_n` is the maximum matched value plus one. This keeps the counter derived
from the human-readable record rather than a second source of truth.

The preview is advisory. The AUTHORITATIVE number is minted by
`vp_append_iteration`, which re-derives the maximum and appends **under a single
hold of the `iterations.md` vaultlock** (see ADR-006 and the
`append-iteration-server-owned` task). Because read-max and append are one
critical section there, two concurrent wraps cannot mint a duplicate — the
check-then-act race the unlocked preview scan is subject to but the write path is
not. The server also composes the canonical `## Iteration N — title` header
itself from the derived number and a caller-supplied title, so a caller can no
longer drift it to the wrong heading level.

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
- The anchors sit beside the code they describe, in the same working tree
  the commits/files probe reads. (The original form of this bullet claimed
  the anchors were committed and travelled with the repo; withdrawn by the
  Amendment below.)

**Negative / trade-offs:**

- The anchor lives in the project tree, adding a `.vibe-palace/` directory
  alongside the single `.vibe-palace.toml` identity file. This is a small,
  deliberate expansion of the project-local footprint for the wrap path.
- `task_deltas` is a set-difference on slugs, so a task that is renamed
  reads as one retired + one added rather than a rename.
- `iter_n` from `vp_collect_wrap_state` is a preview derived from the heading
  convention, so a narrative that drifted from it would mis-count. Since
  `vp_append_iteration` now mints the number server-side and writes the header
  itself, new drift cannot enter through the write path — but the reader stays
  tolerant of the H2/H3 history already on disk.

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

## Amendment (2026-08-29)

Operator decision on the vault task
`wrap-state-tools-orphaned-and-anchor-gitignored`, which found that this ADR and
the project `.gitignore` had specified opposite things since 2026-06-06 and that
the contradiction is why the anchor never advanced.

**Two claims above are WITHDRAWN:**

- `last-iter … (committed)` in the Decision's directory listing.
- *"The anchors are plain files the project can commit, so the reference point
  travels with the repo across machines"* in the Consequences.

They were never true of any shipped tree. `CanonicalProjectGitignorePatterns`
carries the exact line `/.vibe-palace/`, and `ReconcileProjectGitignore`
re-injects it whenever `vp init` or `vp commands upgrade` runs — so a `.gitignore`
edited to track the anchors is undone by the next reconcile. The decision is to
keep the ignore rule and correct this document, not the reverse.

### The contract now

**Both anchor files are host-local.** `.vibe-palace/` is one host-local
directory with several occupants — the two wrap anchors, the `claimed-<uuid>`
capture sentinels, and the ADR-005 enrichment queue — and the same
`/.vibe-palace/` ignore pattern covers all of them.

That shared occupancy is the reason the fork was settled this way. Concurrent
agent panes in one working tree each write capture sentinels into that
directory, while only the orchestrating pane wraps. Un-ignoring the anchors
means punching a hole in a pattern the reconciler owns and that a dozen
concurrent writers depend on, to gain a property (below) that is obtainable
without it.

**`last-iter` is not a git object.** Nothing reads its history, because an
untracked file has none.

**The reference point is `anchor_sha` in `last-tasks-snapshot.json`** — the
project's `git rev-parse HEAD` as of the stamp. `vp_stamp_iter` records it;
`vp_collect_wrap_state` prefers it when `git log … -- .vibe-palace/last-iter`
comes back empty, which on a host-local anchor is always. The wrap window is
therefore *"since the commit that was HEAD at the last stamp"* — the same
"since the last wrap" property the Context asked for, carried in the snapshot
rather than in the git graph.

**First wrap is unchanged.** With no snapshot, and so no `anchor_sha`, the
engine still falls back to the oldest root commit and walks the whole history.
That path is the one the Decision already describes and it keeps working.

### What this costs, and what it does not

The withdrawn property was real: a host that clones the repo fresh has no
anchor, so its first wrap reads as a first wrap and sees the full history.
That is the accepted trade. It is bounded — the fallback is defined, not an
error — and it is the same behaviour a genuinely new project gets.

The Alternatives are **not** reopened by this. *"Store anchors in the vault"*
was rejected because the commits/files probe is about the project repo's
history and the anchor belongs beside the code; that reasoning is about
*location*, not about tracking, and it survives intact.

## References

- PRD §1.1 (MCP-First write/wrap surface), §6.11 (wrap-state tools):
  `doc/PRD-vibe-palace.md`
- Wrap-state engine: `internal/wrapstate/` (`wrapstate.go`, `collect.go`,
  `gitprobe.go`, `stamp.go`)
- Wrap-state tools: `internal/tools/wrapstate_tools.go`
- The MCP-surface-version bump that advertises this expanded surface is
  deferred to the `mcp-surface-handshake` task.
