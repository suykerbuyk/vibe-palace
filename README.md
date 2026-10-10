# Vibe-Palace

> **Persistent memory and team coordination for AI-assisted development.**
> A single-binary MCP server that gives every AI coding assistant on your
> team durable, queryable project context — and keeps every byte of it out
> of your source tree.

**License:** MIT OR Apache-2.0 · **Status:** early public release · **Stack:** Go 1.25, single static binary, zero CGO

**Versioning:** a release tag is `v<MCP surface>.<data format>.<build>`
([ADR-011](doc/adr/011-open-task-header-schema-and-format-axis.md)), so
`v8.2.0` means MCP surface 8 and vault data format 2. The major number
tracks the tool surface, not maturity. `vp version --surface` prints the
installed binary's surface.

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

One call at session start (`vp_bootstrap_context`) returns an index of
the project's state: the head of the task queue, recent sessions, and
the `resume_uri` / `workflow_uri` handles the agent reads with
`vp_read_resource`. One call at session end
(`vp_capture_session`) records what changed and why. Between them,
`vp_search`, `vp_kg_query`, and `vp_get_project_context` let the agent
find anything it needs without a giant upfront context dump.

---

## Built for Teams: the Coordination Model

### A clean source tree, by construction

Nothing the AI tooling produces belongs to your project's git history,
and vibe-palace enforces that mechanically rather than by convention:

- **AI artifacts are host-local and gitignored by the tool itself.**
  `vp` maintains a canonical ignore set (`/CLAUDE.md`, `/AGENTS.md`,
  `/commit.msg`, `/.claude/`, `/.grok/`, `/.vibe-palace/`) and reconciles
  it into the project's `.gitignore` — these files are written into the
  working tree for the host to find, and must never be committed. Two
  exceptions are outside that set. Cursor skill rules
  (`.cursor/rules/vps-*.mdc`, written when `.cursor/` exists) are not
  ignored. A `.cursorrules`, `.rules` or `.github/copilot-instructions.md`
  file that already exists also gets the managed block written into it in
  place, so if the repo tracks that file, the block shows up in the diff.
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
- **Moving a task between projects** is one call:
  `vp_manage_task action=move to_project=<slug>`. Only an active task
  moves. The move is refused if the task's `parent` or any `depends_on`
  does not resolve in the destination, or if a task with that slug is
  already there. It adds a "Moved from <source>" section in the
  destination and files a tombstone at
  `Projects/<source>/tasks/cancelled/<task>.md`. Parent/dependency edges
  are per-project by design — each project's graph stays
  self-contained. (Moving a *whole project* to another vault is covered
  under [Multiple vaults and hosts](#multiple-vaults-and-hosts).)
- **Search spans the portfolio.** `vp_search_cross_project` runs
  semantic search across every indexed project, and `vp_list_projects`
  enumerates the portfolio — so "have we solved this before, anywhere?"
  is one tool call.

### Many developers, many machines

- **The vault syncs like code, because it is code-adjacent.** Standard
  git remotes; `vp vault sync` (or the `vp_vault_sync` MCP tool) pulls,
  tidies machine-generated capture artifacts, and pushes. Every vault
  commit is stamped with the hostname that made it.
- **The search index is compiled per host, not synced.** The tracked
  corpus is the transcript archives; each host rebuilds its own semantic
  index from them with `vp index rebuild` and checks coverage with `vp
  index status` (see
  [The host-local search index](doc/TUTORIAL.md#the-host-local-search-index)).
  A freshly cloned host carries a backlog until its first rebuild.
- **Concurrent writers are arbitrated, not trusted.** Per-path advisory
  locks serialize writers of the same file; a single repo-root commit
  lock serializes all vault committers through the git index; mutating
  file tools support compare-and-set (`expected_sha256`) so a stale
  writer is refused instead of silently clobbering a teammate.
- **Mixed binary versions can't corrupt shared state.** Schema-bearing
  vault writes stamp the MCP surface version; an older binary
  encountering a newer vault refuses to write and names the upgrade —
  a stale install fails loudly instead of quietly downgrading the vault.
  A second axis, the vault **data format**, gates in the other direction.
  A binary refuses a vault whose recorded on-disk format is *behind* the
  format it requires, and it tells you to run the data migration
  (`internal/surface/format.go`). `vp check --check surface` reports the
  surface gate.

### Multiple vaults and hosts

A host can hold several vaults, for example a personal vault and a team
vault. Each project resolves to exactly one vault in three tiers
([ADR-012](doc/adr/012-vault-resolution-precedence-and-host-project-bindings.md)):
1. a `vault_path` in the checkout's untracked `.vibe-palace.toml`;
2. this host's `[project_vaults].<slug>` binding in the global config;
3. the global `vault_path`.

A committed `.vibe-palace.toml` names only the project, because a vault
path belongs to one host. Resolution fails closed: a conflict or a broken
binding refuses rather than falling back to the default vault.

Moving projects between vaults uses these commands:

| Command | Purpose |
|---------|---------|
| `vp vault init <path> --remote <name>=<url>...` | Create a new, empty vault and publish it to every remote (each must be empty). |
| `vp vault copy <project>... --from <remote-url> [--vault <path>]` | Acts on the receiving vault (this host's default vault, or `--vault`): copy projects from another vault's **published remote** in one verified commit. |
| `vp vault project delete <project>... (--moved-to <url> \| --discard) [--vault <path>]` | Acts on the vault the projects leave (default vault, or `--vault`): delete them in one published commit that writes their departure records (`Audits/departures/<project>.json`). |
| `vp config bind <slug>... --vault <path>` | Bind one or more projects to a vault on this host (one `[project_vaults]` write). |
| `vp vault clone <url> <path> [--bind <project>...]` | On another host, after `vp vault pull`: clone the vault and bind the projects in one step. |
| `vp vault status` | Each remote's sync state (ahead, behind, diverged, reachable) plus working-tree dirt. |

`copy`, `project delete` and `clone` share a pattern. You run them with
`--dry-run` first, and the dry run prints a plan, a digest and a
paste-ready line that carries `--expect <digest>`. Each real run also
prints its own undo lines, to paste as printed. Copy and delete print a
`git revert` plus push lines. Clone prints removal of the clone and a
restore of the config backup. `vault init`'s undo also removes the new
directory.

Once a project has departed a vault, vp refuses any further write into it
there. A stale session therefore cannot re-create the project in its old
vault.

The step-by-step procedure, with its undo order, is in
[doc/VAULT-LIFECYCLE.md](doc/VAULT-LIFECYCLE.md). The design is in
[ADR-013](doc/adr/013-vault-project-lifecycle-and-departure-records.md).

---

## Quick Start (5 minutes)

**Install a release binary.** The
[releases page](https://github.com/suykerbuyk/vibe-palace/releases) has
`vp_<version>_<os>_<arch>` archives for linux and darwin (amd64 and
arm64, `.tar.gz`) and windows amd64 (`.zip`), plus a sha256
`checksums.txt`. Each archive holds `vp`, `LICENSE` and `README.md`.

```bash
curl -LO https://github.com/suykerbuyk/vibe-palace/releases/download/v8.2.0/vp_8.2.0_linux_amd64.tar.gz
mkdir -p ~/.local/bin
tar -xzf vp_8.2.0_linux_amd64.tar.gz -C ~/.local/bin vp   # ~/.local/bin must be in PATH
vp version
```

**Or build from source** (Go 1.25+). `make install` puts the binary and
the generated man pages under `~/.local`:

```bash
git clone https://github.com/suykerbuyk/vibe-palace.git
cd vibe-palace
make build && make test && make install
```

**Register the MCP server with your AI host.** `vp init` does not do this
step for you. Restart the host afterwards.

```bash
vp mcp install --claude-plugin   # Claude Code; also --grok, --zed (several at once is fine)
```

**Initialize a project.** The first run also creates the global config and
the vault (default `~/vibe-palace-vault`, or pass `--vault-path`):

```bash
cd ~/code/your-project
vp init
vp check          # installation, config, vault, embedder and host rows
```

If the repo already carries hand-written `CLAUDE.md` / `AGENTS.md` /
`.cursorrules` content, `vp absorb --dry-run` shows how it would be moved
into the vault. The first semantic operation downloads the embedding
model once, so it needs network access the first time.

`vp init` scaffolds the project's space in the vault and writes the
slash-command and skill shims (`.claude/commands/vpc-*.md`,
`.claude/skills/vps-*/SKILL.md`, and Grok/Cursor equivalents where
detected) so your editor discovers the full vibe-palace command catalog.
When the Claude Code (or Grok) plugin from `vp mcp install` is already
healthy, `vp init` leaves the `vpc-*`/`vps-*` shims for that host to the
plugin and does not write the project copies. Bare `/vpc-*` may then need
the `vibe-palace:` prefix.
It also installs the `commit.msg` post-commit reaper described under
[Commands and Skills](#commands-and-skills), unless the repo already has a
post-commit hook of its own.

Hosts without a `vp mcp install` flag (Cursor, other MCP hosts) take a
manual MCP config entry; see the [Tutorial](doc/TUTORIAL.md) for
per-editor setup. Start a new session with `/vpc-restart` and the agent
loads full context on turn one.

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
  completion (`vp_manage_task`, `vp_list_tasks`, `vp tasks`). On the CLI:
  - `vp tasks` shows open work grouped by epic, in dependency order.
  - `vp tasks epics` rolls up each root epic's open/total descendants.
  - `vp tasks read` / `vp tasks edit` open one task body.
  - `vp board` is a chronological Active / Icebox / History report,
    and it filters nothing by default.
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
  against design intent, across every dimension the audit registry holds,
  against an accepted-debt baseline that may only shrink (see ADR-007).
  The dimensions are deliberately not listed here: the report names each
  one and prints the command that reproduces its numbers, so `vp audit
  vault` is the live list.
- **Worktree isolation** — `vp worktree create|remove|list` gives plan
  execution its own branch + working tree, keeping agent edits off your
  checkout until a human merges.
- **Migration** — import existing VibeVault sessions and MemPalace data
  into the palace.
- **Vault lifecycle** — create vaults, move projects between them, and
  bind projects per host: `vp vault init|copy|clone|project delete`,
  `vp config bind` (see [Multiple vaults and hosts](#multiple-vaults-and-hosts)).
- **`vp` CLI** — full command-line surface (`vp --help`, then
  `vp help <command>`, is the authoritative live reference). `make man`
  generates man pages for a subset of the commands.

---

## Commands and Skills

Every capability below is a markdown body served over MCP: the host
holds only a thin shim that calls `vp_cmd` (commands) or `vp_skill`
(skills), so all supported editors run the *same* command, resolved
through the same precedence chain.

### `/vpc-*` commands (embedded set)

| Command | Purpose |
|---------|---------|
| `/vpc-restart` | Turn-1 session bootstrap: vault pull and tidy of capture residue, context load, doctrine fetch. |
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
| `/vpc-herdr` | Load this session's Herdr skill from the installed binary. Inside a Herdr pane, or from outside with an operator-named `herdr --session`. |

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
| `/vps-chair` | Orchestrate subordinate implementors, as visible Herdr panes or as ephemeral subagents without Herdr; loads `/vpc-restart` (and `/vpc-herdr` when Herdr is in use) if this session has not already run them. |
| `/vps-pair-reviewer` | Dual-agent pairing: you hold architecture and review while another agent is the implementation orchestrator. |
| `/vps-second-opinion` | Adversarial review by a different model, headless; findings are witness statements until re-derived from source. |
| `/vps-startup-analyst` | Domain-expert persona (business-plan analysis) with reference library — a worked template for your own skill personas. |

> **This table is a snapshot of the embedded floor.** The live,
> tier-resolved catalog (including your vault and per-project overrides)
> is derived, never hand-maintained — list it with `vp commands list` /
> `vp skills list`, or over MCP with `vp_list_commands` /
> `vp_list_skills`. The embedded floor itself is
> `ls internal/templates/templates/commands/ internal/templates/templates/skills/`
> in the source tree.
>
> **Skill shims are labels, not triggers.** A skill shim's description is
> only `Vibe-palace skill — <short brief>`. On Claude Code the shim also sets
> `disable-model-invocation: true`, so a persona is adopted only when you
> type `/vps-<name>` (or `vps-<name>`). On Cursor and Grok the label is the
> only safeguard.

### Supported platforms

| Host | MCP server | Commands / skills | Automatic hook capture |
|------|-----------|-------------------|------------------------|
| **Claude Code** | `vp mcp install --claude-plugin` | the plugin's `vpc-*` commands and `vps-*` skills; project `.claude/commands/vpc-*.md` + `.claude/skills/vps-*/SKILL.md` only when the plugin surface is not healthy | ✅ `vp hook` on SessionEnd/Stop/PreCompact |
| **Grok Build** | `vp mcp install --grok` | native `.grok/plugins/.../commands/vpc-*.md` + `.grok/skills/` + `/vpc` hub | ✅ **when wired** — `vp hook` accepts Grok's own wire dialect; vibe-palace does not assume it, so MCP capture stays the mechanism to rely on (**inline archive defaults on** for handshake-derived grok/xai with a `transcript`) |
| **Zed — Claude-shaped ACP agent** (the supported Zed path) | `vp mcp install --zed` | via `AGENTS.md` managed block → `vp_cmd` / `vp_skill` | ✅ full Claude hook path, **fired by archiving the thread** — not by idling, `restart`, or `exit` |
| **Zed — native pane** (Zed's default) | `vp mcp install --zed` | same managed block | — MCP-only: inline archive when `transcript` is supplied; **fails loud** when it is not |
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
a banner saying so.
Details: [Tutorial — Zed](doc/TUTORIAL.md#zed),
[Tutorial — Grok Build](doc/TUTORIAL.md#grok-build-xai),
[durability by host](doc/COMMANDS-AND-SKILLS.md#durability-by-host-claude-vs-hook-less),
[inline archive](doc/ARCHITECTURE.md#inline-transcript-archive-on-hook-less-hosts).

---

## Customizing Commands and Skills

Vibe-palace ships its command and skill catalog compiled into the `vp`
binary. These embedded templates are the **floor** — the default every
command and skill resolves from. You customise them in the vault, at one
of two tiers:

- **`<vault>/Templates/`** — the vault-wide override tier, **empty by
  default**. `vp init` never writes it; the embedded floor serves every
  built-in, including the `workflow.md` and `resume.md` templates. A
  file here applies to every project: a **new name** adds a vault-wide
  command or skill, and a built-in's name **overrides** that built-in
  everywhere.
- **`<vault>/Projects/<slug>/commands/`** — the per-project tier,
  scaffolded by `vp init` with a README stub. A file here shadows the
  built-in (and any vault-wide override) for that project only.
  `skills/` works the same way.

No vp reconciler and no upgrade command changes an override in either
tier. "Safe" means exactly that — safe from vp's reconcilers and upgrade
commands; `vp_vault_write` / `vp_vault_edit` / `vp_vault_move` /
`vp_vault_delete` and `vp vault commit --paths .` are direct edits and
reach any tier. What to know about a vault-wide override of a built-in:

- **Edit it before you sync.** `vp config sync` decides whether a
  `Templates/` copy is vp's by its bytes alone. A copy identical, line
  endings aside, to the current built-in or to a version in the frozen
  `internal/templates/shipped.txt` — every version reachable from
  `1f3bb62`, plus the two rows of tag `pre-rebase-501c96e` — is vp's, not
  an override, and is pruned.
- **It shadows the built-in for every project**, and it misses changes
  the binary depends on — `commands/wrap.md` supplies the
  `expected_sha256` that `vp_update_resume` demands, for example. So
  `vp_check` `template-drift` lists every override as `Info`.
- **To drop one, name it:** `vp commands reset NAME` /
  `vp skills reset NAME` (a backup is kept), not `vp vault delete`.

**Precedence (first match wins):** room > wing > project > vault >
embedded. For example,
`<vault>/Projects/myapp/commands/wrap.md` takes precedence over
`<vault>/Templates/commands/wrap.md` (if one exists), which shadows the
embedded `wrap.md` baked into the binary.

### What each command does to `Templates/`

- **`vp init`** — never writes, prunes or reconciles `Templates/`. It
  only reads it, to give each project a shim for your vault-wide
  commands and skills.
- **`vp config sync`** — the override-only reconcile. It never writes a
  template, never prompts about one, and keeps no host-local state (no
  lock file, no sidecar). A copy identical, line endings aside, to the
  current built-in or to an earlier version vibe-palace shipped (the
  frozen `internal/templates/shipped.txt`) is pruned, with no backup —
  those bytes are recoverable from the binary or from vibe-palace's git
  history. Anything else is an override, and is kept. On a git vault it
  removes a copy only after checking that HEAD's copy, and each
  remote's, is vp's too, then commits the removal; when HEAD's copy is
  operator content it restores it in place instead, and a prune git
  cannot verify is deferred. On a vault that is its own repository it
  also commits a removal of vp-shipped bytes already pending in the
  worktree, and removes the retired `.vibe-palace/templates.lock` when
  it is untracked and not ignored.
- **`vp commands upgrade` / `vp skills upgrade`** — never write or
  remove a `Templates/` file, in any mode. They list each override of a
  built-in as `[keep]` and name the reset that removes it, and each copy
  of an earlier shipped version as `[stale]` (`vp config sync` prunes
  it).
  `--overwrite` accepts only vp-owned changes (shims, agent-file
  blocks, the project `.gitignore`, the commit hook).
- **`vp commands reset NAME...` / `vp skills reset NAME...`** — remove
  the named overrides, so the built-in serves them again. Each is first
  backed up to `<file>.<sha12>.bak`, a name derived from its bytes that
  is never overwritten; a copy of vp-shipped bytes (the built-in, or an
  earlier shipped version) needs no backup. On a vault that is its own
  git repository the removal is committed locally; `vp vault sync`
  publishes it. `--dry-run` previews it.

The full walkthrough is in
[Tutorial — Customizing a command template](doc/TUTORIAL.md#customizing-a-command-template).

### Promoting overrides back to source

Vibe-palace does not know where your `vp` source checkout lives, so
promotion is manual on purpose: copy your override from
`<vault>/Projects/<slug>/commands/<name>.md` (or
`<vault>/Templates/commands/<name>.md`) to
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

Zed thread ingestion is also covered: `vp archive threads --adapter zed`
lists threads and `vp archive create --adapter zed --session-id <id>` reads
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
- [Vault Lifecycle](doc/VAULT-LIFECYCLE.md) — new vaults, moving projects between vaults, per-host bindings, undo
- [Migration](doc/MIGRATION.md) — migrating from VibeVault and MemPalace
- [Absorb](doc/absorb.md) — moving existing agent-context files into the vault
- [Template Policy](doc/TEMPLATE_POLICY.md) — how embedded templates, overrides and resets interact
- [Release notes: v8.2.0](doc/rollout/v8.2.0-release-notes.md) (earlier: [v7.2.0](doc/rollout/v7.2.0-release-notes.md))
- [PRD](doc/PRD-vibe-palace.md) — full product requirements
- [ADR 001: Transcript Archive](doc/adr/001-transcript-archive.md) — copyright-provenance ledger format
- [ADR 003: Vault Write Locking](doc/adr/003-vault-write-locking.md) — per-path locks and the CAS contract
- [ADR 006: Derive, Don't Ask](doc/adr/006-derive-dont-ask.md) — where business logic lives: DERIVE / DECLARE / DEFER
- [ADR 007: Vault Audit & Archive Backfill](doc/adr/007-vault-audit-and-archive-backfill.md) — the vault audit and accepted-debt baseline
- [ADR 008: The Instruction Manual Lives in the Binary](doc/adr/008-instruction-manual-lives-in-the-binary.md) — served doctrine, thin project workflow
- [ADR 009: Inviolable Core, Delivered Whole or Fail-Loud](doc/adr/009-inviolable-core-delivered-whole-or-fail-loud.md) — honest context budgets (superseded in full; historical)
- [ADR 010: The Surface Gate Stays at the Dispatch Seam](doc/adr/010-surface-gate-at-the-dispatch-seam.md) — where the surface write-gate is enforced
- [ADR 011: Open Task-Header Schema and the Data-Format/Release-Versioning Coupling](doc/adr/011-open-task-header-schema-and-format-axis.md) — the data-format axis and the release-tag scheme
- [ADR 012: Vault Resolution Precedence and Host-Local Project Bindings](doc/adr/012-vault-resolution-precedence-and-host-project-bindings.md) — how a checkout finds its vault
- [ADR 013: Vault Project Lifecycle Commands and Departure Records](doc/adr/013-vault-project-lifecycle-and-departure-records.md) — copy, delete, departure records
- All ADRs: [doc/adr/](doc/adr/)

## License

Dual-licensed under
[Apache 2.0](https://www.apache.org/licenses/LICENSE-2.0) or
[MIT](https://opensource.org/licenses/MIT), at your option. See
[LICENSE](LICENSE) for details.

The vector index uses [coder/hnsw](https://github.com/coder/hnsw), dedicated to
the public domain under [CC0 1.0](third_party/coder-hnsw/LICENSE).
