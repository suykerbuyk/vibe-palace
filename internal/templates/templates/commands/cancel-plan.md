# Cancel Plan — Task Cancellation

Cancel a planned task that was investigated and found not worth
implementing.

This preserves the analysis for future reference — preventing the
same task from being re-proposed later — while cleanly removing it
from active work. This mechanism is how future sessions (in the same
project **or a different one**) discover that a task was already
evaluated and why it was rejected.

The task-management discipline that governs cancellation — nothing is
cancelled without explicit human approval — is part of the Vibe-Palace
doctrine, served on demand from the binary; if this session has not
loaded it yet, fetch it with `vp_get_doctrine` first.

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

Edit `knowledge.md` **surgically**, exactly as Step 7 edits `resume.md`: read the
raw bytes with `vp_vault_read` on `Projects/{{PROJECT}}/knowledge.md`, then make
the change with `vp_vault_edit`, passing the `sha256` from the read as
`expected_sha256`. Chain the sha the edit returns into any follow-up edit. On a
sha conflict, re-read and recompose — **never force**.

`{{PROJECT}}` is a placeholder token the server resolves to this project — do not
compute the vault path or look up the project name yourself, and do **not** reach
for `vp_get_knowledge` here: that returns the knowledge *graph*, not the prose
file you are editing.

Add a line under an appropriate heading (create a `## Cancelled Plans` section if
one doesn't exist):

```
- **{Task name}** cancelled (YYYY-MM-DD) — {one-sentence reason}.
  See `tasks/cancelled/{filename}`.
```

Anchor the `vp_vault_edit` `old_string` on the heading (or an adjacent line) so
the insertion lands in the right place; this is a single-line addition, not a
whole-file rewrite.

This prevents future sessions — in this project **or any other** —
from re-proposing the same work.

## Step 7: Update resume.md

Edit `resume.md` **surgically**, exactly as `/wrap` does: read the raw bytes
with `vp_vault_read` on `Projects/{{PROJECT}}/resume.md`, then make each change
with `vp_vault_edit`, passing the `sha256` from the read as `expected_sha256`.
Chain the sha the edit returns into the next edit. On a sha conflict, re-read
and recompose — **never force**.

> **Never source write-back text from `vp_get_resume`.** Its body is
> placeholder-**expanded** (the resolver substitutes the double-brace tokens)
> while its `sha256` is over the **raw** bytes. An `old_string` copied from it
> will not match disk wherever a token lives, and a whole body composed from it
> passes compare-and-set and **silently bakes the expanded values onto disk**,
> destroying the live tokens. `vp_get_resume` reads context; `vp_vault_read`
> supplies text you intend to edit.

Both edits here are single-line anchors — do not reach for a whole-file rewrite:

- **Remove the cancelled task's Open Threads bullet.** Anchor `old_string` on the
  bullet itself (leading newline through its last continuation line) and replace
  with `""`. Open Threads is a bullet list.
- **Update the iteration count** if it changed: anchor on the existing
  `- **Iterations:** N` line and replace it in place.
- Keep `resume.md` thin — just remove the pointer, don't add cancellation
  details.

`vp_update_resume` is **not** the path here. It is a full-file regeneration and
migration tool; nothing in this command needs it.

## Step 8: Confirm

Report what was done:

- Task file updated and moved to `cancelled/`
- `iterations.md` entry appended
- `knowledge.md` updated under Cancelled Plans
- `resume.md` cleaned up

Note that vault files were updated via MCP tools and will be synced
on the next `/wrap`.
