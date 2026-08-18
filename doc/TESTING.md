# Testing Strategy

**Last updated:** 2026-08-18

This document describes the testing strategy for vibe-palace, including
the unit test infrastructure, the integration test architecture, and the
ONNX model caching system that makes real-embedding tests practical.

The suite currently runs **~2926 tests** across 48 packages, including
**118 integration tests** (the ONNX/cross-layer tests `make integration`
discovers via the `TestIntegration*` prefix). These counts are approximate
and advisory: they tally `func Test…` declarations — not the table-driven
subtests each may fan out into — and they drift as the suite grows. Derive
fresh numbers with
`grep -rh "^func Test" --include='*_test.go' internal cmd | wc -l` (total),
`grep -rh "^func TestIntegration" --include='*_test.go' internal cmd | wc -l`
(integration), and
`go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | grep -c .`
(packages carrying tests).

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

## Live-vault canaries (`internal/taskgraph/live_vault_test.go`, `internal/storage/tasks_live_vault_test.go`, `internal/tools/bootstrap_live_vault_test.go`)

A small class of test runs against the **operator's real vault**, resolved through
`storage.OpenVaultGlobal()`, and **skips** when no vault is configured. It is not an
integration test and it is not a fixture — it is a **canary over real data**, and it
exists because this project has twice shipped a bug that every fixture test passed:

- `note_path` was empty on **every session ever captured for ~6 months**, green suite throughout.
- Eight write-only `SessionMeta` fields survived because the tests **seeded the fields themselves**
  and asserted they round-tripped.

The mechanism is the same both times: **a test that seeds the value it then asserts cannot catch a
value that is never written.** Only the real corpus contains the fenced snippets, the prose that
merely *discusses* a header line, and the accumulated markdown weirdness of a hundred files.

Current canaries:

| Test | What it would catch |
|  | MCP stdio → capture → archive → storage (+ optional memory) | No* | Post-defaults hook-less path: derived , flag omitted → note + inline archive bi-link + friction + zero makeHandler WARN; Claude SessionEnd leg on a separate temp vault; structural equivalence not byte identity (*search leg needs engine; uses mock/short-safe setup) |
|  | MCP stdio → capture | No | Unknown host without  stays thin (no auto inline archive) |
|---|---|
| `TestLiveVaultHasNoPhantomRelations` | `parseTaskMeta` regressing to a whole-file scan and reading a task's **body** as a real `Parent`/`Depends` — several real task files discuss the header syntax in prose and inside fences |
| `TestLiveVaultGraphIsCleanAndTerminates` | A structural lie in the real backlog (cycle / dangling ref / retired parent with live children) — and, under a bounded timeout, a cycle being **walked instead of detected** |
| `TestLiveVaultAmendNeverDisturbsTheHeaderBlock` | `vp_manage_task amend` splicing a body section in a way that moves a task's status, priority, title or edges. Run over **every real section of every real task file** — the guarantee is structural, so it is asserted against the corpus rather than a body written to make it pass |
| `TestLiveVaultAmendNeverMatchesAFencedHeading` | `sectionBounds` regressing to a naive scan and splicing **into a code fence**. Not hypothetical: **22 H2 headings in this project's own task files exist only as fenced sample text** — including the `## Decision` quoted by the task that specified `amend` |
| `TestLiveVaultAmendIsIdempotentOnRealBodies` | A retried amend **duplicating** a section on a real body instead of converging — the failure a crash-and-retry would produce |
| `TestLiveVaultRetitleNeverDisturbsAnythingElse` | `replaceTitleLine` (whole-file, first-wins, **fence-unaware**) rewriting an H1-shaped line that is not the title. Safe only because `CreateTask` always writes `# Title` first and `validateTaskBody` refuses an unfenced H1 in a body — **that is an invariant about the CORPUS, not the function**, so only the corpus can check it. Asserts exactly one line changed per file |
| `TestBootstrapLiveVaultStillRestoresASession` | A live restart coming back unusable. Asserts the payload an agent actually receives against the real vault: the handles (`resume_uri`, `workflow_uri`, `resume_sha256`) present; **no document body** — neither a `resume` nor a `workflow` key, and no distinctive line of the live resume anywhere in the marshalled payload; `head_of_queue` non-empty when a backlog exists, every row and every session row carrying its URI; `ranking` present and naming the `structural` ranker; the wire carrying no `budget` / `shed_core` / `max_tokens` / pinned-zone banner; and `complete` last on the wire. **It asserts no size at all** — a ceiling reintroduced here would re-create the disease PRD §1.10 removes. Its body assertion was INVERTED at iteration 313: through Phase 2 it required the bodies to be present, which was that phase's gate; Phase 3 made the payload an index, and the assertion was replaced in the same commit rather than left to lie. Only the real vault has a task graph and a session corpus large enough for assembly to go wrong on |

**Rules for adding one:** it must `t.Skip` (never fail) when the vault is absent, it must never
write, and it must assert something a fixture *structurally cannot* — otherwise it is just a slow
unit test with a dependency on one machine.

**🔴 A live-vault canary MUST run uncached, and `go test` will not do that for you.** The vault lives
outside the module, so Go's testlog cannot observe its contents changing and will serve a **cached
verdict** — an instrument confidently describing a vault it never opened. Measured 2026-07-26: growing
the live `workflow.md` from 13.5 KB to 19.5 KB still reported `ok (cached)`, i.e. the canary silently
did not run on precisely the regression it exists to catch. Relying on a human to remember `-count=1`
is the "runs when someone REMEMBERS to run it" failure this whole class of test was built to delete.
So the invocation is a make target, not a convention:

    make live-canary    # go test -count=1 -v -run TestBootstrapLiveVaultStillRestoresASession ./internal/tools/

