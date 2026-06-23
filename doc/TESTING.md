# Testing Strategy

**Last updated:** 2026-05-31

This document describes the testing strategy for vibe-palace, including
the unit test infrastructure, the integration test architecture, and the
ONNX model caching system that makes real-embedding tests practical.

The suite currently runs **~1971 tests** across 38 packages, including
**95 integration tests** (the ONNX/cross-layer tests `make integration`
discovers via the `TestIntegration*` prefix). These counts are approximate
and advisory: they tally `func Test…` declarations — not the table-driven
subtests each may fan out into — and they drift as the suite grows. Derive
fresh numbers with
`grep -rh "^func Test" --include='*_test.go' internal cmd | wc -l` (total)
and `grep -rh "^func TestIntegration" --include='*_test.go' internal cmd | wc -l`
(integration).

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
**Flags:** `go test -count=1 -cover ./...`
**Duration:** ~70 seconds (warm cache)
**ONNX required:** Yes

Runs everything: unit tests plus all integration tests.
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

### Test helper: `testutil.ProjectCacheDir(t)`

The exported `ProjectCacheDir(t)` helper lives in
`internal/testutil/cachedir.go`. It walks up from the test's working
directory to find the project root (`go.mod`), then returns
`.cache/models/` under that root. This ensures all packages share the
same model cache regardless of where `go test` runs from.

Consumed by the ONNX integration tests in `internal/embedder/onnx_test.go`,
`internal/search/integration_test.go`,
`internal/capture/integration_test.go`, and
`internal/integration/helpers_test.go`.

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

### Friction Analytics (friction-analytics-port)

Unit tests for the pure, slice-based friction-analytics functions and the three
CLI commands that wrap them. "Needs model?" marks tests that exercise the
session `model` field (model-regression detection).

