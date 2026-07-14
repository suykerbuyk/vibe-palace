---
type: project-resume
project: {{PROJECT}}
---

# {{PROJECT}} — Working Context

<!-- KEEP THIS FILE THIN. resume.md is a gateway to project context, not a diary.
     - Stable architecture, design decisions, test inventories -> doc/
     - Completed iteration narratives -> iterations.md
     - Active work items -> tasks/ directory
     Only current state, open threads, and pointers to deeper context belong here.

     THE PIN MARKER. A section marked `<!-- vp:pin -->` is ALWAYS-INLINE: it survives
     the vp_bootstrap_context token shed ladder no matter how tight the budget gets.
     Everything else is shed to the resume_uri when the payload will not fit.

     Pin ONLY what an agent must not act without — the rules that stop it corrupting
     the vault. Narrative, history and status are NOT that, and pinning them defeats
     the mechanism: a resume that pins everything sheds nothing. -->

## What This Project Is
<!-- vp:pin -->

<!-- Brief description of the project: purpose, stack, key commands. -->

## Project-Specific Behavioral Notes
<!-- vp:pin -->

<!-- Gotchas that must reach EVERY agent on EVERY host, because getting one wrong
     corrupts the vault or wastes a session. Keep terse. Prune a note only when it
     becomes FALSE, never merely old. This section is load-bearing for correctness —
     it is why the pin marker exists. -->

## Current State

- **Phase**: Initial setup
- **Date**: {{DATE}}
- **Tests**: 0 passing

## Open Threads

<!-- Active work items, unresolved questions, things to pick up next.

     SHAPE: a flat BULLET LIST. Not `###` sub-headings — bullets are canonical.
     Each bullet is a POINTER, not a narrative: a bold title, 2-4 sentences of
     why it matters, and a link to the task file where the detail actually
     lives. Delete a bullet when its thread resolves; the narrative belongs in
     iterations.md, not here.

     Edit these bullets surgically with vp_vault_read + vp_vault_edit (anchor
     on the bullet itself). Follow the exemplar below: -->

- **`some-task-slug`** (priority, filed iter N) -- one or two sentences naming
  the problem and why it is not yet closed. A third sentence for the constraint
  or gotcha that the next session would otherwise rediscover the hard way. See
  `tasks/some-task-slug.md`.

## Reference Documents

| Document | Access | Purpose |
|----------|--------|---------|
| resume.md | `vp_bootstrap_context` | This file — current state and navigation |
| workflow.md | `vp_bootstrap_context` | AI workflow rules and pair programming paradigm |
| iterations.md | `vp_get_project_context` | Append-only archive of iteration narratives |
| tasks/ | `vp_list_tasks` / `vp_get_task` | Active task files; tasks/done/ for completed |
