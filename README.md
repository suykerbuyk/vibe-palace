# Vibe-Palace

> **Persistent memory and team coordination for AI-assisted development.**
> A single-binary MCP server that gives every AI coding assistant on your
> team durable, queryable project context — and keeps every byte of it out
> of your source tree.

**License:** MIT OR Apache-2.0 · **Status:** early public release · **Stack:** Go 1.25, single static binary, zero CGO

---

## The Problem

AI coding assistants forget everything between sessions. Every new
conversation starts cold: no architectural decisions, no project state,
no awareness of what was tried yesterday. Teams re-explain the same
context every morning.

Scale that to **many developers and many projects** and a second problem
appears: the usual workarounds contaminate the code base. Context files,
plan documents, task lists, and agent scratch get committed into the
repo, drift per developer, and turn every AI-assisted project into a
merge-conflict generator.

## The Solution

Vibe-palace is an **MCP server** that gives any AI assistant — Claude
Code, Grok Build, Zed, Cursor, or any MCP host — durable, shared memory.
Sessions, tasks, decisions, and iteration history are captured as
structured markdown in a **git-versioned vault that is a separate
repository from your code**, indexed with hybrid vector + structural
semantic search, and served back on demand through standard MCP tools.

One call at session start (`vp_bootstrap_context`) restores workflow,
resume, active tasks, and recent history. One call at session end
(`vp_capture_session`) records what changed and why. Between them,
`vp_search`, `vp_kg_query`, and `vp_get_project_context` let the agent
find anything it needs without a giant upfront context dump.

---

## Built for Teams: the Coordination Model

### A clean source tree, by construction

Nothing the AI tooling produces belongs to your project's git history,
and vibe-palace enforces that mechanically rather than by convention:

- **Every AI artifact is host-local and gitignored by the tool itself.**
  `vp` maintains a canonical ignore set (`/CLAUDE.md`, `/AGENTS.md`,
  `/commit.msg`, `/.claude/`, `/.grok/`, `/.vibe-palace/`) and reconciles
  it into the project's `.gitignore` — these files are written into the
  working tree for the host to find, and must never be committed.
- **Context files are thin shims, not context.** The `CLAUDE.md` /
  `AGENTS.md` that vibe-palace writes contain a small managed block whose
  only job is to tell the agent to call the MCP server. The real
  workflow, resume, and task state live in the vault and are served over
  MCP — so every developer's agent gets the *same* context without a
  single context file in the repo.
- **The vault is its own git repository.** Project history trackers
  (resume, iterations, tasks, session notes, commit-message archive)
  live under `Projects/<slug>/` in the vault repo, with its own remotes
  and sync — your code repo never sees them.
- **Agent implementation work is isolated in git worktrees.**
  `vp worktree create` cuts a `plan/<slug>` branch into a sibling
  worktree; the orchestrated `/vpc-execute-plan` command implements
  there, and a human lands the result with a fast-forward-only merge.
  Concurrent agent runs can't trample each other or your checkout.
- **Even the operating rules ship out-of-band.** The agent doctrine
  (pair-programming contract, task discipline, verification rules) is
  embedded in the binary and served on demand via `vp_get_doctrine` —
  versioned with the tool, identical for every host, never pasted into
  a repo.

### Cross-project coordination across the vault

The vault is a **portfolio**, not a silo: every managed project lives
side by side under `Projects/<slug>/`, and the task tools address any of
them explicitly.

- **Work any project's backlog from anywhere.** Every task tool takes an
  explicit `project` argument — nothing is pinned to your current
  working directory. An agent sitting in one repo can create, refine,
  re-prioritize, and close tasks in an adjacent project by naming its
  slug. In practice: you notice a bug in a neighboring project's code,
  and your agent files (or fixes and closes) the task in *that*
  project's backlog without you ever leaving your checkout.
- **Epics and stories are derived, not declared.** A task carries two
  edges — `parent` and `depends_on` — and an epic is simply a task that
  others name as their parent. Roll-ups (`/vpc-tasks-epics`), subtrees
  (`/vpc-tasks-epic <slug>`), and dependency ordering are computed from
  the edges, so structure can never go stale.
- **Task lifecycle is human-gated.** Agents plan, implement, and
  propose; `retire` requires an explicit attestation that the human
  called the work done. Nothing silently disappears from a backlog.
- **Moving work between projects** is an agent-orchestrated workflow
  today: the agent creates the task in the destination project and
  closes it in the source, carrying the content across. (A first-class
  atomic `migrate` action is on the roadmap.) Parent/dependency edges
  are per-project by design — each project's graph stays
  self-contained.
