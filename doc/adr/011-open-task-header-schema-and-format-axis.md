# ADR 011: Open Task-Header Schema and the RequiredDataFormat/Release-Versioning Coupling

**Status:** Proposed (2026-09-15)
**Deciders:** Project owner
**Context:** Task-header schema extensibility, the widened task-status lifecycle, and the data-format/release-tag versioning coupling this opens up

## Context

`vp tasks`/`vp board`-shaped reporting (epic `task-epic-board-reporting`) needs two new pieces of
per-task data that do not exist today: a trustworthy `CreateTime`/`ModTime`, and a widened
task-status vocabulary (`planning`/`reviewed` added; `pending`→`planning`, `retired`→`done`
renamed). Both land in the on-disk task-file header, and both are permanent, vault-wide,
forever-format changes — the same class of change this project has a standing precedent for
documenting (ADR-003, vault write locking).

**The header parser is a closed four-field list today, not an open schema.** Confirmed against
source (epic `## 3`): `internal/storage/tasks.go`'s field-name constant block says plainly "A
task's metadata header is a contiguous run of `"**Field:** value"` lines, and these are the only
fields in it" (`tasks.go:1449-1456`), and `isHeaderFieldLine` (`tasks.go:1476-1485`) recognizes
exactly `Status`/`Priority`/`Parent`/`Depends`. `headerBlock` (`tasks.go:1544-1560`) stops scanning
the header at the first line that isn't one of those four — by design, and for a real reason
documented at the same site: `Parent`/`Depends` are *optional*, so a whole-file scan can't safely
use "first occurrence wins" the way the two required fields do (a body paragraph that happens to
resemble a dependency line would otherwise be read as real metadata). The header block's
contiguous-run termination is what makes this safe today, and it is exactly what a naive "just add
two more recognized field names" change would silently break for any field placed ahead of the two
optional ones.

**This project has a direct precedent for what changing "what gets written into every task file"
costs**: an `MCPSurfaceVersion` bump. Adding new header fields and new status values is the same
class of write-shape change as the existing 2→3 bump (epic `## 3`), and needs the same treatment —
an old binary must refuse to *write* into a vault a newer binary has already touched.

Full source-verified reasoning trail this ADR formalizes: `task-epic-board-reporting` `## 3`
(Current State) and `## 4.1` (Data model: `CreateTime`, `ModTime`, and a widened status
vocabulary). This ADR does not re-derive that trail; it cites and formalizes it.

## Decision

**1. The header parser becomes open, not just wider.** Any well-formed `**Field:** value` line is
recognized as header metadata, whether or not the current binary knows what it means. The new
contract, precisely:

- An unrecognized `**Field:** value` line is preserved **verbatim** on any rewrite — never dropped,
  never used to terminate the header block early.
