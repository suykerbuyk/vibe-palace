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
│       ├── tasks/                   # task plans
│       ├── iterations.md           # iteration records
│       ├── resume.md               # project state
│       └── config.toml             # project config overrides
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
- vibe-palace Phases 1–10, 12–18 complete (storage, MCP, context, search, capture, palace, KG, migration, CLI, docs, adaptive room classification, template reconciliation, transcript archive, auto-capture hooks, Zed adapter)
- vibe-palace serves 57 MCP tools via stdio JSON-RPC (count as of that date;
  the live surface is `internal/mcp/tool_surface.golden.json`)
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
  (import via `vp migrate vibevault` is implemented but not yet run)
- vibe-palace has no Claude Code hook equivalent

### Phase 1: Data Import (PRD Phase 8)

Import all historical data into the palace knowledge store.

#### VibeVault Session Import (Task 8.1)

`internal/migrate/vibevault.go` — `ImportVibeVault(ctx, source, destination *storage.Vault, opts) (ImportResult, error)` — reads sessions from `source` and writes to `destination` (see "Source vs. destination" below).

The importer writes **transcript archives**, never tracked drawers or
knowledge-graph files, and it loads no embedding model. For each project:

1. Every `Projects/*/sessions/*.md` note is read: its YAML frontmatter
   gives the session id and date, and the text after the frontmatter is
   the transcript.
2. Each session not yet archived becomes one inline-adapter archive under
   the project, with its manifest. A session whose text is empty is
   recorded as empty and gets no archive.
3. The project's `knowledge.md`, when present and non-empty, is archived
   once as session `knowledge-<slug>`.

**Archive dates.** An archive is dated by its source, never by the clock:

- A session is dated by its frontmatter `date:` at **12:00 UTC**. Its
  `captured_at` is then that UTC day on every host. The archive's filename
  day is taken in the host's local zone, so it is the same date on every
  host whose zone is within **±11 h of UTC**. A host further out (UTC+12,
  UTC+13, UTC+14) names the file one day later; the `captured_at` and the
  content are the same.
- `knowledge.md` is dated by its own frontmatter `date:` at 12:00 UTC.
  Without one it takes a fixed epoch, **2000-01-01 12:00 UTC**, so every
  host computes the same archive.
- A session id of the form `mempalace:<sha256>:<n>` is refused: that is a
  MemPalace import batch id (below), and a session must never carry one.

**Not indexed yet.** The imported archives are not indexed on this host
until a later release indexes archives; there is nothing to run now. The
session notes themselves stay searchable as notes. On a host that already
has an index ledger for the project, the import first adds the source
hashes of the archives it brings in to that host's baseline set, so they
are historical backlog there rather than pending work. Only archives with
no `(session_id, source_sha256)` manifest in the vault yet are added, so a
second host that pulls the archives adds nothing.

**Idempotency marker.** Each project's import marker lives at
`palace/.local/imports/<slug>/imported-sessions.jsonl` in the destination
vault. That directory is git-ignored and host-local. The marker is only a
**hint**: a session counts as imported only while the vault also holds a
manifest for it. A marker left behind by a deleted, recreated, renamed or
departed project therefore never suppresses an import. Archiving is
idempotent anyway: a session whose archive is already there is skipped.
Deleting or splitting a project removes its marker. A marker in the old
location, `palace/<slug>/.local/imported-sessions.jsonl`, is copied into the
new file, without duplicating lines already there. It is then deleted, and
its emptied `palace/<slug>/.local/` directory removed, **unless git tracks
anything under `palace/<slug>/.local/`**. That directory is not ignored, so
a vault may have committed the old marker. A tracked file is left in place
so the vault shows no deletion; removing it from the repository is the
operator's call.

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

With `--agentctx`, each project's agentctx tree (resume, iterations,
workflow, knowledge, tasks, memory) is also copied, if absent.

#### MemPalace ChromaDB Import (Task 8.2)

`internal/migrate/mempalace_store.go` — `ImportMemPalace(ctx, vault, project, writers, emb, export, opts)` — imports a JSON export produced by the MemPalace tool into one **existing** project, named with `--project`. A slug that is in neither `palace/` nor `Projects/` is refused.

An export has no transcripts, so it has no archive. Its drawers, entities
and triples go **only to this host's local index store**, as ledgered
**import batches**:

- A batch holds up to 1000 drawers, or 1000 entity and triple records.
  Entities and triples share batches.
- The batch ids are `mempalace:<sha256 of the export file>:<n>`.
- Blank drawers are skipped.
- Every drawer is embedded again with this host's model, for consistent
  embeddings.
- Every batch carries one start day: the UTC day of the export's earliest
  drawer `filed_at`, or 2000-01-01 when no drawer has one.

Each batch is committed in its own step under the project's index commit
lock. A re-run skips every batch the ledger already records, without
embedding it again; an interrupted import finishes on its next run.

The import refuses, and writes nothing more, when:

- the project's index records another chunk recipe
  (`chunks.fingerprint`);
- this host's embed cache holds vectors from another embedding model.

