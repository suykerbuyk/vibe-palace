# ADR-008 Phase 1 — prepared live-vault edits (apply at rollout ONLY)

**Status: APPLIED 2026-07-25 (iteration 257). This document is now a record, not
a plan — do not re-apply it.** Steps 1-5 below all completed: Edit A landed as
`Projects/vibe-palace/workflow.md` (12,528 -> 13,482 bytes) and Edit B as
`Projects/vibe-palace/resume.md` (42,307 -> 32,586 bytes), both compare-and-set
guarded; the live canary passes at 7,457/8,000.

> **⚠ The step-5 acceptance criteria below were WRONG and are superseded.** They
> asked for a real bootstrap showing `shed_core` core-free and `workflow-caps`
> silent. Neither is achievable by Edit A/B, and this document's own content is
> what proves it: after Edit B the full resume is 32,586 bytes (~8,146 tokens),
> larger than the entire 8,000-token budget on its own, so `resume->pinned` must
> keep shedding; and Edit A's own verbatim replacement text is 13,482 bytes
> against a 4,000-byte workflow cap, so the deliverable violates the criterion
> the same document sets. Phase 1 cut the inviolable core 73% (13,271 -> 3,527
> bytes of pinned resume) but did not make it FIT — ~9.7 KB of Behavioral Notes
> moved from a pinned resume section into a bootstrap-inlined workflow.md, so
> those bytes remain core by a different route. `adr-009-arm-fail-loud-bootstrap`
> therefore stays gated. Do not carry this wording into Phases 2-4: state a
> measured target with a number, or leave it to `workflow-caps` / `resume-caps`,
> which already compute it. Full record in the task
> `mcp-served-doctrine-and-thin-project-workflow`.

> **⚠ The verbatim edit text below asserts that `amend` is the ONLY way to change
> a task's PLAN. That was true when this was applied and is FALSE at HEAD** — it
> appears twice, in Edit A's Project-Specific Workflow Rules and in the B2 block
> quoting the text it replaced. `vp_manage_task` now exposes **eight** actions,
> not seven: `overwrite` was added 2026-08-19 and is the typed path to what
> `amend` structurally cannot reach — the preamble above the first H2, an H2
> heading's own wording, and whole-file migration. It is refused on a done or
> cancelled task, so an archived body still cannot be silently revised. `amend`
> remains the SECTION writer and the normal way to change a plan; it is no longer
> the only one.
>
> **The quoted text is deliberately left as written.** This document is a record
> of what was applied, and correcting a verbatim block in place would make it
> claim it applied text it never applied. Read the blocks below as of 2026-07-25
> and this note as of 2026-09-05. The live copies are what needed repair: the
> tool's own description was corrected in `0d5c88f`, and the embedded
> `doctrine.md` and `workflow.md` templates already say "the normal way". The
> remaining live instance is `Projects/vibe-palace/workflow.md` in the vault,
> which is not reachable from this repository.

Historical intent follows. Per iter-179 rollout discipline and the task's
2026-07-22 triage decision, every live-vault edit below was applied only at the
epic's rollout step, in this order:

1. Merge the epic branch; `make install` the new binary.
2. Restart every AI host (a long-lived `vp mcp` keeps executing the old image).
3. `vp config sync` — materializes the new embedded `doctrine.md` and the
   updated `restart.md` / `review-plan.md` / `cancel-plan.md` /
   thin `workflow.md` templates into `Templates/` (lock updated by the
   reconciler; never hand-edit `templates.lock`).
4. Apply the two live-vault edits below (Edit A, Edit B) via
   `vp_vault_read` + CAS write (`vp_vault_write`/`vp_vault_edit` with
   `expected_sha256` from the read).
5. Re-run the live canary: `go test -count=1 ./internal/tools -run
   TestBootstrapLiveVaultFitsItsOwnBudget`. The budget goes green only AFTER
   this step — not on the branch.

The intermediate state (new binary + fat vault workflow) is safe: doctrine is
served on demand while the fat workflow still inlines; only the budget stays
temporarily red.