`make test` depends on it, so the uncached run happens on every ordinary test invocation. **Any new
live-vault canary belongs in that target's `-run` pattern** — adding one and leaving it to `go test
./...` re-opens the hole.

**No size assertion, deliberately (310).** This canary once asserted a token margin, then core
integrity against `Budget.ShedCore`, against a payload budget of 8,000 and later 16,000. Phase 2
deleted the budget, the ladder and the tier vocabulary outright, so none of those assertions has a
subject any more and none was rewritten into a smaller ceiling — PRD §1.10 removes the numeric
ceiling on a session-start payload rather than tuning it. What the canary measures now is delivery:
the handles, the bodies, the absence of every rationing artifact, and the terminal `complete`
sentinel. **If a budget ever grows back, it fails here first, on the live vault.**

**A canary that finds no hazard should say so, not pass silently.** `TestLiveVaultAmendNeverMatchesAFencedHeading`
skips (rather than passes) when the corpus contains zero fenced-only headings: a green canary with
nothing to guard is indistinguishable from a green canary that is broken, and this project has twice
shipped an auditor that reported zero findings on a tree full of defects.

---

## Bootstrap wire-order and truncation tests (`internal/tools/bootstrap_wire_order_test.go`)

These pin a **transport** property, not a behavioral one: `encoding/json` emits struct fields in
declaration order, nothing on the response path re-serializes through a map (`mcplib.NewToolResultJSON`
marshals the value directly, `vp inject` encodes it directly), so **declaration order is wire order is
cut order**. A host with a fixed inline cap keeps a prefix and discards the rest, and the only thing
that decides what an agent still holds is which fields were declared first.

| Test | What it proves |
|------|----------------|
| `TestBootstrapTruncatedPrefixIsDetectable` | The headline. A real payload cut at the 19,968-byte specimen offset is **detectable from inside the truncated channel**: `complete` is absent from the prefix (and present in the whole document), while `budget`, `resume_uri`, `workflow_uri`, `resume_sha256`, `active_task_count` and `post_bootstrap_instructions` all survive. It also asserts the prefix is *not* valid JSON, so a future payload that shrinks under the cap cannot turn the test green by removing the truncation it measures |
| `TestBootstrapInstrumentsPrecedeBulk` | The order by **byte offset** — the only property a cut respects. Every instrument and recovery handle appears before `"workflow"` and `"resume"` |
| `TestBootstrapCompleteSentinelAlwaysEmitted` | The sentinel's three properties: no `omitempty` (a zero-value result still spells out `"complete":false`, so absence cannot be confused with a false value), **structurally last** in the struct via reflection (the guard against a future field being appended after it), and last on the wire on real marshalled payloads |

The fixture these use is deliberately a payload far larger than the cut, so there is real bulk on the
far side of it. That is not contrived: it is the quantum-ng specimen the `inline-delivery` epic
measured, a real project whose resume and workflow together are ~1.95x a real host's cap. Since 310
vp reduces nothing, so every such payload reaches the host at full size and the cut is the host's
alone — which is exactly the case these tests exist to make survivable.

**The 19,968-byte figure is a specimen, not a constant of the system** — one host on one day
(three Grok results of 60.3 KB, 53.4 KB and 32.7 KB each cut at exactly 19.5 KiB, a *flat* cap rather
than a ratio). The tests cut at an offset and assert what survives it; any offset landing inside the
bulk proves the same property.

### The same contract, generalised (`internal/tools/surface_wire_order_test.go`)

The cap belongs to the **host**, so it applies to every tool result, not just bootstrap's. The
2026-08-12 surface survey measured 19 tools over it and found **every** URI escape hatch declared
*after* the payload it rescues — `vp_get_task` returned 192,060 bytes with `content_uri` at byte
191,956, 172 KB past the cut. These tests pin the fixed layout for the rest of the surface.

| Test | What it proves |
|------|----------------|
| `TestSurfaceTruncatedPrefixIsDetectable` | The headline, table-driven over 12 result structs. Each is marshalled with an over-cap bulk field, cut at the 19,968-byte specimen, and asserted: every recovery handle survives the prefix, `complete` does **not**, and the whole document ends with `,"complete":true}`. Cases that deliberately carry no sentinel (`getLearningResult`, the nested `doctrineResult`) assert its **absence**, so a future blanket "add complete everywhere" cannot land silently. Like the bootstrap version it asserts the prefix is *not* valid JSON, so a payload that shrinks under the cap cannot turn the test green by removing the truncation it measures |
| `TestSurfaceHandlesPrecedeBulk` | The order by **byte offset**, the only property a cut respects. `content_uri`/`content_size` before `content`; `resume_uri` and `sha256` before `content`; `doctrine_uri` before the embedded body; `session_uri` before `body`; `vp_read_resource`'s whole metadata header before its `content` |
| `TestSurfaceCompleteSentinelIsStructurallyLast` | For all 10 sentinel-carrying structs: **structurally last** via reflection (the guard against a future field being appended after it — how this WILL be broken) and no `omitempty` (a zero value still spells `"complete":false`, so absence cannot be confused with a false value) |
| `TestSurfaceHandlersEmitHandleAndSentinel` | The gap the layout tests structurally cannot see: a perfectly ordered struct whose **handler never populates the handle** emits `"content_uri":""` and passes every offset assertion while helping nobody. Three of these URIs (`resume`, `session`, `command`) existed in `internal/mcp`, had registered resource templates, were served by `vp_read_resource`, and were minted by **no handler at all** — exactly this shape of bug. It also pins the deliberate NON-emission: a wing/room-scoped `vp_get_command` withholds the URI, because the command URI template re-resolves unscoped and would address different bytes. A missing hatch is visible; a lying one is not |

These build their values **directly** rather than through handlers, on purpose: the property under
test is the struct's declaration order, and a fixture large enough to overrun the cap through every
one of twelve handlers would be twelve elaborate vault setups measuring the same one thing.
`TestSurfaceHandlersEmitHandleAndSentinel` covers the handler half separately, against a real vault.

---

## Bootstrap delivery-doctrine tests (`internal/tools/bootstrap_doctrine_test.go`)

The tests above prove the signal is *emitted*. These prove it is *taught* — a sentinel no agent
knows how to read changes nothing, which is why the epic's own acceptance note recorded the
measured `complete`-absent result as proof the signal arrives and explicitly **not** as proof an
agent acts on it.

Three surfaces teach the payload's delivery state and all three are pinned as one contract:
`internal/templates/templates/commands/restart.md` (Step 2), the `vp_bootstrap_context` tool
description, and the Grok `/vpc` hub shim (`internal/shims`). The template reaches only hosts that
ran `/vpc-restart`; the description reaches every agent on every host; the hub fronts the exact host
where the flat cut was measured. Fixing one is the ADR-006 failure mode.

| Test | What it proves |
|------|----------------|
| `TestBootstrapDeliveryDoctrine_AbsentBudgetNeverStandsAlone` | No surface asserts that an absent `budget` means nothing was reduced **without conditioning it on `complete` in the same sentence**. Both readings of the unconditioned claim were observed in the field: its free contrapositive (present ⇒ reduced ⇒ truncated) makes the ladder's routine `recent_sessions` shed read as a failed bootstrap, and in a truncated channel `budget` is absent *because it was cut off*, so the rule tells the agent a silent host cut was a clean delivery. The `[^.]` sentence bound is load-bearing: a qualifier three sentences away does not rescue a rule that reads as unconditional where the agent meets it |
| `TestBootstrapDeliveryDoctrine_RestartTeachesSentinelAndFetch` | Anchored the way `TestEmbeddedCommands_CheckSuiteDelivery` anchors — on the **action**, not a mention. `complete` must be raised in a *bullet* inside Step 2 (prose observing that the sentinel exists is not a rule), and Step 2 must mandate the document FETCH: `vp_read_resource`, `workflow_uri`, `resume_uri`, `resume_sha256`, the words "every restart", and the fetch ordered ahead of the `vp_get_doctrine` call. Rewritten at 313: the old assertion pinned the recovery onto the sentinel bullet, which was right while the bodies arrived inline. Once the payload became an index, a rehydrate-on-truncation rule would leave an agent whose payload arrived WHOLE with no resume and no workflow at all — conditioning the fetch on a truncation signal is exactly how that would ship |

Both were confirmed RED by restoring the old wording at each of the three sites in turn.

---

## Placeholder write-back guard (`internal/scopetoken`, `internal/integration/scopetoken_writeback_test.go`)

The vault's template placeholders (`{{PROJECT}}`, `{{WING}}`, `{{ROOM}}`, `{{DATE}}`) are expanded by
every context-serving reader while those readers' `sha256` covers the **raw** bytes. A body composed
from one and written back therefore passes compare-and-set and bakes the expanded values onto disk.
Both whole-file writers refuse that write; these tests are what say so.

Split deliberately across two packages: the **in-package** tests range the unexported token table, so
they cover a fifth placeholder added tomorrow without an exported list; the **integration** tests
drive `Server.HandleMessage`, because a guard proven only through its helper is not proven on the
path it is installed on.

| Test | What it proves |
|---|---|
| `TestTableIsTheSingleSource` | One table backs both the expander and the guard: for **every** entry, `Expand` erases it and `Lost` reports exactly it. An earlier version could only assert "at least one loss" and stayed green while `Lost` swallowed three of four |
| `TestLostReportsEveryDroppedPlaceholder` | Completeness on the shape a real write-back takes — one body losing every placeholder at once — in table order, with counts |
| `TestTokensCanExpandToNothing` | The rejected design. With an empty scope, `{{PROJECT}}`/`{{WING}}`/`{{ROOM}}` all substitute the **empty string**, so a rule keyed on "the expanded value is present" is structurally blind on three of four placeholders. Counting is not a preference |
| `TestFirstWriteIsNotRefused` | The scope boundary: the rule keys on placeholders in the **existing** bytes, which is what keeps first writes, `vp init` scaffolds and template materialization out of it |
| `TestNonScopePlaceholdersAreNotOurs` | The false positive a `{{[A-Z_]+}}` regex would have created — the skill corpus carries many other token shapes (`{{FOCUS}}`, `{{PATH}}`, `{{SHA}}`, …) that must stay writable **and** removable |
| `TestRefusalIsCallerClassified` | The error class at the source: `apperr.IsCaller` holds, so the MCP seam counts it as friction rather than defaulting it to `fault="internal"` and ambering `vp_health` |
| `TestIntegration_VaultWriteRefusesTokenBake` | The original incident end-to-end: a matching digest, an expanded body, refused — and the message names every lost placeholder with counts, plus both routes out |
| `TestIntegration_VaultWriteRefusesBakeWithoutCAS` | `vaultfs.Write` treats an empty `expected_sha256` as *no* compare-and-set, so the **easier** bake needs no digest at all. A guard that only ran under CAS would leave it open |
| `TestIntegration_UpdateResumeRefusesTokenBake` | The second writer. `storage.WriteResume` does not call `vaultfs.Write`, and the diagnostic must survive its `%w` wrap — counts, remedy and `fault=caller` are all asserted on that path |
| `TestIntegration_EditStillRemovesTokenDeliberately` | The escape hatch stays open. There is no opt-out parameter, so removing a placeholder on purpose goes through `vp_vault_edit` with a **raw** `old_string` — which cannot be composed from an expanded read and is its own proof of provenance |
| `TestIntegration_EditRejectsAnExpandedAnchor` | The half of the retired prose rule that was already enforced: an anchor copied from an expanding reader cannot match where a placeholder lives |
| `TestIntegration_WholeFileWriteKeepingTokensAccepted` | The non-regression floor — preserving the placeholders lands normally |

Mutation-proved six ways, each reddening a different set: deleting either call site (disjoint sets),
keying on expanded-value-presence, running only under CAS, dropping the `apperr.Caller` wrap, and
dropping the counts from the message.

**Two harness lessons are baked into how these were verified.** A mutation that leaves an import
unused does not compile, so `go test` emits no `--- FAIL` at all — indistinguishable from a surviving
guard. And a reporting pipeline can erase its own output. Print that the mutation applied, and count
the failing names, before believing any silence.

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
| `HandshakeDoesNotConstructEmbedder` | MCP → tools → search → embedder | No | A real JSON-RPC `initialize` + `tools/list` against the production tool surface constructs the embedder **zero** times; the first `vp_search` constructs it exactly once, and a second search does not reconstruct it (see below) |
| `ColdSearchBuildsIndexLazily` | MCP → tools → search → storage | No | `vp_search` and `vp_search_cross_project` return **real hits** on projects whose index has never been built — no `Rebuild`, no `IndexDrawer`, only drawers on disk (see below) |
| `SurfaceCheck` | MCP → tools → check | No | JSON-RPC `tools/call` for `vp_surface_check` returns `status:"pass"` with the binary's surface version on a compatible vault; the fail path carries the curated remediation `details` across the wire |
| `Check` | MCP → tools → check | No | JSON-RPC `tools/call` for `vp_check` is reachable on `tools/list` (and `vp_check_resume_refs`, which it subsumed, is gone from it); the default run covers every producer in declared order and repeats identically; the `resume-refs` selector's rows match `check.RunSelected` verdict-for-verdict — name, summary and the `details` array — proving the tool and the CLI dispatch one registry; no `Embedder` row ever crosses the wire; an unknown selector is refused rather than silently reporting a clean bill of health |
| `BootstrapFullContext` | tools → context → storage | No | Bootstrap tool assembles workflow, commands from embedded + vault sources |
| `BootstrapWithSessions` | storage | No | Sessions written via API are readable through list/read operations |
| `FrictionScoringOnCapture` | tools → capture → storage | No | `vp_capture_session` computes and persists friction score; high-friction transcript scores >= 50, smooth < 20 |
| `FrictionScoringNoTranscript` | tools → storage | No | Session without transcript gets friction_score = 0 |
| `FrictionTrendsEndToEnd` | tools → capture → storage | No | `vp_get_friction_trends` returns correctly aggregated weekly metrics from stored sessions |
| `HostParityFootprint` | MCP stdio → capture → archive → storage | No | Post-defaults hook-less path: derived `clientInfo=grok`, flag omitted → note + inline archive bi-link + friction + zero makeHandler WARN; Claude SessionEnd leg on a separate temp vault; structural equivalence not byte identity |
| `HostParityNoAutoArchiveUnknownHost` | MCP stdio → capture | No | Unknown host without `archive_transcript` stays thin (no auto inline archive) |
| `FrictionTrendsEmpty` | tools → capture → storage | No | Trends for project with no sessions returns empty result |
| `FrictionSearchByMinScore` | storage | No | `SearchSessions` with minFriction filter returns only sessions above threshold |

### Code fences (`internal/mdfence`) — iter 191

`mdfence` is the **one** definition of a markdown code fence in this codebase.
Three packages previously carried their own copy of the rule "a fence delimiter
is a trimmed line starting with ``` or ~~~", and all three were wrong in the
same way — so these tests exist to keep the rule in one place and to keep it
CommonMark-correct.

The load-bearing case is a line whose **first non-space characters are an inline
code run**, opened and closed on the same line. `iterations.md:698` is one, and
the tests use it **verbatim** rather than paraphrasing it, because a paraphrase
that does not lead with the run will not reproduce the bug:

```text
  ```bash tutorial``` extraction from `doc/TUTORIAL.md` — deferred
```

The naive rule reads that as a lone OPENING fence, inverts its fence state, and
never recovers. What that cost, per caller:

| Caller | Failure the naive rule caused |
|--------|-------------------------------|
| `wrapstate.NextIterFromIterationsMD` | Swallowed 187 of 191 iteration headings → reported iteration **77** on a project at 190 |
| `storage.validateTaskBody` | **Failed open** — skipped the rest of the body, so a duplicate `**Status:**` passed the check added in iter 184 to stop it, reinstating the 183/184 status-line corruption |
| `check.countSectionTableRows` | **Failed open** — hid every table row below such a line, so `resume-caps` would never breach. **Latent**: no resume.md in the vault carries a fence today |

The rule `mdfence` implements, and each test's job:

| Test | What it proves |
|------|----------------|
| `TestTheRealLineIsNotAnOpeningFence` | An opening **backtick** fence's info string may not contain a backtick — the single rule that makes the line above prose, not a fence |
| `TestOutsideFences/inline code run is prose, not a fence` | The line is returned AND the lines after it stay visible |
| `TestOutsideFences/a real fence still works after an inline code run` | Correctness for the pathological line does not break genuine fences |
| `TestOutsideFences/info string opens, only a bare run closes` | A closing delimiter is a bare run of the same char, ≥ the opener's length |
| `TestOutsideFences/four-space indent is not a delimiter` | 4+ leading spaces is an indented code block |
| `TestOutsideFences/unterminated fence swallows the remainder` | Deliberate: a heading-shaped line in a half-open fence is sample text |
| `TestOutsideFencesReportsOriginalLineNumbers` | Line numbers index the ORIGINAL content — they appear in errors a human must act on |
| `storage.TestValidateTaskBodyDoesNotFailOpenOnInlineCodeRun` | Duplicate `**Status:**`/H1 below an inline run is still rejected — **and** `# Usage` inside a *genuine* fence is still accepted, the case iter 184 protected |
| `check.TestCountSectionTableRowsInlineCodeRun` | Table rows below an inline run are still counted |
| `check.TestCountSectionTableRowsRealFenceStillHidesRows` | A genuinely fenced table still does not inflate the count |

**Do not reimplement fence detection.** If a fourth caller needs it, call
`mdfence.OutsideFences` (structurally-real lines) or `mdfence.Scanner` (when a
Delimiter must be told apart from Fenced content — `check` needs this, because a
fence boundary breaks a contiguous table run and must flush it).

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