| Test | Layer | Needs model? | What it proves |
|------|-------|--------------|----------------|
| `TestGetFrictionWindows_Empty` | `internal/capture` | No | Empty session slice yields zero-count windows with no divide-by-zero |
| `TestGetFrictionWindows_OrderAndBuckets` | `internal/capture` | No | Windows returned in requested order; each averages only sessions inside its N-day cutoff |
| `TestGetFrictionWindows_BoundaryInclusive` | `internal/capture` | No | A session exactly at the window cutoff is counted (inclusive boundary) |
| `TestGetFrictionWindows_SkipsUnparseable` | `internal/capture` | No | Sessions with unparseable dates are skipped, not counted |
| `TestGetFrictionWindows_Rounding` | `internal/capture` | No | Average friction rounds to one decimal place |
| `TestComputeFrictionTrend_Unknown` | `internal/capture` | No | No recent sessions yields "unknown" direction and no warn |
| `TestComputeFrictionTrend_Improving` | `internal/capture` | No | 7d average well below the 30d baseline → "improving" |
| `TestComputeFrictionTrend_WorseningAndWarn` | `internal/capture` | No | 7d above an elevated baseline → "worsening" with warn flag and message |
| `TestComputeFrictionTrend_WorseningNoWarnBelowFloor` | `internal/capture` | No | Worsening but recent average below the warn floor → no warn |
| `TestComputeFrictionTrend_Stable` | `internal/capture` | No | 7d within the dead-band of 30d → "stable" |
| `TestDetectModelRegressions_Basic` | `internal/capture` | Yes | Reports the avg-friction delta across a model-change boundary |
| `TestDetectModelRegressions_SkipsEmptyModelAndCounts` | `internal/capture` | Yes | Empty-model sessions excluded from runs and counted as unmodeled |
| `TestDetectModelRegressions_ConsecutiveRunGrouping` | `internal/capture` | Yes | Consecutive same-model sessions collapse into one run |
| `TestDetectModelRegressions_NoBoundaries` | `internal/capture` | Yes | A single model run produces no regressions |
| `TestTopFrictionSessions_OrderAndLimit` | `internal/capture` | No | Highest-friction first, limited to n |
| `TestTopFrictionSessions_TieBreak` | `internal/capture` | No | Friction ties broken by most-recent date then iteration |
| `TestTopFrictionSessions_NonPositiveN` | `internal/capture` | No | n <= 0 returns nil |
| `TestTopFrictionSessions_NLargerThanInput` | `internal/capture` | No | n larger than the input returns all sessions |
| `TestGetCorrectionDensitySeries_MissingAndOrder` | `internal/capture` | No | nil-breakdown sessions counted as missing; points are chronological |
| `TestGetCorrectionDensitySeries_AllMissing` | `internal/capture` | No | All-nil breakdowns → empty points, missing count equals total |
| `TestGetCorrectionDensitySeries_MeasuredZeroNotMissing` | `internal/capture` | No | A present-but-zero breakdown is a measured point, never counted missing |
| `TestComputeEffectiveness_ContextDelta` | `internal/capture` | No | With-context vs without-context outcome split and the delta between them |
| `TestComputeEffectiveness_Empty` | `internal/capture` | No | Empty slice yields zero totals |
| `TestComputeEffectiveness_WeekBucketingAndSkipBadDate` | `internal/capture` | No | ISO-week (Monday) bucketing; unparseable dates skipped |
| `TestAnalyzeFrictionBreakdown_EmptyIsNonNilZero` | `internal/capture` | No | Empty transcript yields a non-nil, all-zero breakdown (measured zero, not absent) |
| `TestAnalyzeFrictionBreakdown_SubScores` | `internal/capture` | No | Each friction signal maps to its capped 0–25 sub-score |
| `TestAnalyzeFrictionBreakdown_TotalMatchesLegacy` | `internal/capture` | No | Breakdown total equals the legacy `AnalyzeFriction` composite score |
| `TestFrictionBreakdownTotal` | `internal/storage` | No | `FrictionBreakdown.Total()` sums the four sub-scores, capped at 100 |
| `TestSessionBreakdownRoundTrip` | `internal/storage` | No | `friction_breakdown` survives a write/read frontmatter round-trip with presence preserved |
| `TestBootstrapFrictionTrendWarn` | `internal/tools` | No | Bootstrap surfaces `friction_trend` with warn and appends the nudge to post-bootstrap instructions |
| `TestBootstrapNoFrictionTrendEmptyVault` | `internal/tools` | No | An empty vault omits the `friction_trend` field |
| `TestRunFrictionEmpty` | `cmd/vp` | No | No sessions prints "No sessions found." |
| `TestRunFrictionHuman` | `cmd/vp` | No | Human output shows the recent-week line and triage table |
| `TestRunFrictionJSON` | `cmd/vp` | No | `--json` emits the `recent_week` + `top` payload |
| `TestRunTrendsEmpty` | `cmd/vp` | No | No sessions renders empty windows, density, and regressions |
| `TestRunTrendsHuman` | `cmd/vp` | No | Human output shows windows, correction density, and model regressions |
| `TestRunTrendsJSON` | `cmd/vp` | No | `--json` emits `windows` + `correction_density` + `model_regressions` |
| `TestRunEffectivenessEmpty` | `cmd/vp` | No | No sessions prints "No sessions found." |
| `TestRunEffectivenessHuman` | `cmd/vp` | No | Human output shows the overall and per-week outcome split |
| `TestRunEffectivenessJSON` | `cmd/vp` | No | `--json` emits the full `EffectivenessResult` |

### `internal/integration/` — Hook Pipeline Tests

| Test | Layers | ONNX? | What it proves |
|------|--------|-------|----------------|
| `HookPipeline_EndToEnd` | hook → archive → capture → storage | No | Full hook flow: archive transcript, create session note with friction score, write claim sentinel, idempotent skip on re-run, isolation between sessions |
| `HookInstall_EndToEnd` | hook → settings | No | Install replaces `vv hook` with `vp hook`, preserves user hooks, uninstall removes cleanly |

### `internal/integration/` — CLI Dispatch Tests

| Test | Layers | ONNX? | What it proves |
|------|--------|-------|----------------|
| `IntegrationDispatchParentBareShowsHelp` | cli → cmd/vp | No | `vp config` renders parent help on stdout and exits 0 via the framework dispatch gate (no per-parent stubby `Run` closure) |
| `IntegrationDispatchParentUnknownSubcommand` | cli → cmd/vp | No | `vp config bogus` routes the unknown token to stderr with `ExitUser` (1); guards against the pre-plan silent-ExitOK behavior |
| `IntegrationDispatchKnownSubcommandHelp` | cli → cmd/vp | No | `vp hook install --help` still routes through the two-word lookup with exit 0 after the dispatch gate was added |

