# Restart — Context Restoration

Restore full AI session context for the {{PROJECT}} project.

This command covers only the session-start **mechanics**. Standing
behavioral rules — pair-programming paradigm, investigation-first
workflow, task-management discipline, core principles — are the
Vibe-Palace **doctrine**, served on demand from the binary. Fetch it
with `vp_get_doctrine` in Step 2 below and follow it for the rest of
the session; the project's `workflow.md` (loaded by
`vp_bootstrap_context`) carries only project-specific patterns.

## Step 1: Vault Sync (multi-machine)

Before loading context, run the surface preflight, then sync the vault
so you pick up the latest state from other machines. Every operation in
this step is an MCP tool call — no Bash is required, so the step works
identically on AI hosts without arbitrary-shell support (Claude, Grok,
Zed).

### Surface preflight (run first)

Before pulling, loading context, or mutating anything, confirm this
binary can safely write to the vault. Call `vp_surface_check` (the MCP
tool). It returns the same surface verdict a mutating vault write would
face — `status` is `"pass"`, `"fail"`, or `"info"` — without loading the
embedder model, so it stays near-instant even on a cold cache.

- If `status` is `"fail"`, the vault was last written by a newer `vp`
  binary than this host has installed. **Halt the entire restart** — do
  not pull, tidy, bootstrap context, sweep plans, or retire tasks — and
  surface the tool's `details` lines to the human verbatim. They name
  the version mismatch and carry the remediation (the `git pull && make
  install` upgrade, plus the at-risk override); relay them as-is rather
  than paraphrasing.
- If `status` is `"pass"` or `"info"`, proceed to the vault sync below.

### Vault sync (pull)

Call `vp_vault_sync` with `action: "pull"` (the MCP tool). It discovers
configured remotes automatically and pulls from each, self-healing
stale local template copies before the merge. It returns
`{status, action, output}`, where `output` is the captured per-remote
git text (`[pull <remote>] …` lines, plus any `[heal] …` lines).

- If `output` shows the pull merged remote changes (anything other than
  "Already up to date"), rebuild the semantic search index with
  `vp_refresh_index` (pass the project slug) so newly-pulled content is
  searchable — the MCP equivalent of the old `vp index`.
- If `output` shows a merge conflict or files git flags for manual
  resolution (e.g. `CONFLICT`, "Automatic merge failed"), inform the
  user before proceeding.
- If the call returns an error (no remote configured, network failure),
  warn and proceed — local state is still valid.

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

`resume` and `workflow` arrive **whole**, on every transport. The one case
where `resume` is reduced is token-budget pressure, and it announces itself
twice: `budget.shed` names `resume->pinned`, and the body opens with a
`⚠ pinned sections only` banner. Only then read `resume_uri` (via
`vp_read_resource`) for the full body. **An absent `budget` means nothing was
reduced** — there is no longer a second, silent path that could shorten the
resume without saying so.

Then fetch the full operating doctrine with `vp_get_doctrine` (pass the
project slug). The inline `workflow` is deliberately **thin** — it carries
only this project's patterns plus a minimal pointer at the doctrine — so
the doctrine fetch is not optional: it is where the standing behavioral
rules arrive.

After bootstrap and the doctrine fetch, continue loading context in the
order below.

## Step 3: Sweep Orphaned Plans

Check your host's local plan/scratch directory for plan files from
prior sessions (on Claude Code this is `~/.claude/plans/`; other hosts
have their own, and some have none — if there is no such directory,
skip this step). That directory is **scratch only, never the source of
truth**: plans live in the vault, reached only through `vp_manage_task`.
Use the Glob tool with pattern `*.md` and path set to the absolute
expansion of that directory — do **not** put the full path in the
pattern when also setting path.

For each file found:

- Read the plan to determine whether it belongs to the current
  project (look for references to project files, directories, or the
  project name).
