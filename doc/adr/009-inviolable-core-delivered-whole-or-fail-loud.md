# ADR 009: Deliver the Inviolable Core Whole, or Fail Loud — Never Silently Truncate Operating Instructions or Active State

**Status:** §1–§2 Accepted (2026-07-21); **§3 WITHDRAWN 2026-08-15 — see the
amendment immediately below, and read it before anything else on this page.**
Partially enforced 2026-07-22
(`e52cfe1`: honest over-budget verdict computed on the final payload +
`budget.shed_core` core-tier report; `cffa14f`: advisory workflow-caps check).
The fail-loud arm remains gated on the ADR-008 rollout
(task `adr-009-arm-fail-loud-bootstrap`; see Consequences). **Still gated after
the ADR-008 Phase 1 rollout landed 2026-07-25 (iteration 257):** that rollout cut
the pinned core 73% but a real bootstrap still reports
`shed_core: ["resume->pinned"]`, because the full `resume.md` (32,586 bytes,
~8,146 tokens) is larger than the entire 8,000-token budget by itself. Arming the
gate now would hard-fail every vibe-palace bootstrap. Clearing it needs the
resume shrink in ADR-008 Phases 2–4, not another rollout pass.

---

## 🔴 AMENDED 2026-08-15 (iteration 291) — §3 IS WITHDRAWN; THE BUDGET IT DEFENDS NO LONGER EXISTS

**Superseding authority: `doc/PRD-vibe-palace.md` §1.10 (operator ruling, 2026-08-15).**
There is no numeric ceiling on a session-start payload. A payload is small because it is
an index (PRD §1.9), not because a number forces it to be. The token budget survives with
a different subject: it measures **one iteration**, and an over-budget iteration is a
warning that too much happened between one `/vpc-capture` and the next `/vpc-wrap`.

So §3 — *"The budget stays binding — the remedy is a smaller core, never a bigger budget"* —
is withdrawn in full. So is the iteration-261 ruling that contradicted it (*"the contract
sets the budget, not the reverse"*). **Both were live simultaneously, in flat opposition,
and an entire epic was built on the later one.** With no payload budget, neither has a
subject to be right about.

### What is NOT withdrawn

**§1 and §2 stand as concerns.** An agent that receives a degraded-but-plausible payload
and never learns a rule is missing is still the failure to prevent, and the 2026-07-21
Grok incident that motivated this ADR — a shed payload, a resume fetched out of band, and
a **disproven** diagnosis repeated from it — is still the canonical specimen. What changed
is the remedy: not a gate over a shed ladder, but not shipping the bulk in the first place.

### §3 was right about the diagnosis and we did the forbidden thing anyway

This is the part worth keeping, because the ADR called its own failure and was not read:

> *"a bloated instruction surface is itself a correctness hazard, because an agent handed
> tens of KB of rules skims them … the fix is to shrink the inviolable core — **move
> enforcement out of prose and into the server (ADR-006)**, and move the generic
> instruction manual out of vault files and into the binary (ADR-008)."*

**ADR-008 shipped. ADR-006's move never happened at scale.** Nine `check.Producers` exist
against roughly fourteen "NEVER do X" rules still carried as prose — and at least one of
them (*never write an absolute vault path into a vault document*) is enforced by the
`vault-abs-paths` check **and** still shipped as a paragraph, so the project pays twice.
Five days after this ADR was accepted, iteration 260 raised
`DefaultBootstrapMaxTokens` 8,000 → 16,000: precisely the move §3 forbids, made because
the core had not been shrunk the way §3 prescribed.

The prescription was correct. It is now `first-principles` Phase 1, and it is the gate on
everything else in that epic.

### §2's transport clause is unimplementable as written

> *"Where a transport physically cannot carry the inviolable core (a hard channel byte
> cap), the correct behavior is the same: fail loud and halt."*

**vp cannot see the transport.** It cannot detect a host inline cap, so it cannot halt on
one. The working substitute is the agent-side `complete` sentinel (`43e79ec`): last field,
no `omitempty`, always true — its **absence** proves the transport cut the payload.
Verified firing correctly on a truncated payload 2026-08-15, on a host that delivered
2,002 of 59,622 bytes and said nothing.

