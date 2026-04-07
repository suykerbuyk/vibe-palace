# Restart — Context Restoration

Restore full AI session context for the {{PROJECT}} project.

## Steps

1. Call `vp_bootstrap_context` to load workflow, resume, active tasks, and
   recent sessions in a single call.

2. Review active tasks and their priorities. Summarize each with status.

3. Check `git log --oneline -10` for recent commits to understand what was
   last worked on.

4. Confirm what you loaded: test count, open tasks, recent session activity,
   and what was last worked on.

5. Recommend which task to start based on priority order and dependencies.
