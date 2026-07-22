Execute an approved plan to completion in an isolated git worktree: dispatch a subagent per phase (or per task, your choice depending on size), and verify each deliverable matches the plan in form, fit, and intent. Because the work runs in a worktree, multiple plans can execute concurrently without contending for the main checkout — you coordinate the fast-forward-only merges back to `main`.

## 1. Isolate the work in a worktree

Before any code changes, create an isolated worktree so this plan never touches `main` and other work (including other `/vpc-execute-plan` runs) can proceed in parallel:

    vp worktree create <slug>

where `<slug>` is the plan's task slug. This creates a sibling worktree at `../wt/<slug>` on a `plan/<slug>` branch cut from `main`, and prints the path. **All subsequent code work — your edits, every subagent's edits, builds, tests — happens inside that worktree, never in the main checkout.** If `vp worktree create` reports that the path or branch already exists, a run for this plan is already in flight: stop and confirm with the human rather than colliding.

## 2. Record that the plan is executing

So concurrent runs stay visible and you have a clean merge queue:

- `vp_manage_task update_status` → `in_progress` for the plan's task.
- `vp_manage_task amend` a `## Execution` section onto that task noting the branch (`plan/<slug>`) and the worktree path. `amend` converges on the heading, so a re-run updates the section instead of duplicating it.

The vault task file is the one writer surface for this — the `vp worktree` command itself writes nothing to the vault.

## 3. Execute the plan, phase by phase

Sequentially dispatch a subagent to complete each phase or task (your choice depending on size), and verify the deliverable matches the plan in form, fit, and intent. This preserves your — the orchestrating agent's — thread context so you can drive the whole plan to the finish line while staying within a sane boundary for your thread token context (~200K tokens). Let subagents do the work; you verify their work and its conformance to the planned deliverables.

**Every subagent prompt must inline the worktree path** (`../wt/<slug>`) and instruct the subagent to do all its work there — a subagent cannot see this command or your context, so a rule you do not state is a rule it will not follow. Inline the same way: the no-AI-attribution rule (no `Co-Authored-By`, no AI author lines, in commits or code) and the exact gate commands it must pass.

Note: when a subagent (or you) reads a task via `vp_get_task`, the body may
arrive as a `content_uri` (with `include_content=false`) rather than inline.
Read the resource (`resources/read`) or page it via `vp_read_resource` to get
the full body before acting on it.

If you encounter an issue during the subagent deliverable review that might affect form, fit, or function of the deliverable, pause and inform the human. Explain the situation and offer options as to how to resolve it. Do things right — don't guess, don't create "slop," don't hallucinate. If in doubt, stop and ask for help from your pair-programming partner, the human architect.

## 4. Gate on the branch

Run the gate inside the worktree before declaring the plan ready. No work is done until:

- We achieve as close to 80% unit test coverage as is realistic.
- We prove the function does what it is supposed to do in the context of the full stack with an integration test.
- Documentation has been updated.
- `go vet` is clean and there are **zero new `gopls` hints** — gate on what your change adds, not an absolute count.

## 5. Stop at the branch — the human merges

**Do not merge to `main`. Do not fast-forward anything yourself.** When the branch is ready and gated green, stop and hand the human:

- the branch — `plan/<slug>`, gated green, in `../wt/<slug>`;
- the merge — `git merge --ff-only plan/<slug>` (run from `main`; if `main` advanced under a concurrent run, rebase the branch onto `main` first, then it fast-forwards);
- the cleanup, after they merge — `vp worktree remove <slug> --delete-branch`.

**Nothing is done until the human commits the completing work (Rule 0).** Do not retire the task — the human's merge of the completing work is the declaration of done.

## Concurrency

Multiple `/vpc-execute-plan` runs can proceed at once, each on its own `plan/<slug>` branch and worktree — that is the point of the isolation. The only serialization is the merge: the human fast-forwards the finished branches into `main` one at a time. If two in-flight plans touch the same files, only the first fast-forwards cleanly; the second must rebase onto the new `main` and re-gate. `vp worktree list` shows what is in flight.
