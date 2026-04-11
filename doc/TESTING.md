# Testing Strategy

**Last updated:** 2026-04-10

This document describes the testing strategy for vibe-palace, including
the unit test infrastructure, the integration test architecture, and the
ONNX model caching system that makes real-embedding tests practical.

---

## Test Tiers

vibe-palace uses three tiers of tests, each with different tradeoffs
between speed and fidelity.

### Tier 1: Unit Tests (short mode)

**Command:** `make test`
**Flags:** `go test -race -short -cover ./...`
**Duration:** ~2 seconds
**ONNX required:** No

Unit tests exercise individual functions and types within a single package.
They use `embedder.MockEmbedder` — a deterministic embedder that produces
L2-normalized vectors derived from SHA-256 hashes of the input text. These
vectors have no semantic meaning (similar texts do NOT produce similar
vectors), but they are stable and reproducible, which is sufficient for
testing index mechanics, storage round-trips, and tool handler logic.

Every package maintains 80%+ unit test coverage. Tests that require ONNX
embeddings call `t.Skip()` in short mode.

### Tier 2: Integration Tests (ONNX)

**Command:** `make integration`
**Flags:** `go test -count=1 -run TestIntegration -v ./...`
**Duration:** ~40 seconds (warm cache), ~70 seconds (cold cache)
**ONNX required:** Yes

Integration tests exercise cross-layer interactions with real ONNX
embeddings. They verify that semantic search actually returns semantically
relevant results, that the full capture-to-search pipeline works, and that
config values propagate correctly across layers.

All integration test function names start with `TestIntegration` so
`make integration` discovers them via the `-run` flag.

### Tier 3: Full Suite

**Command:** `make test-full`
**Flags:** `go test -race -cover ./...`
**Duration:** ~70 seconds (warm cache)
**ONNX required:** Yes

Runs everything: unit tests with race detection plus all integration tests.
This is the gate for commits.

---

## ONNX Model and the Cache System

### What ONNX does

ONNX (Open Neural Network Exchange) is the model format used to run
`all-MiniLM-L6-v2`, a text embedding model from Sentence Transformers.
This model converts arbitrary text into 384-dimensional float32 vectors
that capture semantic meaning — texts about similar topics produce vectors
that are geometrically close (high cosine similarity).

The runtime chain:

```
Text input
  → hugot tokenizer (pure-Go, HuggingFace-compatible)
  → ONNX inference (pure-Go, knights-analytics/hugot)
  → L2-normalized 384-dim vector
```

**hugot** (`knights-analytics/hugot v0.7.0`) is a pure-Go library that
loads and executes ONNX models. No Python, no CGo, no external processes.
This keeps the single-binary, zero-dependency deployment model intact.

### Model cache: `.cache/models/`

The ONNX model file (`onnx/model.onnx`, ~90MB) must be downloaded from
HuggingFace on first use. To avoid re-downloading on every test run, the
model is cached in a project-local directory:

```
.cache/models/           ← project-local, gitignored
  sentence-transformers/
    all-MiniLM-L6-v2/
      onnx/model.onnx    ← the neural network weights (~90MB)
      tokenizer.json     ← vocabulary and tokenization rules
      ...
```

**Cold cache** (first run): hugot downloads the model from HuggingFace.
This adds 10-30 seconds depending on network speed. There is a known
upstream data race in `go-huggingface/hub` during concurrent file downloads
that triggers under `go test -race` on cold cache only. This is not
actionable and does not affect inference.

**Warm cache** (subsequent runs): hugot loads the model directly from
disk. No network access. This is the normal path in development.