### `internal/integration/` — Vault Commit Path Tolerance

Regression for the iter-134 `/wrap` failure: a never-written
`Projects/<slug>/memory/` dir in the `--paths` list made `git add` exit 128 and
aborted the whole commit. `CommitAndPushPaths` now filters supplied paths absent
from both worktree and index before staging, reporting them in `SkippedPaths`.

| Test | Layers | ONNX? | What it proves |
|------|--------|-------|----------------|
| `VaultCommitTolerateMissingPath` | cli → storage → git | No | Full-stack `vp vault commit --paths Projects/demo/resume.md,Projects/demo/memory`: exits 0, prints `Skipped (absent): Projects/demo/memory` on stderr, commits `resume.md` (tracked + "wrap demo" at HEAD), and never creates/commits the absent memory path |

Unit coverage in `internal/storage/vaultsync_test.go`:
`TestCommitAndPushPaths_SkipsNeverExistedPath` (absent path skipped, real path
committed), `TestCommitAndPushPaths_DeletionIsStagedNotSkipped` (tracked-but-
deleted survives the filter, removal staged), `TestCommitAndPushPaths_MixedExistingDeletedAndGhost`
(mixed set partitions correctly), `TestCommitAndPushPaths_AllFilteredOutIsNoOp`
(non-empty input filtering to empty is a benign no-op, not the zero-input error),
and `TestCommitAndPushPaths_NeverWrittenMemoryDir` (the iter-134 memory-dir
regression at the unit boundary).

### `internal/storage/` — Vault Sync Stranded-Commit Hardening

Hardens `CommitAndPushPaths`'s push/rebase recovery so a dirty working tree can
no longer strand a local capture commit, and so an already-ahead branch heals
instead of compounding the strand across sessions (the observed ahead-2 → ahead-N
divergence). Three behavioral fixes — a loud `Stranded` surface (commit created +
push attempted but reached no remote, distinct from a clean no-remote downgrade),
a `--autostash` rebase that distinguishes a TRUE content conflict (state-dir
probe → abort + strand) from an autostash re-apply pop-conflict (commit still
lands and pushes; `PopConflict`/`PopConflictPaths` name the marked files, edits
preserved in the git stash), and a network-free already-ahead reconcile guard.

The git-behavior assumptions were pinned by a throwaway-repo experiment (git
2.54.0): a pop-conflict exits **0** (not non-zero), and `merge-base
--is-ancestor` cannot discriminate true-vs-pop (it is true in both) — so the
`rebase-merge`/`rebase-apply` state-dir is the only reliable discriminator.

Unit coverage in `internal/storage/vaultsync_test.go` (all use real `git`
subprocesses against bare-remote + clone fixtures — full-stack for this path):
`TestPushResult_Stranded` (the four strand/not-strand cases),
`TestCommitAndPushPaths_AutostashDirtyTreeRebases` (dirty tracked file no longer
defeats the rebase — the core fix), `TestCommitAndPushPaths_AutostashPopConflict`
(pop-conflict: commit landed + pushed, `PopConflict` set, NOT re-stranded, stash
retained), `TestCommitAndPushPaths_TrueRebaseConflictStrands` (true conflict
aborts + skips push + strands, no leftover rebase state),
`TestCommitAndPushPaths_AlreadyAheadReconcilesThenPushes` /
`_AlreadyAheadPersistentConflictStrands` /
`_AlreadyAheadGuardFailsOpen` (Fix B reconcile, lossless strand-on-conflict, and
fail-open / no-fire on push=false / unresolved-ref / not-ahead), plus
`TestRebaseInProgress` and `TestUnmergedPaths` (the discriminator helpers). The
pre-existing `TestCommitAndPushPaths_PushRebasesOnNonFastForward` stays green
through the restructured branch. `internal/storage/vaulttidy_test.go` mirrors the
strand at the `TidyVault` boundary (`TestTidyVault_StrandedWhenAllRemotesFail`,
`_NotStrandedOnSuccess`); `internal/tools/system_tools_test.go` covers the MCP
`vp_vault_tidy` surface (`TestVaultTidy_StrandedStatus` → `status:"stranded"`,
`TestVaultTidy_PartialPushCount` → corrected `pushed to N/M remotes` count).

### `test/e2e/dispatch/` — Bash E2E (`make dispatch-e2e`)