### Consequences of this amendment

- `adr-009-arm-fail-loud-bootstrap` is **cancelled** (2026-08-15). It would have armed a
  gate over a shed ladder scheduled for deletion.
- `workflow-digest-core-shed-makes-adr-009-arming-unclearable` is **cancelled** for the
  same reason — the mechanism it reported on is going.
- The shed ladder, `budget.shed_core`, the tier derivations and the `vp:pin` /
  `vp:disposable` marker apparatus are scheduled for removal in `first-principles`
  Phase 2. When they go, the *Consequences* section at the foot of this page describes
  machinery that no longer exists; leave it as the record of what was tried.

**Everything below this block predates the withdrawal.** It is accurate about its own
moment and is retained because the reasoning is instructive — particularly the 262
amendment, whose lesson (*the tier follows the artifact, so it must be READ from the
artifact, not copied into code*) generalises well beyond the mechanism it was written for.

---

**🔴 SUPERSEDED 2026-07-26 (iteration 260) — the gating rationale above no longer
holds, and the root cause was not the resume.** The blocker was diagnosed as "the
core is too big"; measurement said the BUDGET was too small. The core
(resume 18,577 B + workflow 13,482 B ≈ 8,015 tokens) exceeded the 8,000-token
budget *by itself*, so this ADR and that budget were **arithmetically
unsatisfiable** — no amount of shrinking short of gutting the contract could have
armed the gate. Three changes resolved it:

1. **The resume's pin boundary was drawn backwards.** Only three IDENTITY
   sections were pinned while Current State, Open Threads and Known Issues —
   the live hazards this ADR cites as the reason `resume->pinned` is core — sat
   un-pinned and were therefore the first thing spent. All three are now pinned,
   and the three hand-maintained history indexes were deleted (derivable from
   `iterations.md` and `tasks/done|cancelled`). resume.md: 27,602 → 18,577 B.
2. **`DefaultBootstrapMaxTokens` 8,000 → 16,000**, sized from the measured
   11,721-token everything-inline payload plus one growth cycle of slack. The
   contract was NOT shrunk: it sets the budget, not the reverse.
3. **`resume->pinned` was reclassified `shedTierContext`** — its un-pinned
   remainder is now a navigation table, so shedding it is no longer a core loss.
   The tier follows the artifact; if a resume ever un-pins live state again, it
   must go back to core.

A live bootstrap now sheds **nothing at all** (payload 11,721 / 16,000; `budget`
reports nil). **The stated gate on `adr-009-arm-fail-loud-bootstrap` — "arming it
would hard-fail every vibe-palace bootstrap" — is therefore no longer true for
this project**, and that task should be re-evaluated on current measurement
rather than on this paragraph. Note it remains a vault-wide question: the
`core-floor` check reports quantum-ng at 70.4 KB of core (resume 63.3 KB), so
arming globally would still fail there.

**🔴 AMENDED 2026-07-27 (iteration 262) — point 3 above was right about the
document and wrong about where it wrote the answer.** `resume->pinned` is no
longer classified by a constant at all; its tier is **derived per bootstrap**
from the resume being served (`resumeRungTier`, `internal/tools/context_tools.go`):

> Shedding the resume is a **CORE** loss unless **every un-pinned section of that
> project's resume is positively declared disposable** (`<!-- vp:disposable -->`).
> A section carrying neither marker is LIVE STATE, so dropping it is a core loss
> and `budget.shed_core` says so. Anything the derivation cannot rule on — no
> resume resolved, no H2 sections, no declared pin zone — reports **core**: absence
> is not a value, and the direction of a wrong guess is chosen rather than left to
> a zero value.

