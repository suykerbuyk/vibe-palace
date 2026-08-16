# ADR 006: Derive, Don't Ask

**Status:** Accepted (2026-07-14)
**Deciders:** Project owner
**Context:** The `honest-instruments` epic — correctness must stop depending on which model read which paragraph

## Context

Every failure found in iteration 191 was a place where **correctness depended on a
model reading prose and choosing to act on it.** Not one was an agent behaving
badly. The Zed/Grok session followed the documented workflow *correctly* and still
lost its narrative, its transcript, its attribution, its friction score, and its
deliverable.

Iteration 182 had already rejected prose-as-enforcement — *"every rule Grok broke
was already in its injected context, so an 8th restatement changes nothing"* — and
that lesson was recorded and never generalized. This ADR generalizes it.

**The thesis: for any rule you would write in prose, ask whether the server could
simply do it.**

The standing evidence, all of it load-bearing:

- **`vp check` has a full check suite and reaches nobody.** No template invokes it.
  Prose was doing the enforcing, and prose does not enforce. It is worse than
  inert: `resume-caps` has known `resume.md` was 48.7 KB against a 25 KB cap the
  entire time, and prints `[info]` alongside `All checks passed` in the same breath.
- **`resume.md` carried the "keep this file thin" rule in three places** — its own
  header comment, `wrap.md`, and a `vp check` item — and every wrap read it and
  violated it anyway, until the file hit 88 KB.
- **The claim sentinel gates on parameters the template tells the agent not to
  pass,** so it has never fired on the MCP path.
- **`/vpc-restart` tells every agent to call `vp_bootstrap_context` and never names
  its required `project` parameter.** Grok failed the call outright; Claude only
  survives because its cwd leaks the slug.

## The ladder

| Rung | Mechanism | Failure mode |
|---|---|---|
| **L0** | Prose in context | Model-dependent, fails **silently**, discovered months later. **Assume it will not be followed.** |
| **L1** | Tool descriptions | Still prose; merely closer to the point of use. |
| **L2** | Schema validation / `required` | Server-enforced, but the agent still *supplies* the value, so it can still supply a wrong one. |
| **L3** | Server-derived parameters | The agent **cannot** supply it; the parameter is gone. The failure class is **deleted, not policed.** |
| **L4** | Server-owned composition | The tool writes the *structure*; the agent supplies only *content*. |
| **L5** | Transactional completion | A half-finished multi-step operation is **detectable and loud** rather than silent. |

The precedent already in the codebase: `vp_manage_task action: create` writes the
`# Title` / `**Status:**` / `**Priority:**` header itself and rejects a body
carrying its own (184). That is L4, and it is the healthiest tool on the surface.
It got that way by being burned first.

**The precondition the original plan never stated: you cannot move a rule up the
ladder if you cannot see it being broken.** Every failure in 191 was invisible at
the moment it happened. Observability is not a nice-to-have; it is what makes the
rest of this ADR verifiable. It grew into a task of its own
(`capture-silent-failure-observability`, retired at 201), and *that* is the
evidence for this paragraph.

## Decision: the line

The ladder says *climb*. It does not say *how far*, and that omission is the
expensive part — **over-deriving is its own failure mode.** This ADR draws the
line, because a dead-code sweep, a telemetry design, and every future "should the
server own this?" argument are all blocked on it.

**A rule can be owned by the server only if the server can see everything the rule
depends on.** Ask three questions, in order, and stop at the first *yes*.

### 1. Can the server compute the answer from what it can see?

All the inputs are inside the system — files on disk, the request, the config, the
clock — and given those inputs there is **one correct answer, and it is checkable.**

⇒ **DERIVE.** The parameter is deleted. The failure class is deleted, not policed.

*Specimens:* the iteration number (computable from `iterations.md`, which the server
owns and already locks); `request_id` (minted at the dispatch chokepoint); the
sync verdict (`RemoteVerdict` — one function, both front-ends); audit churn (a glob
of session-note filenames).

### 2. Is the answer a fact about INTENT that no amount of parsing recovers?

The information exists, but only in the author's head. The artifact does not contain
it, and no analysis of the artifact will produce it.

⇒ **DECLARE + ENFORCE.** The artifact states its intent in a machine-readable form;
the server enforces the declaration and **refuses to guess when it is absent.** The
absence must be loud.

*Specimens:* the `<!-- vp:pin -->` marker in `resume.md` (which sections are
correctness-critical is authorial intent, not a property of the prose); a task's
`Parent:` / `Depends:` header lines; every `reason` in
`internal/sourceaudit/baseline.json`; the Zed thread ID, which the extension knows
and the server cannot.

### 3. Is the answer a TRADE-OFF — cost, priority, risk appetite, or an irreversible act?