### `internal/storage/` — Vault Pull & Phantom-Template Heal

Covers the incoming half of vault sync added in `storage.Pull` (plain-merge
semantics, per-remote `RemoteResults`, and the dirty-`Templates/commands/*.md`
self-heal that unwedged the triggering `vp commands upgrade` incident). Unit
coverage in `internal/storage/vaultpull_test.go` (real `git` subprocesses against
bare-remote + clone fixtures):

| Test | What it proves |
|------|----------------|
| `TestPull_PhantomTemplateHeal` | A dirty template whose content equals the remote ref is `git checkout HEAD`-discarded and recorded in `HealedTemplates`, so the merge succeeds instead of aborting |
| `TestPull_GenuineEditNotHealed` | A genuinely-edited template (nonzero diff vs the remote ref) is left untouched — the heal never clobbers real edits |
| `TestPull_UnreachableRemote` | An unreachable remote is recorded as a failed `RemoteResult` rather than aborting the whole pull |
| `TestPull_MultiRemoteResultMap` | Every remote is attempted and its outcome lands in the `RemoteResults` map |
| `TestPull_RestartFlow` | The `/restart` pull path merges cleanly after a heal |
| `TestPull_NonMainBranch` | Heal + merge work against a non-`main` branch |
| `TestDirtyTemplateCommandPaths` | The dirty-path scan (reusing tidy's porcelain parser) selects exactly `Templates/commands/*.md` |
| `TestPullResult_Stranded` | `Stranded()` reports a pull that reached no remote |

### `internal/storage/` — Vault Sync Orchestration (tidy-before-push)

Covers `storage.SyncVault` — the default `vp vault sync` that classifies, refuses
on genuine dirt before any network I/O, commits swept artifacts locally, then
pulls and pushes — plus the `storage.PushPlain` loop it delegates the push to and
the classifier's transcript-DEFER guard. Unit coverage in
`internal/storage/vaultsyncflow_test.go`, `vaultsync_test.go`, and
`vaulttidy_test.go` (real `git` subprocesses against bare-remote + clone
fixtures):

| Test | What it proves |
|------|----------------|
| `TestSyncVault_CleanArtifacts` | A tree with only sweepable artifacts is committed locally, then pulled and pushed |
| `TestSyncVault_GenuineDirtRefusesBeforeNetwork` | Genuine non-artifact dirt refuses the sync up front, before any pull/push |
| `TestSyncVault_MemoryDoesNotBlock` | Pending `Projects/<slug>/memory/…` is expected, not dirt — it never blocks the sync |
| `TestSyncVault_DeferredInFlightTranscript` | A `.jsonl.zst` whose sibling manifest is not yet on disk is deferred, never committed half-complete |
| `TestSyncVault_PullConflictAbortsBeforePush` | A merge conflict (recorded in `RemoteResults`, not the Go error) aborts before the push |
| `TestTidyVault_DefersInFlightTranscript` | The classifier routes the manifest-pending transcript half to `Deferred`, not `Swept` |
| `TestPushPlain_SingleRemote` / `_TwoRemotesBothSucceed` / `_BadRemoteBestEffort` | The plain-push loop attempts every remote best-effort and returns no top-level error |

MCP handler coverage in `internal/tools/system_tools_test.go`:
`TestVaultSync_BareTidiesAndPushes` (default `sync` tidies then pushes),
`TestVaultSync_BareRefusesGenuineDirt` (refuses on genuine dirt), and
`TestVaultSync_NoTidyIsRawRefusal` (`no_tidy:true` restores the raw refuse-on-any-dirt path).

### `internal/storage/`, `internal/capture/`, `internal/tools/` — Host-Identity Session IDs

Covers the host-qualified `<date>-<fp8>-<NN>` session-id scheme (see
*Session Identity* in `doc/ARCHITECTURE.md`): cross-host collision avoidance via
`surface.WriterFingerprint`, host-scoped `NextIteration` globbing, legacy
`<date>-<NN>` back-compat, the host-scoped enrichment queue, and the analytics
`(Date, Fingerprint, Iteration)` tiebreak.

- **Storage** (`sessions_test.go`): `TestNextIterationHostScoped`,
  `TestCrossHostNoCollision`, `TestReadLegacySessionFile`; plus host-scoped and
  legacy cases in `TestSessionFile` (`paths_test.go`).
- **Capture** (`enrichqueue_test.go`): `TestDrainLegacyQueueItem` and the
  fingerprint round-trip in `TestEnqueueEnrichmentRoundTrip`;
  (`analytics_test.go`) `TestSortTiebreakByFingerprint`.
- **Tools** (`session_query_tools_test.go`): a new host-scoped parse case plus
  five new malformed cases in `TestParseSessionID` /
  `TestParseSessionIDRejectsMalformed`.

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

### `internal/capture/` — Capture Pipeline Tests (coverage 92.5%)

| Test | ONNX? | What it proves |
|------|-------|----------------|
| `CaptureAndSearch` | Yes | Full transcript → chunk → embed → store → search pipeline |
| `CaptureRoomClassification` | Yes | Chunks land in correct rooms based on content keywords |
| `CaptureEntityExtraction` | Yes | Entities extracted and written to KG with triples |
| `CaptureLargeTranscript` | Yes | 100KB transcript handled within performance bounds |

> **Known caveat (pre-existing, not a regression).** `go test ./internal/capture/
> -race` **without** `-short` times out at Go's 600s default deadline. The race
> detector's instrumentation overhead is pathological inside
> `gomlx/backends/simplego`'s parallel executor: `TestIntegrationCaptureLargeTranscript`
> (~32s un-instrumented) is still grinding through `simplego.(*FunctionExecutable).executeParallel`
> when the deadline fires. This was confirmed against a clean worktree at HEAD — it
> predates the lazy-embedder work and is not caused by it. Neither gate hits it:
> `make test` is `-short -race` (the ONNX tests skip) and `make integration` runs
> without `-race`. Do not combine `-race` with the ONNX tests in this package.

### `internal/search/` — Search Engine Tests (coverage 88.2%)

| Test | ONNX? | What it proves |
|------|-------|----------------|
| `SearchSemanticRanking` | Yes | Related content ranks above unrelated content |
| `RebuildAndCache` | Yes | Embed cache populated on rebuild, used on second rebuild |
| `IndexDrawerAndSearch` | Yes | Incremental indexing preserves all metadata |
| `StructuralBoostsWithRealEmbeddings` | Yes | Wing filter boosts matching results with real vectors |
| `TestSearchLazyBuildsIndex` | No | A `Search` on a project with no index builds it and returns hits, instead of the old silent `nil, nil` |
| `TestCrossProjectSearchLazyBuildsAllIndexes` | No | An unfiltered search builds **every** known project's index (`ensureAllIndexes`), not just one |
| `TestLazyBuildRunsOnce` | No | Concurrent searches for the same project join one in-flight build; the index is not rebuilt per query |
| `TestSearchPropagatesBuildError` | No | A failed build is **returned** to the caller, never swallowed into an empty result |
| `TestRebuildBatchesEmbeddings` | No | `Rebuild` embeds cache misses via `EmbedBatch` in `EmbedderBatchSize` chunks, not one drawer at a time |
| `TestRebuildClearsStaleIndex` | No | A rebuild of a now-empty project drops its index, so it stops serving hits for deleted content |

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

The restore-mcp-vault-surface work added two pure-unit packages (no ONNX,
run in `make test`). They underpin the vault-CRUD, commit, and wrap-state MCP
tools. (A third, `internal/mdutil` — markdown section editing — backed the six
surgical resume editors and was **deleted with them**; see *Resume-Editor
Lost-Update Tests* below.)

### `internal/vaultfs` — Vault File CRUD (coverage 82.0%)

Read / write / edit / delete / move / exists / sha256 over vault-relative
paths, plus the path-safety and stamping primitives. Tests cover the happy
paths, traversal/escape rejection and other safety guards, atomic-write
behavior, optimistic-concurrency `expected-sha256` mismatches, and
enumeration of the cross-package stamp writers.

`vaultfs.Edit` is now the **routine `resume.md` write path** (behind
`vp_vault_edit`), so its three loud failure modes are load-bearing rather than
incidental: `old_string` not found, `old_string` ambiguous without
`replace_all`, and `expected_sha256` mismatch. Each is an assertion that the
caller's model of the file is wrong — see *Resume CAS Tests*.

### `internal/wrapstate` — Wrap-State Collection (coverage 85.2%)

The engine behind `vp_collect_wrap_state` / `vp_stamp_iter` /
`vp_preflight_wrap`. Tests cover iteration parsing
(`^### Iteration (\d+)\b`), task-delta computation as a filesystem
set-difference against the snapshot, the `.vibe-palace/` anchor read/write
round-trip, the wrap-state record shape, the `doc/TESTING.md` headline
parse (the regexes this very headline feeds), and the preflight readiness
matrix.

---

## `iterations.md` Heading-Contract Tests (`iterations-md-heading-contract`)

`storage.AppendIterationOwned` composes every entry as `"\n---\n" +
FormatIterationHeader(n, title) + body`, and the reader half only ever
recognised `## Iteration N`. A heading that sits on that frame but is not that
shape is a **real entry boundary the reader cannot see**: the narrative under it
is unaddressable by number *and* is silently served as the tail of the previous
entry. Measured live, `vp_get_iteration` for 108, 110, 125, 128, 145 and 154
each over-returned exactly that way **and reported success**.

The rules live in exactly one place (`internal/wrapstate/heading_contract.go`);
the check producer, the audit dimension and the migration all call the same
scan. A second private copy of the conditions is the defect the whole feature
exists to prevent.

### Contract and detection (`internal/wrapstate/heading_contract_test.go`, `internal/check/iteration_headings_test.go`)

| Test | What it proves |
|------|----------------|
| `TestScanFrameOrphans_MustFlag` / `_MustNotFlag` | every live orphan shape is caught, and the frame rule does **not** fire on a front-matter `---`, a fenced sample, an indented block, or an H3 sub-section |
| `TestHasDoubledPrefix_AndTheOracleBlindness` | the corruption the round-trip oracle is **structurally blind to**: `titleFromHeader` strips one prefix and `FormatIterationHeader` re-adds one, so a doubled heading is a fixed point and `IsCanonicalHeader` returns true for it |
| `TestScanHeadingDefects_*` | each defect reported **exactly once**, frame class winning any line two rules would claim — an auditor whose counts are inflated is an auditor that gets waved off |
| `TestCanonicalHeaderShapeMatchesTheWriter` | the prose shape quoted to humans is pinned to `FormatIterationHeader`'s real output |

### The reader seam (`internal/tools/get_iteration_orphan_test.go`)

`ParseEntries` now ends a body at the earlier of the next numbered heading and
the next frame orphan, and `heading_contract_test.go` proves that at the helper.
A helper test does not exercise the path the helper is installed on: it would
stay green if `vp_get_iteration` stopped calling the helper. These drive the
real `tool.Handler` and `ResolveURI` — via the existing `callGetIteration`
harness — over fixtures built with a new `orphanFrame` helper that composes the
writer's frame with a heading `FormatIterationHeader` would never emit. All five
are mutation-proven: neutralise the orphan clamp in `ParseEntries` and all five
go red.

| Test | What it proves |
|------|----------------|
| `TestGetIteration_OrphanNotGluedOntoPrecedingEntry` | the measured 110/111 case at the tool: `n=110` returns 110's narrative and neither the orphan's heading nor its body, `bytes` still describes the body advertised, and the orphan gains no phantom entry in the archive extent |
| `TestGetIteration_AddendumStaysItsOwnEntry` | the 108 case. The migration made the orphan a **second canonical 108**, so the seam serves two distinct matches in file order with individual `match_index`/`content_uri`, the first is not fused to the second — and the **last** 108 still stops at the frame orphan that follows it. The trailing orphan is load-bearing: without it the fixture only re-proves numbered-heading splitting, which predates the fix and would not go red |
| `TestGetIteration_UnnumberedOrphanIsNeverServed` | defense in depth for the next hand-appended wrap. An orphan with no recoverable number can reach a caller only as a tail on its predecessor or under a number it does not own; a sweep of every plausible `n` proves neither happens |
| `TestIterationResource_OrphanNotGluedOnResourcePath` | the **other** seam — `vibe-palace://iteration/{project}/{n}` (bare → `LastEntryByN`, indexed → `EntryByNMatch`), the path a host follows for a `body_deferred` row. Byte-identity with the inlined tool body is asserted, so one seam cannot clamp while the other does not |
| `TestGetIteration_RecentModeExcludesOrphan` | `recent` mode (`EntriesNewestFirst`) — the default archive read — carries no fused orphan on any row, and `bytes_inlined` equals the bodies actually delivered |

### Migration planner (`internal/wrapstate/heading_migration_test.go`)

The planner decides what may be repaired. Its one catastrophic failure mode is
**inventing an iteration number**, because once written a guess is
indistinguishable from a number the operator chose — the file is the only record.

| Test | What it proves |
|------|----------------|
| **`TestPlanRefusesOffListOrphan`** | **the anti-derivation pin.** An orphan is planted in a numeric gap, in a project the operator's table *does* cover, with an obvious max+1 available — and the plan must take none of those. Mutation-proven against the refusal branch |
| `TestPlanRefusesDriftedAuthorizedRow` | an authorized row whose recorded text no longer matches is refused, never stamped onto whatever occupies the line now |
| `TestPlanRefusesTitlelessNumberedHeading` | `## Iteration 147` is addressable but titleless; the number is recoverable, the title is not, and the two facts stay separable |
| `TestNoPlaceholderTitleIsEverWritten` | over a spread of adversarial headings × four projects: never `(untitled)`, never `<title>`. The check *suggests* a placeholder for a human to read; nothing writes one, because a placeholder on disk stops reading as a placeholder |
| `TestEveryWrittenNumberHasAProvenance` | every number written traces to one of exactly two origins — recovered from the line, or on the operator's table — and the header shape is integral (no `108.5`) |
| `TestAuthorizedTitleDerivation` | all nine operator rows derive their expected title from the **real table**, computed rather than re-asserted from a second hardcoded list |
| `TestAuthorizedTitleKeepsNonRedundantText` | the safety half: `## 2026-06-17 Wrap` assigned 111 must not become `Iteration 111 — 06-17 Wrap`. The leading-number rule matches the **assigned number literally**, never "leading digits" |
| `TestApplyChangesOnlyHeadingLines` | line-by-line: only heading lines differ, and only planned ones |
| `TestApplyRefusesAPlanFromDifferentContent` | a plan reused against changed content fails loudly instead of writing positionally |
| `TestMigrationPreservesFileOrderAndAdjacency` | sorting was **declined** — file order is the historical record. The two 177 addendums and the two 108s (one created by the migration) stay adjacent and in file order |
| `TestMigrationPreservesEntryBodies` | every pre-existing entry body byte-identical; the migrated orphan's own body byte-identical as its own entry |
| `TestFormatIterationHeaderIsNotZeroPadded` | zero-padding was **declined**; `## Iteration 7 — t`, not `## Iteration 00007 — t` |
| `TestPlanIsIdempotent` | a second run rewrites nothing and reports the rows `already-applied`, not drift — an alarm that cries wolf is one nobody reads on the day it is right |

### The command seam (`cmd/vp/cmd_migrate_iteration_headings_test.go`)

A helper test does not prove the path the helper is installed on. These drive
`cmdMigrateIterationHeadings().Run` and `cmdMigrateIterationsPreamble().Run`
against a git-clean temp vault whose fixtures place the operator's rows on
**exactly** their recorded line numbers, so the real table is exercised rather
than bypassed.

| Test | What it proves |
|------|----------------|
| `TestMigrateIterationHeadingsAppliesThroughTheRealSeam` | heading-only diff on disk, clean archives untouched and unmentioned, and a `.surface` stamp — the structural proof the write went through the vault layer rather than a hand-rolled `os.WriteFile` |
| `TestMigrateIterationHeadingsRefusesOffListOrphan` | the anti-derivation guard survives to the bytes on disk and to the operator's report |
| `TestMigrateIterationHeadingsRefusesDriftedRow` | drift refusal at the seam |
| `TestMigrateIterationHeadingsNeverWritesAPlaceholderTitle` | no `(untitled)`, no `<title>`, no `108.5` anywhere in any archive after an applying run |
| `TestMigrateIterationHeadingsRefusesDirtyTree` | `--apply` refuses unless `git checkout .` is a guaranteed rollback — this rewrites the one vault file with no second copy |
| `TestMigrateIterationHeadingsReportOnlyWritesNothing` | plan-first: the bare command writes nothing and stamps nothing |
| `TestMigrateIterationHeadingsSummaryCounts` | per-class and per-provenance counts, plus: an operator row naming a project absent from this vault is **named**, not silently skipped |
| `TestMigrateIterationsPreambleAtTheSeam` | the false `newest first` comment goes; rezbldr's **true** newest-first sentence survives; the archive body is undisturbed |

### The false preamble (`internal/wrapstate/iterations_preamble_test.go`)

Two archives opened with an HTML comment claiming `newest first`. The writer
appends at EOF, so the claim is not stale but **backwards** — a reader that
trusts it takes the oldest narrative believing it took the newest, and nothing
about the result looks wrong. Three other places in the vault say `newest first`
truthfully, which is why the match is an exact literal rather than a rule about
sentences.

| Test | What it proves |
|------|----------------|
| `TestRepairIterationsPreambleSparesTrueStatements` | rezbldr's scoped pre-rename sentence, the `git log` narratives, and a hand-edited variant are all left alone |
| `TestRepairIterationsPreambleRefusesOddShapes` | two copies, or a copy quoted inside a narrative as evidence, are refused |
| `TestAccuratePreambleSaysWhatIsTrue` | the replacement carries append-only, file-order, and `vp_get_iteration` — a comment nothing compiles needs a test to keep the next rewording from reintroducing an ordering claim |

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

### `internal/tools/surface_tools_test.go` — `vp_surface_check` MCP Tool

The `mcp-pull-parity-and-bashless-preflight` work exposed the surface verdict
as a read-only MCP tool. Covers `SurfaceCheckTool`:
`TestSurfaceCheckTool_NotMutating` (the tool carries no write-gate flag),
`_PassEmptyVault` / `_PassCompatibleStamp` (`status:"pass"`, `binary_surface`
reported), `_FailNewerVault` (an ahead vault → `status:"fail"` with remediation
`details`, `vault_surface`, and `stamp_dir`), and `_EmptyVaultPath` — the same
non-mutating probe that `TestIntegrationSurfaceCheck` drives through the full
JSON-RPC stack.

### `internal/check/selector_test.go` — The Shared Check Selector Registry

`checks-must-reach-every-agent-over-mcp` moved the `--check` selector registry
out of `cmd/vp` (`package main`, unimportable) down into `internal/check` as
`Producers` + `ProducerOrder` + `RunSelected(vaultRoot, filter)`, so the CLI and
the `vp_check` MCP tool dispatch one map instead of two that drift.

`TestProducerOrderCoversProducers` pins the invariant the default-all path rests
on — every producer named exactly once in the declared order, so none can be
added to the map yet silently never run. `TestRunSelectedDefaultsToEveryProducer`
covers the omitted/blank filter running the whole cheap suite instead of the old
`no checks selected` error (an MCP caller omitting an optional argument must not
get a tool error on the happy path). `TestRunSelectedDeclaredOrder` runs the
default sixteen times and asserts a constant order: Go randomizes map iteration
and this is the first iteration of that map anywhere in the tree.
`TestRunSelectedExplicitList` keeps the CLI's semantics — caller order wins,
names are trimmed, an unknown name errors *naming the offender*, and an
explicitly supplied list that names nothing runnable (`",,"`) still reports
`no checks selected`. `TestRunSelectedSkipsWithoutVault` pins the shared
degradation contract (no vault root → `Skip`, never a panic and never a bogus
`Pass`).

`TestRunSelectedIsPure` is the regression guard for the whole extraction: it
points the process cwd — via a real `.vibe-palace.toml` — at a decoy vault whose
resume carries a host-local plan ref, calls `RunSelected` with an **empty** root,
and requires `Skip`. Any reintroduced `os.Getwd` / `ResolveVaultPath` inside
`internal/check` reports the decoy's breach instead and fails the test. The same
call against the decoy root *does* report `Info`, so the test cannot pass
vacuously.

### `internal/tools/check_tool_test.go` — `vp_check` MCP Tool

`vp_check` exposes the named, embedder-free checks host-agnostically and
**subsumed** the per-check `vp_check_resume_refs` wrapper, whose clean-pass,
breach and empty-vault cases were ported here before that file was deleted
(`_ResumeRefsClean`, `_ResumeRefsBreaches` — which also pins that `details`
survives as an **array** rather than `ToJSON`'s folded string — and
`_EmptyVaultRoot`, one of only two tests anywhere dispatching a read-only check
tool against `Root == ""`).

`_ConstructorDoesNotTouchVault` is the guard for a failure with no obvious
connection to this tool: `registeredToolCount` and `tool_surface_golden_test.go`
both register the entire tool set against `storage.NewVault("")` purely to count
tools, so the constructor must stash the vault pointer and dereference nothing.
It asserts a complete tool against an empty root *and* a byte-identical vault
tree (path, size, SHA-256 per file) across construction;
`_RegistersAgainstEmptyVault` drives the real `RegisterAll` against that empty
root. `_DeterministicOrder` repeats the default dispatch and additionally
asserts the order **is** `ProducerOrder`, not an accident. `_NeverLoadsEmbedder`
mirrors the four `cmd_check_test.go` regressions by scanning the marshalled
result for `Embedder` — the selector path must never reach `check.Run`, which
would construct ONNX. `_UsesBoundVaultNotCwd` is the vault-binding regression:
the tool is bound to one temp vault while the process cwd resolves to a
different one, and the breach reported must come from the **bound** vault, since
`vp mcp` is long-lived and its cwd is the host's launch directory.
`_SelectorSubset` and `_UnknownNameErrors` cover the explicit-list path;
`TestCheckStatusProjection` pins all four verdict strings (`check.Status` is an
`int`, so a missed case ships bare integers to agents); and
`TestCheckAggregateIsAdvisoryWorstOf` pins the roll-up *and* records why it is
advisory — the checks legitimately disagree about an absent vault, so consumers
key off the per-check rows.

### `internal/tools/check_registry_test.go` — One Registry, Both Surfaces

`TestCheckSelectorsAreOneRegistry` is the anti-drift test the extraction exists
for: it compares the names the CLI accepts (enumerated from `check.Producers`)
against the names `vp_check` **advertises** (the `enum` decoded out of the tool's
own schema), requires `ProducerOrder` to cover the map exactly, and dispatches
every advertised name to prove it is real. Both sides are derived from the single
registry — a hand-written expected list would be the third copy of the concept
and would itself go stale.

`TestSurfaceSelectorOverlapIsIntentional` records a decision so it never reads as
an oversight: `vp_check` keeps the `surface` selector **and** `vp_surface_check`
stays registered. Filtering `surface` out would make the tool's name set diverge
from the CLI's — the drift the shared registry prevents, reappearing as a filter
over the shared map — while `vp_surface_check` is the preflight the surface gate
itself depends on and carries `binary_surface` / `vault_surface` / `stamp_dir`,
which the uniform envelope does not. The test asserts both are registered, that
`vp_check_resume_refs` is **not**, and that the two overlapping paths return the
same verdict.

### `internal/storage/vaultfetchage_test.go` — Network-Free Fetch Age

Covers `VaultFetchAge` (pure `os.Stat` of the tracking-ref / `FETCH_HEAD`
mtime, no network): old vs recent tracking ref, no-remote and
remote-but-no-tracking-ref (both `known=false`), and origin preference.

### `internal/tools/vault_staleness_test.go` — Bootstrap Staleness Field

Covers `computeVaultStaleness` and its wiring into `vp_bootstrap_context`:
old (>24h → `warn` + message), recent (no warn), unknown-fetch (never
fetched → warn), the no-warn boundary, and `TestBootstrapPopulatesVaultStaleness`
(bootstrap emits the `vault_staleness` field).

### `internal/check/json_test.go` — `vp check --json` Report Shape

Covers `ToJSON`: status-string projection (`pass`/`fail`/`skip`/`info`),
per-bucket summary tallies, `exit_code = 1` iff any check is `Fail`, the
folding of `Summary`+`Details` into one `detail` line, stable JSON field
names, and the deliberate omission of vibe-vault's `schema` field (no
context-schema counter exists in vibe-palace).

