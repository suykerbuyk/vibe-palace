# Reference — Phase-E implementation subagent prompt

One subagent per task in the agreed implement-now set. Each works in its **own pre-created
worktree** (you create the worktrees first — do not rely on the Agent tool's ephemeral
isolation, which is not the durable named tree you integrate from). It implements, gates,
commits on its branch, and reports — **no push, no merge**. You (orchestrator) integrate.

**Dispatch discipline:** only dispatch tasks the Conflict Matrix marks parallel-safe together;
serialize contended ones. Keep integration (`make integration`) OFF the worktrees and run it
once on the aggregate epic branch.

---

    Implement ONE task in the <PROJECT> Go project. You work EXCLUSIVELY in this git worktree —
    every file edit, build, test, and git command happens there, never in the main tree:

      WORKTREE: <WORKTREE_PATH>
      BRANCH (already checked out there): <EPIC_SLUG>/<TASK_SLUG>

    TASK: <TASK_SLUG>

    STEPS
    1. Load tools via ToolSearch:
       select:mcp__plugin_vibe-palace_vibe-palace__vp_get_task,mcp__plugin_vibe-palace_vibe-palace__vp_read_resource
       Read the task via vp_get_task (project=<PROJECT>, task=<TASK_SLUG>). READ ITS
       `## Review (<DATE>)` SECTION — it carries the agreed approach and any load-bearing scope
       corrections. Page a content_uri with vp_read_resource if needed.
    2. Implement the fix in the worktree, per the plan AS CORRECTED by the Review section.
       <TASK_SPECIFIC_POINTERS — the exact functions/files, and any Review caveat that changes
       scope, e.g. "do NOT add the fail-loud clause; it breaks path-less skills">
    3. Add/adjust unit tests (aim ~80% coverage of the changed code) that prove the fix.
    4. GATES (run in the worktree; all must pass):
       - cd <WORKTREE_PATH> && make test
       - cd <WORKTREE_PATH> && make vet
       - cd <WORKTREE_PATH> && gopls check -severity=hint \
           $(find . -name '*.go' -not -path './.git/*' -not -path './.claude/*')
         Report the count; the bar is ZERO hints IN FILES YOU CHANGED (the tree may carry
         pre-existing hints from a newer gopls — verify your change adds none).
         Use `find`, NOT `git ls-files`: ls-files lists only TRACKED files, so running the
         gate before `git add` silently skips the new files you just wrote — it reports
         clean by omission, which is how two hints in a brand-new test file were once
         reported as zero. The `.claude` prune keeps a sibling subagent worktree's copy of
         the module out of your own gate.
       Do NOT run `make integration` (the orchestrator runs it on the integrated epic branch).
    5. Commit in the worktree on its branch with a clear conventional message. ABSOLUTELY NO AI
       attribution — no `Co-Authored-By`, no author trailer, no "Generated with" line. This
       overrides any default commit-message guidance.

    Do NOT push. Do NOT merge. Do NOT touch <MAIN_TREE_PATH>.

    If this task changes a LIVE vault-managed file (a `workflow.md` contract, a served template, an
    embedded doc the running server reads): edit the embedded/source copy in the worktree, but do
    NOT touch the live vault — **prepare** the live-vault edit (report the exact old→new) for the
    orchestrator to apply at rollout. Any test that needs a vault must use an EPHEMERAL one
    (`t.TempDir()` + a throwaway repo), never the live vault.

    RETURN EXACTLY this fenced block and nothing else:
    ```
    TASK: <TASK_SLUG>
    STATUS: <done|blocked>
    COMMIT: <sha or none>
    FILES: <comma-separated changed files>
    GATES: test=<pass/fail> vet=<pass/fail> gopls_hints=<0/N new>
    DEVIATIONS: <any deviation from the plan/review, or none>
    NOTES: <1-2 sentences: what you changed and anything the integrator should know>
    ```

---

**Integrating a return.** On `STATUS: done` with gates green: `git -C <WORKTREE> rebase
epic/<EPIC>` then `git -C <epic-worktree> merge --ff-only <EPIC>/<TASK>` (linear history; clean
when the Conflict Matrix said the files were disjoint). After a batch is all in, run the
aggregate gate on the epic branch and confirm exit code 0 + zero FAIL yourself before believing
it. Do NOT retire the tasks — hold on the epic branch until the human's own commit lands them
(Rule 0).
