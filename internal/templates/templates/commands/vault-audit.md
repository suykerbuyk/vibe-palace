# Vault Audit — verify REALITY against design intent

Audit the whole vault. **The mechanical part is already code** — run it, read it, then do
the part code cannot do.

**This command is NOT the checks.** `vp audit vault` (CLI) and `vp_audit_vault` (MCP) run
every mechanical dimension: transcript round-trips, project-tree coherence, KG
portability, resume discipline, iteration headings. Your job is **Layer 2** — the
adversarial posture, the invariant nobody has written yet.

---

## Step 1: Run the mechanical audit

Call `vp_audit_vault` (or run `vp audit vault`). It is vault-global; it takes no project
parameter, because the thing being audited is the vault.

Read the result. For each dimension:

- **`pass`** — no NEW drift. **This is not the same as CLEAN.** A dimension with 105
  accepted findings and 0 new ones passes. Read the `accepted` count, always.
- **`fail`** — either NEW drift, or a **STALE** baseline entry (something was fixed and
  the record was not updated). Both matter. A stale entry means the baseline is decaying
  into a lie.
- **`unknown`** — **the auditor could not read something.** This is NOT a pass. Chase it
  down or say plainly that you could not.

### Remediate what is RECOVERABLE — but only on human approval

The `archive-roundtrip` dimension does more than flag stranded transcripts: it marks the
ones that are **mechanically recoverable** (a note carries the session id as a
caller-pushed `session_key`) and annotates the finding with the exact repair command.
List them read-only with `vp archive backfill`; each recoverable pair prints its own
`vp archive link <session-id> -p <project>`.

**Do NOT apply them yourself as part of the audit.** Running `vp archive link` (or
`vp_archive_link`) is a WRITE, and per ADR-007 running it *is* the human's approval for
that one pair — there is deliberately no bulk mode. Report the recoverable pairs to the
human and hand them the commands. A stranding that is *not* recoverable (the note
predates `session_key`, 199) is permanently lost; say so, and let the human decide
whether to `--accept` it as debt (Step 5). **The audit REPORTS and DEFERS; it does not
repair.**

---

## Step 2: The three method invariants — they govern everything below

**1. 🔴 ASK THE ARTIFACT, NOT THE CODE.**

Drive the real tools, read their **actual output**, compare against what is **on disk**.
Never audit by reading source and reasoning about what it *should* do. That is precisely
how the test suite missed `note_path` for six months, and how `mdfence` passed every
fixture while corrupting the real file.

*An audit command that audits by reading code is the bug it exists to catch, wearing a
lab coat.*

**2. 🔴 YOU MUST BE ABLE TO SAY "UNKNOWN".**

If you cannot read something, say `unknown` — never `pass`. *"I have no information" is
not "nothing is wrong."* This is the `vp_health` invariant applied to the auditor itself,
and it is the difference between an audit and a rubber stamp.

**3. 📏 RECORD THE GREP, NEVER THE COUNT.**

Every number you report carries the command that produced it, or it rots by the next run.
Every hand-recorded census in this project has rotted — *every single one*, usually within
one iteration.

---

## Step 3: The adversarial pass — what code cannot do

The dimensions catch what we already know is broken. **Go looking for what nobody has
written a check for yet.** Some angles that have paid off before:

- **A capability built, and nothing invokes it.** This project's single most repeated root
  cause. The Zed adapter, `vp check`, `vp_health`, the MCP-path claim sentinel — all built,
  all reaching nobody. `internal/sourceaudit` now catches the source-level cases; ask what
  it *cannot* see.
- **A field declared and never assigned.** `note_path` was empty on every note ever
  captured, for six months, with a green suite.
- **A number in a document that nothing verifies.** Counts rot. Find one and check it.
- **A comment that promises a mechanism.** `LinkSessionNote` had a comment saying a future
  hook run would call it. Nothing did.
- **Something the vault does that nobody asked for.** 36% of session notes are
  auto-captures nobody reads.
- **Two implementations of one rule**, one of them unreachable — they diverge silently.

---

## Step 4: 🔴 THE RATCHET — the most valuable output is not the report

**For every finding you made by JUDGMENT rather than by a check: WRITE THE CHECK.**

Name where it goes:

- a **`vaultaudit` dimension** (`internal/vaultaudit/dimensions.go`) — for anything about
  vault CONTENT;
- a **`sourceaudit` rule** (`internal/sourceaudit/`) — for anything about the SOURCE;
- a **test** — for anything about behaviour.

Then file the task, or write it now if it is small.

**A finding that does not become a check will be rediscovered from scratch next week**,
and in six months this audit will report PASS while something rots. That is precisely the
failure mode iterations 197–201 spent five sessions deleting.

**A ratchet-less audit is a rubber stamp with extra steps.**

---

## Step 5: Accepting debt (deliberately, and rarely)

If a finding is real but cannot be fixed now — immutable history, a migration that has not
landed — accept it:

    vp audit vault --accept

**Then give every UNTRIAGED dimension a REAL reason** in `Audits/baseline.json`: the
verdict, the evidence, and the task that owns the fix. An unexplained entry is
indistinguishable from an oversight, and in six months nobody will know which it was.

**THE BASELINE MAY ONLY SHRINK.** `--accept` exists for a first run, or for debt with a
named owner. It is **not** for silencing new drift. When the fix lands, the accepted
entries stop being findings, the audit reports them **STALE**, and the baseline is *forced*
to shrink — that is the mechanism, and it only works if you do not abuse the escape hatch.

---

## Step 6: Write the report

    vp audit vault --write

It lands in `Audits/<date>-vault-audit.md`, which `vault tidy` sweeps and commits — so
`git log -p Audits/` shows week-over-week **drift**. Summarize for the human: what is new,
what is stale, what is unknown, and **which checks you wrote**.

**Note the Bash dependency honestly.** This command drives the CLI and the filesystem. On a
host without a shell, run `vp_audit_vault` over MCP instead — every dimension pushed down
into code is one more that reaches every agent, which is the whole point of the ratchet.