**Section outlines (the required split deliverable).** Generic → embedded
`doctrine.md` (shipped in the binary): The MCP Is the Center (air-gap /
accessor rules) · Pair Programming · Investigation-First Workflow · Task
Management (seven-action model, human-commit-as-confirmation) · Commit
Discipline · Core Principles. Project → thin `Projects/vibe-palace/workflow.md`
(Edit A below): Bootstrap Contract · Files + destination rule ·
Project-Specific Workflow Rules (no-AI-attribution, wrap timing, project task
notes) · Project-Specific Behavioral Notes (migrated from resume.md) ·
Auto-memory (this project) · Subagent worktree lifecycle. Deleted outright:
the `vp-cutover note (2026-06-06)` HTML comment naming an absolute
`~/obsidian/...` path (violates "never write an absolute vault path into a
vault doc" — dropped, not relocated).

---

## Edit A — REPLACE `Projects/vibe-palace/workflow.md` in full

Old: the entire current 97-line / 12,528-byte file (read it first with
`vp_vault_read Projects/vibe-palace/workflow.md` and pass its sha as
`expected_sha256`).

New content (verbatim, whole file):

```markdown
# vibe-palace — Workflow

<!-- THIN BY DESIGN (ADR-008). This file carries ONLY vibe-palace-specific
     workflow patterns. The generic Vibe-Palace doctrine — pair programming,
     investigation-first workflow, task management, vault-accessor rules,
     commit discipline, core principles — is owned by the binary and served
     on demand; do not copy it back in here. -->

## Bootstrap Contract

Call `vp_bootstrap_context` at session start. The full Vibe-Palace operating
doctrine is served on demand, off the bootstrap payload: fetch it with
`vp_get_doctrine` (or read `vibe-palace://doctrine/vibe-palace` via
`vp_read_resource`) before starting work, and follow it for the rest of the
session. Nothing is done until the human says it is done.

## Files

