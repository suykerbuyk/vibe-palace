# The issue register

The register is the audit half of `code-digger`'s output. It is **not** a bug
tracker and **not** a backlog — it is a **findings ledger**: what was seen, where,
how bad, and on what evidence.

It is written *by the orchestrator*, from the `## NEW ISSUES` blocks that workers
return. Never by a worker directly: a worker sees one component and cannot grade
severity against a whole product line, and cannot know what is already recorded.

## Structure

```
# Issues found in <the thing>

**<N> findings in <M> parts, <K> Critical.** IDs are unique across all parts
and never reused; each part is ordered by severity.

- **Part 1 — <scope>** (C1–L19). Read against <repo> @ <sha>, <date>.
- **Part 2 — <scope>** (C20–L30). Found <date> while doing <what>.
- ...

Legend: **C**ritical · **H**igh · **M**edium · **L**ow
```

Then one section per finding, severity-ordered within its part:

```
## C72 — iamd generates AWS secret access keys with math/rand

<what it is, 1–3 sentences>
<the evidence, citing path:line>
<why it matters — the exploit, the data loss, the blast radius>
<the fix, concretely>
```

## The conventions that make it usable

### IDs are global, sequential, and never reused

`C1`…`L19`, then `C20`…`L30`, then `C31`… — a single monotonic sequence across
every part, with the letter carrying the severity. **Not** per-part numbering.

Why: findings get cited across documents, in escalations, and in conversations with
humans, for months. `H32` must mean exactly one thing forever. Renumbering — or
reusing the id of a downgraded finding — silently corrupts every citation that
already exists.

The **severity letter is part of the id**, so a regrade changes the id. Accept it:
record the old id in the text (*"formerly M45"*) rather than pretending it never
moved.

### Parts are appended, never rewritten

Each new pass appends a **new part** with its own id range and a one-line statement
of what was read, at what SHA, on what date. Old parts are not reorganised when new
ones land. The register is a **ledger with a history**, not a tidy current-state
document, and its trustworthiness comes from the fact that you can see what was
known when.

### Every Critical is verified by hand

State it in the preamble, and mean it:

> *Every Critical and every security or data-integrity finding was verified by hand
> at the workspace root before being recorded — a subagent's summary is not a
> sufficient basis for an escalation.*

This is the sentence that makes the whole document credible. Do not write it unless
it is true.

### The conditional grade

Some defects' severity depends on a fact the source tree cannot answer — most often
**"does anyone actually deploy this?"**

Do not guess, and do not split the difference. Record the **conditional**:

> *"**High** — tape writes are never forced to medium and the only durable copy is
> then deleted. **→ Critical if anyone deploys `tape-manager`**, which nothing in
> this workspace can establish."*

and put the question to a human, explicitly, in the state file's open threads.

This is more honest than either alternative, and — importantly — it is *actionable*:
it tells the reader exactly what one sentence from them would change.

### A finding is about the code you read, not the family it belongs to

Before generalising a defect across a product line, **check the siblings.** The
origin run found a tape-durability bug in the Rust implementation and nearly
reported it against the whole tape stack; the shipped Go implementation does the
same operation **correctly**, deliberately, with a test proving it.

Where a sibling is clean, **say so in the finding** — it protects the reader from
over-reading, and it is exactly the detail that earns an engineer's trust.

### Absence is a finding

No auth on a data plane. No TLS. No `SYNCHRONIZE CACHE` anywhere in the repo. No
tests run by CI.

None of these appear in any diff, so nobody has ever reviewed them. They are
frequently the most serious things in the register, and they are only findable by a
reader who is explicitly asking *"what is missing?"*

### Duplicated implementations are a finding

Two or more places that implement the same responsibility — the same write primitive, the
same decision table, the same parser, the same validation or auth check — are a **fragility
finding**, even when each copy is individually correct today. A fix, or a security patch,
applied to one copy silently leaves the siblings wrong, and the copies drift apart over time
until they disagree about the same rule.

Record it as **one** finding: name every parallel implementation with its `path:line`, state
the single responsibility they share, and propose the consolidation. The most dangerous form
is a **central primitive that private copies bypass** — those copies also skip whatever the
primitive centralises (a lock, an fsync, a stamp, an auth check), so the duplication and an
*absence* finding are the same defect wearing two hats.

Do not confuse this with the sibling-check rule above: there, a sibling that does the same
thing *correctly* protects you from over-reporting one copy's bug across the family; here, the
existence of siblings that do the same thing *at all* is itself the risk, because the
duplication — not any one copy's current behaviour — is what will bite when one is changed.

### Do not inflate

Every finding you overstate costs the reader trust in the ones you got right. A
register that cries Critical is a register nobody reads. If the severity is
arguable, argue it in the text and pick the lower grade.

## What goes at the end

A section recording **what was actually fixed** — and where. In a read-only
reconstruction that list is usually short and lives entirely at the workspace root;
saying so plainly prevents a reader from assuming any of it reached upstream.
