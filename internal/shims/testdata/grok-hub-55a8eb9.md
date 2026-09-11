---
name: vpc
description: Vibe-palace command hub. Naked /vpc lists all commands via vp_cmd {}; /vpc <cmd> <args> dispatches via vp_cmd name=<cmd>. Use for restart, wrap, review-plan, execute-plan, cancel-plan and other vibe-palace operations.
metadata:
  short-description: "Vibe-palace command hub (/vpc)"
---

<!-- vibe-palace:shim v=1 sha=7b5690c -->
# Vibe-Palace Command Hub (/vpc)

A single `/vpc` slash command that mirrors the Claude `.claude/commands/vpc-*.md`
shims. It lists vibe-palace commands and dispatches them through the `vp_cmd`
MCP tool. Grok skills take arguments, so this one skill replaces every
per-command shim.

## Tool

Use the qualified MCP tool name `vp_cmd`. If it is not already available in
this session, load its schema first (search your tool list for `vp_cmd`), then
call it. Never guess parameter names — use only the schema the tool exposes.

## Naked `/vpc` — list commands

Call `vp_cmd` with empty input `{}`. Do NOT pass `project` — let `vp_cmd`
resolve it from the working directory / `.vibe-palace.toml`. Present the returned
commands (name, source, brief) to the user and offer to run one.

## `/vpc <cmd> <args>` — dispatch a command

Parse the first word of the argument as the command name (e.g. `review-plan`)
and treat the rest as its arguments. Call `vp_cmd` with `name="<cmd>"`. Do
NOT pass `project` — `vp_cmd` resolves it, exactly as the Claude `vpc-*`
shims do, which keeps this portable across projects. Then **follow the returned
instructions verbatim** — do not summarize; execute every step as written.

## Reading tasks (review-plan, cancel-plan, execute-plan)

For the task-reading commands `review-plan`, `cancel-plan`, and
`execute-plan` the argument is a task name (e.g. `/vpc review-plan <task-name>`).
BEFORE acting, call `vp_get_task` with the resolved `project` and `task` to
read the task. For a large task body, call `vp_get_task` with
`include_content=false` — it drops the big inline body and returns a
`content_uri` plus a short `excerpt` — then page the full body with
`vp_read_resource(uri, offset, limit)`, advancing `offset` by the
returned `offset+length` until `eof`. Do NOT assume your client
surfaces `resources/read` to the model; `vp_read_resource` is a tool you
can always call. Task files live ONLY in the vault and are reachable solely
through the MCP task tools and these `vibe-palace://` resource URIs.
NEVER grep or scan the filesystem for task files, never fall back to stale
resume prose, and never write a task to a repo-relative
`tasks/` path. If a task tool is not loaded, load its schema first, then call
it.

## Session start

When restoring context (e.g. `/vpc restart`), call `vp_bootstrap_context`.
`resume` and `workflow` arrive whole on every transport. If the payload is
over its token budget the ladder reduces `resume` to its pinned sections and
SAYS SO — `budget.shed` names `resume->pinned` and the body opens with a
`⚠ pinned sections only` banner. Only then read `resume_uri` via
`vp_read_resource`. An absent `budget` means nothing was reduced.

## After execution

Confirm what was done. For a review or plan, ask whether to proceed with
implementation. Update task status in the vault via the MCP task tools when
appropriate.
<!-- vibe-palace:shim-end -->