- **resume.md** — current project state, open threads, navigation (thin gateway)
- **iterations.md** — iteration narratives and project history (append-only archive)
- **tasks/** — active tasks; **tasks/done/** — completed. Vault-resident under `Projects/<p>/tasks/`, mutated **only** via `vp_manage_task` and read via `vp_get_task`/`vp_list_tasks` — never a working-directory-relative path, never a raw `Write`.
- **commands/** — slash commands (/restart, /wrap)
- **doc/** — stable project reference: architecture, design decisions, testing (source-controlled)

**Destination rule:** vault history trackers (resume, iterations, features, tasks) go under `Projects/<p>/`; long-lived implementation reference (architecture, design, testing) goes under `doc/`.

## Project-Specific Workflow Rules

- **No AI attribution in commits or code.** Do not add `Co-Authored-By` trailers, author lines, or any other AI authorship marker. Applies to commits you make directly AND to instructions you give subagents — override the Bash tool's built-in commit-message guidance where it conflicts.
- **`/wrap` describes canonical state, not transient state.** Timing depends on the commit pattern:
  - **Direct commits to main** (default): run `/wrap` BEFORE `git commit`. `/wrap` stages project files and writes `commit.msg`; human reviews the diff and runs `git commit -F commit.msg`. Vault narrative describes the about-to-commit state.
  - **Feature-branch aggregate merge** (multi-commit features): run `/wrap` AFTER `git merge --ff-only` to main, push, and feature-branch deletion. Wrapping on the feature branch pre-merge bakes "merge pending" text into resume.md and iteration narratives that falsifies the moment main advances, requiring reconciliation next session.
- **Task files, this project:** `amend` is fence-aware for good reason here — 22 H2-shaped headings quoted inside code fences exist in this project's own task files. **NB (corrected 205):** `vp_manage_task` used to have no content-update action — `amend` is now that action, and it is the ONLY way to change a task's PLAN. Do **not** hand-write a task body with `vp_vault_write`.

## Project-Specific Behavioral Notes

<!-- Cross-agent memory home (decided 2026-06-09; relocated here from resume.md
     under ADR-008). Project-specific gotchas that must reach EVERY agent go
     here — workflow.md is bootstrap-inlined, so it percolates to every host.
     Keep terse. This section is NOT diary: it is load-bearing for correctness.
     Prune it only when a note becomes FALSE, never merely old. -->

- **A stale binary is a stale *server process*.** `make install` alone is **not enough**: a
  long-lived `vp mcp` keeps executing the image it started with — `make install` renames a new
  inode into place and the running process holds the old, now-deleted one (`/proc/<pid>/exe` shows
  `(deleted)`). The AI **host** must be restarted, not just the binary reinstalled. Killing the
  server alone is wrong too: its parent host will not respawn one, leaving that session with no
  `vp_*` tools. Caught **twice** (180, 186), both times in the *same* place: a different Claude
  session (tmux `johns-dot`, cwd `~/dotfiles`) holding a deleted inode — **check ALL hosts, not
  just this one.** The check is one command: `readlink /proc/<pid>/exe` for every
  `pgrep -f "vp mcp"` — `(deleted)` means that host is lying.
- **🔴 NEVER source write-back text from `vp_get_resume` / `vp_bootstrap_context`.** Their bodies are
  placeholder-**EXPANDED** (`expandScoped` substitutes `{{PROJECT}}`/`{{DATE}}`) while their `sha256`
  is over the **RAW** bytes. So a whole body composed from them **passes compare-and-set and silently
  bakes the expanded values onto disk**, destroying the live tokens — and an `old_string` copied from
  them will not match disk wherever a token lives. `expandScoped` is a blind `strings.ReplaceAll`: it
  does **not** respect backticks, so tokens inside code spans are eaten too. **`vp_vault_read` is the
  only legal source of text you intend to write back.** `vp_get_resume` is for *reading*. The vault
  resume **carries such tokens today**.
- **📏 RECORD THE GREP, NEVER THE COUNT.** Every hand-recorded census of the placeholder tokens in
  resume.md has rotted (180, 181, 185 each cited a figure that was wrong by the next iteration); every
  edit shifts them. The same rot hit the row-count claims. **State the command, not the answer:**
  `grep -n '{{[A-Z]*}}' resume.md`, and let `vp check --check resume-caps` count rows.
- **⚠ ROLLOUT ORDERING (179): the binary and the vault-served templates must ship TOGETHER.**
  `vp_update_resume` **requires** `expected_sha256`, and `Templates/commands/wrap.md` supplies
  it. A **new binary against an un-synced vault template** breaks `/vpc-wrap` outright. The reverse
  is harmless. Note `Templates/` is **not** in wrap's default sync paths; name it explicitly.
- **`resume.md` has NO blind whole-file overwrite path.** `vp_update_resume` is compare-and-set:
  `expected_sha256` is **required**, and `""` means **assert-absent** (first write), not "skip the
  check". On `"conflict":true`, **re-read and recompose — never force**. Routine wrap edits use
  `vp_vault_read` + `vp_vault_edit` (locked + CAS); `vp_update_resume` is for deliberate whole-file
  regeneration only.
- **The CAS digest is of the RAW, PRE-EXPANSION bytes.** The resolver runs `expandScoped()` on
  everything it returns ({{PROJECT}}, {{DATE}}=`time.Now()`, …), so hashing the *content you
  received* matches nothing on disk and would differ on every call. **Never** re-hash resolved
  content, and never take the digest from a second read — that reintroduces the race. See ADR-003.
- **🗺 NEVER write an absolute filesystem path for the vault into a vault document.** The vault is
  **synced to every machine** and lives somewhere different on each, so an absolute path is a fact
  about *the host that wrote it* and is false everywhere else. (188: resume.md claimed the vault was
  at a path that was true only on the **old WSL** host — and an **empty directory** sits there, so
  the wrong answer *looks* plausible instead of failing loudly.) **Resolve, don't recall:**
  `vault_path` in `~/.config/vibe-palace/config.toml` (per-tree override: `.vibe-palace.toml`);
  `vp status` prints what the binary resolved. Write the **constraint** (POSIX filesystem, never
  NTFS/exFAT), never the **path**.
- **🔴 A REMOTE-TRACKING REF IS NOT THE REMOTE (191).** `git rev-list @{u}..HEAD` reports phantom
  unpushed commits if the repo was never fetched this session — the ref is a **cache**. 191 reported
  four phantom commits five times, and wrote it into resume.md, before `git ls-remote` caught it.
  **Fetch in the repo you are reporting on, or use `git ls-remote`, which cannot go stale.**
- **📏 VERIFY AGAINST THE LIVE VAULT, NOT FIXTURES (190, and re-earned in 191 and 193).** 191's fence
  bug passed every unit test its author wrote — all the fixtures had balanced fences — and surfaced
  only when the tool was run against the real file. 193's `note_path` bug passed every test in the
  tree for **six months** and was caught by *reading a tool's actual output during a wrap*.
- **✅ The vault is now NTFS/exFAT-portable (229).** The live KG triple-filename migration ran: all
  50,599 triples use the flat `slug+sha256[:8]` encoding — zero colons/slashes/newlines, zero nesting —
  and `format = 1` is stamped in `.vibe-palace/vault.toml`. Windows testing is unblocked.
  `encodeTripleComponent` is flat; never reintroduce raw strings into KG filenames.
- **⚠ A THROWAWAY VAULT THAT SILENTLY IS NOT ONE (210).** `vault_path` in `.vibe-palace.toml` is a
  **top-level key**, so a `[table]` written above it **swallows it as a sub-key** and `vp` falls back to
  the **global config — the LIVE vault**. It does not fail; it scaffolds a stray `Projects/<basename>/`
  from the CWD name and captures into it. Caught at 210 while driving `vp hook` against what was
  supposed to be a temp vault. **Put `vault_path` FIRST, above every table, and confirm with
  `vp check | grep vault_path` — which prints both the resolved path AND its source — BEFORE any run
  that writes.**
- **`vp mcp` resolves `vault_path` once, at startup.** A mid-session config change does not
  re-resolve it — the CLI sees the new vault while the running server keeps writing to the old one.
  Reload MCP after any vault-path change.
- **Never write a vault file by hand from `internal/tools`.** All vault I/O belongs in
  `storage`/`vaultfs`, which own the `vaultlock` discipline (ADR-003). A tool that does its own
  `os.ReadFile` + `atomicfile.Write` opts *out* of an advisory lock other writers are taking — which
  is exactly how the resume.md hole was born. (Corrected 194: this note used to end by prescribing
  "`mdutil` transforms passed to `storage.EditResume`" — **both were deleted in 186.** Routine resume
  edits go through `vp_vault_edit`.)
- **Never call `lockedWrite` while holding that path's lock.** It re-acquires, and
  `vaultlock.Acquire` is a blocking `LOCK_EX` with no `LOCK_NB` and no timeout — so re-entry is a
  **permanent hang**, not an error. Use raw `atomicfile.Write(v.Root, …)` under the held lock (and
  pass the real root, or you silently skip the surface stamp).
- **There is no `.surface` merge driver any more** (174). Stamps are byte-stable, so the conflict
  class it resolved no longer exists. Deployed `~/.gitconfig` files keep an inert orphaned
  `[merge "vp-surface"]` entry that binds nothing — harmless. **Do not re-add the attribute without
  re-adding the driver:** git emits no diagnostic for an undefined driver and silently text-merges.
- **Never place a `.vibe-palace.toml` at `$HOME`** (167). The "walk stops at `$HOME`" contract is
  unimplemented, so a marker there enrolls the whole home tree into auto-capture. Stow-style dotfile
  managers place one there by accident. See `tasks/home-marker-must-not-enroll-home-tree.md`.
- **📌 A RESUME SECTION MARKED `<!-- vp:pin -->` IS ALWAYS-INLINE IN `vp_bootstrap_context`; AN
  UNMARKED ONE IS SHED (209).** The token ladder drops the un-pinned zone of resume.md (it is
  reachable via `resume_uri`) and **cannot touch a pinned one**. Pinned today: *What This Project
  Is*, *Data workflow*, *Quick Reference* — verify with `grep -n -B1 'vp:pin' resume.md`, never from
  this list. **Consequences for editing resume.md:** (1) a correctness note MUST live in a pinned
  section (or here in workflow.md, which is bootstrap-inlined) or it will not reach an agent on a
  busy project; (2) **pinning more defeats the mechanism** — a resume that pins everything sheds
  nothing and goes back over budget; (3) **renaming a pinned H2 is safe** (the marker travels with
  the section), but **deleting its marker line silently un-pins it**. A resume with **no marker at
  all** is treated as un-sheddable and bootstrap reports itself **over budget** rather than guessing.
- **Template edits (183):** edit **only** the Go-embedded copy under
  `internal/templates/templates/` (including `doctrine.md` and `commands/`), then `make install`,
  then `vp config sync`. Never hand-edit `templates.lock`. Byte-identical copies do **not**
  hard-error.

## Auto-memory (this project)

**Auto-memory needs no per-host setup (no symlink).** The host-local symlink approach
(`memory-link-command`) was **cancelled** — see `doc/adr/004-mcp-native-memory.md`: a symlink is
Claude-shaped (Grok/Zed have nothing to link), leaks the host layout into the vault, and gives no
host-agnostic write surface. Memory reaches the synced vault (`Projects/<p>/memory/`) two ways,
neither of which requires a link: (1) the host-agnostic `vp_memory_*` MCP tools write straight to
the vault; (2) Claude Code's native host-local memory (`~/.claude/projects/<encoded>/memory/`) is
drained one-way into the vault by the `vp hook` SessionEnd **harvest** (`internal/memory/harvest.go`),
which locates both sides independently via `EncodeProjectDir`. The vault `memory/` dir is created
lazily on first write — an absent `Projects/<p>/memory/` on a fresh host just means nothing has been
written yet, not a misconfiguration. Do **not** create a symlink. The only per-host concern is that
`vp hook` is wired into the host so the harvest runs.

## Subagent worktree lifecycle

**Subagent worktrees under `.claude/worktrees/agent-<id>/` are exclusively LLM-managed. Do not check
out, edit, or commit in them — operator-style off-branch work goes anywhere ELSE.** When dispatching
subagents with worktree isolation, treat those trees as ephemeral scratch owned by the agent run.
```

Triage record for Edit A (old file → destination): the `CLAUDE.md`/`AGENTS.md`
preamble (L3) → replaced by the Bootstrap Contract; Files + destination rule
(L5-13) → kept; generic Workflow Rules (L17-19), Pair Programming (L25-28),
Investigation-First (L30-37), generic Task Management incl. the seven-action
table (L39-66 except the "22 headings" statistic), Core Principles (L68-73),
Vault file accessors (L75-79) → embedded `doctrine.md` (genericized,
host-neutral); no-AI-attribution (L20) + wrap timing (L21-23) + the 22-headings
statistic (L58) → Project-Specific Workflow Rules; auto-memory (L81) →
Auto-memory section; L83 ("Read resume.md…") → covered by doctrine, dropped;
worktree lifecycle (L85-87) → kept; `vp-cutover note` (L89-96, `~/obsidian/…`)
→ **DELETED**. (The stale `vp_thread_*`/`vp_carried_*` tool names in old L79
were not carried into the doctrine — those tools no longer exist; the doctrine
names the live typed writers.)

## Edit B — `Projects/vibe-palace/resume.md` surgery (two deletions + one line edit)

Apply with `vp_vault_read` + `vp_vault_edit` (CAS-chained). All three pinned
survivors (*What This Project Is*, *Data workflow*, *Quick Reference*) keep
their `<!-- vp:pin -->` marker lines untouched.

**B1 — delete the whole `## Project-Specific Behavioral Notes` section**
(H2 line + its `<!-- vp:pin -->` marker + intro comment + all bullets, i.e.
everything from the line `## Project-Specific Behavioral Notes` up to but NOT
including the line `## Data workflow`). It is relocated into the thin
workflow.md (Edit A) with three deliberate text adaptations: the intro comment
now says the section lives in workflow.md; self-references to "this file"
now name resume.md explicitly; the `vp:pin` note's "Pinned today" list drops
*Project-Specific Behavioral Notes* and *Task Management* and adds the
"or here in workflow.md" routing clause. Replacement: empty string.

**B2 — delete the whole `## Task Management` pinned block near the file
bottom** — exactly these lines, replaced by empty string:

```markdown
## Task Management
<!-- vp:pin -->

Open tasks live in `tasks/`, completed in `tasks/done/`, cancelled in `tasks/cancelled/`.
Use `vp_manage_task` — **seven actions, each field with exactly ONE writer** (`create` | `amend` |
`set_meta` | `update_status` | `set_relations` | `retire` | `cancel`). On completion: retire via
`action: retire`, append a narrative via `vp_append_iteration`, and add a one-line row to
**Completed Plans** above.
**NB (corrected 205):** this section used to say *"`vp_manage_task` has no content-update action"* —
**`amend` is now that action**, and it is the ONLY way to change a task's PLAN. Do **not** hand-write
a task body with `vp_vault_write`.
```

The generic seven-action content is now embedded `doctrine.md`; the
project-specific "NB (corrected 205)" rides in the thin workflow's
Project-Specific Workflow Rules (Edit A), not the doctrine.

**B3 — Reference Documents table: repoint the workflow row and add doctrine.**
Old lines:

```markdown
| workflow.md | `vp_get_workflow` | AI workflow rules and pair programming paradigm |
```

New lines:

```markdown
| workflow.md | `vp_get_workflow` | Project-specific workflow patterns (thin; ADR-008) |
| doctrine | `vp_get_doctrine` | Generic Vibe-Palace operating doctrine, served from the binary |
```