⇒ **REPORT + DEFER.** The server surfaces the facts, names the options, and a human
decides. It does not act.

*Specimens:* the vault audit's 19-projects-in-one-tree finding (index the history,
or delete the leftover store? *an audit does not get to make that call*); task
retirement; whether to bump `MCPSurfaceVersion`.

**And the anti-posture: PROSE.** A rule in a document, enforced by nobody. Prose is
not a fourth option; it is the absence of one. If a rule is not DERIVED, DECLARED,
or DEFERRED, it is not enforced — it is merely written down, and this project has
four years of evidence about what that is worth.

### The asymmetry rule — which way to err

The three postures **do not fail symmetrically**, and this is the most useful thing
in the ADR:

- A **REPORT** that should have been a DERIVE costs a human one minute.
- A **DECLARE** that nobody declared is caught immediately — *if and only if the
  absence is loud.*
- A **DERIVE of something that is not derivable is a silent wrong guess wearing the
  face of a measurement**, and you find it six months later.

⇒ **When unsure which side of the line a rule falls on, move DOWN the list, not up.**
Under-deriving is recoverable. Over-deriving is the failure this project keeps
paying for:

- `archive_adapter` defaulting to `"claude-code"` — a fabricated value indistinguishable
  from a measured one. It is why the Zed adapter has never once run.
- The audit-staleness nag (209) read a **missing** `session_notes` anchor as **zero**
  and announced *"634 session notes written since yesterday's audit"* — on a vault
  holding 634 notes **in total**. Caught by driving the real server; no test would
  have found it, because every fixture carried the stamp.
- A code-side allowlist of pinned `resume.md` heading names (the obvious, rejected
  design) would have **silently un-pinned the behavioral notes the first time
  someone renamed a heading** — the notes that stop an agent corrupting the vault.

**The corollary, and it is not optional: ABSENCE IS NOT A VALUE.** Every derivation
must distinguish *"I computed X"* from *"I could not compute it."* A default that is
indistinguishable from a measurement is a lie the system tells itself. Three real
specimens, all fixed: `"claude-code"`, `session_notes: 0`, `request_id: ""`.

## Decision: derive ≠ verify ≠ trust

"The server derives it" and "the server verifies what the agent claimed" are
**different moves**, and conflating them is how theatre gets built.

| | who produces the value | who checks it | what goes wrong |
|---|---|---|---|
| **DERIVE** | the server | nobody needs to | the parameter does not exist, so it cannot be wrong |
| **VERIFY** | the agent | the server | the agent supplies a wrong value; the server rejects it |
| **TRUST** | the agent | nobody | the agent supplies a wrong value; **it stands** |

**Use the leftmost the information allows.** DERIVE when the server can see the
inputs. VERIFY when the agent holds information the server lacks but the server can
*check the result* — JSON-schema validation is VERIFY; `create` rejecting a body that
carries its own `**Status:**` line is VERIFY. TRUST only when the server can neither
compute nor check.

**And when it is TRUST, it must be LABELLED as trust.** `approved_by_human: true` is
the canonical case: nothing verifies it, it is an *attestation*, and
`vp_manage_task`'s own schema says so in as many words. A `required` keyword makes
the agent **say** it; nothing makes it **true**.

⇒ **The trap to watch for: a VERIFY that cannot actually verify.** A required
parameter that nothing can check is not enforcement — it is a field. This project
has already deleted one (`vp_health`'s required `project` parameter, which the tool
never used — removed at 201). **"Guard" remains a banned word** for exactly this
reason (184).

## The non-claim — state it, and keep stating it

**This is NOT a security boundary and must never be described as one.** Prevention is
unachievable in-process while the agent holds Bash. Server-side derivation does not
stop an agent from writing the vault directly; it was never meant to.

**Nothing that failed in 191 was an agent behaving badly.** The failures were
well-intentioned agents doing the wrong thing because the instruction never reached
them, and *that* is the class L3/L4 deletes.

**It is a correctness boundary, not a trust boundary.**

## Where the children sat

The DECISION-205 children of `derive-dont-ask-server-owned-business-logic` all
retired on `main`. The umbrella itself retired 2026-08-16 (orphans declined).
This section is the historical placement on the line, not a work order.

### `append-iteration-server-owned` → **DERIVE (L4)** — shipped, retired

The iteration number is computable from `iterations.md`, which the server owns and
already locks. One correct answer, checkable. The parameter goes away.

**With the caveat the 204 review found, and it is the whole lesson:** the derivation
must happen **inside the lock that the append already takes**. `NextIterFromIterationsMD`
is an unlocked `os.ReadFile`; a handler that reads-max-then-appends is a check-then-act
across two critical sections, and two concurrent wraps both write duplicate `N`. That is
the counter corruption of 191, **reintroduced under this ADR's own banner.**
**Deriving under a race is not deriving. It is guessing with extra steps.**

