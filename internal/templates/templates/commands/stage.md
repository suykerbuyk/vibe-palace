# Stage — Prepare a Commit

Prepare the project working tree for a commit: run a light quality gate, author
the `commit.msg`, and stage the changed files by path — then hand off so you can
run `git commit -F commit.msg` yourself. This is the commit-preparation subset of
`/wrap`, extracted so a feature-branch commit gets the same authoring and staging
without the full end-of-session coherency sequence.

Do not ask for confirmation — run the gate, write `commit.msg`, stage the files,
show what changed, and note that you should review before committing.

## Step 1: Light Quality Gate

`/stage` prepares an in-progress commit, so its gate is LIGHTER than `/wrap`'s: it
confirms the tree can safely build and write, not that it is warning-clean. That
hard "zero warnings/diagnostics" gate stays at the aggregate merge (`/wrap`);
transient diagnostics on an in-progress feature branch are allowed — they must
resolve before the aggregate merge, not before every intermediate commit.

- **Surface preflight.** Call `vp_surface_check`. If `status` is `"fail"`, the vault
  was last written by a newer `vp` binary than this host has installed — HALT and
  surface the tool's `details` lines to the human verbatim (they name the version
  mismatch and carry the `git pull && make install` remediation). If `"pass"` or
  `"info"`, proceed.
- **It builds.** Run the project's build (e.g. `make build`, or `go build ./...`). If
  the build fails, STOP and fix before staging — a commit that does not build is not
  ready even on a feature branch.

## Step 2: Nothing to Stage?

Run `git status`. If the project working tree has NO changes — nothing staged or
unstaged, no untracked project files — there is nothing to prepare: report
"working tree clean — nothing to stage" and STOP. Do not author a `commit.msg` for
a commit that will not exist.

## Step 3: Author commit.msg (project-root only)

`git commit -F commit.msg` reads the **project-root** `commit.msg`. Author it there:

1. **Read** the project-root `commit.msg` first with the `Read` tool. It usually
   exists from a prior stage or wrap, and the `Write` tool refuses to overwrite a
   file it has not read this session — so this Read is mandatory, and it confirms you
   are not clobbering anything unexpected.
2. **Write** the new message to the project-root `commit.msg`.

Make the message **complete and standalone**: document every code change, feature
added, and bug or warning resolved. Don't be terse — the commit message is the
project's history. Do **not** add "Co-Authored-By" lines or any other AI-authorship
marker, in the message or in code.

**`/stage` does not touch the vault.** It does not run `vp_ingest_commit_msg` and does
not commit any vault commit-message artifact. The permanent history —
`Projects/<slug>/commit-log.md` — is populated at **wrap** time by
`vp_archive_commit_log`, which walks `git log` over the commits that actually
landed and appends their real messages. Because it derives from git after the
fact, it captures this feature-branch commit no matter when or by whom it was
made, and you never have to remember to archive a message in a particular
order. Authoring the project-root `commit.msg` here is all a commit needs.

## Step 4: Stage Project Files

Stage the modified and newly-added project files with `git add` and **explicit file
paths** — never `git add -A` or `git add .`. Only stage files inside the git repo, and
only files that belong to this change; leave unrelated stray files untracked. The
project-root `commit.msg` is gitignored host-local scratch — never stage it.

## Step 5: Report

Run `git status` and report:

- The light-gate result (surface preflight + build).
- `commit.msg` authored (its first line / byte count).
- The files staged, by path — and anything left untracked — so you can verify.

Note that you should review the staged diff and `commit.msg`, then run
`git commit -F commit.msg`. `/stage` makes no commit itself. On a multi-commit feature
branch, repeat `/stage` + commit per commit; after the branch is merged to `main`, run
`/wrap` — on the now-clean tree it is coherency-only (it records the session and syncs
the vault without re-staging).
