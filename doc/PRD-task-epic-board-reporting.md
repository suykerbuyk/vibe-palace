# PRD: Chronological Task/Epic Reporting ("Board View")

**Status:** Final (v10). Implemented as the vault epic `task-epic-board-reporting`
(`Projects/vibe-palace/tasks/task-epic-board-reporting.md` in the vibe-palace-vault) — that task's
own body carries this same content plus the child-task breakdown; this document is the durable,
source-controlled record of the design and its full reasoning trail.
**Author:** Chair, grounded in a direct read of `internal/storage/tasks.go`,
`internal/taskgraph/graph.go`, `internal/surface/version.go`, and `internal/surface/format.go` at
`HEAD` `0a91053`. See §9 for the full revision history (v1 → v10).

## 1. Problem Statement

`vp tasks` answers "what should I work on next" well: tasks are grouped by epic, ordered by
priority and dependency-topological order, with icebox and done/cancelled items filtered out by
default. That is a *prioritization* view.

It does not answer a different question: **"what happened, in what order, and when"** — the
question `git log` answers for code. Concretely, today:

- `vp tasks --all` (the actual daily-driver command named in the source request) forces a flat
  list whenever `--done` is added without `--epic`/`--standalone` — a day-to-day check-in that
  wants to see recent completions still grouped under their epic can't get that from the default
  invocation. `vp tasks --epic <slug> --done` already renders a grouped tree with the archive
  included (`GroupedArchived`, confirmed in source — §3); the gap is the *no-epic-named,
  see-everything* daily path, not a missing capability.
- There is no stored creation date, and no record of when a task file was last touched, anywhere
  in a task file (confirmed, §3).
- An epic has no notion of "when was this opened" independent of guessing from its slug or body.
- The task lifecycle itself is coarser than how this project actually works: there is no
  structural distinction between "not yet looked at" and "planned and reviewed, ready to start" —
  a real gate this project's own doctrine already applies in practice (investigation → plan →
  human/Chair review → implementation), today recorded only as prose inside a task body.

**Three problems remain explicitly out of scope**, by the requester's own framing:

1. Visual indication of intended implementation order within/across epics beyond priority labels
   — future work.
2. The abandon/reword/promote-into-a-new-epic lineage hazard — no existing mechanism handles this
   (confirmed, §3). **RESOLVED (§7.11): a minimal structured `superseded_by`/`supersedes` link**,
   mirroring the existing `depends_on` edge.