Keeps the optional `iteration` override the PRD already specifies: **derive when
absent, honor when present, record that it was overridden.**

### `clientinfo-seam-derive-model` → **SPLIT** — host DERIVE shipped; model DECLARE

Three values that look like one problem and sit on three different sides:

- **The host / adapter is DERIVE.** `clientInfo.Name` is a fact the server can see —
  at the *handler* seam, never `contextFunc`, which runs before `initialize` and
  silently yields the `claude-code` default. **Shipped.** The child is retired.
- **The model is NOT derivable from `clientInfo`.** `clientInfo.Name` is the HOST
  (`"Zed"`), not the model (Grok). Writing it into `SessionMeta.Model` feeds
  `DetectModelRegressions` a host name as a model *identity* — **worse than the current
  omission, and silent.** It *could* be derived from the **transcript** (adapter-aware).
  That slice was **declined** as residual of this umbrella (operator, 2026-08-16):
  runtime stays **caller-declared**. MCP copies `model`; the hook still uses path-based
  `InspectClaudeJSONL`. Until a future task exists, the posture is **DECLARE**, not a
  silent guess from `clientInfo`.
- **The Zed thread ID is not derivable AT ALL.** Zed multiplexes every thread in a
  window onto one `vp mcp` process, so two threads on one folder are indistinguishable
  *in principle* — and empirically `folder_paths` is populated on 14 of 270 rows.
  **It must be PUSHED by the extension that knows it.** That is DECLARE, and it lives
  on `zed-pane-capture-parity`, not this umbrella.

**On failure: degrade with recorded provenance — never a silent default.** Record what
was derived and *how*; on failure record `unknown`. **Never fall back to `claude-code`.**
This is the same rule as the `ChurnKnown` fix (209): absence is not a value.

### `derive-dont-ask-dead-code-sweep` → **REPORT + DEFER, with a DECLARE escape** — shipped, retired

Two questions, and they are not the same question:

- *"Is this symbol dead?"* is **DERIVE** — an AST fact. `internal/sourceaudit` computes it.
- *"Should this dead symbol be deleted?"* is a **JUDGMENT.** A functional option with no
  caller today may be API you intend to keep. The AAAK functions in `internal/palace` —
  the sweep's largest concentration of unreachable code — were **deliberately parked**
  (205). **A sweep that reaps them deletes a decision.**

⇒ The sweep **reports**; the **baseline is the DECLARE channel**, and every survivor
carries a reason naming its verdict, its evidence, and the task that owns it.

**The baseline is not a suppression list. It is the artifact declaring an intent the
code cannot express** — the same shape as the `vp:pin` marker. That is why **it may
only shrink**, and why a stale entry fails the build exactly as loudly as a new finding.

## Consequences

**What this buys.** A rule that is DERIVED cannot be forgotten, because there is
nothing to remember. A rule that is DECLARED cannot be silently dropped, because its
absence is loud. A rule that is DEFERRED cannot be wrongly automated, because it was
never automated. The three postures between them have no silent-failure mode — which
is the entire point, and is why the epic is called `honest-instruments`.

**What it costs.** DECLARE puts a marker in the artifact, and every artifact author
must learn it. REPORT keeps a human in a loop they might have preferred to leave.
Both are real costs and both are cheaper than a silent wrong guess.

**The self-indictment, kept because it is the sharpest evidence the ADR has.** The
plan written to condemn prose-dependent correctness **was itself wrong on three of its
four concrete items** — it tried to derive the model from `clientInfo` (a host name),
tried to derive Zed thread identity (impossible in principle), and picked a seam that
runs before the data exists — in ways that only *reading the source* caught. It then
proposed building a command whose name was **already taken by a shipped feature**. If
the habit is sticky enough to catch its own critic, four times, while they are actively
diagnosing it, then prose will not fix it.

That is the argument. **The ADR is not the enforcement either — the server is.**

## Related

- `doc/adr/003-vault-write-locking.md` — the lock discipline `append-iteration-server-owned` must derive *inside*.
- `internal/sourceaudit` — DERIVE ("is it dead?") plus DECLARE (the baseline's reasons).
- `internal/vaultaudit` — REPORT + DEFER, and an accepted-ID ratchet that forces its own record to shrink.
- `internal/resumezone` — the `vp:pin` / `vp:disposable` markers: DECLARE + ENFORCE, with a loud refusal to guess. An H2 carrying NEITHER marker is undeclared LIVE STATE, and `check.CheckPinCoverage` REPORTs it by name.