- New fields (`CreateTime`, `ModTime`, and the per-file schema marker below) are appended
  **strictly after** the existing optional fields, never inserted before them. This is a real
  constraint, not a style preference: the surface gate (`MCPSurfaceVersion`) blocks an old binary's
  *write* against a newer vault, but it does not gate *reads* — `internal/surface/format.go`'s doc
  comment frames the surface axis specifically as a write hazard (contrasted against the
  data-format axis's read hazard, quoted in full in Decision 3), and `internal/surface/gate.go`'s
  `EnforceFailStop`/`EnforceWarnOnly` doc comments independently confirm the surface gate is
  invoked only from write entry points, never from reads. An old binary can still open a migrated
  file, and today's `headerBlock` stops at the first line it doesn't recognize; a new field placed
  *before* the two optional fields would make an old reader silently lose them — no error, just a
  task that looks dependency-free. Appending costs nothing and avoids this outright.

**Header-block termination rule under the open schema, stated precisely.** `headerBlock`
(`tasks.go:1544-1560`) does not special-case a blank line: it consumes a contiguous run of lines
from the top of the file (after skipping any leading blank lines following the H1) for exactly as
long as each line individually satisfies `isHeaderFieldLine`, and stops at the first line that
doesn't — whether that line is blank, prose, or a heading. Opening the schema changes only what
`isHeaderFieldLine` accepts (any well-formed `**Field:** value` line, not one of four fixed names);
the termination *mechanism* — stop at the first line that isn't itself field-shaped — is unchanged,
and is the correct, backward-safe choice. The alternative (switch to "terminate at the first blank
line" instead) would be strictly riskier: it would swallow any intervening non-field-shaped text
ahead of a blank line into the header, rather than excluding it the moment it stops looking like a
field, the way the current mechanism already does.

This generalization does carry one real, non-hypothetical risk, flagged rather than dismissed:
today, a body line directly beneath the last recognized field, with no blank-line separator, that
happens to have a bold-key-colon-value shape (e.g. a stray `**Note:** see below`) is correctly
excluded from the header, because it isn't one of the four recognized names. Under the open schema
it would instead be absorbed into the header block and preserved verbatim as an unrecognized field
— a reclassification of existing content, not merely an addition. **Forward-looking requirement on
`board-reporting-open-header-schema` as a pre-merge check** (epic `## 4.1d`): before the
open-schema parser ships — as part of that task itself, not the later vault-wide rename sweep
(`board-reporting-one-time-migration`, which depends on the surface/format version bump and runs
afterward) — run a corpus check across every existing task file in the vault for exactly this
shape — a non-blank, bold-key-colon-value-shaped line sitting immediately after the header with no
blank-line separator before it — and handle any hit explicitly (most likely by inserting the
missing blank line) rather than discovering the reclassification after the fact.

**2. The task-status vocabulary widens and two values rename**, as part of one migration sweep:
the old "not started" value renames to `planning`, and the old terminal-archive value renames to
`done` (matching the directory it already lives in); a new value, `reviewed` (plan accepted, not
yet started), joins the existing `in_progress`/`blocked`/`icebox`/`cancelled`. Full rationale and
the complete list of call sites this must touch (including the live migration command that would
otherwise silently revert the rename) is in epic `## 4.1` point 5 and `## 8` task 2; this ADR does
not repeat that site list.

**3. The two version axes, and why this feature uses `RequiredDataFormat`, not
`MCPSurfaceVersion`.** This project has exactly two version axes, and they answer different
questions by design. `internal/surface/format.go`'s own doc comment states this explicitly — quoted
verbatim, because the independence it asserts is the crux of this decision:

> The vault DATA-FORMAT axis is a second, orthogonal version mechanism to the
> .surface tool-surface axis (Stamp/CheckCompatible). The two answer different
> questions and MUST stay independent:
>
>   - .surface (surface = N): is the reading binary at least as new as the one
>     that last WROTE this vault? Absence-means-pass; fires when the vault is
>     AHEAD of the binary; a WRITE hazard.
>   - vault.toml (format = N): is the DATA on disk written in the old encoding
>     or the new one? Absence-means-format-0 (unmigrated, a POSITIVE signal);
>     fires when the vault is BEHIND the binary; a READ hazard.

`MCPSurfaceVersion` (`.surface`) answers "is the binary new enough to write here" — a write hazard,
already gating `CreateTask`-shape changes via the existing 2→3 precedent, and it is what this
feature's new fields/values need bumped for the same reason. `RequiredDataFormat` (`vault.toml`)
answers "is the data on disk in the format I need" — a read hazard, and the *actual* question this
feature needs answered for reporting: whether `vp board` can trust a given file's `CreateTime`/
`ModTime`/widened status values, independent of whether the reading binary is new enough to write
there. The per-file schema marker this feature adds (recording the `RequiredDataFormat` a file was
migrated under, for cheap lookup by future tooling such as a Jira importer) belongs on this axis,
not the write axis.

**This is not "either/or" — both axes bump for this feature.** `MCPSurfaceVersion` bumps because
the *write* shape changes (new fields, new status values, appended after the existing optional
fields). `RequiredDataFormat` bumps to `2` because the *data*, once migrated, becomes trustworthy
for board reporting in a way it wasn't before — a genuinely different axis answering a genuinely
different question, and conflating them (as an earlier revision of the epic's own PRD did, before a
second independent review caught it — epic `## 9`, v8→v9) would couple a read-trust signal to a
write-permission signal that has no reason to track it.

**4. Backward-compatibility contract for an old binary reading a post-migration file.** Stated
plainly, as a requirement, not an assumption:

- An old binary must **not** corrupt or misread the existing optional header fields on a migrated
  file. This is guaranteed by the field-ordering rule in decision 1 (new fields always append after
  them), not by the old binary understanding anything new.
- An old binary **is** expected to ignore fields it doesn't recognize (the open-schema contract in
  decision 1 makes this the natural behavior, not a special case it must implement).
- An old binary is **not** expected to understand the widened status vocabulary, and is not
  expected to trust `CreateTime`/`ModTime` as meaningful — that trust question is exactly what the
  `RequiredDataFormat` read gate exists to answer for any caller (old or new) that cares, via
  `EnforceFormatFailStop`.

**5. The release-versioning extension covers three independent enforcement sites, not one.**
`MCPSurfaceVersion`'s major-version gate against a release tag is implemented independently in
three places today — confirmed by reading each:

- `.github/workflows/release.yml:21-34` — the CI guard (`Guard tag major against
  MCPSurfaceVersion`), the canonical enforcement point; a pushed tag whose major disagrees with
  `MCPSurfaceVersion` fails the release job.
- `Makefile:8-11,247-253` — `SURFACE_MAJOR`, derived the same way from `internal/surface/version.go`,
  checked by the `release` target as, per the Makefile's own comment, "a local convenience only":
  "The release.yml CI guard re-derives the same value from a pushed tag and refuses a mismatch;
  this copy is for local `make release`."
- `internal/check/release_version.go:32-92` — `CheckReleaseVersion()`, exposed via `vp check
  --check release-version`, which compares an already-built, already-running binary's stamped
  version major against `MCPSurfaceVersion` (catching a stale binary that predates a surface bump,
  independent of any tag-time check).

All three exist specifically to mirror each other, and this ADR requires all three to move
together: each gets a sibling minor-version check against `RequiredDataFormat`, using the same
parse-and-compare shape it already uses for major, giving
`<MCPSurfaceVersion>.<RequiredDataFormat>.<build>` as the project's release-tag convention going
forward. Landing the minor-version check at only one site (most likely `release.yml`, since it's
the one already named in the epic) would leave the other two silently accepting a tag whose minor
component disagrees with `RequiredDataFormat` — the same class of incomplete-site-list mistake the
epic's own v8→v9 revision caught and fixed for the `Status` rename (epic `## 8` task 2). **The
sibling task `board-reporting-surface-and-format-version-bump` inherits this three-site scope from
this ADR** — it is not free to treat `release.yml` alone as the deliverable.

**This supersedes, and requires updating, `internal/surface/version.go`'s own stated tag invariant
— a fourth site, documentation rather than enforcement.** `version.go:30-38`'s doc comment on
`MCPSurfaceVersion` currently states, as of "the versioning task filed 2026-09-12": "a bump here
means the next cut git tag must be `v<N>.0.0`" — minor and patch pinned at zero. This ADR replaces
that specific invariant with `v<MCPSurfaceVersion>.<RequiredDataFormat>.<build>`; the doc comment
must be rewritten to say so when this ADR's decision lands, or it becomes a stale, actively
misleading claim sitting next to the constant it documents.

**`<build>` is defined as a plain incrementing counter, not tied to either axis**: it distinguishes
multiple release cuts made at the same `(MCPSurfaceVersion, RequiredDataFormat)` pair (a patch
release, a re-cut after a failed release job, etc.) and carries no independent versioning semantics
of its own — it is *not* a third orthogonal axis, just a tie-breaker within one.

**The existing `v5.0.0` tag predates this convention and is grandfathered, not a violation.** It
was cut under the prior `v<N>.0.0` rule, when `RequiredDataFormat` was already `1` (armed at 1 for
the KG-triple-filename migration, per `internal/surface/format.go`) — so under the new rule it
would read as `v5.1.0`. This ADR's versioning scheme is prospective only, applying to tags cut
after this decision lands; a future reader diffing `v5.0.0` against the new rule should read the
mismatch as *predates the rule*, not as a violation of it.

## Consequences

**Positive:**

- The header format is open for good: a future Jira-style importer/exporter, or any other field
  this project adds later, no longer needs its own `MCPSurfaceVersion`-plus-migration ADR just to
  be preserved through a rewrite by an old binary.
- The two version axes stay independent and legible: a reader can ask "can I write here" and "can
  I trust this data" as two separate questions, matching how `internal/surface/format.go` already
  documents them, rather than overloading one counter to answer both.
- The release tag itself becomes self-describing for both hazards a consumer of a tagged binary
  might care about.

**Negative / trade-offs:**

- Every mutating task action stamps `ModTime`, and task creation stamps both `CreateTime` and
  `ModTime` — more server-side write surface than the four-field header had, though the open-schema
  contract in decision 1 means none of it is a breaking change for a reader.
- Two independent version numbers must be bumped correctly for this one feature
  (`MCPSurfaceVersion` and `RequiredDataFormat`), and the release pipeline must be extended in
  lockstep across all three enforcement sites plus the `version.go` doc comment (decision 5) or the
  tag stops accurately describing the vault it was cut against and at least one site silently
  reverts to checking major only.
- The open-schema header-block termination rule (decision 1) needs a one-time corpus check across
  the existing vault for the bold-key-shaped-line-with-no-blank-line edge case before it ships, or a
  rare existing task file could have body content silently reclassified as header metadata.
- `ModTime`-as-completion-date is best-effort, not a hard invariant (epic `## 4.1` point 3: archived
  tasks are not actually frozen at the storage layer, since the existing migrate-task-status/
  migrate-task-header commands can and do rewrite an archived header) — accepted, with git history
  as the fallback, not fixed by this ADR.

## Alternatives considered

- **Couple the per-file schema marker to `MCPSurfaceVersion` instead of `RequiredDataFormat`.**
  Rejected (epic `## 3`, revised after a second independent review): this conflates a write hazard
  ("is the binary new enough to write here") with a read hazard ("is the data on disk in the format
  I need"), which is exactly the independence `internal/surface/format.go`'s doc comment says the
  two axes must preserve.
- **Add the two new fields to the existing closed four-field list without opening the schema.**
  Rejected: `isHeaderFieldLine`/`headerBlock`'s contiguous-run termination means any field an old
  reader doesn't recognize ends the header block early; if such a field were ever written ahead of
  the two optional fields, an old reader would silently lose them. Opening the schema and appending
  new fields strictly after them is the only design that keeps that failure impossible by
  construction rather than by convention.
- **Skip the `RequiredDataFormat` bump and rely on `MCPSurfaceVersion` alone to gate trust in the
  new fields.** Rejected: `MCPSurfaceVersion` gates writes, not reads (documented explicitly in
  `internal/surface/format.go`'s doc comment and confirmed by `gate.go`'s write-only call sites), so
  it says nothing about whether *already-written* data on disk is in the migrated shape. `vp board`
  needs a read-side answer, which only `RequiredDataFormat` provides.
- **Terminate the header block at the first blank line, instead of the first non-field-shaped
  line.** Rejected (decision 1): this would swallow any non-field-shaped prose ahead of a blank
  line into the header, which is strictly worse than the existing mechanism's behavior of excluding
  a line the moment it stops looking like a field.

## References

- Source-verified reasoning trail this ADR formalizes: `task-epic-board-reporting` `## 3` (Current
  State) and `## 4.1` (Data model: `CreateTime`, `ModTime`, and a widened status vocabulary).
- Header parser: `internal/storage/tasks.go` (`isHeaderFieldLine`, `headerBlock`,
  `headerFieldValue`, lines ~1449-1560).
- Version axes: `internal/surface/version.go` (`MCPSurfaceVersion` and its `v<N>.0.0` tag-invariant
  doc comment, `:30-38`, superseded by this ADR); `internal/surface/gate.go`
  (`EnforceFailStop`/`EnforceWarnOnly`, confirming the surface gate fires only on writes);
  `internal/surface/format.go` (`RequiredDataFormat`, `ReadFormat`/`WriteFormat`,
  `EnforceFormatFailStop`, the read-hazard gate, and the doc comment quoted in Decision 3 that
  frames the surface axis as the write hazard).
- Release-tag gates to extend (three independent sites, decision 5):
  `.github/workflows/release.yml:21-34` ("Guard tag major against MCPSurfaceVersion" CI step);
  `Makefile:8-11,247-253` (`SURFACE_MAJOR`, the `release` target's local check);
  `internal/check/release_version.go:32-92` (`CheckReleaseVersion`, `vp check --check
  release-version`).
- Precedent for this ADR's shape and numbering: `doc/adr/003-vault-write-locking.md`.