### `internal/check/resume_test.go` — resume.md Cap Detection

Covers `CheckResumeCaps` and its line-oriented table counter. `countSectionTableRows`
is table-driven over the shapes that actually occur in the wild: a missing
section, a section with no table, an **empty table** (header + delimiter, zero
data rows), a **header-only** run with no delimiter (not a GFM table → 0),
trailing prose after the last row, a `###` sub-heading *not* closing the
section, pipe-leading lines inside a **fenced code block** (skipped), cells
carrying **escaped pipes and code spans** (still one row), two tables in one
section (summed), a table running to EOF, and CRLF line endings.
`isTableDelimiter` is asserted against `|---|`, alignment colons, and the empty
`|   |   |` data row that must *not* be mistaken for a delimiter.

`resumeBreaches` is exercised **at** each cap (silent — 15 history rows and 12
completed rows are the allowed maximum, not a violation), **over** each cap
independently (size / Project History / Completed Plans, each producing exactly
one breach line), and over **all three at once**. `CheckResumeCaps` itself
covers: no `Projects/` directory, a project with **no resume.md** (skipped, not
counted, not flagged), a resume with **neither section** (Pass), a mixed vault
where three projects breach different caps and a fourth stays silent, dot- and
underscore-prefixed directories being ignored, and an unreadable `Projects/`.
`TestCheckResumeCaps_IsReadOnly` proves the central constraint: after a run that
flags a resume, its bytes, size and mtime are unchanged and no sibling file was
written.