- **Search spans the portfolio.** `vp_search_cross_project` runs
  semantic search across every indexed project, and `vp_list_projects`
  enumerates the portfolio — so "have we solved this before, anywhere?"
  is one tool call.

### Many developers, many machines

- **The vault syncs like code, because it is code-adjacent.** Standard
  git remotes; `vp vault sync` (or the `vp_vault_sync` MCP tool) pulls,
  tidies machine-generated capture artifacts, and pushes. Every vault
  commit is stamped with the hostname that made it.
- **Concurrent writers are arbitrated, not trusted.** Per-path advisory
  locks serialize writers of the same file; a single repo-root commit
  lock serializes all vault committers through the git index; mutating
  file tools support compare-and-set (`expected_sha256`) so a stale
  writer is refused instead of silently clobbering a teammate.
- **Mixed binary versions can't corrupt shared state.** Schema-bearing
  vault writes stamp the MCP surface version; an older binary
  encountering a newer vault refuses to write and names the upgrade —
  a stale install fails loudly instead of quietly downgrading the vault.

---

## Quick Start (5 minutes)

**Prerequisites:** Go 1.25+, `~/.local/bin` in `PATH`.

```bash
git clone https://github.com/suykerbuyk/vibe-palace.git
cd vibe-palace
make build && make test && make install
```

Initialize a project (creates the global config and the vault on first
run, then registers the project):

```bash
cd ~/code/your-project
vp init
```

`vp init` scaffolds the project's space in the vault and writes the
slash-command and skill shims (`.claude/commands/vpc-*.md`,
`.claude/skills/vps-*/SKILL.md`, and Grok/Cursor equivalents where
detected) so your editor discovers the full vibe-palace command catalog.
It also installs the `commit.msg` post-commit reaper described under
[Commands and Skills](#commands-and-skills), unless the repo already has a
post-commit hook of its own.

Then add `vibe-palace` to your editor's MCP config (see the
[Tutorial](doc/TUTORIAL.md) for per-editor setup). Start a new session
with `/vpc-restart` and the agent loads full context on turn one.

---

## What You Get

- **Context injection** — single-call restoration via
  `vp_bootstrap_context`. The payload is an **index**, not documents: head
  of queue, a session index, instruments, and the handles (`resume_uri`,
  `workflow_uri`) the agent fetches the bodies through. It ends with
  `complete: true`, so a truncated result is detectable rather than
  plausible.
- **Session capture** — agent-driven recording via `vp_capture_session`,
  plus automatic host-hook capture (`vp hook` on
  SessionEnd/Stop/PreCompact), with chunking, embedding, and semantic
  indexing. On a handshake-derived hook-less host an **inline transcript
  archive is default-on** when a transcript is supplied — and when one is
  **not**, the capture **fails loud** instead of reporting success: no hook
  will archive that session later, so the note would otherwise be born
  permanently archive-less.
- **Task management** — vault-resident tasks with derived epic/story
  structure, explicit cross-project addressing, and human-gated
  completion (`vp_manage_task`, `vp_list_tasks`, `vp tasks`).
- **Semantic search** — hybrid vector + structural search across all
  captured knowledge, single-project or portfolio-wide.
- **Knowledge graph** — temporal entity-relationship graph with
  time-travel queries, integrated with session capture.
- **Served doctrine** — the agent operating manual ships in the binary
  and is fetched over MCP (`vp_get_doctrine`), so behavior rules are
  versioned and uniform across hosts.
- **Friction tracking** — automated session difficulty scoring with
  trend analysis (`vp friction`, `vp trends`, `vp effectiveness`).
- **Vault integrity audit** — `vp audit vault` checks the whole vault
  against design intent (transcript round-trips, project-tree coherence,
  KG portability, resume discipline) against an accepted-debt baseline
  that may only shrink (see ADR-007).
- **Worktree isolation** — `vp worktree create|remove|list` gives plan
  execution its own branch + working tree, keeping agent edits off your
  checkout until a human merges.
- **Migration** — import existing VibeVault sessions and MemPalace data
  into the palace.
- **`vp` CLI** — full command-line surface with generated man pages and
  shell completions (`vp --help` for the live list).

---

## Commands and Skills

Every capability below is a markdown body served over MCP: the host
holds only a thin shim that calls `vp_cmd` (commands) or `vp_skill`
(skills), so all supported editors run the *same* command, resolved
through the same precedence chain.

### `/vpc-*` commands (embedded set)

