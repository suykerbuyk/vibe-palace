# Vibe-Palace

> **Persistent memory for AI coding assistants.** An MCP server that
> captures, indexes, and serves session context — so your AI picks up
> where you left off, every time, on every machine, in any editor.

**License:** MIT OR Apache-2.0 · **Status:** early public release · **Stack:** Go 1.25, single static binary

---

## The Problem

AI coding assistants forget everything between sessions. Every new
conversation starts cold: no architectural decisions, no project
state, no awareness of what was tried yesterday. Teams re-explain the
same context every morning. Knowledge that took hours to build up
evaporates the moment a session ends.

## The Solution

Vibe-palace is an **MCP server** that gives any AI assistant — Claude
Code, Cursor, or any MCP host — durable, queryable memory. Sessions
are captured as structured markdown in a git-versioned vault,
indexed with hybrid vector + structural semantic search, and served
back on demand through a small set of standard tools.

One call at session start (`vp_bootstrap_context`) restores
workflow, resume, active tasks, and recent history. One call at
session end (`vp_capture_session`) records what changed and why.
Between them, `vp_search`, `vp_kg_query`, and `vp_get_project_context`
let the agent find anything it needs without loading a giant
upfront context dump.

---

## Quick Start (5 minutes)

**Prerequisites:** Go 1.25+, `~/.local/bin` in `PATH`.

```bash
git clone https://github.com/suykerbuyk/vibe-palace.git
cd vibe-palace
make build && make test && make install
```

Create `~/.config/vibe-palace/config.toml`:

```toml
vault_path = "/home/you/your-vault"
```

Initialize a project:

```bash
cd ~/code/your-project
vp init
```

`vp init` scaffolds an `agentctx/` package in the vault and writes
`.claude/commands/vpc-*.md` + `.grok/plugins/vibe-palace/commands/vpc-*.md` + `.claude/skills/vps-*/SKILL.md` + `.grok/skills/...` shims
into the project so Claude Code (or any `vpc-`/`vps-`-aware editor)
discovers the full vibe-palace command and skill catalog.

Then add `vibe-palace` to your editor's MCP config (see the
[Tutorial](doc/TUTORIAL.md) for per-editor setup). Start a new
session with `/vpc-restart` and the agent will load full context on
turn one.

---

## What You Get

- **Context injection** — single-call restoration of workflow, resume,
  tasks, and recent sessions via `vp_bootstrap_context`.
- **Session capture** — model-agnostic recording with automatic
  chunking, embedding, and semantic indexing.
- **Semantic search** — hybrid vector + structural search across all
  captured knowledge, with optional cross-project queries.
- **Knowledge graph** — temporal entity-relationship graph with
  time-travel queries, integrated with session capture.
- **Palace navigation** — spatial metaphor (wings/rooms/halls) for
  browsing and traversing stored knowledge.
- **Friction tracking** — automated session difficulty scoring with
  weekly trend analysis.
- **Migration** — import existing VibeVault sessions and MemPalace
  data into the palace.
- **Room classification tuning** — configurable keyword weights,
  algorithmic audit, offline LLM-assisted weight discovery.
- **Vault integrity audit** — `vp audit vault` checks the whole vault
  against design intent (transcript round-trips, project-tree coherence,
  KG portability, resume discipline, iteration headings) against an
  accepted-debt baseline that may only shrink, and recovers stranded
  transcript↔note links via `vp archive backfill` / `vp archive link`
  (see ADR-007).
- **`vp` CLI** — 22 commands, man pages, shell completions.
- **Cross-IDE shims** — `vp init` writes slash-command shims
  (`.claude/commands/vpc-*.md` and `.grok/plugins/vibe-palace/commands/vpc-*.md`)
  and skill directories (`.claude/skills/`, `.grok/skills/`, `.cursor/rules/`)
  (`.claude/skills/vps-*/SKILL.md`) so every vibe-palace capability
  is discoverable by fuzzy-matching `/vpc-` or `/vps-` in Claude
  Code. `vp commands upgrade` and `vp skills upgrade` keep both sets
  in sync with embedded updates.

### Embedded commands

| Command          | Purpose                                                        |
|------------------|----------------------------------------------------------------|
| `/vpc-restart`   | Turn-1 session bootstrap. Pulls vault, sweeps orphan plans, reports retirement candidates for the human to decide, loads context. |
| `/vpc-wrap`      | Session wrap. Quality-gate check, captures session, updates resume, stages files, syncs the vault. |
| `/vpc-capture`   | Mid-session checkpoint without full wrap.                      |
| `/vpc-review-plan` | Senior-staff-engineer review of a task plan before implementation. |
| `/vpc-cancel-plan` | Record the rationale for abandoning a plan so future sessions don't re-litigate it. |
| `/vpc-execute-plan` | Orchestrated execution of a multi-phase plan via subagents. |
| `/vpc-license`   | Apply or refresh dual MIT/Apache-2.0 licensing and SPDX banners. |
| `/vpc-makefile`  | Audit or create a self-documenting Makefile facade for the native build system. |
| `/vpc-vault-audit` | Adversarial vault audit: runs `vp audit vault` and adds the human-judgment layer code cannot (Layer 2). |