### `cmd/vp` — Flag Wiring

`cmd_check_test.go` drives `vp check --json` end-to-end (JSON parse, binary
block, surface row present, `exit_code`/exit-code agreement) and asserts the
registered tool count is positive. It also covers the iter-158 `--check
NAME[,NAME...]` **selective-execution** flag (`vp-check-section-filter`):
`--check` parse (single + comma form), surface-only human output (asserts no
"Embedder" model-load line on stdout or stderr), surface-only `--json` (exactly
one `Surface` check, summary total 1, real `MCPSurfaceVersion` constant with
`tools:0` — never a false `surface:0` — and the commit echoed), the unknown-name
`ExitUser` path, the `isSurfaceOnly` normalization table, and two full-stack
tests driving the real `cmdCheck(info).Run([]string{...})` dispatch
(ParseFlags → runCheck → runSelectedChecks → ToJSON). Since
`checks-must-reach-every-agent-over-mcp` the producers these tests exercise are
`check.Producers` in `internal/check`, shared with the `vp_check` MCP tool;
`runSelectedChecks` is now only the cwd→vault-root resolution the CLI owns.

The `resume-caps` producer is covered against a seeded temp vault holding one
over-every-cap project: `--check resume-caps` human output (the `[info] Resume
caps` row plus all three breach strings; asserts no embedder load and no Surface
row), its `--json` projection (exactly one `Resume caps` check, `status: info`,
`summary.fail == 0`, **`exit_code 0`** — a cap breach warns and must never fail
the run), and the no-vault-resolved path degrading to `Skip`.
`cmd_version_test.go` covers `vp version --surface` (`surface: <N>`).

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
golden currently records `surface_version: 2`; derive the tool count and
mutating split from the golden itself — `jq '.tools | length'
internal/mcp/tool_surface.golden.json` and
`jq '[.tools[] | select(.mutating)] | length' …` — recorded counts rot.

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

### `cmd/vp` — Surface Merge Driver (REMOVED, iter 174)

The `vp-surface` git merge driver and its auto-installer were **deleted**. They
existed to resolve `*.surface` stamp conflicts to `max(ours, theirs)`, but the
byte-stable stamp (iter 168, `6d53b70`) removed the conflict class they served:
`WriteStamp` is a no-op at equal surface version and emits a surface-only stamp,
so routine writes never touch the file and two hosts bumping to the same version
produce byte-identical content that git merges cleanly on its own.

Deleted with the driver: `vault_merge_driver.go`, `vault_merge_driver_install.go`
and both test files; the `vp vault merge-driver` subcommand; the
`--no-install-merge-driver` opt-out on `vault pull`/`vault sync`; and the
`*.surface merge=vp-surface` line in the vault's `.gitattributes`.

Rationale and the two scenarios that decided it are recorded in
`tasks/done/mcp-pull-parity-and-bashless-preflight.md`.

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

### `internal/atomicfile` — Windows Rename Retry (coverage 86.5%)

The `windows-lock` job above flaked on a **rename**, not on the lock: one child
of sixteen died in `atomicfile.Write`'s `MoveFileEx` with `Access is denied`
while the flock had serialized every child correctly (ADR-003 amendment
2026-08-18). These tests cover the retry that absorbs it, and they all run on
**Linux** — the retry loop reaches `os.Rename`, the errno classifier and
`time.Sleep` through unexported package vars, so the policy is exercised without
a Windows runner.

| Test | What it proves |
|------|----------------|
| `TestRenameWithRetry_RetriesThenSucceeds` | a rename failing twice with a retryable error then succeeding returns nil, and the attempt **count** proves the loop actually re-ran rather than the first call quietly succeeding. |
| `TestRenameWithRetry_NoRetryOnPermanent` | a non-retryable error returns after exactly **one** attempt, unwrapped, so `errors.Is` still reaches the original — a permanent failure must not be sat on for 785ms. |
| `TestRenameWithRetry_ExhaustsBound` | an always-failing retryable error gives up after the documented bound and returns the last error wrapped with `%w`. |
| `TestRenameRetryBound` | pins the bound (7 attempts) **and** that total backoff stays under 1s, so the numbers in the source comment cannot silently drift. |
| `TestWrite_RetriesTransientRename` / `TestWrite_PropagatesNonRetryableRename` | prove `Write` is actually wired to the retry — both go red if the call reverts to a bare `os.Rename`. |
| `rename_other_test.go` (`!windows`) | the off-Windows classifier returns false for every error shape, including `syscall.Errno(5)`/`(32)`, so unix never sleeps on a doomed rename. |
| `rename_windows_test.go` (`windows`) | classifies real `ERROR_ACCESS_DENIED` / `ERROR_SHARING_VIOLATION` (including through the `*os.LinkError` `os.Rename` returns) as retryable and `fs.ErrNotExist` as not. Compiled by `GOOS=windows go vet`; **runs** in the `windows-lock` CI job. |

**Mutation-proven.** Reverting `Write` to a bare `os.Rename` reds
`TestWrite_RetriesTransientRename` (`rename attempts = 0, want 3`); making the
loop single-attempt reds `TestRenameWithRetry_RetriesThenSucceeds` and
`TestRenameWithRetry_ExhaustsBound` (`rename attempts = 1, want the documented
bound 7`). Both are behaviour failures, not build breaks.

---

## Resume-Editor Lost-Update Tests (`resume-md-lost-update`) — RETIRED

The six surgical `resume.md` editors (`vp_thread_insert`/`_replace`/`_remove`,
`vp_carried_add`/`_remove`/`_promote_to_task`) used to read-modify-write
`Projects/<slug>/resume.md` from `internal/tools` **without** taking the
`vaultlock` that `storage.WriteResume` takes — and an advisory lock only excludes
the writers that take it, so it bought nothing. That hole was closed by routing
them through the locked RMW combinator `storage.(*Vault).EditResume`; see the
*Amendment* in `doc/adr/003-vault-write-locking.md`.

**The six tools, `EditResume`, and every test listed in this section have since
been deleted.** No command template ever named the tools, so no agent could reach
them, and `vp_thread_insert` with `position: "top"` against a bullet-shaped
`## Open Threads` was itself a silent data-loss path. With the last caller gone,
`EditResume` went too — leaving `resume.md` with exactly **two** writers, both of
which take the lock **and** a compare-and-set guard: `storage.WriteResume`
(whole-file regeneration and migrations, behind `vp_update_resume`) and
`vaultfs.Edit` (the one-section-at-a-time path behind `vp_vault_edit`, which is
the **routine** wrap path — see *Full-Stack CAS Dispatch* below). CAS is a
strictly stronger contract than the lock alone, and it is now the primary
discipline rather than a backstop. The retired tests were:
`TestEditResume*` (`internal/storage/project_dirs_test.go`),
`TestThreadInsert_*` / `TestCarriedPromoteToTask_*` (`internal/tools`), and
`TestIntegration_ResumeEditorsConcurrentDispatch` (`internal/integration`).

The one test from this epic that survives is the `CreateTask` TOCTOU guard,
because `CreateTask` outlived the editor that was its second caller.

### `internal/storage` — `CreateTask` TOCTOU (`tasks_test.go`)

| Test | What it proves |
|------|----------------|
| `TestCreateTask_ConcurrentSameSlugExactlyOneWins` | the already-exists `os.Stat` now runs **inside** the per-path lock: of N concurrent creates of one slug, exactly one succeeds and the rest error — previously both passed the check and one silently overwrote the other |

---

## Blind-Overwrite CAS Tests (`default-cas-for-blind-overwrites`)

The lock (above) closed the *race* on `resume.md`; it could not close the
*stale read* — an agent that reads `resume.md`, thinks for minutes, and
blind-writes a full body computed from that stale snapshot is not racing
anyone, it is simply reverting whoever wrote in between. `storage.WriteResume`
is now **compare-and-set** and `expected_sha256` is **required** on
`vp_update_resume`; `""` means *assert-absent*, never *skip the check*, so
there is no blind whole-file resume overwrite path left. See the second
*Amendment* in `doc/adr/003-vault-write-locking.md`.

Two properties govern these tests. First, the digest is of the **RAW
pre-expansion bytes** — the resolver runs `expandScoped` (`{{PROJECT}}`,
`{{DATE}}`, …) on what it returns, and `{{DATE}}` is `time.Now()`, so a digest
of the *returned* string would match nothing on disk and would differ on every
call. Second, the empty guard is an assertion, not an escape hatch. All of
these are pure-unit / in-process (no ONNX) and run in `make test`.

### `internal/storage` — CAS Writer (`project_dirs_test.go`)

| Test | What it proves |
|------|----------------|
| `TestWriteResumeCAS` | the full matrix: a matching sha writes; a mismatched sha is refused with `*ResumeConflictError` (unwrapping to `vaultfs.ErrShaConflict`) carrying the actual current digest; `""` creates when absent; `""` is a **conflict** when the file exists (an omitted guard cannot degrade to last-writer-wins); a non-empty sha against an absent file conflicts with `Current: "(absent)"` |
| `TestWriteResumeRefusesStaleRead` | the motivating scenario end to end: A reads, B writes, A's write against its stale sha is refused and **B's content is still on disk** — the file is left untouched by the refusal |

