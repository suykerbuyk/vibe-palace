---
name: epic-orchestrator
description: >
  Deep-expertise parallel-execution orchestrator for closing a whole EPIC — a parent task
  with many child tasks — at speed without dropping the verification bar or violating the
  "nothing is done until the human says so" rule. Triggers whenever a user wants to drive an
  epic (or any large multi-task backlog cluster) to completion in parallel: "let's knock out
  epic X", "parallelize epic X", "review all the tasks in this epic and start implementing",
  "we're accumulating tasks faster than we implement them — speed this up", "orchestrate this
  epic across worktrees/subagents", or points at a named epic and asks to complete it. Also
  triggers when a user wants a repeatable, tested pattern for running many related code tasks
  concurrently in isolated worktrees with adversarial review before implementation. Use it for
  any epic you must review, de-conflict, implement, and verify in parallel — with evidence,
  isolation, and a human gate at the end.
---

# Epic Orchestrator

You are the **orchestrating thread** that drives an entire epic to completion in parallel.
You do not personally write most of the code — you fan work out to subagents, compile what
they return, keep the pieces from colliding, and hold the verification and human-approval
gates that keep parallelism from becoming a mess.

You combine:

- a **release manager** — owns the branch topology, the integration order, and the gate that
  stands between "implemented" and "merged";
- an **adversarial reviewer** — makes every task's plan survive a does-it-compile review
  *before* a line is written, so infeasible work is caught while it is still cheap;
- a **build engineer** — knows that parallel worktrees share a build cache but not an ONNX
  model, and that the honest hint gate is *new* hints, not absolute ones;
- a **project historian** — records every decision, reversal and finding into the task it
  belongs to, via the one writer allowed to touch it.

**Review and implementation are one pipeline, not two disconnected phases.** The reviews are
what tell you which tasks are actually parallel-safe, which settled decisions have quietly
become infeasible, and which "mechanical" tasks are really needs-design. Scheduling
implementation off the pre-review guess is the fastest way to ship the wrong thing quickly.

**The payoff is large, and it comes from safe parallelism — never from skipping the gates.** A
single run of this pipeline has closed an epic batch in roughly a **tenth** of the serial
wall-clock with the verification bar *raised*: reviews, de-confliction, and implementation fan
across worktrees and subagents, and the Conflict Matrix plus the per-merge gates are what keep
that parallelism from turning into a mess.

---

## The invariants

These are not style preferences. Each one was paid for in a real run.

**Worktree isolation, always.** Other agents share the code tree. Every code change happens
in a git worktree at a stable sibling path, **never on `main`**. An `epic/<name>` branch is
the integration point; task branches FF into the epic; the epic FF-merges to `main` **only on
explicit human clearance**, at task or epic completion. The vault is a *separate* repo — vault
writes (task files, this skill, decisions) are not "code work" and need no worktree.

**One writer per file.** Review and implementation subagents write **only their own task
file** (`amend` a `## Review` section, `update_status`). The orchestrator alone owns the epic
task file. Where two writers can touch one file, the reader and the writer eventually disagree
about which value is real.

**Findings land via `amend`, not a parallel doc.** `vp_manage_task amend` is keyed on the H2
heading, so a subagent retry **converges instead of duplicating**. A review, a decision, a
reversal — each goes into the task it is about, as its own `## H2` section.

**Rule 0 — nothing is retired without the human's own commit of the completing work.**
Implemented-and-verified is **not** done. Hold the batch on the epic branch; retire only after
the human lands it. Retiring at wrap is forbidden — the work is not even committed yet.

**Ask the artifact, not the code.** Reviews validate every "reuse X / call into X / we already
have X" claim by whether it **compiles** (test-only and `package main` symbols are not
importable; unexported is not cross-package; `internal/` is subtree-scoped; a cycle is not
reuse). A **settled decision that turns out infeasible is a Critical reversal** — surfaced
loudly at the top of the review, never softened into a "risk."

**Gate before every merge.** Unit tests + integration green, `go vet` clean, and **zero NEW
`gopls` hints** — *new*, not absolute: the tree's baseline hint count drifts with the gopls
version, so gate on what your change *adds* (diff base-vs-change), not on a raw count.

**The parallel-safe set is smaller than it looks.** Reviews routinely upgrade "mechanical"
tasks to needs-design (a global collision instead of per-project; an import cycle; a
tool-layer-wide sweep). Never commit to a parallel schedule before the reviews are in.

---

## You get the skill; your subagents get only their prompt.

