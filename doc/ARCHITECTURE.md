# Architecture: Vibe-Palace

**Last updated:** 2026-04-09

Vibe-palace is a compiled Go binary that serves as an MCP (Model Context
Protocol) server for AI-assisted development. It provides context injection,
session capture, semantic search, and palace-based knowledge navigation through
26 MCP tools over stdio JSON-RPC 2.0.

**Design principles:**
- Single binary, zero-CGo, no external services
- Filesystem-native storage (JSONL, markdown, JSON) — all data human-readable
  and git-mergeable
- 3-tier precedence everywhere (embedded < vault < project)
- LLM-agnostic: works with any editor that speaks MCP

---

## System Components

| Package | Responsibility | Key Types |
|---------|---------------|-----------|
| `internal/storage` | Vault layout, CRUD, config | `Vault`, `Config`, `Drawer`, `SessionMeta` |
| `internal/mcp` | JSON-RPC server, tool registry | `Server`, `Registry`, `Tool` |
| `internal/context` | Precedence-aware template resolution | `Resolver`, `ResourceInfo` |
| `internal/tools` | 26 MCP tool implementations | (see tool table below) |
| `internal/embedder` | ONNX text embedding | `Embedder` interface, `ONNXEmbedder` |
| `internal/search` | Hybrid semantic + structural search | `Engine`, `VectorIndex`, `SearchResult` |
| `internal/capture` | Session ingest, chunking, friction | `Indexer`, `ChunkConfig` |
| `internal/palace` | Wing/room/hall classification, graph | `PalaceGraph`, `GraphNode`, `AAKResult` |
| `internal/project` | Project detection from working dir | `ProjectConfig` |
| `internal/kg` | Entity detection + triple extraction | `DetectedEntity`, `ExtractedTriple` |
| `internal/vplog` | Structured logging (slog to file) | `Init()`, `Close()` |

---

## Storage Engine (Phase 1)

### Vault Concept

The vault is an out-of-band directory separate from source repositories. A
single vault holds knowledge and workflow state for all projects. The vault
path is configured in `~/.config/vibe-palace/config.toml`:

```toml
vault_path = "/home/you/your-vault"
```

### Two Directory Trees

The vault contains two top-level trees with different purposes:

```
{vault}/
├── palace/                         # Knowledge (content, vectors, models)
│   ├── .local/
│   │   └── models/                 # ONNX model cache (vault-wide)
│   └── {project}/
│       ├── drawers/{wing}/{room}/drawers.jsonl
│       ├── kg/
│       │   ├── entities.jsonl
│       │   └── triples/{subj}--{pred}--{obj}.json
│       └── .local/                 # Machine-local state (embed cache)
└── Projects/                       # Workflow (sessions, tasks, config)
    └── {project}/
        ├── sessions/YYYY-MM-DD-NN.md
        └── agentctx/
            ├── config.toml         # Project-level config overrides
            └── tasks/
                ├── {slug}.md
                ├── done/{slug}.md
                └── cancelled/{slug}.md
```

