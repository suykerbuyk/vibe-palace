# Migration: vibe-vault to vibe-palace

This document describes the migration path from vibe-vault to vibe-palace.
The two systems share the same vault directory and are designed to coexist
during a gradual transition.

---

## Architecture: Shared Vault, Separate Subtrees

Both systems read their vault path from independent config files but point
to the same directory:

| System | Config file | vault_path |
|--------|-------------|------------|
| vibe-vault | `~/.config/vibe-vault/config.toml` | `~/obsidian/VibeVault` |
| vibe-palace | `~/.config/vibe-palace/config.toml` | `~/obsidian/VibeVault` |

Within the vault, each system owns distinct subtrees:

```
{vault}/
├── palace/                         # vibe-palace exclusive
│   ├── .local/models/              # ONNX model cache (machine-local)
│   └── {project}/                  # knowledge store (drawers, KG, embeddings)
│       ├── drawers/                # chunked + embedded content
│       └── kg/                     # knowledge graph (entities, triples)
├── Projects/                       # shared (vibe-vault writes, both read)
│   └── {project}/
│       ├── sessions/*.md           # session records (YAML frontmatter + transcript)
│       └── agentctx/               # tasks, iterations, resume, config
│           ├── tasks/
│           ├── iterations.md
│           ├── resume.md
│           └── config.toml
└── .vibe-vault/                    # vibe-vault exclusive
    └── session-index.json          # session metadata index
```

**No write conflicts:** vibe-palace never writes to `Projects/` or
`.vibe-vault/`. vibe-vault never writes to `palace/`. The `Projects/`
directory is the shared contract — vibe-vault creates session and workflow
files there; vibe-palace reads them for import and context injection.

---

## Current State (as of 2026-04-09)

- **715 sessions** in the vibe-vault index across **29 projects**
- **691 session markdown files** in `Projects/*/sessions/`
- **palace/ directory** contains only the ONNX model cache — no imported data yet
- vibe-palace Phases 1–10 complete (storage, MCP, context, search, capture, palace, KG, migration, CLI, docs)
- vibe-palace serves 26 MCP tools via stdio JSON-RPC
- vibe-vault remains the active session capture system (Claude Code hook)

---

## Migration Phases

### Phase 0: Coexistence (Current)

Both systems run simultaneously. vibe-vault handles session capture via its
Claude Code hook (`vv hook`). vibe-palace provides MCP tools for context
injection and semantic search over any content captured through its own
`vp_capture_session` tool.

**What works today:**
- `vp check` verifies installation, config, model, and project detection
- `vp_bootstrap_context` loads resume, tasks, sessions from `Projects/`
- `vp_search` performs semantic search over palace-captured content
- `vp_capture_session` creates new sessions in both `Projects/` (markdown)
  and `palace/` (chunked + embedded)

**What doesn't work yet:**
- Historical vibe-vault sessions are not searchable via vibe-palace
- vibe-palace has no Claude Code hook equivalent
- No CLI commands beyond check/help/version

### Phase 1: Data Import (PRD Phase 8)

Import all historical data into the palace knowledge store.

#### VibeVault Session Import (Task 8.1)

`internal/migrate/vibevault.go` — `ImportVibeVault(vaultPath string) (ImportResult, error)`

For each `Projects/*/sessions/*.md` file:

1. Parse YAML frontmatter → extract metadata (date, project, friction score,
   tool counts, summary, tags, session ID)
2. Extract transcript content (everything after the `---` frontmatter)
3. Run through the existing capture pipeline:
   - Format detection (markdown, plain text, JSON-RPC chat)
   - Sliding-window chunking (800 chars, 100 overlap, sentence-boundary aware)
   - Room classification (keyword heuristics from chunk content)
   - Hall classification (facts, events, discoveries, preferences, advice)
   - Batch embedding (all-MiniLM-L6-v2, 384 dimensions)
   - Index into `palace/{project}/drawers/`
   - Entity extraction (file paths, URLs, function names) into KG
4. Create session record in `palace/{project}/` linking to the original
   `Projects/{project}/sessions/` file
5. Skip if session ID already exists in palace (idempotent)

**Source data format** (session markdown frontmatter):
```yaml
date: 2026-04-08
type: session
project: vibe-palace
branch: main
session_id: "ae7a8247-dbac-404b-8152-679f2c65b90a"
duration_minutes: 19
messages: 95
tokens_in: 4946945
tokens_out: 21418
tool_uses: 57
friction_score: 34
tags: [implementation]
summary: "feat: restart (5+9 files, tests pass)"
```