### `internal/tools` — Digest Read Path and the Required Guard

| Test | What it proves |
|------|----------------|
| `TestGetResumeSha256MatchesDisk` (`context_query_tools_test.go`) | `vp_get_resume`'s `sha256` equals the sha256 of the bytes actually on disk — i.e. it is a usable CAS guard, not a decoration |
| `TestGetResumeSha256IsOfRawBytes` (`context_query_tools_test.go`) | the pin for the whole read-path contract: with a `{{DATE}}`/`{{PROJECT}}` placeholder in the file, the reported digest is of the **raw** bytes, not the expanded body — hashing post-expansion would match nothing on disk and would change every call |
| `TestGetResumeSha256EmptyWithoutProjectFile` (`context_query_tools_test.go`) | no project-tier `resume.md` → empty `sha256`, which round-trips into `WriteResume`'s assert-absent create |
| `TestGetWorkflowSha256MatchesDisk` (`context_query_tools_test.go`) | the same digest contract holds for `vp_get_workflow` |
| `TestUpdateResumeCASRoundTrip` (`context_query_tools_test.go`) | the sha handed out by `vp_get_resume` is accepted verbatim by `vp_update_resume` — read → write is a closed loop with no re-hashing on the caller |
| `TestUpdateResumeStaleShaIsMachineParseableError` (`context_query_tools_test.go`) | a stale write is refused with a **machine-parseable** conflict carrying the current digest, so a caller can rebuild its retry payload without a second read and without scraping the error string |
| `TestUpdateResumeSchemaRequiresExpectedSha` (`context_query_tools_test.go`) | `expected_sha256` is `required` in the registered tool schema: an **omitted** guard is rejected by schema validation before the handler runs (a `*mcp.ValidationError` naming the property), while a **present-but-empty** guard clears validation — `required` mandates presence, not non-emptiness, which is exactly the assert-absent case and why there is deliberately no `minLength`. This is what makes "no blind path" structural rather than advisory |
| `TestBootstrapResumeSha256MatchesDisk` (`context_tools_test.go`) | `vp_bootstrap_context`'s `resume_sha256` matches disk, so a session that bootstraps can wrap without a redundant `vp_get_resume` |
| `TestBootstrapShedResumeSha256IsOfFullBody` (`context_tools_test.go`) | when the ladder sheds the resume to its pinned zone, the digest is still of the **full** body, computed pre-shed — a reduced delivery still yields a guard that will actually match. Migrated from the deleted byte-axis `slim` path: the mechanism went, the invariant did not |
| `TestBootstrapCarriesNoDocumentBodyAtAnySize` (`context_tools_test.go`) | no document body is on the wire, asserted at both ends of the size range (a one-line resume and a 400-line one) plus a content check a renamed field would fail, with the handle and its digest still present. The size sweep is the point: a single fixture would pass an implementation that inlined small documents and dropped large ones, which is a size rule wearing an index's clothes. Replaces `TestBootstrapResumeIsNeverExcerptedByBytes`, whose subject — the resume arriving whole — Phase 3 removed |
| `TestHeadOfQueueIsGraphOrderNotListOrder` (`bootstrap_rank_test.go`) | the queue comes from the task GRAPH, not the directory listing. The fixture is built so the two orders DISAGREE — `a-blocked-task` sorts first by filename and must not appear at all, `z-in-progress` sorts last and must lead — because a fixture where they agree passes with the derivation replaced by `vault.ListTasks` |
| `TestSessionIndexRanksByRelevanceNotRecency` (`bootstrap_rank_test.go`) | the positive control for the ranker. The relevant session is written FIRST, making it the oldest, and must still come back at the top; a ranker that scored nothing and returned the newest rows passes every fixture where relevance and recency agree |
| `TestSessionIndexCarriesNoSummaryBody` (`bootstrap_rank_test.go`) | the session row keeps the metadata a reader chooses on and drops the narrative, and always carries the URI — dropping a body without a handle deletes the session from the agent's reach |
| `TestHeadOfQueueTermsDropNoiseWords` (`bootstrap_rank_test.go`) | the query keeps the words that discriminate and drops the ones that match every note. A query of "the"/"and"/"for" scores every candidate identically and hands the ordering back to the recency tie-break while the payload still reports itself as ranked |
| `TestBootstrapResumeSha256EmptyWithoutProjectFile` (`context_tools_test.go`) | absent resume → empty `resume_sha256`, feeding the assert-absent create |

### `internal/integration` — Full-Stack CAS Dispatch (`resume_cas_test.go`)

`TestIntegration_UpdateResumeStaleWriteRefused` drives the stale-write refusal
**end-to-end through real JSON-RPC `tools/call` dispatch** — `tools.RegisterAll`
on a real `mcp.Server`, the registry's schema validation and mutating-write gate,
then the handler. The storage- and schema-layer tests above would all still pass
if the tool mapped a conflict to a SUCCESS result or lost the error on the way
back through dispatch; this is the test that makes those failure modes visible.

| What it proves |
|----------------|
| Writer A reads `resume.md`, writer B lands a concurrent `vp_vault_edit`, and A's whole-file `vp_update_resume` against its now-stale sha is **refused** — B's edit survives. Mutation-proven: breaking the in-lock compare in `storage.WriteResume` makes it fail with *"the stale `vp_update_resume` SUCCEEDED; writer B's edit was silently reverted."* |
| `vp_vault_read` serves **RAW** bytes — the fixture carries a live `{{DATE}}` token and the test asserts it is still there, pinning the expanded-vs-raw distinction as an executable assertion rather than a comment |
| `vp_vault_edit` moves the sha, so the recovery path (re-read → recompose → resubmit) is exercised, not just asserted |

**Why the racer is `vp_vault_edit`.** The original version of this test used a
concurrent `vp_thread_insert`, and was deleted along with the six surgical
editors. `vp_vault_edit` is not an arbitrary substitute: it is the tool
`vp_update_resume` now points agents at, it takes the **same** per-path
`vaultlock` on `resume.md`, and it is itself CAS-capable — so it is the writer
that realistically races a whole-file rewrite in production. A second
`vp_update_resume` would only prove CAS against itself; a *different-writer*
racer is the shape the 179 hole actually had.

---

## Lazy Startup Tests

The MCP server no longer loads the ONNX model or reindexes the vault before it
answers `initialize`. Both used to happen inside `bootstrap()`, and both could
blow past the host's initialize timeout — leaving a live session with zero tools.
Three layers of test hold that down.

### `internal/embedder/lazy_test.go` — the `LazyEmbedder` proxy (unit, no ONNX)

| Test | What it proves |
|------|----------------|
| `TestLazyDefersConstruction` | The wrapped constructor has run **zero** times before first use, once after |
| `TestLazyConstructsOnceUnderConcurrency` | 32 concurrent `Embed` calls produce exactly one construction |
| `TestLazyCloseDoesNotConstruct` | Closing a never-used embedder does **not** load the model just to tear it down |
| `TestLazyCloseDelegatesAfterUse` | After construction, `Close` reaches the underlying embedder exactly once |
| `TestLazyDimensions` | `Dimensions()` forces construction and returns the model's real dimensionality — it cannot be known before the model exists, which is why it returns `(int, error)` |
| `TestLazyEmbedBatch` | `EmbedBatch` delegates after forcing construction |
| `TestLazyMemoizesConstructionError` | A **failed** load is memoized, not retried on every call — a hot loop of searches must never re-attempt a 90MB download |

### `internal/search/engine_test.go` — lazy index build and batching

See the search-engine table above: `TestSearchLazyBuildsIndex`,
`TestCrossProjectSearchLazyBuildsAllIndexes`, `TestLazyBuildRunsOnce`,
`TestSearchPropagatesBuildError`, `TestRebuildBatchesEmbeddings`,
`TestRebuildClearsStaleIndex`.

### `internal/integration/lazy_startup_test.go` — full-stack (no ONNX)

The unit tests above prove `LazyEmbedder` defers and `Engine` builds on demand.
Neither proves that nothing on the **server's** startup path forces the model
anyway, or that a cold `vp_search` actually reaches the disk. These two do,
through real JSON-RPC dispatch against the production tool surface
(`tools.RegisterAll` on a real `mcp.Server`).

| Test | What it proves |
|------|----------------|
| `TestIntegration_HandshakeDoesNotConstructEmbedder` | Registering the surface, answering `initialize`, and answering `tools/list` construct the embedder **zero** times. The count — not wall-clock time — is the observable: timing assertions are unworkable under CI's `-race`. Non-vacuity: the first `vp_search` drives the count to exactly 1, and a second search leaves it at 1 |
| `TestIntegration_ColdSearchBuildsIndexLazily` | `vp_search` (project-scoped) and `vp_search_cross_project` (all projects) return **real, correct hits** on projects that have never been rebuilt — drawers on disk and nothing else |

**Why the second test matters more than it looks.** With the eager reindex gone,
a `Search` against a project with no index used to take a `return nil, nil`
branch: an empty result **and a nil error**. Every agent on every fresh session
would have been told, plausibly and silently, that the vault is empty — a failure
invisible to any test that only asserts "no error". Mutation-proven: deleting the
`ensureIndex` / `ensureAllIndexes` calls from `Engine.Search` makes it fail with
*"cold vp_search returned 0 results on a never-rebuilt project — the lazy index
build is gone and every agent now sees an empty vault (raw: [])"*.

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

## Session `note_path` Tests (`capture-silent-failure-observability` §1a)

`vp_capture_session` returned `note_path: ""` — with `status: "ok"` — on **every
session ever captured**, for the entire life of the project. It passed every gate
for six months. The tests below exist because of *how* it did that, which is a
more useful lesson than the bug.

**Why the suite could not see it.** `storage.SessionMeta.NotePath` is yaml-tagged
`omitempty` and **nothing ever assigned it**, so the key was never written to a
note and the read-back unmarshalled a field that was not there. Two test-shaped
reasons this went unseen:

1. `TestWriteSessionHappyPath` asserted `Status`, `Project`, `SessionID` and
   `Iteration` — **every field except the broken one.**
2. The only test touching `NotePath` (`TestWriteSessionAllFields`) **seeded the
   value itself** — `NotePath: "/notes/session.md"` — and asserted it round-tripped.
   No production caller ever set that field. The field was **covered on paper and
   unassigned in fact**, and the value it enshrined was an *absolute* path, which
   is separately forbidden (see below).

**The invariant.** `note_path` is **writer-owned** and **vault-relative**.
`storage.SessionRelPath` is the single definition of where a session note lives;
`SessionFile` is its absolute form. `WriteSession` and `RewriteSession` both pin
`note_path` as an identity coordinate, so a caller-supplied value is **ignored**
and pre-fix notes are **backfilled** when the enrichment drain rewrites them.

It is vault-relative because the vault syncs to other machines: one project lives
at different absolute paths on different hosts (and in different subtrees on one
host), `note_path` is **persisted**, and it is exactly the form `vp_vault_read`
consumes. An absolute path here is a fact about the writing host and a lie
everywhere else.

| Test | What it proves |
|------|----------------|
| `TestWriteSessionNotePathNamesARealFile` (`internal/capture`) | The returned `note_path` is non-empty, **relative**, slash-separated, **resolves to a file that exists** under the vault root, and that file carries both the session ID and **its own `note_path` in frontmatter** |
| `TestWriteSessionAllFields` (`internal/storage`) | The writer **ignores** a caller-supplied `NotePath` (seeded with a bogus absolute path on purpose) and stamps the real vault-relative one |
| `TestRewriteSessionByteIdenticalFraming` (`internal/storage`) | `note_path` is pinned identically by **both** writers, so the drain's rewrite cannot drift a note's stated location away from its actual one |

