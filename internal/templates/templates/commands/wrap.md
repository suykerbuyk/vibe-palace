# Wrap — Session Capture

Update `resume.md` and its dependent documents to reflect the current
state of the {{PROJECT}} project, record a session note, and sync the
vault.

`resume.md` is a **thin gateway** — not a diary. Keep it focused on
current state, open threads, and pointers. Stable reference material
belongs in `doc/` under source control. Completed work details belong
in `iterations.md` only.

Do not ask for confirmation — just do the updates, stage the files,
show what changed, and note that the user should review before
committing.

## Step 1: Quality Gates

Before capturing anything, the working tree must be clean:

- All code compiles without warnings, errors, or diagnostics.
- All unit and integration tests pass.

If any gate fails, **stop and fix before continuing**. A wrap that
records a broken state is worse than no wrap.

### Surface preflight

Before capturing the session, updating resume, or staging anything,
confirm this binary can safely write to the vault. Call
`vp_surface_check` (the MCP tool). It returns the same surface verdict a
mutating vault write would face — `status` is `"pass"`, `"fail"`, or
`"info"` — without loading the embedder model, so it stays near-instant
even on a cold cache.

- If `status` is `"fail"`, the vault was last written by a newer `vp`
  binary than this host has installed. **Halt** — do not capture the
  session, update resume, append iterations, or stage any file — and
  surface the tool's `details` lines to the human verbatim. They name
  the version mismatch and carry the remediation (the `git pull && make
  install` upgrade, plus the at-risk override); relay them as-is rather
  than paraphrasing.
- If `status` is `"pass"` or `"info"`, proceed to Step 2.

## Step 2: Capture the Session

Call `vp_capture_session` with:

- **project**: `{{PROJECT}}`
- **summary** (required): what was accomplished
- **tag**: `implementation` | `debugging` | `refactor` | `exploration` | `review` | `docs` | `planning`
- **decisions**: key technical decisions made
- **files_changed**: files created or modified
- **open_threads**: unresolved items or follow-up work

Write summaries that help a developer resuming this work tomorrow.

## Step 3: Update Resume

**Prune before you append.** `resume.md` must stay a thin gateway — every
wrap that only adds bloats it and taxes `vp_bootstrap_context` at session
start. The full record already lives elsewhere (`iterations.md`,
`tasks/done/`, `tasks/cancelled/`), so the gateway's growing sections are
**bounded** — trim them *every* wrap, BEFORE adding anything new. Compressing
rows is not enough; these tables are append-only by nature and must be
**capped**, or they regrow indefinitely:

- **Project History** — keep only the **most recent 15 rows**. Delete older
  rows outright; their full narrative is already in `iterations.md`. Compress
  any surviving row that spills past one line to the
  `| # | Summary | Key Changes |` one-liner.
- **Completed Plans** — keep only the **most recent ~12 rows**, each a true
  **one line** (`| Task | Iteration | File |`). If a cell has grown into a
  paragraph, cut it to the slug plus a few words — the full content is in
  `tasks/done/<slug>.md`. Delete older rows; the directory is the index.
- **Known Issues** — **delete** an entry the moment it is resolved. NEVER keep
  a "RESOLVED iter N …" narrative inline; that is history, and history lives
  in `iterations.md`. This section holds only *currently-open* issues.
- **Open Threads** — delete every thread now done or cancelled. Do **not**
  leave a `~~struck-through~~` "DONE iter N" entry inline; move its pointer to
  **Completed Plans** / **Cancelled Plans** and its narrative to
  `iterations.md`. Open Threads holds only genuinely-open work.
- **Current State** — keep to terse bullets + pointers (counts, what's live,
  links to `doc/`); move per-iteration detail to `iterations.md`.

Target: keep `resume.md` **under ~25 KB**. Pruning to the caps above is not
optional and is never deferred to "later" — a wrap that adds a row without
trimming past-cap rows is the bug this section exists to prevent.