Also import from each project's `agentctx/`:
- `iterations.md` → iteration records
- `tasks/` and `tasks/done/` → task records
- `knowledge.md` → knowledge entries (if present)

#### MemPalace ChromaDB Import (Task 8.2)

`internal/migrate/mempalace.go` — for importing from the predecessor
MemPalace system's ChromaDB persistent storage and SQLite KG database.
Re-embeds all content with ONNX to ensure consistent embeddings.

#### Import CLI (Task 8.3)

```bash
vp migrate vibevault [--vault-path PATH] [--dry-run]
vp migrate mempalace [--palace-path PATH] [--kg-path PATH] [--dry-run]
```

- Progress reporting: session count, drawer count, entity count
- `--dry-run` reports what would be imported without writing
- Individual item failures are reported but don't abort the import
- Safe to re-run (idempotent by session ID)

### Phase 2: Hook Replacement (PRD Phase 9)

Replace vibe-vault's Claude Code hook with a vibe-palace equivalent.

Currently, vibe-vault captures sessions via a Claude Code settings.json hook:
```json
{"type": "command", "command": "vv hook"}
```

vibe-palace needs an equivalent that captures session transcripts directly
into the palace knowledge store, bypassing the intermediate markdown-only
path. This is the critical handoff point — once vibe-palace has its own
hook, new sessions go directly into `palace/` with full chunking, embedding,
and KG extraction at capture time.

### Phase 3: CLI Parity (PRD Phase 9)

Add human-facing CLI commands that cover vibe-vault's functionality:
- `vp init` — initialize a project
- `vp search` — CLI semantic search
- `vp status` — palace overview
- `vp sessions` — list recent sessions
- `vp tasks` — list active tasks

### Phase 4: Retire vibe-vault

Once vibe-palace covers all of vibe-vault's functions:

1. Remove the `vv hook` from Claude Code settings.json
2. Remove `vv mcp` from any editor MCP configs
3. Replace with `vp` in all editor MCP configs (if not already done)
4. The vibe-vault config and `.vibe-vault/` index can be left in place
   (they don't interfere) or cleaned up at leisure
5. The `Projects/` directory remains — vibe-palace continues to read it
   for workflow state (sessions, tasks, iterations)

---

## Deployment Sequence (Operator Checklist)

This is the practical step-by-step for a single-machine deployment:

### Today (Phase 0)

```bash
# Build and install vibe-palace
cd ~/code/vibe-palace
make build && make test && make install

# Create config pointing to existing vault
mkdir -p ~/.config/vibe-palace
cat > ~/.config/vibe-palace/config.toml << 'EOF'
vault_path = "~/obsidian/VibeVault"
EOF

# Verify installation
vp check

# Add to editor MCP config (alongside existing vibe-vault)
# Claude Code: .mcp.json or ~/.claude.json
# Zed: ~/.config/zed/settings.json
```

### After Phase 8 is implemented

```bash
# Import all historical sessions (dry run first)
vp migrate vibevault --dry-run
vp migrate vibevault

# Verify: search should now find historical content
# (via editor MCP tool vp_search)
```

### After Phase 9 is implemented

```bash
# Replace vibe-vault hook with vibe-palace hook in Claude Code settings
# Remove vv mcp from editor configs if still present
# vibe-vault is now optional
```

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Import corrupts session data | High | Idempotent import; original files in Projects/ are read-only; palace/ data can be deleted and re-imported |
| Embedding model mismatch | Medium | vibe-palace re-embeds all content during import (doesn't reuse old vectors) |
| Large import takes too long | Low | 691 sessions × ~10 chunks each ≈ 7K embeddings. Batch embedding at 32/batch = ~220 batches. Estimated <5 minutes on CPU |
| Hook gap during transition | Low | Both systems can run simultaneously; no session data is lost |
| Projects/ format changes | Low | vibe-palace reads Projects/ but never writes to it; format is stable markdown + YAML frontmatter |

---

## Non-Goals

- **Vault directory migration**: The vault stays where it is. No files move.
- **vibe-vault data deletion**: vibe-vault's data (`Projects/`, `.vibe-vault/`)
  is never deleted. It remains as the source of truth for session markdown
  and workflow state.
- **Backwards compatibility**: vibe-palace does not need to produce output
  that vibe-vault can consume. The transition is one-directional.
- **Multi-vault support**: Both systems point to a single vault. Multi-vault
  is out of scope for the migration.
