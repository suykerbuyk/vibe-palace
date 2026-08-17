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

> **Feature-branch commits use `/stage`, not `/wrap`.** `/stage` is the
> commit-preparation subset — a light gate, author `commit.msg`, `git add` by
> path — for each intermediate commit on a feature branch. `/wrap` runs ONCE,
> AFTER the branch is `--ff-only` merged to `main`: on the now-clean tree its
> Steps 7–8 (commit.msg, staging) self-skip and it is coherency-only. In the
> direct-to-main flow `/wrap` still does everything, including the staging that
> `/stage` would do.

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
- If `status` is `"pass"` or `"info"`, proceed to the hygiene checks
  below.

### Vault hygiene checks (advisory — reported, never a gate)

Wrap is where `resume.md` is **written**, so this is the moment a fresh
breach gets introduced — run the read-only diagnostic suite here, before
capturing anything, and carry its findings into Step 3. Call `vp_check`
(the MCP tool) with an **explicit** selector list:

```json
{"checks": ["vault-filesystem", "stray-scaffolds", "resume-caps", "resume-refs", "vault-abs-paths", "core-floor", "pin-coverage", "template-drift", "writer-identity", "stale-mcp"]}
```

Name that list deliberately. Do **not** omit the argument, and do
**not** add `surface`: `vp_surface_check` already ran above, so including
it would repeat a scan for no new information.

The result is `{status, summary, checks: [{name, status, summary,
details[]}]}`. Report the **per-check rows** to the human — for every row
that is not `"pass"`, give its name, its status, its summary, and its
`details` lines verbatim. The top-level `status` is an **advisory**
worst-of roll-up across independent scans that disagree about an absent
vault; key nothing off it.

- 🔴 **An `"info"` verdict is a REPORT, never a gate.** These are
  Info-or-Pass by design. The exception is `vault-filesystem`,
  which returns `"fail"` for a vault sitting on a filesystem that
  rejects `":"` in filenames (NTFS/exFAT) — a real finding, and still
  only a finding here: relocating a vault is a deliberate human move,
  not something a wrap performs or waits on. Whatever comes back, report
  it and **continue to Step 2 regardless**. Vaults carry standing
  `pin-coverage` and `resume-caps` findings today; a wrap that refused
  to proceed on those would strand every host.
- **Report, do not auto-fix.** The one exception is the `resume-refs`
  row: you are about to edit `resume.md` in Step 3 anyway, so a
  host-local path it names is fixed there under the guardrail bullet —
  not silently, and not by any other rewrite of the file.
- If the call itself errors (an older `vp` on this host may not carry
  the tool), say so once and continue — these are diagnostics, not a
  gate.

### Vault integrity (advisory — NOT a gate)

Optionally run `vp audit vault` (or `vp_audit_vault`). It is **advisory and
never blocks** — a FAIL exits 0, so it is *not* a quality gate and must not stop
the wrap. It catches vault-integrity drift the compile/test gates cannot see
(stranded transcripts, project-tree incoherence, resume over cap). Report any
new/stale/unknown findings to the human. **Do NOT apply archive backfills as
part of the wrap** — running `vp archive link` is the human's per-pair approval
(ADR-007). The staleness nag surfaces at the next bootstrap regardless, so this
is a convenience, not a requirement.

### Wrap readiness probe

Call `vp_preflight_wrap` with `project_path` set to the local project repo root
(any path inside it resolves). **`project_path` is required and the project is
never inferred from the server's working directory** — `vp mcp` is long-lived and
its cwd is the host's launch directory, not the repo you are working in.

It returns `{ok, warnings[], errors[], notes[]}`. **Only `ok: false` halts the
wrap** — that is a surface incompatibility, the same class the preflight at the
top of this step gates on. Warnings and notes never flip `ok`; report them and
continue.

Report every warning to the human. One of them needs an action rather than a
mention:

- **`commit_msg_unconsumed`** — `<project_root>/commit.msg` exists while the
  project tree is **clean**. That message was authored and never consumed by a
  commit, so the next `git commit -F commit.msg` would reland it on unrelated
  work. **Read the file and tell the human what it says and which commit it
  looks like it was meant for**, before Step 7 overwrites it. If this session
  wrote project code, Step 7 replaces it and the warning is resolved by the
  normal flow; if it did not, Step 7 self-skips and the stale file survives —
  say so explicitly, because nothing else will.

## Step 2: Capture the Session

Call `vp_capture_session` with:

- **project**: `{{PROJECT}}`
- **summary** (required): what was accomplished
- **tag**: `implementation` | `debugging` | `refactor` | `exploration` | `review` | `docs` | `planning`
- **decisions**: key technical decisions made
- **files_changed**: files created or modified
- **open_threads**: unresolved items or follow-up work
- **model**: the model identifier you are running as — **always pass it.** The session note's DoD requires `model`, and the field is caller-declared on the MCP path: only the SessionEnd hook derives it, and only on Claude. So on a hook-less host (Grok, Zed pane, HTTP serve) your self-report is the only source, and omitting it records the session **unmodelled**; on Claude Code the hook also fills it in, so supplying it is harmless
- **transcript**: when the host can supply the session transcript text, pass it (required for friction scoring and durable archive content on hook-less hosts)
- **archive_transcript**: `true` — for hook-less hosts (Grok, Zed pane, HTTP serve) / when not relying on SessionEnd; Claude Code no-ops this when the server can derive the host session id

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

### How to write resume.md: read raw, edit surgically, re-chain the sha

`resume.md` is edited **surgically**, one section at a time, with
`vp_vault_read` + `vp_vault_edit` under compare-and-set. There is no blind
whole-file rewrite in the wrap path.

1. **READ the raw bytes.** Call `vp_vault_read` on
   `Projects/{{PROJECT}}/resume.md`. It returns the file's **RAW** contents
   and a `sha256` computed over **those same raw bytes**. That text is what
   is actually on disk, so an `old_string` copied out of it will match, and
   that sha is the compare-and-set guard for the write.

   > **The trap — read this twice.** **NEVER use `vp_get_resume` (or
   > `vp_bootstrap_context`) as the source of text you intend to write
   > back.** Their bodies are placeholder-**EXPANDED** — the resolver
   > substitutes the double-brace tokens (`PROJECT`, `DATE`, `WING`, `ROOM`;
   > written here with names only, because a literal token in this file
   > would itself be expanded before you read it) — while their `sha256` is
   > computed over the **RAW** bytes. The two do not describe the same text.
   > So: an `old_string` copied from them **will not exist on disk** wherever
   > a token lives, and the edit hard-fails; and a whole body composed from
   > them **passes compare-and-set and silently bakes the expanded values
   > onto disk**, destroying the live tokens permanently. This project's
   > `resume.md` carries such tokens *today*. `vp_get_resume` is for
   > *reading* context. `vp_vault_read` is for text you intend to *edit*.

2. **EDIT one section at a time** with `vp_vault_edit`, passing
   `expected_sha256` from the read. `vp_vault_edit` does an exact
   `old_string` → `new_string` replace: it takes the per-path vault lock,
   re-hashes the file and compares the CAS **inside** the lock, writes
   atomically, and stamps `.surface`. Its failures are **LOUD** and they are
   the safety net:
   - `old_string` not found → error. Your anchor does not match the disk.
   - `old_string` occurs more than once (without `replace_all`) → error.
     Your anchor is ambiguous.
   - sha mismatch → error. The file moved under you.

   When an edit fails, **fix the anchor by making it more specific**
   (add the neighbouring line, extend the row) — **never** by broadening it
   until something matches, and never by falling back to a whole-file
   rewrite. A loud failure has told you your model of the file is wrong;
   re-read and look.