**The assertion that has teeth is not "non-empty."** Mutation-proven: with the
frontmatter stamp removed, `TestWriteSessionNotePathNamesARealFile` still finds a
non-empty `note_path` — because `capture.WriteSession` **derives** the path rather
than reading it back, so the *return value* stays correct on its own. What fails is
the assertion that **the persisted frontmatter matches the returned string**. A
weaker test would have shipped a note whose own frontmatter disagreed with the
value handed to the agent.

**Deriving, not reading back, is the fix.** The old read-back existed solely to
recover the never-assigned field, and it discarded its own `readErr`. Deleting it
removes that silent site **by construction** — no read, no error to drop — which is
why there is no test for "the read-back error is logged": there is no read-back.

---

## Capture Failure + Idempotency Tests (`capture-silent-failure-observability` §1b)

§1a made capture's losses **visible**. §1b makes them **survivable** and then makes
them **fail**. Three invariants carry the whole design, and each has a test that was
**mutation-proven** — the defect was deliberately reintroduced and the test watched
to go red. A green test that has never been seen to fail is not evidence here; that
is the explicit lesson of the six-month `note_path` bug above.

### Invariant 1 — THE NOTE ALWAYS LANDS

`capture.WriteSession` **accumulates** failures and continues. There is exactly
**one** fatal error in the pipeline: `storage.WriteSessionRef`. Everything else
appends a `CaptureFailure` and the pipeline proceeds.

This is structural, not stylistic. Enrichment, archive resolve and friction scoring
**feed the frontmatter** and therefore run **before the note exists**, so an early
return on any of them writes **no note at all** — losing the session entirely, which
is strictly worse than losing the archive link it was trying to report. **There must
be no `return` between validation and the write.**

### Invariant 2 — THE KEY IDENTIFIES THE CAPTURE *ATTEMPT*, NOT THE SESSION

An agent captures once per **work unit** and a session holds several, so a
session-scoped key would make each work-unit capture **overwrite the last**. The
server **mints** a key, writes the note, and hands the key back — in the result on
success, in the error payload on failure. A **retry pushes it back** and updates in
place; **new work omits it** and gets a new note. Identity is *pushed, never derived*.

### Invariant 3 — AN UPDATE IS READ-MERGE-REWRITE, NEVER REWRITE-BLIND

`RewriteSession` marshals **exactly what it is handed**, and every `SessionMeta`
field is `omitempty` — so a field absent from the incoming meta is not "left alone",
it is **deleted**. `mergeCaptureMeta` therefore inherits what the new capture did not
recompute: `archive:`, `friction_breakdown` (whose **nil is load-bearing** — nil means
*never scored*, so dropping it does not merely lose data, it **lies**), and any
LLM-synthesized narrative. That last one is a **live race**: the enrichment drain
rewrites notes *asynchronously, after* the capture that created them.

| Test | What it proves |
|------|----------------|
| `TestWriteSessionNoteLandsDespitePreWriteFailures` (`internal/capture`) | A failing enricher **and** an unresolvable archive together still leave the note **on disk**, with both losses reported |
| `TestWriteSessionNoteLandsDespitePostWriteFailure` (`internal/capture`) | A post-write loss cannot retroactively make the capture fatal |
| `TestCaptureMintsAKeyAndReturnsIt` (`internal/capture`) | A keyless capture gets a key **minted, persisted, and returned** — the only way a retry can name the attempt it is retrying |
| `TestCaptureRetryWithSameKeyUpdatesInPlace` (`internal/capture`) | A retry with the same key rewrites the **same note** — **one** note on disk, not two |
| `TestCaptureWithoutKeyMintsANewNote` (`internal/capture`) | Distinct work units are **not** merged (the failure mode of the superseded session-key design) |
| `TestCaptureRetryPreservesEnrichmentArchiveAndFriction` (`internal/capture`) | A re-capture preserves the drain's LLM narrative, the `archive:` link, and the `friction_breakdown` |
| `TestConcurrentCapturesDoNotClobber` (`internal/capture`) | 8 concurrent captures produce 8 notes — the **pre-existing** unlocked-`NextIteration` race is closed |
| `TestCaptureSessionFailsHardOnLoss` (`internal/tools`) | The MCP tool returns **`isError`**, never `ok`, and the error carries a **JSON payload** naming `note_path`, `session_key`, what was lost, and the remedy |
| `TestCaptureSessionRetryAfterFailureDoesNotDuplicate` (`internal/tools`) | The full agent loop — fail, read the key out of the error, retry — leaves **one** note |
| `TestCaptureSessionClaimSurvivesTheErrorPath` (`internal/tools`) | The claim sentinel is written **before** the error is raised |
| `TestRun_ClaimWrittenDespitePeripheralLoss` (`internal/hook`) | The claim survives a peripheral loss on the hook path too |
| `TestRun_CaptureFailureDoesNotError` (`internal/hook`) | A capture failure never becomes a hook **run** error |
| `TestRunHookNeverExitsBlockingOnCaptureFailure` (`cmd/vp`) | `vp hook` **never exits 2** on a capture failure, and logs an `ERROR` to `vp.log` instead |

### Why the claim sentinel is written on the SUCCESS path

The claim asserts *"this session has a note"*, which stays **true** when a peripheral
stage was lost. Gating it on a clean run — the obvious-looking tidy — would leave
every hard-failing capture unclaimed, so the next hook event captures the session
**again**: a duplicate note per turn, forever, over a missing archive link. It is
correspondingly **withheld** when no note was written, so a failed capture stays
retryable rather than permanently marked done.

### Why the hook never exits 2

`cli.ExitSystem` is `2`, and `2` is Claude Code's **reserved blocking-error code**.
The hook fires on `Stop` — **once per assistant turn** — so a deterministically
failing capture that exits 2 would block the first turn of every session and feed its
own stderr back into the model. A loop. At `SessionEnd` the same code blocks nothing
and is invisible. The alarm is the durable log, not the exit code.

### Why failing hard REQUIRED idempotency first

An `isError` on a call that has **already written its note** is an invitation to
retry. Without a key, that retry writes a **second** note — turning one lost archive
link into two conflicting session records. That is why Invariant 2 landed before the
MCP path was allowed to fail.

---

## Inline Transcript Archive Tests (`native-capture-session` + `capture-defaults-for-hookless-hosts`)

Hook-less hosts get a transcript archive at capture time when a non-empty
`transcript` is supplied. Handshake-derived grok/xai/zed **auto-enable**
inline archive even if `archive_transcript` is omitted; the flag remains an
explicit force for other empty-id hosts and a no-op on Claude Code (see
`doc/ARCHITECTURE.md`). Coverage spans the three layers the feature touches:

| Test | What it proves |
|------|----------------|
| `TestInlineAdapter_*` (`internal/archive`, 6 tests) | The `inline` adapter round-trips caller-supplied bytes **verbatim** into a manifest + compressed archive pair; empty `SourceContent` is a clear hard error, never a fallback; re-`Create` with the same id is idempotent; changed content preserves the prior manifest; the temp file is cleaned up |
| `TestCaptureKeySource*` / `TestInlineProvenanceConstantValues` (`internal/capture`) | A caller-vouched `SessionKeySource` is recorded **verbatim** (a handler-minted key never masquerades as caller-supplied); leaving it unset keeps today's caller/minted inference; the provenance constant values are pinned |
| `TestCaptureSessionInlineArchive*` (`internal/tools`) | Explicit force: note + archive land **born-linked** (`archive_session_id_source: inline`, `session_key_source: minted`); retry converges; Claude/derivable host is a **no-op**; failed archive is a `transcript_archive` incomplete-capture entry and the note still lands |
| `TestCaptureSessionInlineArchiveAutoOnDerivedGrok` / `…AutoOffUnknownHost` / `…ExplicitTrueAnyHost` | **Defaults:** derived grok + transcript auto-archives without the flag; unknown host does not auto-on; explicit `true` still forces under empty id + transcript |

---

## `vp_health` Tests (`capture-silent-failure-observability` §1c)

`vp_health` is the instrument that reads the log the rest of this task writes to. It
had five defects, and the fifth is the one that made the other four moot.

### Invariant 1 — "UNKNOWN" IS NOT "HEALTHY"

A tool that cannot **read** the log must not report that the system is **fine**. A
missing log returned `status: "healthy"`; an unopenable one did the same and did not
even set `scan_error`. And "log missing" is the *normal* state on a fresh host and the
*permanent* state for any process that never initialized the logger — which is exactly
the condition this task was filed to make visible.

**The old test asserted the bug.** `TestHealthToolHealthy` checked
`status == "healthy"` against a vault with **no log file** and went green on it — the
disease sitting in the test suite of the tool built to detect it. It is now
`TestHealthToolUnknownWhenItCannotReadTheLog`, asserting the opposite.

### Invariant 2 — `status` IS DERIVED FROM EVERY IN-WINDOW ENTRY, NOT THE DISPLAY LIST

`status` was computed by looping over `RecentWarns` — the **capped** list. So an
`ERROR` past the cap was tallied into `warn_counts` and then **never set
`status: "errors"`**: the tool reported `"warnings"` while holding an `ERROR` it had
counted itself, contradicting itself **inside its own payload**. Fixing the tail alone
does *not* fix this; it only changes *which* errors get missed.

Related: `recent_warns` kept the **oldest** N, because it appended while
`len < limit` while scanning **forward** through an append-only file. The old test
asserted only the *length* of that list, never *which* entries — a test named for
recency that never tested recency.

### Invariant 3 — A BOUNDED TAIL, NEVER A SCAN

`vplog.Summarize` reads only the last `vplog.TailBytes`. This is a hard constraint:
it runs on the `vp_bootstrap_context` path — the hottest call in the system, which
iteration 190 spent a session taking from ~0.4 s to 0.012 s — and the log is capped at
**8 MiB**. Scanning it end to end on every session start would hand that win back.
`Truncated` reports when the view is partial, so a partial count can never read as an
authoritative one.

### Invariant 4 — PUSHED, NOT PULLED; SILENT WHEN HEALTHY

**Nothing ever called `vp_health`** — not a template, not a command, not a skill. It
was itself a member of the class it was built to detect: *capability built, nothing
invokes it*. *"Who calls it?"* was the wrong question, because every pull-based answer
is a rule in prose, and `vp check` is this project's standing proof that prose reaches
nobody.

So health **rides in the `vp_bootstrap_context` payload** every session already loads,
and is **absent entirely when healthy** — an always-on green light is the soft signal
agents learn to skim past, the same reasoning that killed the `partial` capture tier.
The field appearing *at all* means something needs looking at.

| Test | What it proves |
|------|----------------|
| `TestSummarizeUnknownNotHealthyOnMissingLog` (`internal/vplog`) | A log that cannot be read is **`unknown`**, never `healthy`, and `scan_error` says why |
| `TestSummarizeStatusOverAllEntriesNotTheDisplayCap` (`internal/vplog`) | An `ERROR` **outside** the display cap still sets `status: "errors"` |
| `TestSummarizeRecentWarnsIsNewestN` (`internal/vplog`) | `recent_warns` holds the **newest** N, not the oldest |
| `TestSummarizeReadsABoundedTailNotTheWholeLog` (`internal/vplog`) | A warning buried above the tail window is **not** seen, and `Truncated` says so |
| `TestSummarizeDropsThePartialFirstLine` (`internal/vplog`) | The line fragment a mid-file seek lands in is never parsed as a record |
| `TestHealthToolUnknownWhenItCannotReadTheLog` (`internal/tools`) | Same, through the MCP tool (replaces the test that asserted the bug) |
| `TestHealthStatusSeesErrorsBeyondTheDisplayCap` (`internal/tools`) | Same, through the tool |
| `TestBootstrapPushesHealthWhenDegraded` (`internal/tools`) | A degraded vp **reaches the agent** without the agent asking |
| `TestBootstrapPushesHealthWhenBlind` (`internal/tools`) | A **blind** vp reaches the agent too — blindness is not health |
| `TestBootstrapIsSilentWhenHealthy` (`internal/tools`) | A healthy vp says **nothing** |

