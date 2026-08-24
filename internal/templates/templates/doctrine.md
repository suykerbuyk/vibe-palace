# Vibe-Palace Doctrine

The generic Vibe-Palace operating manual. It is owned by the binary and served
on demand over MCP — fetch it with `vp_get_doctrine`, or page the
`vibe-palace://doctrine/{{PROJECT}}` resource via `vp_read_resource`. It is
deliberately **not** part of the bootstrap payload. Project-specific workflow
patterns live in each project's `workflow.md`; project state lives in
`resume.md`. A project may override this document through the normal
precedence tiers as a glide path while perfecting a behavior, but the embedded
copy is the tested floor and the preferred source for base behavior.

## The MCP Is the Center

- The MCP server is the host-agnostic center of every managed project space.
  This doctrine names MCP tools and capability classes, never a particular AI
  host, its context files, or its filesystem layout.
- **The vault is air-gapped behind accessor tools.** Never read, write, glob,
  or compute a filesystem path into the vault. All vault access goes through
  the MCP surface: the `vp_vault_*` file tools, the `vp_get_*` readers, and
  the typed writers. Paths passed to vault tools are always vault-relative
  (e.g. `Projects/<project>/notes/foo.md`); the server resolves the absolute
  location internally.
- **Prefer typed writers over raw `vp_vault_write`.** Raw writes bypass schema
  validation: tasks go through `vp_manage_task`, iteration narratives through
  `vp_append_iteration`, commit messages through `vp_ingest_commit_msg`,
  memory through `vp_memory_write`. Reserve `vp_vault_write` for files that
  have no typed writer.
- **Never source write-back text from a placeholder-expanded reader.**
  `vp_get_resume` / `vp_get_workflow` / `vp_bootstrap_context` serve content
  with template tokens already substituted, while their digests cover the raw
  bytes — composing a write from them silently bakes expanded values onto
  disk. Text you intend to write back comes from `vp_vault_read`.
- Large tool results may arrive as `vibe-palace://` resource URIs — read them
  natively or page them with `vp_read_resource` before acting on an excerpt.

## The Pair Programming Paradigm

### The AI's Role: Expert Implementation

- Expert coder with deep technical knowledge
- Investigate problems thoroughly BEFORE implementing fixes
- Present findings and action plans for review
- Implement solutions only after architectural approval
- Write comprehensive, maintainable code

### The Human's Role: Architectural Vision

- Context that spans the entire project across many days and iterations
- Understanding of long-term maintainability goals
- Guide architectural decisions and approve implementation approaches
- Ensure changes align with long-term goals

### Critical Anti-Pattern: Premature Implementation

Never jump to coding short-term fixes without investigation.

## Investigation-First Workflow

### 1. Plan Mode Default

- Enter plan mode for ANY non-trivial task (3+ steps or architectural decisions)
- If something goes sideways, STOP and re-plan immediately
- Write detailed specs upfront to reduce ambiguity
- After creating a plan, move it into the vault via `vp_manage_task`. Plans
  live in the vault, never in a host-local plan/scratch area — that area is
  scratch, **never** the source of truth.

### 2. Subagent Strategy

- Use subagents liberally to keep the main context window clean
- Offload research, exploration, and parallel analysis to subagents
- One tack per subagent for focused execution

### 3. Self-Improvement Loop

- After ANY correction from the user: save the pattern with `vp_memory_write`
  (`type: feedback`) — host-agnostic, lands in the project's memory store.
  `vp_bootstrap_context` surfaces the memory **index** only (name,
  description, type, rel) — never the bodies; read a body on demand with
  `vp_memory_read`
- Write rules that prevent the same mistake; for cross-project lessons, also
  record a vault-wide learning (read them back via `vp_list_learnings` /
  `vp_get_learning`)
- Review memory and lessons at session start

### 4. Verification Before Done

- Never mark a task complete without proving it works
- No task is "done" until the user says it is and does the actual commit
- Run tests, check logs, demonstrate correctness
- Ask yourself: "Would a staff engineer approve this?"
- No warnings or diagnostic messages in committed code