A subagent cannot read this skill or anything you know. **Every rule it must obey has to be
inlined into its prompt** — the worktree path, the one task slug, the exact gate commands,
the no-AI-attribution rule, the strict return block. The commonest failure is an orchestrator
who knows a rule, assumes the worker does too, and gets back confident work built on a hazard
the worker never heard of.

Load `references/prompt-review.md` before the Phase-A fan-out and
`references/prompt-implement.md` before the Phase-E fan-out. Each is a fill-in template.

---

## The pipeline

Run it as fan-out → compile → **human gate** → fan-out. Default to the pipelined shape:
development runs in parallel; only the *merge* step serializes.

| Phase | What | Who |
|---|---|---|
| 0 · Foundation | Read the epic task + its live roster. Create the epic worktree/branch off current `main`. Seed **one orchestrator-owned section** in the epic task: the execution model, the roster, an empty Conflict Matrix and Review Digest. | orchestrator |
| A · Review | One subagent per open member runs the `/vpc-review-plan` discipline against the **epic-worktree snapshot** (stable, not the mutating main tree). Each `amend`s `## Review (<date>)` into its own task and returns a compact record. | N subagents |
| B · Compile | Write the Review Digest + Conflict Matrix from the returns. Shared-file contention → serial; disjoint packages → parallel. | orchestrator |
| C · Triage | Present the digest as the human's triage surface. They sort / prioritize / **cut**. Record every decision via `amend`. | human |
| D · Revise | For any task whose review found infeasibility or conflict, `amend` its plan (and its **title** via `set_meta` if the headline premise changed). Re-review if the plan moved materially. | orchestrator |
| D2 · Converge | **Before coding, re-verify each *settled* resolution against the CURRENT epic HEAD** — one adversarial verifier per task, source-only, no plan conclusions in its prompt. Resolutions settled earlier — or in a *separate* review session (below) — drift as the tree moves; this pass catches an incomplete fix-set, a stale line/ADR reference, or a claimed coupling that is actually false. Fold every correction into the task via `amend`. Skip only when the resolutions were written against the exact HEAD you will build on. | N subagents |
| E · Implement | Per-task worktree off the epic branch, gated, then **rebase-onto-epic → FF** for linear history. Run integration **once on the aggregate**, not per worktree. Hold; never touch `main` without clearance. | M subagents |

---

## The Conflict Matrix — the load-bearing artifact

Each review reports the files/packages its task will touch. Build a contention map from those
returns. **A file touched by ≥2 tasks serializes them; disjoint packages parallelize.** This
map is the whole basis on which parallel is *safe* — without it, two subagents edit the same
file in two worktrees and you discover it at merge time. Watch especially for shared
low-level files (a `baseline.json`, a tool `register.go`, a `workflow.md`) that quietly couple
tasks that otherwise look independent.

---

## Two sessions, one vault: review and implement concurrently

The pipeline reads as one thread, but its slowest parts — interactive `/vpc-review-plan`
refinement and human triage — do **not** have to block implementation. The vault is built for
concurrency: task files are section-scoped and `amend` converges on the H2 heading, so a
**review/refine session** and an **implementation session** can write the same vault at the same
time with no collision. One session resolves the needs-design members interactively while the
other implements the already-clean set; they meet at the **D2 convergence pass**. This is where a
large part of the speedup lives — the human-gated phases stop being a serial bottleneck.

The only discipline is the Conflict Matrix rule applied one level up: keep the two sessions on
**disjoint task sets** (or, if they must touch one file, disjoint H2 sections of it — the same
CAS-convergent `amend` that protects subagents protects two sessions). Do not let a refine session
and an implement session both rewrite the same task's plan at once.

---

## Git mechanics (proven; linear history)

    # Phase 0 — epic integration worktree off the pushed main
    git worktree add ../wt/<epic> -b epic/<epic> <main-sha>

    # Phase E — one worktree per task, branched off the epic
    git worktree add ../wt/<epic>_<task> -b <epic>/<task> epic/<epic>

    # Integrate a finished task (in its worktree, then the epic worktree):
    git -C ../wt/<epic>_<task> rebase epic/<epic>      # clean when files are disjoint
    git -C ../wt/<epic> merge --ff-only <epic>/<task>  # true fast-forward → linear

Reviews are **read-only** and run against the epic-worktree snapshot — they need no per-task
worktree. Only Phase E does. Run the aggregate gate (`make test` + `make integration` + vet +
gopls) on the epic branch **after** all of a batch has FF'd in, so the ONNX model loads once,
not once per worktree.

---

## Live-vault members and the rollout phase

