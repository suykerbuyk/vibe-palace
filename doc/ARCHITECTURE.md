# Architecture: Semantic Search

**Last updated:** 2026-04-08

This document describes the semantic search architecture in vibe-palace —
how text is converted to vectors, stored, indexed, and retrieved. Future
revisions will expand to cover the full system architecture including vault
layout, palace directory structures, session capture, and knowledge graphs.

---

## Overview

Vibe-palace implements semantic search as a pipeline:

```
Text → Embed → Vector → Index → Search
```

Unlike keyword search, semantic search understands meaning. The query
"how do goroutines work" finds content about "lightweight concurrency
with channels" even though no words overlap. This is possible because
both texts produce nearby vectors in embedding space.

---

## Embedding Pipeline

### The Model: all-MiniLM-L6-v2

Vibe-palace uses `all-MiniLM-L6-v2` from Sentence Transformers, a
compact text embedding model trained on over 1 billion sentence pairs.
It produces 384-dimensional vectors from input text up to 256 tokens
(~200 words).

Key properties:
- **384 dimensions** — each text becomes a float32[384] vector
- **L2-normalized** — all vectors have unit length, so cosine similarity
  reduces to a dot product
- **Semantic** — texts with similar meaning produce geometrically close
  vectors (high cosine similarity)
- **Model size** — ~90MB on disk (ONNX format)

### The Runtime: hugot (Pure-Go ONNX)

The model runs via `knights-analytics/hugot`, a pure-Go ONNX inference
library. This preserves vibe-palace's single-binary, zero-CGo architecture.

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

`EmbedBatch` processes multiple texts in a single call, chunking them
into configurable batch sizes (default 32) for memory efficiency. The
ingest pipeline uses `EmbedBatch` to embed all chunks from a transcript
in one call rather than N individual `Embed` calls.

### Performance Characteristics

From the hugot spike (see `doc/spike-hugot-pure-go-embedding.md`):

| Metric | Measured |
|--------|----------|
| Single embed latency | 66ms |
| Batch 32 throughput | 290ms |
| Peak memory (1024 embeds) | 306 MB heap |
| Binary size (stripped) | 17 MB |

---

## Vector Index

### Brute-Force Cosine Search

```
internal/search/vector_index.go
```

Vibe-palace uses brute-force nearest-neighbor search with cosine distance.
Every search compares the query vector against all stored vectors and
returns the top-N closest.

Properties:
- **100% recall** — guaranteed to find the true nearest neighbors
- **~1-5ms latency** at 10K vectors — fast enough for the personal
  knowledge management use case
- **Thread-safe** — `sync.RWMutex` allows concurrent reads with
  exclusive writes

The PRD originally specified HNSW (Hierarchical Navigable Small World)
for approximate nearest-neighbor search. During Phase 4 implementation,
`coder/hnsw` was found to have a critical recall bug: `replenish()` in
`graph.go` hardcodes `CosineDistance` regardless of the configured
distance function, corrupting graph topology. Measured recall was 0-2/10
across all parameter combinations. HNSW is deferred to the roadmap
pending a reliable pure-Go implementation.

Brute-force is the correct choice at current scale. At 10K vectors with
384 dimensions, the search computation is:
- 10K dot products × 384 multiplications = ~3.8M FLOPs
- Modern CPUs handle this in <5ms with SIMD

### Index Operations

```go
// Build creates the index from a batch of vectors (used by Rebuild).
func (idx *VectorIndex) Build(vectors [][]float32, ids []string) error

// Insert adds or replaces a single vector (used by incremental indexing).
func (idx *VectorIndex) Insert(id string, vec []float32) error

// Search returns the top-k nearest vectors to the query.
func (idx *VectorIndex) Search(query []float32, k int) ([]VectorResult, error)

// Delete removes a vector by ID.
func (idx *VectorIndex) Delete(id string)
```

---

## Search Engine

### Hybrid Semantic + Structural Search

```
internal/search/engine.go
```

The search engine combines vector similarity with structural metadata
to produce ranked results. The pipeline:

```
1. Embed query text → query vector
2. Vector search → top limit*3 candidates (over-fetch for filtering)
3. Metadata filter → remove non-matching wing/room/hall/date
4. Score conversion → 1 / (1 + cosine_distance)
5. Structural boost → multiply score for matching filters
6. Deduplicate → keep highest-scored per SourceRef
7. Return top-N
```

### Structural Boosts

When a search includes filters, matching results get a score multiplier:

| Filter | Default Boost | Effect |
|--------|--------------|--------|
| Wing match | +12% | Results in the searched wing score higher |
| Hall match | +24% | Results in the searched hall score higher |
| Room match | +34% | Results in the searched room score higher |

Boosts are configurable via the `[search]` TOML config section and stack
multiplicatively. A result matching all three filters gets:
`score × 1.12 × 1.24 × 1.34 = score × 1.85`

This means structural metadata isn't just a filter — it's a ranking signal.
A result that is semantically relevant AND in the right structural location
ranks significantly higher than one that is only semantically relevant.

### Deduplication

When content from the same source (e.g., a session transcript) is chunked
into multiple drawers, the search engine deduplicates by `SourceRef`.
Only the highest-scored chunk from each source is returned. This prevents
a single verbose session from dominating search results.

---

## Embed Cache

```
internal/search/cache.go
```

The embed cache stores pre-computed vectors on disk to avoid redundant
ONNX inference:

```
palace/{project}/.local/embed-cache/
  {drawer_id}.vec     ← raw little-endian float32 (1536 bytes for 384 dims)
```

The cache is checked during `Engine.Rebuild()`: for each drawer, if a
cached vector exists, it's used directly; otherwise the embedder is called
and the result is cached. `IndexDrawer` (incremental) always embeds and
caches. `IndexDrawerWithVec` (batch pipeline) caches the pre-computed
vector without calling the embedder.

The cache is a pure performance optimization. It is never authoritative —
deleting it forces re-embedding on next rebuild but doesn't lose data.

---

## Ingest Pipeline (Capture → Search)

### How Content Gets Indexed

```
internal/capture/indexer.go
internal/capture/chunker.go
internal/capture/detector.go
```

When a session is captured with a transcript, the ingest pipeline:

```
1. Detect format (plain text, markdown transcript, JSON-RPC chat)
2. Split into segments (paragraphs, turn boundaries, or role markers)
3. Build chunks (sliding window, 800 chars, 100 overlap)
   - Never split mid-sentence
   - Keep exchange pairs together when possible
   - Overlap snapped to word boundaries
4. Classify each chunk:
   - Wing ← project slug (default)
   - Room ← keyword heuristics (testing, api, devops, debugging, ...)
   - Hall ← memory type heuristics (facts, decisions, discoveries, ...)
5. Store each chunk as a Drawer in vault JSONL
6. Batch embed all chunks via EmbedBatch (one call, not N calls)
7. Index each chunk+vector via IndexDrawerWithVec
8. Extract entities (file paths, URLs) → write to knowledge graph
```

### Chunk Configuration

Chunk size and overlap are configurable via TOML:

```toml
[chunker]
max_chars = 800    # target chunk size
overlap = 100      # overlap between consecutive chunks
```

Defaults are tuned for the `all-MiniLM-L6-v2` model's 256-token input
limit. At ~4 characters per token, 800 characters ≈ 200 tokens, leaving
headroom for tokenization variance.

---

## Data Flow Diagram

```
                     ┌─────────────┐
                     │  MCP Client  │
                     │ (AI / human) │
                     └──────┬───────┘
                            │ JSON-RPC
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

## Future Expansion

This document currently covers semantic search only. Planned additions:

- **Vault layout**: directory conventions, palace hierarchy (project →
  wing → room), JSONL storage format, session file structure
- **Knowledge graph**: entity detection, triple storage, temporal validity,
  query patterns
- **Context injection**: 3-tier precedence, template expansion, bootstrap
  assembly
- **Configuration**: 3-tier TOML precedence (embedded → vault → project),
  all configurable parameters
- **MCP protocol**: tool registry, JSON-RPC dispatch, HTTP transport