1. Read the current resume with `vp_get_resume`.
2. Compare against the actual codebase state (files, tests,
   architecture).
3. Update with `vp_update_resume`: current state (test count,
   iteration count), open threads.

**Do not** add file inventories, architecture diagrams, design
decisions, or module tables to `resume.md` — those belong in `doc/`
files.

4. Add **one terse row** to the Project History table in `resume.md`.
   The table is a scannable **index**, not a diary — the full narrative
   goes ONLY to `iterations.md` (Step 4), never duplicated here. Format:
   `| # | Summary | Key Changes |` where Summary is the
   feature/phase in a few words and Key Changes is a short comma-list of
   the most salient artifacts. Keep the whole row to a single line of a
   few hundred characters at most. **Do not paste the iteration narrative
   into this table** — that is what bloats `resume.md` and taxes every
   `vp_bootstrap_context` at session start. After adding the row, trim the
   table back to the **15-row cap** (see *Prune before you append*).
5. Keep the gateway sections current (all in `resume.md`, all terse):
   - **Quick Reference** — update only if build/test/run commands changed.
   - **Completed Plans** — when a task is retired (Step 6), add a one-line
     row `| Task | Iteration | File |` pointing at `tasks/done/<slug>.md`.
     The full task content moves to `tasks/done/`; the narrative goes to
     `iterations.md` (Step 4). Do not paste task content here. Then trim the
     table back to the **~12-row cap** (see *Prune before you append*).
   - **Cancelled Plans** — when a plan is cancelled, add a row with the
     rejection reason and the `tasks/cancelled/<slug>.md` pointer.
   - **Open Threads** — add genuinely-open follow-ups; **delete** entries
     the moment they are done or cancelled (see *Prune before you append*
     above) instead of striking them through.
   - **Known Issues** — add an entry when a standing issue is found;
     **delete** it the moment it is resolved (do not keep a "RESOLVED" note
     inline — that history belongs in `iterations.md`).

## Step 4: Append Iteration Narrative

Use `vp_append_iteration` to describe what changed this session and
why (past tense, technical detail).

## Step 5: Update Stable Docs (if changed)

If stable project documentation changed (architecture, design
decisions, test structure), update the relevant `doc/` file directly.

## Step 6: Retire Completed Tasks

Use `vp_list_tasks` to check each active task against the session's
work and resume.md history. If a task has been implemented and
committed, use `vp_manage_task` with `action: retire` to move it to
`tasks/done/`, then add its one-line row to the **Completed Plans**
table in `resume.md` (Step 3.5).

## Step 7: Update commit.msg (Two-Copy Workflow)

There are **two** `commit.msg` copies and **both** must be kept in
sync:

1. **Vault archive** — canonical; **committed** to the vault repo each
   wrap and synced across machines. `git log -p` over it is the
   permanent history of every commit message (future MCP data-mining):
   `<vault_path>/Projects/<project>/commit.msg`
2. **Project-root working copy** — gitignored, host-local scratch; the
   path that `git commit -F commit.msg` reads when the user runs it,
   consumed once and never committed:
   `<project_root>/commit.msg`

### Workflow

Use the typed writer `vp_ingest_commit_msg`: it reads the
**project-root** copy off disk and writes the vault copy atomically,
surface-stamped. Author the message once in the project root, then
ingest — do **not** hand-copy the vault file or `cp` between the two.

1. **Read** the project-root `commit.msg` first with the `Read` tool.
   It almost always exists from a prior wrap, and the `Write` tool
   refuses to overwrite a file it has not read this session — so this
   Read is mandatory. It also lets you confirm you are not clobbering
   anything unexpected before replacing it.
2. **Write** the new message to the project-root `commit.msg` via
   `Write`.
3. Call **`vp_ingest_commit_msg`** with `project_path` set to the
   local project repo root. It reads the file you just wrote and emits
   the vault copy (`<vault_path>/Projects/<project>/commit.msg`)
   atomically and surface-stamped — there is no `content` parameter,
   so the message is emitted exactly once.
