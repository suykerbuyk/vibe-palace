# The escalation document

When a `code-digger` run finds something that a human needs to act on **now**, the
issue register is the wrong vehicle. A 158-finding ledger is a reference work; you
cannot hand it to a busy engineer and expect the two things that matter to survive
contact.

The escalation doc is the **forwardable extract**: self-contained, short, and
written for someone who has never read any of your other output and never will.

## When to write one

When a finding is (a) Critical, (b) exploitable or actively losing data, and (c)
about something that is **shipping**. All three. A Critical in a component nobody
deploys goes in the register with a conditional grade and a question — not in an
escalation.

Be sparing. The escalation doc's power is that it is rare.

## The test it must pass

> **Could you forward this file, with no covering note, to an engineer who has
> never heard of your documentation project — and would they know what to do?**

That means: no references to your other docs, no internal jargon, no "as noted in
Part 5", no severity ids the reader would have to look up. It stands alone or it
fails.

## Structure

```
# Security escalation — <the two or three things, named plainly>

**Date:** <date>
**Raised by:** <who/what, and how — "read-only reconstruction; no builds run">
**Severity:** <one line: what grade, and what compounds it>
**Verification:** <how each claim was checked — by hand, against source, at what SHA>

## Why this is being escalated ahead of everything else
<3–6 sentences. The reader is deciding whether to keep reading. Give them the
blast radius, not the mechanism.>

## Finding 1 — <plain-language title>

**Severity: Critical.** <one sentence of consequence, in the reader's terms —
"the shipped image can accept a PUT, return 200, and persist nothing">

### The defect
<what the code does, citing path:line>

### Why it is exploitable, not merely weak
<the part that turns a theoretical concern into a real one. This section is what
distinguishes an escalation from a code-review comment. Do the work: if it is a
weak PRNG, show the seed's real entropy, the oracle that confirms it, and why the
draws are recoverable. If it is a data-loss window, show the exact interleaving.>

### The compounding High — <the thing that removes the mitigation>
<Criticals rarely stand alone. The credential is weak AND the API that hands it
out is unauthenticated. The fail-safe is compiled out AND the process runs as
root. Name the compounding factor: it is usually what turns "bad" into "urgent".>

### Suggested remediation
<concrete, minimal, and ordered. The one-line fix first, the structural fix
second. You are not writing the patch; you are making it obvious.>

## Finding 2 — ...
```

## Discipline

- **Every claim hand-verified, and say so in the header.** An escalation built on a
  subagent's summary is a career-limiting document. Read the lines yourself.
- **Lead with consequence, not mechanism.** "Customer S3 credentials are
  recoverable by construction" before "`math/rand` is seeded from
  `time.Now().UnixNano()`". The mechanism goes in the section below, for the
  engineer who has now decided to care.
- **Name the compounding High.** A Critical whose mitigation is intact is a
  Critical you have time to fix properly. A Critical whose mitigation was *also*
  removed is an incident. That distinction is the whole reason for the doc.
- **Do not editorialise about the team.** No "this should never have shipped." State
  what the code does and what it costs. The reader will draw the conclusion, and
  they will trust the document more for not having been told to.
- **Quote the code's own comments when they convict it.** A guard whose comment says
  the fallback "returns 200, persists nothing" tells the reader that someone knew,
  which is a fact about *risk*, not about blame.
- **Give the remediation.** A finding without a fix reads as criticism. A finding
  with a two-line fix reads as help, and gets acted on.

## After it is written

**A written escalation that nobody has read is not an escalation.** Track it as an
open thread in the state file — *"nothing in this register has been reported to
anyone"* — until a human has actually received it, and say so plainly in every
status summary until that changes.

Writing it is the easy half.