3. **RE-CHAIN the sha between edits.** Each successful `vp_vault_edit`
   changes the file, so the sha you read is now stale. `vp_vault_edit`
   returns `{bytes, sha256, replacements}` — its `sha256` is the digest of
   the **post-edit** file, so feed **that** value in as the
   `expected_sha256` of your next edit. (If you lose the chain, or anything
   else may have written, call `vp_vault_read` again.) **Never reuse a stale
   sha across edits**, and never pass `""` to opt out of the guard.

**On conflict: re-read and recompose. NEVER force.** A sha mismatch means
another writer landed while you were composing. Re-read `resume.md` with
`vp_vault_read`, re-derive your `old_string` anchors from the *new* body,
and resubmit with the new sha.

`vp_update_resume` is **not** the routine wrap path. It is what its own
description says: full-file **regeneration** and **migrations** — bootstrap
of a resume that does not exist yet, or a wholesale structural rewrite done
deliberately. Reach for it in this command only if there is no `resume.md`
at all.

### The four edit shapes

You now compose anchors by hand, so use these shapes. Prefer **line-oriented
anchors** — anchor on whole table rows and whole bullets, never on individual
cells. Table cells can contain backticked code spans and pipes, and
cell-level surgery breaks on them; whole-line anchoring is cheap insurance
against a class of failure you cannot see coming, not a claim that such rows
are common (they are not).

**1. Append a row to a table** — anchor on the **last existing row** and
re-emit it followed by the new row:

```
old_string: "| 41 | Vault CAS writes | vaultfs.Edit, resume_cas_test.go |"
new_string: "| 41 | Vault CAS writes | vaultfs.Edit, resume_cas_test.go |\n| 42 | Surgical wrap path | wrap.md, vault_file_tools.go |"
```

**2. Delete a row or a bullet** — anchor on the row/bullet **itself**,
including its leading newline, and replace with the empty string:

```
old_string: "\n| 26 | Old iteration long since narrated | foo.go, bar.go |"
new_string: ""
```

```
old_string: "\n- **Retire auto-close** — decide whether wrap may retire tasks (iter 38)."
new_string: ""
```

**3. Rewrite a row in place** — anchor on the **whole row**, identified by
its leading cell text, and emit the whole replacement row:

```
old_string: "| resume-cas-hardening | 44 | tasks/active/resume-cas-hardening.md |"
new_string: "| resume-cas-hardening | 45 | tasks/done/resume-cas-hardening.md |"
```

**4. Replace a section body** — anchor from the section heading through the
end of its body (a stable following heading is the cheapest terminator), and
emit heading + new body:

```
old_string: "## Current State\n\n- 412 tests passing; 44 iterations.\n- Vault writes go through vaultfs.\n\n## Open Threads"
new_string: "## Current State\n\n- 431 tests passing; 45 iterations.\n- Vault writes go through vaultfs; resume edits are CAS-guarded.\n\n## Open Threads"
```

`## Open Threads` is a **bullet list**, not a set of `###` blocks — a thread
is one bullet, so adding or removing one is shape 1 or shape 2, not shape 4.

### What goes in, what stays out

Compare the resume against the actual codebase state (files, tests,
architecture) and bring the gateway sections current.

**Do not** add file inventories, architecture diagrams, design
decisions, or module tables to `resume.md` — those belong in `doc/`
files.

- **Project History** — add **one terse row** (shape 1). The table is a
  scannable **index**, not a diary — the full narrative goes ONLY to
  `iterations.md` (Step 4), never duplicated here. Format:
  `| # | Summary | Key Changes |` where Summary is the feature/phase in a few
  words and Key Changes is a short comma-list of the most salient artifacts.
  Keep the whole row to a single line of a few hundred characters at most.
  **Do not paste the iteration narrative into this table** — that is what
  bloats `resume.md` and taxes every `vp_bootstrap_context` at session start.
  After adding the row, delete the overflow rows (shape 2) to hold the
  **15-row cap** (see *Prune before you append*).
