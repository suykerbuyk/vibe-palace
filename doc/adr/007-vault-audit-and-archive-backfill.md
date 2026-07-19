# ADR 007: The Vault Audit and the Archive Backfill

**Status:** Accepted (2026-07-17)
**Deciders:** Project owner
**Context:** The `honest-instruments` epic — the system reports success for work it did not do — and its longest-lived specimen, `capture-note-archive-link-never-closes`

## Context

ADR-006 named three postures a rule can take (DERIVE / DECLARE+ENFORCE /
REPORT+DEFER) and one anti-posture (PROSE). It named `internal/vaultaudit` in its
"Related" list as the canonical **REPORT + DEFER** case and left it at that. This
ADR is the subsystem that reference points at: what the vault audit checks, why it
is advisory, how its accepted-debt baseline stays honest, and how a class of finding
it surfaces — a transcript stranded from its note — gets *remediated* without the
audit ever making the call itself.

The audit exists because of a specific, humiliating discovery. The note↔transcript
cross-link that ADR-001 described as a working bidirectional loop **had regressed at
the 2026-06-06 capture cutover and nobody noticed for six weeks.** 105 of 417
manifests vault-wide carried no back-link to any note; the *agent's own wrap note*
— the thing a human would actually want to reach from a transcript — was linked
**0 times in 61 tries** for implementation-tagged sessions. Every unit test in the
tree passed the whole time. Nothing in the system was positioned to see it, because
"is every transcript reachable from a note?" is a question about the vault as a
whole, and no per-session, per-project, or per-tool code path asks it.

That is the shape of every `honest-instruments` defect: a claim (the loop is closed)
that no instrument ever checked against the artifact. The audit is the instrument.

## Decision: the audit is advisory, vault-global, and its `unknown` is not a `pass`

`vp audit vault` (and the MCP `vp_audit_vault`) scans the whole vault against
design intent and reports **pass / fail / unknown per dimension**. Five dimensions
ship today (`internal/vaultaudit`):

| Dimension | Checks |
|---|---|
| `archive-roundtrip` | every transcript manifest back-links to a session note that exists on disk |
| `project-tree-coherence` | every project appears in BOTH `palace/` and `Projects/` (a one-tree project is drift) |
| `kg-portability` | KG triple filenames are representable on NTFS/exFAT (the Windows blocker) |
| `resume-discipline` | no `resume.md` exceeds the size cap |
| `iteration-headings` | `iterations.md` headers are canonical H2, so the counter derives correctly |

Three properties are load-bearing, and each is a scar:

- **It is ADVISORY. A FAIL exits 0.** The audit reports; it does not block. An audit
  that failed the build would be an audit people learn to disable, and a disabled
  instrument is worse than none — it issues a clean bill of health it never earned.
  Blocking is a REPORT+DEFER violation: the audit does not get to decide that a
  finding is worth stopping work over. The human does.
- **It is VAULT-GLOBAL and takes no `project` parameter.** Scoping it to whichever
  project the caller invoked from is exactly how a project nobody has opened in three
  months escapes scrutiny forever. (Its sibling `vp audit rooms` *is* per-project and
  takes `--project`; that audits a project's drawer classification, a different thing.
  The asymmetry is deliberate.)
- **`unknown` is carried separately from `pass`, and loudly.** A transcripts directory
  the auditor could not read is `unknown`, never `pass`. "I could not look" is not
  "there was nothing there" — the exact `vp_health` bug of 201, and the reason
  `auditArchiveRoundTrip` probes `os.ReadDir` before `archive.ListEntries` (whose
  underlying `filepath.Glob` swallows permission errors and would report an unreadable
  tree as empty).

**Where it sits on the ADR-006 line: REPORT + DEFER.** The audit surfaces facts and
names options; it never repairs. Its 19-projects-in-one-tree finding — index the
history, or delete the leftover store? — is ADR-006's own specimen of a call an
audit does not get to make.

## Decision: the accepted-debt baseline is a DECLARE channel that may only shrink

Known, owned debt lives in `Audits/baseline.json`. A finding whose `(Dimension,
Artifact)` pair is accepted there is reported as **accepted**, not **new**, and does
not fail the dimension. This is not a suppression list — it is the artifact
**declaring an intent the code cannot express**, the same shape as ADR-006's
`vp:pin` marker and `internal/sourceaudit`'s baseline reasons. Every accepted entry
carries a `reason` naming why the debt is tolerated and which task owns paying it
down.

The property that keeps it honest: **fixing the bug FORCES the baseline to shrink.**
When a backfill links a stranded manifest that was an accepted entry, that entry is
no longer a finding, so it becomes **STALE** — and a stale accepted entry **fails the
dimension exactly as loudly as a new finding.** The baseline may only shrink; it can
never quietly grow to cover new drift. Finding identity is `(Dimension, Artifact)`
and nothing else — the human-readable `Detail` message is never part of the key — so
a finding's message can be annotated freely (see below) without perturbing a single
accepted entry.

**The staleness nag** (`internal/vaultaudit/staleness.go`) rides in
`vp_bootstrap_context` and is **silent when fresh**: it trips only on churn (~50
notes vault-wide since the last audit) or age (7 days). It is a REPORT that costs
nothing when there is nothing to say — the discipline every bootstrap alert must
follow, and the one the 209 version violated by reading a *missing* `session_notes`
anchor as **zero** and announcing a 634-note vault's entire history as one day's
churn. That bug is ADR-006's headline `ABSENCE IS NOT A VALUE` specimen.

## Decision: the archive backfill — REPORT the recoverable, DEFER the repair, DERIVE never-guess

