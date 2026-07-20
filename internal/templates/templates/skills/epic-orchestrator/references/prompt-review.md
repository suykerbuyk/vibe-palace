# Reference — Phase-A review subagent prompt

One subagent per open epic member. It is **read-only investigation + recording findings**: it
must not implement or edit source. Fill the `<...>` placeholders and inline the whole thing —
the subagent sees only this prompt.

**Dispatch discipline:** run these in parallel (one per task). Collect every return before
compiling the digest. The subagent writes ONLY its own task file; you (orchestrator) own the
epic file.

---

    You are performing a senior-staff architecture review of ONE task plan in the <PROJECT>
    project, following the /vpc-review-plan discipline. Read-only investigation plus recording
    findings. Do NOT implement anything, do NOT edit source code.

    CONTEXT
    - Project slug: <PROJECT>
    - Task to review: <TASK_SLUG>
    - Read source ONLY from this stable worktree snapshot (a clean checkout at the epic base):
      <EPIC_WORKTREE_PATH> — do NOT read or touch the main tree at <MAIN_TREE_PATH> (other
      agents are mutating it).
    - Member of the "<EPIC_SLUG>" epic. Hint: <ONE_LINE_HINT — known edges, recorded DECISION,
      suspected conflicts, cross-epic blocks>.

    STEPS
    1. Load tools via ToolSearch:
       select:mcp__plugin_vibe-palace_vibe-palace__vp_get_task,mcp__plugin_vibe-palace_vibe-palace__vp_manage_task,mcp__plugin_vibe-palace_vibe-palace__vp_read_resource
       Read the task with vp_get_task (project=<PROJECT>, task=<TASK_SLUG>). If the body is a
       content_uri, page it with vp_read_resource.
    2. Extract: what code it proposes to modify/create, its assumptions about existing
       abstractions, its phase ordering, and any recorded DECISION section.
    3. Validate every claim against ACTUAL source under the worktree path. For every "reuse X /
       call into X / we already have X" claim, write the literal import + call and decide
       whether it COMPILES (a symbol in a _test.go file or package main is not importable;
       unexported is not visible cross-package; internal/ trees are subtree-scoped; a reuse
       that would create an import cycle is not reuse). For every threshold/timeout/budget,
       verify against the real call site and what else runs in that process.
    4. Adversarial self-review: re-derive the key claims from source without trusting the
       plan's framing. A divergence is a finding. If a SETTLED DECISION is infeasible (won't
       compile, won't run where it claims, or its premise is false), flag it CRITICAL and say
       it must be reversed — do not soften it.
    5. Rank findings Critical / High / Medium / Low. No praise, no repeating the plan back,
       reference file:line.

    RECORD: vp_manage_task action=amend, project=<PROJECT>, task=<TASK_SLUG>,
    section="Review (<DATE>)", content=<structured review body>. Do NOT put a `## H2` heading in
    content (the section param supplies it); use `###` for sub-headings. This amend CONVERGES on
    re-run, so it is safe.

    RETURN (data for the orchestrator, not a human message — return EXACTLY this fenced block
    and nothing else):
    ```
    TASK: <TASK_SLUG>
    TOP_SEVERITY: <Critical|High|Medium|Low|None>
    VERDICT: <proceed|revise|cut|investigate>
    FILES_TOUCHED: <comma-separated packages/paths the implementation will modify or create>
    CONFLICTS_WITH: <other epic task slugs likely to touch the same files, or none>
    SUMMARY: <2-3 sentences: the single most important finding and the recommendation>
    ```

---

**Compiling the returns (Phase B).** `FILES_TOUCHED` builds the Conflict Matrix; `VERDICT`
sorts the digest (proceed-and-clean → implement now; revise → amend the plan first; investigate
→ scope unclear; cut → propose retirement). `TOP_SEVERITY` orders the human's triage. Any
Critical is a reversal — surface it at the top, loudly.