- **Quick Reference** — update only if build/test/run commands changed.
- **Completed Plans** — when a task is retired (Step 6), add a one-line row
  `| Task | Iteration | File |` pointing at `tasks/done/<slug>.md` (shape 1).
  The full task content moves to `tasks/done/`; the narrative goes to
  `iterations.md` (Step 4). Do not paste task content here. Then delete the
  overflow rows (shape 2) to hold the **~12-row cap**.
- **Cancelled Plans** — when a plan is cancelled, add a row with the
  rejection reason and the `tasks/cancelled/<slug>.md` pointer (shape 1).
- **Open Threads** — add genuinely-open follow-up bullets (shape 1);
  **delete** a bullet (shape 2) the moment it is done or cancelled, instead
  of striking it through.
- **Known Issues** — add an entry when a standing issue is found; **delete**
  it (shape 2) the moment it is resolved (do not keep a "RESOLVED" note
  inline — that history belongs in `iterations.md`).
- **Current State** — terse bullets and pointers; a full rewrite of this
  section is shape 4.
- **No host-local or ephemeral paths** — `resume.md` is committed and synced
  across machines, so a pointer into a host's scratch is **dangling by
  construction** on every other machine. **Never** reference `~/.claude/plans/…`,
  an absolute `/…/.claude/plans/…`, or the project-root `commit.msg` from
  `resume.md`. Point at the **durable** equivalent instead: a vault task slug
  (`Projects/{{PROJECT}}/tasks/…`), a `tasks/done/<slug>.md` pointer, or a
  `doc/…` file under source control. You do not have to eyeball this: the
  `resume-refs` row of the `vp_check` call in Step 1 already flagged the
  `.claude/plans/` case by line. If that row reported findings, repair
  them **here**, in the same surgical edit pass, and say so in the report
  — this is the one hygiene finding the wrap is allowed to fix, because
  the wrap is what writes the file.

## Step 4: Append Iteration Narrative

Use `vp_append_iteration` to describe what changed this session and why
(past tense, technical detail). Pass two fields:

- **`title`** — the short title, WITHOUT any `## Iteration N —` prefix.
- **`narrative`** — the body (markdown), WITHOUT a leading iteration header.
  Use **H3 (`###`)** for sub-sections *inside* the narrative
  ("### Phase 1 — …", "### Results").

**Do NOT compute or pass the iteration number.** The server mints it under the
`iterations.md` lock and composes the canonical `## Iteration N — title` header
itself — so the number cannot drift to the wrong heading level, and two
concurrent wraps cannot collide on it (the counter-corruption class this
deletes rather than polices). The tool returns `iter_n`, the number actually
written. Supply the optional `iteration` override ONLY to backfill a specific
number; if it disagrees with what the server would mint, the result reports
both `overridden` and `derived_n`.

## Step 5: Update Stable Docs (if changed)

If stable project documentation changed (architecture, design
decisions, test structure), update the relevant `doc/` file directly.

## Step 6: Retire Tasks — Only on Explicit Human Approval

**Retire a task only when the human has said, in this session, that it is
done.** Nothing else counts. Not passing tests, not a clean build, not your
own reading of the diff.

Note where you are in the flow: **wrap runs BEFORE the commit.** Step 8 stages
files and the human commits afterward. So at this point the work is not even
committed, and "it's implemented, I'll retire it"
is the agent adjudicating its own completion — exactly what the operator's
standing Rule 0 forbids: *nothing is done until the human says it is done.*

So:

- Use `vp_list_tasks` to check each active task against the session's work.
- For any task that **looks** finished, say so — "task `<slug>` looks complete;
  retire it?" — and leave it active.
- Retire **only** a task the human explicitly approved. `vp_manage_task`
  `action: retire` takes an `approved_by_human` parameter; pass `true` only
  when the human actually said so in this session. It is an attestation you
  are making, not a check being run on you — asserting it falsely just means
  you lied in the record.
- After an approved retirement, add its one-line row to the **Completed
  Plans** table in `resume.md` (Step 3, *What goes in, what stays out* —
  a shape-1 `vp_vault_edit`, not a whole-file rewrite).

## Step 6b: Sweep Orphaned Plans

