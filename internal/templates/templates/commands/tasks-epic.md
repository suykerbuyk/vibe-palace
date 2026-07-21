---
argument-hint: <epic-slug>
---
# Tasks: Epic — one epic's subtree

Show the subtree of tasks rooted at an epic (or story) in {{PROJECT}},
re-rooted so it reads as its own tree.

## Steps

1. Call `vp_list_tasks` with:
   - **project**: `{{PROJECT}}`
   - **epic**: `$ARGUMENTS`

2. Render the returned `tasks` array. Each item carries a `role`
   (`epic` / `story` / `task`) the server already derived — use it to indent or
   label rows. Show each task's title, priority, and status.

The server has already resolved the subtree and every task's role
(per ADR-006, "derive, don't ask"). Render what it returns — do not recompute
parentage or re-scan the tasks tree.