- If it belongs to a **different** project, leave it where it is.
- If it belongs to **this** project, create it as a task with a
  descriptive slug derived from the plan title (rules below), then
  delete the original scratch file.

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

### Strip the plan's metadata block before creating the task

**This is mandatory — skipping it makes the create call fail.** Agent-written
plans idiomatically open with a metadata block:

    # Some Plan Title
    **Status:** Draft
    **Priority:** High

`vp_manage_task` with `action: create` **supplies that block itself**, and it
**rejects** any `content` that carries its own `**Status:**` line or its own
top-level `# ` heading. So before you pass the plan body:

- **Delete the leading metadata block** — the `# Title` line, the
  `**Status:**` line, and the `**Priority:**` line — and pass only the body
  that follows.
- Keep the title's meaning in the **slug** (and, if you like, in a `## `
  subheading inside the body).
- (H1-shaped shell comments *inside* a fenced code block — ``` or ~~~ — are
  fine and are not treated as headings.)

**Why:** two `**Status:**` lines in one task file means the reader and the
writer disagree about which one is real. `vp_get_task` and `vp_list_tasks`
would report one status while `vp_manage_task update_status` rewrites the
other. The create call refuses the duplicate rather than letting the file rot.

Then use `vp_manage_task` with `action: create`, `task` set to the derived
slug, and `content` set to the **stripped** plan body.

Plans **must** live in the vault under `Projects/<slug>/tasks/`, reached only
via the MCP task tools — never in a host's local plan/scratch directory.
Summarize each plan's disposition (created as task, other project,
etc.) when done.

## Step 4: Report Retirement Candidates (never retire)

**Never retire a task here. Never retire a task anywhere without explicit
human approval.** The operator's standing Rule 0 is: *nothing is ever done
until the human says it is done.* A session-start sweep that retires tasks on
its own reasoning violates that directly — and a plan that was written but
never implemented would get filed into `done/` at the next session start,
where nobody would look at it again.

So this step **reports**, it does not act:

- List active tasks with `vp_list_tasks` (excludes `done/` and `cancelled/`).
- For each, read its title and status line, and check
  `git log --oneline -20` for commits that plausibly implement it.
- If a task **looks** complete (status says "Done"/"Complete", or every
  checklist item is checked and recent commits match its subject), add it to
  a **"possible retirement candidates"** list you show the human.
- Present that list and stop. The human decides. If they approve a
  retirement, then — and only then — call `vp_manage_task` with
  `action: retire` and `approved_by_human: true`, and record it with
  `vp_append_iteration`.
- Report candidates as "Task {slug} may be complete — review recommended".
  Leaving a finished task active costs nothing; burying an unfinished one in
  `done/` costs a whole plan.

## Step 5: Structured Context (optional)

- Call `vp_get_project_context` for structured context (sessions,
  threads, decisions, friction trends, knowledge).
- Read `iterations.md` on demand only — not required for routine work.
- `doc/*.md` — stable reference (architecture, design, testing) —
  read on demand when needed.
- **Vault-audit staleness rides in the `vp_bootstrap_context` payload and is
  silent when fresh.** If it fired, that is your cue to run `vp audit vault`
  (or the `/vpc-vault-audit` command for the full adversarial pass). The audit
  is advisory — a FAIL exits 0 — so it never blocks the restart; surface any
  new/stale/unknown findings to the human. Do NOT run it unprompted when the
  payload is quiet.

## Step 6: Confirm and Recommend

Briefly confirm what you loaded and note the current state: test
count, open tasks, recent session activity, and what was last worked
on based on recent git history. If active task files exist,
summarize each with its priority and status, and recommend which to
start based on priority order and dependencies.

After this command, follow the doctrine (fetched with `vp_get_doctrine`
in Step 2) for the standing rules that govern the rest of the session,
and the project's `workflow.md` (already loaded by
`vp_bootstrap_context`) for this project's own patterns.