Wrap runs a **narrow** plan-hygiene pass over the host's local plan/scratch
directory — on Claude Code that is `~/.claude/plans/`; other hosts have their
own, and some have none. **If there is no such directory, skip this step.**
That directory is **scratch only, never the source of truth** — plans live in
the vault, reached only through the MCP task tools.

**This is a reconcile-and-report step, not a promote-everything step.** The
session-start sweep in `/restart` is the one that promotes strays into tasks;
wrap runs **before the commit** (Step 8 stages, the human commits after), so
under the operator's standing Rule 0 — *nothing is done until the human says it
is done* — wrap does not adjudicate arbitrary plans on its own reasoning.

**Prefer the MCP reporter.** Call `vp_scan_plans` (read-only — it never
promotes, deletes, or writes anything). It returns, per stray plan, the file,
the absolute paths grepped from its body, and a `resolution` — `managed`
(belongs to a vibe-palace-managed project, with the slug), `unmanaged` (a real
directory with no `.vibe-palace.toml`), `none` (no absolute path in the body,
so it is unattributable), or `ambiguous` (multiple candidate roots). Use that
structured attribution to decide each plan's disposition **instead of**
hand-globbing and eyeballing.

- **Fallback:** if `vp_scan_plans` is unavailable (an older binary that a
  freshly synced template is briefly running against), fall back to globbing
  `~/.claude/plans/*.md` with the Glob tool and reading each file by hand —
  the same rollout hedge Step 10 makes for `vp_vault_tidy` vs `vp vault tidy`.

Then, for each stray, act **narrowly**:

- **Promoted this session → delete.** Wrap may delete a scratch plan file
  **only** when its content was promoted to a vault task **during this
  session** — its work is now captured under `Projects/{{PROJECT}}/tasks/` and
  the scratch copy is pure redundancy. Confirm the task exists first
  (`vp_list_tasks`), *then* delete the scratch `.md`.
- **Everything else → report, do not act.** Do **not** auto-create tasks from
  strays here — that is `/restart`'s session-start job. Do **not** delete
  other-project (`managed` for a different slug), `unmanaged`, `none`, or
  `ambiguous` plans, and do **not** delete a plan for **this** project that was
  **not** promoted this session. List each such plan for the human in the Step
  11 report with its attribution and let the human decide.

The one deletion wrap owns is the redundant-scratch-after-promotion case;
every other disposition is a **report**, never an action.

## Step 7: Update commit.msg (Two-Copy Workflow)

### Skip this step entirely when the session wrote no project code

A commit message describes a commit. If this session changed **nothing in the
project repo** — a docs-only pass in the vault, a pure investigation, a
read-only review — then there is no commit to describe and **you must skip
Step 7 altogether**. Do not write a `commit.msg`, do not ingest one, do not
carry a stale one forward.

`vp_ingest_commit_msg` **refuses on a clean repo** — it returns an error
rather than archiving a message for a commit that will never exist. That is
not a failure to work around; it is the tool agreeing with you. Check
`git status` first: if the project working tree has no staged or unstaged
project changes, note "no project code changed — commit.msg skipped" in the
Step 11 report and move on to Step 8.

Everything below applies **only** when the session did write project code.

There are **two** `commit.msg` copies and **both** must be kept in
sync:

1. **Vault mirror** — the **latest** commit message, **committed** to the
   vault repo each wrap and synced across machines (single-overwrite, not a
   history): `<vault_path>/Projects/<project>/commit.msg`. The permanent
   history is a separate append-log, `commit-log.md` — Step 7b keeps it.
2. **Project-root working copy** — gitignored, host-local scratch; the
   path that `git commit -F commit.msg` reads when the user runs it,
   never committed and **removed by the commit that consumes it**:
   `<project_root>/commit.msg`

   **This file has a lifecycle, and absence is its steady state.** It
   exists only between authoring (below) and the commit that reads it —
   the handoff command Step 11 gives the human ends in `&& rm
   commit.msg`, so consumption removes it. A `commit.msg` sitting beside
   a **clean** project tree is therefore an anomaly: a message that was
   authored and never consumed, or a commit made without consuming it.
   That is the state that lands a previous session's message on a new
   commit, and `vp_preflight_wrap` reports it as `commit_msg_unconsumed`
   in Step 1.

