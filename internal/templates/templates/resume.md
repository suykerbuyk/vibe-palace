---
type: project-resume
project: {{PROJECT}}
---

# {{PROJECT}} — Working Context

<!-- KEEP THIS FILE THIN. resume.md is a gateway to project context, not a diary.
     - Stable architecture, design decisions, test inventories -> doc/
     - Completed iteration narratives -> iterations.md
     - Active work items -> tasks/ directory
     Only current state, open threads, and pointers to deeper context belong here. -->

## What This Project Is

<!-- Brief description of the project: purpose, stack, key commands. -->

## Project-Specific Behavioral Notes

<!-- Gotchas that must reach EVERY agent on EVERY host, because getting one wrong
     corrupts the vault or wastes a session. Keep terse. Prune a note only when it
     becomes FALSE, never merely old. This section is load-bearing for
     correctness. -->

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

<!-- A pointer table, and nothing but: every row names an artifact and the tool
     that fetches it. Losing it costs an agent nothing — the artifacts are still
     there and the tools still list them — so it is the one section here that is
     honestly safe to shed. Keep it that way: the moment a row carries content
     that exists ONLY here, this marker is a lie. -->

| Document | Access | Purpose |
|----------|--------|---------|
| resume.md | `vp_bootstrap_context` | This file — current state and navigation |
| workflow.md | `vp_bootstrap_context` | AI workflow rules and pair programming paradigm |
| iterations.md | `vp_get_iteration` (n or recent+max_bytes); `vibe-palace://iteration/{project}/{n}` | Append-only archive of iteration narratives. Structure-aware read — never load the whole file via `vp_vault_read` for a single N |
| tasks/ | `vp_list_tasks` / `vp_get_task` | Active task files; tasks/done/ for completed |