| Command | Purpose |
|---------|---------|
| `/vpc-restart` | Turn-1 session bootstrap: vault sync, orphan-plan sweep, context load, doctrine fetch. |
| `/vpc-wrap` | Session wrap: quality gate, capture the session, update resume, stage files, sync the vault. |
| `/vpc-stage` | Commit prep only: light quality gate, author `commit.msg`, stage changed files by path (never `git add -A`). |
| `/vpc-capture` | Mid-session checkpoint without the full wrap sequence. |
| `/vpc-review-plan` | Critical senior-staff architecture review of a task plan before implementation. |
| `/vpc-execute-plan` | Execute an approved plan in an isolated git worktree, one subagent per phase; human ff-only merge. |
| `/vpc-cancel-plan` | Cancel a plan found not worth implementing, preserving the analysis so it isn't re-proposed. |
| `/vpc-license` | Add or refresh dual MIT/Apache-2.0 licensing and SPDX banners (idempotent). |
| `/vpc-makefile` | Audit or create a self-documenting Makefile facade over the native build system. |
| `/vpc-vault-audit` | Run the mechanical vault audit, then the adversarial human-judgment pass on top. |
| `/vpc-tasks-epics` | Roll-up table of every epic — open/total counts, priority, status. |
| `/vpc-tasks-epic <slug>` | One epic's subtree, re-rooted, each task tagged with its derived role. |
| `/vpc-tasks-standalone` | The standalone bucket — tasks that belong to no epic. |
| `/vpc-tasks-read <name>` | Print a single task or epic body verbatim (searches active/done/cancelled). |
| `/vpc-herdr` | Load this session's Herdr skill from the installed binary, only when the session runs inside a Herdr pane. |

**The `commit.msg` lifecycle, and why it has two halves.** `/vpc-wrap` and
`/vpc-stage` author `commit.msg` at the project root and print
`git commit -F commit.msg && rm commit.msg`. **The printed `&& rm` is still the
command to run** — it is what consumes the message. But a procedure the operator
must remember is not enforcement: omit the `rm` (muscle memory, an IDE commit, a
copied older line) and the file survives, so the *next* `git commit -F` relands
that message onto different work.

A **post-commit git hook** closes that. After a successful commit it compares
`git stripspace` of the new `HEAD` message against `git stripspace` of
`commit.msg`, and deletes the file only when they match — proof the commit just
consumed it, never a timer. An unrelated `git commit -m "typo"` does not match,
so the hook cannot destroy a message you have written and not yet committed. It
is plain `sh` + `git` (it never shells out to `vp`), and git ignores
post-commit's exit status, so a failed reap can never block or slow a commit.

`vp init` installs it, `vp commands upgrade` installs it, and `/vpc-wrap`
installs it on the repo it is authoring a message for — that last one is the
reach into an existing clone, which never re-runs `vp init`. **Your clone does
not necessarily have it**: run `vp check` and read the *Git commit.msg hook*
row, which is advisory (a repo without it is the pre-hook status quo, not
damage). The hook **refuses** rather than clobbers — a foreign post-commit hook
or a repo-wide `core.hooksPath` is reported and left alone, because that is a
directory the repo does not own.

This is a **git** hook. It is unrelated to `vp hook`, which means AI-host
session hooks (SessionEnd/Stop/PreCompact); the two vocabularies must not be
mixed.

### `/vps-*` skills (embedded set)

| Skill | Purpose |
|-------|---------|
| `/vps-code-digger` | Read-only codebase cartographer/auditor: onboarding maps, architecture deep-dives, severity-ranked issue register. |
| `/vps-epic-orchestrator` | Parallel-execution orchestrator that closes a whole epic across worktrees/subagents with adversarial review and a human gate. |
| `/vps-pair-reviewer` | Dual-agent pairing: you hold architecture and review while another agent is the implementation orchestrator. |
| `/vps-startup-analyst` | Domain-expert persona (business-plan analysis) with reference library — a worked template for your own skill personas. |

> **This table is a snapshot of the embedded floor.** The live,
> tier-resolved catalog (including your vault and per-project overrides)
> is derived, never hand-maintained — list it with `vp commands list` /
> `vp skills list`, or over MCP with `vp_list_commands` /
> `vp_list_skills`. The embedded floor itself is
> `ls internal/templates/templates/commands/` in the source tree.

### Supported platforms