3. **Structured tracking of *why*/*what* a `blocked` task is blocked on** — see §4.4. Named by the
   requester as something they explicitly do not know how to solve without a large amount of
   out-of-band auditing; this PRD does not attempt to either.
4. **Formalizing "Story" as a fully first-class, well-defined concept** — see §4.5. The structural
   support already exists and this feature will use it; giving it a real definition and lifecycle
   is bigger than this PRD and is recommended as its own future epic.

## 2. Goals

1. A reporting view (`vp board` — see §4.3), reachable from the CLI, that renders:
   - Every not-fully-done epic, sorted by the epic's own creation date, listing completed
     children (created + completion date) and open children (created date only).
   - Standalone tasks, same treatment, own section.
   - An **Icebox** section — unlike `vp tasks`, always shown, never hidden by default (§4.2).
   - A **History** section: everything fully completed, most-recent-first; completed children
     within a completed epic likewise most-recent-first.
2. Dates that are **trustworthy** — server-stamped at the moment of a mutation, never agent-typed.
3. **Do it once, properly, rather than pay repeatedly for a cheaper approximation** — persisted,
   versioned data plus a one-time migration (operator decision, §4).
4. **Open the task header format for future extensibility** — a Jira-style importer/exporter is a
   named, not-yet-planned future use; this round should not make that harder. This includes a
   per-file schema marker tied to the same version counter that gates the vault itself (§4.1b).
5. **A task lifecycle that reflects how work actually happens here**, not just a reporting
   overlay: a real `planning → reviewed → in_progress → {done, cancelled}` vocabulary, with
   `blocked`/`icebox` as orthogonal pause states, replacing the flatter status model that
   conflated "not started" with "not yet reviewed."
6. Backward compatibility: today's priority/dependency-ordered `vp tasks` default is unchanged.

## 3. Current State (verified against source)

**`TaskMeta` (`internal/storage/tasks.go:36-44`) has seven fields** — `Slug, Title, Status,
Priority, Done, Parent, Depends`. None is a timestamp. **`Depends` already exists** — dependency
tracking between tasks is a solved problem today, written by `set_relations` and consumed by
`internal/taskgraph` for topological ordering. Nothing in this PRD needs to add a dependency
mechanism; see §4.4 for the narrower, genuinely unsolved question the requester raised.

**The task file header is currently a closed four-field set, not an open schema.**
`isHeaderFieldLine` (`tasks.go:1476-1484`) recognizes exactly `Status`/`Priority`/`Parent`/
`Depends` — the comment above the constant block says "these are the only fields in it."
`headerBlock` stops at the first line that isn't one of those four. This is the constraint being
removed (§4.1), not worked around.

**Status today (`tasks.go:107-134`) is four non-terminal values** — `pending`, `in_progress`,
`blocked`, `icebox` — **plus two terminal values reached only by moving the file**:
`StatusRetired = "retired"` (→ `tasks/done/`) and `StatusCancelled = "cancelled"`
(→ `tasks/cancelled/`). **A pre-existing naming mismatch, resolved by this PRD**: the directory is
named `done/` but the stored status string was `"retired"`, not `"done"`. Operator ruling:
`"retired"` is not itself a meaningful state — the natural-language phrasing ("when a task is
done, we retire it") names an *action* (confirm the work delivered what it was chartered to do,
confirm it's in source control, then archive it), not a status. **The status value becomes
`"done"`; the `vp_manage_task` action verb stays `retire`** — retiring a task is still the act you
perform, `done` is what it leaves the task marked as. Renamed as part of the same one-time
migration sweep as `pending`→`planning` (§4.1).

**This project has a direct precedent for what changing "what gets written into every task file"
costs: an `MCPSurfaceVersion` bump.** `internal/surface/version.go:46-67` documents the 2→3 bump as
five distinct on-disk shape changes; `CreateTask` emitting an unconditional `## Context` heading
(lines 56-59) is one of the five, not the sole cause, but is the closest precedent for this PRD's
own header changes. New header fields and a widened status vocabulary, written from
`CreateTask`/`moveTask`/`update_status` onward, are the same class of change and bump
`MCPSurfaceVersion` the same way — a v8-era binary must refuse to *write* a vault a newer binary
has already touched, exactly as today.

**A second, genuinely different axis is the correct home for the per-file schema marker (§4.1b),
not `MCPSurfaceVersion` — revised after a second Grok review verified this against source.**
`internal/surface/format.go` documents `RequiredDataFormat` as "a second, orthogonal version
mechanism to the `.surface` tool-surface axis... The two answer different questions and MUST stay
independent": `.surface`/`MCPSurfaceVersion` asks "is the *binary* new enough to write here" (a
write hazard); `RequiredDataFormat`/`vault.toml`'s `format` field asks "is the *data on disk* in
the format I need" (a read hazard) — precisely this PRD's actual question for `CreateTime`/
`ModTime`/the widened `Status` values. `RequiredDataFormat` is already a single vault-wide integer
(currently `1`, for the KG-triple-filename migration), stamped via a dedicated `WriteFormat`
(monotone, called only by a migration on completion) — the exact shape this PRD's own migration
already needs. **Operator decision: use this axis, bumped to `2`, rather than coupling the marker
to `MCPSurfaceVersion`.** Both bumps still happen for this feature — `MCPSurfaceVersion` because
the *write* shape changes (new fields, new status values), `RequiredDataFormat` because the *data*
isn't trustworthy for board reporting until migrated — they answer different questions and this
PRD needs both, not one standing in for the other.

**Operator decision, related but broader than this feature: extend the existing release-tag
convention to encode both axes.** `MCPSurfaceVersion` is already gated as the release tag's major
version component by a CI check in `.github/workflows/release.yml` ("Guard tag major against
MCPSurfaceVersion"). Going forward, the tag's *minor* component should likewise be gated against
`RequiredDataFormat`, giving `<MCPSurfaceVersion>.<RequiredDataFormat>.<build>` as the project's
release-versioning scheme — a small, direct extension of an already-half-built pattern, not a new
one. This is a project-wide policy decision, not scoped to task reporting alone; recommend
capturing it in the same ADR (§8, task 0) since both changes land in the same release.

**`CalendarDay`, not RFC3339, is this project's established convention for a human-facing date.**
`internal/storage/clock.go` distinguishes RFC3339 UTC instants (logs/archives) from a writer-local
`YYYY-MM-DD` `CalendarDay` (anything a human reads as "when"). New date fields reuse
`storage.CalendarDay`.

**Sorting today is priority + dependency order, never date** — `grouped()`
(`internal/taskgraph/graph.go:593-666`).

**`GroupedArchived` already exists** (`graph.go:583-588`) — the grouping half of "completed +
open children together, under their epic" is already built. Reuse it.

**Icebox is a status, not a location** — composes cleanly, but this feature's default display
policy differs from `vp tasks`'s (§4.2): icebox is always shown here, never hidden.

**Cross-project moves are a bare rename plus a separately-sequenced tombstone.**
`MoveTaskToProject` (`tasks.go:1066-1073`) writes no provenance or tombstone itself; the `move`
action of `vp_manage_task` separately files a tombstone at the source
(`Projects/<source>/tasks/cancelled/<task>.md`) and a "Moved from" note at the destination. **Any
git-history-derived date must detect this and label it "moved from `<project>`,"** not silently
misdate it as cancel-then-rebirth.

**`amend`/`overwrite` are refused on an archived task only at the MCP/CLI caller layer, not at the
storage layer — corrected after a second Grok review.** `vp_manage_task`'s `overwrite` handler and
the `vp tasks edit` CLI path both refuse to touch a `done`/`cancelled` task. But the storage
primitive underneath, `Vault.OverwriteTaskFile`, says explicitly: "Whether writing to an ARCHIVED
task is permitted is the CALLER's concern... This writer honors whatever path resolveTaskFile
returns." A sibling function, `OverwriteTaskFileRewritingHeader`, exists specifically so migration
commands *can* rewrite an archived header, and two already do: `vp migrate task-status` (whose own
doc comment is "the one-time pass that makes an ARCHIVED task's `Status:` line agree with the
directory it sits in") and `vp migrate task-header`. **So an archived task's `ModTime` is not
actually a hard invariant — see §4.1 for the operator's call on this.**

**"Epic" and "Story" are both purely derived, never stored.** `IsEpic()`'s own comment: "Derived,
never stored." **Confirmed: `Role()` (`graph.go:544-560`) already classifies a non-root node with
its own children as `"story"`** — a task that is itself an epic's child *and* has children of its
own is already, mechanically, a story today. The Epic→Story→Task structure the operator wants
(§4.5) is not new engineering; it is an existing derivation nobody has deliberately leaned on yet.

**`StaleParents` (an active child under an already-terminal parent) is already computed and
already surfaced in plain `vp tasks` output, but has no `vp_check`/`vp audit vault` dimension or
repair mechanism** — corrected after the second Grok review found `vp tasks` already prints it
(confirmed: `cmd_tasks.go:368-369`, `"stale parent: %s is active but its epic %s is finished"`,
inside its `PROBLEMS` section). The gap is narrower than first stated: no dedicated diagnostic
dimension, and no action exists to fix one once found — reopening an archived parent isn't
possible via any existing action, so a fix is either a new "reopen" action or reparenting the
stale child via `set_relations`. See §4.5.

**No supersession/promotion mechanism exists for one task pointing at another — resolved, §4.6.**
`vp_manage_task`'s nine actions (`create, amend, overwrite, set_meta, update_status,
set_relations, retire, cancel, move`) have no such concept today, and the deleted
`vp_carried_promote_to_task` tool (confirmed via `doc/PRD-vibe-palace.md` §6.10 and ADR-003)
promoted a `resume.md` "Carried forward" *bullet* into a new task — it never linked one task to a
successor. The gap the requester named is real; nothing pre-existing solved it. Operator decision:
add a minimal structured link, mirroring `depends_on` — see §4.6.

## 4. Requirements

### 4.1 Data model: `CreateTime`, `ModTime`, and a widened status vocabulary

**Operator decisions (2026-09-15):**

1. Persist timestamps via an open/extensible header schema and a one-time migration, rather than
   deriving them at query time (superseding v2's read-time design — see §9).
2. **Four header fields**: `CreateTime`, `Status`, `Priority` (unchanged), `ModTime`. `CreateTime`
   never mutates. **`ModTime` is a generic last-modification timestamp — like a filesystem
   `mtime` attribute — not a status-transition-specific field.** It updates on *any* write to the
   task file (a status change, an `amend`, a `set_meta`, a `set_relations` edit).
3. **`ModTime` as a completion date is best-effort, not a guaranteed invariant — operator decision
   after this was found to be false.** Archived files are *not* actually frozen at the storage
   layer (§3): the existing `vp migrate task-status`/`vp migrate task-header` commands can and do
   rewrite an archived task's header, which would perturb its `ModTime` for reasons unrelated to
   completion. **Accepted, deliberately**: this is expected to be rare (a maintenance migration
   touching an already-archived file), and when it happens, vault git history remains the fallback
   to re-derive the true completion date — the same recovery path this PRD already relies on for
   the initial backfill (§4.1d). No new enforcement (freezing archived files, or excluding them
   from `vp migrate task-*`) is being added to guarantee this; it is a known, accepted edge case,
   not a defect being engineered around. For an *active* task, `ModTime` answers a genuinely
   different, useful question regardless — "when was this last touched at all."
4. **`ModTime` loses nothing relative to a full transition log**: every write is already a vault
   git commit. If "when did this specifically leave `planning`" is ever needed,
   `git log -p -- tasks/<slug>.md` still answers it. `ModTime` is the cheap current-state summary;
   git is still the record it's derived from.
5. **The status vocabulary widens**, and existing `pending` values are renamed to `planning` as
   part of the same one-time migration sweep (safe to do uniformly: `reviewed` did not exist
   before this PRD, so no historical task can have correctly passed that gate). The set below is
   illustrative, not necessarily final, per the operator's own framing:
   - **`planning`** (renamed from `pending`) — not yet reviewed.
   - **`reviewed`** — plan accepted, not yet started. Confirmed correct over `review` (§7.14) —
     past tense, since it names a completed review, not a pending one. Set explicitly via
     `update_status`, not auto-inferred from an `amend` call.
   - **`in_progress`** — unchanged.
   - **`blocked`**, **`icebox`** — unchanged in meaning; orthogonal pause states, not sequence
     positions.
   - **`done`** (renamed from `retired`) — terminal, now matches its `done/` directory. The
     `vp_manage_task` action verb stays `retire` (see §3).
   - **`cancelled`** — unchanged (terminal).
6. **Transitions are freely settable, not a strictly-enforced sequence** — consistent with this
   project's doctrine to skip ceremony for a trivial change.

**Mechanism, per §3's closed-header-set finding:**

**(a) Generalize the header parser from a closed list to an open, extensible schema.** Any
well-formed `**Field:** value` line is recognized as header metadata, whether or not the current
binary knows what it means — an unrecognized field is preserved verbatim on rewrite rather than
dropped or used to terminate the header block early. **New fields are appended after `Parent`/
`Depends`, never inserted before them — a real constraint, not a style preference.** The surface
gate already refuses an old binary's *write* against a newer vault (confirmed), but it does not
gate *reads* — `internal/surface/version.go` documents the check as specifically "a WRITE hazard."
An old binary can still open a migrated file; today's `headerBlock` stops at the first line it
doesn't recognize, so a new field placed *before* `Parent`/`Depends` would make an old reader
silently lose them (no error, just a wrong-looking task with no dependencies). Appending costs
nothing and avoids this outright.

**(b) RESOLVED — a per-file header schema/version marker is built now, and it uses the
`RequiredDataFormat` axis, not `MCPSurfaceVersion`** (revised, §3): the marker's value is the
`RequiredDataFormat` the file was migrated under, recorded per-file for cheap lookup by future
tooling (a Jira importer, or the next reporting feature) — reusing the axis that already asks "is
this data in the format I need," not the one that asks "is this binary allowed to write here."

**(c) `CreateTime` and `ModTime` header fields**, `CalendarDay`-formatted, stamped **server-side
only**, never accepted as agent input. `CreateTask` stamps both, equal, at creation. Every other
mutating action that touches an active task (`amend`, `set_meta`, `update_status`,
`set_relations`, `retire`, `cancel`, `overwrite`, `move`) stamps `ModTime` at the moment of the
actual write.

**(d) A one-time, explicit migration.** Gated on every host being confirmed on the new
`MCPSurfaceVersion` (the write axis — every binary must be updated first) **and completing by
bumping `RequiredDataFormat` to `2` on success** (the data axis — this is what tells `vp board`
the data is now trustworthy). The migration, per existing task file:
- Renames `pending` → `planning` and `retired` → `done` in the `Status` field.
- Stamps the per-file schema marker (§4.1b) with `RequiredDataFormat = 2`.
- Derives `CreateTime` from the earliest commit touching the file's current path (with the
  cross-project-move detection from §3 — label a moved task "moved from `<project>`" instead of
  asserting a plain creation date).
- Derives `ModTime` from the single most recent commit touching the file's current path — a plain
  `git log -1`.
- **RESOLVED — manual, and run at a coordinated maintenance window**, not merely "manual" in the
  abstract: every other agent/session across every project shut down, every host's `vp` binary
  updated to the new surface version first, *then* the migration runs once, vault-wide, ending
  with `WriteFormat(root, 2)`. Matches this project's "report, don't auto-fix" convention,
  sharpened with an explicit operational procedure rather than left as "run it whenever."

**After migration, all sorting/reporting reads the persisted fields directly.**

**Complementary, optional:** an opportunistic "upgrade on touch" self-heal for any legacy header
encountered by another code path before the explicit migration reaches it (this project already
uses this pattern elsewhere — the ONNX self-heal, the stale-template-copy self-heal in
`vp_vault_sync`). Decide in the task breakdown whether to build this alongside the explicit sweep.

**Backward-compatibility requirement to define, not assume:** what an **older binary reading** a
post-migration file should do — the open-schema design in (a) makes "ignore fields it doesn't
recognize" the natural answer, but state it as a requirement for whoever implements it.

**No longer a verification requirement** — resolved by the operator's decision above:
`ModTime`-as-completion-date is explicitly best-effort, with git history as the fallback for the
rare case a later migration perturbs it, so confirming every action's archived-write behavior is
no longer load-bearing for this PRD (though the audit exists — see §9's v8→v9 note — for whoever
implements it, as useful context, not a gate).

### 4.2 Reporting query: three buckets — Active, Icebox, History

- Reuse `GroupedArchived` for the grouping half; it already exists.
- **Three buckets, not two — and bucket assignment is decided ONLY by the entity's own `Status`
  field, never by inspecting its children.** Resolved after discussion (§7.6 revised): the
  entity's own status is the single source of truth for its own bucket, exactly as already agreed
  for History (§7.5) — extending that same principle to Icebox rather than special-casing it.
  - **Active** — own `Status` is `planning`, `reviewed`, `in_progress`, or `blocked`.
  - **Icebox** — own `Status` is `icebox`.
  - **History** — own `Status` is `done`/`cancelled` (confirmed, §7.5).
  - **Explicitly rejected**: a child-driven promotion rule (an epic auto-moves to Icebox when
    every remaining non-terminal child happens to be `icebox`, even though the epic's own status
    says otherwise). Operator ruling: this would let icebox-ing one task silently change how a
    *different*, untouched task (its epic) displays — a violation of the principle of least
    astonishment. A mutation should only ever have visible consequences on the record it was
    actually made to. If an epic itself should read as parked, someone icebox-es the epic,
    deliberately — nothing does it for them as a side effect of a child's own state.
- **Icebox is always shown in this report — never hidden by default**, unlike `vp tasks` (resolved,
  §7.6). This report is meant to be a complete picture, not a filtered work queue.
- Sort key for Active epics/standalone: `CreateTime`. Sort key for History: `ModTime` descending.
- Sort key for open children *within* an active epic: **`CreateTime`, newest first** (resolved,
  §7.8). Priority/dependency order and cross-epic dependency ordering were both considered and
  explicitly deferred — the latter's complexity (dependencies can cross epic boundaries) is a
  real complication, not solved here.
- **`StaleParents` handling**: an active child found under an already-`done`/`cancelled` parent
  (§3) is flagged distinctly in the report, not silently sorted into either bucket. **A repair
  mechanism is a separate, follow-on task** (§8) — no default is proposed here, since "reopen the
  parent" and "reparent the child" are materially different fixes with no obvious single answer.

### 4.3 CLI surface: a new `vp board` command; MCP tool as fast-follow

- **RESOLVED (§7.10) — a new top-level `vp board` command**, not another `vp tasks` flag. The
  original request describes one continuous report — active work with dates, then icebox, then
  history — which reads more naturally as its own command than a flag combination on `vp tasks`,
  and matches this PRD's own "Board View" framing.
- **RESOLVED (§7.12) — scoped to the current project by default**, matching `vp tasks`'s existing
  `--project`/auto-detect convention (`detectTasksProject`, `cmd_tasks.go`) exactly rather than
  inventing a new scoping mechanism: no cross-project view, one project unless `--project` is
  given.
- **MCP tool surface: still deferred as an explicit fast-follow**, not built in this round.

### 4.4 Dependencies and blockers: what's solved, what isn't

- **"This task depends on that task" is already solved** — `Depends` (§3) already exists and is
  already consumed by `internal/taskgraph` for dependency-topological ordering.
- **"Why is this task `blocked` / what is it blocked *on*" is genuinely unsolved, and stays
  unsolved in this round.** Designing structured tracking for this risks exactly the "huge amount
  of out-of-band auditing" the operator flagged, with no forcing function to solve it now. The
  existing mechanism — free-text prose in the task body — remains how this is recorded.

### 4.5 Stories and StaleParents: real gaps, deliberately not solved here

- **Story is structurally already supported** (`Role()`, §3) — this feature's display logic can
  render Epic→Story→Task nesting today with no new `taskgraph` work, simply by reading the
  existing derivation. **Making "Story" a fully first-class, well-defined concept — its own
  lifecycle expectations, when one should be created — is a bigger question the operator has said
  they cannot yet answer**, and is recommended as its own future epic rather than guessed at here.
- **`StaleParents` needs a repair mechanism that doesn't exist yet** (§3, §4.2). Scoped as its own
  follow-on task (§8) rather than solved inline, since the right fix (reopen vs. reparent) is a
  real design question, not a mechanical one.

### 4.6 Supersession/rework: a minimal structured link (resolved, §7.11)

Operator decision: **option (b)** — a real, structured link, not deferred and not left freeform.
Mirrors the existing `depends_on` edge, but with the opposite meaning: `depends_on` says "that
task must finish before this one can," while `superseded_by`/`supersedes` says "this task's
remaining intent moved to that one." Concretely:

- A new field pair, analogous to `Parent`/`Depends`: e.g. `SupersededBy` on the abandoned task
  (pointing at its successor), derivable in reverse as `Supersedes` for display purposes the same
  way `IsEpic()` derives epic-ness from inbound `Parent` edges — i.e. likely only one direction
  needs to be *written*, with the other *derived*, matching this project's "reuse, don't store
  what's derivable" bias (§6). Exact mechanics (a written field vs. fully derived, whether it
  lives in the header like `Depends` or needs its own action) are a task-breakdown decision.
- Most natural to set at `cancel` time — when task A is abandoned in favor of task B, the cancel
  action (or a follow-up `set_relations`-style call) records the link.
- Reporting payoff: History can render "cancelled → superseded by `task-b-slug`" instead of a bare
  "cancelled," closing the exact gap the requester named — a reader of history no longer hits a
  dead end at an abandoned task with no indication the work continued elsewhere.
- Since the header is already being opened up for `CreateTime`/`ModTime` (§4.1a), this is
  meaningfully cheaper now than it would have been before this PRD started.

## 5. Non-goals (this round)

1. Priority/order visualization beyond labels (future work, per requester).
2. Any change to the existing priority/dependency `vp tasks` default view.
3. ~~Solving the supersession/rework hazard~~ — **RESOLVED, no longer a non-goal**: a minimal
   structured link, §4.6.
4. Building the Jira importer/exporter itself — only avoiding decisions that would make it harder.
5. An MCP tool for this view in the first pass (§4.3) — named fast-follow, not indefinite deferral.
6. A full append-only status-transition history log in the header (§4.1) — not a loss, since git
   history already is that log.
7. Enforcing status-transition order (§4.1) — status remains freely settable.
8. Structured tracking of what/why a `blocked` task is blocked on (§4.4) — explicitly deferred.
9. Formalizing "Story" as a first-class concept (§4.5) — structural support is used as-is; a real
   definition is recommended as separate future work.
10. Building a `StaleParents` repair mechanism as part of this feature (§4.5) — flagged, filed as
    a separate follow-on task, not designed here.

## 6. Design Constraint: Reuse, Don't Parallel-Build

Generalize the *existing* header parser into an open schema, widen the *existing* `Status` enum,
reuse the *existing* `Role()` derivation for Story, and reuse the *existing* `RequiredDataFormat`
axis for the per-file marker (§4.1b) — rather than: a sidecar JSON/index file per task holding
timestamps or lifecycle state; a second grouping mode parallel to `GroupedArchived`; a new date
format alongside `CalendarDay`; a new status-transition enforcement layer; a new dependency
mechanism parallel to `Depends`; or a third, brand-new version axis alongside `MCPSurfaceVersion`
and `RequiredDataFormat` when the second of those already answers this PRD's exact question. The
header stays the single source of truth.

## 7. Open Questions for the Operator

1. **Header field names** — `CreateTime`/`ModTime` (confirmed), cosmetic naming-style note only.
2. **RESOLVED — build the per-file schema/version marker now, using `RequiredDataFormat`**
   (revised from `MCPSurfaceVersion` — §3, §4.1b), bumped to `2` for this feature. The release-tag
   convention extends to encode both axes: `<MCPSurfaceVersion>.<RequiredDataFormat>.<build>`.
3. **RESOLVED — yes, this gets its own ADR.** Filed as task 0 in §8.
4. **RESOLVED — manual, run at a coordinated maintenance window**: every other agent/session
   across every project shut down, every host updated to the new binary first, then the migration
   runs once, vault-wide. The surface gate itself already handles "is the vault safely aligned" —
   no separate detection mechanism needed (§4.1d).
5. **RESOLVED** — epic-in-History = epic's own `Status` is `done`/`cancelled`.
6. **RESOLVED — a third bucket, Icebox, decided solely by the entity's own `Status` field** —
   revised after discussion (§4.2): an epic's bucket never depends on inspecting its children
   (POLA — icebox-ing a child must not silently reclassify its untouched epic). This report
   always shows icebox, with no `vp tasks`-style default hiding.
7. **RESOLVED — flagged, and a repair mechanism is needed but not designed here** (§4.5) — filed
   as a separate follow-on task.
8. **RESOLVED — creation date, newest first**, for open children within an active epic.
9. **PARTIALLY RESOLVED — use the existing `Role()` derivation for display now; formalizing
   "Story" is recommended as separate future work** (§4.5).
10. **RESOLVED — a new `vp board` top-level command** (§4.3). Confirmed.
11. **RESOLVED — option (b)**: a minimal structured `superseded_by`/`supersedes` link. Design
    sketch in §4.6; exact mechanics (written field vs. derived, where it's set) are a
    task-breakdown decision.
12. **RESOLVED — the `vp board` report is per-project by default**, matching `vp tasks`'s existing
    `--project`/auto-detect convention exactly; the migration (a separate concern) stays
    vault-wide, unchanged.
13. **RESOLVED — rename `"retired"` → `"done"`.**
14. **RESOLVED — `reviewed`** (past tense — a completed review — vs. `review`, which would read as
    a pending/future action). Confirmed correct.

## 8. Proposed Task Breakdown (preview — finalize after this PRD is approved)

0. **An ADR** (confirmed, §7.3) documenting the open/extensible header schema, the widened status
   vocabulary, the per-file schema marker's coupling to `RequiredDataFormat` (not
   `MCPSurfaceVersion`), and the extended `<surface>.<format>.<build>` release-tag convention.
1. **Generalize the header parser/writer** from closed-list to open/extensible — preserve unknown
   fields verbatim on any rewrite; new fields appended strictly after `Parent`/`Depends` (§4.1a);
   add the per-file schema/version marker (confirmed, §7.2), storing `RequiredDataFormat`.
2. **Widen and rename the `Status` enum**: add `planning`, `reviewed`; rename `retired`→`done`
   (the Go constant, e.g. `StatusRetired`→`StatusDone`). **Must include `cmd_migrate_task_status.go`
   and `internal/vaultaudit`'s `DimTaskStatusDirectory`** — confirmed by the second Grok review:
   `cmd_migrate_task_status.go`'s `archiveDirs` mapping is a live command whose entire job is
   writing `StatusRetired` into every `done/` file; left unchanged, running it post-rename would
   silently revert `done` back to `retired`. (Correction: the PRD's earlier example citation
   `tasks.go:731` was wrong — that line is a directory-name string, not a status comparison; the
   real sites are `tasks.go:132` (`StatusRetired` const), `:148` (`IsTerminalStatus`), `:745`
   (`CreateTask`'s `pending` literal), the MCP schema enum (`task_tools.go:478`), and the two
   migrate-command/vaultaudit sites above.) The `retire` action verb in `vp_manage_task`'s schema
   is unaffected — only the stored status string and its dependent comparison sites change.
3. **Add `CreateTime`/`ModTime` header fields**, stamped server-side by every mutating action that
   touches an active task, using `storage.CalendarDay`. `ModTime`-as-completion-date is best-effort
   (§4.1) — no archived-immutability enforcement needs building.
4. **Bump `MCPSurfaceVersion`** (write axis — new fields/values written from `CreateTask` onward)
   **and separately bump `RequiredDataFormat` to `2`** (data axis — gates `vp board` trusting the
   new fields), following the 2→3 and format-`0→1` precedents respectively; update golden/tests.
5. **One-time migration**: `pending`→`planning` and `retired`→`done` renames (task 2's full site
   list), per-file schema marker stamping with `RequiredDataFormat`, plus git-history-derived
   `CreateTime`/`ModTime` backfill (including cross-project-move detection), ending with
   `WriteFormat(root, 2)`, gated on confirmed surface-version rollout completion.
6. **(Optional, §7.4)** Lazy upgrade-on-touch self-heal for any legacy file the sweep missed.
7. **Three-bucket grouping** (Active/Icebox/History) in `internal/taskgraph`, reading the
   persisted fields directly. Bucket is a pure function of the entity's own `Status` field alone
   (§4.2, §7.5, §7.6) — no child-inspection logic, deliberately.
8. **CLI: `vp board`** (§4.3) — new command, Active + Icebox + History in one report.
9. **(Fast-follow, separate task)** MCP tool exposing the structured data.
10. **(Separate task, not designed here)** `StaleParents` repair mechanism (§4.5).
11. **Supersession/rework tracking** (§4.6) — the `superseded_by`/`supersedes` link, and wiring
    History's rendering to show it. No longer conditional; confirmed in scope.
12. **(Separate future epic, not part of this one)** Formalize "Story" as a first-class concept
    (§4.5).

Likely ordering: 0/1/2 are foundational and gate everything that touches the header or status
model. 3 depends on 1 and 2. 4 (the surface bump) lands in the same release as 1-3. 5 depends on 4
having *actually rolled out* vault-wide. 6 is optional, alongside or after 5. 7 depends on 3, and
works for new/updated tasks immediately even before 5 completes. 8 depends on 7. 11 (supersession)
is confirmed in-scope but independent of 1-8's critical path — it touches the header (so benefits
from landing alongside 1) but nothing else depends on it. 9, 10, 12 are later, independent
follow-ons — none blocks this epic's own completion.

## 9. Revision History

**v1 → v2:** An independent adversarial review (Grok, blind, read-only) verified every v1 claim
against source and found four high-severity problems: the header was assumed open when it's a
closed four-field parser; the resulting design needed an `MCPSurfaceVersion` bump v1 never
mentioned; v1 duplicated the already-existing `GroupedArchived`; and v1's git-backfill heuristic
would have misdated cross-project moves. v2 responded by avoiding the schema change entirely.

**v2 → v3:** The operator made an explicit architectural call in the opposite direction: pay the
header/schema/surface-version cost once, deliberately, and use the opportunity to open the header
format for future extensibility (a possible future Jira import/export named as the motivating,
not-yet-planned case).

**v3 → v4:** Split the conflated `Created`/`Completed` framing into an immutable creation
timestamp and a mutable "current state since" timestamp (`StatusChanged`), and widened `Status` to
include `planning` (renamed from `pending`) and a new `reviewed` state.

**v4 → v5:** Confirmed `StatusChanged` loses nothing relative to a full log (git already is that
log); confirmed an ADR is warranted; resolved the `"retired"`/`"done"` naming mismatch as a rename.

**v5 → v6:** Replaced `StatusChanged` with `ModTime`, a generic last-modification timestamp,
after the operator identified the narrower field was itself a further conflation. Confirmed this
loses nothing for the completion-date use case, since archived tasks are frozen. Clarified that
`Depends` already solves dependency tracking, and split off "why is this blocked" as a separate,
explicitly unsolved non-goal.

**v6 → v7:** Resolved six more open questions in one pass: the per-file schema marker is built now
and is explicitly the same counter as `MCPSurfaceVersion`, never a parallel one; the migration
detection concern is moot given the existing surface gate; icebox gets a third report bucket and
is always shown; `StaleParents` is confirmed to have no existing repair mechanism and is filed as
a separate task; open-child sort order is creation date, newest first; and "Story" is confirmed to
already have structural support (`Role()`), with full formalization recommended as separate future
work. Proposed `vp board` as a new top-level command. Mid-revision, the operator also resolved the
supersession/rework hazard — **option (b)**, a minimal structured `superseded_by`/`supersedes`
link mirroring `depends_on` (§4.6) — no longer conditional; folded in as task 11. Left open: the
exact scope of the *report* itself (per-project vs. cross-project, as distinct from the
migration's already-settled vault-wide scope), and the "review"/"reviewed" naming question.

**v7 → v8:** All four remaining open questions resolved. The migration is manual, run at a
coordinated maintenance window (every session/project shut down, every host updated, then the
vault-wide sweep) — not merely "manual" in the abstract. `vp board` confirmed as the command name.
`vp board` is scoped to the current project by default, matching `vp tasks`'s existing
`--project`/auto-detect convention exactly — the migration's separate vault-wide scope is
unaffected. `reviewed` confirmed correct over `review` (past tense — a completed review — vs. a
reading that would imply a pending action). No open questions remain; this PRD is ready to become
an epic.

**v8 → v9:** A second Grok review (blind, read-only, against the finalized v8) found one critical
and several high-severity problems, all hand-verified against source before acting on them.
**Critical, accepted rather than fixed**: archived tasks are not actually frozen at the storage
layer — `Vault.OverwriteTaskFile` explicitly leaves archived-write permission to the caller, and
`vp migrate task-status`/`task-header` already rewrite archived headers. Operator decision:
`ModTime`-as-completion-date stays best-effort; git history is the accepted fallback for the rare
case a migration perturbs it, and no new enforcement is added. **High, fixed**: the `Status` rename
plan now explicitly includes `cmd_migrate_task_status.go` (a live command that would otherwise
silently revert `done`→`retired` post-migration) and corrects a wrong example citation
(`tasks.go:731` was a directory name, not a status comparison). **High, fixed**: new header fields
must be appended after `Parent`/`Depends`, not before — the surface gate blocks old-binary
*writes*, not reads, so field order is what protects an old binary from silently losing
dependencies it can't parse. **High, resolved by a real architectural correction**: the per-file
schema marker moves from `MCPSurfaceVersion` to `RequiredDataFormat` — a second, pre-existing axis
`internal/surface/format.go` documents as deliberately independent for exactly "is the data on
disk in the format I need," which is this PRD's actual question. This also motivated an adjacent,
broader operator decision: extend the release-tag convention (already gating major version to
`MCPSurfaceVersion`) to also gate minor version to `RequiredDataFormat`. Also corrected: the
`StaleParents` gap was overstated — `vp tasks` already surfaces it in its `PROBLEMS` section; only
the dedicated check dimension and repair mechanism are actually missing. **Left open**: the
three-bucket (Active/Icebox/History) classification rule is genuinely ambiguous for an epic whose
own status is non-terminal but every child is terminal-or-icebox; a priority-ordered tie-breaker
is proposed and under discussion, not yet confirmed.

**v9 → v10:** The three-bucket tie-breaker resolved by rejecting the children-inspecting option
entirely, on a general principle the operator named explicitly: **the principle of least
astonishment** — a mutation should only ever have visible consequences on the record it was
actually made to. Icebox-ing a child task must not silently change how a *different*, untouched
task (its epic) is classified in the report. Bucket assignment is therefore a pure function of the
entity's own `Status` field alone, exactly mirroring the already-agreed History rule (§7.5) rather
than special-casing Icebox with child-aware logic. A broader sweep against this same principle
found the rest of the design already consistent: `StaleParents` is a deliberate anomaly *warning*
(not a silent reclassification) and stands as designed; the pre-existing `Parent`→`IsEpic()`
derivation is the same shape of thing but is foundational, pre-existing architecture well outside
this PRD's scope, not something this feature touches. No open questions remain; this PRD is ready
to become an epic.