End-user-level verification that every parent-command exit-code
contract holds against the real binary.

| Case | What it asserts |
|------|-----------------|
| `01-parent-bare-shows-help.sh` | `vp config` → help on stdout, empty stderr, exit 0 |
| `02-unknown-subcommand-is-exit-user.sh` | `vp config bogus` → `unknown subcommand "bogus"` on stderr, empty stdout, exit 1 |
| `03-known-subcommand-help.sh` | `vp hook install --help` → help on stdout, exit 0 (two-word lookup regression guard) |
| `04-bare-parent-exit-code-discipline.sh` | Fan-out check across `vault`, `commands`, `migrate`, `skills`, `archive` — bare exits 0 with help, unknown-subcommand exits 1 |

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

### `internal/search/` — Recall Harness (`recall_test.go`)

A model-free harness that guards the **`VectorIndex` brute-force exactness
claim** (100% recall) against an independent ground-truth scan. It runs in the
`-short` fast suite — **no ONNX**.

- **Corpus:** four constitutional documents under
  `internal/search/testdata/constitution/` (US Constitution, US Amendments,
  Canada Constitution Act 1867, Mexico 1917 Constitution), chunked at ~600
  chars into **~801 chunks**.
- **Vectorizer:** a deterministic, model-free hash-TF-IDF embedder
  (2048-dim) — stop words removed, each remaining word feature-hashed to one
  dimension and weighted by corpus IDF, then L2-normalized. Shared words between
  query and document land on the SAME dimensions, producing real cosine signal
  without any neural model.
- **Ground truth:** an independent exhaustive cosine scan computed with the
  *same* `cosineDistanceF32` the index uses, so GT and index are bit-identical.
- **Exactness assertion:** **tie-robust distance-bound** — every returned
  distance must be `<=` the GT k-th-smallest distance (plus a tiny `eps` for
  float32 op-ordering). It deliberately does **not** assert per-rank ID equality
  nor set-overlap recall, because the corpus has duplicate structural lines that
  produce ties at the k-th boundary and `sort.Slice` is not stable — those
  assertions would be tie-fragile and flaky. The distance-bound property is the
  real correctness signal.

| Test | ONNX? | What it proves |
|------|-------|----------------|
| `TestConstitutionRecall` | No | Across k ∈ {5,10,20} and sampled queries, every returned distance stays within the GT distance bound (set-overlap recall@k is logged for information only) |
| `TestConstitutionCrossDocumentSearch` | No | Re-embedded natural-language phrase queries (e.g. "necessary and proper", "right to bear arms", "amparo") surface their expected source document within the top-10 |
| `TestConstitutionDeleteAndSearch` | No | After deleting ~20% of the corpus, search still satisfies the distance bound over survivors and never returns a deleted id |

**Scope (important):** this harness guards the **`VectorIndex` KNN data
structure** with synthetic vectors. It does **not** exercise the production
384-dim ONNX embedding path or `Engine.Search` — those are covered by the
real-embedder integration tests in `internal/search/integration_test.go`. The
harness retains a set-overlap recall@k metric (logged, not asserted here);
should the index boundary ever be pointed at an approximate backend such as
HNSW, that recall@k becomes the meaningful threshold to assert (e.g. `>= 0.90`),
since an approximate index is not expected to hold the exact distance bound.

---

## Write/Wrap Surface Unit Tests

The restore-mcp-vault-surface work added three pure-unit packages (no ONNX,
run in `make test`). They underpin the vault-CRUD, commit, resume-edit, and
wrap-state MCP tools.

### `internal/vaultfs` — Vault File CRUD (coverage 82.6%)

Read / write / edit / delete / move / exists / sha256 over vault-relative
paths, plus the path-safety and stamping primitives. Tests cover the happy
paths, traversal/escape rejection and other safety guards, atomic-write
behavior, optimistic-concurrency `expected-sha256` mismatches, and
enumeration of the cross-package stamp writers.

### `internal/mdutil` — Markdown Section Editing (coverage 93.4%)

Section-aware editing used by the resume-edit tools: locating and rewriting
`##`/`###` blocks, and the reserved `### Carried forward` bullet operations
(add / remove / promote). Tests cover heading normalization, slug matching,
insert positioning, and idempotent removal.

### `internal/wrapstate` — Wrap-State Collection (coverage 80.1%)

