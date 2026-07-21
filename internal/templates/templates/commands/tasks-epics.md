# Tasks: Epics — the epic roll-up

List every epic in {{PROJECT}} as a roll-up table — open/total counts,
priority, and status.

## Steps

1. Call `vp_list_tasks` with:
   - **project**: `{{PROJECT}}`
   - **epics_only**: `true`

2. Render the returned `epics` array as a table with columns:
   **slug**, **open/total** (each epic's `open` over its `total`),
   **priority**, **status**, **title**.

The server has already derived the epic set and each epic's transitive
open/total counts (per ADR-006, "derive, don't ask"). Render what it
returns — do not re-scan the tasks tree or recompute the counts.