4. **Verify** both copies match (compare byte counts / first line).
   The project-root copy is what `git commit -F commit.msg` reads; the
   vault copy is the archive Step 9 commits. A missing or stale
   project-root copy means `git commit -F commit.msg` would fall back
   to a stale version or fail.

The **project-root** copy is host-local scratch — never stage or
commit it in the project repo. The **vault** copy is the committed
archive: Step 9 syncs it (`vp_vault_sync` with the vault `commit.msg`
among its paths), so each wrap records the new message in vault git
history.

**Failure mode to avoid:** do **not** hand-edit the *vault* `commit.msg`
directly — while editing vault files in sequence (resume.md,
iterations.md, tasks/done/) it sits right next to them and
pattern-matches as "the" commit.msg. Author the **project-root** copy
and let `vp_ingest_commit_msg` produce the vault copy; that keeps them
byte-identical and correctly surface-stamped. If you edit the vault
copy by hand instead, the project-root copy that `git commit -F` reads
silently diverges.

### commit.msg quality

Make the message **complete and standalone** in documenting all
code changes, features added, and bugs or warnings resolved. Don't
be terse — be verbose. The commit message is the project's history.

Do **not** add "Co-Authored-By" lines to commit messages or source
files.

## Step 8: Stage Project Files

Stage all modified and newly added project files using `git add`
with **explicit file paths** — never `git add -A` or `git add .`.
Only stage files inside the git repo.

Track files modified or created this session and `git add` each
path by name. Untracked files unrelated to the task (stray
`temp.txt`, etc.) must not be staged. After staging, run
`git status` and report what is staged vs. left untracked so the
user can verify before committing.

Only the vault repo has standing permission for autonomous commits
and `git add -A`.

## Step 9: Sync the Vault

Call `vp_vault_sync` (the MCP tool — prefer it over Bash so the
command works in AI hosts without arbitrary-shell support).

`vp_vault_sync` commits vault changes and pushes to every configured
remote in one call. If a push fails for one remote, it continues to
the next and surfaces the error at the end. If all pushes fail
(no remote, network error), warn and proceed — local state is still
valid.

Include `Projects/<slug>/commit.msg` among the synced paths — the vault
copy is the committed archive (Step 7), so each wrap records the new
commit message in vault git history. (The project-root copy stays
gitignored and is never synced.)

Include `Projects/<slug>/memory/` among the synced paths — AI memory is
user-persistent content like `resume.md` and `tasks/`, so wrap commits it
on the host-agnostic path (Grok/Zed never run `vp hook`). Uncommitted
memory at wrap time is **expected**, not a problem: Claude's SessionEnd
harvest auto-commits it and wrap commits it here, so preflight surfaces
memory dirt as a NOTE, never a blocker.

## Step 10: Vault Tidy (sweep capture artifacts)

After the narrative sync, sweep the machine-generated capture
artifacts this session produced — the `.surface` stamp churn from the
wrap itself, plus any session summaries, transcript archives, and
knowledge-graph / drawer writes that the narrative `vp_vault_sync`
(which commits only the explicit `--paths` you named) did not include.

Call `vp_vault_tidy` (the MCP tool — prefer it over Bash so the
command works in AI hosts without arbitrary-shell support; fall back
to `vp vault tidy` via Bash only if the tool is unavailable). It
commits **only** classified capture artifacts and pushes, never
`git add -A`; non-artifact dirt is reported, not committed.

If the result lists any **Reported** paths, surface them to the user
before finishing — they need human eyes.

## Step 11: Report

Report what was done:

- Session captured (`vp_capture_session` ID)
- `resume.md` updated
- `iterations.md` entry appended
- Tasks retired (if any)
- `commit.msg` updated in both locations (with verification)
- Project files staged (by path)
- Vault synced
- Vault tidied (capture artifacts swept; any reported dirt surfaced)

Note that the user should review the staged diff and the
`commit.msg` before running `git commit -F commit.msg`.
