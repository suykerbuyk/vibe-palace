---
argument-hint: <task-or-epic-name>
---
# Tasks: Read — print a task body verbatim

Print the raw markdown body of a single task or epic in {{PROJECT}}, exactly
as stored.

## Steps

1. Call `vp_get_task` with:
   - **project**: `{{PROJECT}}`
   - **task**: `$ARGUMENTS`

2. Print the returned markdown body **verbatim** — do not summarize, reformat,
   or truncate it. If the body arrives as a `content_uri` rather than inline,
   read the resource (`resources/read`) or page it via `vp_read_resource`, then
   print the full body.

`vp_get_task` finds the task across the active, done, and cancelled trees on
its own — do not guess a path or scan the filesystem for the file.
