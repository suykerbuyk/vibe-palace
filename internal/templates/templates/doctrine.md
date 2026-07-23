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

**Seven actions, and each field has exactly ONE writer.** That disjointness is
the design: where two writers can set one field, the reader and the writer
eventually disagree about which value is real.

| action | writes | notes |
|---|---|---|
| `create` | the whole file | requires a substantive `content` body |
| `amend` | one H2 **section** of the body | **the only way to change a task's PLAN** |
| `set_meta` | `title`, `priority` | **the only writer for either** |
| `update_status` | `status` | non-terminal values only |
| `set_relations` | `parent`, `depends_on` | structure is derived from these two edges |
| `retire` / `cancel` | moves the file | `retire` requires `approved_by_human` |

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