**palace/** stores knowledge artifacts — content chunks in JSONL drawers,
knowledge graph entities/triples, and machine-local caches. This data grows
with captured sessions and can be rebuilt from source.

**Projects/** stores workflow artifacts — session markdown files, task
plans, and per-project configuration overrides. This is the collaboration
layer between human and AI.

### Storage Formats

- **Drawers**: JSONL (one JSON object per line) — append-only, deduped by ID
- **Sessions**: Markdown with YAML frontmatter (date, tag, friction, decisions)
- **KG Triples**: Individual JSON files keyed by `{subj}--{pred}--{obj}`
- **KG Entities**: JSONL (append-only, name + type + properties)
- **Config**: TOML at all three precedence levels

### Configuration: 3-Tier TOML Precedence

Configuration follows a 3-tier override chain:

1. **Embedded defaults** — compiled into the binary via `//go:embed config/defaults.toml`
2. **Vault-level** — `~/.config/vibe-palace/config.toml`
3. **Project-level** — `{vault}/Projects/{project}/agentctx/config.toml`

Each level overlays the previous. Key sections:

```toml
vault_path = "/path/to/vault"
http_port = 7423
log_level = "info"

[embedder]
model = "sentence-transformers/all-MiniLM-L6-v2"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100

[palace.rooms.custom]          # project-level only
keywords = ["keyword1", "keyword2"]
```

Key type: `storage.Config` — a flat struct populated by decoding TOML at each
level in sequence. `GetConfigValue(project, key)` returns value + source level.

---

## MCP Server (Phase 2)

### Protocol Layer

Vibe-palace communicates via stdio JSON-RPC 2.0, using `mark3labs/mcp-go` as
the protocol implementation. The `Server` wraps `mcp-go`'s `MCPServer` and
injects the vault reference into every request context.

```
cmd/vp/main.go
├── storage.OpenVault("")         # read config, resolve vault path
├── embedder.NewONNX(...)         # load ONNX model
├── search.NewEngine(emb, v, cfg) # create search engine
├── context.NewResolver(v.Root)   # template resolver
├── mcp.NewServer(v)              # create MCP server
├── tools.RegisterAll(...)        # register all 26 tools
└── srv.Serve(ctx)                # start stdio transport
```

### Tool Registration

`tools.RegisterAll` registers all tools with the `Registry`. Each tool
provides a JSON Schema for parameter validation:

```go
Registry.Register(Tool{
    Name:        "vp_search",
    Description: "Semantic search within a project's knowledge base.",
    Schema:      searchSchema,       // JSON Schema for params
    Handler:     searchHandler,      // func(ctx, params) → (any, error)
})
```

On dispatch, the registry validates incoming params against the compiled
schema before calling the handler. Handlers extract the vault from context
and operate on storage directly.

### 26 MCP Tools

| Tool | Source File | Category |
|------|-----------|----------|
| `vp_bootstrap_context` | context_tools.go | Context |
| `vp_get_command` | command_tools.go | Context |
| `vp_get_skill` | command_tools.go | Context |
| `vp_list_commands` | command_tools.go | Context |
| `vp_list_skills` | command_tools.go | Context |
| `vp_cmd` | cmd_tools.go | Context |
| `vp_skill` | cmd_tools.go | Context |
| `vp_palace_status` | palace_tools.go | Palace |
| `vp_list_wings` | palace_tools.go | Palace |
| `vp_list_rooms` | palace_tools.go | Palace |
| `vp_traverse` | palace_tools.go | Palace |
| `vp_find_tunnels` | palace_tools.go | Palace |
| `vp_health` | health_tools.go | Health |
| `vp_kg_query` | kg_tools.go | Knowledge Graph |
| `vp_kg_add` | kg_tools.go | Knowledge Graph |
| `vp_kg_invalidate` | kg_tools.go | Knowledge Graph |
| `vp_kg_timeline` | kg_tools.go | Knowledge Graph |
| `vp_kg_stats` | kg_tools.go | Knowledge Graph |
| `vp_search` | search_tools.go | Search |
| `vp_search_cross_project` | search_tools.go | Search |
| `vp_capture_session` | session_tools.go | Session |
| `vp_get_project_context` | session_query_tools.go | Session |
| `vp_search_sessions` | session_query_tools.go | Session |
| `vp_get_session_detail` | session_query_tools.go | Session |
| `vp_get_effectiveness` | session_query_tools.go | Session |
| `vp_get_friction_trends` | friction_tools.go | Session |

Tools 1–18 (context, palace, health, KG) are always registered. Tools 19–26
require a search engine (embedder must initialize successfully).

### HTTP Transport

An HTTP transport (`internal/mcp/transport_http.go`) is implemented but not
wired into the main binary. It provides REST endpoints:

- `GET /health` — server status
- `GET /tools` — list registered tools
- `POST /tools/{name}` — invoke a tool

CORS middleware is included for browser-based clients.

---

## Context Injection (Phase 3)

### 3-Tier Precedence Resolution

Templates and resources follow the same override pattern as configuration:

1. **Project override**: `{vault}/Projects/{project}/agentctx/{path}`
2. **Vault template**: `{vault}/Templates/{path}`
3. **Embedded default**: Compiled-in `templates/{path}`

The `Resolver` walks these levels in reverse priority order. The highest
priority match wins.

### Embedded Templates

Templates are compiled into the binary via `//go:embed templates`:

```
templates/
├── workflow.md                    # Pair programming workflow rules
├── resume.md                      # Project state template
└── commands/
    ├── vp-capture.md              # Session capture instructions
    ├── vp-restart.md              # Context restoration instructions
    ├── vp-review-plan.md          # Plan review instructions
    ├── vp-cancel-plan.md          # Plan cancellation instructions
    └── vp-wrap.md                 # Session wrap-up instructions
```

Templates support `{{PROJECT}}` and `{{DATE}}` variable expansion.

### Bootstrap Context

`vp_bootstrap_context` is the primary entry point for AI context restoration.
A single call assembles: workflow rules, project resume, active tasks, recent
sessions, KG snapshot, and available commands/skills. This replaces the
multi-file CLAUDE.md pattern used by legacy systems.

### Commands and Skills

**Commands** are instructions for the AI to execute immediately (e.g.,
"capture this session"). **Skills** are behavioral guidelines applied
throughout a session (e.g., "pair programming mode"). Both resolve via
the same 3-tier precedence system and can be listed or invoked by name.

---

## Semantic Search (Phase 4)

### Embedding Pipeline

Vibe-palace uses `all-MiniLM-L6-v2` (Sentence Transformers) via
`knights-analytics/hugot`, a pure-Go ONNX inference library.

```
internal/embedder/embedder.go    ← Embedder interface
internal/embedder/onnx.go        ← hugot ONNX implementation
internal/embedder/mock.go        ← deterministic hash vectors for testing
```

The `Embedder` interface:

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
    Close() error
}
```

Properties:
- 384-dimensional vectors, L2-normalized (cosine = dot product)
- ~90MB model, downloaded on first use, cached at `{vault}/palace/.local/models/`
- Single embed: ~66ms; batch of 32: ~290ms; 17MB binary contribution

### Vector Index: Brute-Force Cosine

```
internal/search/vector_index.go
```

Search uses brute-force nearest-neighbor with cosine distance. Every query
compares against all stored vectors and returns top-N.

- **100% recall** — guaranteed correct results
- **~1-5ms** at 10K vectors — sufficient for personal knowledge management
- **Thread-safe** — `sync.RWMutex` for concurrent reads

The PRD specified HNSW (Hierarchical Navigable Small World), but `coder/hnsw`
had critical recall bugs (Heap.Max/PopLast returning wrong elements, 0–2/10
recall). A hardened fork is in progress; brute-force is the correct choice at
current scale.

### Hybrid Search Engine

```
internal/search/engine.go
```

The search pipeline combines vector similarity with structural metadata:

```
1. Embed query text → query vector
2. Vector search → top limit×3 candidates (over-fetch for filtering)
3. Metadata filter → remove non-matching wing/room/hall/date
4. Score conversion → 1 / (1 + cosine_distance)
5. Structural boost → multiply score for matching filters
6. Deduplicate → keep highest-scored per SourceRef
7. Return top-N
```

Structural boosts (configurable):

| Filter | Default | Effect |
|--------|---------|--------|
| Wing match | +12% | Results in searched wing score higher |
| Hall match | +24% | Results in searched hall score higher |
| Room match | +34% | Results in searched room score higher |

Boosts stack multiplicatively. A result matching all three gets
`score × 1.12 × 1.24 × 1.34 ≈ score × 1.86`.

### Embed Cache

```
internal/search/cache.go
```

Pre-computed vectors are cached on disk at
`palace/{project}/.local/embed-cache/{drawer_id}.vec` (raw little-endian
float32). The cache avoids redundant ONNX inference during `Rebuild()`.
Deleting the cache forces re-embedding but loses no data.

---

## Session Capture (Phase 5)

### Capture Flow

When `vp_capture_session` is called:

```
1. Write session markdown to {vault}/Projects/{project}/sessions/
   - YAML frontmatter: date, tag, friction, decisions, files_changed
   - Auto-increments iteration number per day
2. If transcript provided:
   a. Detect format (plain text, markdown transcript, JSON-RPC chat)
   b. Chunk transcript (sliding window, sentence-boundary aware)
   c. Classify each chunk: wing (project), room (keyword), hall (memory type)
   d. Batch embed all chunks (one EmbedBatch call)
   e. Store each chunk as a Drawer in JSONL
   f. Index each chunk+vector in the search engine
   g. Extract entities (file paths, URLs) → knowledge graph
3. Compute friction score (0–100) from transcript
```

### Chunking Engine

```
internal/capture/chunker.go
internal/capture/detector.go
```

Format detection inspects the first 500 bytes:
- **JSON-RPC**: starts with `[` or `{` and contains `"role"` — split on exchange boundaries
- **Markdown**: contains turn markers (`## Human`, `## Assistant`) — split on turns
- **Plain text**: default — split on paragraph boundaries

Chunks are built with a sliding window:
- Target size: 800 chars (configurable)
- Overlap: 100 chars between consecutive chunks
- Never splits mid-sentence
- Keeps exchange pairs together when possible
- Overlap snapped to word boundaries

Defaults tuned for all-MiniLM-L6-v2's 256-token input limit (~200 words ≈ 800 chars).

### Friction Analysis

```
internal/capture/friction.go
```

Friction score (0–100) measures session difficulty via four signals:

| Signal | Weight | Keywords |
|--------|--------|----------|
| Corrections | ×8 (max 25) | wrong, undo, revert, actually, mistake |
| Retries | ×10 (max 25) | Same tool called ≥3 times |
| Error density | ×5 (max 25) | error, failed, exception per 1000 tokens |
| Rework | ×10 (max 25) | go back, start over, scratch that |

`GetFrictionTrends` aggregates weekly averages and maximums, grouped by
ISO week (Monday start).

### Session Query Tools

- `vp_search_sessions` — filter by date range, friction score, tag, text query
- `vp_get_session_detail` — full session markdown + metadata
- `vp_get_effectiveness` — compares friction for sessions with/without rich context
- `vp_get_friction_trends` — weekly friction averages and maximums

---

## Palace Architecture (Phase 6)

### Memory Palace Metaphor

Content is organized using a spatial metaphor:

- **Wing** — project-level dimension (defaults to project slug)
- **Room** — functional area (testing, api, devops, debugging, etc.)
- **Hall** — memory type (facts, decisions, discoveries, preferences, advice, events)
- **Drawer** — individual content chunk (the atomic storage unit)

### Classification

```
internal/palace/metadata.go
```

**Wing detection**: Returns project slug if available, else first path component,
else "unknown".

**Room detection** (4-tier cascade, first match wins):
1. Custom keywords from `[palace.rooms]` config
2. Filename pattern matching (test files, configs, CI/CD, etc.)
3. Built-in content keywords (10 room types: testing, devops, api, data,
   config, debugging, refactoring, architecture, performance, security)
4. Fallback: "general"

**Hall detection**: Classifies content into memory type by keyword presence
(decided → decisions, found → discoveries, prefer → preferences, should →
advice, happened → events, default → facts).

### AAAK Compression

```
internal/palace/aaak.go
```

AAAK (Autonomous Adaptive Associative Knowledge) is a lossy compression
format for token-efficient context loading:

```
ZID:ENTITIES|topics|"key_quote"|WEIGHT|EMOTIONS|FLAGS
```

Components: entity detection (capitalized multi-word → 3-letter codes),
topic extraction (top-3 frequent non-stopwords), key quote (longest
entity-relevant sentence), weight (density score), emotion/flag detection.

### Graph Traversal

```
internal/palace/graph.go
```

`BuildGraph` constructs an in-memory graph from vault drawers:
- **Nodes**: each unique wing/room combination
- **Intra-wing edges**: all rooms in the same wing are adjacent
- **Cross-wing edges** (tunnels): same room slug across different wings

`Traverse(startKey, maxHops)` performs BFS from a starting room,
returning reachable nodes with hop distances. `FindTunnels()` returns
rooms appearing in 2+ wings — these are cross-domain connections.

### Palace Tools

- `vp_palace_status` — overview: wing/room/drawer counts, tunnel count
- `vp_list_wings` — all wings with room and drawer counts
- `vp_list_rooms` — rooms in a wing with drawer counts and hall distribution
- `vp_traverse` — BFS graph walk from a starting room
- `vp_find_tunnels` — cross-wing room connections

---

## Data Flow Diagram

```
                     ┌─────────────┐
                     │  MCP Client  │
                     │ (AI / human) │
                     └──────┬───────┘
                            │ JSON-RPC (stdio)
                     ┌──────▼───────┐
                     │  MCP Server   │
                     │  (mcp pkg)    │
                     └──────┬───────┘
                            │ tool dispatch
              ┌─────────────┼─────────────┐
              │             │             │
       ┌──────▼──────┐ ┌───▼────┐ ┌──────▼──────┐
       │ vp_capture   │ │vp_search│ │vp_bootstrap │
       │ _session     │ │        │ │ _context     │
       └──────┬───────┘ └───┬────┘ └──────┬──────┘
              │             │             │
       ┌──────▼──────┐ ┌───▼────────┐ ┌──▼───────┐
       │   Indexer    │ │  Search    │ │ Context  │
       │  (capture)   │ │  Engine    │ │ Resolver │
       └──┬───┬───┬──┘ └───┬────────┘ └──────────┘
          │   │   │        │
   chunk  │   │   │ embed  │ vector search
          │   │   │        │
       ┌──▼┐ │ ┌─▼──────┐ │
       │   │ │ │Embedder │◄┘
       │ D │ │ │ (ONNX)  │
       │ e │ │ └─────────┘
       │ t │ │
       │ e │ │ store
       │ c │ │
       │ t │ ┌▼──────────┐
       │   │ │  Storage   │
       └───┘ │  (vault)   │
             │  drawers   │
             │  sessions  │
             │  KG        │
             └────────────┘
```

---

## Structured Logging (Phase 7)

### Two Logging Domains

The system distinguishes between **startup crashes** (stderr + exit) and
**runtime degradation** (slog + continue). Five `fmt.Fprintf(os.Stderr, ...)`
calls in `main.go` handle fatal startup errors before the logger is even
initialized. The structured logger handles runtime best-effort failures.

### Log Infrastructure (`internal/vplog`)

`vplog.Init(path, level)` opens a JSON log file with `O_APPEND` for POSIX
atomic writes (safe for concurrent vp instances without advisory locking).
Entries are typically 200–500 bytes, well under PIPE_BUF (4096 bytes).

- **Location:** `{vault}/palace/.local/vp.log`
- **Format:** JSON via `slog.NewJSONHandler`, leveled (DEBUG/INFO/WARN/ERROR)
- **Rotation:** Rename to `.log.1` at 1MB (single backup)
- **Fallback:** Discard handler on init failure (never blocks startup)
- **Level:** Configured via `log_level` in config.toml (default: "info")

After init, all packages use `slog.Info/Warn/Error` directly (no vplog import
needed). Expected "already exists" entity duplicates log at DEBUG to avoid
flooding WARN during re-index (~1000 duplicates per startup).

### Health Tool (`vp_health`)

Reads the last 24 hours of WARN/ERROR entries from `vp.log`, groups by
message prefix, and returns a structured summary. Does NOT duplicate
`vp check` (which validates installation and config). The health tool
answers "what failed at runtime?" while `vp check` answers "is the system
installed correctly?"

---

## Knowledge Graph (Phase 7)

### Existing Storage Layer

The storage layer (`internal/storage/knowledge_graph.go`) provides full KG
CRUD: `AddEntity`, `GetEntity`, `ListEntities`, `AddTriple`, `QueryEntity`,
`InvalidateTriple`, `Timeline`, `KGStats`. Entities are stored in JSONL,
triples as individual JSON files keyed by `{subj}--{pred}--{obj}`.

### Entity Detection (`internal/kg/entity_detector.go`)

`DetectEntities(text)` finds person, project, tool, and concept entities
using compiled regex patterns:

- **Person:** Verb patterns (`Name said/thinks/mentioned`), capitalized
  multi-word names. Confidence stacking (0.3 base + 0.4 verb signal).
- **Tool:** Curated known-tool list (40+ entries) + `using/with/via X`.
- **Project:** Hyphenated lowercase names near project-context words,
  version references (`X v1.2`).
- **Concept:** Capitalized terms near conceptual signals (architecture,
  pattern, strategy).

Dedup by normalized name, false positive rejection via 100+ common word list.

### Triple Extraction (`internal/kg/extractor.go`)

`ExtractTriples(text, entities, validFrom)` matches 6 relationship patterns:

| Pattern | Predicate | Confidence |
|---------|-----------|------------|
| `X works on Y` | `works_on` | 0.7 |
| `X decided to use Y` | `decided` | 0.8 (+ DECISION flag) |
| `X started/joined Y` | `member_of` | 0.7 (+ ValidFrom) |
| `X depends on Y` | `depends_on` | 0.6 |
| `X created/built Y` | `created` | 0.7 |
| `X uses Y` | `uses` | 0.6 |

Subjects/objects are filtered against detected entities to reduce noise.
Dedup by (subject, predicate, object), keep highest confidence.

### Capture Pipeline Integration

`IndexTranscript` runs entity detection and triple extraction after chunking.
Detected entities get `mentioned_in` triples linking them to the source
session. All KG operations are best-effort with slog logging.

### 5 KG MCP Tools

| Tool | Purpose |
|------|---------|
| `vp_kg_query` | Query facts about an entity (direction, temporal filter) |
| `vp_kg_add` | Add a fact, auto-creating entities if needed |
| `vp_kg_invalidate` | Set end date on a fact (temporal invalidation) |
| `vp_kg_timeline` | Chronological fact history for an entity |
| `vp_kg_stats` | Entity/triple counts, predicate types, type breakdown |

All registered unconditionally (no embedder needed).

---

## Build System

```
make build      # go build ./...
make test       # fast unit tests (-race -short, no model download)
make test-full  # full suite including ONNX integration
make integration # integration tests only
make install    # build + install to ~/.local/bin/vp
make cover      # HTML coverage report (short mode)
make clean      # remove build artifacts (preserves model cache)
make dist-clean # remove everything including model cache
```

Binary: `vp` installed to `${PREFIX}/bin/vp` (default `~/.local/bin/vp`).

---

## Roadmap

Phases 7–10 are complete (knowledge graph, migration, CLI, documentation).

| Phase | Goal |
|-------|------|
| 11. Pluggable Embedding Backends | Swap ONNX embedder for API-based alternatives |