The `archive-roundtrip` dimension does not only detect stranded manifests; it
distinguishes the **recoverable** ones and hands the human the exact command to fix
each. This is the full REPORT+DEFER loop made concrete, and every step of it was
constrained by ADR-006's asymmetry rule: **err DOWN the list — a wrong repair is a
silent lie; an unrepaired stranding is a visible one.**

**What is recoverable, and why it is a DERIVE and not a guess.** A note whose capture
key was PUSHED by the hook (`session_key_source: caller`) carries the harness session
id *as its `session_key`* — the hook passed `payload.SessionID`. So pairing that note
with a stranded manifest whose `session_id` matches is an **exact identity
derivation**, not a proximity heuristic. The candidate predicate, defined once in
`internal/vaultaudit/backfill.go` and used by all three surfaces (the audit
annotation, the CLI lister, the applier):

> a note N and manifest M pair iff, within one project:
> `N.session_key_source == "caller"` ∧ `N.session_key == M.session_id`
> ∧ `N.archive_session_id == ""` ∧ `M.vault_rel_session_note == ""`

**What is NOT recoverable stays stranded, honestly.** Every note predating the
`SessionKey` feature (199) never recorded which harness session produced it. That
association is **lost, not merely unwritten** — matching by timestamp proximity or
`git_head` is "a guess wearing the face of a measurement" and is refused. Those
manifests remain findings until a human accepts them as permanently-lost debt.

**The surfaces:**

- `vp archive backfill` — read-only. Lists each recoverable pair and prints the exact
  `vp archive link <session-id> -p <project>` that repairs it.
- `vp audit vault` — annotates a recoverable stranded finding's *message* (never its
  identity) with the same command.
- `vp archive link` / `vp_archive_link` — the applier. **Running it IS the human
  approval for that one pair.** There is deliberately no bulk mode: a `--all` flag
  would be the audit deciding to repair, which is the REPORT+DEFER line this ADR
  draws. Batching is a human running a loop, not a tool growing a flag.

Backfilled links carry provenance `archive_session_id_source: backfilled`, distinct
from the live path's `derived` (ADR-006's `clientinfo` derivation), so an audit or a
human can always tell a repaired link from one the server closed at capture time.
**Absence is not a value, and neither is a repair pretending to be a live link.**

### Two constraints the review found before a line was written

Both are ADR-006's asymmetry rule applied to this subsystem, and both are the kind of
silent-wrong-guess the whole epic exists to prevent:

- **The applier selects the target manifest by the PREDICATE, never by
  `archive.ResolveEntry`.** `ResolveEntry` resolves a multi-match session
  newest-`CapturedAt`-wins, *blind to link state*. A session with two stranded
  manifests (the PreCompact-then-SessionEnd shape) would have its note linked to a
  manifest the report never named, and the reported finding would never clear. The
  applier links the newest **stranded** manifest; older stranded siblings stay
  stranded by design and fall to the accepted-debt set. (Verified live: session
  `a8829e34` had two stranded manifests; the newer linked, the older stayed
  stranded.)
- **`--accept` is GATED on every host running the current binary.** Accepting the
  baseline is report-global — it accepts *everything currently found*. A stranding
  produced by a stale-binary host between the backfill and the accept would be
  silently baselined, and the accrual signal the baseline exists to keep loud would
  go quiet. Manifests carry `vp_version` and `captured_by_hostname` precisely so a
  post-fix stranding from another host is checkable before the accept.

## Consequences

**What this buys.** A claim the code makes about the vault — every transcript is
reachable — is now checked against the artifact on demand, and the check is honest
about what it could not see. Debt is a recorded, owned, shrinking fact instead of a
thing every session rediscovers. A stranded transcript that CAN be recovered is
recovered by a human command the audit hands them verbatim; one that cannot is left
visibly broken rather than papered over with a guess.

**What it costs.** The audit is another thing to run, and its staleness nag is another
bootstrap signal (the fourth; ADR-006's "revisit before adding a fifth" rule stands).
The backfill keeps a human in a per-pair loop they might have preferred to leave —
which is the point.

**The lesson, kept because it is the sharpest evidence.** The plan for the backfill
carried its **own confirmation criterion** — *"long sessions link, short ones strand;
if that hypothesis holds it confirms the fix"* — and the live vault **inverted it**
(linked median 200 KB, stranded 1.04 MB). A plan that carries its own confirmation
criterion can hand you a false witness. Then three of the plan's settled premises were
disproved by *reading the live vault before writing code*: the `PreCompact` hypothesis,
the "mechanically recoverable" backfill (only 19 of 118 were), and the claim that
matching by proximity was safe. **Ask the artifact, not the test, and not the plan.**
That is the discipline the audit institutionalizes: it is the standing instrument that
asks the artifact so a human does not have to remember to.

## Related

- `doc/adr/006-derive-dont-ask.md` — the three postures; this subsystem is its
  REPORT+DEFER (the audit) and DECLARE (the baseline) specimens made concrete.
- `doc/adr/001-transcript-archive.md` — the manifest format and the bidirectional
  link this audit checks and this backfill repairs. ADR-001 predates the link ever
  failing to close; this ADR is where that story is told.
- `doc/adr/003-vault-write-locking.md` — `storage.BackfillArchiveLink` holds the
  sessions-directory lock exactly as `LinkArchiveToSessions` does; the lock order is
  that ADR's.
- `internal/vaultaudit` — the audit, the baseline, the staleness nag, and the backfill
  predicate. `internal/storage.BackfillArchiveLink` — the applier's write path.
