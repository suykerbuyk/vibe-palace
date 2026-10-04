# Testing Strategy

**Last updated:** 2026-09-28

This document describes the testing strategy for vibe-palace, including
the unit test infrastructure, the integration test architecture, and the
ONNX model caching system that makes real-embedding tests practical.

As of v8.2.0 the suite runs **~5309 tests** across 59 packages, including
**153 integration tests** (the ONNX/cross-layer tests `make integration`
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
**Runs:** the `build`, `fmt-check` and `vet` prerequisites, then
`go test -race -short -cover ./...`, then `make live-canary` (see
*Live-vault canaries*)
**Duration:** not seconds; the larger packages (`internal/storage`,
`cmd/vp`, `internal/integration`) dominate, and `-race` slows them further
**ONNX required:** No

Unit tests exercise individual functions and types within a single package.
They use `embedder.MockEmbedder` — a deterministic embedder that produces
L2-normalized vectors derived from SHA-256 hashes of the input text. These
vectors have no semantic meaning (similar texts do NOT produce similar
vectors), but they are stable and reproducible, which is sufficient for
testing index mechanics, storage round-trips, and tool handler logic.

The coverage target is 80%+ per package. It is a target, not a guarantee:
several packages sit below it today. `make cover` writes the short-mode
report to `coverage.html`. Tests that require ONNX embeddings call `t.Skip()`
in short mode.

**`-short` skips the derived-gate source-audit rule.** The module-deriving
tests of the type-checked derived-gate rule in `internal/sourceaudit` call
`skipUnlessFullSuite` (`internal/sourceaudit/derived_gate_test.go`) and skip
under `-short`, so `make test` does **not** run the rule. `make source-audit`
(the whole package, no `-short`, no `-race`) runs it, and so do the CI
`source-audit` and `ubuntu26-canary` jobs that invoke it. The manual
`make test-full` and `make cover-full` pass no `-short`, so they run it too.
See *Source Audit*.

### Tier 2: Integration Tests (ONNX)

**Command:** `make integration`
**Flags:** `go test -count=1 -run TestIntegration -v ./...`
**Duration:** depends on the model cache; a cold cache adds the model download
**ONNX required:** Yes

Integration tests exercise cross-layer interactions with real ONNX
embeddings. They verify that semantic search actually returns semantically
relevant results, that the full capture-to-search pipeline works, and that
config values propagate correctly across layers.

All integration test function names start with `TestIntegration` so
`make integration` discovers them via the `-run` flag.

**`make model-test`** is the real-model tier outside `internal/integration`:
`go test -count=1 -timeout 10m -p 1` over every package with a test file that calls
the real ONNX constructor (`embedder.NewONNX(`), with neither `-short` nor
`-race`. The package list is **derived** by a grep in the Makefile
(`MODEL_TEST_PKGS`), never hand-listed, and an empty derivation fails the
target closed. Today it yields `internal/capture`, `internal/check`,
`internal/embedder` and `internal/search`: `TestCheckEmbedder`, the `TestONNX*`
tests, and the `TestIntegration*` tests of `internal/search` and
`internal/capture` (the derived packages' fast tests run again too, which is
cheap). The pattern needs the `LPAREN := (` indirection: make counts a literal
`(` inside `$(shell …)` and a backslash does not escape it; dropping the paren
instead would match comments that merely name `NewONNX` and pull `cmd/vp` and
`internal/tools` into a non-`-short` run. It is the CI `model` job's only step.

### Tier 3: Full Suite

**Command:** `make test-full`
**Runs:** the `build` and `vet` prerequisites, then
`go test -count=1 -cover -v ./...`
**Duration:** longer than Tier 1 (no `-short`, so every integration and
real-model test runs)
**ONNX required:** Yes

Runs everything: unit tests plus all integration tests. It is a manual
target: no CI job invokes it (see *CI job shape*).

---

## Live-vault canaries

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
|---|---|
| `TestLiveVaultHasNoPhantomRelations` | `parseTaskMeta` regressing to a whole-file scan and reading a task's **body** as a real `Parent`/`Depends` — several real task files discuss the header syntax in prose and inside fences |
| `TestLiveVaultGraphIsCleanAndTerminates` | A structural lie in the real backlog (cycle / dangling ref / retired parent with live children) — and, under a bounded timeout, a cycle being **walked instead of detected** |
| `TestLiveVaultAmendNeverDisturbsTheHeaderBlock` | `vp_manage_task amend` splicing a body section in a way that moves a task's status, priority, title or edges. Run over **every real section of every real task file** — the guarantee is structural, so it is asserted against the corpus rather than a body written to make it pass |
| `TestLiveVaultAmendNeverMatchesAFencedHeading` | `sectionBounds` regressing to a naive scan and splicing **into a code fence**. Not hypothetical: **22 H2 headings in this project's own task files exist only as fenced sample text** — including the `## Decision` quoted by the task that specified `amend` |
| `TestLiveVaultAmendIsIdempotentOnRealBodies` | A retried amend **duplicating** a section on a real body instead of converging — the failure a crash-and-retry would produce |
| `TestLiveVaultRetitleNeverDisturbsAnythingElse` | `replaceTitleLine` (whole-file, first-wins, **fence-unaware**) rewriting an H1-shaped line that is not the title. Safe only because `CreateTask` always writes `# Title` first and `validateTaskBody` refuses an unfenced H1 in a body — **that is an invariant about the CORPUS, not the function**, so only the corpus can check it. Asserts exactly one line changed per file |
| `TestLiveVaultSessionNotesAllRoundTrip` (`internal/storage/session_yaml_safety_test.go`) | A session note at rest that cannot be re-emitted, so `vp vault tidy` and every `RewriteSession` caller would refuse it. Unparseable notes are counted and skipped, not failed on |
| `TestLiveVaultSessionIndexIsNotEmpty` (`internal/storage/session_skips_test.go`) | The session index coming back empty on the real corpus. Asserts no count: the index is non-empty and whatever is skipped is named |
| `TestLiveVaultKnowledgeGraphIsReadable` (`internal/storage/kg_record_skip_test.go`) | A project with an entities file yielding an empty listing. Asserts no count; whatever is skipped is named |
| `TestBootstrapLiveVaultStillRestoresASession` | A live restart coming back unusable. Asserts the payload an agent actually receives against the real vault: the handles (`resume_uri`, `workflow_uri`, `resume_sha256`) present; **no document body** — neither a `resume` nor a `workflow` key, and no distinctive line of the live resume anywhere in the marshalled payload; `head_of_queue` non-empty when a backlog exists, every row and every session row carrying its URI; `ranking` present and naming the `structural` ranker; the wire carrying no `budget` / `shed_core` / `max_tokens` / pinned-zone banner; and `complete` last on the wire. **It asserts no size at all** — a ceiling reintroduced here would re-create the disease PRD §1.10 removes. Its body assertion was INVERTED at iteration 313: through Phase 2 it required the bodies to be present, which was that phase's gate; Phase 3 made the payload an index, and the assertion was replaced in the same commit rather than left to lie. Only the real vault has a task graph and a session corpus large enough for assembly to go wrong on |

Vault resolution differs by file: the `internal/taskgraph` and `tasks_live_vault_test.go` canaries use
`storage.OpenVaultGlobal()`; the bootstrap canary honours `VP_LIVE_VAULT` first; the three session and
KG canaries use `liveVaultRoot` (`internal/storage/session_yaml_safety_test.go`), which reads
`VP_LIVE_VAULT` and otherwise falls back to `~/vibe-palace-vault`.

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

`make test` runs it last, so the uncached run happens on every ordinary test invocation. **Any new
live-vault canary belongs in that target's `-run` pattern** — adding one and leaving it to `go test
./...` re-opens the hole. As of v8.2.0 the Makefile does not follow this rule: `live-canary` runs only
`TestBootstrapLiveVaultStillRestoresASession`, and the `TestLiveVault*` canaries in the table above
run only through the cached `go test ./...`. List them with
`grep -rn '^func TestLiveVault' --include='*_test.go' internal cmd`.

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
| `TestBootstrapTruncatedPrefixIsDetectable` | The headline. A real payload cut where the index begins (at `"head_of_queue":`) is **detectable from inside the truncated channel**: `complete` is absent from the prefix (and present in the whole document), while `resume_uri`, `workflow_uri`, `resume_sha256`, `active_task_count`, `ranking` and `post_bootstrap_instructions` all survive. It also asserts the prefix is *not* valid JSON, so a future payload that shrinks under the cap cannot turn the test green by removing the truncation it measures |
| `TestBootstrapInstrumentsPrecedeBulk` | The order by **byte offset** — the only property a cut respects. Every instrument and recovery handle (`resume_uri`, `workflow_uri`, `resume_sha256`, `active_task_count`, `ranking`) appears before the index (`head_of_queue`, `recent_sessions`) |
| `TestBootstrapCompleteSentinelAlwaysEmitted` | The sentinel's three properties: no `omitempty` (a zero-value result still spells out `"complete":false`, so absence cannot be confused with a false value), **structurally last** in the struct via reflection (the guard against a future field being appended after it), and last on the wire on real marshalled payloads |

The bootstrap payload no longer inlines the resume or workflow bodies, so an honest payload does not
reach a host's fixed cap and cutting it at a fixed offset would truncate nothing. The bootstrap tests
therefore cut at the instrument/index boundary (`cutBootstrapAtBulk`), which proves the same property
— what survives when a host keeps only a prefix — at every payload size. Since 310 vp reduces
nothing, so the cut is the host's alone.

**The 19,968-byte figure is a specimen, not a constant of the system** — one host on one day
(three Grok results of 60.3 KB, 53.4 KB and 32.7 KB each cut at exactly 19.5 KiB, a *flat* cap rather
than a ratio). The surface tests below still cut at that offset and assert what survives it; any
offset landing inside the bulk proves the same property.

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

Three surfaces teach the payload's delivery state, and `doctrineSites` collects all three:
`internal/templates/templates/commands/restart.md` (Step 2), the `vp_bootstrap_context` tool
description, and the Grok `/vpc` hub shim (`internal/shims`). As of v8.2.0 the one remaining test
asserts only the `restart.md` site. The template reaches only hosts that
ran `/vpc-restart`; the description reaches every agent on every host; the hub fronts the exact host
where the flat cut was measured. Fixing one is the ADR-006 failure mode.

| Test | What it proves |
|------|----------------|
| `TestBootstrapDeliveryDoctrine_AbsentBudgetNeverStandsAlone` (RETIRED) | Deleted in 1537d83 together with the `budget` field it policed: with no `budget` on the payload, there is no absent-budget claim left to condition on `complete` |
| `TestBootstrapDeliveryDoctrine_RestartTeachesSentinelAndFetch` | Anchored the way `TestEmbeddedCommands_CheckSuiteDelivery` anchors — on the **action**, not a mention. `complete` must be raised in a *bullet* inside Step 2 (prose observing that the sentinel exists is not a rule), and Step 2 must mandate the document FETCH: `vp_read_resource`, `workflow_uri`, `resume_uri`, `resume_sha256`, the words "every restart", and the fetch ordered ahead of the `vp_get_doctrine` call. Rewritten at 313: the old assertion pinned the recovery onto the sentinel bullet, which was right while the bodies arrived inline. Once the payload became an index, a rehydrate-on-truncation rule would leave an agent whose payload arrived WHOLE with no resume and no workflow at all — conditioning the fetch on a truncation signal is exactly how that would ship |

Both were confirmed RED by restoring the old wording at each of the three sites in turn, before the first was retired.

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

## CI job shape (`.github/workflows/ci.yml`)

CI triggers on every push to `main` and on every pull request. The jobs, as of
v8.2.0 (`sed -n '/^jobs:/,$p' .github/workflows/ci.yml | grep -E '^  [a-z0-9-]+:$'`
lists them):

| Job | Runs | Why it exists |
|---|---|---|
| `fmt` | `make fmt-check` | Fast, toolchain-only formatting verdict. `gofmt -l` alone prints drift and exits 0, so the target fails on any drift |
| `vet` | `go vet ./...` | Static checks, on Linux only |
| `goreleaser-check` | `make goreleaser-check` | Validates `.goreleaser.yml` with the same GoReleaser the release job installs, after a negative control proves the validator can still reject |
| `test` | `go test -short -race -cover ./...` | The unit tier. Not `make test`: no `fmt-check` (the `fmt` job has it) and no `live-canary` (CI has no live vault). Carries no model cache, because no `-short` test loads the model |
| `model` | `make model-test` | The real-ONNX tier (see Tier 2), with a `~/.cache/huggingface` cache and `timeout-minutes: 15` |
| `source-audit` | `make source-audit` | Of the jobs that run on pull requests as well as pushes, the only one that runs the type-checked derived-gate rule, which skips under `-short` (see *Source Audit*) |
| `hnsw` | `make hnsw-check` | `goreleaser build --snapshot --clean` of `./cmd/vp` for every goreleaser target (the only PR-time cross-build), the vendored `coder-hnsw` drift check, and the slow HNSW recall-floor and search-cost tests (`VP_HNSW_SLOW=1`, no `-race`); `timeout-minutes: 20` |
| `hnsw-measure` (its own workflow, `.github/workflows/hnsw-measure.yml`, nightly and `workflow_dispatch`, never on a push or PR) | `make hnsw-measure` | The asserted 50k HNSW recall test with churn and the 10k harness slice (`VP_HNSW_MEASURE=1`, no `-race`), too long for the PR-time `hnsw` job; `timeout-minutes: 45` |
| `windows-lock` | `go test -short ./internal/vaultlock/...`, then `go test -run TestIntegration_VaultLockCrossProcess ./internal/integration/` | The sole runtime proof of the Windows byte-range lock (`flock_windows.go`); the rest of CI is Linux-only |
| `build` | `CGO_ENABLED=0 go build -o vp ./cmd/vp`, then `./vp version` | The shipped zero-CGO build links and runs |
| `init-e2e` | `go test -race -run '^TestIntegrationE2EInit' -v ./internal/integration/...` | Exec-based e2e tier |
| `dispatch-e2e` | `go test -race -run '^TestIntegrationDispatch' -v ./internal/integration/...` | Exec-based e2e tier |
| `githook-e2e` | `go test -race -run '^TestIntegrationE2EGithook' -v ./internal/integration/...` | Exec-based e2e tier |
| `walkthrough-e2e` | `go test -race -run '^TestIntegrationE2EWalkthrough' -v ./internal/integration/...` | Exec-based e2e tier; `timeout-minutes: 10` |
| `workflows-e2e` | `go test -race -run '^TestIntegrationE2EWorkflows' -v ./internal/integration/...` | Exec-based e2e tier; `timeout-minutes: 10`; uploads `metrics.jsonl` on success |
| `ubuntu26-canary` | on `ubuntu-26.04`: `go test -short -race ./...`, `make source-audit`, the walkthrough e2e, `make model-test` | Temporary, push-only rehearsal of the next runner image, with a removal condition in its ci.yml comment; `timeout-minutes: 25`, no model cache by design |

Each of the five e2e jobs (`init-e2e` through `workflows-e2e`) uploads its
retained tmpdir when it fails (an `if: failure()` upload step).

`make test-full`, `make cover-full` and `make integration` are manual targets
that no job invokes. So any `TestIntegration*` test that skips under `-short`
and is not matched by one of the e2e filters above runs only when someone runs
`make integration` or `make test-full`.

Two properties of the workflow file are load-bearing and easy to undo by
accident.

**Every job runs under an explicit timeout where a hang has been observed.**
GitHub's default is **six hours**. On 2026-08-18 run 32165092806 sat
`in_progress` for over an hour on a single wedged step, and would have held the
runner for six. `workflows-e2e` and `walkthrough-e2e` therefore carry
`timeout-minutes: 10` — roughly 15x the ~40s the harnesses actually take, so a
slow runner is never a false red, but a wedged step is a red job in ten minutes
instead of a runner lost for the afternoon. The timeout is a backstop against
**any** stuck step, not a bound on harness runtime. The `model` job carries
`timeout-minutes: 15` for the same reason — a wedged model download — and its
`make model-test` passes `go test -timeout 10m`, so go's own timeout panic
(with its goroutine dump) fires before the runner kills the job.

**Real-model coverage is its own job.** `model` runs `make model-test` (see
Tier 2) with the `~/.cache/huggingface` cache (key `hf-cache-v1-…`), and it is
the only CI job that loads the ONNX model on pull requests as well as pushes (the push-only
`ubuntu26-canary` also runs `make model-test`, cold). `test` runs `-short -race` and never
reaches the model, so it carries no model cache. Before this split the only CI
exercise of the model was a side effect: a step that warmed the cache by
running one `cmd/vp` check test by name. The `model` job depends on
huggingface.co being reachable on every run (see "Model cache").

**One in-flight run per ref.** The workflow-level `concurrency` group is
`${{ github.workflow }}-${{ github.ref }}` with `cancel-in-progress: true`, so a
new push to `main` (or a new PR head) cancels the CI still running for that ref's
previous SHA. `github.ref` is `refs/heads/main` for a main push and
`refs/pull/<n>/merge` for a pull request, so PRs are mutually isolated and cannot
cancel each other or `main`. The accepted trade-off is that a superseded `main`
SHA loses its CI verdict — which is the intent, since that verdict describes code
`main` has already moved past.

### `jq` is no longer a CI dependency

`workflows-e2e` and `walkthrough-e2e` used to run bash harnesses
(`test/e2e/workflows/run.sh`, `test/e2e/walkthrough/run.sh`) that shelled out
to `jq` for JSON shape checks and the metrics summary table, and briefly ran
`sudo apt-get update && sudo apt-get install -y jq` before that — a step that
was itself the source of a multi-hour CI hang (32165092806) and pure waste
besides (jq already ships in the `ubuntu-latest` image). That apt step was
deleted outright once the preflight-and-fail-fast approach below replaced it,
and jq itself is now gone as a dependency of these two jobs entirely: the Go
port (`internal/integration/e2e_walkthrough_test.go`,
`internal/integration/e2e_workflows_test.go`) decodes JSON directly via
`encoding/json` into typed structs (`palace.TuneReport` for the tune report,
an anonymous `{Project string}` for `vp inject`'s bootstrap payload) instead
of shelling out to `jq`. Neither job's `ci.yml` step installs or requires jq
any more.

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

No `-short` test reaches that download. The `vp check` full-suite tests
route the Embedder row through the `newVaultEmbedder` seam and stub it (see
"`cmd/vp` — Flag Wiring"), and the migrate and search tests validate their
inputs before constructing anything (see "Model-free migrate and search
tests"). Real-model coverage in CI is the `model` job, which runs `make
model-test` without `-race`, so a cold-cache download there is the
un-instrumented kind the race detector never sees. CI's
`~/.cache/huggingface` cache serves that job.

**Warm cache** (subsequent runs, destination already fully populated):
`NewONNX` checks the destination for a complete snapshot (`tokenizer.json`
plus at least one `.onnx` file) before doing anything else, and when one is
present it skips `hugot.DownloadModel` — and the whole `go-huggingface`
repo-handle path — entirely. No revision-info request, no network, no risk
of the delete-then-refetch bug that used to destroy the cached
`info/<revision>` file on an offline attempt (`hugot.DownloadModel` forces a
revision refresh on every process's first call against a `Repo`, and
`go-huggingface`'s locked downloader deletes the cached file before
re-fetching it — safe only because a warm destination now bypasses that path
rather than relying on it not to fail). `make model-test-offline` is an
opt-in regression check for this (Linux + `unshare -rn` only; it skips with
a message otherwise) — it is not part of `make model-test`, `make
integration`, `make test-full`, or CI.

A **cold or partial** destination still goes through `hugot.DownloadModel`
unchanged: full network required, and still subject to the pre-existing
upstream delete-then-refetch bug if the network drops mid-download — not a
regression, since a partial cache offline failed the same way before this
fix. So `make model-test`, `make integration` and `make test-full` still
need network access on anything but a destination that was already fully
downloaded by a prior successful run.

**A stalled connection to huggingface.co no longer hangs `NewONNX`
indefinitely.** `hugot.DownloadModel`'s two internal network calls run on an
uncancellable `context.Background()` with no `http.Client` timeout anywhere
in either vendored package (`hugot`, `go-huggingface`), so a blackholed
connection used to block the calling goroutine forever, holding the
model-cache lock, with nothing surfacing an error (`vp-search-can-hang-
indefinitely-with-no-surfaced-error`). `NewONNX` now runs the download in a
goroutine and bounds its own wait with `modelDownloadTimeout` (10 minutes,
package `var`, overridable in tests). This bounds only the *caller's* wait
per attempt — it does not make the underlying network call cancellable. On
timeout, `NewONNX` returns a clean error immediately, but the model-cache
lock stays held until the leaked goroutine's download actually finishes
(success or error): a second `NewONNX` call still has to wait behind it,
bounded in turn by `modelCacheLockTimeout` (8 minutes). A durably broken
network therefore produces a sequence of bounded waits instead of one
infinite hang, not zero waiting. See `internal/embedder/onnx_download_timeout_test.go`
for the regression coverage, including the lock-held-until-the-leaked-
goroutine-finishes test.

Cache lifecycle:
- `make clean` — preserves model cache (only removes build artifacts)
- `make dist-clean` — deletes this project's local `.cache/` (its
  `VP_OFFLINE_MODEL_CACHE_DIR`-style destination), but NOT the separate
  HF hub download cache at `${XDG_CACHE_HOME:-$HOME/.cache}/huggingface/hub`
  go-huggingface maintains — so it forces a re-copy from that hub cache,
  not necessarily a re-download over the network. Delete the hub cache too
  to force an actual re-download.
- `git clean -fxd` — also deletes `.cache/` (it's gitignored), same caveat

### Embed cache: `palace/.local/embed-cache/{project}/`

Separate from the model cache, the **embed cache** stores pre-computed
embedding vectors for drawer content that has already been embedded. This
is a second-level cache used by `search.Engine.Rebuild()`:

```
palace/.local/embed-cache/my-project/
  a1b2c3d4.vec    ← raw little-endian float32 (1536 bytes for 384 dims)
  e5f6g7h8.vec
  ...
```

It lived at `palace/{project}/.local/embed-cache/` until 2026-09-10. The first
cache operation of each `EmbedCache` sweeps that legacy layout into the new one
(see ARCHITECTURE, "Embed Cache"), so a test that seeds a vector at the old
path is simulating a binary from before the move — which is exactly what
`TestEmbedCache_LegacyVectorsMigrateThenReembedOnce` and the legacy leg of
`TestIntegrationPulledDeletionLeavesNoHusk` do. A test that expects to watch a
sweep must build a FRESH `EmbedCache` (or engine): the sweep is a per-instance
`sync.Once`, and the harness engine's may already have fired.

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

Consumed by the ONNX tests in `internal/embedder/onnx_test.go`,
`onnx_crossprocess_test.go` and `onnx_selfheal_test.go`,
`internal/search/integration_test.go`, `internal/capture/integration_test.go`,
and by `testinfra.NewHarness` (`internal/testinfra/harness.go`) when a harness
asks for the real embedder, which is how `internal/integration` reaches it.
Find the current list with `grep -rln 'ProjectCacheDir(' --include='*.go' .`.

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
| `EmbedCacheLivesOutsideProjectTrees` | MCP → tools → search → storage → vaultaudit | No | `vp_search_cross_project` runs FIRST, on a harness engine that has never indexed the notes-only project (asserted), and must return a hit from it — reverting `ensureAllIndexes` to a palace-only enumeration turns it red; then `vp_search` on the notes-only project writes its vector at `palace/.local/embed-cache/notesonly/note.notesonly.<stem>.c0.vec` and creates no `palace/notesonly`; `project-tree-coherence` still reports the project; `palace-local-only` passes (`embed_cache_layout_test.go`) |
| `PulledDeletionLeavesNoHusk` | git → storage.Pull → search → check → vaultaudit | No | The incident end to end, with **host B = the harness root** `git init`-ed in place and a bare remote, and host A a plain clone never opened as a vault (the harness cannot root a vault at a clone). Legacy leg: a vector at the OLD path survives A's pulled deletion as a husk, which `ListAllProjects` and both audit dimensions ignore and `vp_check palace-local-only` reports; one search on a FRESH engine heals the husk and reaps its cache (a keeper project keeps the reaper's zero-projects guard from declining). New-layout leg: the same pulled deletion leaves no `palace/stub2` at all, and the next sweep reaps its cache. Git runs with `GIT_CONFIG_GLOBAL=/dev/null`; `PullResult.RemoteResults` is checked, since `Pull` reports per-remote failure there (`embed_cache_layout_test.go`) |
| `ConcurrentSweepsConverge` | search → storage | No | Four engines, each with its own sweep Once, search at the same instant over a vault in the legacy layout (two real stores, a notes-only phantom, an incident-shape husk, a crash-state empty `.local`): every search succeeds, every known project's vector ends at the new path byte-identical, no legacy cache survives, husks heal, real stores keep their drawers (`embed_cache_layout_test.go`) |
| `LocalOnlyPalaceDirIsNotAStore` | MCP → tools → storage → check → vaultaudit | No | A hand-seeded legacy husk (`palace/stub/.local/embed-cache/x.vec`) and an empty subtree (`palace/empty/drawers/w/r/`) are absent from `vp_list_projects`' `projects` and `drift`; `vp_check {checks:["palace-local-only"]}` returns one Info row naming both with what each holds and no disposition word; `vaultaudit.Run` reports neither under `palace-store-drawers` or `project-tree-coherence`, and still reports a notes-only project under `project-tree-coherence` (`palace_presence_test.go`) |
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
| `IntegrationCheckReachesRealHostRegistry` | cmd/vp → check → mcphost | No | The built `vp check --json`, run directly (it exits 1 with no global config, so not through `runVP`) with an explicit env — temp `HOME`/config/cache/data dirs, a dead proxy, and a scripted `grok` first on `PATH` — reports `MCP host: grok` as `pass` and the script saw exactly `mcp list`. It is the binary-level proof that production still asks `mcphost.Registry()`, which the stubbed `cmd/vp` tests cannot give. The Zed row varies with the parent `PATH` and is not asserted. Skipped on Windows (`check_mcp_hosts_test.go`) |

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

Hardens `CommitAndPushPaths`'s push recovery so a dirty working tree can
no longer strand a local capture commit, and so an already-ahead branch heals
instead of compounding the strand across sessions (the observed ahead-2 → ahead-N
divergence): a loud `Stranded` surface (commit created + push attempted but
reached no remote, distinct from a clean no-remote downgrade), and a network-free
already-ahead reconcile guard.

**The reconcile merges; it never rebases, autostashes or force-pushes**
(`vault-sync-rebase-killed-mid-run-strands-the-shared-vault`, 2026-10-02). It
used to `rebase --autostash` under gitCmd's 60 s SIGKILL deadline: a 990-commit
replay was killed at pick 590 and stranded the shared vault mid-rebase, with the
caller's own files held in the autostash. Both reconcile paths now go through
`mergeFetchedTip` (departure guard → `git merge` → `merge --abort` on a merge
left in progress, its failure reported), and the multi-remote convergence is a
plain fast-forward push. The autostash `PopConflict`/`PopConflictPaths` fields
are gone, because nothing can set them.

Unit coverage in `internal/storage/vaultsync_test.go` (all use real `git`
subprocesses against bare-remote + clone fixtures — full-stack for this path):
`TestPushResult_Stranded` (the four strand/not-strand cases),
`TestCommitAndPushPaths_DisjointDirtyFileSurvivesReconcile` (a dirty tracked file
the merge does not touch keeps its bytes, nothing stashed),
`TestCommitAndPushPaths_TrueMergeConflictStrands` (true conflict aborts + skips
push + strands, no merge left in progress, HEAD back on the capture commit),
`TestCommitAndPushPaths_AlreadyAheadReconcilesThenPushes` /
`_AlreadyAheadPersistentConflictStrands` /
`_AlreadyAheadGuardFailsOpen` (Fix B reconcile, lossless strand-on-conflict, and
fail-open / no-fire on push=false / unresolved-ref / not-ahead),
`TestCommitAndPushPaths_PushMergesOnNonFastForward`, plus `TestRebaseInProgress`
and `TestUnmergedPaths` (the state probes).

`internal/storage/vaultsync_merge_test.go` pins the merge itself, each against
the behaviour it replaced:
`TestCommitAndPushPaths_ReconcileKeepsLocalCommitSHAs` (`rejected_push`,
`already_ahead`: every local commit keeps its SHA — no replay),
`_ReconcileNeverStashesTheCallersWork` (a dirty file the remote also changed is
left byte-for-byte, no stash, no merge or rebase state, the commit strands),
`_ReconcileAbortFailureIsReported` (a PATH-shim `git` fails `merge --abort`; the
per-remote error carries it), `_ConvergenceNeverForcePushes` (the first remote is
fast-forwarded, never rewritten; an argv-logging shim sees no forced push),
`_ConvergenceRefusesAConcurrentWriter` (via `afterPushHook`: a remote moved
between our push and the convergence is not overwritten),
`_CommitSHAIsTheCallersCommitAfterReconcile` (`CommitSHA` is the caller's
commit, not the merge on top of it), and the source pin `TestNoVaultPushForces`
(no production file in `internal/storage` spells `--force`,
`--force-with-lease`, `-f` or a `+refspec` on a push; its subtest proves it fires).
`_LeavesAMergeItDidNotStartAlone` (a hand-resolved merge pullCore left in
progress: the call refuses naming `MERGE_HEAD`, and the resolution's bytes, index
entry and `MERGE_HEAD` survive — `mergeFetchedTip` only aborts a merge it
started), `TestKilledMergeErrorNamesTheLockAndRemovesNothing` (a deadline kill is
reported as one, names the `index.lock` it left, and removes nothing),
`_KilledMergeRefusesTheCommit` (end to end: a shim `git merge` leaves
`index.lock` and hangs past a lowered `mergeTimeout`; the call returns a
`*vaultTreeUnsafeError` naming the lock, with no `merge --abort`, no `git add`
and HEAD unmoved), `_SignalledMergeRefusesTheCommit` (the same for a merge
killed by a signal, the shim's SIGTERM to itself), and
`_CallersOwnDirtyFileDoesNotStrandTheReconcile` (the already-ahead reconcile's
refusal over the caller's own uncommitted file is not recorded; the post-commit
reconcile merges and pushes). In `vaultsync_verify_test.go`,
`TestPruneMirrorsVerified_MergeInProgressRemovesNothing` pins the prune's side
of that refusal: with an operator's merge in progress and the branch ahead, the
prune keeps every path, deletes nothing on disk, stages nothing and releases the
commit lock.
`internal/storage/vaulttidy_test.go` mirrors the
strand at the `TidyVault` boundary (`TestTidyVault_StrandedWhenAllRemotesFail`,
`_NotStrandedOnSuccess`); `internal/tools/system_tools_test.go` covers the MCP
`vp_vault_tidy` surface. `TestVaultTidy_StrandedIsAnError` requires a tool
**error** (not a status) naming `STRANDED`, saying the commit `EXISTS locally`,
and naming the failing remote. `TestVaultTidy_PartialPushIsAnError` requires a
1-of-2 push to be an error naming `PARTIAL` and the dead remote, and not
`STRANDED`.

### `internal/storage/`, `internal/onboard/` — Project Scaffold Commit

When a project is initialised into an EXISTING vault, `vp init` / `vp_init`
commits the project scaffold it lays down (`storage.CommitProjectScaffold` with
`IncludeStamp`): the two marker READMEs plus `Projects/<p>/.surface`, as one
local commit scoped to those paths. Without the commit, tidy reports all three
and `vp_vault_sync` refuses. A FRESH vault's own `.gitignore` and
`.vibe-palace/vault.toml` are not covered (task
`fresh-vault-init-leaves-gitignore-and-vault-toml-uncommitted`). The onboard
fixtures pre-commit `.gitignore` for that reason. `vp config sync` commits only
the markers, and only for a project whose `.surface` is already tracked
(`RequireTrackedStamp`).

In `internal/onboard/steps_scaffold_commit_test.go` (real `git`, a bare remote):
- `TestProjectScaffold_LeavesNoUncommittedInitPaths` pins the commit to exactly
  those three paths.
- `_FollowingSyncDoesNotRefuse` is the incident's acceptance test.
- `_ReinitCommitsAnEarlierUncommittedScaffold` covers the heal.
- `_CommitFailureIsAFailRow` checks that the run is exit-worthy and names the
  remedy.
- `_NoIdentityFailRowNamesTheRemedy` checks that the row names `git config
  user.name` / `user.email`.
- `_StaleStubMarkerIsAnInfoRow` covers bytes that are not vp's current stub,
  which are never committed.
- `_GitDisabledIsADetailNotAFail` checks that a git-disabled host gets a detail
  line, not a failure.
- Three guards cover never committing unrelated dirt, a stray scaffold still
  being reported, and a converged re-init making no commit.

`internal/storage/project_scaffold_commit_test.go` pins the helper itself:
- markers count only when their bytes match the stub (`_NonStubMarkerIsKeptNotCommitted`);
- `.surface` is committed only alongside a marker (`_StampOnlyJoinsAnActualCommit`);
- an absent marker inside an untracked directory is neither committed nor kept
  (`_AbsentMarkerInAnUntrackedDirIsNeitherCommittedNorKept`);
- a vault nested in another repository is skipped and never staged into
  (`_NestedVaultIsSkippedNotAnError`);
- `RequireTrackedStamp` skips a project with an untracked stamp and otherwise
  commits markers only (`_RequireTrackedStampSkipsAnUntrackedStamp`,
  `_TrackedStampCommitsMarkersOnly`);
- with no git on `PATH` it still refuses up front (`_GitDisabledRunsNoGit`).

`cmd/vp/cmd_config_sync_scaffold_test.go` covers the config-sync caller:
- `TestConfigSyncCommitsMarkersOfADeliberatelyInitialisedProject`: a tracked
  stamp, so the markers are committed and the stamp is not.
- `TestConfigSyncCommitsNothingForAStray`: hook-capture residue, so nothing is
  committed and vault sync still refuses.

`cmd/vp/cmd_init_test.go` `TestInitIntoANestedVaultDoesNotFail` covers an
existing install with a `--vault-path` inside another repository: exit 0 and
nothing staged there.

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

### `internal/storage/`, `internal/check/`, `cmd/vp/` — Vault Resolution Tiers (ADR-012)

Covers `storage.ResolveVaultBinding`: the checkout `vault_path`, then the host's
`[project_vaults]` binding, then the global `vault_path`, and the single typed
refusal (`ErrVaultBindingRejected`) every rejected binding carries. Hermetic
`$HOME`/`XDG_CONFIG_HOME` fixtures; the git-origin derivation is a seam
(`gitRemoteSlug`) so a test can count its calls.

| Test | What it proves |
|------|----------------|
| `TestResolveBinding_PrecedenceMatrix` | Each tier alone, and tiers 1+2 agreeing, give the expected path and source prefix |
| `TestResolveRefusesCwdBindingDisagreement` | A checkout `vault_path` and a binding naming different vaults refuse, naming both sources |
| `TestResolveRefusesMissingBoundVault` | A bound target that is missing or has no manifest refuses, and nothing is created there |
| `TestResolveRefusesUnnamedMarkerWithBoundGitSlug` | A marker naming no project (or no marker) refuses while its git-origin slug is bound |
| `TestResolveUnboundHostNeverExecsGit` | A host with no bindings never runs the git-origin derivation |
| `TestResolveWarnsWhenGitSlugBoundButMarkerNameIsNot` | An unbound marker name resolves tier 3, with a warning naming both slugs |
| `TestResolveBinding_MalformedTableRefused` | Case-variant table, invalid key, non-string/empty value, non-table, unparseable config, dangling symlink: all refuse |
| `TestResolveBindingReachesAFreshWorktree` | A fresh `git worktree` (committed marker only) resolves the binding |
| `TestMachineWideSurfacesIgnoreProjectBindings` | `ResolveGlobalVaultPath`/`OpenVaultGlobal` stay on tier 3 from inside a bound checkout |
| `TestStaleBindingAcceptsTier2Binding` | The MCP drift check sees tier 2, and reports a stale server with the binding source |
| `TestSwallowedStillMatchesBothSentinels` | A swallowed `vault_path` matches both `ErrSwallowedVaultPath` and `ErrVaultBindingRejected` |
| `TestHookRejectedBindingCapturesNothing` (`cmd/vp`) | A rejected binding captures nothing: no global-vault fallback |
| `TestSkillsShowDoesNotDegradeARejectedBinding` (`cmd/vp`) | A rejected binding stays a usage error, never "no vault configured" |
| `TestCheckConfigReportsRejectedBindingNotMissing` (`internal/check`) | `vp check` reports a rejected binding, never "not found / run vp init" |
| `TestCheckTrackedMarkerVaultPath` (`internal/check`) | The CLI-only row flags a committed marker that sets `vault_path`, and only that |
| `TestResolveRefusesABrokenMarker` | A found marker with a syntax error, a wrong-typed `vault_path` or EACCES is a typed refusal, with or without bindings |
| `TestResolveWhenGitCannotRuleOutABinding` | With `PATH=""` on a binding host: an unnamed marker refuses, a named one warns, a non-repo needs no git |
| `TestResolveRefusesARelativeBindingTarget` | A relative target is refused even when a vault exists at that path relative to cwd |
| `TestResolveAbsentOrDanglingConfigIsNoBindings` | An absent or dangling-symlink config is "no bindings" and still reads as not-configured |
| `TestResolveUnreadableConfigFailsClosed` | An unparseable or EACCES config fails with `ErrHostConfigUnreadable`, not a binding refusal |
| `TestResolveRefusesEmptyValueAndFileTargetAsSuch` | An empty value and a file target are each refused as what they are |
| `TestResolverAndDetectionShareOneMarker` | A padded name and a symlinked checkout bind and label from the same marker |
| `TestGitRemoteSlugChecked` (`internal/project`) | "No repo"/"no origin" is `("", nil)`; git missing from PATH is an error |
| `TestHookBrokenMarkerOfBoundProjectCapturesNothing` (`cmd/vp`) | A bound project's broken marker captures nothing, in either vault |
| `TestMemoryHarvestCLIWritesTheBoundVault` (`cmd/vp`) | `vp memory harvest` in a bound checkout writes the bound vault, not the live one |
| `TestHookSessionEndHarvestsIntoTheBoundVault` (`cmd/vp`) | The SessionEnd harvest writes the vault the hook resolved |
| `TestRunSelectedChecksBindingRefusalAndUnreadableConfig` (`cmd/vp`) | `vp check --check` errors on a binding refusal, and runs with a leading Config row on an unreadable config |
| `TestGatherCheckResultsSkipsSurfaceOnRejectedBinding` (`cmd/vp`) | The full `vp check` skips the Surface row for a rejected binding |
| `TestPlansScanStatesARejectedBinding` (`cmd/vp`) | `vp plans scan --json` states the rejected binding |
| `TestVaultPlanNamesARejectedBinding` (`internal/reconcile`) | `vp config sync`'s vault tier names the rejection, not the `vp init` remedy |

### `internal/storage/`, `internal/tools/`, `cmd/vp/` — Binding a Project to a Vault (`vp config bind`)

Covers `storage.BindProjectVault` (the one `[project_vaults]` writer), its CLI
(`vp config bind`) and MCP (`vp_config_bind`, stdio only) front ends, and
`RebindCheckout` (split binds, rename moves the key). Hermetic HOME/XDG
fixtures with a real git remote on the target vault.

| Test | What it proves |
|------|----------------|
| `TestBindMovedBindsEveryCheckout` | A moved bind makes every named checkout, and a fresh worktree, resolve through the binding |
| `TestBindRefusals` | No departure record, label mismatch, unlabelled, relative/missing/wrong-format/empty target, the default vault, a live slug, an unreadable or malformed config, a re-point, a checkout naming another project, git missing: each refuses and leaves the config byte-identical |
| `TestBindAllowUnlabelledAndAlreadyBound` | `allow_unlabelled` binds; the same root again is "already bound" with no write and no `.bak` |
| `TestBindNewMode` | Mode new binds a slug the default vault never held, and refuses one it holds |
| `TestBindChangesOnlyItsKey` | The splice appends the table at EOF, or inserts into an existing one, byte-preserving the rest |
| `TestBindRefusesConcurrentConfigChange` | The write is a compare-and-set on bytes; another writer's change survives |
| `TestBindRestoresPreImageWhenACheckoutShadows` | A checkout whose own `vault_path` disagrees fails verification and the config is restored |
| `TestVaultRemoteURLs` | No remotes is `(nil, nil)`; not-a-repo and git-missing are errors |
| `TestBindDryRunWritesNothing` | A dry run reports the change and writes nothing |
| `TestRebindCheckoutSplitBindsWithoutTouchingTheMarker` | A split binds, leaves the marker byte-identical, prints no git commands, and refuses (restoring the config) a disagreeing or swallowed `vault_path` |
| `TestRebindRenameAddsTheBindingKeyAndKeepsTheOld` / `TestRebindRenameRefusesABindingConflict` | A rename adds `[project_vaults].<to>` and keeps `<from>` (a second checkout still naming `<from>` stays bound), and refuses a conflicting key or a binding into another vault before any write |
| `TestBindCannotBeRacedIntoARepoint` / `TestRebindRenameCannotBeRaced` / `TestSpliceNeverRepoints` | Bindings are parsed from the bytes the compare-and-set checks, so a concurrent writer's binding is never overwritten |
| `TestBindRefusesAHeaderlessTable` | A dotted or inline `project_vaults` table refuses instead of taking an invalid appended header |
| `TestNormaliseRemoteURL` / `TestBindMatchesRemoteSpellings` | ssh://, scp-like and https spellings of one repository match (host case-folded, path not); another repository or a non-URL label refuses |
| `TestBindKeepsConfigModeAndSymlink` / `TestWriteHostLocalWithBackup` | A 0600 config and its `.bak` stay 0600; a symlinked config is written through to its target |
| `TestBindRestoreDoesNotOverwriteAnotherWriter` / `TestRestoreHostLocalCAS` | The post-verify restore is a compare-and-set against the bytes the bind wrote |
| `TestBindRefusesATargetThatRecordsTheSlugDeparted`, `TestBindPostconditionRunsBeforeTheWrite`, `TestRebindRenameVerifiesAfterWriting`, `TestBindVerifyRequiresTheBindingSource` | The target-departed guard, the pre-write postcondition, the rename's post-write verification, and verification requiring a `binding:` source each fire |
| `TestRebindRenameRestoresTheConfigWhenTheTomlWriteFails` | A failed toml write restores the config key already moved |
| `TestRebindCheckoutUsesTheSharedMarker` / `TestRebindRenameToleratesAWrongTypedTag` | `RebindCheckout` reads through `project.ReadMarker` and refuses a marker the resolver does not read |
| `TestGateLeavesBindToolAlone` (`internal/tools`) | The departed-project seam lets `vp_config_bind` bind a departed slug |
| `TestOnlyTheBindToolTargetsAProjectBySlug` (`internal/tools`) | The `slug` exemption is scoped to exactly `vp_config_bind` |
| `TestStdioOnlyToolsAreAbsentOnServe` (`cmd/vp`) | `vp_config_bind` is never served over HTTP, even with `--allow-writes` |
| `TestConfigBindVaultCallsBind` / `TestConfigBindRefusesAndValidatesUsage` (`cmd/vp`) | The CLI binds and verifies every repeated `--checkout`; refusals and usage errors exit `ExitUser` |

#### Multi-slug bind and stale bindings (`internal/storage/project_bind_batch_test.go`, `internal/mcp/stale_project_binding_test.go`)

`vp config bind` takes several slugs in one call, and the resolver refuses a
binding that no longer matches what the vault holds.

| Test | What it proves |
|------|----------------|
| `TestBindProjectVaultsWritesEverySlugInOneWrite` / `TestBindProjectVaultsIsAllOrNothing` | Every slug lands in one config write, and one refused slug writes none of them |
| `TestBindProjectVaultsRefusesAConcurrentWrite` | The batch write is a compare-and-set; another writer's change survives |
| `TestBindRefusesATargetWithARecordOverRealContent` | A target holding both a departure record and real content for the slug refuses |
| `TestBindProjectVaultsCheckRootIsApartFromVaultPath` / `TestBindProjectVaultsVerifiesEachCheckoutsOwnSlug` | A dry run may check a scratch tree for a path that does not exist yet, while a real bind refuses unless the check root is the vault path; each checkout is verified against the slug its own marker names |
| `TestResolverRefusesAStaleBinding` and its `…KeptAliveByIgnoredBak` / `…KeptAliveByMachineLocalResidue` variants, `TestResolverRefusesABindingWhenTheDefaultVaultHoldsTheProject` | A binding whose target no longer holds the project, or whose project is back in the default vault, refuses; an ignored `.bak` or machine-local residue does not keep it alive |
| `TestResolverAcceptsAPalaceOnlyProjectInTheTarget`, `TestResolverIgnoresResidueInTheDefaultVaultForANewBinding`, `TestResolverNeverRefusesANewProjectWithNoFilesYet` | The non-refusals: a palace-only project counts as held, an empty `palace/<p>` or ignored residue in the default vault does not block a `--new` binding, and a new project with no files is never refused |
| `TestRebindRenameVerifiesWhenToIsAlreadyBound` (`project_bind_round2_test.go`) | A rename whose target slug is already bound writes no binding but still verifies the renamed checkout; a disagreeing `vault_path` is refused and the marker restored |
| `TestHookUnreadableConfigCapturesNothing` (`cmd/vp/cmd_binding_round2_test.go`) | A host config that exists but is unreadable makes the hook capture nothing and exit 0: never `ExitSystem`, which blocks the turn, and never the global fallback vault |
| `TestStaleProjectBindingRefusesWritesOnARunningServer` (`internal/mcp`) | A server started while the binding was valid refuses writes once the move is undone underneath it. The server's root never changed, so only the stale-binding rule can see it, and it must reach the dispatch gate |

### Vault lifecycle and departure tests

The lifecycle commands (`vp vault init`, `vp vault copy`, `vp vault project
delete`, `vp vault clone --bind`) and departure records, which only
`vp vault project delete` and the `vp_vault_split` purge write. The
user guide is [doc/VAULT-LIFECYCLE.md](VAULT-LIFECYCLE.md); the decision record
is [ADR-013](adr/013-vault-project-lifecycle-and-departure-records.md). Per-file
counts drift; derive them with `grep -c '^func Test' <file>`.

**End to end (`internal/integration/lifecycle_e2e_test.go`).** These build the
real `vp` binary and drive two hosts, each with its own isolated HOME and XDG
directories, against `file://` bare remotes, to split two projects out of one
vault into a second vault. Each printed real-run line and Undo line is run
through `sh -c` exactly as printed, and both vault paths contain a space, so a
quoting fault fails the test.

| Test | What it proves |
|------|----------------|
| `TestIntegrationLifecycleHappyPath` | `init` publishes both remotes, the copy is byte-identical, the second host's pull sweeps its departed embed cache, a raw write into the moved tree refuses, and `clone --bind` makes the second host's write land in the new vault |
| `TestIntegrationLifecycleCloneBeforePullRefuses` | Cloning before the source delete has been pulled refuses with "pull first" and writes nothing; after the pull the same clone succeeds |
| `TestIntegrationLifecycleBindRefusesALabelMismatch` | A vault whose remotes match none of the departure record's label is refused |
| `TestIntegrationLifecycleInitRefusesANonEmptyRemote` | `init` into a non-empty remote refuses and publishes nothing |
| `TestIntegrationLifecycleUndoThenRedo` | The printed Undo lines, run in order with the second host already bound, revert the move, and the redo completes it |
| `TestIntegrationLifecycleStrandedWorkSurvives` | A host's unpushed work under a moved project survives the undo and the redo, and that host can then finish the procedure |
| `TestIntegrationLifecycleRaceBetweenDryRunAndRun` | Another host pushing between the delete's dry run and its real run makes the real run refuse before writing; after a pull the same printed line succeeds (the digest binds the footprint) |
| `TestIntegrationLifecycleNoNewCheckFinding` | `vp check` reports no new finding in either vault after the move, and the copy does not carry a retired project config into the new vault |

All eight skip under `-short` ("run with make integration") and when `git` is
missing. As of v8.2.0 no CI job runs them: the `test` job passes `-short`, and
the e2e jobs' `-run` filters match `^TestIntegrationE2E…` and
`^TestIntegrationDispatch` only. They run under `make integration` and
`make test-full`.

**Kill and re-run (`internal/storage/lifecycle_e2e_kill_test.go`).**
`TestLifecycleMoveKilledTwiceFinishesOnReRun` kills the copy mid-copy and the
delete after its commit, through the commands' own in-process seams, and proves
a plain re-run of each finishes the move. `TestLifecycleDeleteKilledBeforeItsCommitFinishesOnReRun`
kills the delete after it writes its records and before its commit; the re-run
rolls the records back and deletes afresh. They have no `-short` skip, so the
CI `test` job runs them.

**Command units.**

- `internal/storage`: `vault_init_test.go`, `lifecycle_copy_test.go`,
  `lifecycle_delete_test.go`, `lifecycle_git_test.go`,
  `lifecycle_publish_test.go`, `vault_clone_test.go`,
  `vault_clone_resume_test.go`.
- `cmd/vp`: `cmd_vault_init_test.go`, `cmd_vault_copy_test.go`,
  `cmd_vault_project_delete_test.go`, `cmd_vault_clone_test.go`.
- `internal/tools`: `vault_copy_test.go` (`vp_vault_copy` plan then apply),
  `vault_split_departure_test.go` (a successful purge writes one departure
  record per purged slug; a refused purge writes none).

**Departure records and the departed-write gate.**

- `internal/departure`: `departure_test.go` (record parsing, chains and cycles,
  validation, an older binary reading a deleted record as departed),
  `departedpath_pin_test.go`, `residue_test.go`.
- `internal/departedpath/departedpath_test.go`: the record check fails closed
  when it cannot inspect, and `RefuseAbs` still judges a path that is lexically
  outside the named root but resolves inside it (the root named through a
  symlink).
- `internal/storage`: `departures_test.go`, `departures_delete_test.go`,
  `departed_write_test.go`, `departed_funnel_test.go`,
  `departure_guard_test.go`, `departed_record_test.go`,
  `departed_residue_test.go`, `embedcache_departed_test.go`.
- `internal/vaultfs/departed_test.go` (every entry point refuses a departed
  project, delete stays allowed), `internal/search/departed_test.go`,
  `internal/project/departed*_test.go`, `internal/check/departed_caches_test.go`.
- `internal/mcp/departure_gate_test.go` (the dispatch gate refuses a departed
  project and allows read-only calls) and `internal/tools/departed_seam_*_test.go`,
  `vault_departed_write_test.go`, `vault_sync_departed_test.go`,
  `departed_residue_test.go`.
- `cmd/vp`: `cmd_vault_departed_pull_test.go`, `cmd_vault_departed_write_test.go`,
  `cmd_archive_departed_test.go`, `cmd_departed_harvest_test.go`.
- The `departure-record-writer` source-audit rule (see *Source Audit*) pins who
  may write a departure record.

### Board and epics tests

`vp board`, `vp tasks epics` / `vp tasks --epic`, and the derived task graph
behind them.

- `internal/taskgraph/graph_test.go`: the graph is derived, never stored.
  Epics are derived from children; dangling references and cycles (dependency,
  parent and supersession) are reported and never walked; ordering puts a
  dependency before its dependent; subtrees are transitive and terminate on a
  cycle; and the board partition covers every task exactly once, with the
  bucket and sort rules for the Active, Icebox and History groups.
- `cmd/vp/cmd_board_test.go`: `vp board` rendering (empty board, mixed epics,
  the icebox always shown, history most-recent-first, supersession labels,
  missing dates, stale parents, column widths), `--project`, `--json` round
  trip and flag validation.
- `cmd/vp/cmd_tasks_test.go`: `TestRunTasksEpicsText` and
  `TestRunTasksEpicsTransitiveCounts` (`vp tasks epics`),
  `TestRunTasksEpicSubtreeReRoots` and `TestRunTasksEpicUnknownAndLeaf`
  (`vp tasks --epic`), `TestRunTasksReadOpensAnEpic`.
- `cmd/vp/cmd_migrate_task_board_fields_test.go`: the one-time migration to
  the board-reporting schema (active `pending` renamed to `planning`,
  `CreateTime`/`ModTime` backfilled from git history, the per-file
  `DataFormat` marker stamped).

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

### `internal/integration/e2e_*_test.go` — exec-based end-to-end tiers

The five `test/e2e/**` bash tiers (init, dispatch, githook, walkthrough,
workflows — 17 case scripts total, not the 16 an earlier draft of the
migration task claimed) were retired and ported onto
`internal/testinfra.RunCLI`: an exec-based helper (`BuildVPBinary` +
`RunCLI` + `Must` + `RetainOnFailure`, `internal/testinfra/runcli.go`)
promoted from `buildVPBinary`/`runVP` (`template_reconcile_test.go`) and the
hand-rolled runner `cli_dispatch_test.go` already had. Every case still
execs the real built `vp` binary — none of this runs in-process — under a
per-case `testinfra.IsolateEnv(t)` HOME/XDG_CONFIG_HOME sandbox, matching the
bash harnesses' `fresh_home` contract.

Run one tier locally with `go test -race -run '<pattern>' -v
./internal/integration/...` (see each tier's pattern below); all of them also
run as an ordinary side effect of `make integration` / `make test-full`,
since none skip under anything but `-short`.

**Retained properties**, each independently verifiable (see the migration
task for the full acceptance list): the walkthrough transcript is written
directly to `os.Stdout` (not `t.Log`) so it prints on both pass and fail;
every tier's working directory survives a failure via
`testinfra.RetainOnFailure` (`/tmp/vp-e2e-<tier>.*`, same convention `ci.yml`'s
per-tier `if: failure()` artifact upload already globs); and the workflows
tier's `metrics.jsonl` now actually reaches its 14-day-retention CI upload —
see the "Fixed, not merely ported" note below.

#### `test/e2e/init/` → `TestIntegrationE2EInit*` (pattern: `^TestIntegrationE2EInit`)

| Test | What it asserts |
|------|-----------------|
| `TestIntegrationE2EInitPositionalProjectDir` | `vp init` in a git-inited dir creates `.vibe-palace.toml` + default vault under HOME; no Templates/ pass |
| `TestIntegrationE2EInitExplicitVaultPath` | `--vault-path` is honored; vault lands there, not under HOME |
| `TestIntegrationE2EInitPositionalAndVault` | Both a positional project dir AND `--vault-path` land independently |
| `TestIntegrationE2EInitNonexistentPositional` | A nonexistent positional path aborts ExitUser BEFORE any filesystem write, stderr names `--vault-path` |
| `TestIntegrationE2EInitNoGitFlag` | `--no-git` creates the vault dir without `.git` |
| `TestIntegrationE2EInitCleanupIsolation` | Meta-safety: HOME resolves entirely under the harness sandbox (construction-guaranteed by `testinfra.IsolateEnv`, kept for documentation value) |
| `TestIntegrationE2EInitReinitIdempotent` | A second `vp init` converges: exit 0, no `[FAIL]` row, `.vibe-palace.toml` byte-identical, vault artifacts survive |
| `TestIntegrationE2EInitSkillShimFallbackReachable` | The persona shims' MCP-less `vp skills show <name>` fallback works verbatim, embedded then project-override; no shim names a vault path |

#### `test/e2e/dispatch/` → `cli_dispatch_test.go` (pattern: `^TestIntegrationDispatch`)

End-user-level verification that every parent-command exit-code contract
holds against the real binary. Extended, not replaced — the 3 pre-existing
tests keep their names; only the 4th (the former `dispatch/04-*.sh`) is new.

| Test | What it asserts |
|------|-----------------|
| `TestIntegrationDispatchParentBareShowsHelp` | `vp config` → help on stdout, empty stderr, exit 0 |
| `TestIntegrationDispatchParentUnknownSubcommand` | `vp config bogus` → `unknown subcommand "bogus"` on stderr, empty stdout, exit 1 |
| `TestIntegrationDispatchKnownSubcommandHelp` | `vp hook install --help` → help on stdout, exit 0 (two-word lookup regression guard) |
| `TestIntegrationDispatchBareParentExitDiscipline` | Fan-out check across `vault`, `commands`, `migrate`, `skills`, `archive` — bare exits 0 with help, unknown-subcommand exits 1 |

#### `test/e2e/githook/` → `TestIntegrationE2EGithook*` (pattern: `^TestIntegrationE2EGithook`)

| Test | What it asserts |
|------|-----------------|
| `TestIntegrationE2EGithookInitInstallsAndReapFires` | `vp init` installs the post-commit hook; a real `git commit -F commit.msg` without `&& rm` leaves no `commit.msg` on disk, even for a multi-paragraph message with trailing whitespace |
| `TestIntegrationE2EGithookCheckReportsMissingOnExistingClone` | A deleted hook is reported by `vp check`, repaired by re-running `vp init`, then reports clean — AND neither the ONNX embedder nor any agent CLI is ever constructed/exec'd (`testinfra.SentinelPATH` + a dead proxy, mirroring `TestIntegrationMigrateLoadsNoModel`) |
| `TestIntegrationE2EGithookInitRefusesForeignHookAndSharedHookspath` | A foreign post-commit hook is never touched (sha256-verified); a repo with `core.hooksPath` set is never written to at all; both refusals leave `vp init` at exit 0 |

#### `test/e2e/walkthrough/` → `TestIntegrationE2EWalkthroughHappyPath` (pattern: `^TestIntegrationE2EWalkthrough`)

A single, deliberately singular test mirroring `doc/TUTORIAL.md` Part 2
("Project Setup") end to end: project dir → load-bearing `git init` → `vp
init` → seed a drawer (via the inlined `seedDrawer` helper, not `vp
capture`, which is MCP-only) → `vp status` / `vp inject` (bootstrap JSON
`.project` non-empty) → a second project attached to the same vault without
reinitializing it (`vault/.git/HEAD` unchanged). Its stdout transcript is the
documentation artifact — if this test and Part 2 ever disagree, one of them
is wrong.

#### `test/e2e/workflows/` → `TestIntegrationE2EWorkflowsTuneRoomsLoop` (pattern: `^TestIntegrationE2EWorkflows`)

A two-iteration measurement loop: seed drawers → `vp tune rooms --export` →
two `vp tune rooms --apply` calls (idempotency via decoded-TOML-struct
equality, not textual — `toml.NewEncoder` output is not byte-stable across
round-trips). Hard-asserts cover exit codes, `report.json` decoding into
`palace.TuneReport`, `.Project == "proj"`, `.SamplesTotal >= 4`, and apply
idempotency; everything else (ms per command, sample/proposal/agreement
counts) is a tracked metric via a small JSONL emitter, never an assertion —
it depends on the mock LLM's canned response distribution, not `vp tune`'s
callable contract. The mock LLM itself is an in-process `httptest.Server`
(`internal/integration/e2e_helpers_test.go`'s `newMockLLMServer`), not an
exec'd binary.

**Fixed, not merely ported: the `metrics.jsonl` CI upload.** The retired
`test/e2e/workflows/run.sh`'s own success-path cleanup trap deleted
`$TMPROOT` — `metrics.jsonl` included — before `ci.yml`'s separate "Upload
workflows metrics on success" step ever ran, so that upload had been
silently uploading nothing on every green run since it was added
(`if-no-files-found: ignore` swallowed it). The Go port's
`workflowsMetricsDir` helper writes `metrics.jsonl` under a directory whose
retention policy depends on where it runs: under CI (`CI=true`, which GitHub
Actions sets on every job) it is a plain `os.MkdirTemp` directory with **no**
cleanup registered, so it is still there, unmodified, when the upload step
runs; outside CI it is `t.TempDir()`, which IS cleaned up automatically, so a
developer running `make integration` repeatedly over weeks does not
accumulate one leaked directory per run on their own machine. `ci.yml`'s
upload glob changed accordingly, from
`/tmp/vp-e2e-workflows.*/cases/*/metrics.jsonl` to
`/tmp/vp-e2e-workflows-metrics.*/metrics.jsonl`.

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

> **Known caveat (pre-existing, not a regression).** `go test ./internal/capture/
> -race` **without** `-short` times out at Go's 600s default deadline. The race
> detector's instrumentation overhead is pathological inside
> `gomlx/backends/simplego`'s parallel executor: `TestIntegrationCaptureLargeTranscript`
> (~32s un-instrumented) is still grinding through `simplego.(*FunctionExecutable).executeParallel`
> when the deadline fires. This was confirmed against a clean worktree at HEAD — it
> predates the lazy-embedder work and is not caused by it. Neither gate hits it:
> `make test` is `-short -race` (the ONNX tests skip) and `make integration` runs
> without `-race`. Do not combine `-race` with the ONNX tests in this package.

### `internal/search/` — Search Engine Tests

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
| `TestCollectNoteCorpus_IndexesBodyNotFrontmatter` | No | The **third** corpus source (`internal/search/notes.go`) indexes the BODY of `Projects/<p>/sessions/*.md` and nothing else — a frontmatter-only string must appear in **no** indexed text, since `vp_search_sessions` already serves frontmatter off disk. Also pins the synthetic location (`history` / `session-notes` / `narrative`, `source_type=session-note`) and that the wing is **not** a project slug, which `palace.DetectWing` returns for every transcript drawer |
| `TestCollectNoteCorpus_IDsUniqueAndStable` | No | Ids are unique across notes AND chunks and identical across two calls, so a rebuild reuses the embed cache; chunks of one note share a `source_ref` so search dedup keeps one hit per note |
| `TestCollectNoteCorpus_EmptyBodyContributesNothing` | No | An empty or whitespace-only body is skipped — an empty chunk in the index is a hit with no content. Non-vacuous: the same project with one real body yields exactly one row |
| `TestCollectNoteCorpus_NoSessionDirIsEmpty` | No | A missing `sessions/` directory is empty-and-nil, matching `collectIterationCorpus`'s missing-file convention rather than inventing a third one |
| `TestCollectNoteCorpus_UnparseableNoteIsSkipped` | No | A note whose frontmatter will not parse contributes nothing **without failing the rebuild**, matching what the iteration source does when `ParseEntries` yields no entries. An I/O failure still fails the whole rebuild, matching its `os.ReadFile` |
| `TestNoteCacheID_KeyedOnIdentityNotContent` | No | The honest residual, both halves: ids are `note.{project}.{stem}.c{i}` — keyed on IDENTITY, the opposite trade from `DrawerID = md5(wing+content)` — so a note chunk can never share a key with a transcript chunk, **and** two notes carrying identical text still produce two separate rows. The note corpus is additive and does not dedup against transcripts |
| `TestRebuild_NoteOnlyProjectIsSearchable` | No | The point of the change: a project captured as notes only — no transcript, no archive, no drawer store — becomes reachable from `vp search`, with `source_type=session-note`, the note's date, and a `source_ref` that navigates back to the file |
| 🔴 `TestRebuild_NoteCorpusWritesNoKnowledgeGraphAndNoDrawers` | No | **The load-bearing test, and it pins a ruling.** The note pass must never invent knowledge-graph facts from wrap prose. Achieved STRUCTURALLY — nothing calls `capture.IndexTranscript`, so `extractEntities` is not on the path — rather than by a boolean a later edit could flip. Asserts on the FILESYSTEM after a real `Rebuild` over a note-only project: no KG entity file, no `kg/` file anywhere under `palace/`, and no `drawers.jsonl` anywhere; re-asserted after a second rebuild. Non-vacuous: fails first if `NoteChunks == 0`. The prose is deliberately dense in the entity shapes capture's extractor keys on, so a KG write would have something to find |
| `TestRebuild_NoteChunksSurviveAlongsideIterations` | No | The note source is a THIRD corpus, additive rather than a replacement: a project with both iterations and notes gets `Indexed == IterationChunks + NoteChunks` and both hits come back |
| `TestSearchCrossProject_BuildFailureNamesTheProject` / `_UnreadableVaultIsAnError` | No | Widening the enumeration kept the error contract: one project that cannot be built fails the whole cross-project search naming it, and an enumeration that could not look is an error, never an empty result |
| `TestSearchCrossProject_CoversProjectsOnlyProject` | No | Cross-project search enumerates the union of both trees, so a notes-only project (no `palace/` store) is hit — and indexing it creates no `palace/<slug>/`. Reverting `ensureAllIndexes` to a `palace/`-only enumeration turns it red |
| `TestEmbedCachePut_NeverCreatesAProjectTree` (`cache_test.go`) | No | A `Put` on a slug with no tree lands at `palace/.local/embed-cache/<slug>/<id>.vec` and leaves `palace/<slug>` and `Projects/<slug>` absent. Pointing `path()` back at `LocalDir` turns it red |
| `TestEmbedCache_LegacyVectorsMigrateThenReembedOnce` (`cache_test.go`) | No | Vectors seeded at the legacy path are moved to the new path by the first cache operation, but they carry no embedding-regime fingerprint, so the first `Rebuild` re-embeds them once (`Embedded == 2`, `CacheHits == 0`) and heals away the emptied legacy `.local`. A fresh engine then serves them as hits (`Embedded == 0`, `CacheHits == 2`) |
| `TestEmbedCachePut_RetriesWhenItsDirectoryVanishes` (`cache_test.go`) | No | Through the `cacheWriteFile` seam, Put's first write finds its directory removed and fails with ENOENT; Put must re-create the directory, write once more, and serve the vector. Removing the retry from Put turns it red |
| `TestEmbedCache_SweepsOncePerInstance` / `_SweepFailureIsNotFatal` / `_RefusesInvalidSlug` (`cache_test.go`) | No | The sweep Once is per instance (a second op does not re-sweep; a fresh instance does); an unreadable `palace/` fails the sweep without failing the cache; an invalid slug is refused on every operation |
| `TestEmbedCache_ConcurrentInstancesConverge` (`cache_test.go`) | No | Six `EmbedCache` instances — each its own Once — sweep and `Put` at once under `-race`: every legacy vector ends at the new path with its bytes, every `Put` is readable, and the husks are healed |
| `TestCollectIterationCorpus_DegenerateCachedSummaryProducesNoSummaryRow` | No | A cached `IterationSummary` that passes `ok && MatchIndex == matchIndex` but renders to an empty string (`Summary`/`Decisions`/`Unblocks` all empty) must NOT set the raw row's `SummaryAvailable`: the flag must reflect actual summary-row emission, not the stale cache-hit precheck alone |
| `TestSearch_IterationRawHiddenByDefaultWhenSummaryExists` | No | Engine-level proof of the suppression fix: once an iteration entry has a summary row, default `Search` (`IncludeRaw` unset/false) never surfaces that entry's RAW row, even for a query term appearing only in the raw text and nowhere in the summary — the suppression is a hard filter, not a scoring nudge |
| `TestSearch_IterationIncludeRawRestoresRawAlongsideSummary` | No | `SearchFilters{IncludeRaw: true}` makes both the raw row and the summary row visible for the same entry, restoring exactly what the default path hides |
| `TestSearch_IterationRawStillDefaultVisibleWithoutSummary` | No | Recall regression guard: an entry with NO cached summary at all still surfaces its raw row under the default (`IncludeRaw` unset/false) — the common case, since most entries are never summarized |
| `TestSearch_NoteRawHiddenByDefaultWhenSummaryExists` | No | Mirrors the iteration suppression test for the note corpus: a session note with `meta.SearchSummary` set has its raw row hidden from default `Search` |
| `TestSearch_NoteIncludeRawRestoresRawAlongsideSummary` | No | Mirrors the iteration include-raw test for the note corpus: `IncludeRaw: true` restores both the note's raw row and its summary row |
| `TestSearch_NoteRawStillDefaultVisibleWithoutSummary` | No | Mirrors the iteration no-summary test for the note corpus: a note with no `SearchSummary` still surfaces its raw row by default |

### `internal/search/` — Recall Harness (`recall_test.go`)

A model-free harness that guards the **`bruteIndex` exactness claim** (100%
recall) against an independent ground-truth scan. It runs in the
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

**Scope (important):** this harness guards the **`bruteIndex` KNN data
structure** with synthetic vectors. It does **not** exercise the production
384-dim ONNX embedding path or `Engine.Search` — those are covered by the
real-embedder integration tests in `internal/search/integration_test.go`. The
harness retains a set-overlap recall@k metric (logged, not asserted here). The
approximate HNSW index has its own recall tests in the next section; the
recall bar on real vectors belongs to task
`hnsw-parameters-from-real-vector-recall-and-production-wiring`.

### `internal/search/` — Vector index contract and HNSW wrapper

`VectorIndex` has two implementations behind one contract: `bruteIndex`
(exact; production; the oracle) and `hnswIndex` (`github.com/coder/hnsw`
behind a safety wrapper, vendored under `third_party/coder-hnsw/` until the
upstream windows fix merges). Task `vector-index-interface-and-coder-hnsw-wrapper`.
Every test below was broken once on purpose and failed; the mutants are listed
in that task. No test asserts a duration: costs are counted (distance
evaluations through the test-only `countingCosine`, library searches through
`hnswIndex.librarySearches`). The library is NOT reproducible even with a seeded
`Rng` (`layer.entry()` ranges over a Go map), so no test pins an exact recall.

**Shared contract** (`vector_index_contract_test.go`, every case runs on both
implementations):

| Test | What it proves |
|---|---|
| `TestContractBuildDuplicateIDsLastWins` | `Build` deduplicates ids before adding anything; the last occurrence wins and `Len` counts distinct ids |
| `TestContractDuplicateThenDeleteLeavesNoGhost` | Deleting an id `Build` saw twice removes it entirely (brute force once kept the earlier duplicate searchable forever) |
| `TestContractKZeroIsEmpty` / `TestContractKNegativeIsError` | `k = 0` is an empty result; `k < 0` is an error |
| `TestContractUnusableVectorsAreSkippedAndLogged` | Zero, NaN and infinite vectors are never stored, by `Build` or `Insert`, and each skip is logged with its id |
| `TestContractExtremeNormVectorsAreUnusable` | A finite vector whose float32 squared norm leaves [2^-60, 2^60] (under- or overflow, or a pair whose norm product would) is unusable in `Build`, `Insert` and as a query, for both implementations alike |
| `TestContractUnusableQueryIsError` | An unusable query is an error (brute force once scored a zero query 1.0 against everything) |
| `TestContractUnusableInsertDeletesTheID` | Inserting an unusable vector for a stored id deletes it, so its older vector stops being searchable |
| `TestContractStoresACopy` | Mutating a slice after `Build` or `Insert` changes nothing stored |
| `TestContractWrongDimsIsError` | `Build`, `Insert` and `Search` refuse the wrong dimensionality |
| `TestContractInsertUpserts` | Inserting an existing id replaces its vector |
| `TestContractHugeKIsBounded` | `k = 1<<40` sizes no allocation by `k` (the HNSW request is capped at the graph size) |
| `TestContractImplementationsAgree` | On a corpus small enough for an exhaustive HNSW search, the two return the same ids in the same order, distances within `crossImplTolerance` (1e-5: float64 brute force against float32 SIMD cosine) |
| `TestNewIndexUnknownKindIsError` | `newIndex` refuses an unknown kind instead of falling back |
| `TestCandidateCountSaturates` | `limit*3` saturates instead of overflowing into a negative `k` |

**HNSW wrapper** (`hnsw_index_test.go`, `main_test.go`, `hnsw_rebuild_test.go`):

| Test | What it proves |
|---|---|
| `TestDistanceNameIsPinned` | `distanceName` is `vp-cosine-nanguard`, and `TestMain` found the library resolving it to `cosineNaNGuard` before any constructor ran (registration only in `init()`) |
| `TestGuardedDistanceSurvivesExportImport` | A graph built with the production distance exports, imports with the same function, and answers ten searches identically |
| `TestRegistrationDoesNotRaceExport` | Under `-race`: constructing indexes while others `Export` touches no unlocked library state |
| `TestCosineNaNGuard` / `TestNaNScoreGuard` | A NaN score becomes +Inf, and no non-finite result ever reaches the caller |
| `TestHNSWIdenticalReinsertIsNoop` / `TestHNSWUpsertTombstonesTheOldKey` | An identical re-insert makes no tombstone and no mutation; an upsert tombstones the old key rather than re-adding it (issue #15) |
| `TestHNSWSearchFillsKPastTombstones` | When tombstones crowd the first answer, `Search` widens its request until it has `k` live results |
| `TestHNSWConcurrentSearchDuringInsert` | Under `-race`: eight readers and one writer |
| `TestHNSWChurnSafety` | `-short`, under `-race` (≈1.3 s; 300 random 32-dimension vectors, shrunk from 2k × 384 at 19.8 s with both churn mutants still killed): 30% deletes, 20% re-inserts, colliding ids, and a tombstone rebuild started during the churn (asserted; deterministic, because the trigger runs synchronously in the crossing write). Searches are checked while the rebuild may still run and again after it finishes, and the index is closed. No panic, no deleted id, no stale vector, `Len` equal to the live count |
| `TestHNSWChurnRecallFloor5k` | `VP_HNSW_SLOW=1`, `make hnsw-slow`: after churn on seeded random 5k vectors, recall@10 at the provisional (16, 100) ≥ 0.44 (12-run minimum 0.4645 − 0.02), and at `EfSearch` 20 at least 0.02 below the floor, on every run. Random, not clustered: on the clustered corpus the `EfSearch` 100/20 gap after churn is 0.011 (0.9745 against 0.9635), under the margin, so only random data can catch `EfSearch` left at 20. This is the only test that asserts the `ef` gate. `make test` keeps no HNSW recall floor (ADR-014, operator ruling) |
| `TestHNSWTombstoneHeavySearchCost` | `VP_HNSW_SLOW=1`: on seeded random 10k vectors with rebuilds pinned off, 15% tombstones cost ≤ 2× the distance evaluations per query and ≤ 1 + ⌈log2(Len/k)⌉ library searches |
| `TestHNSWUpsertsAloneCrossTheThreshold` | Upserts alone trigger the tombstone rebuild; exactly one starts and the swapped graph holds no tombstone |
| `TestHNSWWritesDuringAHeldRebuild` | Writes made while a rebuild is held after its snapshot (more than `finalReplayMax`, so the swap first catches up off-lock) are all applied: a deleted id never returns, an upsert answers with its new vector, a second trigger starts no second build |
| `TestHNSWRebuildCancelledOnClose` | `Close` cancels a held rebuild and returns only after its goroutine has exited; it never swaps |
| `TestHNSWBuildCancelsARunningRebuild` | `Build` cancels a running rebuild, which never swaps its stale snapshot over `Build`'s contents |
| `TestHNSWRebuildAbandonedWhenWritesOutpaceCatchUp` | A writer landing more than `finalReplayMax` writes before every off-lock catch-up round (a flat-out writer, made deterministic by a hook) gets the rebuild ABANDONED: no swap, nothing replayed under the lock, the old graph still correct; the next rebuild waits out the backoff (`MinTombstones` writes), and a quiet rebuild then swaps within the lock budget and resets the backoff |
| `TestHNSWSmallRemainderReplayedUnderLock` | Writes within the lock budget (5 deletes, 5 upserts) skip the off-lock catch-up and are all replayed under the lock at the swap: `Len` is the live count, every live id is found at its own vector, no deleted id returns |
| `TestHNSWBuildResetsTheRebuildBackoff` | `Build` resets an inherited rebuild backoff, so the fresh graph's first threshold crossing rebuilds at once |
| `TestRebuildBackoffGens` | The backoff rule: `MinTombstones` writes (at least 1) after the first abandon, ×4 per further consecutive abandon, capped |
| `TestHNSWTombstoneMeasurement` | `VP_HNSW_MEASURE=1`, by hand, asserts nothing: the 0–20% tombstone table at 5k and 20k behind `TombstoneRatio`, then one rebuild under a flat-out writer (swap or abandon, and what the lock replayed) |

**HNSW recall measurement** (`hnsw_realcache_test.go`, `hnsw_harness_test.go`,
`hnsw_recall_measure_test.go`; task
`hnsw-parameters-from-real-vector-recall-and-production-wiring`). Every test
below was broken once on purpose and failed; the mutants are recorded in that
task.

- **Recall is tie-aware, at two k:** k = 10 and k = `candidateCount(10)` = 30,
  the engine's real request. Each returned id is first checked against its
  CURRENT vector, as `checkChurnedSearches` does: a non-live id, an id returned
  twice, or a reported distance that is not the distance to the id's current
  vector (a stale vector answered) fails. A hit is a result whose recomputed
  distance is within τ + 1e-5, τ being the k-th oracle distance; the epsilon
  covers ties only. Set-overlap recall@10 is logged for information only.
- **Held-out queries** are partitioned out before `Build`: a seeded permutation,
  the first 1,000 or 2% of the union as queries, never in the index.
- **Churn** is the shared `churn` helper (30% deleted, 20% re-inserted, 5%
  upserted, 1% colliding ids at `Build`), always with tombstone rebuilds pinned
  off. Its `replaced` record drives an old-vector probe: a replaced id searched
  by its old vector is absent or at its current vector's distance, never ≈ 0.
- **The real-vector source** is a scratch COPY of a host's embed caches, read
  raw through an `fs.FS`, never through `EmbedCache` (whose first use can
  delete vectors). `VP_HNSW_REAL_CACHE` is the trigger: unset, the real tests
  skip; set, a missing allow-list (`VP_HNSW_REAL_ALLOWLIST`, a file of slugs in
  scratch, never committed), fingerprint (`VP_HNSW_REAL_FINGERPRINT`, the
  expected `.fingerprint` text) or directory fails. The quantum projects
  `qa-metabuild-system` and `orchestrator` are excluded in code: an allow-list
  naming one refuses before anything is read, and the walk never opens a
  directory it skips. A file loads only as a `.vec` of exactly 1536 bytes with a
  usable vector; ids are `<project>/<stem>`.

| Test | Where | What it proves |
|---|---|---|
| `TestRealCacheSetButEmptyFails` | `make test` | The shared input check returns an error naming an empty or nonexistent cache once `VP_HNSW_REAL_CACHE` is set, and returns "no run" with no error when it is unset |
| `TestRealCacheInputsRequired` | `make test` | With the cache set, a nonexistent cache directory, an unset, missing or empty allow-list, an unset or blank fingerprint, and (for the grid) an unset `VP_HNSW_GRID_OUT` are each an error, never a skip |
| `TestRealCacheExclusionRefuses` | `make test` | An allow-list naming either quantum project refuses before the loader opens anything, and the input check refuses it too |
| `TestRealCacheSkippedDirsNeverOpened` | `make test` | An open-counting `fs.FS` records nothing under `orchestrator/`, `qa-metabuild-system/` or an unlisted directory; each is reported by name |
| `TestRealCacheDecoyTree` | `make test` | On the committed decoys (`testdata/hnsw-realcache-decoys/`): a non-`.vec` file, 1532- and 1540-byte `.vec` files, and an all-zero and a NaN vector of the right length are skipped and counted by reason; a wrong-regime project and a project with no `.fingerprint` are each skipped whole; a 32-hex stem loads as an opaque id |
| `TestRealCacheNamespacesIdsByProject` | `make test` | One stem in two projects loads as two ids, counts as one cross-project stem, and survives `Build` |
| `TestHNSWLibraryVersionMatchesGoMod` | `make test` | `hnswLibraryVersion` (`hnsw_index.go`, a graph-fingerprint input) equals go.mod's `require` pseudo-version for `github.com/coder/hnsw`, parsed with `golang.org/x/mod/modfile`, not the `replace` target |
| `TestTieAwareRecall` | `make test` | A tie at the k boundary counts as a hit where set-overlap undercounts; a stale vector (even 1e-3 off), a non-live id and a duplicate id are each an error |
| `TestTieAwareRecallMissBeyondTau` | `make test` | With every distance distinct (no tie at k or k+1), the (k+1)-th neighbour, 5e-4 beyond τ, or a far result, in place of the k-th scores 0.5, not 1; with fewer live ids than k, returning all of them scores 1 |
| `TestHeldOutQueriesNotInCorpus` | `make test` | Queries and corpus are disjoint by id AND by vector (each query is its own id's vector, and none equals a pool vector), the query count is min(1,000, 2%), and the built index holds no tombstone |
| `TestHNSWMeasureGridResumes` | `make test` | The grid driver skips cells recorded under the run identity, re-runs a cell recorded under an identity differing in any one component (ids, fingerprint, seed, query count, library version, harness version), truncates a torn final line, and appends each cell before the next starts |
| `TestHNSWClusteredChurnRecall50k` | `make hnsw-measure` | Seeded clustered 50k at the provisional (16, 100), churned: tie-aware recall at k = 10 and 30 ≥ 0.92 (lowest of 5 runs 0.9498 − 0.02, rounded down); no deleted id, no stale vector, no old vector. Breaks: the tombstone filter removed fails it, and so does `EfSearch` left at 20 (0.78–0.81 at k = 10, 0.87–0.90 at k = 30), unlike the 5k clustered gap |
| `TestHNSWHarnessSlice10k` | `make hnsw-measure` | A generated 10k cache tree plus the decoys through the real loader, the partition and both recall measures: decoys skipped and counted, no query in the index |
| `TestHNSWRealVectorRecall` | `make hnsw-measure-real` | The 50k assertions on a copy of real embed caches, against 0.90 at both k, with churn's fresh vectors from the union's reserve; fails, naming the shortfall, below a 50k corpus |
| `TestHNSWMeasureGrid` | `make hnsw-measure-grid` | The measurement itself, asserting nothing: M ∈ {16, 24, 32} × ef ∈ {100, 200, 400} × n ∈ {5k, 10k, 20k, 50k, all}, one subtest per cell, each result fsynced as a JSON line to `VP_HNSW_GRID_OUT` under its run identity, then the crossover table. The library has one `ef` for build and query |

**Targets.** All four HNSW test targets share one recipe (`hnsw_run_named` in
the Makefile): an anchored `-run` list, no `-race`, the output and wall time
printed, and a failure unless every listed test printed `--- PASS: <name>`, so
a filter that matches nothing or a test that skips cannot pass.

| Target | Gate | Tests | `-timeout` | Run by |
|---|---|---|---|---|
| `make hnsw-slow` | `VP_HNSW_SLOW=1` | `TestHNSWChurnRecallFloor5k`, `TestHNSWTombstoneHeavySearchCost` | 15m | the CI `hnsw` job |
| `make hnsw-measure` | `VP_HNSW_MEASURE=1` | `TestHNSWClusteredChurnRecall50k`, `TestHNSWHarnessSlice10k` | 30m | `.github/workflows/hnsw-measure.yml` (nightly and dispatch), and by hand |
| `make hnsw-measure-real` | `VP_HNSW_MEASURE=1` plus `VP_HNSW_REAL_CACHE`, `VP_HNSW_REAL_ALLOWLIST`, `VP_HNSW_REAL_FINGERPRINT` | `TestHNSWRealVectorRecall` | 2h | the operator |
| `make hnsw-measure-grid` | as above plus `VP_HNSW_GRID_OUT` | `TestHNSWMeasureGrid` | 6h | the operator |

A bare `VP_HNSW_MEASURE=1 go test ./internal/search/` runs
`TestHNSWClusteredChurnRecall50k`, `TestHNSWHarnessSlice10k` and
`TestHNSWTombstoneMeasurement` (the real-vector test and the grid skip without
`VP_HNSW_REAL_CACHE`), under go test's default 10-minute timeout, which a 50k
build can exceed: use the targets.

**Engine lifecycle** (`engine_index_lifecycle_test.go`):

| Test | What it proves |
|---|---|
| `TestEngineRebuildClosesTheReplacedIndexOffLock` | On both `Rebuild` paths (replace, and the empty-corpus drop) the old index is closed, its rebuild cancelled and exited, and `e.mu` is free when it closes (`beforeIndexClose` hook, `TryLock`) |
| `TestEngineCloseClosesEveryIndex` | `Engine.Close` closes every index before the embedder |
| `TestEngineCloseClearsTheBuiltMemo` | `Engine.Close` clears the lazy-build memo with the index map, so no project reads as built without an index |
| `TestEngineRebuildRunsOffLock` | A rebuild triggered by the engine's eviction (`evictMemory`, under `e.mu`) is held while the eviction returns and searches on the same and another project return; every wait is bounded at 60 s with a goroutine dump, never a timing assertion |
| `TestEngineSearchHugeLimit` | `Search` with `Limit = math.MaxInt` neither errors nor allocates by the limit |

**Vendored copy:** `scripts/check-hnsw-vendor.sh` (`make hnsw-vendor`, networked)
exits 0 when `third_party/coder-hnsw` is upstream at the required version plus
`vp.patch` (bytes and executable bits), 1 on drift, and 2 when the check could
not run. `make fmt` and `fmt-check` skip `third_party/`.

### `internal/indexstore/` — Host-local index store, ledger and locks

The per-host search index under `palace/.local/index/<p>/` (ADR-014): the two
index locks, the store change counter, the chunk store, the ingest ledger, the
commit steps, discards, graph writes and the orphan reaper. Task
`host-local-index-store-ledger-and-fingerprint`. Every test below was broken
once on purpose and failed; the mutants are recorded in that task.
Multi-process rows re-exec the test binary as a helper (`main_test.go`,
`runHelper`) and synchronise through files and exit codes; no test asserts a
duration (a 60 s liveness bound is a deadlock detector only). `TestMain`
installs a lock recorder over the whole binary that fails the run if any
goroutine nests two index commit locks or tries the run lock while holding one.

**Locks and the counter** (`runlock_test.go`, `tx_test.go`, `generation_test.go`):

| Test | What it proves |
|---|---|
| `TestRunLockIsNonBlockingAndNamesItsHolder` | While a helper holds the run lock, `TryRunLock` returns at once with `ok=false`, and `ReadHolder` names the helper's pid, kind, project and start time |
| `TestRunLockLivenessIsTheOSLock` | After the holder is SIGKILLed, the next `TryRunLock` succeeds although the holder record still names the dead pid |
| `TestHolderRecordIsAdvisory` | A deleted or corrupt holder record frees nothing; a record naming a live foreign pid blocks nothing |
| `TestHolderRecordLifecycleAndProgress` | `SetProgress` shows in the record; `Release` removes the record before it unlocks; `ReadHolder` takes no lock |
| `TestNoLostTrigger` | Both forced orders of a trigger against a releasing holder commit the trigger's archive exactly once |
| `TestRecheckSkipsWhatTheLastRescanSaw` | `ReleaseAndRecheck` re-acquires only for an archive its last rescan did not see |
| `TestLocksLiveInPalaceLocalLocks` | Both locks are files in `palace/.local/locks/`; nothing goes under `index/<p>/` or `.vp-locks/`; deleting `index/<p>/` frees nothing |
| `TestCommitLockTimesOutAndHonoursItsContext` | `Lock` with a zero timeout or a cancelled context returns at once without acquiring |
| `TestLockNoticesACancelDuringAFiniteWait` | A cancel during a finite wait is noticed during the wait |
| `TestCommitLockIsALeaf` | The binary-wide checker catches a nested commit lock and a run lock under a commit lock; no `*Tx` method calls `Lock` or `TryRunLock` |
| `TestRemovedProjectIsNotRecreated` | A project removed while `Lock` waits gives `ErrProjectGone`, and neither `index/<p>/` nor its counter is created |
| `TestGenerationTellsEachHolderWhatChanged` | `Tx.Generation` lets each holder see another process's write; a holder's own commit is not reported back to it, and one holder's lock consumes nothing for another |
| `TestWritesGoThroughThisFile` | No non-test file but `write.go` calls a raw `os` write or removal, and `write.go` only `os.OpenFile` for appends |
| `TestIndexstoreDoesNotImportSearch` | `go list -deps` of the package names neither `internal/search` nor `internal/capture` |
| `TestStoreCounter` | An empty `Tx` leaves the counter; an append moves `gen` only; a write that does more moves `gen` and the epoch; a recreated counter gets a new epoch |
| `TestGenerationFileFormat` | The counter file is `gen epoch`; malformed contents are an error to a reader |
| `TestMalformedCounterIsReplacedUnderTheLock` | A malformed counter is replaced under the lock with `gen` 0 and a new epoch, never wedging the project |
| `TestCounterAndHolderWritesAreFsynced` | Counter and holder writes fsync the temp file and, after the rename, the directory |

**Chunk store, ledger and commit steps** (`store_test.go`):

| Test | What it proves |
|---|---|
| `TestCollidingDrawerIDsAreTwoStoreChunks` | Two contents whose 32-bit drawer ids collide are two stored chunks |
| `TestPerArchiveWriteOrderAndKills` | Vectors, chunks, KG, then the ledger record (with chunk count and start day); a kill after each step leaves the session pending and invisible, and a rerun leaves no duplicate line |
| `TestSearchLoadsOnlyLedgeredRecords` | Unledgered archives and batches are invisible; note-owned chunks are visible |
| `TestSupersedeSteps` | The three supersede steps: shared chunk kept, private chunk deleted, note chunk untouched, generation 2, epoch changed; kills after steps 1 and 2 resume |
| `TestSamePathSupersede` | Owners are `source_sha256`, so a re-archive at the same path supersedes correctly, whatever the chunk order |
| `TestSupersedeAgainstAnIngestInAnotherProcess` | An ingest of the older archive in another process, forced first, loses its ownership to the supersede; forced after, it is refused |
| `TestSupersedeRefusesAReplacedArchive` | A supersede back to an archive recorded superseded is refused |
| `TestPendingFold` | Pending is: no record, a different live sha, or superseding to the archive |
| `TestReplacedArchivesAreNeverPending` | An archive replaced by a supersede, or recorded superseded, is never pending |
| `TestAFailureRecordIsNeverAnIngest` | A session with only failure records is not ledgered, stays pending, has no start day, loads nothing |
| `TestFailureCount` | Counts accumulate; only a rebuild proof clears them, keeping the failures of its own run; an ingest run cannot make a proof |
| `TestBaselineSet` | The baseline set: built once minus the trigger's archive, added to, cleared only by a proof, recreated by a discard; an addition creates no ledger |
| `TestChunkCountIsDistinctIDs` | The ledger's chunk count is distinct ids; `CountChunks` reports a shortfall |
| `TestStartDay` | Start days from session and batch records follow a supersede; a batch named after a session is refused |
| `TestSharedRecordDating` | A shared chunk's fields come from the live owner with the earliest day, in any ingest order; a batch's day comes from the ledger |
| `TestReplaceOwned` | `ReplaceOwned` adds, rewrites a re-dated owner line, removes and deletes; the epoch changes exactly when more than an append happened |
| `TestReplaceOwnedAppendsAgainstTheFileItRead` | The append branch indexes from the file it read, not a stale cache |
| `TestStoreCounterAcrossWrites` | Each kind of write moves the counter as specified (a destructive one by two); a failed append moves nothing; a failed destructive write has already moved it |
| `TestGraphWrites` | Graph writes move `gen` only, leave records byte-identical, and a failed one keeps the old graph |
| `TestDiscardScope` | Each discard removes exactly its files; a vector discard keeps the embed cache's sidecar |
| `TestDiscardChunksKilledPartWay` | A chunk discard killed after any step leaves no live session over missing chunks |
| `TestACrashedDiscardInAnotherProcessInvalidatesTheCache` | A discard that dies in another process still makes this process reload: no write from a stale cache |
| `TestALedgerIsNeverBegunByASessionRecord` | A ledger is only ever begun by its baseline |
| `TestTornFinalLine` | Readers skip a torn tail and stop their offset before it; the next append cuts it first |
| `TestAppendDoesNotRescan` | 10,000 appends read `chunks.jsonl` at most once (skipped under `-short`) |
| `TestTwoProcessAppendSameIDs` / `TestTwoProcessAppendDistinctIDs` | Two processes appending the same or distinct ids produce whole, deduplicated files |
| `TestCachedStateReloadsAfterAnotherProcessWrites` | A process's id set reloads after another process's append |
| `TestCacheIsDroppedAfterAFailedWrite` | A write that lands and then errors drops the cache |
| `TestAppendsAreFsynced` | Chunk, KG and ledger appends are fsynced |
| `TestCommitRefusesForeignIDsAndVectors` | A chunk id that is not `index.ChunkID` of its content, and a vector for no chunk, are refused |
| `TestStoreWritesLeaveTheTreeClean` | With the canonical `.gitignore`, `git status --porcelain -uall` is empty after every kind of store write |

**Reaper, legacy ledger and structure** (`reap_test.go`, `legacy_test.go`, `structure_test.go`):

| Test | What it proves |
|---|---|
| `TestReapCollectsAKilledArchiveLaterSuperseded` | The reaper drops a superseded archive's ownership, deletes what it alone owned, and unlinks those vectors |
| `TestReapCollectsVectorsASupersedePruned` | Vectors of chunks a supersede pruned are unlinked; stored chunks and other sources' ids keep theirs |
| `TestReapKeepsAPendingArchive` | A pending archive's and an unledgered batch's records and vectors survive |
| `TestReapWithNothingToCollectWritesNothing` | A reap with nothing to collect does not move the counter |
| `TestReapAbortsOnALiveSetError` | No vector is unlinked when the live set cannot be read |
| `TestReaperIsSafeAcrossProcesses` | A helper ingesting while this process reaps in a loop: every stored chunk keeps its vector |
| `TestReapWaitsForAnInFlightCommit` | A reap started between a commit's vector and chunk steps waits for the commit and keeps its vector |
| `TestDeleteLegacyLedgerIfUntracked` | The legacy ledger is deleted only when git tracks no drawer of the project, whatever untracked drawer files sit on this host's disk; kept in a non-git vault |
| `TestWritesNeedATx` | Every exported writer is a `*Tx` method or on a reasoned allowlist that names only real writers |
| `TestOnlyTheRebuildDiscards` | Outside this package nothing discards, clears or makes a rebuild proof (allowlist empty until the rebuild driver lands) |

**Elsewhere:** `internal/index` (`TestChunkIDIsWideAndHashesContentAlone`,
`TestIndexPackageIsALeaf`); `internal/vaultlock`
(`TestAcquireFileWithTimeoutOnANamedFile`,
`TestAcquireFileWithTimeoutHonoursItsContext`,
`TestAcquireWithTimeoutSharesTheLoop`, `TestAcquireWithTimeoutNegativeTriesOnce`);
`internal/atomicfile` (`TestWrite_FsyncAndDirFsyncAreObservable`,
`TestRemoveWithRetry`); `internal/storage` (`TestIndexPaths`,
`TestProjectExistsIsListAllProjectsMembership`); `internal/search`
(`TestReapOrphanVectors`: the engine's reaper, under the commit lock, keeps a
tracked drawer's and a stored chunk's vectors, unlinks the rest and evicts
them from memory; `TestRebuildSkipsTheReapWhileTheCommitLockIsBusy`: a busy
commit lock makes Rebuild skip its reap, without error and without waiting;
`TestReapRunsTheEmbedCacheCheckFirst`: the reap resolves the embed cache first,
so its one-time sweep and fingerprint check run even for a corpus-less
project; `TestReapFailsOnAnUnreadableRoom`: an unreadable room fails the
live-set read, and nothing is reaped; `internal/integration`'s
`TestIntegrationPulledDeletionLeavesNoHusk` pins the same sweep end to end);
`internal/tools`
(`TestRefreshIndexKeepsTheLegacyLedgerWhileDrawersAreTracked`,
`TestRefreshIndexDeletesTheLegacyLedgerOnceDrawersAreUntracked`).

### Importers write the frozen corpus (`importers-write-the-frozen-tracked-corpus`)

The shared prepare step:

| Test | What it proves |
|------|----------------|
| `internal/palace`: `TestPrepareMatchesTheGolden` | `palace.Prepare` on `testdata/prepare_fixture.md`, under the default config, yields the wings, rooms, halls, chunk indexes, contents, dates, entities and triples of `testdata/prepare_golden.json`; chunk ids are compared by content, since `Prepare` ids are `index.ChunkID` and the golden's are drawer ids |
| `internal/palace`: `TestPrepareDoesNoIO` | A temp vault root, `HOME` and `TMPDIR` are byte-identical before and after a `Prepare` call; the sourceaudit planner rule, which lists `Prepare`, is the static half |
| `internal/capture`: `TestIndexTranscriptMatchesThePrepareGolden` | `IndexTranscript` under the default config produced the committed golden, which pins that factoring `Prepare` out changed nothing |
| `internal/capture`: `TestIndexTranscriptChunksWithTheProjectsRecipe`, `TestIndexTranscriptClassifiesWithTheProjectsScoring` | `IndexTranscript` takes its chunk recipe and room scoring from `palace.ProjectIndexing`, configured through host config files |
| `internal/capture`: `TestWriteKGDoesNotAddPerEntity` | The KG write adds entities in one batch, not one call each |
| `internal/archive`: `TestSessionDayReadsTheTranscriptStart`, `TestSessionDayFallsBackToCapturedAt`, `TestClaudeSessionStart` | An archived session's day is the UTC day of its first timestamped record, else of `captured_at`, in every process zone |
| `internal/archive`: `TestSessionDayHasNoDecisionChunkCaller` | No capture, tools or palace code calls `SessionDay`: decision chunks take the ledger's day |

**Regenerating the golden.** The golden is captured from
`IndexTranscript`, not from `Prepare`, and committed as bytes. Regenerate it
only when a change to chunking, classification or extraction is intended:

```bash
go test ./internal/capture -run TestIndexTranscriptMatchesThePrepareGolden -update-golden
go test ./internal/palace ./internal/capture   # both must then pass
```

The vibevault importer (`internal/migrate/vibevault_archive_test.go`):

| Test | What it proves |
|------|----------------|
| `TestVibevaultWritesArchivesOnly` | An import writes archives and manifests, and no tracked drawer or KG file |
| `TestVibevaultArchiveDate` | An archive is dated by the session's `date:` at noon UTC, never the clock or midnight |
| `TestVibevaultBaseline` | On a host with a ledger, only the archives this run brings in join the baseline set; with no ledger the import creates none, and a ledger created later holds every archive in its baseline |
| `TestVibevaultBaselineBeforeCreate` | The baseline addition happens before the first archive is written |
| `TestVibevaultCrossHostIdempotence` | A second host that already holds the archives adds nothing to its baseline and writes nothing |
| `TestVibevaultKnowledgeDate` | `knowledge.md` takes its frontmatter date, else the fixed epoch, and counts as an archive, not a session |
| `TestVibevaultEmptySession` | An empty session gets a marker and no archive |
| `TestVibevaultReimportWritesNothing` | A re-run writes nothing |
| `TestVibevaultRefusesABatchIDSessionID` | A session id shaped like a MemPalace batch id is refused |
| `TestVibevaultLegacyMarkerIsReadOnceThenDeleted` | The old `palace/<p>/.local/` marker is migrated, deleted, and its empty directory removed |
| `TestVibevaultTrackedLegacyMarkerIsCopiedNotDeleted` | A legacy marker that git tracks is copied once and left in place: git shows no deletion, and a second run copies nothing more |
| `TestVibevaultDiagnosticMarkerIsWrittenOnce` | An undated session is reported on every run, but its `no_date` marker line is written once |
| `TestVibevaultADeletedAndRecreatedProjectIsImportedAgain` | The marker is a hint: with no manifest behind it, the session is archived again |

The MemPalace importer (`internal/migrate/mempalace_store_test.go`). The
rows that pin "exactly one batch record" count raw `"kind":"batch"` lines in
`ledger.jsonl`, because the ledger fold is last-wins and would hide a
duplicate.

| Test | What it proves |
|------|----------------|
| `TestMempalaceRefusesAnUnknownProject` | A slug in neither tree is refused and nothing is written |
| `TestMempalaceImportIsSearchableAndSurvivesTheSweep` | The import is found by search, and the index sweep keeps the store |
| `TestMempalaceImportIsLocalOnlyAndBatchOwned` | 2,500 drawers and 40 + 60 KG records land only in the local store, as 3 + 1 batches, every chunk owned by its batch |
| `TestMempalaceFinishesAHalfWrittenBatch` | A batch whose records were appended without its ledger record (seeded via `tx.Append`) is finished on the next run |
| `TestMempalaceReimportIsIdempotent` | A re-run embeds nothing and leaves the ledger byte-identical; a changed export gets new batch ids |
| `TestMempalaceReportsTheLedgerItCreated` | The import that creates the ledger reports it; one under an existing ledger does not |
| `TestMempalaceBatchStartDay` | The start day is the UTC day of the earliest drawer `filed_at` in every zone, else the epoch |
| `TestMempalaceStartDayReadsPythonIsoformat` | A zone-less `filed_at` with microseconds (`2026-03-15T10:00:00.123456`, Python's `isoformat()`) is read as UTC in every zone |
| `TestMempalaceStartDayFallsBackToValidFrom` | A KG-only export takes the earliest triple `valid_from`; with nothing parseable, the epoch, and the result names the source |
| `TestParseExportTime` | The accepted timestamp forms, and the ones refused |
| `TestMempalaceRefusesABatchIDThatNamesASession` | A batch id the ledger records as a session is refused (`ErrBatchIsSession`), never read as imported |
| `TestMempalaceWritesTheChunksFingerprint` | The first import records the project's chunk recipe; a later recipe change through config reads as a mismatch and the engine reports the project stale |
| `TestMempalaceCommitsUnderTheLockInBatches` | 2,500 drawers and 100 KG records are exactly 4 `CommitBatch` calls, each under a held Tx; no two commit locks are held at once; the import waits for a lock another process holds |
| `TestMempalaceRecheckUnderTheLock` | A batch committed between the pre-check and the lock is skipped, not committed twice |
| `TestMempalaceAbortsOnAnotherEmbedRegime` | Another embedding regime aborts with `ErrEmbedRegimeMismatch` |
| `TestMempalaceAbortsOnAnotherRecipe` | Another chunk recipe refuses with `ErrRecipeMismatch` and creates no ledger |
| `TestMempalaceSkipsBlankDrawers` | Blank drawers are counted and skipped |

Elsewhere:

| Test | What it proves |
|------|----------------|
| `internal/integration`: `TestIntegrationVibeVaultImportWritesArchives`, `TestIntegrationVibeVaultIdempotentReimport` | End to end, vibevault archives sessions and a re-run adds nothing |
| `internal/integration`: `TestIntegrationMemPalaceImportToSearch`, `TestIntegrationMemPalaceIdempotent` | End to end, a MemPalace import into an existing project is searchable and its KG readable through `kgread`; a re-run adds nothing |
| `internal/storage`: `TestDelete*` (`delLeftovers`) | Deleting a project removes its `palace/.local/imports/<p>/` marker |
| `internal/tools`: `TestVaultSplitPurge_RemovesSourceTreesAfterVerify`, `TestPurgeCommitsItsWholeResultInOneCommit` | A split departure purges the slug's `palace/.local/imports/<s>/` marker and leaves another slug's |
| `internal/storage`: `TestLegacyEntityLineWithPropertiesSurvives` | `Entity` has no `Properties` field, but a tracked line that carries `properties` still lists and is never rewritten |

### Index fingerprints, the migration marker and the lifecycle removal

Task `index-fingerprints-project-lifecycle-and-migration-marker` (ADR-014
decision 3, "the migration marker", and the host-local counterparts). Every
test below was broken once on purpose and failed; the mutants are recorded in
that task.

| Test | What it pins |
|---|---|
| `internal/surface`: `TestWriteFormat_KeepsTheMarker` | A format bump is a read-modify-write that keeps `authored_only` |
| `TestMarker_UnquotedDateAndWrongType` | An unquoted TOML date decodes; a wrong-typed marker is an error from `ReadVaultManifest` only, `ReadFormat` still answers, and `WriteFormat` refuses rather than drop it |
| `TestMarker_V8DecodeIgnoresIt` | A v8 `struct{Format int}` still decodes a file carrying the marker (regression guard) |
| `TestManifestBytes_UnchangedWithoutMarker` | With no marker the one encoder writes exactly `format = <n>`, so `isBornCurrentStamp` and a clean `vp init` are unchanged |
| `TestWriteVaultManifest_IsOneWrite` | Every field reaches disk in one write; a failed write leaves the old file |
| `internal/storage`: `TestVaultMigratedReadsTheKeyOnly` | The marker is the key, never the ignore lines; a malformed marker is an error |
| `TestIndexReapableIsProjectExistsAndThePendingKeep` | Reapable is `!ProjectExists` plus the rename-pending keep, never the embed cache's `Lstat` rule; a departed slug with palace residue that counts toward presence (a KG record) is kept, and one whose only residue is the derived ingest ledger is reapable (the presence rule's derived clause) |
| `TestIndexReapCandidates` | Only valid, non-dot, reapable directories; nothing under the embed-cache sweep's guards (empty listing, symlinked root) |
| `TestRenamePendingRecords` | The record path is slug-validated; a damaged record is an error |
| `internal/palace`: `TestRoomClassifierIsDeterministic` | Overrides merge in sorted key order: a tie between override-only rooms and the digest are the same over 200 builds |
| `TestRoomClassifierDigest`, `TestRecipePartsCoverTheirInputs`, `TestNormalizedIsWhatChunkUses`, `TestProjectIndexingReadsThePerProjectLayer` | The digest covers overrides and minimum score; each recipe part covers what its chunker reads and nothing else; `Normalized` is `Chunk`'s clamp; the recipe and classifier come from `LoadConfig(project)` |
| `internal/indexstore`: `TestMissingFingerprintIsUnbuilt`, `TestFirstChunkWriteWritesTheSidecar`, `TestNoRecipeNoFirstWrite`, `TestACommitNeverRewritesAMismatch`, `TestMismatchNeverDiscardsOutsideARebuild` | Missing is unbuilt; every chunk-writing step can write the first sidecar, none without a recipe; a commit never rewrites one; reading a mismatch changes nothing |
| `TestDiscardWritesTheFingerprintLast`, `TestDiscardWithoutARecipeRemovesNothing` | A discard killed at any step reads mismatch, never match; no recipe, no removal |
| `TestDeleteLegacyLedgerOnTheMarker` | The marker deletes the legacy ledger with drawers still tracked; a malformed marker keeps it |
| `TestLifecycleLockForm` | `LockLifecycle` takes a gone project's lock; `LifecycleTx` has exactly three methods (reflection); a held lock times out |
| `TestRemoveProjectReChecksUnderTheLock`, `TestRemoveProjectCrashPoints` | The removal re-checks under the lock; a crash leaves the whole store or the whole tombstone, the next sweep finishes, a re-created slug is unbuilt |
| `TestReapGoneProjectsRemovesUnderTheLock`, `TestReapGoneProjectsSkipsABusySlug`, `TestRenamePendingKeepOnTheRenamingHostOnly`, `TestRemoveGoneProjectOutcomes` | The sweep removes under the commit lock and keeps the counter and lock files, skips a busy lock, keeps a store only while this host's rename-pending record exists and reports a stale one; the delete/split entry point runs with the vault root lock free |
| `internal/search`: `TestIndexSweepIsNotInsideTheEmbedCacheSweep`, `TestEngineReapSweepsGoneProjectsIndexStores` | A cache's first `Put` under a commit lock does not run the index pass; the engine path reaps gone stores before its own `Lock` (the kept project's palace residue is a KG record: a derived file alone no longer makes a store) |
| `internal/tools`: `TestVaultSyncToolPullRemovesGoneProjectsIndexStore`, `TestSplitPurgeRemovesPurgedProjectsIndexStores`, `TestVaultProjectDeleteToolRemovesIndexStore`, `TestFingerprintNeverGatesAVaultWrite` | Pull and sync, the split purge and `vp_vault_project_delete` remove the gone stores (the delete lists them unhashed and reports `index_removal`); a mismatched fingerprint never blocks `vp_kg_add` |
| `cmd/vp`: `TestVaultPullAndSyncCLIRemoveGoneProjectsIndexStore`, `TestVaultProjectDeleteCLIRemovesIndexStore` | The same for `vp vault pull`, `vp vault sync` and `vp vault project delete` |
| Code review, 2026-10-03: `internal/search` `TestEngineSweepRunsBeforeTheCommitLock` | Through the `indexstore.ObserveCommitLocks` test seam: the engine's sweep never takes a gone project's commit lock while the engine holds one |
| `internal/index` `TestRecipeSumIsCanonicalOverCustomRooms`; `internal/palace` `TestRoomClassifierDigestCoversWeights` | Several custom rooms give one `Sum` over 200 builds; moving an override keyword from High to Low changes the digest |
| `internal/indexstore` `TestCommitArchiveWritesTheSidecarBeforeItsChunks`, `TestReplaceOwnedAndSupersedeWriteTheFirstSidecar` | The sidecar is down before any chunk line; ReplaceOwned and Supersede write a missing one |
| `TestDamagedRenamePendingRecordKeepsTheStore`, `TestTombstoneNameCarriesTheEpoch`, `TestCrashedRenameHolderDoesNotHideTheWarning`, `TestRemoveProjectWithNoStoreChangesNothing`, `TestMalformedMarkerKeepsTheLegacyLedger` | A damaged record keeps the store; the tombstone name is the new epoch; a crashed run's holder record does not hide the stale warning (the run lock is try-locked); no store, no epoch change and no counter; a malformed marker keeps the legacy ledger whatever git tracks |
| `internal/storage` `TestListRenamePendingSkipsDotFiles`; `internal/surface` `TestMarker_OnlyALocalDate` | A temp file in the records directory is skipped; a local time, local datetime or offset datetime is not a marker date |
| `internal/tools` `TestVaultProjectDeletePlanListsOnlyExistingStores`, `TestVaultProjectDeleteToolWaitsForABusyLock`; `cmd/vp` `TestVaultProjectDeleteCLIPrintsIndexRemoval` | The dry run lists only stores that exist; the MCP delete waits for a busy commit lock rather than trying once; the CLI text output prints the index_removal lines |

---

## Write/Wrap Surface Unit Tests

The restore-mcp-vault-surface work added two pure-unit packages (no ONNX,
run in `make test`). They underpin the vault-CRUD, commit, and wrap-state MCP
tools. (A third, `internal/mdutil` — markdown section editing — backed the six
surgical resume editors and was **deleted with them**; see *Resume-Editor
Lost-Update Tests* below.)

### `internal/vaultfs` — Vault File CRUD

Read / write / edit / delete / move / exists / sha256 over vault-relative
paths, plus the path-safety and stamping primitives. Tests cover the happy
paths, traversal/escape rejection and other safety guards, atomic-write
behavior, optimistic-concurrency `expected-sha256` mismatches, and
enumeration of the cross-package stamp writers.

`vaultfs.Edit` is now the **routine `resume.md` write path** (behind
`vp_vault_edit`), so its three loud failure modes are load-bearing rather than
incidental: `old_string` not found, `old_string` ambiguous without
`replace_all`, and `expected_sha256` mismatch. Each is an assertion that the
caller's model of the file is wrong — see *Blind-Overwrite CAS Tests*.

#### Export-destination containment (`internal/vaultfs/destination_test.go`, `cmd/vp/export_guard_test.go`)

`RefuseDestinationInsideVault` is the **inverse** of `ResolveSafePath`: that one
proves a vault-relative path stays UNDER the vault, this one proves an
operator-typed HOST path stays OUT. It backs `--export` on `audit rooms`,
`discover rooms` and `tune rooms`, and **`--to`** on `archive extract` — four
commands that previously handed a user path straight to `os.Create`, so an
export could overwrite a vault document with no lock, no stamp, no containment
and no CAS, and still report success.

| Test | What it proves |
|------|----------------|
| `TestRefuseDestinationInsideVaultRefusesAnExistingVaultDocument` | The base case: a live document under the vault root is refused |
| `TestRefuseDestinationInsideVaultSymlinkedRoot` | **The test that proves the predicate rather than the plumbing.** A lexical `HasPrefix` passes this wrongly. Asserted in BOTH directions — destination through the symlink with the root as realpath, and the mirror image |
| `TestRefuseDestinationInsideVaultDestDoesNotExistYet` | Parent-resolution rung: an export target that does not exist yet is judged by the directory it would be created in |
| `TestRefuseDestinationInsideVaultParentDoesNotExistEither` | Lexical `filepath.Clean` fallback still refuses when no part of the chain resolves |
| `TestRefuseDestinationInsideVaultSiblingPrefixIsAllowed` | `pathIsUnder` is separator-safe: `/x/vault-backup` is NOT inside `/x/vault` |
| `TestRefuseDestinationInsideVaultOutsideIsAllowed` | The guard does not refuse legitimate exports |
| `TestRefuseDestinationInsideVaultRefusesTheVaultRootItself` | Equality counts as inside |
| `TestRefuseDestinationInsideVaultRelativeDest` | **`filepath.Abs` on both sides is load-bearing.** `EvalSymlinks` preserves relativity and a relative candidate never prefix-matches an absolute root, so without `Abs` this fails OPEN — `--export Projects/x/resume.md` from inside the vault writes through |
| `TestRefuseDestinationInsideVaultUnresolvableRootFailsClosed` | Fail-closed: an unresolvable root errors rather than returning nil, and does NOT report as the inside-vault sentinel |
| `TestRefuseDestinationInsideVaultEmptyRootFailsClosed` | `filepath.Abs("")` succeeds as the cwd, so an empty root must be refused BEFORE `Abs` — otherwise the predicate silently answers a question about the working directory |

The CLI half pins the policy the predicate cannot express on its own:

| Test | What it proves |
|------|----------------|
| `TestAuditRoomsRefusesExportInsideVault` | Site 1 (`--export`) refuses, and asserts the target document was **not** overwritten — not merely the exit code |
| `TestDiscoverRoomsRefusesExportInsideVault` | Site 2 (`--export`) |
| `TestTuneRoomsRefusesExportInsideVault` | Site 3 (`--export`) |
| `TestArchiveExtractRefusesToInsideVault` | Site 4, whose flag is **`--to`**, driven through the real command so the flag name itself is under test. A `--export`-only suite would close the class at 3 of 4 |
| `TestAuditRoomsAllowsExportInsideVaultWithOverride` | `--allow-inside-vault` still permits a deliberate in-vault write |
| `TestGuardUnresolvableRootIgnoresTheOverride` | **The override never gates the predicate call.** With an unresolvable root the guard returns `ExitSystem` for `allow=false` AND `allow=true`, and the remediation must NOT name the flag — offering an escape there would write the fail-open back into the message |
| `TestGuardEmptyVaultRootIgnoresTheOverride` | Same rule for an empty vault root |
| `TestGuardOverrideStillPermitsAResolvableInsideVaultDest` | Both directions in one test, so tightening the cannot-verify path cannot silently kill the override |

Exit codes are part of the contract: `ExitUser` when the destination resolves
inside the vault (the operator can retype it), `ExitSystem` when the vault root
cannot be resolved (a config or runtime fault, with no override).

### `internal/wrapstate` — Wrap-State Collection

The engine behind `vp_collect_wrap_state` / `vp_stamp_iter` /
`vp_preflight_wrap`. Tests cover iteration parsing
(`^### Iteration (\d+)\b`), task-delta computation as a filesystem
set-difference against the snapshot, the `.vibe-palace/` anchor read/write
round-trip, the wrap-state record shape, the `doc/TESTING.md` headline
parse (the regexes this very headline feeds), and the preflight readiness
matrix.

The anchors are host-local (ADR-002's 2026-08-29 amendment), so a test that
walks the wrap window has to seed the snapshot `anchor_sha` — committing
`last-iter` in a fixture repo would exercise a path no real host takes.

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

## Template Provenance Tests

`vp config sync`, the upgrade reports, the reset verbs and the
`template-drift` check decide whether a vault `Templates/` copy is vp's by
its bytes alone (`templates.ClassifyVaultCopy`): the current embedded copy,
an earlier shipped version, or an operator's override. See
[ARCHITECTURE — The provenance decision table](ARCHITECTURE.md#the-provenance-decision-table).

### `internal/templates` — frozen shipped-version manifest

`internal/templates/shipped.txt` lists every version of every built-in a vp
binary could ever have written into a vault, frozen at the `1f3bb62`
last-writer boundary. **It is never regenerated**: there is no
`-update-golden`, no history walk, no corpus-coverage or append-only
ratchet, and no shallow-clone skip — so no test reads vibe-palace's own git
history, and none can pass vacuously in a shallow CI clone. Two tests pin
it:

| Test | Pins |
|---|---|
| `TestShippedManifestWellFormed` | a header naming the boundary and the derivation; every row `^[0-9a-f]{64}  \S.*\.md$`, a clean relative relpath, sorted by (relpath, key), unique; exactly two `# extra:` annotations, each directly before one of the two `pre-rebase-501c96e` tag rows and naming its blob OID; no CR in the file; every row parses |
| `TestShippedManifestIsFrozen` | the sha256 of the file on disk, and of the embedded copy, equals `frozenManifestSHA256`; the failure message says the file is frozen and that a change is a deliberate, reviewed edit that updates the constant in the same commit |

A template edit needs nothing here. The earlier-version tests elsewhere
(`cmd/vp`, `internal/integration`) read a committed fixture,
`internal/templates/testdata/earlier/commands/restart.md` — the version of
`commands/restart.md` before its current one in `1f3bb62`'s history — and
`TestEarlierFixtureIsAShippedVersion` pins that it classifies `earlier`.
`TestClassifyVaultCopy`, `TestProvenanceKey` and `TestShippedVersionParse`
cover the classifier, the CRLF key (one pass: `\r\r\n` becomes `\r\n`)
and the parser (a trailing `\r` stripped, a malformed line skipped).

To verify the rows once, reproduce the 172 non-tag rows by running the
derivation from the file's header in a full clone with tags fetched
(`git fetch --tags`). It fails closed — a revision that does not resolve is a
non-zero exit, never zero rows — and its output equals the file's rows minus
the two `# extra:`-annotated rows, which are carried verbatim and pinned by
blob hash alone (the tag they came from, `pre-rebase-501c96e`, was deleted
from `github` on 2026-09-14 and no longer resolves):

```sh
set -eu -o pipefail
revs="1f3bb62"
for r in $revs; do git rev-parse -q --verify "$r^{commit}" >/dev/null || { echo "derive: $r does not resolve (git fetch --tags)" >&2; exit 1; }; done
git rev-list --full-history $revs -- internal/templates/templates internal/context/templates \
 | while read -r c; do git ls-tree -r "$c" -- internal/templates/templates/ internal/context/templates/ || exit 1; done \
 | awk '$2=="blob" && $4 ~ /\.md$/ {sub("^internal/(templates|context)/templates/","",$4); print $3" "$4}' | LC_ALL=C sort -u \
 | while read -r oid rel; do s=$(git cat-file blob "$oid" | sha256sum) || exit 1; printf '%s  %s\n' "${s%% *}" "$rel"; done \
 | LC_ALL=C sort -k2,2 -k1,1 -u
```

## MCP Surface-Handshake Check Tests

The `mcp-surface-handshake` epic added the `vp check` surface row and the
`vp check --json` machine-readable report. These are pure-unit tests (no
ONNX, run in `make test`).

### `internal/surface` — the stamp, format and gate primitives

The leaf package under the check has its own unit tests
(`find internal/surface -name '*_test.go'`): `version_test.go` (reading and
writing the `.surface` stamp, monotonic and byte-identical across writers),
`format_test.go` (the `vault.toml` data-format axis, where absence means format 0), `gate_test.go` (fail-stop
versus warn-only enforcement and their bypass and quiet switches),
`guard_test.go` (a test write into a non-temp vault panics),
`staleself_test.go` (detecting a replaced running binary and the advisory it
prints), and `stranded_host_test.go` (a host on an older surface gets a message
naming both versions and a way out, and the remediation prose has one source).
`export_test.go` holds test-only reset seams and no tests.

### `internal/check/surface_test.go` — Surface Compatibility Check

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

### `internal/tools/search_tools_test.go` — `include_raw` on `vp_search` / `vp_search_cross_project`

Covers the MCP wiring for `SearchFilters.IncludeRaw` on both search tools,
using a real on-disk `iterations.md` plus a cached `storage.IterationSummary`
(via `vault.IterationsFile`/`WriteIterationSummary`, not
`seedDrawer`/`AppendDrawer`, since only the iteration-corpus collector sets
`SummaryAvailable`): `TestSearchToolIncludeRawDefaultHidesRawWhenSummaryExists`
and `TestCrossSearchToolIncludeRawDefaultHidesRawWhenSummaryExists` prove a
raw-only query term stays hidden by default once a summary row exists for the
same entry; `TestSearchToolIncludeRawTrueRestoresRawRow` and
`TestCrossSearchToolIncludeRawTrueRestoresRawRow` prove `include_raw: true` in
the tool's JSON params restores the raw row alongside the summary row for
`vp_search` and `vp_search_cross_project` respectively — the latter pinning
that the cross-project handler's `SearchFilters` literal (which carries no
`Project` field) also threads `IncludeRaw` through.

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

**The two host-state dependencies that used to leak are now seamed.**
`gatherCheckResults` (the unfiltered `vp check`) has two dependencies that
reach host state, and each goes through a seam: the Embedder row calls
`check.CheckEmbedder(newEmb)` with a closure over `newVaultEmbedder` (the
migrate seam, so `setupTestVaultEnv`'s default-on forbid guard covers every
`runCheck` caller), and the MCP host rows call
`check.CheckMCPHosts(mcpHostRegistry())`, where `mcpHostRegistry` is a package
var that production never reassigns. Before this, five check tests ran the real
`grok mcp list` against the developer's `~/.grok`, and two loaded the real
model. The rule: **a new dependency of `gatherCheckResults` that reaches host
state — spawns a binary, loads a model, touches the network — gets a seam.**
This is narrower than full hermeticity: the Git row still runs a real
`exec.Command("git", "-C", vaultPath, "remote")` (`internal/check/check.go`)
against the vault's own repo, and the stale-MCP-server row still reads the
real `/proc` (`listMCPProcessesFrom`, `internal/check/stale_mcp.go`). Both are
accepted, pre-existing scope — not seamed, and not a regression.

Every full-suite test runs in one of two helpers (`check_testenv_test.go`):

- `healthyCheckEnv(t)` — `setupTestVaultEnv` (sandboxed `HOME`, config and
  cache dirs), a `Projects/` dir, a temp cwd whose `.vibe-palace.toml` names
  project `checktest` under `[project]`, `stubVaultEmbedder(t,
  embedder.NewMock(384))`, and the host stub. The config names no
  `git_enabled`, and `VaultReconciler.gitEnabled` reads it through
  `storage.HostGitEnabled`, where an absent key means enabled, so the Git row
  checks the vault's repository (the fixture vault is not one: "vault is not a
  git repository").
- `unconfiguredCheckEnv(t)` — `initTestEnv(t, false)` (no global config),
  `forbidVaultEmbedder`, a temp cwd, and the host stub.

The host stub returns `e.Hosts` (nil by default, so the report carries the
single `MCP hosts` Skip row) and counts into `e.HostCalls`. Like the embedder
helpers it sets `VP_TEST_MCP_HOST_SEAM` through `t.Setenv` before swapping, so
a parallel caller panics instead of racing.

| Test | What it proves |
|------|----------------|
| `TestCheckCommand` | Human/JSON parity in two envs — healthy, and healthy plus a vault-root `.gitattributes` naming the `vp-surface` merge driver (exactly one Fail): the human `[FAIL]` count equals `summary.fail`, and the human exit is `ExitUser` exactly when `exit_code` is 1. Mutating `runCheck`'s `n > 0` to `n > 1` turns the merge-driver row red |
| `TestCheckFullSuiteReportsInjectedMCPHosts` | The MCP host rows come from `mcpHostRegistry`: a registered fake reads `pass`, an unregistered one `info` naming `vp mcp install --<flag>`, and the registry is asked once |
| `TestCheckFullSuiteRoutesEmbedderRowThroughSeam` | The Embedder row is `pass … 384 dimensions` with one construction; a stub whose `Dimensions` errors gives `fail` and `exit_code` 1; `http_port = "x"` (Config passes, Settings fails) skips the row with zero constructions |
| `TestFullCheckExecsNoAgentCLI` | The lock. Recording `sh` sentinels for every name in `mcphost.Host.Executables()` over `Registry()` go first on `PATH`; the full suite must leave the log empty, ask the registry once and construct the embedder once |
| `TestCheckEnvHelpersStubHostRegistry`, `TestStubMCPHostRegistryRefusesParallel` | Both helpers install the host stub and restore the real registry; the stub refuses `t.Parallel` before swapping |

**What the lock does and does not catch.** The two counts catch a revert of
either seam: the real registry or a direct ONNX construction leaves them at
zero. The sentinels catch a **new** exec route that bypasses the stubbed
registry, for example a future check that runs `grok --version`. They do not
catch a route through an agent CLI that no host reports; a package-wide
sentinel `PATH` belongs to `test-infra-consolidation-ephemeral-vault`. The
sentinels are `sh` scripts, so the test skips on Windows, whose CI job does not
run `cmd/vp`.

The sentinel names come from what each host actually runs, not from its
`Name()`: `Host.Executables()` is the **source** of the names a host looks up
and runs — `NewGrokHost` and `NewZedHost` pass `h.Executables()[0]` to
`exec.Command` / `exec.LookPath` — so the name executed and the name reported
cannot drift, and production calls the accessor (no source-audit baseline entry
needed). `internal/mcphost/executables_test.go`'s
`TestHostExecutablesMatchWhatTheyRun` parses the package's non-test files with
`go/parser`, attributes every `exec.Command` / `exec.CommandContext` /
`exec.LookPath` to the host type whose method or `New<Type>` constructor holds
it, requires the binary argument to be exactly `<ident>.Executables()[i]`, and
asserts each `Registry()` host's used indexes cover its `Executables()`. A call
in a free function, in a non-host type, an uncalled reference (`lookPath:
exec.LookPath`), or a binary taken from anything else — a string literal, a
const, a variable — fails with its file and line. `ClaudeHost` delegates to `internal/plugin`, which has no
`os/exec`, so its empty set is proven by the scan. `grok_exec_test.go`'s
`TestNewGrokHostExecsGrokOnPATH` covers `NewGrokHost`'s real exec closures
against a scripted `grok` with `PATH` set to its temp dir only and `HOME`
empty: list names the server → detected and installed; list exits 1 → `(false,
nil)`; no `grok` and no `~/.grok` → not detected, nothing executed.
`zed_test.go`'s `TestNewZedHostLooksUpZedOnPATH` does the same for
`NewZedHost`'s lookup (a `zed` file on a temp-only `PATH`, never executed).

The `resume-caps` producer is covered against a seeded temp vault holding one
over-every-cap project: `--check resume-caps` human output (the `[info] Resume
caps` row plus all three breach strings; asserts no embedder load and no Surface
row), its `--json` projection (exactly one `Resume caps` check, `status: info`,
`summary.fail == 0`, **`exit_code 0`** — a cap breach warns and must never fail
the run), and the no-vault-resolved path degrading to `Skip`.
`cmd_version_test.go` covers `vp version --surface` (`surface: <N>`).

The **dirty-build stamp** (iter 321) is covered in two places, and the split is
deliberate. `internal/cli/version_test.go` tables `BuildInfo.String()` over
dirty / explicitly-clean / unstamped, plus `TestBuildInfoIsDirty` pinning that
only a positive stamp counts — an unstamped build is neither dirty nor clean.
`internal/integration/version_dirty_test.go` covers what a unit test cannot:
`TestMakefileDerivesDirtyFromVersion` drives the real Makefile with `make -n
install VERSION=<x>` to prove the marker is derived from `$(VERSION)` and from
no second git call, and `TestStampedBinaryReportsDirty` builds a real binary
with explicit ldflags and reads `vp version`. That one deliberately does **not**
reuse `buildVPBinary`: that helper caches one binary per process behind a
`sync.Once` and builds with no ldflags, so it could never carry a stamp.

The load-bearing case is the **dirty** one. A clean-only assertion is satisfied
by a stub that always reports clean, which is exactly the bug this replaced.

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
golden's `surface_version` is `MCPSurfaceVersion` in
`internal/surface/version.go` (read it there, or `jq .surface_version
internal/mcp/tool_surface.golden.json`); derive the tool count and mutating
split from the golden itself — `jq '.tools | length'
internal/mcp/tool_surface.golden.json` and
`jq '[.tools[] | select(.mutating)] | length' …` — recorded counts rot, and so
did the version literal that stood here through two bumps.

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

## Palace Store Presence Tests

A `palace/<slug>/` is a store only if it holds a regular file outside its
top-level `.local/` (see ARCHITECTURE, "What counts as a palace store"). The
tests pin the predicate, its one consumer on each side, and the instrument that
reports the complement.

### Marker-gated tidy, pull and audit (task `tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`)

Toy-repo fixtures only (`marker_fixtures_test.go`): a "migrated" vault is the
marker, the derived ignore lines and the drawers untracked and deleted in one
commit; "migrated then reverted" adds `git revert`. None runs the migration.

| Test | What it proves |
|---|---|
| `internal/surface`: `TestParseVaultManifest_SameAsTheFileReader` | The bytes parser the git readers use and the file reader agree, and a malformed marker names `authored_only` |
| `internal/storage`: `TestVaultGitignore_LinesNeverPrecedeTheMarker`, `TestVaultGitignore_RevertedVaultStaysClean` | No derived ignore line reaches a vault without the marker through either reconciler path, and a dirty tracked drawer still tidies |
| `TestVaultGitignore_MigratedVaultGetsTheLines`, `TestVaultGitignore_MalformedMarkerFailsBeforeAnyWrite` | With the marker both paths add exactly the two lines; an unreadable marker fails every writer before it writes |
| `TestCommitVaultInit_MigratedTopUpIsCommitted`, `TestCommitVaultInit_MalformedMarkerFailsAndCommitsNothing` | `vp init` recognises its own top-up of the derived lines and leaves the vault clean; a malformed marker commits nothing |
| `internal/reconcile`: `TestVaultReconcile_CreateBranchIsNotBornMigrated`, `TestScaffoldNewVault_IsNotBornMigrated`, `TestVaultReconcile_TopUpFollowsTheMarker`, `TestVaultReconcile_MalformedMarkerWritesNothing` | Born migrated is `InitVault`-only; the reconciler's top-up follows the marker; a malformed marker is a Skip naming the key |
| `internal/storage`: `TestInitVault_BornMigrated`, `TestInitVault_ResumedIsBornMigrated` | A new vault's first commit holds the marker, the lines and `Audits/.surface` at `surface.MCPSurfaceVersion` (symbolic, never a literal 9), fresh and resumed through both init seams. `TestInitVault_AdminStep1TwoRemotes` now lists `Audits/.surface` among the tracked files |
| `TestStageInBatches_DropsATrackedFileUnderAnIgnoredDirectory`, `TestStageInBatches_UnmigratedDrawerIsStaged`, `TestStageInBatches_OneCheckIgnorePerBatch` | The staging guard drops a tracked file under an ignored directory (which `GitPathIgnored`, consulting the index, calls not ignored) and logs it, drops nothing on an unmigrated vault, and costs one `check-ignore` per batch. `TestStageInBatches_MagicLookingNamesAreNames`: names like `:!x.md` stage as themselves through `checkIgnored`'s `./` prefix. `TestCommitAndPushPaths_DoesNotCommitADroppedTrackedDrawer`, `TestCommitAndPushPaths_AnUntrackedDroppedDrawerDoesNotFailTheCommit`: a path the guard drops leaves the commit too. `TestCommitAndPushPaths_ReportsAnIgnoredPathAsSkipped`: a tracked authored file under a user's ignore line is reported in `SkippedPaths` with `SkipIgnored`, never dropped silently. `TestCommitRemovals_DropsAnIgnoredRemoval`, `TestPruneMirrors_KeepsAnIgnoredMirror`: the same drop at `CommitRemovals` (reported as skipped) and the mirror prune (kept, restored from HEAD, not left deleted) |
| `TestTidy_MigratedTriplesByOrigin`, `TestTidy_UnmigratedTriplesAllSwept`, `TestTidy_MigratedUnreadableTripleIsReported`, `TestTidy_MigratedEntitiesLineDiff`, `TestTidy_MigratedDrawerIsNotSwept`, `TestTidy_MalformedMarkerFailsTheRun` | On a migrated vault tidy sweeps only authored KG records (`ClassifyTriple`, `ClassifyEntityLine` over the lines added against HEAD), reports unreadable ones without erroring, never sweeps a drawer; a malformed marker fails tidy |
| `TestPresence_SameAcrossHosts`, `TestPresence_BeforeTheMarker`, `TestPresence_IgnoresTheLockDirectory`, `TestPresence_KGClause`, `TestPresence_MalformedMarkerIsInclusiveAndLogged` | The presence rule: derived files never count (before the marker too), untracked `kg/` residue does not count once migrated, the two callers still partition `palace/`; a malformed marker takes the inclusive rule and logs a line naming `authored_only` |
| `TestHeal_LaggingHostPullsTheMigration`, `TestHeal_MigratedHostMergesALaggingDrawerCommit` | The heal: `UD` through `pullCore` (result overwritten, `SyncVault` pushes, no `healed/`), and `DU` through the push-rejection reconcile, the commit-and-push reconcile and the mirror prune — the marker read from the merged index, not `MERGE_HEAD` |
| `TestHeal_FastForwardOfTheMigrationNeverCallsTheHeal`, `TestHeal_RefusedMergeIsLeftAlone`, `TestHeal_MixedConflictIsNotHealed`, `TestHeal_NonDerivedConflictStillAborts`, `TestHeal_UnmergedManifestFailsClosed`, `TestHeal_NonASCIIDerivedPath`, `TestHeal_EntitiesBothChangedIsNamed`, `TestHeal_MalformedMarkerInTheMergedTree` | No heal without unmerged paths; all-or-nothing; a non-derived conflict still aborts; an unmerged `vault.toml` is an error, never "unmigrated"; names survive non-ASCII; a both-changed `entities.jsonl` is named, not resolved; a malformed marker in the merged tree fails both sites closed |
| `TestUntrack_CleanMergeReTracksADrawer`, `TestUntrack_UnmigratedCleanMergeUntracksNothing`, `TestUntrack_RetriedOnAnUpToDateRun`, `TestUntrack_DrawerRewrittenBeforeTheCommit`, `TestUntrack_FailureRestoresTheIndex`, `TestUntrack_MalformedHEADManifest` | The post-merge untrack at both sites keeps the file on disk, runs on an up-to-date merge too, commits from the index (a rewrite in between is harmless), restores the index on failure, and fails closed on a malformed marker |
| `TestIsDerivedPath`, `TestPull_IsTheWayOutOfAMalformedMarker` | The derived predicate needs a valid slug segment; `vp vault pull` still repairs a malformed marker that makes `SyncVault` refuse |
| `internal/vaultaudit`: `TestKGTrackedExtracted_ReportsOnAMigratedVault`, `TestMarkerAuditChanges_OffWithoutTheMarker`, `TestPalaceStoreDrawers_SkippedWhenMigrated`, `TestMarkerAuditChanges_MalformedMarker` | `kg-tracked-extracted` reports tracked extracted records only with the marker (`kg-portability` unchanged); `palace-store-drawers` is skipped, with its reason, only with the marker; a malformed marker is one finding naming the key |

Fixtures elsewhere that built a "real store" from a drawer or the ingest ledger
alone now use a KG record or a `.surface` stamp, because a derived file no
longer counts toward presence: `newDivergentVault` (`TestListAllProjects_UnionOfBothTrees`),
`TestDepartedCaches_ListedStoreKeepsItsCache`, `TestEngineReapSweepsGoneProjectsIndexStores`,
`TestIndexReapableIsProjectExistsAndThePendingKeep`, and `seedArchiveDrawer` in
`internal/vaultaudit/archive_test.go`.

### `internal/storage/projects_test.go` — the presence rule

`TestPresenceRule_PalaceStoreNeedsAFileOutsideLocal` enumerates every shape:
`.local`-only (an embed-cache husk, an `imported-sessions.jsonl` marker), a bare
directory, an empty `drawers/` or `kg/` subtree, `.local` plus an empty
subtree, and derived files alone (a drawer, the `ingested-archives.jsonl` ledger,
drawers plus `.local`) are **not** stores; a `kg/` file (zero-length too), a lone
`.surface`, a nested (non-top-level) `.local/` file and a `drawers/` that is not
the top-level one **are**; `palace/.local` is never a project; and `ListAllProjects` agrees with `listPalaceStores`, the only
path palace/ is enumerated through. Deleting the
predicate turns the `.local`-only case red. `TestPresenceRule_UnreadableDirCountsAsStore`
pins that an unwalkable directory is counted rather than dropped, and does not
fail the enumeration. `TestPalaceNonStores_IsTheComplement` asserts that
`PalaceNonStores` and `InPalace` partition `palace/`, and pins the per-directory
summary (`.local/` entries with file counts, empty subtree, `Projects/` presence);
the remaining `PalaceNonStores_*` tests cover an absent `palace/`, an unreadable
one (an error), and an uncountable `.local/` subtree (reported as `-1`).
`TestTrackedPalaceLocalFiles*` pins the one `git ls-files` behind every "tracked"
claim: it counts only files under a slug's top-level `.local/` (not untracked
ones, not `palace/.local`, not a nested `.local`), an empty map outside any
repository, an error for a `.git` git cannot read, and — with `GIT_DIR`,
`GIT_WORK_TREE` and `GIT_INDEX_FILE` pointing at another repository — still the
vault's own answer. The
`newDivergentVault` fixture seeds real files, because a bare `palace/<slug>/` is
no longer a store.

### `internal/check/palace_local_only_test.go` — the `palace-local-only` row

Pass on a clean vault and on one with no `palace/`; one Info row per non-store
shape with its exact detail line (`embed-cache/ (2 files)`,
`imported-sessions.jsonl`, `(empty)`, `(empty subtree only)`, `Projects/<slug>/:
present|absent`); `TestCheckPalaceLocalOnly_PrescribesNoDisposition` matches
`rm`, `rmdir`, `delete`, `deletion`, `remove`, `removal`, `discard`, `purge`, `wipe`,
`leftover`, `residue`, `clean up` and `prune` on **word boundaries**, so a slug such
as `platform` cannot trip it; the tracking line claims "not tracked" only when git
was asked and said so — `_TrackedFilesAreNamed` commits a `.local/` file and
requires its count on that directory's line, `_TrackingUnknownMakesNoClaim` gives
git a broken `.git` and requires the could-not-check wording, and
`_EmptyDirsNeedNoGit` covers the case git cannot carry at all;
`TestCheckPalaceLocalOnly_NeverWrites` hashes the whole tree before and after; an
unreadable `palace/` is Info, never Fail; and the registry producer skips with
`no vault configured` like every vault-scoped producer.

### `internal/storage/embedcache_sweep_test.go` — the one-time layout sweep

`SweepEmbedCaches` moves legacy `palace/<slug>/.local/embed-cache/` caches to
`palace/.local/embed-cache/<slug>/`, heals the directories that leaves empty, and
reaps caches for slugs in neither tree. Pinned: the move is byte-identical; a
merge into an existing target never overwrites (`os.Link` refuses, the new-layout
vector wins, the legacy copy is counted `Dropped`); a husk heals; **crash
states** — an empty `.local` left after the rename, and a half-drained legacy
directory — are finished by the next run; a `palace/<slug>/` holding drawers,
`kg/`, `.surface` or `.local/imported-sessions.jsonl` is never removed; a
directory without `.local/` is never touched even when empty; a second run and a
vault with no `palace/` are no-ops; an unreadable `palace/` is an error. Reaping:
an orphaned cache is removed while one belonging to a project in either tree
survives (the fixtures seed a keeper project); `TestSweepEmbedCaches_ReapGuards`
pins that **zero projects** and an **enumeration error** each reap nothing, and
that a non-vector file blocks its directory's removal. `_NeverFollowsSymlinks`
covers a symlinked `palace/<slug>/` pointing at a real store with a cache inside
(untouched, and its new-layout cache not reaped) and a symlinked cache
directory. `_OddShapesAreLeftAlone` covers a file where the legacy cache or the
target should be, and a non-regular entry inside a legacy cache.
`_FailuresLeaveTheLegacyCacheInPlace` drives each failure branch with file
modes — an unstatable or unwritable target, an unreadable or unwritable legacy
directory, a file where the cache root goes, a heal blocked by permissions — and
asserts each is reported per slug and leaves the legacy vector where it was.
`paths_test.go`'s `TestEmbedCacheDir` pins the path and the slug validation.
`_ConcurrentSweepsConverge` runs six sweepers alongside writers that do exactly
what `EmbedCache.Put` does — `MkdirAll`, `WriteFile`, one retry on ENOENT —
checks every write's error, and requires every written vector present with its
bytes, no legacy cache, husks healed and real stores intact. It clears `PATH`
so the tracked-file probe spawns no git: a git process delays each sweeper long
enough that the writers finish first and the rename-over-an-empty-directory race
is never reached. Measured on 2026-09-10: with the writers' retry removed it
fails 72 of 1000 runs; with it, 0 of 1000 without `-race` and 0 of 100 with
`-race`. (Real `EmbedCache` Puts cannot be imported here; `search`'s
`TestEmbedCache_ConcurrentInstancesConverge` races them.)

Git tracking: `_TrackedLegacyIsLeftAlone` commits one slug's legacy vector and
requires that slug untouched and reported while an untracked slug beside it
migrates; `_OutsideARepositoryNothingIsTracked` covers a non-repository and a
repository that ignores `palace/*/.local/`; `_GitFailureSkipsTheMigration` gives
git a broken `.git` and requires stage 1 skipped (and reaping still run). Through
test seams for `os.Rename`/`os.Link`: `_CopyFallbackWhenLinkAndRenameAreRefused`
(an EXDEV-style refusal of both still heals the husk by copying, with no temp
file left, the new-layout copy winning, and copied vectors at mode 0644);
`_RemovesOnlyStaleCopyTemps` (a crash's hour-old `.sweep-*.tmp` is collected, a
fresh one a running sweep may own is kept, a look-alike name is kept, and an
orphan held only by a stale temporary is then reaped); `_OneFailureDoesNotStopTheMerge`;
and `_TargetVanishingMidMergeIsRecreated` (the branch a concurrent reap
reaches). `_MergeLeavesForeignFilesAndSymlinkedTargets` pins that only regular
`*.vec` files move and a symlinked target is never followed.
`_NeverFollowsSymlinks` also covers a real `palace/<slug>/` whose `.local` is a
symlink (turning the `Lstat` into `Stat` turns it red). The reap:
`_ReapNeedsAProjectsTree` (a dangling `Projects/` reaps nothing),
`_ReapKeepsAnythingThatExists` (a slug that exists only as a dangling symlink
keeps its cache), and `_ReapKeepsAProjectBornDuringTheSweep`, which pins through
the cache-read seam that a project appearing just before the read keeps its
cache. That property used to depend on reading the cache before listing
projects; with the per-slug existence check, swapping the two reads is an
equivalent mutant, so the test pins the property rather than the order.

### Split, merge and copy between migrated vaults (task `split-and-merge-exclude-derived-palace-paths`)

Split's destination is now made the way an operator makes one: a v9 `vp vault
init` (`splitInitDest`, against a local bare remote). The split, merge and copy
suites write the migration marker into both vaults (`splitFixtureVault`,
`formatManifest`, the copy tool and CLI fixtures). Tests that assumed split
creates its destination now assert the destination is unchanged instead.

| Test | What it proves |
|---|---|
| `internal/storage`: `TestDerivedResidue_TrackedDrawerIsNever`, `TestDerivedResidue_PathAndUntrackedAndIgnored`, `TestDerivedResidue_RechecksEveryLine`, `TestDerivedResidue_OneGitCall`, `TestDerivedResidue_GitFailureIsAnError`, `TestDerivedResidue_NotAGitVault` | The residue is untracked AND ignored AND derived (never a tracked drawer, never an ignored `*.bak`), every listed line is re-checked with `isDerivedPath`, one git call for all slugs, and a git failure is an error |
| `internal/tools`: `TestVaultSplit_RefusesEveryPairingButMigratedIntoMigrated`, `TestVaultMerge_RefusesEveryPairingButMigratedIntoMigrated`; `internal/storage`: `TestCopy_RefusesEveryPairingButMigratedIntoMigrated`, `TestCopy_ReadsTheSourceMarkerAtTheTip` | Each command refuses every pairing but migrated into migrated, and an unreadable marker, before any write (plan, apply and verify for split and merge; the dry run lists copy's refusal); copy reads the source marker at the tip |
| `TestVaultSplitApply_IntoAVaultInitDestination`, `TestVaultSplitApply_RefusesAnUnsafeExistingDestination`, `TestVaultSplitApply_RefusesADestinationInsideAnotherRepository` | Split copies into a `vp vault init` destination end to end (its remote and `Audits/.surface` pass verify, its `vault.toml` is never rewritten), and refuses a destination that already holds the slug, carries a pending lifecycle marker, has an unrecorded remote, or is nested in another repository |
| `TestVaultSplitPlan_LeavesOutDerivedResidueOnly`, `TestVaultSplitPlan_KeepsAnUnignoredDerivedFile`, `TestVaultSplit_ResidueApplyVerifyPurge`, `TestVaultSplitVerify_InventoryUsesTheSourceResidue`, `TestVaultSplitPlan_GlobalWalksIgnoreTheResidueRule`, `TestVaultMergePlan_LeavesOutDerivedResidue` | Split and merge leave the source's residue out, keep the `*.bak` manifests and unignored files, verify's inventory uses the source's residue set, purge's unaccounted check skips the residue and its cleanup removes it; the global walks are untouched |
| `TestBaselineAdd_MergeAddsTheIncomingArchives`, `TestBaselineAdd_NoLedgerWritesNothing`, `TestBaselineAdd_NeverFailsTheCommand`, `TestBaselineAdd_CopyAddsAfterThePublishOnly` | Copy and merge add the incoming archives to this host's baseline set under one commit lock, write nothing without a ledger, warn rather than fail on a lock timeout, and a refused copy adds nothing |

### `internal/tools/vault_split_apply_test.go` — purge reaps the moved cache

`TestVaultSplitPurge_RemovesSourceTreesAfterVerify` seeds each slug's cache at
the new location and asserts purge removes the purged slug's
`palace/.local/embed-cache/<slug>/` and leaves the other slug's in place.
`TestVaultSplitPurge_CacheRefusalLeavesTheRealTrees` puts a symlink purge cannot
classify in the cache and requires the refusal to leave `palace/alpha` and
`Projects/alpha` intact: the cache is purged first, because once the real trees
are gone the manifest no longer binds and purge cannot be re-run.

### `internal/tools/vault_merge_test.go` — collision text for a non-store

`TestVaultMergePlan_LocalOnlyCollisionSaysSo`: a destination whose
`palace/<slug>/` holds only `.local/` state still refuses the colliding slug —
membership is the rule — and the refusal now says the directory is host-local
state and names `vp check --check palace-local-only`; a directory of empty
subdirectories is described as that, not as machine-local state; a real store
collision carries no such sentence.

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
| `TestConcurrentAppendIterationOwned` | N iteration numbers minted concurrently on one iterations file through `AppendIterationOwned` (which derives max+1 under the same lock it appends within) come back as exactly {1..N}: distinct and gapless |
| `TestConcurrentAddEntity` | N distinct entities added concurrently to one JSONL file all survive and the file stays well-formed JSONL |

### `internal/archive` — Manifest Lock and the Hook Posture Split (`-race`)

`lock_test.go`. Two sites lock, both keyed on the **manifest path**
(`internal/archive/lock.go`); the lock is deliberately *not* inside
`WriteManifest`, which both sites and `vaultaudit.ApplyBackfill`'s
sequential-never-nested sequence share. Every test here runs under a watchdog
(`mustFinishWithin`), because the failure these tests guard is a timeout-free
`LOCK_EX` that **hangs** rather than errors — without the watchdog a regression
wedges the package instead of naming itself.

| Test | What it proves |
|------|----------------|
| `TestCreate_ConcurrentChangedSourcePreservesExactlyOneBak` | 8 goroutines archive one session with a CHANGED source against one vault, over 4 independent rounds: exactly one `.bak` survives, it holds the PRIOR hash, the live manifest holds the NEW one, and no racer errors. Unlocked, racers derive the same `.bak` name and collide on the rename (or read past it and preserve nothing). Non-vacuity: at least one racer must report `Skipped=false`, so a build where every racer deduped cannot pass by never reaching the arm |
| `TestLinkSessionNote_ConcurrentLinkersLeaveACoherentManifest` | 16 concurrent linkers on one manifest leave it readable, with a `vault_rel_session_note` some linker actually wrote and every untouched field (`session_id`, `source_sha256`) intact — never a splice of two |
| `TestLinkSessionNote_ConcurrentWithCreateNeverRollsBackTheRecord` | the lost-update case that needs the lock: a linker reading *before* a concurrent `Create`'s rename and writing *after* it resurrects the old `source_sha256` over an archive whose bytes on disk are the new ones — coherent and wrong. The linkers loop **until `Create` finishes** rather than a fixed count, because `Create` hashes and compresses megabytes before the write they must straddle and a fixed burst is over long before it; a floor on the observed op count keeps the assertion non-vacuous |
| `TestCreate_NonBlockingPostureRefusesOnHeldLock` | with the manifest lock held from outside, `LockPosture: LockNonBlocking` returns an error wrapping `ErrManifestLocked`, leaves **no** `.bak`, and does not rewrite the manifest. Under `LockBlocking` this call never returns — which is what the watchdog reports |
| `TestTryLinkSessionNote_RefusesOnHeldLock` | the same refusal for the link path, and a refused link writes nothing |
| `TestLinkSessionNote_BlockingFormActuallyBlocks` | the OTHER direction of the split: against a held lock the blocking form is still waiting after 250 ms, then completes and lands its value once the lock is released. Without it, a change that quietly made every site non-blocking would still pass the two refusal tests while the CLI/MCP path started dropping writes |
| `TestCreate_ThenLinkSessionNote_NoSelfDeadlock` | `Create` → `LinkSessionNote` → `Create` (through the `.bak` arm) → `LinkSessionNote`, sequentially in one process, completes. `flock` does not recurse, so anyone who moved the acquire down into the shared `WriteManifest` would deadlock here permanently rather than fail |

`internal/hook`:

| Test | What it proves |
|------|----------------|
| `TestRun_ArchiveManifestLockContentionIsNonFatal` | **the test that proves a contended lock cannot lose a SessionEnd.** The manifest lock is held from outside; a full `SessionEnd` still returns, reports the refusal in `Result.Error`, sets no `ArchivePath`, and writes its session note. `cmdHook` is registered UNWRAPPED and `cmd_hook.go` passes `context.Background()`, so there is no timeout on that path — mutating the posture to `LockBlocking` does not fail this test, it **hangs** it, and the watchdog is what turns that into a legible failure |

### `internal/integration` — Cross-Process (`-short`-skipped, `make integration`)

| Test | What it proves |
|------|----------------|
| `TestIntegration_VaultLockCrossProcess` | builds the real `vp` binary and launches N concurrent `vp vault edit` child **processes** contending on one seeded file; every fixed-width anchor is converted to its DONE marker exactly once, proving the advisory flock serializes whole-file RMW across separate OS processes (CLI vs MCP). Skipped under `-short`. |

### `internal/atomicfile` — Windows Rename Retry

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
| `TestRenameWithRetry_NoRetryOnNonRetryable` | a non-retryable error returns after exactly **one** attempt, unwrapped, so `errors.Is` still reaches the original — a permanent failure must not be sat on for 785ms. |
| `TestRenameWithRetry_ExhaustsBound` | an always-failing retryable error gives up after the documented bound and returns the last error wrapped with `%w`. |
| `TestRenameRetryBound` | pins the bound (7 attempts) **and** that total backoff stays under 1s, so the numbers in the source comment cannot silently drift. |
| `TestWrite_RetriesTransientRename` / `TestWrite_PropagatesNonRetryableRename` | prove `Write` is actually wired to the retry — both go red if the call reverts to a bare `os.Rename`. |
| `rename_other_test.go` (`!windows`) | the off-Windows classifier returns false for every error shape, including `syscall.Errno(5)`/`(32)`, so unix never sleeps on a doomed rename. |
| `rename_windows_test.go` (`windows`) | classifies real `ERROR_ACCESS_DENIED` / `ERROR_SHARING_VIOLATION` (including through the `*os.LinkError` `os.Rename` returns) as retryable and `fs.ErrNotExist` as not. As of v8.2.0 no CI job compiles or runs it: `vet` runs on Linux, and `windows-lock` runs only `./internal/vaultlock/...` and one `./internal/integration/` test, not `./internal/atomicfile`. Run it with `GOOS=windows go vet ./internal/atomicfile/` or `go test ./internal/atomicfile/` on Windows. |

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

## Model-free migrate and search tests

`vp migrate` and `vp search` used to build the ONNX embedder before validating
their inputs, so `--dry-run`, a mistyped `--export-path` or `--vault-path`, or an
unknown project paid for a ~90 MB model download before failing (or before
answering `No results found.`). Now every input is validated first, a dry run
passes a nil engine and embedder, and the model is constructed only where a run
will actually embed.

### The seam and the guard

Every CLI construction routes through one package variable,
`newVaultEmbedder` (`cmd/vp/embedder.go`). `setupTestVaultEnv` installs
`forbidVaultEmbedder`, so a test built on it that constructs the model **fails**
(through `Errorf`, since the MCP lazy path can construct off the test
goroutine). A test that needs an embedder substitutes one with
`stubVaultEmbedder(t, emb)`, which returns a construction counter. Both helpers
call `t.Setenv` before touching the variable, so a test that has called
`t.Parallel` panics instead of racing on it. `setupTestVaultEnv` also puts
`XDG_CACHE_HOME` under its sandboxed home; the old pin to the host cache is
gone.

**What the guard covers, exactly:** the seam-routed sites — `setupEmbedder`
(`vp migrate mempalace`; `vp migrate vibevault` loads no model), `vp search`, `bootstrap()` (which captures the
constructor once, before its lazy closure), and `vp check`'s Embedder row,
whose `check.CheckEmbedder` takes a constructor that `gatherCheckResults` feeds
from `newVaultEmbedder`. A `setupTestVaultEnv` test that drives `runCheck`
against its valid temp vault fails loudly rather than cold-downloading; the
full-suite check tests stub the seam through `healthyCheckEnv` (see "`cmd/vp` —
Flag Wiring").

### `cmd/vp/cmd_migrate_test.go` (all under the guard)

| Test | What it proves |
|------|----------------|
| `TestMigrateMemPalaceMissingExportBuildsNoEmbedder` | A missing export exits 1 with `read export file`, dry run and real run alike |
| `TestMigrateMemPalaceDirectoryExportIsUserError` | A directory as `--export-path` exits 1 |
| `TestMigrateMemPalaceMalformedExportBuildsNoEmbedder` | `{` exits 1 with `parse export JSON` |
| `TestMigrateMemPalaceUnreadableExportIsSystemError` | A mode-000 export stays exit 2: the exit-1 mapping covers not-found, directory, and malformed JSON only |
| `TestMigrateMemPalaceDryRunBuildsNoEmbedder` | A valid dry run reports the export's counts and writes nothing under `palace/` |
| `TestMigrateMemPalaceRefusesAnUnknownProject` | A `--project` the vault does not hold exits 1 after the export is parsed, and builds no embedder |
| `TestMigrateMemPalaceRealRunBuildsEmbedderOnce` | A real run constructs exactly once and commits chunks to the project's host-local store |
| `TestMigrateMemPalaceOutputStatesTheCaveats` | The output says single-host, not tracked, that a full rebuild discards the import, and "Keep the export file"; it names no command (no match of `\bvp [a-z]`); its summary counts batches, never "projects", and each batch's progress line carries its position |
| `TestMigrateMemPalaceSaysWhichDayTheBatchesCarry` | The output names the batches' day and its source, and says when the epoch was used |
| `TestMigrateVibeVaultExitsNonZeroWhenAnItemFailed` | An undated session, or an archive write that fails, ends the import with "The import is incomplete" and exit 2 |
| `TestArchivedNoticeCountsSessionsApartFromKnowledge`, `TestProgressLineNeverClaimsAPositionItLacks` | The notice never counts `knowledge.md` as a session; a progress line never prints `[0/0]` |
| `TestMigrateMemPalaceNoEmbeddableDrawersBuildsNoEmbedder` | An export whose only drawer is blank imports its entities and triples with zero constructions |
| `TestMigrateVibeVaultDryRunBuildsNoEmbedder` | A seeded dry run names the session, counts it, and writes no import marker |
| `TestMigrateVibeVaultMissingSourceBuildsNoEmbedder` | A `--vault-path` with no `Projects/` exits 1 for `--dry-run`, for `--yes`, and on the **prompt path** (neither flag, stdin a pipe) — where it must say `has no Projects/` and never `requires --yes`, pinning that the check runs before the confirmation gate |
| `TestMigrateVibeVaultInPlaceEmptyVaultMessage` | An in-place run names the configured vault and never blames `--vault-path` |
| `TestMigrateVibeVaultSourceProjectsIsFileIsUserError` / `…StatFailureIsSystemError` | A `Projects` file exits 1; an unstat-able source (ENOTDIR) exits 2 |
| `TestMigrateVibeVaultRealRunArchivesAndNamesNoCommand` / `…NoSessionsBuildsNoEmbedder` | A real run archives the session and `knowledge.md` with zero constructions, says the archives are not indexed yet, and names no command (no match of `\bvp [a-z]`); `--agentctx --no-sessions` never constructs either |

### `cmd/vp/cmd_search_test.go` (all under the guard, each in a temp cwd)

| Test | What it proves |
|------|----------------|
| `TestSearchInvalidProjectSlugBuildsNoEmbedder` | `-p "../Bad Slug"` exits 1 |
| `TestSearchUnknownProjectBuildsNoEmbedder` | A well-formed `-p` the vault does not hold exits 1 and names it |
| `TestSearchNotesOnlyProjectReachesEmbedder` | A notes-only project (no palace store) is a real target: the check admits exactly what search indexes (`ListAllProjects`) |
| `TestSearchUnknownDetectedProjectBuildsNoEmbedder` | With no `-p`, a cwd-detected slug the vault lacks exits 1 and the message says it was detected. The test `t.Chdir`s into `<tmp>/unknownrepo`: from the package's own cwd, detection yields `vibe-palace` and the test would prove nothing |
| `TestSearchDetectedProjectReachesEmbedder` | A marker naming a seeded project reaches the embedder once |
| `TestSearchUnreadableProjectsIsSystemError` | An unlistable `Projects/` exits 2 — "could not look" is not "absent" |

### Harness and library

| Test | What it proves |
|------|----------------|
| `TestForbidVaultEmbedderRecordsAndRestores` | Through a recorder: the forbidden seam reports an error naming `stubVaultEmbedder`, returns the sentinel, and is restored by cleanup |
| `TestSetupTestVaultEnvInstallsEmbedderForbid` | The guard is installed by the harness itself — removing the call turns this red |
| `TestEmbedderSeamHelpersRefuseParallel` | Both helpers panic under `t.Parallel` before swapping the variable |
| `internal/migrate`: `TestLoadMemPalaceExport`, `TestMemPalaceExportEmbeddableDrawers` | The loader's error classes (`fs.ErrNotExist`, `fs.ErrInvalid`, `*json.SyntaxError`, `*json.UnmarshalTypeError`) and the one blank-drawer predicate |
| `internal/migrate`: `TestImportMemPalace_DryRunEmbedsNothing` | Zero `EmbedBatch` calls under `DryRun`, and dry-run counts equal a real run's — **on a fresh vault with unique IDs only** |
| `internal/migrate`: `TestImportMemPalace_DryRunNilEngineAndEmbedder`, `TestImportVibeVault_DryRunNilEngineAndEmbedder` | Both importers accept nil/nil under `DryRun` |

### `internal/integration/migrate_no_model_test.go` — the real binary, dead proxy

`TestIntegrationMigrateLoadsNoModel` runs the built `vp` with an explicit
environment (temp `HOME`, config and cache dirs; `HTTPS_PROXY`/`HTTP_PROXY` at
`http://127.0.0.1:1`) for: mempalace missing export / valid export `--dry-run` (both with `--project`)
(exit 1 / 0), vibevault missing source / seeded `--dry-run` (1 / 0), and
`vp search` with a bad slug, an unknown `-p`, and an unknown cwd-detected
project (all 1). The observable is the HF hub cache directory — neither
`<XDG_CACHE_HOME>/huggingface` nor `<HOME>/.cache/huggingface` may appear —
never elapsed time. `palace/.local/models` is only a secondary: a failed
download never creates it. A regression costs ~20 s per subtest (hugot's retry
loop) before it goes red. It is not `-short`-gated.

For a manual full-package check where unprivileged user namespaces exist, run
`go test -race -short ./cmd/vp/` under
`unshare -rn sh -c 'ip link set lo up && exec env -i …'` with a cold `HOME`.
Bring loopback up, or about ten unrelated `httptest`/MCP-serve tests fail. CI
runners generally lack user namespaces, so this is manual only.

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

## Drawer-Seeding Batch Invariant

### `internal/testinfra/seed_test.go` — the drawer-seeding batch invariant

`internal/testinfra/seed.go`'s `flushDrawers` batches every `WithDrawers`/
`WithDrawer`/`WithDrawerOut` option targeting the same `(project, wing, room)`
into exactly ONE `storage.Vault.AppendDrawers` call, instead of one call per
drawer — `AppendDrawer` (the singular n=1 wrapper) re-scans the whole room file
for dedup on every call, so a per-drawer loop would make seeding O(N²).
`TestHarness.AppendDrawersCallCount` instruments the one call site in
`flushDrawers` directly (an `atomic.Int32`, matching the idiom
`countingLazyEmbedder` already uses below) so this batching property is a
checked call-count assertion, not just an inference from correct output.

| Test | What it proves |
|------|----------------|
| `TestSeedBatchesManyDrawerOptionsIntoOneRoom` | A 50-entry batch (30 via `WithDrawers`, 19 via `WithDrawer`, 1 via `WithDrawerOut`) into ONE `(project, wing, room)` group lands correctly AND flushes with exactly **1** `AppendDrawers` call, regardless of how many separate option calls contributed to it. Count — not wall-clock time — is the observable; see the same rule under *Lazy Startup Tests* (`TestIntegration_HandshakeDoesNotConstructEmbedder`) |
| `TestSeedAppendDrawersCallCountMatchesGroupCount` | Non-vacuity: seeding into **three** distinct `(project, wing, room)` groups in one `New(t, ...)` call drives the count to exactly **3** — one call per group, not one call overall and not one call per option/drawer |

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
`.processing` item is left untouched (it does not match `jobqueue.Claim`'s
`.json`-suffix check), corrupt-item removal,
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

## `vp_refresh_index` — the mutating flag and a ratchet that had to be narrowed

`internal/tools/refresh_index_backfill_test.go`. From
`refresh-index-reports-rebuilt-while-writing-nothing`.

### 🔴 The ratchet this task ASKED for is the wrong test — do not reinstate it

The task demanded that a `"rebuilt"` status imply **at least one observable write**
under `palace/<project>/`. That expectation is **refuted**. `Rebuild` builds the
index IN MEMORY (`e.indexes[project]`); `.vec` files are written only as a
cache-**MISS** side effect (`embedMisses` → `cache.Put`). A rebuild whose vectors
are all cached correctly writes nothing, so the `dotfiles` control case the task
filed as its sharpest evidence was legitimate behaviour.

That ratchet would have gone red on correct code, and the "fix" would have been to
make the tool write something it does not need to write.

| Test | What it proves |
|------|----------------|
| `TestRefreshIndexRebuiltOnlyWhenThereWasSomethingToRefresh` | Ratchet 1, written to the property that IS true: `rebuilt` is claimed only when there was something to refresh, the refusal fires when there was not, and the counts that make the claim falsifiable are present with at least one non-zero. **Asserts no filesystem write** |
| `TestRefreshIndexCacheHitRebuildIsLegitimate` | The control case, kept as a PASSING test so the refuted expectation cannot be re-derived from the symptom text. A second refresh is all cache hits: `indexed > 0` with `embedded == 0` is CORRECT |
| `TestRefreshIndexIsRegisteredMutating` | The flag itself, plus absence from `ReadOnlyServeToolNames` |

`TestRefreshIndexStillRefusesWhenThereIsNothingToBackfill` (Piece 1) remains the
no-store half; ratchet 1 sits beside it rather than replacing it.

### The session-note corpus narrowed the refusal — `refresh_index_notes_test.go`

From `session-notes-without-transcript-have-no-index-backfill`. `Rebuild` gained
a **third** corpus source (the bodies of `Projects/<slug>/sessions/*.md`), so a
project captured as notes only — no transcript, no archive, no drawer store — now
has indexable content and the refusal must no longer fire for it. The refusal's
message also had to be corrected: its corpus enumeration was short by one source,
and its "delete the orphaned history" advice became actively harmful.

| Test | What it proves |
|------|----------------|
| 🔴 `TestRefreshIndexNoLongerRefusesANoteOnlyProject` | Drives the refusal **branch**, not its wording: `stats.Indexed` moves off zero, so `!hadStore && stats.Indexed == 0 && …` is unreachable for this project and the tool takes the success path. Asserts the search hit comes back with `source_type=session-note` and a navigable `source_ref`, and that the note pass created neither a drawer store nor a `kg/` directory. Carries no "unreachable before the refresh" control, unlike the archive test — `Engine.Search` calls `ensureIndex`, so the note corpus is reachable from the FIRST search with no explicit refresh at all |
| `TestRefreshIndexStillRefusesAProjectWithNoNotesEither` | Keeps the refusal **reachable** — a refusal that can never fire is the failure mode one layer below the one this change fixes. Fixture asserted empty on all four axes (no store, no notes, no iterations, no archives). Also pins the corrected message: it enumerates `0 session-note chunks`, no longer advises deleting history the note corpus can index, and keeps the principle that the tool "cannot invent content that was never captured" |
| `TestRefreshIndexNoteCorpusIsNotLimitedToNoteOnlyProjects` | The widened blast radius, pinned honestly: a project with iterations **and** notes gets both, `indexed == iteration_chunks + note_chunks`. Nothing in this change may claim the note source affects only note-only projects |

### Why the flag is load-bearing in two places at once

`vp_refresh_index` writes on three paths — the archive backfill via
`AppendDrawer` → `atomicfile.Write`, `.vec` cache files on every embed miss, and
`Rebuild` creating `palace/<slug>/`. Registered non-mutating, that one bit both
under-gated the tool for a stale binary AND published a writer on the read-only
`vp mcp serve` allow-list, which `readonly_serve.go` calls a security failure that
is not detectable after the fact.

Correcting it requires the constructor flag **and** a `MutatingToolNames` entry:
`TestMutatingToolNamesMatchRegistry` pins that pair, and
`TestReadOnlyServeAgreesWithSurfaceGateToday` / `TestReadOnlyServePartitionsTheRegistry`
require the tool to move BETWEEN the two declarations rather than out of one.

---

## Task-file write discipline — `overwrite`, the refuse-gate, and the conventional first heading

From `task-preamble-is-unreachable-by-every-write-action`. Three surfaces, one
rule: **every field a task carries has exactly ONE writer.**

### 🔴 A fix blinded the test that proved it — read this before editing these tests

`CreateTask` emits `## Context` (`storage.ConventionalFirstHeading`) FIRST, so
everything passed as `content` lands UNDERNEATH it and **a freshly created
task's preamble is EMPTY**.

The original round-trip test edited fixture text that *read* like a preamble and
was body text under that heading. It proved heading-wording revision and proved
nothing about the preamble — and the landing fix is what hid it, since before the
conventional heading that same text *would* have been preamble.

The lesson generalises: **when a change moves structure, re-derive what each
existing test actually reaches.** A test that keeps passing while measuring the
wrong thing is worse than a missing one.

### `internal/tools/task_overwrite_test.go`

| Test | What it proves |
|------|----------------|
| `TestOverwriteRevisesThePreambleAboveTheFirstH2` | **The acceptance gate.** Asserts POSITION, not presence: `headerAndPreamble` derives the region between the header block and the first H2, a precondition asserts a created task's preamble is EMPTY, then an overwrite INSERTS a provenance line and asserts `index(line) < index("## Context")`, and a second overwrite CHANGES it. The header block must be byte-identical both times |
| `TestOverwriteRoundTripRevisesBodyAndHeadingWording` | Whole-file round trip and revision of an H2's own WORDING — which `amend` cannot do, being keyed on that text. Renamed from `...RevisesPreambleAndHeading`, which claimed coverage it did not have |
| `TestOverwriteRefusesArchivedTask` | Retired AND cancelled refused; file byte-identical after. The refusal is in the HANDLER — `OverwriteTaskFile` resolves all three dirs by design and documents the archived question as the caller's |
| `TestOverwriteRefusesHeaderSmuggling` | `validateWholeTaskFile` checks SHAPE only and never sees the old file. Status, priority and title each refused, the refusal names the owning action, and the file is untouched |
| `TestOverwriteAcceptsAnUnchangedHeader` | Negative control — a guard that refused everything would pass the row above |
| `TestOverwriteRequiresContent` | The handler-side arm, for a direct call that bypasses schema validation |
| `TestCreatedTaskHasTheConventionalFirstHeading` | The heading is emitted and is the FIRST H2 — for a body with no heading, a body that already opens with one (empty `## Context`, the accepted cost), and an empty body |

### `internal/vaultfs/task_path_test.go`

The refuse-gate is in `vaultfs`, not in the MCP tool, so `vp vault write` /
`vp vault edit` are covered by the same rule: **a guard only an agent can trip is
not a guard.**

| Test | What it proves |
|------|----------------|
| `TestIsTaskFilePath` | Active, `done/`, `cancelled/` and case variants match; `notes/tasks.md`, `Knowledge/tasks/…` and a bare `tasks/` do not |
| `TestWriteRefusesTaskPaths` / `TestEditRefusesTaskPaths` | Both writers refuse all three task dirs, name `vp_manage_task` and `overwrite`, and leave the file unchanged |
| `TestNonTaskPathsStillWriteAndEdit` | **Positive control** — a gate that refused everything would pass every row above while breaking the vault |

`OverwriteTaskFile` is NOT routed through the refusal and needs no exemption: it
calls `atomicfile.Write` directly, which makes it the sanctioned path
structurally rather than by a carve-out that can rot.

### `internal/storage/tasks_test.go`

`TestCreateTask_ConcurrentSameSlugExactlyOneWins` hand-built its expected file
content and so encoded the absence of the heading. It now builds from
`ConventionalFirstHeading`, keeping it a pin on the no-torn-write property rather
than on the heading's spelling.

---

## Source Audit — the gate that would have caught `note_path` in five minutes

`internal/sourceaudit` is a static analysis **of this repository's own source**, run as
a test. It exists because of a measured fact: **every serious defect found in
iterations 191–201 was caught by looking at a real artifact, and NOT ONE was caught by
a test, a check, or a code review** — and two of them were *mechanically detectable*
and hid for months anyway.

### Every rule, and where it is tested

As of v8.2.0 the audit reports 12 kinds of finding. Derive the list with
`grep -rhoE 'Kind[A-Za-z]* *= *"[a-z-]+"' internal/sourceaudit/*.go`. `Run`
(`sourceaudit.go`) applies the syntactic rules; `RunModule` (`derived_gate.go`)
applies the type-checked one. All files are under `internal/sourceaudit/`
unless a path says otherwise.

| Kind | What it pins | Tests |
|------|--------------|-------|
| `write-only-field` | A yaml/json-tagged field on a struct the code constructs that nothing assigns | `sourceaudit_test.go` |
| `uninvoked` | A non-test function or method that no non-test code calls | `sourceaudit_test.go` |
| `ungated-vault-writer` | A command registered without `mutates()` whose call graph reaches a stamped vault write. Syntactic, and still runs under `-short` | `ungated_writer_test.go` |
| `vault-write-outside-funnel` | A vault mutation that bypasses the shared write primitives (`atomicfile.Write`, `vaultfs.Delete`/`Move`), or an `atomicfile.Write` whose `vaultRoot` defeats the surface stamp | `vault_write_funnel_test.go` |
| `surface-remediation-lost` | A second copy of the surface-mismatch remediation prose, or a consumer of `*surface.IncompatibleError` that never reaches the way out | `surface_remediation_test.go` |
| `git-exec-unsafe-env` | A `git` subprocess whose environment is not built through `SafeGitEnv` | `git_env_funnel_test.go` |
| `env-isolation-bypass` | An `internal/integration` test that sets `HOME`, `XDG_CONFIG_HOME` or `CLAUDE_HOME` directly instead of through `testinfra.IsolateEnv` | `env_isolation_test.go` |
| `planner-write` | A migration planner (a function that derives values a later executor writes) reaching a write call | `planner_no_write_test.go` |
| `shared-enumeration` | An evidence reporter (the `vp` command a vault-audit dimension publishes for corroboration) reaching the enumerator the dimension itself uses | No planted-bug test; exercised by `TestSourceAuditGate`. The differential it protects is `cmd/vp/cmd_audit_task_files_differential_test.go` |
| `git-enabled-owner` | `git_enabled` read, or its refusal forged, anywhere other than `storage.RefuseIfGitDisabled` and its allow-listed readers | `git_enabled_owner_test.go` |
| `literal-pathspec-opt-out` | vp's git taken off literal pathspecs: `gitenv.GlobPathspecs` used outside `storage.checkIgnored` (the one `git check-ignore`, behind `GitPathIgnored` and the staging guard), a `GIT_*_PATHSPECS` variable or `--no-literal-pathspecs`/`--glob-pathspecs`/`--icase-pathspecs` spelled in a string literal outside `gitenv`, or a string literal beginning with pathspec magic (`:(`, `:!`, `:^`) | `literal_pathspec_owner_test.go` |
| `departure-record-writer` | Any caller of a privileged departure-record entry point (`vaultfs.WriteDepartureRecord` and its siblings); each allowed caller is a reviewed baseline entry | `departure_record_writer_test.go` |
| `derived-gate-divergence` | The surface-gate predicate derived from the call graph disagrees with the hand-declared one for a CLI command or MCP tool. Each accepted divergence is a baseline entry whose reason records the ruling | `derived_gate_test.go`, `derived_gate_declared_test.go`, and `TestDerivedGateBaselineIsCurrent` in `sourceaudit_test.go` |

**Where the derived-gate rule runs.** It type-checks the whole module
(go/packages, SSA and a VTA call graph), so its module-deriving tests call
`skipUnlessFullSuite` and skip under `-short`. (The fixture tests in
`derived_gate_declared_test.go` load only a small synthetic module and run
under `-short` too.) `make test` and the CI `test` job both pass `-short`.
The rule therefore runs automatically only in the CI `source-audit` job and
the push-only `ubuntu26-canary` job, both through `make source-audit`
(`go test -count=1 ./internal/sourceaudit/`, no `-short`, no `-race`). The
manual `make test-full` and `make cover-full` targets pass no `-short`
either, so they run it too. If those CI jobs go away, no automated run
checks the rule. The ruling is recorded on `skipUnlessFullSuite`
in `derived_gate_test.go` and above `source-audit:` in the `Makefile`.

### The first two findings, and why they are the same bug

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

This table covers the first two rules. The later rules, except `shared-enumeration`, have their own
test files with planted-bug and clean-code cases; see *Every rule, and where it is tested* above.

### 🔴 Interface dispatch: the one exemption, and where the line is drawn

A method reached only through an interface has no direct call site, so it looks
uninvoked. The tempting fix — **exempt every method that satisfies an interface** — is
**wrong**, and triaging the first baseline proved it: six `reconcile.*.Requires()`
methods satisfied `reconcile.Reconciler`, an interface the tree genuinely uses, and
declared a dependency graph that the driver loop re-derived by hand in a `switch` and
**never once read**. The blanket rule would have hidden all six. An interface method
nobody dispatches on is not noise; it is this project's signature bug wearing a contract.
(The six were later deleted — the protocol went, the hand-written order stayed — so the
fixture in `TestInTreeInterfaceMethodNobodyCallsIsStillFlagged` is now the only copy of
that shape.)

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

The rules `Run` applies are **syntactic** (go/ast, stdlib only, no type information), which
biases them toward **false negatives**. The derived-gate rule is the exception: it is type-checked
(`golang.org/x/tools` go/packages, SSA and a VTA call graph), and its known imprecisions (a shared
generic instantiation and closure attribution, documented in `derived_gate.go`) make it
**over-derive**, which is why a divergence is a question to rule on, not an automatic finding. The
rest of this section is about the syntactic rules. Field names are not unique across structs, so `Foo.Name = x` counts
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
intent (the registry is `dims` in `audit.go`; the count is deliberately not restated
here — see ADR-007). Its
tests carry the same doctrine — *an auditor
validated only by its own logic is the thing this epic exists to prevent* — so the
central test is a **mutation test**, exactly as in `sourceaudit`.

| Test (file) | What it proves |
|------|----------------|
| `TestArchiveRoundTrip_FindsKnownDefects` (`archive_test.go`) | hand a vault three KNOWN defects (a stranded manifest, a dangling back-link, a readable-but-empty tree) and assert it finds exactly those — **an auditor that cannot fail issues a clean bill of health it never earned** |
| `TestArchiveRoundTrip_CleanVaultIsClean` (`archive_test.go`) | a fully-linked vault produces no findings — trustworthy only *because* the mutation test above can fail |
| `TestRun_FixingTheBugForcesTheBaselineToShrink` (`archive_test.go`) | linking a stranded-but-accepted manifest turns its baseline entry STALE — the ratchet, exercised end to end |
| `TestRun_LiveVaultCanary` (`archive_test.go`) | the audit runs against the **real vault** — the discipline the whole epic rests on. It skips unless `VP_LIVE_VAULT` names a vault root, and neither `make test` nor CI sets it |
| `dimensions_test.go` | `TestEvidence_ReproducesTheGoRule` runs `EvidenceProjectTreeCoherence` and `EvidencePalaceStoreDrawers` under bash on a fixture holding every shape the Go side filters (a `.local`-only husk, an empty subtree, an invalid slug, symlinked project directories, a zero-length `drawers.jsonl`) and requires each to print exactly the dimension's artifacts; the commands use no GNU-only `find` (no `-quit`, no trailing-slash start path) so BSD `find` agrees. project-tree-coherence, KG-portability, resume-discipline, iteration-headings, memory-portability, task-heading-markers, palace-store-drawers each find their planted defect and pass a clean fixture; palace-store-drawers additionally pins that an ABSENT `drawers/` and a PRESENT-BUT-EMPTY one produce **distinguishable details**, that a populated `Projects/<slug>/iterations.md` (a separate ingest corpus) does **not** silence the finding, that an unreadable store lands in `unknowns` rather than passing, and — the mutation test — that emptying a populated drawer set is what produces the finding; `TestPalaceStoreDrawers_PalaceOnlyProjectDetailTellsTheTruth` asserts the detail's WORDS, not just the count, because the gate admits a palace-only project for which the two-tree explanation would be false; `TestPalaceStoreDrawers_IsRegistered` proves `Run`'s hand-edited `dims` literal actually carries it; `TestPalaceStoreDrawers_LocalOnlyDirIsNotAStore` pins the population split — a `.local`-only or empty-subtree `palace/` directory is reported by neither `palace-store-drawers` nor `project-tree-coherence` — and `TestPalaceStoreDrawers_KGOnlyStoreStillReported` pins that a real kg-only store still is, with a detail that names every drawer source as fact and says neither "never drawer-indexed" nor "UNSEARCHABLE" (fixtures seed a real file, since a bare directory is not a store); `task-preamble` pins that a task written by the real `storage.CreateTask` is **not** flagged (the positive control that ties the dimension to a writer's guarantee rather than to taste), that prose above the first H2 **is** flagged exactly once on the vault-relative artifact, that both paths of `PreambleSkippedNoH2` — no unfenced `## ` anywhere, and an unfenced `## ` sitting ABOVE the header block — render **distinguishable details** that are each true of their own file (asserted on the detail TEXT, since the outcome cannot tell them apart), that a `## ` appearing only inside a code fence falls into that degenerate class because fence-awareness comes from the predicate rather than from a local re-implementation, that `tasks/done/` and `tasks/cancelled/` are out of scope because `OverwriteTaskFile` is active-only and a finding there would be unrepairable, that an unreadable tasks dir or task file lands in `unknowns` rather than passing, that the region is disjoint from `task-heading-markers` on a file carrying both defects, and — the mutation test, `TestTaskPreamble_MutationMovingThePreambleDownClearsTheFinding` — that moving the SAME prose down under `## Context` in the SAME file clears the finding, which is what proves the rule tracks the region and not the harness; `TestTaskPreamble_IsRegistered` proves `dims` carries it, and `TestTaskPreambleText_RecoversExactlyWhatTheMigratorWrote` pins the dimension's one inference — that the detail's size and excerpt are read back out of the migrator's own before/after pair rather than from a second local copy of `storage`'s header-block rule |
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

## Background summarization queue + wrap-triggered drain

A new job-kind sibling of `internal/capture/enrichqueue.go`, split across three
new packages plus a detached-launch CLI subcommand and two new MCP tools. The
queue/dispatch/drain mechanics below landed first, against a fake
`Summarizer`; the real LLM-backed one (`internal/itersummary`) and its wiring
into `vp drain summaries` landed in a later phase of the same effort — see
"Real Summarizer wiring" below.

- **`internal/jobqueue`** (`jobqueue_test.go`) — the three primitives
  generalized out of `enrichqueue.go` (`Claim`/`Requeue`/`Done`), tested
  independently of any job schema: `TestClaimRaceNoDoubleClaim` (atomic-rename
  claim safety under concurrent callers), `TestClaimStaleReclaim*` (orphaned
  `.processing` reclaim, and the drop-not-clobber case when a fresh job already
  occupies the reclaim target), `TestRequeueDeadLetterAtCap` /
  `TestRequeueAttemptsAboveCapAlsoDeadLetters` (dead-letter to `.failed`, never
  deleted), `TestRequeue*RestoresClaim` (a `reencode`/write failure restores the
  claim rather than losing the job), `TestClaimRequeueDoneFullRoundTrip`.
- **`internal/summarize`** (`summarize_test.go`) — `SummaryItem` (the one
  exported type shared by the persisted queue item and `Summarizer`'s
  parameter — see its doc comment for why there is deliberately only one),
  `Summarizer`/`SummaryResult` (an intentionally thin placeholder; a later
  piece of work defines real fields), and `DrainSummarizationQueue` built on
  `internal/jobqueue`: `TestEnqueueDrainRoundTrip_{Iteration,SessionNote}`,
  `TestDrain_DeadLetterOnRepeatedFailure`, `TestDrain_NilSummarizerIsNoop`,
  `TestDrain_MaxBoundsProcessedCount`, `TestDrain_ContextCancellationStopsEarly`
  — all against a fake `Summarizer`, never a real LLM client.
- **`internal/detachlaunch`** (`launch_test.go`) — POSIX/Windows detached
  process launch (`Launch`/`LaunchFunc`), mirroring `internal/vaultlock`'s
  `flock_unix.go`/`flock_windows.go` build-tag split: `TestLaunchReturnsImmediately`,
  `TestLaunchSelfRelaunch` (`binary == ""` resolves `os.Executable()`),
  `TestLaunchWritesLogFile`, `TestLaunchNoDeadlockOrLeak` (the reaper goroutine
  doesn't hang the test binary). True survive-the-parent behavior is **not**
  claimed by any unit test here — see the file's own doc comment for why that
  is honestly unprovable in-process, and what a manual/CI shell-script check
  would look like instead.
- **`cmd/vp/cmd_drain.go`** (`cmd_drain_test.go`) — the one-shot `vp drain
  summaries --project-path <path> [--max N]` subcommand: explicit
  `OpenProjectVaultAt` resolution (never `os.Getwd()`, since a detached
  child's inherited cwd is not reliably the project being drained), the
  `--max` default (`50`) applied in code (`cli.FlagDef.Default` is
  display-only and is never read by `ParseFlags`), and the single-flight
  lock — an `internal/vaultlock` OS-level advisory flock (its sidecar under
  `.vibe-palace/.vp-locks/`), not a hand-rolled pidfile-plus-staleness
  heuristic: an earlier revision of this file used exactly that (an
  `O_EXCL` pidfile reclaimed after a fixed staleness window), and had a real
  fencing gap — a drain running longer than the window could have its lock
  "reclaimed" by a second caller while still running, and its own cleanup
  would then delete the second caller's lock. `vaultlock.TryAcquire` has no
  staleness window at all (a flock is either held or not, and the OS
  releases it automatically even on a crash), which is what actually closes
  that gap. Also load-bearing: the lock's *target* must be an absolute,
  already-existing path (the queue directory itself) — `vaultlock`'s own
  `canonicalKey` resolves a relative, nonexistent target via
  `EvalSymlinks` against the *calling process's cwd*, which would have
  made two different callers (a detached drain vs. a manually-run `vp
  drain summaries`) hash to two different lock files for the same
  project, silently defeating the single-flight guarantee.
  `TestRunDrainSummaries_ConcurrentDrainReportsAlreadyRunning` pins the
  guarantee itself (holding the lock via `vaultlock.TryAcquire` directly,
  the same way a concurrent drain would); `TestRunDrainSummaries_Success`
  additionally proves the lock is actually released (a second sequential
  call succeeds, not `already_running`).
- **`internal/tools/summarize_tools.go`** (`summarize_tools_test.go`) — the
  two new MCP tools, `vp_enqueue_iteration_summary` and
  `vp_trigger_summarization_drain`, mirroring `vp_stamp_iter`'s
  `project`/`project_path` shape. The drain-trigger tool takes an injected
  `detachlaunch.LaunchFunc` (`TriggerSummarizationDrainTool(vault, launch)`) —
  the seam that keeps `internal/integration/tool_coverage_test.go`'s real
  dispatch test from spawning an actual OS process (see below).
- **The injection seam itself** — `tools.WithLaunch` (a `RegisterOption` on
  `RegisterAll`, defaulting internally to `detachlaunch.Launch` when omitted,
  so none of `RegisterAll`'s ~16 existing call sites needed to change) and
  `testinfra.TestHarness.Launch` / `RecordedLaunches()` (a recording fake
  installed by default via `testinfra.NewRecordingLaunch`, so any
  harness-dispatched call to the drain-trigger tool records its args instead
  of spawning anything). `internal/tools/register_test.go`'s
  `TestRegisterAllZeroOptionsUnchanged` pins that the ~16 existing zero/`WithConfig`-only
  call sites are unaffected. `internal/integration/tool_coverage_test.go`'s
  `vp_trigger_summarization_drain` fixture is the concrete end-to-end proof:
  it pre-populates a fake queue file, dispatches the tool through the real MCP
  JSON-RPC path, and asserts the exact launch args landed in
  `h.RecordedLaunches()` — real dispatch, zero real subprocesses.
- **`internal/capture/session.go`** — a new best-effort, always-fires-when-`cwd`-is-set
  (not miss-only, unlike its `EnqueueEnrichment` sibling) call to
  `summarize.EnqueueSessionSummary`, logged via `StageSessionSummaryEnqueue` on
  failure. Covered in `session_test.go`; its side effect (a `.vibe-palace/summarization-queue/`
  directory appearing whenever `cwd` is set) required updating three
  pre-existing tests' stricter "no `.vibe-palace` dir at all" assumption down to
  the actually-load-bearing "no **claim file**" check (`host_parity_test.go`,
  `session_inline_archive_test.go`).
- **`internal/templates/templates/commands/wrap.md`** — Step 2 gained a `cwd`
  field instruction, Step 4 gained an iteration-summary enqueue call, and a new
  Step 10b triggers the drain; Step 11's report says the drain was
  **triggered**, not that summarization finished (it is a detached background
  process wrap never waits on).

### Real Summarizer wiring

The queue above originally always drained against a fake `Summarizer` (or a
`nil` one, a documented no-op). This phase supplies the real, LLM-backed
implementation and wires it all the way through:

- **`internal/storage`** — a new `[summarization]` TOML section
  (`SummarizationConfig`, `config.go`/`config/defaults.toml`/
  `config/template.toml`), deliberately its own section rather than reusing
  `[enrichment]`, since the two features can be enabled/pointed at different
  models independently. Also new: `IterationSummaryFile`/
  `WriteIterationSummary`/`ReadIterationSummary` (`paths.go`,
  `iteration_summary.go`), a vault-committed cache at
  `palace/{project}/iteration-summaries/{n}.json` keyed by `MatchIndex` so a
  later same-`N` entry invalidates a stale cached summary.
- **`internal/itersummary`** (new package, `itersummary_test.go`) — the actual
  LLM-calling `IterationSummarizer`. `NewIterationSummarizerFromConfig`
  resolves a project's `[summarization]` config into a real client (mirroring
  `internal/capture`'s own `NewEnricherFromConfig` pattern: disabled or
  unresolvable config is a warn-log, not a hard error), and resolves
  multi-entry-per-`N` iterations.md sections via `wrapstate.LastEntryByN` so
  only the current, last file-order match for a given `N` is ever summarized.
- **`internal/summarize`** — `DispatchSummarizer`, a `Kind`-routing wrapper
  around per-kind summarizers (today just `Iteration`), plus an
  `ErrUnsupportedKind` sentinel so `DrainSummarizationQueue` correctly
  `continue`s past a kind with no handler registered (deferring the job back
  to the queue) instead of aborting the whole drain.
- **`internal/search/iterations.go`** — `collectIterationCorpus` now emits a
  summary row (read from the vault cache, when present) *and* the raw entry
  row for each iteration, with distinct `SourceType`/`SourceRef`/cache-id
  families so the two never dedup-collide or share a vector-cache entry.
- **`cmd/vp/cmd_drain.go`** — `runDrainSummaries` now loads the project's
  `[summarization]` config and constructs a real
  `summarize.DispatchSummarizer{Iteration: iterationSummarizer}` itself
  (there is no longer an injectable `Summarizer` parameter on this function);
  `drained` in its report now reflects genuine LLM-backed work once
  `[summarization]` is enabled, not just a queue-mechanics no-op.
- **`cmd/vp/cmd_summarize.go`** (new) — `vp summarize iterations
  --project-path PATH [--force]`, a synchronous, one-shot, operator-triggered
  command that walks every `iterations.md` entry directly (no queue involved)
  and (re)generates its cached summary; `--force` bypasses the
  `MatchIndex`-freshness check. This is deliberately a *separate* code path
  from the queue-drain one above — an operator wanting to backfill or
  regenerate summaries for a whole project shouldn't have to enqueue one job
  per iteration first. Covered in `cmd_summarize_test.go`: happy path,
  additive skip-when-cached, `--force` bypass, stale-cache
  (`MatchIndex` mismatch) re-summarization, multi-entry-per-`N`, a missing
  `iterations.md` (an empty, not erroring, result), and disabled
  `[summarization]` (a real `ExitUser`, unlike the drain path's silent
  no-op — this command's whole purpose is to summarize).
- **`cmd/vp/integration_iteration_summary_test.go`** (new) —
  `TestEnqueueThenDrain_EndToEnd`, the one test in this whole feature that
  goes through the actual production job-*creation* entrypoint instead of
  calling `runDrainSummaries`/`runSummarizeIterations` directly or hand-
  seeding a queue file: it calls `EnqueueIterationSummaryTool().Handler`
  (the `vp_enqueue_iteration_summary` MCP tool's own handler function, called
  directly rather than through a real MCP server) to create the queue job,
  confirms the resulting file on disk, then drives `runDrainSummaries` (the
  real `vp drain summaries` body) against it and asserts exactly one HTTP
  call to a canned `httptest` server and a matching vault-cached summary —
  proof that `vp_enqueue_iteration_summary` and `vp drain summaries`,
  wired through `DispatchSummarizer`, work end-to-end as an operator or
  agent would actually trigger them.

### Session-note summarizer wiring

A later phase of the same effort adds the session-note counterpart to
`internal/itersummary` above, wired the same way but through a genuinely
different job-*creation* path: unlike iterations, there is no
`vp_enqueue_session_summary` MCP tool. Enqueue is implicit and additive,
inside `internal/capture.WriteSession`, on every real (non-auto-capture)
capture — see "Background summarization queue + wrap-triggered drain" above
for that call site.

- **`internal/notesummary`** (new package, `session_note_summarizer_test.go`,
  `client_test.go`) — `SessionNoteSummarizer` implements
  `summarize.Summarizer` for `SummaryJobKind` `KindSessionNote`, mirroring
  `internal/itersummary.IterationSummarizer`'s shape: it reads the note via
  `vault.ReadSession`, asks its LLM client (`Summarizer.Generate`, its own
  `resultJSON.SearchSummary` wire field, distinct from
  `storage.SessionMeta.SearchSummary`) for a dense, keyword-forward 1-3
  sentence recap, and writes the result back onto the note's frontmatter
  (`SearchSummary`/`SearchSummaryAt`/`SearchSummaryModel`) via
  `vault.RewriteSession`, leaving the note's body untouched.
  `NewSessionNoteSummarizerFromConfig(cfg storage.SummarizationConfig, vault
  *storage.Vault)` resolves the SAME `[summarization]` config section
  iterations use (there is deliberately no second config section for this) and
  mirrors `NewIterationSummarizerFromConfig`'s exact return contract: disabled
  config returns `(nil, nil)`; enabled-but-unresolvable returns `(nil, err)`;
  resolvable returns `(summarizer, nil)`.
- **`notesummary.LengthGateBytes`** (1600 bytes, defined once in
  `prompt.go`) — the point past which a session note's raw body stops
  fitting inside `chunk.DefaultChunkConfig`'s first couple of raw chunks and
  starts fragmenting across 3+, which is exactly where a single dense summary
  row helps retrieval most; below it, a note is already a compact,
  easily-retrievable unit and a summarization pass buys little. Enforced
  twice: `internal/capture/session.go`'s enqueue site (`len(body) >
  notesummary.LengthGateBytes`, alongside the existing `!isAutoCapture` and
  `p.CWD != ""` gates — see the "Background summarization queue" section
  above) is the PRIMARY gate, so a short note is never even queued;
  `SessionNoteSummarizer.Summarize`'s own belt-and-suspenders check against
  the same constant is a benign no-op success (not a retry-consuming error)
  guarding only a job enqueued before the gate existed or before a config
  change took effect.
- **`internal/summarize.DispatchSummarizer`** — its `SessionNote Summarizer`
  field (alongside the existing `Iteration` field) now gets a real handler.
- **`cmd/vp/cmd_drain.go`** — `runDrainSummaries` resolves
  `notesummary.NewSessionNoteSummarizerFromConfig(cfg.Summarization, vault)`
  alongside the existing iteration-summarizer resolution, with its own
  warn-and-proceed handling on an unresolvable config (never a hard drain
  failure). It then builds `dispatcher := &summarize.DispatchSummarizer{
  Iteration: iterationSummarizer, SessionNote: sessionNoteSummarizer}`,
  applying the EXACT same nil-boxing-safe pattern the iteration side already
  uses: `sessionNoteSummarizer` is declared as the `summarize.Summarizer`
  INTERFACE and assigned only inside `if ns != nil`, never assigned directly
  from the concrete `*notesummary.SessionNoteSummarizer` — a direct
  assignment would box a non-nil-typed nil pointer into a non-nil interface
  value (Go's typed-nil trap), which would make `DispatchSummarizer`'s own
  `d.SessionNote != nil` check pass and dispatch into `Summarize` on a nil
  receiver instead of correctly treating a disabled/unresolvable summarizer
  as "no handler registered".
- **`internal/search/notes.go`** — `collectNoteCorpus` now emits an
  additional summary row (`SourceType noteSummarySourceType`, its own
  `SourceRef`/cache id) whenever `meta.SearchSummary` is set, alongside the
  note's existing raw row(s) — the session-note-corpus analogue of the
  iteration corpus's summary-row addition above. Covered in
  `notes_test.go`'s `TestCollectNoteCorpus_SummaryRowDistinctFromRawRow` and
  `TestCollectNoteCorpus_SummaryCacheIDDistinctFromRawChunkZero`.
- **`cmd/vp/integration_session_summary_test.go`** (new) —
  `TestCaptureThenDrain_SessionSummaryEndToEnd`, this side's end-to-end proof,
  built the same way as `TestEnqueueThenDrain_EndToEnd` above but against the
  session-note job-creation path: it calls
  `tools.CaptureSessionTool(vault, nil).Handler` (the real `vp_capture_session`
  MCP tool's own handler, called directly with JSON-marshaled args) with a
  summary long enough that the rendered body clears
  `notesummary.LengthGateBytes` — asserted explicitly in the test rather than
  assumed, mirroring `internal/capture/session_test.go`'s own length-gate
  tests — and a tag other than `storage.TagAutoCapture`. It confirms the real
  `KindSessionNote` queue file lands on disk, drives `runDrainSummaries` (the
  real CLI entrypoint body) against it, and asserts the queue file is gone and
  `vault.ReadSession` now reports `SearchSummary`/`SearchSummaryAt`/
  `SearchSummaryModel` all populated from the canned `httptest` response. As
  the final tie-together step it drives `internal/search`'s real corpus path
  — `search.NewEngine` + `Engine.Rebuild` + `Engine.Search` (all exported),
  since `collectNoteCorpus` itself is unexported and this test lives outside
  package `search` — and asserts the rebuilt index produces a summary-row
  search hit distinct in both `SourceRef` and `SourceType` from the note's raw
  row, proving the whole pipeline (capture → enqueue → drain → LLM → cache
  write → search index) connects end to end, not just that each piece works
  in isolation.

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