**Why the static classification was wrong, and it was not the verdict.** The 260
reasoning was sound and its evidence was real — vibe-palace's live-state and
live-hazard sections had just been pinned, so its un-pinned remainder genuinely
was a navigation table. What was wrong was the SHAPE of the answer: **a claim
about the contents of one document, on one day, encoded as a project-agnostic
constant.** That made one project's editorial state the reported tier for every
project in every vault. The map even carried its own remedy — *"if a future
resume ever un-pins live state again, this must go back to core"* — addressed to
nobody and checked by nothing, which is the rule-with-no-reader failure this
whole epic is named after.

Measurement settled it. Once `check.CheckPinCoverage` (iteration 262) could read
pin markers vault-wide, the constant was **false for 8 of 8 projects** in the
live vault: every one had undeclared live sections, and the shed ladder was
dropping them on the exposed ones while `shed_core` reported nothing. A live run
on 2026-07-27 confirms both directions — quantum-ng (core 76.9 KB, over the
56,000 B floor; five undeclared sections: *Current State, Project History,
Completed Plans, Open Threads, Reference Documents*) sheds its resume and now
reports `shed_core: ["resume->pinned"]` where it reported none; vibe-palace, which
sheds nothing at all, still reports `budget: null`. Note vibe-palace's own resume
is undeclared too, on one section (*Reference Documents* — the very section the
shipped template marks `<!-- vp:disposable -->`, left unmarked in the live file),
so the erring-downward rule would call its rung core the day its core crosses the
floor. That is the rule working, and the remedy is one marker line.

The general rule this ADR should have followed from the start: **the tier follows
the artifact, so it must be READ from the artifact, not copied out of it into
code.** The same discipline `internal/resumezone` exists to enforce — one
fence-aware reader of the markers, shared by the ladder, the advisory and now the
tier — applies to the classification too.

**Scope: this is REPORTING, not enforcement.** The fail-loud arm stays gated
(`adr-009-arm-fail-loud-bootstrap`). The ladder sheds the same rungs in the same
order and the payload an agent receives is byte-identical; only `budget.shed_core`
changes. The un-armed `workflow->excerpt` restore in `shedToBudget` is likewise
unchanged — an under-declared `resume->pinned` is now a genuine, *announced*,
un-restored core loss, and the remedy available today is to rule on the named
sections (`vp check --check pin-coverage` lists them by heading), not to alter the
payload.

