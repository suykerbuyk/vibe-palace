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

## Documentation

- [Tutorial](doc/TUTORIAL.md) — installation, editor setup, daily workflow
- [Architecture](doc/ARCHITECTURE.md) — system design and package reference
- [Testing](doc/TESTING.md) — test strategy and integration test inventory
- [Migration](doc/MIGRATION.md) — migrating from VibeVault and MemPalace
- [PRD](doc/PRD-vibe-palace.md) — full product requirements (Phases 1–10, 12
  implemented, Phase 11 planned)
