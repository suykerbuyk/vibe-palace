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

     THE THREE STATES. Every `##` section below is in exactly one of three states,
     and the state is declared HERE, in the artifact, by an HTML-comment marker on
     the line under the heading. The two pinned sections below show the literal
     syntax; it is not repeated inside this comment, because a marker written here
     would close this comment block early.

     vp:pin         ALWAYS-INLINE. Survives the vp_bootstrap_context token shed
                    ladder no matter how tight the budget gets. Everything else is
                    shed to the resume_uri when the payload will not fit.
     vp:disposable  SAFE TO DROP. Pure navigation and pointers — an agent that loses
                    it re-derives it from a tool call, at no cost.
     no marker      LIVE STATE. Nobody has ruled on this section yet. Absence is NOT
                    a value, and silence is NOT consent to drop: the section stays
                    live until someone decides otherwise, and the `pin-coverage`
                    check names it by heading until they do. Reach that check with
                    the `vp_check` MCP tool (selector `pin-coverage`) — that is the
                    host-agnostic form, and the only one available to an agent
                    without a shell; `vp check --check pin-coverage` is the CLI
                    equivalent where one is.

     Pin ONLY what an agent must not act without — the rules that stop it corrupting
     the vault. Narrative, history and status are NOT that, and pinning them defeats
     the mechanism: a resume that pins everything sheds nothing.

     Mark disposable ONLY what genuinely costs nothing to lose. Current State and
     Open Threads below are deliberately left UNMARKED: they are live state, not
     reference, and declaring them sheddable is how a session drops the thread it
     was about to pull. -->

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
<!-- vp:disposable -->

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