### The pre-existing bug §1c uncovered: alerts were dropped under token pressure

The token-budget truncation shed the command list and then **re-rendered**
`post_bootstrap_instructions`. That re-render was a blind **assignment**, which threw
away every alert appended before it — friction, vault-staleness, and health.

So the payload discarded its warnings **exactly when it was too big to fit**, which is
when a project is busiest and the warnings matter most. Alerts are now collected
separately and **re-composed**, so re-rendering the directive cannot erase them
(`composeDirective`).

`TestBootstrapAlertsSurviveTokenTruncation` was **deleted** at iteration 313, not
renamed. It drove the handler with `{"max_tokens":1}` to force the shed path — a
parameter Phase 2 removed. An unknown JSON key is ignored, so the call kept
succeeding and the test kept passing while exercising the same path as
`TestBootstrapPushesHealthWhenDegraded`. A test naming a mechanism the binary no
longer has reads as coverage of that mechanism and is worth less than no test.
The surviving property is covered by `TestBootstrapPushesHealthWhenDegraded` and
`TestDirectiveCutKeepsAlertsAndLosesAnnouncement`.

---

## Source Audit — the gate that would have caught `note_path` in five minutes

`internal/sourceaudit` is a static analysis **of this repository's own source**, run as
a test. It exists because of a measured fact: **every serious defect found in
iterations 191–201 was caught by looking at a real artifact, and NOT ONE was caught by
a test, a check, or a code review** — and two of them were *mechanically detectable*
and hid for months anyway.

### The two findings, and why they are the same bug

| Kind | What it finds | The defect that earned it |
|------|---------------|---------------------------|
| `write-only-field` | a yaml/json-tagged field on a struct the code **constructs**, that nothing ever assigns | **`note_path`** — tagged, serialized, never assigned; `vp_capture_session` reported `note_path: ""` for the life of the project. **Six months. Every test green.** |
| `uninvoked` | a function declared in non-test code and called from **nowhere** in non-test code | **"capability built, nothing invokes it"** — the Zed archive adapter, `vp check`, `vp_health`, and the claim sentinel on the MCP path |

Both are one shape: **a symbol nothing ever produces.** A field only ever read. A
function only ever defined. The compiler is happy, the tests are green, the feature is
inert.

### Why it is a TEST and not a `vp check` item

`vp check` runs from an **installed binary against a vault**, on a host that may have
no source tree at all — it structurally cannot do this. A test can, it runs on **every
change** instead of once a week, and it cannot be forgotten.

### 🔴 The "constructs" qualifier is the whole trick

A naive *"tagged field nobody assigns"* flags every **deserialized** struct in the tree
— every MCP `*Params`, `hook.Payload`, the Zed DB rows, LLM responses. Those are
populated by `Unmarshal` through reflection, so **no** field is ever hand-assigned.
That is 46 false positives, and **a noisy gate is a disabled gate** — which is how
`note_path` survived six months in the first place.

So a struct is audited **only when the code constructs it** (at least one keyed
composite literal of that type exists). Input structs drop out entirely, and what
remains is exactly the `note_path` shape: **a record the code builds, with one field it
forgot.**

### 🔴 The baseline can only SHRINK — this is the ratchet

The tree did not start clean, so known findings live in `baseline.json`. The gate fails
on **two** conditions, and the second is the point:

1. a **new** finding — new debt; and
2. a **baseline entry that is no longer a finding** — fixed debt, still recorded.

Without (2) the baseline rots into a lie: you fix something, the list keeps claiming it
is broken, and the list stops meaning anything. With it, the list **can only shrink** —
no fix can be quietly un-recorded. Regenerate with
`go test ./internal/sourceaudit -update-baseline`; every entry must carry a **reason**.

### The analyzer has its own mutation tests, and it needs them

While it was being built, **two separate bugs made it report ZERO findings on a tree
full of defects** — a walk that skipped its own root, then a type resolver that could
not see `pkg.Type{…}` literals. Both times it produced a confident clean bill of
health. *That is the disease, inside the tool built to cure it.*

| Test | What it proves |
|------|----------------|
| `TestFindsAPlantedWriteOnlyField` | it **finds** a planted `note_path`-shaped bug — an analyzer that cannot prove this is worth nothing |
| `TestFindsAPlantedUninvokedFunc` | it finds a function nothing calls, and does **not** flag one that is called |
| `TestIgnoresDeserializedStructs` | it does **not** fire on `Unmarshal`-populated structs (the false-positive flood that would disable it) |
| `TestFuncValuesCountAsInvoked` | a handler passed as a **value** is invoked through that value — else every MCP handler looks dead |
| `TestFuncSeamInValueSpecCountsAsInvoked` | a package-level seam (`var Impl = realImpl`) does **not** make `realImpl` look dead — **the false positive the gate actually shipped with** |
| `TestStdlibDispatchedMethodsAreExempt` | `Unwrap` on an error type is not flagged: `errors.Is` dispatches it from **outside** the tree |
| `TestInTreeInterfaceMethodNobodyCallsIsStillFlagged` | an **in-tree** interface method no driver calls **is still flagged** — the exemption must not overreach |
| `TestBaselineRegenPreservesReasons` | regeneration keeps a survivor's reason — else the first regen erases every triage |
| `TestBaselineCanOnlyShrink` | a **fixed** baseline entry fails the build |
| `TestSourceAuditGate` | the gate itself, over this repo |

### 🔴 Interface dispatch: the one exemption, and where the line is drawn

A method reached only through an interface has no direct call site, so it looks
uninvoked. The tempting fix — **exempt every method that satisfies an interface** — is
**wrong**, and triaging the first baseline proved it: six `reconcile.*.Requires()`
methods satisfy `reconcile.Reconciler`, an interface the tree genuinely uses, and they
declare a dependency graph that the driver loop re-derives by hand in a `switch` and
**never once reads**. The blanket rule would have hidden all six. An interface method
nobody dispatches on is not noise; it is this project's signature bug wearing a contract.

Exempting on *"tests call it"* is equally wrong, and for the same reason: **every dead
functional option in the tree has a passing unit test.** That rule turns the gate from
*"capability built, nothing invokes it"* into *"capability built, and its own unit test
invokes it, so we're fine."*

So the exemption keys on **where the dispatcher lives** (`stdlibContracts`):

- **Out of tree** — `log/slog` calls `Handle`, `errors.Is` calls `Unwrap`. No in-tree
  call site can *ever* exist, so the finding can never be actioned. **Exempt.**
- **In tree** — keep flagging. This needs no special case at all: calls are tracked by
  bare name, so the moment any code calls `x.Requires()`, every implementation of it
  goes quiet on its own.

### Honest limits

It is **syntactic** (go/ast, stdlib only, no type information), which biases it toward
**false negatives**. Field names are not unique across structs, so `Foo.Name = x` counts
as an assignment to every `Name` in the repo — which is why it found **4** of
`SessionMeta`'s 8 dead fields and not 8.

**It is NOT immune to false positives, and this doc used to claim it was.** It shipped
one: `templates.realEmbeddedSHA` runs in production on every `vp init`, and the analyzer
called it dead because it never visited `*ast.ValueSpec` — so the package-level seam
`var EmbeddedSHA = realEmbeddedSHA` marked nothing as used. **A gate that reports live
code as dead is how you get a disabled gate without anyone deciding to disable it.** That
is the noisy-gate failure arriving from the opposite direction, and it is why
`TestFuncSeamInValueSpecCountsAsInvoked` exists.

**Missing a real bug is bad; crying wolf is worse**, because a noisy gate gets switched
off and then catches nothing at all.

## Vault Audit — the instrument that asks the artifact so a human need not remember to

`internal/vaultaudit` is the runtime sibling of the source audit: where `sourceaudit`
checks the repo's own source, the vault audit checks the **live vault** against design
intent (five dimensions; see ADR-007). Its tests carry the same doctrine — *an auditor
validated only by its own logic is the thing this epic exists to prevent* — so the
central test is a **mutation test**, exactly as in `sourceaudit`.

| Test (file) | What it proves |
|------|----------------|
| `TestArchiveRoundTrip_FindsKnownDefects` (`archive_test.go`) | hand a vault three KNOWN defects (a stranded manifest, a dangling back-link, a readable-but-empty tree) and assert it finds exactly those — **an auditor that cannot fail issues a clean bill of health it never earned** |
| `TestArchiveRoundTrip_CleanVaultIsClean` (`archive_test.go`) | a fully-linked vault produces no findings — trustworthy only *because* the mutation test above can fail |
| `TestRun_FixingTheBugForcesTheBaselineToShrink` (`archive_test.go`) | linking a stranded-but-accepted manifest turns its baseline entry STALE — the ratchet, exercised end to end |
| `TestRun_LiveVaultCanary` (`archive_test.go`) | the audit runs against the **real vault** in `make test` — the discipline the whole epic rests on |
| `dimensions_test.go` | project-tree-coherence, KG-portability, resume-discipline, iteration-headings each find their planted defect and pass a clean fixture |
| `baseline_test.go` | `(Dimension, Artifact)` identity; an accepted pair is `accepted` not `new`; a **fixed** accepted entry goes **STALE and FAILS** (the may-only-shrink ratchet); `Regenerate` preserves reasons |
| `staleness_test.go` | the nag is **silent when fresh** and trips on churn/age — a missing anchor must read as *unknown*, never `0` (the 209 `ABSENCE IS NOT A VALUE` bug) |

### Archive backfill — the remediation path, tested against the artifact

`backfill.go` and `storage.BackfillArchiveLink` are covered in `backfill_test.go`
(both packages) and asserted against the **written files**, not the tool's own return
value — the bug that started the whole epic survived every test that trusted the result
struct.

| Test | What it proves |
|------|----------------|
| `TestBackfillArchiveLink_*` (`internal/storage`) | stamps a caller-keyed note (provenance `backfilled`); idempotent re-run writes nothing; skips a **minted**-key coincidence; **refuses an identity conflict** loudly; never re-points an already-linked note; canonical prefers the non-stub |
| `TestBackfillCandidates_TargetIsNewestStranded` | the multi-manifest case (H1): a session with two stranded manifests targets the **newest**; the older stays stranded by design |
| `TestBackfillCandidates_UnreadableTranscriptsDirIsError` | an unreadable dir is an **error, not "no candidates"** — `filepath.Glob` swallows permission errors, so the scan `os.ReadDir`-probes first |
| `TestApplyBackfill_EndToEnd` | note→manifest→note round trip verified by **reading both files off disk** — frontmatter carries `archive_session_id`, `…_source: backfilled`, `archive:`; the manifest back-links the note |
| `TestArchiveRoundTrip_AnnotatesRecoverable` | the audit annotation touches the finding **message only**; `Artifact` is byte-stable so the accepted baseline cannot churn |

### ⚠ Known gap: the `write-only-field` charter and its heuristic disagree

The rule fired on `skills.SkillFrontmatter`, which **is** `Unmarshal`-populated and by
the *"only structs the code constructs"* qualifier above should have been **suppressed** —
a single composite literal in `internal/shims/target.go` enrolled it. **It found a true
bug anyway, but for the wrong reason**, which means the next struct in that shape is a
coin flip. Close this before it matters.

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