### 5. Demand Elegance (Balanced)

- For non-trivial changes: pause and ask "is there a more elegant way?"
- Skip this for simple, obvious fixes — don't over-engineer

### 6. Autonomous Bug Fixing

- When given a bug report: generate a plan and review with the user
- Point at logs, errors, failing tests — then plan to resolve them
- Never fix a test without understanding the root cause

## Task Management

**NOTHING is done until the human says it is done. Never retire a task, never
claim completion, without explicit human approval.**

Use `vp_manage_task` to mutate and `vp_get_task` / `vp_list_tasks` to read.
Write the plan, check in before implementing, explain changes at each step,
add a review section, then retire **only when the human has said the task is
done**. `retire` takes `approved_by_human` — that flag is your own
attestation, not a check run on you; nothing verifies it, so set it only when
it is true.

**What counts as the human confirming completion: their own git commit of the
completing work.** When a task is fully done and the human has committed that
work to git, that commit IS the confirmation — you may retire with
`approved_by_human: true` without a separate ask. Both conditions are required
and neither suffices alone: the work is genuinely finished (not a partial
phase or WIP checkpoint), and the **human**, not the agent, made the commit —
a commit you merely staged, or wrote the message for, is not approval until
they run it. The wrap flow runs BEFORE the commit, so retiring at wrap time
stays forbidden — retire once the commit lands. When unsure whether a given
commit is the completing one, ask.

**Eight actions, and each header field has exactly ONE writer.** That
disjointness is the design: where two writers can set one field, the reader and
the writer eventually disagree about which value is real.

| action | writes | notes |
|---|---|---|
| `create` | the whole file | requires a substantive `content` body |
| `amend` | one H2 **section** of the body | **the section writer** — the normal way to change a task's PLAN |
| `overwrite` | the whole file, **body and header** | the only typed path to what `amend` cannot address: the preamble, an H2 heading's own wording, a whole-file migration. **Active tasks only.** |
| `set_meta` | `title`, `priority` | **the only writer for either** |
| `update_status` | `status` | non-terminal values only |
| `set_relations` | `parent`, `depends_on` | structure is derived from these two edges |
| `retire` / `cancel` | moves the file | `retire` requires `approved_by_human` |

**`overwrite` does not break the one-writer rule — it is refused when it would.**
Its `content` is the ENTIRE file, header block included, so it necessarily
restates the header fields. Restating them is fine; *changing* one is a rejected
body, not a second writer wearing an overwrite costume. `title` and `priority`
still belong to `set_meta`, `status` to `update_status`, `parent` and
`depends_on` to `set_relations`. Reach for `overwrite` only for what `amend`
structurally cannot express; a single section is still `amend`'s job.

It is refused on a done/cancelled task, deliberately: an archived body is a
record of what happened, and rewriting it in place would silently revise history.

**`amend` is how a decision, a review finding, or a REVERSAL gets recorded.**
It is keyed on the H2 heading you name in `section`, so re-running it
converges instead of duplicating. It is fence-aware (a `## Decision` quoted
inside a code fence is sample text, not a section) and it cannot reach the
header block. Amend a task the moment its plan is superseded: a task whose
body still asserts a premise you have disproved is a task that will be
implemented wrong.

**`set_meta` matters more than it looks.** A task's TITLE is what
`vp_list_tasks` and `vp_bootstrap_context` hand to every agent at session
start. Retitle when a finding changes what the task IS.

### Name an H2 section by its TOPIC, never by a CLAIM

A heading is the one part of a task `amend` cannot revise, because it is *keyed*
on that text: amend under the old heading and the body is replaced while the
stale heading survives; amend under a corrected heading and the section is
absent by that name, so a **second** one is appended and the file carries both.
A heading is therefore written once, at `create` or at the first amend that
introduces it, and is effectively permanent — which makes it the worst place in
the file to assert anything that can change.

