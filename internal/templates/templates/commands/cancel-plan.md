# Cancel Plan — Task Cancellation

Cancel a planned task that was investigated and found not worth
implementing.

This preserves the analysis for future reference — preventing the
same task from being re-proposed later — while cleanly removing it
from active work. This mechanism is how future sessions (in the same
project **or a different one**) discover that a task was already
evaluated and why it was rejected.

## Inputs

An optional argument names the task file to cancel (without path or
extension, e.g. `/cancel-plan cloud-transcription`). If no argument
is given, follow the disambiguation steps below.

## Step 1: Identify the Task to Cancel

Use `vp_list_tasks` to list all active tasks.

- **If 0 tasks exist**: tell the user there are no active tasks to
  cancel. Stop here.
- **If 1 task exists**: proceed with that task. Show its name and
  status, and confirm with the user before continuing.
- **If 2+ tasks exist**: present a numbered list showing each task's
  name, priority, and status. Ask the user which one to cancel. Wait
  for their response before continuing.

If an argument was provided, match it against the task filenames.
If no match, show what's available and ask the user to clarify.

## Step 2: Draft Cancellation Rationale

Based on the conversation context (the investigation, analysis, or
discussion that led to cancelling), draft a concise cancellation
rationale (2-4 sentences). Focus on **why** the task isn't worth
doing — not what it proposed.

Present the draft to the user and ask them to either:

- Accept it as-is
- Edit or rewrite it

Wait for their response before continuing.

## Step 3: Update the Task File

Use `vp_get_task` to read the task file. The body may arrive as a `content_uri`
(with `include_content=false`) rather than inline; read the resource
(`resources/read`) or page it via `vp_read_resource` to get the full body before
editing. Make these changes:

1. Update the **Status** line to `Cancelled (rev N+1)` (increment
   the existing rev).
2. Add a **Cancelled** date line (today's date, format: YYYY-MM-DD).
3. Add a `## Cancellation Rationale` section immediately after the
   metadata block (Status/Created/Priority lines), containing the
   approved rationale.

## Step 4: Move to cancelled/

Use `vp_manage_task` with `action: cancel` to move the task to
`cancelled/`.

## Step 5: Append Iteration Entry

Use `vp_append_iteration` to append a brief entry:

- **Title**: `Cancelled: {task name}`
- **Narrative**: `Investigated {task name}; cancelled. {One-sentence
  rationale summary}. See tasks/cancelled/{filename} for the full
  analysis.`

## Step 6: Update knowledge.md

Read the project's `knowledge.md` with `vp_get_knowledge`. Then use
the `Edit` or `Write` file tools to add a line under an appropriate
heading (create a `## Cancelled Plans` section if one doesn't
exist):

```
- **{Task name}** cancelled (YYYY-MM-DD) — {one-sentence reason}.
  See `tasks/cancelled/{filename}`.
```

Resolve the `knowledge.md` path as
`<vault_path>/Projects/<project>/knowledge.md` (read `vault_path`
from `~/.config/vibe-palace/config.toml`; get the project name from
the first line of `vp inject` output, `# Context: {name}`).

This prevents future sessions — in this project **or any other** —
from re-proposing the same work.

## Step 7: Update resume.md

Using `vp_update_resume` (read the resume with `vp_get_resume` first and
pass its `sha256` as the REQUIRED `expected_sha256` guard; on a
`"conflict":true` error, re-read and recompose rather than forcing):

- Remove or update any reference to the cancelled task in the Open
  Threads section.
- Update iteration count if changed.
- Keep `resume.md` thin — just remove the pointer, don't add
  cancellation details.

## Step 8: Confirm

Report what was done:

- Task file updated and moved to `cancelled/`
- `iterations.md` entry appended
- `knowledge.md` updated under Cancelled Plans
- `resume.md` cleaned up

Note that vault files were updated via MCP tools and will be synced
on the next `/wrap`.
