# The subagent harness

How `code-digger` orchestrates workers. Read this before dispatching anything.

## The law

> **The orchestrator gets skills. Subagents get pasted prompts.**

A subagent receives **only its prompt**. It cannot load this skill, cannot read
the project's agent-instructions file, cannot see the vault, cannot see what you
know. Everything it must obey has to be **inlined into its prompt** — which is why
the environment hazards exist as a verbatim-pasteable block and why every prompt
template in `references/` is self-contained.

The commonest failure in this whole method is an orchestrator who *knows* a rule,
assumes the worker knows it too, and gets back confident work built on a hazard the
worker never heard of.

## Dispatch shapes

### 1. The paired dispatch — writer + adversary

For any component where durability, correctness, or security is load-bearing,
dispatch **two agents concurrently** against the same component:

- the **doc-writer** (`prompt-deep-dive.md`) — holds `Write`, produces the doc;
- the **adversarial auditor** (`prompt-adversarial-audit.md`) — holds **no write
  tool**, attacks a named thesis, returns findings only.

They must not see each other's work. The independence is the point.

**When they contradict each other, that is the harness working, not failing.**

In iteration 8 of this project's origin run, the writer and the auditor reached
opposite conclusions about whether a data-durability bug existed. Resolved by hand,
**both were half right**: the bug was *refuted* at the spool layer (the blob really
was `sync_all()`'d before the metadata commit — deliberately) and *confirmed* at
the tape layer (the filemark is written with SCSI `IMMED=1`, no `SYNCHRONIZE CACHE`
exists anywhere in the repo, and the worker then deletes the only durable copy).

Neither agent was lying. They were **answering at different layers**. A single
agent would have picked one layer, been internally consistent, and been wrong.

**So: when two agents disagree, do not average them and do not pick a winner. Find
the layer boundary they are talking past.** The disagreement is a pointer to
exactly where the interesting thing is.

### 2. One tack per subagent

Give a worker **one** job with **one** thesis. An agent asked to "document it and
also audit it and also check the CI" produces three mediocre passes. The parallel
dispatch is cheap; the muddled prompt is not.

### 3. Breadth and depth run concurrently, not in sequence

The tier ladder is a **dependency order, not a schedule.** Do not finish all
breadth docs before starting any deep-dive. **Dispatch a deep-dive the moment its
breadth doc can brief it** — the seed is the trigger.

Sequencing them serially was the single biggest wall-clock waste in the origin run.

## The seed handoff

Each breadth doc ends with `## 9. Deep-dive seed` — 2–4 focus questions, file
anchors, open unknowns. The orchestrator lifts that section **verbatim** into the
deep-dive prompt's `{{FOCUS}}` slot.

That is the whole handoff. It means a breadth doc briefs its own deep-dive and the
orchestrator never has to re-read the repo to write focus questions.

> **Honesty note.** In the origin run this convention was *believed* to exist and
> did not — the state file asserted a "DEEP-DIVE SEED" section that no breadth doc
> ever contained. The focus questions were in fact hand-lifted from each doc's
> closing unknowns list. The convention above is the **fix**, adopted after writing
> this skill exposed the gap. Mentioned because it is the method's own best
> example of received wisdom surviving unchecked — including about itself.

## State: the thing that actually improves

**The prompts are pure functions. The project's state file supplies the arguments.**

`{{KNOWN_CONTEXT}}` is hydrated from the running state file (`resume.md`) at
**every** dispatch. It carries the established facts *and every correction earned
so far*.

This is why late workers outperform early ones. The method does not get better
during a run — the **context** does. An orchestrator who dispatches ten agents with
an empty `{{KNOWN_CONTEXT}}` gets ten agents that each independently re-learn, and
re-mis-learn, what the first one already paid for.

So: **after every returned `## CORRECTIONS` block, write the correction into the
state file before the next dispatch.** That single discipline compounds harder than
anything else in this method.

## Verifying what comes back

### Verify a Critical by hand. Always.

**A subagent's word is not sufficient basis for an escalation.** Before a Critical
or a security finding enters the register — let alone a human's inbox — re-check it
yourself, at the workspace root, reading the cited lines.

This is cheap, and in the origin run it caught framing errors that would otherwise
have gone into the register as fact. It is also the difference between a document a
staff engineer trusts and one they spot-check once, find wrong, and never open
again.

### Grade the deployment condition separately from the defect

A data-loss bug in a component **nobody deploys** is not a Critical. It is a High
with a condition attached: *"→ Critical if deployed."*

Do not let a worker collapse those two. Whether anything actually runs a component
is frequently **not answerable from the tree** — and when it is not, the honest
output is the conditional grade plus an explicit question for a human. Inventing a
severity to avoid the awkwardness of an open question is how registers lose their
credibility.

### Cross-check a finding against its siblings before generalising it

The origin run found that the Rust `tapemanager` wrote tape filemarks with
`IMMED=1` and no cache-sync. The obvious next move — report it against the whole
product line — would have been **wrong**: the shipped Go `tape-engine` sets
`IMMED=0`, deliberately, with a golden-CDB test proving it.

Same hardware, same operation, two implementations, opposite correctness. **A
finding is about the code you read, not the family it belongs to.**

## Anti-patterns

- **Trusting the general rule about build inputs.** Always check the consuming
  build file. This has been the source of nearly every falsified claim.
- **A recursive search from the workspace root** when the shell's `grep` honours
  `.gitignore` — it returns a clean-looking zero. Use `command grep -rn <pattern>
  <explicit-path>`.
- **Letting a worker mark a command as runnable** when nothing was run. Everything
  inferred is `[unverified]`.
- **A confident paragraph in place of an unknown.** The unknowns list is the most
  honest section in any of these documents and it must never be quietly emptied.