The engine behind `vp_collect_wrap_state` / `vp_stamp_iter` /
`vp_preflight_wrap`. Tests cover iteration parsing
(`^### Iteration (\d+)\b`), task-delta computation as a filesystem
set-difference against the snapshot, the `.vibe-palace/` anchor read/write
round-trip, the wrap-state record shape, the `doc/TESTING.md` headline
parse (the regexes this very headline feeds), and the preflight readiness
matrix.

---

## MCP Surface-Handshake Check Tests

The `mcp-surface-handshake` epic added the `vp check` surface row and the
`vp check --json` machine-readable report. These are pure-unit tests (no
ONNX, run in `make test`).

### `internal/check/surface_test.go` — Surface Compatibility Check (combined new-code coverage 87.8%)

Covers `CheckSurface(vaultRoot)`: empty vault → `Pass`, empty/unreachable
path → `Pass`, a stamp at `MCPSurfaceVersion` → `Pass`, and an ahead vault
(stamp at `MCPSurfaceVersion+1`) → `Fail` with remediation `Details`. The
ahead-vault staging helper mirrors `internal/mcp/surface_gate_test.go`.

### `internal/check/json_test.go` — `vp check --json` Report Shape

Covers `ToJSON`: status-string projection (`pass`/`fail`/`skip`/`info`),
per-bucket summary tallies, `exit_code = 1` iff any check is `Fail`, the
folding of `Summary`+`Details` into one `detail` line, stable JSON field
names, and the deliberate omission of vibe-vault's `schema` field (no
context-schema counter exists in vibe-palace).

### `cmd/vp` — Flag Wiring

`cmd_check_test.go` drives `vp check --json` end-to-end (JSON parse, binary
block, surface row present, `exit_code`/exit-code agreement) and asserts the
registered tool count is positive. `cmd_version_test.go` covers
`vp version --surface` (`surface: <N>`).

### `cmd/vp` — Tool-Surface Golden Invariant (Phase 6)