| Host | MCP server | Commands / skills | Automatic hook capture |
|------|-----------|-------------------|------------------------|
| **Claude Code** | registered by `vp` | `.claude/commands/vpc-*.md` + `.claude/skills/vps-*/SKILL.md` | ✅ `vp hook` on SessionEnd/Stop/PreCompact |
| **Grok Build** | registered by `vp` | native `.grok/plugins/.../commands/vpc-*.md` + `.grok/skills/` + `/vpc` hub | ✅ **when wired** — `vp hook` accepts Grok's own wire dialect; vibe-palace does not assume it, so MCP capture stays the mechanism to rely on (**inline archive defaults on** for handshake-derived grok/xai with a `transcript`) |
| **Zed — Claude-shaped ACP agent** (the supported Zed path) | registered by `vp` | via `AGENTS.md` managed block → `vp_cmd` / `vp_skill` | ✅ full Claude hook path, **fired by archiving the thread** — not by idling, `restart`, or `exit` |
| **Zed — native pane** (Zed's default) | registered by `vp` | same managed block | — MCP-only: inline archive when `transcript` is supplied; **fails loud** when it is not |
| **Cursor** | manual MCP config | `.cursor/rules/vps-*.mdc` (skills) | — |
| **Any MCP host** | manual MCP config | `vp_cmd` / `vp_skill` tools directly | — |

Claude Code is the most exercised surface.

**Grok Build is not structurally hook-less.** `vp hook` reads Grok's hook
payloads — Grok sends `sessionId` where Claude Code sends `session_id`, and
that spelling is what names the host (`internal/hook`) — so a Grok session
reaches the hook path when the host's hook wiring is present. What vibe-palace
relies on is still MCP capture (`/vpc-wrap` → `vp_capture_session`), which is
why grok stays in the inline-archive allow-list.

**On Zed, durability depends on which pane you work in, and that is a choice
you make outside vibe-palace.** A **Claude-shaped** ACP agent configured under
Zed's `agent_servers` inherits Claude Code's hook path and reaches the full
durable footprint with no vibe-palace change. **This is not a property of
ACP** — the agent must be Claude-shaped; `gemini` configured and ended the
same way produced nothing. And the session closes when you **archive the
thread**: that is what fires `SessionEnd`. Zed's **native** pane is the
default, is MCP-only, and is not the supported path.

Zed is **not** a first-class host and no Zed extension ships:
`doc/PRD-vibe-palace-zed-assistant.md` describes an unbuilt design and carries
a banner saying so. The native pane's remaining gap is tracked as
`zed-pane-capture-parity` (open, **high**, scoped to the native pane).
Details: [Tutorial — Zed](doc/TUTORIAL.md#zed),
[Tutorial — Grok Build](doc/TUTORIAL.md#grok-build-xai),
[durability by host](doc/COMMANDS-AND-SKILLS.md#durability-by-host-claude-vs-hook-less),
[inline archive](doc/ARCHITECTURE.md#inline-transcript-archive-on-hook-less-hosts).

---

## Customizing Commands and Skills

Vibe-palace ships its command and skill catalog compiled into the `vp`
binary. These embedded templates are the **floor** — a last-resort
default. Your vault is the primary editable surface.

- **`<vault>/Templates/commands/`** — materialized from the embedded
  defaults the first time you run `vp init`. Edit files here freely:
  a `templates.lock` sidecar (`<vault>/.vibe-palace/templates.lock`)
  records the embedded SHA that shipped with your binary, so
  `vp commands upgrade` can tell a user edit apart from a binary
  bump. `<vault>/Templates/workflow.md` and
  `<vault>/Templates/resume.md` are materialized the same way.
- **`<vault>/Projects/<slug>/commands/`** — per-project override
  directory, scaffolded with a README stub. A file here shadows the
  vault-level `Templates/` copy for that project only, letting one
  project diverge permanently without affecting the others.
  `skills/` works the same way.

**Precedence (first match wins):** room > wing > project > vault >
embedded. For example,
`<vault>/Projects/myapp/commands/wrap.md` takes precedence over
`<vault>/Templates/commands/wrap.md`, which shadows the embedded
`wrap.md` baked into the binary.

### Three-SHA reconciliation (a vignette)

You installed vibe-palace last month. `vp init` wrote the command
templates into `<vault>/Templates/commands/` and recorded their
SHAs in `templates.lock`.

Since then you've edited `wrap.md` to add a team-specific
integration-test gate. Your vault's `wrap.md` now differs from the
lock SHA — that's a **user edit**.

Today you upgrade `vp` to a new release. The new binary ships a
revised embedded `wrap.md`. The embedded SHA now also differs from
the lock SHA — that's a **binary bump**.

When you run `vp commands upgrade`:

- For files you never touched (same vault SHA as lock), it
  auto-updates to the new embedded version and writes a `.bak` of
  the previous vault content.
- For files where both you **and** the embedded default have
  diverged from the recorded lock SHA (like your `wrap.md`), it
  prompts: `[s]kip / [o]verwrite (writes .bak) / [n]ew-sidecar`.
  Uppercase `S`/`O`/`N` applies the choice to every remaining
  conflict.

No magic. No merge. You stay in control of anything you've changed,
and get everything you haven't for free.

### Promoting vault edits back to source

Vibe-palace does not know where your `vp` source checkout lives, so
promotion is manual on purpose: copy the file from
`<vault>/Templates/commands/<name>.md` to
`internal/templates/templates/commands/<name>.md` in your
vibe-palace checkout and commit. The next `vp` build ships your
edit as the new embedded floor for everyone.

---

## How This Compares to vibe-vault

Vibe-palace's predecessor,
[vibe-vault](https://github.com/suykerbuyk/vibe-vault) (`vv`),
pioneered the session-observability story: hook into Claude Code,
parse the JSONL transcript, turn it into structured Obsidian notes.
Vibe-palace keeps the vault-as-source-of-truth philosophy, adds a
memory fabric and team-coordination layer on top, and has since
absorbed most of `vv`'s distinctive strengths.

| Axis | vibe-vault (`vv`) | vibe-palace (`vp`) |
|------|-------------------|--------------------|
| Primary surface | Post-hoc hook reads JSONL transcripts | Live MCP server; agents call tools on demand |
| Session capture | Automatic via `SessionEnd` / `Stop` / `PreCompact` | Both: agent-driven `vp_capture_session` **and** automatic `vp hook` on the same events (Claude Code) |
| Search | Heuristic cross-session linking | Hybrid vector + structural semantic search, cross-project |
| Tasks / epics | — | Vault-resident task graph with derived epics, cross-project addressing |
| Knowledge graph | — | Temporal entity-relationship graph with time-travel |
| Friction analytics | `vv friction` / `trends` / `effectiveness` | `vp friction` / `vp trends` / `vp effectiveness` |
| LLM enrichment | Enrichment at capture time | Agent authors sections directly; optional enrichment layer (ADR-005) |
| IDE coverage | Claude Code + Zed | Claude Code + Grok Build + Zed + Cursor + any MCP host |
| LLM dependency | Optional enrichment layer | None required for capture; optional for tuning |

Zed thread ingestion is also covered: `vp archive --adapter zed` reads
Zed's SQLite thread DB and archives threads by id, alongside the
default Claude Code JSONL adapter.

In short: `vv` remains a fine passive observer for a single developer;
reach for `vp` when you want agents to actively *use* shared memory —
and when more than one person (or more than one project) is involved.

---

## Documentation

- [Tutorial](doc/TUTORIAL.md) — installation, editor setup, daily workflow
- [Commands & Skills](doc/COMMANDS-AND-SKILLS.md) — the full catalog, precedence tiers, and authoring guide
- [Architecture](doc/ARCHITECTURE.md) — system design and package reference
- [Testing](doc/TESTING.md) — test strategy and integration test inventory
- [Migration](doc/MIGRATION.md) — migrating from VibeVault and MemPalace
- [PRD](doc/PRD-vibe-palace.md) — full product requirements
- [ADR 001: Transcript Archive](doc/adr/001-transcript-archive.md) — copyright-provenance ledger format
- [ADR 003: Vault Write Locking](doc/adr/003-vault-write-locking.md) — per-path locks and the CAS contract
- [ADR 006: Derive, Don't Ask](doc/adr/006-derive-dont-ask.md) — where business logic lives: DERIVE / DECLARE / DEFER
- [ADR 007: Vault Audit & Archive Backfill](doc/adr/007-vault-audit-and-archive-backfill.md) — the vault audit and accepted-debt baseline
- [ADR 008: The Instruction Manual Lives in the Binary](doc/adr/008-instruction-manual-lives-in-the-binary.md) — served doctrine, thin project workflow
- [ADR 009: Inviolable Core, Delivered Whole or Fail-Loud](doc/adr/009-inviolable-core-delivered-whole-or-fail-loud.md) — honest context budgets

## License

Dual-licensed under
[Apache 2.0](https://www.apache.org/licenses/LICENSE-2.0) or
[MIT](https://opensource.org/licenses/MIT), at your option. See
[LICENSE](LICENSE) for details.