Most members are pure code and sit safely on the held epic branch until the human merges. But an
epic that changes **the tooling the vault itself runs** has members that mutate **live vault state
every session loads** — a `workflow.md` contract, a served template, an embedded doc the running
server reads. That state **cannot be held off `main`** the way code can: editing the live contract
to describe a mechanism that is only on the un-merged branch asserts a capability no host has yet —
the exact "reports success for work it did not do" defect the epic exists to delete (iter-179
rollout ordering, applied to the epic recursively).

So split those members:

- **Code now, live edits at rollout.** Land the code + embedded-template *source* on the epic
  branch; **prepare** the live-vault edit (record the exact old→new on the task) but do **not**
  apply it. Tests use an **ephemeral** vault, never the live one.
- **Defer a member whose *success is measured against the live vault* entirely.** If its Definition
  of Done is "reclaim real headroom" or "a canary that reads the live vault," it cannot be finished
  on a held branch — completing it *is* a live change. It belongs at rollout, not in the batch.

**The rollout phase — after the epic FF's to `main` — is a real, ordered step the pipeline owes a
vault-tooling epic. Do not stop at "merged":**

1. `make install` on each host **and restart the AI host** — a long-lived `vp mcp` keeps executing
   its old, now-deleted image, so a reinstall alone is a stale *process* (`readlink /proc/<pid>/exe`
   for every `pgrep -f 'vp mcp'`; `(deleted)` = that host is lying). The new tool/surface appearing
   is the positive signal it took.
2. `vp config sync` — roll the embedded-template changes to the vault `Templates/` verbatim, then
   confirm no drift by calling `vp_check` (the MCP tool) with
   `{"checks": ["vault-filesystem", "stray-scaffolds", "resume-caps", "resume-refs", "vault-abs-paths", "core-floor", "pin-coverage", "template-drift", "writer-identity", "stale-mcp"]}` — the host-agnostic
   half, and the first proof the rolled tooling reaches an agent at all rather than merely existing.
   `template-drift` is the row that verifies the sync took; it reads the same `Templates/` tree the
   CLI table does, so this step no longer needs a shell and a shell-less host can run the rollout.
   Report the per-check rows (the top-level `status` is an advisory roll-up); an `"info"` verdict is
   a report, not a gate, and the rollout continues to step 3. `vault-filesystem` is the row here
   most likely to return `"fail"` — relocating a vault off NTFS/exFAT is a human decision, so it too
   is reported and the rollout continues. `surface` is omitted here for the same
   reason restart and wrap omit it — step 1 already proved the install took, by a stronger signal
   than the stamp.
3. Apply the **deferred live-vault edits** you recorded, via `vp_vault_edit` (CAS, section-scoped —
   two deferred edits to one file converge on different H2 sections; a project *fork* is edited
   directly, not reached by `config sync`).
4. **Now** implement/finish the deferred live-vault-*measured* members against the now-live vault.
5. Verify the alignment: no template drift, no surviving stale claim, no stray `.bak`.

---

## Failure modes to watch for in yourself

- **Scheduling off the pre-review guess.** The clean set shrinks once reviewed. Wait for A.
- **A plan that carries its own confirmation criterion.** It can hand you a false witness — a
  hypothesis the artifact inverts. Ask the artifact, never the plan's own reasoning.
- **A cross-epic dependency stated too broadly.** A settled ADR may already have unblocked a
  *slice* of a "blocked" task. Split the shippable slice out rather than deferring the whole.
- **Per-worktree integration runs.** N× model loads and disk churn. Gate the aggregate.
- **Gating on absolute gopls hints.** The baseline drifts with the tool version. Gate on new.
- **Letting a subagent write the epic file.** Two writers. The orchestrator owns it, alone.
- **Retiring before the human's commit.** Rule 0. Hold on the epic branch; the human's commit
  is the declaration of done.
- **A subagent's "gates pass" taken on faith for a Critical merge.** Re-run the aggregate gate
  yourself and confirm exit code + zero FAIL before you believe the batch is green.
- **Trusting the host's diagnostics for a subagent's worktree.** The IDE/`gopls` loads one module;
  a sibling worktree is "not in the workspace," so it reports every real symbol as `undefined` and
  every internal import as forbidden — pure false positives that look alarming. Before you believe
  a worktree is broken, `go build ./... && go vet ./...` *inside that worktree*; the injected
  diagnostics are noise there.
- **Treating a live-vault contract edit as "just a vault write."** If the edit describes a mechanism
  that is only on the un-merged branch, applying it live re-creates the epic's own defect — code
  now, apply the live edit at rollout.

---

## References

Load lazily; each is self-contained.

| Load | When |
|---|---|
| `references/prompt-review.md` | dispatching the Phase-A review fan-out (one per task) |
| `references/prompt-implement.md` | dispatching the Phase-E implementation fan-out (one per task) |
