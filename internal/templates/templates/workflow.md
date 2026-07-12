# {{PROJECT}} — Workflow

## Files

- **resume.md** — current project state, open threads, navigation (thin gateway)
- **iterations.md** — iteration narratives and project history (append-only archive)
- **tasks/** — active tasks; **tasks/done/** — completed
- **commands/** — slash commands
- **doc/** — stable project reference: architecture, design decisions, testing

## Workflow Rules

- **Never commit without explicit human permission.** Stage files and update
  commit.msg freely, but the actual git commit requires human approval.
- **Never commit AI context files into the project repo.** CLAUDE.md, the
  project-root `commit.msg`, and anything under .claude/ are host-local only.
  (The *vault* copy of `commit.msg` is the exception: it is committed to the
  vault repo each wrap as the canonical commit-message archive.)
- **Git commit messages are the project's history.** Write them to be clear,
  detailed, and self-sufficient.

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

### 2. Subagent Strategy

- Use subagents liberally to keep main context window clean
- Offload research, exploration, and parallel analysis to subagents
- One tack per subagent for focused execution

### 3. Self-Improvement Loop

- After ANY correction from the user: save the pattern with `vp_memory_write`
  (`type: feedback`) — host-agnostic, lands in `Projects/<slug>/memory/`.
  `vp_bootstrap_context` surfaces the memory **index** only (name, description,
  type, rel) — never the bodies; read a body on demand with `vp_memory_read`
- Write rules that prevent the same mistake; for cross-project lessons, also
  record under vault `Knowledge/learnings/`
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

**NOTHING is done until the human says it is done. Never retire a task, never claim completion, without explicit human approval.**

1. Write plan using `vp_manage_task` or plan mode
2. Check in before starting implementation
3. Track progress and explain changes at each step
4. Add review section to the task file
5. When the human confirms completion: retire the task and record an iteration summary

## Core Principles

- **Simplicity First**: Make every change as simple as possible but no simpler
- **No Laziness**: Find root causes. No temporary fixes. Senior developer standards.
- **Minimal Impact**: Changes should only touch what's necessary
- **Test Coverage**: Ensure close to 80% unit test coverage for code changes
