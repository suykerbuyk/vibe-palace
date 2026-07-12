# Review Plan — Architecture Review

Perform a critical architecture review of a task plan before
implementation begins.

This is a **senior staff engineer review**. The goal is to find
real problems — not to rubber-stamp the plan. Read the plan, read
the code it references, and identify what will actually go wrong.

## Inputs

**Where plans live.** Tasks live in the vault, under
`Projects/<slug>/tasks/`, and are reached **only** through the MCP task tools:
`vp_manage_task` to mutate, `vp_get_task` / `vp_list_tasks` to read. Your
host's local plan/scratch directory (on Claude Code, `~/.claude/plans/`; other
hosts differ, and some have none) is **scratch — never the source of truth**.
Never review from it, and never leave a plan there.

Before reviewing, check that scratch directory for any plan files not yet
promoted into the vault. If found, create each as a task via `vp_manage_task`
(`action: create`) using the slugification rules from `/restart`, then delete
the original scratch file. Then review from the vault copy via `vp_get_task`.

**Strip the plan's metadata block before passing it as `content`.** Plans
idiomatically open with `# Title`, `**Status:**`, and `**Priority:**` lines.
`vp_manage_task create` **supplies those itself** and **rejects** any `content`
carrying its own `**Status:**` line or its own top-level `# ` heading — so
delete that leading metadata block and pass only the body beneath it. (H1-shaped
shell comments inside a ``` or ~~~ fence are fine.) **Why:** duplicate status
lines make the reader and the writer disagree about which one is real — the
task tools would report one status while `update_status` rewrites the other.

If no argument is given, use `vp_list_tasks` to find all active
tasks and review them. If a filename is given, use `vp_get_task` to
read only that task.

## Step 1: Read the Plan

When reading a task via `vp_get_task`, the body may arrive as a `content_uri`
(with `include_content=false`) rather than inline. Read the resource
(`resources/read`) or page it via `vp_read_resource` to get the full body
before reviewing it.

Read the task file(s) and extract:

- What code it proposes to modify or create
- What assumptions it makes about existing abstractions
- The proposed phase ordering and dependencies

## Step 2: Validate Against the Codebase

For every claim the plan makes about existing code, **read the
actual source**. Do not trust the plan's characterization of
coupling, interfaces, or behavior. Specific checks:

- **Function signatures**: Does the plan accurately describe them?
- **Coupling claims**: If the plan says "loose coupling" or "reuse
  directly", verify by reading the function body. Look for hidden
  dependencies (subprocess calls, global state, hardcoded values,
  implicit assumptions).
- **Data flow**: Trace how data actually moves through the
  pipeline. Does the plan's proposed integration point actually
  work?
- **Naming and constants**: Are there hardcoded strings, fallback
  values, or field names that assume a single source/context?

Use subagents liberally to parallelize investigation of
independent components.

### Feasibility, not existence

A symbol existing is not the same as the plan being able to use it. Existence
checks pass on plans that cannot compile. So:

- For **every "reuse X" / "call into X" / "we already have X" claim**, write
  out the literal `import` line and the literal call, and decide whether it
  **compiles**. Specifically:
  - A symbol defined in a `_test.go` file is **not importable** from
    non-test code.
  - A symbol in `package main` is **not importable** at all.
  - A lowercase (unexported) symbol is **not visible** outside its package.
  - A symbol in an `internal/` tree is not importable from outside that
    module subtree.
  - Reuse that would create an **import cycle** is not reuse.
- For **every threshold, timeout, budget, or timing claim**, check **where the
  code runs** and **what else runs there** — a 200 ms budget is meaningless
  until you know it shares a process with an embedder load, a git pull, or a
  full vault walk. Verify the number against the real call site, not the
  plan's assertion about it.

### Adversarial self-review

If the same model authored the plan, an ordinary review just re-confirms the
plan's own reasoning. Break that loop:

- **Re-derive the key claims from source**, using independent subagents whose
  prompts carry **none of the plan's conclusions** — no citations, no "verify
  that X", no framing. Ask them **"is this possible?"** and **"how would you
  do this?"**, never "is this cited correctly?".
- Compare what they come back with against what the plan asserts. A divergence
  is a finding, not a rounding error.
- **An infeasible settled decision must be reversed, loudly.** "We already
  decided this" is not evidence that it works. If a decision the plan treats
  as settled turns out not to compile, not to run where it claims, or not to
  exist, say so at the top of the review as a **Critical**, name the decision,
  and state plainly that it must be reversed. Do not soften it into a "risk"
  and do not route around it.

## Step 3: Structured Review

Produce a review covering these categories. Be specific — reference
file paths and line numbers. Skip categories where there are no
findings.

### Factual errors

Where the plan misunderstands or misrepresents the codebase.
Include the plan's claim and the actual code behavior.

### Architectural concerns

Is the approach sound? Anti-patterns? Better alternatives? Consider:

- Flag-argument anti-patterns (optional fields that create hidden
  code paths)
- Premature or missing abstractions
- Whether the refactoring preserves existing behavior

### Risk assessment

What could go wrong? What is the plan underestimating? Consider:

- File locking and concurrency
- Backward compatibility of schema/index changes
- External dependencies (stability, size, transitive deps)
- Edge cases the plan does not address

### Performance concerns

Identify bottlenecks, quadratic behavior, unnecessary I/O.
Consider the expected data volume and whether the approach scales.

### Dependency concerns

For any new dependency: actual binary size impact, transitive
dependency count, maintenance status, alternatives. Verify the
plan's size claims against reality.

### Missing considerations

What did the plan forget? Check for:

- Commands that will silently break with the new data
- Fallback strings or defaults that assume a single source
- Migration needs for existing data
- Test strategy gaps

### Opportunities

What could be done better? Richer data available? Simpler
approach? Existing code that could be leveraged?

### Phasing critique

Is the phase ordering correct? Look for:

- Dependencies between phases that force a different ordering
- Phases that cannot be tested independently
- Work that is deferred but should be concurrent (e.g., test
  fixtures)

## Step 4: Severity Ranking

Rank all findings by severity:

| Severity     | Meaning |
|--------------|---------|
| **Critical** | Will cause incorrect behavior or build failure. Must fix before implementation. |
| **High**     | Significant design flaw or risk. Should fix before implementation. |
| **Medium**   | Suboptimal but workable. Fix during implementation. |
| **Low**      | Minor improvement. Address if convenient. |

## Step 5: Present to User

Present the review as a structured summary with the highest-
severity items first. For each **Critical** or **High** item,
include:

- What the plan says
- What the code actually does
- The recommended fix

End with a clear recommendation: proceed as-is, revise the plan
first, or investigate further before deciding.

## Anti-Patterns to Avoid in the Review

- **Do not praise the plan.** Focus only on problems and
  improvements.
- **Do not repeat the plan back.** The user has already read it.
- **Do not speculate about code behavior** — read the source.
- **Do not flag theoretical risks** that cannot happen given the
  architecture.
- **Do not suggest adding abstractions "for future extensibility"**
  unless a concrete third case is imminent.