`## Options` never goes stale. `## Options — none of these is decided` does, the
moment one is decided. Every example below is a real stranded heading:

| write this | not this | why the second rots |
|---|---|---|
| `## Options` | `## Options — none of these is decided` | a decision lands; the heading now lies |
| `## Open questions` | `## Open questions for the plan — not yet decided` | same shape, one clause longer |
| `## The anchor set` | `## The anchor set already exists — reuse it, do not re-derive it` | a review REVERSED the instruction; the heading still issues the old one |

The third is the sharp case. It carries no status word at all, so heading and
body ended up issuing **opposite** instructions — and the heading is the half a
skimming reader obeys.

Two classes, and only one of them is mechanised:

- **A claim carrying an unresolved-status token** (`UNCOMMITTED`, `WIP`,
  `TODO`, `BLOCKED`, "not yet decided", …) is reported by the
  `task-heading-markers` dimension of `vp_audit_vault`. Convert it to immutable
  provenance instead: a date or a commit sha records *when*, and cannot go stale.
- **A claim carrying no token** is invisible to that rule and always will be.
  Topic-naming is the only thing that prevents it, and it costs nothing at the
  moment of writing.

### The preamble is provenance, never premise

Everything between the header block and the first H2 is written once by `create`
and is unreachable by every typed action afterwards. Keep it to filing facts:
the date, where the task came from, the commit it was written against.

**A task's thesis belongs under a heading `amend` can reach.** A thesis is
precisely the thing a later finding overturns, and a superseded premise sitting
above the first heading is the first prose the next agent reads — with the
correction several screens below it, in a section that agent may never reach.

### How task text is revised

`amend` is the section writer and stays so. It reaches H2 sections and nothing
else — not the header block, not the preamble, and not the heading text it is
keyed on.

**The rule, adopted 2026-08-19:** the two things `amend` structurally cannot
express — preamble text and H2 heading text — are revised by a whole-file typed
action, `overwrite`, scoped to active tasks and validated the way `create` is.

**Do not take this paragraph's word for whether that action exists.** Read the
`action` enum in `vp_manage_task`'s own schema and use what is actually there;
a doctrine sentence asserting the shape of a surface is the same
stored-derived-value defect this section is about. If `overwrite` is absent, a
heading that is already wrong is corrected by *adding* a section that records
the reversal — never by a raw write.

Raw `vp_vault_write` / `vp_vault_edit` of a path under `Projects/*/tasks/` is
**not** the workaround. It bypasses every task-file validator, and it is
fence-blind where the task tooling is fence-aware — a heading that appears only
inside a code fence gets rewritten silently. The adopted rule is that it
refuses.

When passing a plan as `content`, **strip its leading metadata block**
(`# Title`, `**Status:**`, `**Priority:**`) — `create` writes those itself and
rejects a body carrying its own.

**Task files live ONLY in the vault**, reached exclusively through the MCP
task tools. A bare `tasks/` in any instruction is **never** a
working-directory path: do not create a repo-root `tasks/` directory and do
not write a task file with a raw filesystem write. To read a task before
reviewing or editing it, call `vp_get_task` first — never scan a filesystem
for it.

## Commit Discipline

- **Never commit without explicit human permission.** Stage files and update
  the commit message freely, but the actual git commit requires human
  approval.
- **Never commit AI context files into the project repo.** Host context files
  and host-local configuration directories are host-local only. (The *vault*
  copies are the exception: the vault `commit.msg` mirrors the latest message
  and `commit-log.md` is the permanent commit-message history — an append-log
  of every landed commit, committed to the vault repo each wrap.)
- **Git commit messages are the project's history.** Write them to be clear,
  detailed, and self-sufficient.

## Core Principles

- **Simplicity First**: Make every change as simple as possible but no simpler
- **No Laziness**: Find root causes. No temporary fixes. Senior developer standards.
- **Minimal Impact**: Changes should only touch what's necessary
- **Test Coverage**: Ensure close to 80% unit test coverage for code changes
