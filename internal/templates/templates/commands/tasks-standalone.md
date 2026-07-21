# Tasks: Standalone — the no-epic bucket

List the standalone tasks in {{PROJECT}} — the tasks that are nobody's child
and nobody's parent.

## Steps

1. Call `vp_list_tasks` with:
   - **project**: `{{PROJECT}}`
   - **standalone**: `true`

2. Render the returned `tasks` array as a table. Each item carries a `role`;
   show each task's title, priority, and status.

The server derives the standalone bucket (per ADR-006, "derive, don't ask").
Render what it returns — do not re-scan the tasks tree.