### Embedded skills

- **`vps-startup-analyst`** — a worked-example domain-expert skill
  (SKILL.md + 5 reference documents on CapEx/OpEx, competitive
  landscape, funding, reality validation, strategic partnerships).
  Use it as a template for your own skill personas.

## Supported IDE Surfaces

| Surface           | Commands rendering            | Skills rendering                  |
|-------------------|-------------------------------|-----------------------------------|
| Claude Code       | `.claude/commands/vpc-*.md`                  | `.claude/skills/vps-*/SKILL.md`          |
| Grok Build        | `.grok/plugins/.../commands/vpc-*.md` (native) | `.grok/skills/vps-*/` + `/vpc` hub       |
| Cursor (rules/)   | via MCP (`vp_get_command`)    | `.cursor/rules/vps-*.mdc`         |
| Any MCP host      | `vp_get_command` tool         | `vp_skill` tool                   |

---

## Customizing Commands and Skills

Vibe-palace ships a small catalog of command and skill templates
compiled into the `vp` binary. These embedded templates are the
**floor** — a last-resort default. Your vault is the primary
editable surface.

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

You installed vibe-palace last month. `vp init` wrote eight command
templates into `<vault>/Templates/commands/` and recorded their
SHAs in `templates.lock`.

Since then you've edited `wrap.md` to add a team-specific
integration-test gate. Your vault's `wrap.md` now differs from the
lock SHA — that's a **user edit**.

Today you upgrade `vp` to a new release. The new binary ships a
revised embedded `wrap.md` (added a richer two-copy commit.msg
workflow). The embedded SHA now also differs from the lock SHA —
that's a **binary bump**.

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
Vibe-palace keeps the vault-as-source-of-truth philosophy but
changes the capture model and adds a memory fabric on top.

| Axis                  | vibe-vault (`vv`)                                       | vibe-palace (`vp`)                                     |
|-----------------------|---------------------------------------------------------|--------------------------------------------------------|
| Primary surface       | Post-hoc hook reads JSONL transcripts                   | Live MCP server; agents call tools on demand           |
| Session capture       | Automatic via `SessionEnd` / `Stop` / `PreCompact`      | Explicit via `vp_capture_session` (agent-driven)       |
| Search                | Heuristic cross-session linking                         | Hybrid vector + structural semantic search             |
| Knowledge graph       | —                                                       | Temporal entity-relationship graph with time-travel    |
| Navigation metaphor   | File browse + Dataview                                  | Palace: wings → rooms → halls                          |
| IDE coverage          | Claude Code + Zed                                       | Claude Code + Cursor + any MCP host                    |
| LLM dependency        | Optional enrichment layer                               | None required for capture; optional for tuning         |

### What vibe-vault still does better

Vibe-palace is intentionally a different product, not a drop-in
replacement. If you want any of the following, vibe-vault is the
right tool — and the two can run alongside each other:

- **Fully automatic, zero-agent-effort session capture.** `vv`
  hooks into Claude Code and writes a note at `SessionEnd` with no
  prompting required. Vibe-palace relies on the agent calling
  `vp_capture_session` (the `/vpc-wrap` command does this, but it's
  opt-in).
- **LLM-enriched note synthesis.** `vv` can call an LLM at capture
  time to produce "What Happened", "Key Decisions", and "Open
  Threads" sections. Vibe-palace lets the agent author these
  directly — no enrichment layer.
- **Friction analytics.** `vv friction`, `vv trends`,
  `vv effectiveness` surface correction density, model regressions,
  and context-effectiveness signals across months of history. Not
  in vibe-palace.

(Zed thread ingestion is no longer vibe-vault-only: `vp archive
--adapter zed` reads Zed's SQLite thread DB and archives threads by
id, alongside the default Claude Code JSONL adapter.)

In short: reach for `vv` when you want a passive observer. Reach for
`vp` when you want the agent to actively *use* memory during the
session.

---

## Documentation

- [Tutorial](doc/TUTORIAL.md) — installation, editor setup, daily workflow
- [Architecture](doc/ARCHITECTURE.md) — system design and package reference
- [Testing](doc/TESTING.md) — test strategy and integration test inventory
- [Migration](doc/MIGRATION.md) — migrating from VibeVault and MemPalace
- [PRD](doc/PRD-vibe-palace.md) — full product requirements (Phases 1–10, 12–18 implemented; Phase 11 planned)
- [ADR 001: Transcript Archive](doc/adr/001-transcript-archive.md) — copyright-provenance ledger format and reasoning
- [ADR 006: Derive, Don't Ask](doc/adr/006-derive-dont-ask.md) — where business logic lives: DERIVE / DECLARE / DEFER
- [ADR 007: Vault Audit & Archive Backfill](doc/adr/007-vault-audit-and-archive-backfill.md) — the five-dimension vault audit, the accepted-debt baseline, and the transcript-link backfill

## License

Dual-licensed under
[Apache 2.0](https://www.apache.org/licenses/LICENSE-2.0) or
[MIT](https://opensource.org/licenses/MIT), at your option. See
[LICENSE](LICENSE) for details.
