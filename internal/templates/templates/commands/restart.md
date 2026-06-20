# Restart — Context Restoration

Restore full AI session context for the {{PROJECT}} project.

This command covers only the session-start **mechanics**. Standing
behavioral rules — pair-programming paradigm, investigation-first
workflow, task-management discipline, core principles — live in
`workflow.md`. Load that file via `vp_bootstrap_context` below and
follow it for the rest of the session.

## Step 1: Vault Sync (multi-machine)

Before loading context, sync the vault so you get the latest state
from other machines.

Run `vp vault pull` via Bash. It discovers configured remotes
automatically and pulls from each.

- If it reports "regenerated", also run `vp index` to rebuild
  auto-generated files.
- If it reports files needing manual review, inform the user before
  proceeding.
- If it fails (no remote, network error), warn and proceed — local
  state is still valid.

Do **not** fall back to raw `git -C <vault>` commands — not every AI
host supports arbitrary Bash, and `vp vault` covers every needed
operation.

### Surface preflight

After the vault sync, confirm this binary can safely write to the
vault before loading context or mutating any task. Run
`vp check --json` via Bash and parse the JSON: find the entry in
`checks[]` whose `name` is `"Surface"`.

- If its `status` is `"fail"`, the vault was last written by a newer
  `vp` binary than this host has installed. **Halt** — do not
  bootstrap context, sweep plans, or retire tasks — and surface that
  entry's `detail` field to the human verbatim. It names the version
  mismatch and the remediation: upgrade `vp`
  (`cd ~/code/vibe-palace && git pull && make install`), or override
  at risk with `VP_SURFACE_GATE=warn`.
- If its `status` is `"pass"` or `"info"`, proceed to the vault tidy
  below.

### Vault tidy (heal capture residue)

After the Surface preflight passes (and only then), sweep any
uncommitted capture artifacts left by prior sessions, crashes, or
other machines so the vault is clean before context loads. Pull ran
first, so this tidies the already-merged state.

Call `vp_vault_tidy` (the MCP tool — prefer it over Bash so the
command works in AI hosts without arbitrary-shell support; fall back
to `vp vault tidy` via Bash only if the tool is unavailable). It
commits **only** machine-generated capture artifacts (session
summaries, transcript archives, `.surface` stamps, knowledge-graph
entities/triples, drawers) and pushes to every configured remote,
degrading to a local-only commit when no remote is configured. It
**never** runs `git add -A`: non-artifact dirt is reported, never
committed.

If the result lists any **Reported** paths, surface them to the user
— they are unexpected vault dirt that needs human eyes (e.g. a stray
`Projects/<slug>/` scaffold from an accidental `vp init`). Then
proceed to Step 2.

## Step 2: Bootstrap Context

Call `vp_bootstrap_context` to load workflow, resume, active tasks,
and recent sessions in a single call.

Under `slim`, `resume` may arrive as a banner-led **excerpt** plus a
`resume_uri` (marked with the `⚠ excerpt` banner) rather than the full body;
`workflow` stays inline. When `resume` is an excerpt, read its `resume_uri`
(via `vp_read_resource`) before relying on full resume content.

After bootstrap, continue loading context in the order below.

## Step 3: Sweep Orphaned Plans

Check `~/.claude/plans/` for plan files from prior sessions (Claude
Code creates these when plan mode is used). Use the Glob tool with
pattern `*.md` and path set to the absolute expansion of
`~/.claude/plans/` — do **not** put the full path in the pattern when
also setting path.

For each file found:

- Read the plan to determine whether it belongs to the current
  project (look for references to project files, directories, or the
  project name).
- If it belongs to a **different** project, leave it in
  `~/.claude/plans/`.
- If it belongs to **this** project, create it as a task with a
  descriptive slug derived from the plan title (rules below), then
  delete the original file from `~/.claude/plans/` using `rm` via
  Bash.

### Slugification rules

1. Find the first markdown heading (`# ...` or `## ...`).
2. Strip common prefixes: `Plan:`, `Task:`, `Feature:`, `Bug:`,
   `Fix:`, `Implementation Plan:`.
3. Lowercase the remaining text.
4. Replace spaces and underscores with hyphens.
5. Remove all characters except `a-z`, `0-9`, and `-`.
6. Collapse consecutive hyphens; trim leading/trailing hyphens.
7. Truncate to 60 characters (break at a hyphen boundary if possible).
8. Fallback: if no heading or the result is empty, use the original
   filename without `.md`.

Example: `# Plan: Deprecate Agentctx Symlinks` →
`deprecate-agentctx-symlinks`.

Use `vp_manage_task` with `action: create`, `task` set to the derived
slug, and `content` set to the full plan file content.

Plans **must** live in `agentctx/tasks/`, never in `~/.claude/plans/`.
Summarize each plan's disposition (created as task, other project,
etc.) when done.

## Step 4: Auto-Retire Completed Tasks

List active tasks (exclude `done/` and `cancelled/` subdirectories).
For each task:

- Read its title and status line.
- Check `git log --oneline -20` for commits that clearly implement
  the task's objective (matching keywords from the title).
- **Auto-retire if** the status already says "Done"/"Complete", OR
  all checklist items (`- [x]`) are checked with none unchecked
  (`- [ ]`) **and** recent commits match the task's subject matter.
- **Never auto-retire if** unchecked checklist items remain, the
  status says "In Progress" or "Blocked", or no matching commits
  are found.
- For each retirement: use `vp_manage_task` with `action: retire`,
  then `vp_append_iteration` with a brief narrative noting which
  commits fulfilled the task.
- On uncertainty, report "Task {slug} may be complete — review
  recommended" and leave it active. False negatives are far better
  than false positives.

## Step 5: Structured Context (optional)

- Call `vp_get_project_context` for structured context (sessions,
  threads, decisions, friction trends, knowledge).
- Read `iterations.md` on demand only — not required for routine work.
- `doc/*.md` — stable reference (architecture, design, testing) —
  read on demand when needed.

## Step 6: Confirm and Recommend

Briefly confirm what you loaded and note the current state: test
count, open tasks, recent session activity, and what was last worked
on based on recent git history. If active task files exist,
summarize each with its priority and status, and recommend which to
start based on priority order and dependencies.

After this command, consult `workflow.md` (already loaded by
`vp_bootstrap_context`) for the standing rules that govern the rest
of the session.