**Deciders:** Project owner
**Context:** The bootstrap shed ladder trims the payload to a token budget by dropping rungs in order — and today the operating contract (`workflow`) and active project state (`resume`'s un-pinned zone, the active task list) are ordinary rungs. A shed that fits the budget by dropping them returns **success**, so an agent about to change code can receive a degraded-but-plausible payload and never know a rule or a hazard is missing.

## Context

`vp_bootstrap_context` assembles the session-start payload and, when it exceeds
`max_tokens` (default 8000), sheds rungs in a fixed order
(`internal/tools/context_tools.go`):

```
recent_sessions → memory → kg_snapshot → commands+skills → resume->pinned → active_tasks → workflow->excerpt
```

The instrument is honest about *having* shed — `budget.shed` names each rung
used, and `over_budget` is deliberately never silent (it raises an alert in
`post_bootstrap_instructions` and a WARN in `vp.log`). That was the fix for the
earlier fully-silent shed loop. But two rungs in that ladder are not context —
they are **safety surface**:

- `resume->pinned` drops resume's un-pinned zone, which today still carries
  **Open Threads and active Known Issues** — live project state and live hazards.
- `workflow->excerpt` truncates the **operating contract itself** to an excerpt
  behind a "read the URI before acting" banner.

And `over_budget` only fires when the ladder runs *out* of rungs and is *still*
over. A shed that **succeeds** at fitting the budget by dropping `resume->pinned`
or excerpting `workflow` reports the loss in `budget.shed` and otherwise returns
a normal, successful payload. Nothing treats the missing rule or hazard as a
correctness problem; the agent is not halted.

This is not hypothetical. On 2026-07-21 a Grok session's payload was shed, it
fetched the full resume out of band, and it repeated a **disproven** hook
diagnosis it read there — because the correction lived in a zone the budget
treats as optional. The failure mode is general: an agent that never sees a
"never do X" rule will do X, and a payload that looks complete while missing one
is worse than one that announces it is incomplete.

Truncating the operating contract also re-introduces the exact anti-pattern
**ADR-006 ("Derive, Don't Ask")** exists to kill: "read the URI before acting"
makes correctness depend on whether a given model obeyed a paragraph — the
prose-as-enforcement failure the project has repeatedly watched not hold.

## Decision

**The bootstrap payload has two tiers, and the token budget governs only one of
them.**

### 1. Two tiers, partitioned by criticality — not by hand-placed pin convenience

- **Inviolable core:** the operating contract (the `workflow` rules, the
  vault-accessor and task-model rules) **and** active constraints/hazards (the
  behavioral "never do X" notes, the current task, open threads, live known
  issues). This tier is delivered **whole, always**.
- **Context:** recent sessions, memory index, KG snapshot, iteration/narrative
  history, the browsable command/skill catalog. Sheddable — but **every** shed
  is explicitly marked in the payload the agent receives, never silent.

The current pin mechanism decides inviolability by a hand-placed
`<!-- vp:pin -->` marker, and its un-pinned zone contains active hazards. That
misclassification is the bug: criticality, not editorial convenience, decides
the tier. Active hazards move into the inviolable core; only genuinely
historical or server-derivable content is context.

### 2. The inviolable core is delivered whole, or the call fails loud — to the agent

The budget applies to the **context** tier. The inviolable core sizes the
**floor**. If the inviolable core *alone* exceeds the budget, that is **not a
shed decision** and must **not** be resolved by dropping or excerpting it into a
"read the URI" stub. It is a hard `over_budget` condition that raises an
**agent-visible** halt banner: *"operating instructions/active state exceed the
context budget — this payload is incomplete; treat it as unsafe to act on and
get a human."* A red `make test` or a WARN in `vp.log` the agent never reads is
not "yelling"; the signal must reach the entity about to change code.

Where a transport physically cannot carry the inviolable core (a hard channel
byte cap), the correct behavior is the same: fail loud and halt, not pretend an
excerpt-with-banner is equivalent.

### 3. The budget stays binding — the remedy is a smaller core, never a bigger budget

The budget is a forcing function, and it is load-bearing: a bloated instruction
surface is itself a correctness hazard, because an agent handed tens of KB of
rules skims them. So this ADR does **not** lift or soften the budget. When the
floor breaches it, the fix is to **shrink the inviolable core** — move
enforcement out of prose and into the server (ADR-006), and move the generic
instruction manual out of vault files and into the binary (ADR-008) — so the
core is small enough to always fit. "Never silently truncate the core" is
affordable precisely because the core is kept minimal.

## Consequences

- The shed ladder must be re-partitioned: `workflow` and the active-state
  sections of `resume` leave the sheddable rung set and join the inviolable
  core; `over_budget` (agent-visible halt) fires when the core does not fit,
  rather than the ladder quietly succeeding by dropping them.
- Resume's active hazards must be reclassified out of the un-pinned/sheddable
  zone (the pin mechanism, or its successor, must guarantee they are inviolable).
- The over-budget signal must be surfaced to the agent in the returned payload,
  not only as a failing test and a log line.

This ADR is the **policy**. The **mechanisms** that make it affordable already
have owners; they are linked here so the policy and its enablers stay connected:

- `bootstrap-payload-exceeds-its-own-token-budget` (**retired**) — the task that
  made the shed loop *honest*: it added `budget.shed` and the never-silent
  `over_budget` alert, ending the fully-silent shed. This ADR is the next step on
  that arc — from "report that you shed" to "never shed the core at all."
- `workflow-md-is-the-new-binding-constraint-on-the-payload` (**active**) — the
  live over-budget breach and the inviolable core's largest item; shrinking it is
  how §2/§3 are met.
- `derive-dont-ask-server-owned-business-logic` (ADR-006) and ADR-008
  (instruction manual in the binary) — the two mechanisms that shrink the
  inviolable prose toward near-zero, so the core always fits and this invariant
  costs nothing to hold.