Cache lifecycle:
- `make clean` — preserves model cache (only removes build artifacts)
- `make dist-clean` — deletes `.cache/` entirely (forces re-download)
- `git clean -fxd` — also deletes the cache (it's gitignored)

### Embed cache: `palace/{project}/.local/embed-cache/`

Separate from the model cache, the **embed cache** stores pre-computed
embedding vectors for drawer content that has already been embedded. This
is a second-level cache used by `search.Engine.Rebuild()`:

```
palace/my-project/.local/embed-cache/
  a1b2c3d4.vec    ← raw little-endian float32 (1536 bytes for 384 dims)
  e5f6g7h8.vec
  ...
```

When rebuilding a project's search index, the engine checks the embed
cache before calling the embedder. On a hit, it skips inference entirely.
This makes repeated rebuilds fast even for large projects.

The embed cache is a pure performance optimization — it is never
authoritative. Deleting it simply forces re-embedding on next rebuild.

### Test helper: `projectCacheDir(t)`

Each package that runs ONNX integration tests includes a
`testcache_test.go` file with a `projectCacheDir(t)` function that walks
up from the test's working directory to find the project root (`go.mod`),
then returns `.cache/models/` under that root. This ensures all packages
share the same model cache regardless of where `go test` runs from.

Currently present in: `internal/embedder/`, `internal/search/`,
`internal/capture/`, `internal/integration/`.

---

## Integration Test Inventory

### `internal/integration/` — Cross-Layer Tests

These tests exercise interactions between multiple packages. They use a
shared `testHarness` type that bundles vault, engine, embedder, MCP server,
context resolver, and config into a single test fixture.

| Test | Layers | ONNX? | What it proves |
|------|--------|-------|----------------|
| `StorageToSearch` | storage → search | Yes | Drawers written via storage become searchable after rebuild; semantic ranking, wing/hall/date filters all work |
| `StorageSearchMetadataPreservation` | storage → search | Yes | All drawer metadata fields (wing, room, hall, source_type, source_ref, date) survive the full round-trip |
| `ConfigBoostValues` | config → search | Yes | Non-zero boost values produce higher scores for matching filters; zero boosts produce equal scores |
| `ConfigChunkSize` | config → capture | No | ChunkMaxChars config actually controls how many chunks a transcript produces |
| `ConfigSearchLimit` | config → search | No | SearchDefaultLimit config constrains the number of results returned |
| `KGEntityRoundTrip` | capture → storage (KG) | No | Entities extracted from a transcript are written to the KG with correct triples and queryable |
| `KGEntityDeduplication` | capture → storage (KG) | No | Re-indexing the same transcript doesn't create duplicate entities |
| `SessionCaptureToSearch` | tools → capture → search | Yes | `vp_capture_session` with transcript → chunks indexed → searchable with correct metadata |
| `SessionCaptureWithoutTranscript` | tools → storage | No | Capture without transcript writes session file but creates no drawers |
| `SessionIterationAcrossSessions` | tools → storage | No | Multiple captures auto-increment iteration numbers |
| `MCPSearchEndToEnd` | MCP → tools → search | Yes | JSON-RPC `tools/call` for `vp_search` returns semantically correct results |
| `MCPSearchValidation` | MCP → tools | No | Invalid parameters produce proper JSON-RPC error responses |
| `BootstrapFullContext` | tools → context → storage | No | Bootstrap tool assembles workflow, commands from embedded + vault sources |
| `BootstrapWithSessions` | storage | No | Sessions written via API are readable through list/read operations |
| `FrictionScoringOnCapture` | tools → capture → storage | No | `vp_capture_session` computes and persists friction score; high-friction transcript scores >= 50, smooth < 20 |
| `FrictionScoringNoTranscript` | tools → storage | No | Session without transcript gets friction_score = 0 |
| `FrictionTrendsEndToEnd` | tools → capture → storage | No | `vp_get_friction_trends` returns correctly aggregated weekly metrics from stored sessions |
| `FrictionTrendsEmpty` | tools → capture → storage | No | Trends for project with no sessions returns empty result |
| `FrictionSearchByMinScore` | storage | No | `SearchSessions` with minFriction filter returns only sessions above threshold |

### `internal/integration/` — Phase 12 Tests (Room Classification)

| Test | Layers | ONNX? | What it proves |
|------|--------|-------|----------------|
| `WeightedRoomScoring` | palace → storage | No | Weighted keyword scoring ranks rooms correctly; high/medium/low tiers produce expected scores |
| `ScoringOverrides` | config → palace → storage | No | `[palace.scoring]` config overrides merge with defaults and change classification outcomes |
| `DrawerIDStableAcrossRooms` | storage | No | Drawer IDs are deterministic from wing + content only; room changes don't break identity |
| `AuditDetectsMismatches` | palace → storage | No | Audit correctly identifies drawers classified into the wrong room |
| `AuditApplyFixes` | palace → storage | No | `--apply` reclassifies mismatched drawers and search index reflects moves |
| `AuditWithScoringOverrides` | config → palace → storage | No | Audit respects scoring overrides when re-scoring drawers |
| `AuditKeywordCoverage` | palace → storage | No | Audit reports which keywords fired vs dead weight per room |
| `TuneDetectsAndProposes` | palace → llm → storage | No | Tune workflow samples drawers, queries mock LLM, proposes weight changes |
| `TuneApplyImproves` | palace → llm → config | No | `--apply` writes proposals to config; subsequent audit shows improvement |
| `TuneEstimate` | palace | No | `--estimate` reports token count without making API calls |
| `DiscoverDetectsAndProposes` | palace → llm → storage | No | Discovery finds new keywords from unclassified content via mock LLM |
| `DiscoverApplyReducesGeneral` | palace → llm → config | No | `--apply` reduces "general" fallback rate in subsequent audit |
| `DiscoverEstimate` | palace | No | `--estimate` reports token count without making API calls |
| `DiscoverRejectsRegressions` | palace → storage | No | Proposals causing regressions (negative score) are filtered out |

### `internal/capture/` — Capture Pipeline Tests

| Test | ONNX? | What it proves |
|------|-------|----------------|
| `CaptureAndSearch` | Yes | Full transcript → chunk → embed → store → search pipeline |
| `CaptureRoomClassification` | Yes | Chunks land in correct rooms based on content keywords |
| `CaptureEntityExtraction` | Yes | Entities extracted and written to KG with triples |
| `CaptureLargeTranscript` | Yes | 100KB transcript handled within performance bounds |

### `internal/search/` — Search Engine Tests

| Test | ONNX? | What it proves |
|------|-------|----------------|
| `SearchSemanticRanking` | Yes | Related content ranks above unrelated content |
| `RebuildAndCache` | Yes | Embed cache populated on rebuild, used on second rebuild |
| `IndexDrawerAndSearch` | Yes | Incremental indexing preserves all metadata |
| `StructuralBoostsWithRealEmbeddings` | Yes | Wing filter boosts matching results with real vectors |

---

## MockEmbedder vs Real ONNX

| Aspect | MockEmbedder | ONNX Embedder |
|--------|-------------|---------------|
| Vectors | Deterministic SHA-256 hash | Learned semantic representation |
| Similar text → similar vectors? | No | Yes |
| Speed | ~0ms | ~1-5ms per text |
| Dependencies | None | hugot, model file |
| Use case | Index mechanics, storage, tool logic | Semantic ranking, retrieval quality |

Unit tests use MockEmbedder because they test *mechanics* (does the index
insert/delete correctly? does the tool parse parameters?). Integration
tests use real ONNX because they test *behavior* (does search return
relevant results? does the pipeline produce searchable content?).

---

## Writing New Tests

### Unit test in an existing package

Follow the package's existing patterns. Use `embedder.NewMock(384)` for
any test that needs an embedder. Use `t.TempDir()` for vault roots.

### Integration test requiring ONNX

1. Place it in `internal/integration/` (cross-layer) or alongside the
   package (single-package integration).
2. Name it `TestIntegration*` so `make integration` discovers it.
3. Call `newHarness(t, true)` or check `testing.Short()` to skip in
   short mode.
4. Use `projectCacheDir(t)` for the ONNX model path.

### Integration test NOT requiring ONNX

Same as above but use `newHarness(t, false)` — gets a MockEmbedder.
These tests run in all modes including `make test`.
