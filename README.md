# Vibe-Palace

An MCP server that gives AI coding assistants persistent memory across sessions.
Vibe-palace captures session context, indexes it with semantic search, and
delivers it back through a standard MCP interface — decoupled from any specific
AI provider or IDE.

**License:** MIT OR Apache-2.0

## Quick Start

Prerequisites: Go 1.25+, `~/.local/bin` in PATH.

```bash
git clone https://github.com/suykerbuyk/vibe-palace.git
cd vibe-palace
make build && make test && make install
```

Create `~/.config/vibe-palace/config.toml`:

```toml
vault_path = "/home/you/your-vault"
```

Connect your editor — see the [Tutorial](doc/TUTORIAL.md) for setup.

## What It Does

- **Context injection** — single-call restoration of workflow, resume, tasks,
  and recent sessions via `vp_bootstrap_context`
- **Session capture** — model-agnostic recording with automatic chunking,
  embedding, and semantic indexing
- **Semantic search** — hybrid vector + structural search across all captured
  knowledge, with cross-project support
- **Palace navigation** — spatial metaphor (wings/rooms/halls) for browsing
  and traversing stored knowledge
- **Friction tracking** — automated session difficulty scoring with weekly
  trend analysis
- **Knowledge graph** — temporal entity-relationship graph with time-travel
  queries, integrated with session capture
- **Migration** — import existing VibeVault sessions and MemPalace data
- **Room classification tuning** — configurable keyword weights, algorithmic
  audit, LLM-assisted weight tuning and keyword discovery (offline only)
- **CLI** — `vp` binary with 20+ commands, man pages, and shell completions
- **Slash-command shims** — `vp init` writes one `.claude/commands/vpc-<name>.md`
  per vibe-palace command, so typing `/vpc-` in Claude Code fuzzy-matches the
  whole command set; `vp commands upgrade` keeps the shim set in sync.
  `/vpc-restart` is the recommended first message of every Claude Code session —
  it's the deterministic turn-1 bootstrap trigger, since `CLAUDE.md` isn't
  loaded until after the human's first turn
- **Skill shims** — `vp init` also writes one `.claude/skills/vps-<name>/SKILL.md`
  per vibe-palace skill (directory-form persona), so typing `/vps-` surfaces
  the whole skill catalog. When `.cursor/rules/` exists, Cursor `.mdc` rules
  are emitted alongside. `vp skills upgrade` handles the interactive
  two-SHA refresh loop (grouped per skill by default, `--granular` for
  per-file prompts)

### Supported IDE surfaces

| Surface           | Commands rendering            | Skills rendering                  |
|-------------------|-------------------------------|-----------------------------------|
| Claude Code       | `.claude/commands/vpc-*.md`   | `.claude/skills/vps-*/SKILL.md`   |
| Cursor (rules/)   | via MCP (`vp_get_command`)    | `.cursor/rules/vps-*.mdc`         |
| Any MCP host      | `vp_get_command` tool         | `vp_skill` tool                   |

## Customizing commands and skills

Vibe-palace ships a small catalog of command templates (`restart`, `wrap`,
`review-plan`, `cancel-plan`, `capture`) compiled into the `vp` binary.
These embedded templates are the *floor* — a last-resort default. Your
vault is the primary editable surface.

- **`<vault>/Templates/commands/`** — materialized from the embedded
  defaults the first time you run `vp init`. Edit files here freely: a
  `templates.lock` sidecar (`<vault>/.vibe-palace/templates.lock`) records
  the embedded SHA that shipped with your binary, so `vp config sync` can
  tell a user edit apart from a binary bump. `<vault>/Templates/workflow.md`
  and `<vault>/Templates/resume.md` are materialized the same way.
- **`<vault>/Projects/<slug>/commands/`** — per-project override
  directory, scaffolded empty with a README stub. A file here shadows the
  vault-level `Templates/` copy for that project only, letting one project
  diverge permanently without affecting the others. `skills/` works the
  same way (directory + README stub; no embedded skill content yet — see
  the roadmap).

**Precedence (first match wins):** room > wing > project > vault >
embedded. For example, `<vault>/Projects/myapp/commands/wrap.md` takes
precedence over `<vault>/Templates/commands/wrap.md`, which in turn
shadows the embedded `wrap.md` baked into the binary.

**Promoting an edit back to source.** Vibe-palace cannot automate
promotion because at runtime it does not know where your vibe-palace
source checkout lives — this is a deliberate boundary, not a missing
feature. To land a vault-side template edit into the next release,
manually copy the file from `<vault>/Templates/commands/<name>.md` to
`<vp-repo>/internal/templates/templates/commands/<name>.md` in your
vibe-palace checkout and commit it like any other source change. The
next `vp` build will ship your edit as the new embedded floor for
everyone.

When a new `vp` binary changes an embedded default, `vp config sync`
reconciles the drift per the three-SHA table. For files you never
touched, it auto-updates and writes a `.bak` of the previous vault
content. For files where both you and the embedded default have
diverged from the recorded lock SHA, it presents a three-option prompt:
`[s]kip / [o]verwrite (writes .bak) / [n]ew-sidecar` (writing
`<name>.md.new` for side-by-side review). Uppercase `S`/`O`/`N` applies
the choice to every remaining item.

## Documentation

- [Tutorial](doc/TUTORIAL.md) — installation, editor setup, daily workflow
- [Architecture](doc/ARCHITECTURE.md) — system design and package reference
- [Testing](doc/TESTING.md) — test strategy and integration test inventory
- [Migration](doc/MIGRATION.md) — migrating from VibeVault and MemPalace
- [PRD](doc/PRD-vibe-palace.md) — full product requirements (Phases 1–10, 12
  implemented, Phase 11 planned)
