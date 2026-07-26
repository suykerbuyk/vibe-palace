# ADR 009: Deliver the Inviolable Core Whole, or Fail Loud — Never Silently Truncate Operating Instructions or Active State

**Status:** Accepted (2026-07-21) — partially enforced 2026-07-22
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