Both conditions end with the next full index rebuild.

**Caveats** (the command prints these):

- **Single-host and not tracked.** The import lives only in this host's
  local index, under the git-ignored `palace/.local/`. No other host
  receives it.
- **A full rebuild discards it.** If the project's chunk recipe changes,
  the next full index rebuild discards the import, because there is no
  archive to rebuild it from.
- **Keep the export file.** Re-running the import from the export file is
  the only way to restore a discarded import.
- **A changed export.** Any change to the export file changes its hash, so
  the next import adds it as new batches and embeds them again. The
  batches of the old export stay live alongside them until a full index
  rebuild.
- **Historical backlog.** When the import creates the project's index
  ledger on this host, the project's existing tracked archives are
  recorded as historical backlog. A later full index rebuild indexes them.

#### Import CLI (Task 8.3)

```bash
vp migrate vibevault [--vault-path PATH] [--dry-run] [--yes] [--slug-map OLD=NEW,...] [--strict]
vp migrate mempalace --export-path PATH --project SLUG [--dry-run]
```

- Progress reporting: session and archive counts for vibevault; batch,
  drawer, entity and triple counts for MemPalace
- `vp migrate vibevault` never loads the embedding model, dry run or real.
- `--dry-run` reports what would be imported without writing. It
  prompts for slug-collision resolution just like a real run; use
  `--yes` to auto-accept default rename suggestions. A dry run **loads no
  embedding model**, so previewing an import never pays for the ~90 MB model
  download.
  - Inputs are validated before any model loads: the MemPalace export is read
    and parsed, and the vibevault source's `Projects/` directory is checked
    (before the cross-vault confirmation prompt, too). A missing, directory, or
    malformed export, and a source with no `Projects/` directory, exit **1**.
    Other I/O failures (a permission error, say) exit 2.
  - A real MemPalace import loads the model only when some drawer has non-blank
    content; an export of entities and triples alone needs none.
  - What a dry run does **not** prove: a MemPalace dry run no longer runs the
    drawers' text through the embedder, so it cannot show that every drawer
    embeds cleanly.
  - MemPalace dry-run counts ignore the ledger: over a project that already
    holds the export, the dry run over-reports what a real run would commit.
    A vibevault dry run does honour the import markers, so its session counts
    are exact.
- Individual item failures are reported but don't abort the import
- Safe to re-run: vibevault is idempotent by session and source hash, and
  MemPalace by batch id

#### Source vs. destination (`--vault-path`)

`--vault-path` names the **source** vault that sessions are *read* from —
its `Projects/<name>/sessions/*.md` files. It is **not** a write target.
The **destination** is always the configured `vault_path` from
`~/.config/vibe-palace/config.toml`. Everything the migration writes —
session archives, per-project import markers
(`palace/.local/imports/*/imported-sessions.jsonl`), and the per-project `Projects/<slug>/{commands,skills}/`
init scaffold — lands in the destination, never in the source. (Before
v7.2.0 it also wrote a `Projects/<slug>/config.toml`; that per-project vault
config is retired and nothing writes it now.)

When `--vault-path` is omitted, source and destination are the same
configured vault — the original single-vault, in-place behavior.

##### Cross-vault import

To import from a different vault than the one you write to, point
`--vault-path` at the source:

```bash
vp migrate vibevault --vault-path /home/johns/obsidian/VibeVault --yes
```

Every run (real or dry) prints a banner to stderr before scanning:

```
Source:      /home/johns/obsidian/VibeVault
Destination: /home/johns/obsidian/vibe-palace-vault
Same vault:  no
```

A **real** (non-dry-run) run where source ≠ destination requires
confirmation: pass `--yes` to proceed non-interactively, or answer the
interactive `[y/N]` prompt. With no TTY and no `--yes`, the command
aborts before loading the embedder.

**Orphan-marker note.** If the source vault holds import markers from a
prior run but the destination has none, the command prints a non-blocking
NOTE. Markers are read from the destination only, so those sessions are
checked against their archives again. An archive already there is
skipped, so nothing is duplicated and nothing needs copying.

#### Slug collision resolution

When two `Projects/*` directories slugify to the same value (e.g. a
directory renamed during an earlier workflow), `vp migrate vibevault`
resolves the collision without losing data:

- The first directory (by `os.ReadDir` sorted name order) keeps the
  original slug.
- Later colliders are renamed to `{slug}-vp`, escalating to
  `{slug}-vp2`, `{slug}-vp3`, … past any already-taken or already-on-disk
  slugs from prior migrations.
- **Interactive (default with a TTY):** the command proposes the
  default rename and accepts Enter to confirm, a custom slug to
  override, or Ctrl-D / EOF to abort cleanly (no writes).
- **`--yes`:** silently accept every default rename.
- **`--slug-map OLD=NEW[,OLD=NEW…]`:** pre-specify renames; any
  collisions not covered by the map fall back to interactive or
  auto mode.
- The final remap is printed in the summary (dry-run and real).

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

# The sessions are archived; a later release indexes the archives
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