### Workflow

Use the typed writer `vp_ingest_commit_msg`: it reads the
**project-root** copy off disk and writes the vault copy atomically,
surface-stamped. Author the message once in the project root, then
ingest — do **not** hand-copy the vault file or `cp` between the two.

1. **Write** the new message to the project-root `commit.msg` via
   `Write`. In the steady state the file does **not** exist — the
   previous commit consumed it — and `Write` needs no prior `Read` to
   create a file.

   **If `Write` refuses because the file exists and has not been read
   this session, that refusal is a finding, not an obstacle.** A
   `commit.msg` surviving into this wrap means the last commit did not
   consume it. `Read` it (that clears the refusal), then report to the
   human what it says and which commit it appears to describe, before
   overwriting it — the message you are about to destroy may be the only
   record of what a mis-messaged commit was supposed to say.
2. Call **`vp_ingest_commit_msg`** with `project_path` set to the
   local project repo root. It reads the file you just wrote and emits
   the vault copy (`<vault_path>/Projects/<project>/commit.msg`)
   atomically and surface-stamped — there is no `content` parameter,
   so the message is emitted exactly once.
3. **Verify** both copies match (compare byte counts / first line).
   The project-root copy is what `git commit -F commit.msg` reads; the
   vault copy is the archive Step 9 commits. If the project-root copy is
   missing at commit time, `git commit -F` fails outright — `fatal: could
   not read log file` — and nothing lands. That is the intended failure:
   a commit with no authored message is refused rather than given a
   stale one.

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

## Step 7b: Archive landed commits to the permanent history

Call **`vp_archive_commit_log`** with `project_path` set to the local
project repo root. It walks `git log <last-archived>..HEAD` and appends
each **landed** commit's full message to `Projects/<slug>/commit-log.md`
(the permanent history), then advances the anchor. Run it **every wrap,
in BOTH flows** — it is DERIVED from git and idempotent, so it needs no
call-order care and a re-run with no new commits is a no-op.

This is the step that captures **feature-branch** commits: they land
*before* the wrap, so Step 7's dirty-tree ingest self-skips and never
sees them; this walk reads the real committed messages after the fact.
It runs **regardless** of whether Step 7 fired, so do not gate it behind
Step 7's "no project code" skip.

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

Include `Projects/<slug>/commit.msg`, `Projects/<slug>/commit-log.md`,
and `Projects/<slug>/commit-log.anchor` among the synced paths — the
`commit.msg` mirror is written by Step 7, the append-log and its anchor
by Step 7b, and all three must be committed so the permanent history and
its cursor advance together each wrap. (The project-root `commit.msg`
copy stays gitignored and is never synced.)

If this wrap changed any vault-served template, name `Templates/`
explicitly among the synced paths — it is **not** in the default set. The
binary and the vault-served templates ship together: a template supplies
arguments the binary requires, so a new binary against an un-synced vault
template breaks that command outright (the reverse is harmless). The
`template-drift` row in Step 1's `vp_check` call is what reports the gap.

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
- Tasks retired (only those the human explicitly approved) and tasks
  reported as retirement candidates awaiting the human's call
- `commit.msg` updated in both locations (with verification) — or
  "no project code changed — commit.msg skipped"
- Project files staged (by path)
- Vault synced
- Vault tidied (capture artifacts swept; any reported dirt surfaced)

Note that the user should review the staged diff and the
`commit.msg`, then commit with:

```sh
git commit -F commit.msg && rm commit.msg
```

**Hand over that whole command, including the `&& rm`.** The removal is
what consumes the message: it runs only if the commit succeeded, and it
is what stops the next `git commit -F` from silently relanding this
message on different work. A failed commit leaves the file in place, so
nothing is lost.