`tool_surface_golden_test.go` pins the full MCP tool surface against the
build-time golden at `internal/mcp/tool_surface.golden.json`. The golden is
`{surface_version, tools:[{name, mutating, schema_sha256}]}`, name-sorted for
determinism, generated from the real registry (`Registry.List()`) via the same
throwaway-registry construction `registeredToolCount` uses. `mutating` is the
Phase 2 write-gate flag; `schema_sha256` is the hex sha256 of each tool's
parameter JSON Schema canonicalized through `encoding/json` (so whitespace and
key order don't affect the hash). `ToolInfo` has no tool-level `required`
field, so — unlike vibe-vault's `required_inputs` golden — the per-tool hash is
what catches param-schema drift.

The test fails on any surface change: a tool added/removed, a `mutating` flag
flipped, or a schema edited. The drift message tells the dev to regenerate with
`-update-golden` AND to *consider* whether `internal/surface.MCPSurfaceVersion`
needs a bump. Regenerating the golden does **not** bump the version — the
surface-version bump is a deliberate operator decision made separately. The
golden currently records `surface_version: 1`, 57 tools (20 mutating).

It lives in `cmd/vp` (not `internal/mcp`) because building the full tool set
requires `internal/tools`, which imports `internal/mcp` — so `internal/mcp`
cannot enumerate the tools itself without an import cycle. The golden *file*
still lives next to the registry under `internal/mcp/`. Regenerate / verify:

```
go test ./cmd/vp -run TestToolSurfaceGolden -update-golden   # regenerate
go test ./cmd/vp -run TestToolSurfaceGolden                  # verify
```

It runs as part of `make test` (`go test ./...`) and CI's
`go test -short -race ./...` with no extra wiring (there is no `pre-commit`
aggregate target).

### `cmd/vp` — Surface Merge Driver (Phase 5, combined new-code coverage ~91%)

The `vp-surface` git merge driver resolves `*.surface` stamp conflicts to
`max(ours, theirs)` (monotonic — never go backwards) so two hosts that both
bump a directory's surface version converge on a vault merge instead of
hard-conflicting on the integer. It is registered in `~/.gitconfig` as
`[merge "vp-surface"]` and activated per-path by `*.surface merge=vp-surface`
in the vault's `.gitattributes`; `vp vault pull/sync` auto-install it on the
live (configured-remote) path unless `--no-install-merge-driver` is given.

- `vault_merge_driver_test.go` — the resolution table (ours>theirs,
  theirs>ours, equal, all-zero), missing/malformed ancestor, malformed/missing
  ours/theirs (→ exit 1 + text-conflict markers), auxiliary-field clearing,
  arg-count validation, and the `cmd vault merge-driver` Run path. Inputs are
  arbitrary temp `%O/%A/%B` paths parsed into `surface.Stamp`.
- `vault_merge_driver_install_test.go` — `EnsureMergeDriverInstalled`
  idempotency (two installs → exactly one entry in each file), partial install,
  existing-content preservation, and read-error branches. The **primary**
  auto-invoke test stages a temp vault git repo with a local bare remote and
  drives `vp vault pull` twice on the configured-remote path, asserting the
  driver installs and stays idempotent across repeated real pulls; a defensive
  test covers the no-remote no-op (returns at `gitRemotes()` before install)
  and the `--no-install-merge-driver` opt-out. All tests use a temp `HOME` plus
  `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM` isolation — they never touch the real
  `~/.gitconfig` or vault.

---

## Vault-Write-Concurrency Tests

The `vault-write-concurrency` epic added a per-path advisory write lock
(`internal/vaultlock`) that serializes vault read-modify-write so concurrent
writers cannot lose updates. The unit tests below are pure-unit (no ONNX) and
are written to run under the race detector (`go test -race -short ./...`,
already the `make test` / Tier 1 default); the cross-process test is an
integration-tier test, `-short`-skipped, run by `make integration`.

### `internal/vaultlock` — Lock Primitive

| Test | What it proves |
|------|----------------|
| `TestAcquireCreatesLockDirAndFile` | `Acquire` creates `<root>/.vp-locks/` and exactly one `.lock` sidecar |
| `TestSameTargetSameLockFile` | repeated and lexically-equivalent spellings (`.`/`..` segments) of one path map to a single lock file |
| `TestSymlinkedRootSameLockFile` | a path reached through a symlinked root canonicalizes to the same key (the `vaultfs`-resolved vs `storage`-lexical contract) |
| `TestSerialization` | 50 goroutines on one path never overlap their critical sections — the lock actually serializes |
| `TestDifferentTargetsDifferentLocks` | distinct paths get distinct locks and do not block each other (watchdogged) |
| `TestReleaseIdempotent` | a second `release()` is a no-op returning nil |
| `TestAcquireNonExistentTarget` | locking a not-yet-existing file exercises the `EvalSymlinks` parent-fallback branch |
| `TestAcquireInvalidVaultRoot` | empty or relative `vaultRoot` is rejected |

### `internal/vaultfs` — Concurrent Write Path (`-race`)

| Test | What it proves |
|------|----------------|
| `TestEdit_ConcurrentNoLostUpdate` | 32 goroutines each `Edit` their own anchor in one file; with the lock held across the whole read-modify-write every contribution survives (no lost update) |
| `TestWrite_ConcurrentSameBaseNoCorruption` | many blind `Write`s at one target: last-writer-wins is fine but the final content is exactly one writer's payload, never an interleaved/partial file |

The `.vp-locks` segment refusal is covered by
`TestIsRefusedWritePath_RejectsVpLocksSegment` and
`TestIsRefusedWritePath_AllowsVpLocksSubstringNotSegment` in
`internal/vaultfs/safety_test.go`.

### `internal/storage` — Interlock and RMW (`-race`)

`vaultlock_write_test.go`:

| Test | What it proves |
|------|----------------|
| `TestDeleteDrawerAppendDrawerInterlock` | concurrent `AppendDrawer` and `DeleteDrawer` on one drawers file interlock on the same lock object: the file stays valid JSONL, every appended-and-kept ID survives, every deleted ID is gone (the historical non-interlock bug) |
| `TestConcurrentInvalidateTriple` | concurrent `InvalidateTriple` RMWs — same-triple invalidations converge deterministically and never corrupt the file; distinct triples each land correctly |
| `TestConcurrentAppendIteration` | N distinct markers appended concurrently to one iterations file all survive with separators intact |
| `TestConcurrentAddEntity` | N distinct entities added concurrently to one JSONL file all survive and the file stays well-formed JSONL |

### `internal/integration` — Cross-Process (`-short`-skipped, `make integration`)

| Test | What it proves |
|------|----------------|
| `TestIntegration_VaultLockCrossProcess` | builds the real `vp` binary and launches N concurrent `vp vault edit` child **processes** contending on one seeded file; every fixed-width anchor is converted to its DONE marker exactly once, proving the advisory flock serializes whole-file RMW across separate OS processes (CLI vs MCP). Skipped under `-short`. |

---

## MCP-Native Memory Tests

The `mcp-native-memory` epic added a host-agnostic AI-memory surface stored in
the vault, a one-way harvest of Claude's host-local native memory, and the dual
commit/sync model (SessionEnd harvest + `/wrap`). See ADR-004
(`doc/adr/004-mcp-native-memory.md`) for the design. These are pure-unit tests
(no ONNX, run in `make test`).

### `internal/storage` — Memory Storage (`memory_test.go`)

Frontmatter parse/write round-trip for `Projects/<slug>/memory/` files
(top-level `name`/`description`, nested `metadata.type`), lenient parsing across
top-level `type:` and native `metadata.type:`, the valid-type set
(`user`/`feedback`/`project`/`reference`), `MEMORY.md` index skipping in list
operations, and the `Rel` population on list/read.

### `internal/memory` — Harvest Engine (`harvest_test.go`)

The one-way drain: native-dir resolution from transcript and from cwd, routing
typed files into the vault, dedup of identical content, `.harvested-<ts>`
suffixing on same-name/different-content, `MEMORY.md` index drop, host-local
deletion of routed/deduped/index originals, dry-run zero-mutation reporting, the
native-missing clean no-op (Grok/Zed), and the commit-if-dirty step that catches
direct `vp_memory_write`s.

### `internal/tools` — Memory MCP Tools (`memory_tools_test.go`)

Handler logic for `vp_memory_write`/`read`/`list`/`delete`/`harvest`: parameter
parsing and validation, project scoping, the list index shape (no bodies), and
harvest param wiring (`project`/`cwd`/`dry_run`/`push`).

### `internal/wrapstate` — Memory Dirt Categorization (`collect_test.go`)

| Test | What it proves |
|------|----------------|
| `TestDirtyProbes` | vault dirt is split into non-memory vs memory by path (including rename-by-destination and quoted paths) |
| `TestCollect_MemoryDirtNotNagWorthy` | memory-only dirt sets `MemoryHasUncommittedWrites` but not `VaultHasUncommittedWrites` |
| `TestPreflight_MemoryDirt` | memory-only dirt emits a `memory_dirty` NOTE and no `vault_dirty` warning; non-memory dirt still warns |

### `internal/hook` — SessionEnd Harvest (`hook_test.go`)

| Test | What it proves |
|------|----------------|
| `TestRun_SessionEndHarvests` | `SessionEnd` drains the native dir into the vault and commits |
| `TestRun_StopIsHarvestNoop` | `Stop` never harvests |
| `TestRun_PreCompactIsHarvestNoop` | `PreCompact` never harvests |
| `TestRun_ClaimDecoupling_ArchiveAndHarvestRunWhenClaimed` | archive and harvest run regardless of claim state |

---

## LLM-Enrichment-Synthesis Tests

The `llm-enrichment-synthesis` epic replaces the heuristic SessionEnd
auto-summary with a real LLM synthesis (summary/decisions/open-threads/tag),
behind the opt-in `[enrichment]` config block, with a synchronous pass plus a
host-local async queue + drain. See ADR-005
(`doc/adr/005-llm-enrichment-synthesis.md`) for the design. These are pure-unit
tests (no ONNX, run in `make test`) except where a live `httptest` LLM endpoint
is noted; those still run in `make test` (no network — the fake server is
in-process).

### `internal/llm` — Completer, Anthropic, Shared Retry

`retry_test.go` covers the `retryWithBackoff` helper extracted from
`ChatCompletion`: success first try, retry on 429 and on 500 then success,
retries exhausted, a non-retryable status returned immediately, context-cancel
mid-backoff, and a transport error exhausting retries.

`completer_test.go` covers `*Client.Complete` (system/user → first choice, error
propagation), `Client.Name`, and the `NewCompleter` factory (OpenAI default,
Anthropic selection, Anthropic constructor validation).

`anthropic_test.go` covers the native Anthropic client against an `httptest`
server: request shape (`x-api-key`/`anthropic-version` headers, body), the
`max_tokens` override vs default, retry on 429, non-OK error, empty-content
error, invalid-JSON response, and the default-endpoint constructor.

### `internal/enrichment` — Extraction, Synthesis, Template

`extract_test.go` — `ExtractPromptInput`: both content shapes (plain string +
content-block array), tool-count tallying, file-path collection, message counts,
and the 12000-char UserText/AssistantText truncation.

`enrichment_test.go` — `Generate`/`Enricher`: happy path, fenced and bare-fenced
JSON stripping, nil completer, completer error, the one-shot corrective reprompt
(recovers / still fails / errors), `validateTag` and invalid-tag emptying, user
prompt assembly, and the `Enricher` lifecycle (custom/empty system prompt, nil
receiver, nil completer, zero-timeout default).

`template_test.go` — `LoadSystemPrompt` precedence (empty vault, vault override,
missing vault file falls back to embedded), prompt integrity, and a drift guard
on the embedded `enrichment.md`.

### `internal/storage` — RewriteSession and the Enrichment Config Block

`sessions_test.go` adds `TestRewriteSessionOverwritesInPlace` (fixed-path
overwrite, no iteration increment), `TestRewriteSessionByteIdenticalFraming`
(shares `marshalSessionFile` framing with `WriteSession`), and
`TestRewriteSessionInvalidArgs`.

`config_test.go` adds `TestConfigEnrichment` and `TestConfigEnrichmentEmpty`
(the `[enrichment]` block resolves into `EnrichmentConfig`; an absent block is
the zero value) and `TestCurrentVersionMinor` (the additive 1.0 → 1.1 bump).

### `internal/capture` — Enrichment Integration, Queue, Config Builder

`session_test.go` adds the inline-enrichment cases:
`TestWriteSessionEnrichmentSuccess` (summary/decisions/threads/tag overwritten,
`enriched_by`/`enriched_at` set, `<!-- enriched -->` fence present),
`TestWriteSessionEnrichmentFailureFallsBack` (LLM failure → plain note, no
provenance, capture still succeeds), and `TestWriteSessionNilEnricherUnchanged`
(nil enricher is byte-for-byte the old plain behavior), plus a direct
`buildSessionBody` fence test (adding `EnrichedBy` wraps the plain body verbatim;
clearing it restores the byte-identical plain body).

`enrichqueue_test.go` — the host-local `<CWD>/.vibe-palace/enrichment-queue/`
queue and drain: enqueue round-trip, drain happy path, the byte-identical
inline-vs-drain convergence (and `EnrichedAt` preserved on re-drain), transient
failure renames the claim back (no stranded `.processing`), an already-claimed
`.processing` item is left untouched (glob invisibility), corrupt-item removal,
nil-result enqueue, the `max` cap, the nil-enricher / empty-queue no-ops, and
`TestWriteSessionEnqueueOnMiss` / `TestWriteSessionNoEnqueueWithoutCWD`.

`enricher_config_test.go` — `NewEnricherFromConfig`: disabled config → nil
enricher, missing `api_key_env` name, unset env var, Anthropic provider, and the
OpenAI-compatible provider requiring (and accepting) a `base_url`.

### `internal/tools` — `vp_capture_session` enrich Param (incl. live path)

`session_tools_test.go` covers the opt-in `enrich` bool:
`TestCaptureSessionEnrichDefaultPlain` (default false → plain note),
`TestCaptureSessionEnrichDisabledConfig` (enrich requested but `[enrichment]`
disabled → plain), and `TestCaptureSessionEnrichLive` — a full-stack pass that
points a project `[enrichment]` config's `base_url` at an in-process `httptest`
fake LLM endpoint and asserts the note is synthesized end to end.

### `internal/hook` — SessionEnd Enrichment (`hook_test.go`)

`TestRun_EnrichmentEnabled` drives the SessionEnd auto-capture path with
`[enrichment]` enabled in the project config, pointing `base_url` at an
`httptest` fake LLM endpoint, and asserts the hook synthesizes (and drains) end
to end — the full-stack integration coverage for the hook side, mirroring the
MCP-tool live path above.

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
4. Use `testutil.ProjectCacheDir(t)` for the ONNX model path.

### Integration test NOT requiring ONNX

Same as above but use `newHarness(t, false)` — gets a MockEmbedder.
These tests run in all modes including `make test`.
