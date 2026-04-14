# Vibe-Palace Project Health & Maintainability Report

**Date:** 2026-04-14 (revised after critical review)
**Scope:** Full review of design & implementation across all internal packages,
CLI commands, MCP tools, and integration test strategy.
**Method:** Four parallel deep-dive reviews by focus area, then a targeted
code-verification pass that corrected several factual errors in the initial
synthesis. All citations in this revision have been grepped against HEAD.

---

## TL;DR

Vibe-palace is functional and well-tested (280 Go files, ~63K LOC, 30+
integration test files, consistent ONNX gating), but it shows classic
**feature-accretion drift**: each new capability (migrate → absorb →
reconcile → commands → skills → shims → skill artifacts) added its own
orchestration path. The result is:

- Two orchestrators calling the same low-level writer for `config.toml`.
- Three orchestrators driving the same single `agentfile.Wire` call.
- Two public indexing methods on `search.Engine` with overlapping contracts.
- Four identifiable duplicated primitives (slug validation, `drawerID`,
  `projectCacheDir`, vault-dirty git check).
- Three sequential regex-based entity extractors writing to the same KG
  without coordination.
- A logging framework (`vplog` → `slog`) that is correctly installed but
  under-used: several error paths discard information that should be
  captured and reported through `slog` without changing the caller's
  best-effort semantics.

The codebase does not need a rewrite. It needs **seam-tightening**: choose a
canonical orchestrator per artifact, consolidate duplicated primitives,
promote one integration-test style (tool-orchestration user journeys) over
per-function asserts, and audit every swallowed error against the
"capture-and-log, then decide" rule.

---

## Architecture at a Glance

```
                            ┌────────────── User / Agent ──────────────┐
                            │                                           │
                         CLI (vp)                                   MCP server
                            │                                           │
                 ┌──────────┼──────────┐                      ┌─────────┴────────┐
                 │          │          │                      │                  │
              cmd_init  cmd_commands cmd_skills  cmd_absorb   tools/* (38+ tools)
                 │          │          │              │               │
           ┌─────┴──────────┴──────────┴──────────────┴───────────────┘
           │
           ▼
   ┌───────────────────────────────────────────────────────────────┐
   │   Internal packages (24)                                       │
   │                                                                 │
   │   Vault mutation:  context · palace · project · capture ·       │
   │                    migrate · absorb · reconcile · templates ·   │
   │                    slug · agentfile                             │
   │                                                                 │
   │   Search stack:    search · embedder · storage · kg · llm       │
   │                                                                 │
   │   Surface glue:    cli · commands · skills · shims · check      │
   │                                                                 │
   │   Infra:           mcp · tools · integration · vplog            │
   └───────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
                      Obsidian vault / project scaffolds / agent files
```

---

## Cross-Cutting Finding #1 — Multiple Orchestrators Per Artifact

The CLAUDE.md rule is explicit: "any single file system artifact or knowledge
tracking feature should have ONE primary interface whenever reasonable." The
code mostly obeys this at the **writer** level — each artifact has exactly
one writing primitive. The violation is at the **orchestrator** level:
multiple CLI commands build their own plan-and-apply flows that call the
shared writer with different pre/post conditions.

### Orchestrators-per-artifact matrix (verified)

| Artifact | Writer primitive | Orchestrators | Severity |
|---|---|---|---|
| `<vault>/Projects/<slug>/config.toml` | `storage.Vault.WriteVaultProjectConfig` (`storage/config_writer.go:108`) | `migrate/vibevault.go:78` (direct); `reconcile/vault_project.go:147` (via `Apply`) | **High** — migrate bypasses the reconciler's Check/Plan/Apply ceremony, so idempotency + drift detection semantics differ between re-migration and `vp init` re-runs. `absorb` only *reads* this file as a guard (`absorb/writer.go:177`), it does not write it. |
| `<project_root>/.vibe-palace.toml` (cwd project file — distinct from the vault-project `config.toml`) | `reconcile/cwd_project.go` via its `Apply` | `cmd_init` only | Low — single orchestrator. |
| `<vault>/Templates/commands/*.md` | Embedded-FS copy | `reconcile.TemplateTree.Apply` (materialize at init); `commands.Apply` (upgrade) | **High** — init uses `reconcile.ActionPrompt` ceremony with the lock file; `commands.Apply` is a bare atomic write. Two different upgrade stories for the same file set. |
| `<vault>/Templates/skills/*/*.md` | Embedded-FS copy | `reconcile.TemplateTree.Apply` (init); `commands.ApplyWithBackup` (skills upgrade) | **High** — plus asymmetric `.bak` emission (skills only, undocumented). |
| `.claude/commands/vpc-*.md` | `shims.Wire*` | `shims.WireSkill` (init); `shims.Apply` (commands upgrade) | Medium — no lock coordination; correct writer. |
| `CLAUDE.md` / `AGENTS.md` / `.cursorrules` / `.rules` / copilot | `agentfile.Wire` (sole writer — atomic, verified single primitive) | `cmd_init.go:478`; `commands/managed.go:73` (`ApplyAgentBlocks`); `absorb/writer.go:430` | Medium — **one writer, three orchestrators**. Not a CLAUDE.md rule violation, but the three orchestration sites have drifted scope (init wires all targets; `ApplyAgentBlocks` rewires stale blocks only; `absorb` wires the one just-written target). Consolidation is an ergonomics win, not a correctness fix. |
| Drawer index vectors | `search.Engine.indexVec` (internal) | `IndexDrawer` (embed-then-index), `IndexDrawerWithVec` (caller supplies vector), `Rebuild` (batch scan) | Medium — two public entry points with documented but overlapping contracts. |
| KG entities & triples | `storage.Vault.AddEntity`, `AddTriple` | Three sequential extractors in `capture/indexer.go:121,148,176` | Medium — three extraction passes, same write target, no coordination or frequency filter. |

### Consolidation recommendations (rescoped)

1. **`config.toml`** → promote `reconcile.VaultProject.Apply` as the sole
   orchestrator. `migrate/vibevault.go:78` should build a desired-state
   input and call the reconciler instead of `WriteVaultProjectConfig`
   directly. One idempotency story; one drift-detection story.
2. **Template / skill materialization** → introduce `internal/templates.Executor`
   owning the embedded-FS traversal currently in
   `reconcile/template_tree.go` (598 LOC). `reconcile.TemplateTree` and
   `commands.Apply`/`ApplyWithBackup` both delegate. Decide `.bak` policy
   centrally (always / never / opt-in).
3. **Agent-file wiring** → introduce `agentfile.WireAll(projectRoot)` that
   detects-all-targets and rewires-all-targets. Call from `cmd_init`,
   `commands.ApplyAgentBlocks`, and post-`absorb.Apply`. This is an
   **extraction of orchestration**, not writer unification — the writer
   (`agentfile.Wire`) is already single.

---

## Cross-Cutting Finding #2 — Duplicated Primitives (verified)

| Primitive | Duplicated in | Fix |
|---|---|---|
| Slug validation | `slug.Validate()` and `storage.ValidateSlug()` — byte-for-byte identical (regex, constants, error strings) | Delete `storage.ValidateSlug`; update callers in `project/detect.go:139,150,160`, `cmd_init.go`, internal `validateSlugs` helper in `storage/paths.go:43` to import `slug` |
| `drawerID()` | `storage/drawers.go:31` and `capture/indexer.go:207` (comment at line 205 acknowledges "mirrors storage.drawerID") | Export `storage.DrawerID`; delete the capture copy; import |
| Slug scanning (existing-slug collision) | `migrate/resolver.go:58` and `cmd/vp/cmd_migrate.go:141` | Merge into one exported helper (likely `slug.ScanExisting(vaultRoot)` or `migrate.ResolutionContext`) |
| `projectCacheDir()` (test helper) | `embedder/testcache_test.go` and `search/testcache_test.go` | Extract to `internal/testutil/cachedir.go` |
| Vault-dirty git check | `cmd_commands.go:211` (ad-hoc `git status` invocation); needed by skills-upgrade and config-sync too | Promote to `check.VaultClean(vaultRoot) Result` |

**NOT a duplication — leave alone:**
- `slug.Slugify` vs the variants in `project/detect.go` and `tools/kg_tools.go`.
  `slug/slug.go:44-47` documents these as *intentional* divergences (project
  detection slugs and KG entity IDs have different rules from path slugs).
  Any "unify slugification" refactor must first argue against that
  documented decision.

---

## Cross-Cutting Finding #3 — Error Capture & Logging Discipline

**CLAUDE.md rule**: "No silent failures in core business logic. We have the
`slog.Error` logging framework whose purpose is to capture what would
otherwise become silent errors in core business logic. No silent errors
should go uncaptured, even if the caller does not see the error."

**Current state.** `vplog.Init` (`internal/vplog/vplog.go:27`) sets
`slog.Default` to a JSON handler with 1MB rotation and a discard fallback.
The framework is correctly installed. Usage is **inconsistent**:

- **Good**: `capture/indexer.go:128-145,158-171,186-190` logs every KG write
  failure via `slog.Warn` with the offending entity/triple. The file-level
  comment at line 119 explicitly states KG enrichment is best-effort per
  PRD — this is a **documented design decision**, not a rule violation,
  and it already captures via `slog.Warn`.
- **Bad**: Silent `_ = f.Close()` patterns at `agentfile/wire.go:151,156`,
  `shims/wire.go:132,137`, `absorb/writer.go:476,481`. A `Close` on a
  write-side file descriptor can return a deferred write error; discarding
  it can mask data-loss.
- **Bad**: `migrate/resolver.go:79` "AutoResolver silently accepts the
  default rename suggestion" — a user-visible naming decision made without
  any log trail.
- **Ambiguous**: `reconcile/template_tree.go:251` "silent-adopt would skip
  it here and the main logging" — the file has six references to "silent"
  adoption of templates; none emit structured log records a maintainer
  could audit.
- **Missing**: `mcp/tools.go:147-169` (`makeHandler`) has zero `slog`
  calls. A panicking or error-returning handler leaves no trace at the
  protocol boundary — the single most important place to log, because
  tool calls are the agent's contract with the system.

### Capture-and-log discipline (normative for the cleanup sprint)

For every error-bearing call in `internal/` and `cmd/vp/`:

1. **If the error is acted on** — leave the existing error return in place.
2. **If the error is best-effort by design** — log via
   `slog.Warn` (not `Debug`) with: the package-qualified operation name,
   the identifying inputs (`project`, `slug`, `path`, `entity`), and the
   error. Never use `_ = err`.
3. **If the error is on a defer-Close or deferred-cleanup path** — log
   via `slog.Error` when the enclosing operation was a *write*. A
   write-side `Close` error can indicate a failed fsync; silencing it
   masks data loss. For read-side closes, `slog.Debug` is acceptable.
4. **If the code comment says "silently adopted / silently accepted /
   silently ignored"** — the log record is the maintainer's only
   audit trail. Emit one.
5. **At the MCP dispatch boundary** (`mcp/tools.go:makeHandler`) — log
   on entry (tool name, request ID), on handler return (elapsed, error
   or result size), and inside any `recover()` guard. This is the single
   highest-leverage log site in the project.

A dedicated refactor item (#7 below) tracks this sweep.

---

## Area Findings

### A. Vault-Mutation Stack (context · palace · project · capture · migrate · absorb · reconcile · templates · slug · agentfile)

**Intent:** Own the agent-context layer of the Obsidian vault — project
scaffolds, workflow/resume/knowledge files, managed blocks in agent files.

**Issues:**

- `internal/palace/metadata.go:28` and `discover.go:88` call
  `storage.Vault.ListWings/ListRooms/ListDrawers` directly. Palace should
  be a stateless classifier receiving pre-listed inputs; current coupling
  makes palace untestable without a full vault.
- `internal/context/precedence.go:68-138` has three near-identical nested
  loops constructing tier paths. Collapse to a single `(tier, rel) → path`
  helper.
- `internal/absorb/classifier.go:38` hardcodes section-to-file routes; no
  registry. Adding a new routing rule requires a code change.
- `internal/templates/embedded.go` only exposes `FS()` — every caller walks
  the embed.FS themselves. Promote to a `Template` struct with
  `(Name, Bytes, Hash, Rel)` triples used by both the reconciler and
  `commands.Apply`.

**Positives:** `internal/slug` is tight and minimal (74 LOC).
`internal/llm` is clean (90 LOC). `capture/` is reasonably isolated.
`absorb` guards correctly on `requireVaultProject` before writing.

---

### B. Search Stack (search · embedder · storage · kg · llm)

**Intent:** Chunk sessions, embed with ONNX (hugot) or mock, index into a
brute-force vector store, hybrid-rank with metadata, extract KG entities
and triples.

**Issues:**

- `search/engine.go:148` (`IndexDrawer`) and `search/engine.go:261`
  (`IndexDrawerWithVec`) are both public. The second exists for the
  capture pipeline to avoid double-embedding a pre-batched vector set.
  `IndexDrawerWithVec` has exactly one non-test caller
  (`capture/indexer.go:106`). Make it unexported or collapse both into a
  single `IndexDrawers(batch)` method that handles the batching decision
  internally.
- `search/engine.go:335-362` has incomplete chunk-aware dedup. Current
  heuristic (first-per-SourceRef) is shipping as a placeholder;
  `SearchResult` lacks a `ChunkIndex` field, so the documented follow-up
  cannot be implemented without a struct change.
- `capture/indexer.go:121-191` runs three sequential extractors against
  the same transcript, each writing to the same KG store, with
  duplicated "check exists → add entity → add triple" scaffolding. No
  frequency-of-mention filter, so a one-off typo becomes a permanent KG
  node. Error logging is already in place (good), but the extractors do
  not deduplicate across passes.
- No cache-invalidation story: `search/cache.go` writes under
  `vault.LocalDir()` but `storage` has no knowledge the cache exists.
  Delete a drawer → orphaned cache file.

**Positives:** `testing.Short()` gating is consistent across all four
ONNX-sensitive test files. Mock embedder is honest about its limits
(makes no semantic claims). `internal/llm` is minimal and well-scoped.
All three KG extractor sites already log their errors via
`slog.Warn`/`Debug`.

---

### C. User-Facing Surface (cmd/vp · cli · commands · skills · shims · check)

**Intent:** `vp init`, `vp commands upgrade`, `vp skills upgrade`,
`vp absorb`, `vp migrate`, `vp check` — the user-visible entry points.

**Issues (verified LOC):**

- `cmd_init.go` (**772 LOC**) orchestrates 4 phases across 5 reconcilers
  plus `agentfile.Wire` plus `shims.WireSkill`. No composition interface;
  phase order is rigid and not reused by other callers.
- `cmd_commands.go` (**597 LOC**) and `cmd_skills.go` (**491 LOC**) share
  the prompt/grouping scaffold in `upgrade_common.go` (143 LOC) but
  diverge on agent-block wiring, shim emission, and `.bak` policy. Not
  "twins" but heavily parallel.
- `commands.Apply()` vs `commands.ApplyWithBackup()`: commands never emit
  `.bak`, skills always do. No documented reason; user sees inconsistent
  behavior between upgrade paths.
- `cmd_config.go` has two upgrade paths: a deprecated legacy TOML-parsing
  path (lines 53-179) and the new reconciler-based `sync`. Needs a
  deletion date.
- `cmd_check.go:132` calls `check.CheckAgentDrift` ad-hoc, outside the
  `Plan → Apply` reconciler contract. Drift detection is divorced from
  remediation; what `check` finds has no single `vp fix` target.

**Positives:** `main.go` + `bootstrap.go` dispatch is clean. `cmd_absorb`
has clear scope. Flag conventions (`--dry-run`, `--overwrite`, `--only`)
are mostly consistent.

---

### D. MCP, Tools, Integration Tests, Logging (mcp · tools · integration · vplog)

**Intent:** Expose vibe-palace to agents via MCP; validate end-to-end
behavior through integration tests.

**Findings:**

- **38+ MCP tools, minimal CLI duplication.** `AssembleBootstrap`,
  `vp_cmd`, `vp_skill_cmd`, and command/skill resolution share their
  core between CLI and MCP — the right pattern. Most palace/KG/session
  tools are MCP-only, acceptable given MCP is the primary agent
  interface.
- **Validation duplication.** `tools/search_tools.go:107-117` validates
  params twice: JSON Schema at `mcp/tools.go:156` then manual `if
  p.Query == ""`. Remove the manual guards; trust the schema.
- **Integration-test style is split.** 10 of 31 files use `callTool`
  (protocol-layer, realistic). 21 test lower-layer logic without
  simulating the MCP boundary. The `testHarness` in `helpers_test.go` is
  excellent and under-used.
- **No tool-orchestration journeys.** No test exercises
  `bootstrap → search → resolve command → execute` as a single flow.
  Every test is single-tool. Real agent turns chain multiple tools; that
  integration is untested.
- **MCP dispatch is unlogged.** `mcp/tools.go:147-169` (`makeHandler`)
  contains zero `slog` calls. A silent handler panic or error leaves no
  trace at the single most important boundary in the system. Fix in
  refactor #7.

**Positives:** `testHarness` is reused across all 10 MCP tests.
`vplog/vplog.go` lifecycle is correct: one-shot init, graceful discard
fallback, never blocks startup.

---

## Integration Testing: A Better Strategy

The user's explicit goal — "best possible end-user experience" — calls
for a second layer of tests beyond the existing lower-layer asserts.

**Promote tool-orchestration user-journey tests** that chain multiple MCP
tools per test, matching what an agent does in a real session:

1. `TestJourney_NewProject_Bootstrap_Capture_Search`
   — `vp_init_project` → `vp_bootstrap_context` → `vp_capture_session`
   → `vp_search` finds the captured content.
2. `TestJourney_CommandDiscovery_And_Execution`
   — `vp_bootstrap_context` → `vp_list_commands` → `vp_get_command`
   → `vp_cmd`; verify the instruction body returned to the agent is
   self-consistent (no stale placeholders).
3. `TestJourney_KG_Lifecycle`
   — `vp_kg_add` → `vp_kg_query` → `vp_kg_timeline` → `vp_kg_stats`.
4. `TestJourney_Task_Lifecycle`
   — `vp_manage_task create` → `vp_list_tasks` → `vp_manage_task update`
   → `vp_manage_task retire`; verify task moves to `done/` and
   iterations are appended.
5. `TestJourney_Skill_Materialization_CrossIDE`
   — materialize a skill, then verify `.claude/skills/`,
   `.cursor/skills/`, AGENTS.md block, and `vp_get_skill_section` all
   agree.

These should use the existing `testHarness` and deliberately probe the
**protocol boundary** rather than internal functions. They complement
(do not replace) the lower-layer unit tests.

Secondary: a schema-drift matrix test in `tools_lifecycle_test.go` that,
for every registered tool, verifies missing-required-field requests
return a well-formed JSON-RPC error.

---

## Top 10 Refactoring Wins (revised and prioritized)

Ranked by impact ÷ effort across all four areas. Every item has been
verified against HEAD.

| # | Refactor | Scope | Effort | Impact |
|---|---|---|---|---|
| 1 | **Unify slug validation** — delete `storage.ValidateSlug`, route all callers through `slug.Validate`. Leave `slug.Slugify` and the intentional variants in `project/detect.go` + `tools/kg_tools.go` alone (documented at `slug/slug.go:44-47`). | slug, storage, project, cmd_init | ~2h | High |
| 2 | **Single `config.toml` orchestrator** — `reconcile.VaultProject.Apply` becomes the only orchestrator; `migrate/vibevault.go:78` builds a desired-state input and delegates. Scope strictly to the vault-project `config.toml`; do **not** touch the cwd `.vibe-palace.toml`, which already has a single orchestrator. | migrate, reconcile | ~3h | High |
| 3 | **Collapse `IndexDrawer` / `IndexDrawerWithVec`** — hide batching in the engine; single public `IndexDrawers(batch)` method. `capture/indexer.go:106` is the only non-test external caller of the vectored form. | search, capture | ~2h | High |
| 4 | **Extract `agentfile.WireAll(projectRoot)` orchestrator** — single rewire pipeline called by `cmd_init`, `commands.ApplyAgentBlocks`, and `absorb.Apply`. This is orchestration extraction, not writer unification; `agentfile.Wire` is already the sole writer. | agentfile, cmd_init, commands, absorb | ~3h | Medium-High |
| 5 | **Template `Executor` API** — move embedded-FS traversal out of `reconcile/template_tree.go` (598 LOC) into `internal/templates`; collapse `TemplateTree.Apply`, `commands.Apply`, and `commands.ApplyWithBackup` onto one plan/apply surface. Decide `.bak` policy centrally and document it. | templates, reconcile, commands | ~6h | High |
| 6 | **Tool-orchestration user-journey tests** — 5 multi-tool tests exercising real agent flows, using the existing `testHarness`. | internal/integration/ | ~6h | High |
| 7 | **Error capture & logging sweep** — implement the discipline in Finding #3: (a) add entry/exit/error logging in `mcp/tools.go:makeHandler`; (b) replace every `_ = f.Close()` on write-side descriptors with `slog.Error` on failure; (c) add `slog.Warn` at every code site whose comment says "silently adopted / accepted / ignored" (`migrate/resolver.go:79`, `reconcile/template_tree.go:251`, etc.); (d) add `slog` call plus identifying inputs to any best-effort error that is currently discarded. Does not change user-visible behavior; adds audit trail. | mcp, migrate, reconcile, absorb, shims, agentfile | ~4h | High |
| 8 | **Deduplicate KG entity extraction** — single `kg.ExtractAll(text, validators)` with a frequency filter and a dedup-across-passes coordinator; `capture/indexer.go` calls it once instead of three separate extractor loops. Keep the `slog.Warn` coverage already present. | capture, kg | ~3h | Medium |
| 9 | **Extract `drawerID` + `projectCacheDir`** — promote `drawerID` to `storage.DrawerID` (exported); extract `projectCacheDir` test helper to `internal/testutil/cachedir.go`. | storage, capture, internal/testutil | ~30m | Medium |
| 10 | **Delete deprecated `vp config upgrade` legacy path** — consolidate to reconciler-based `sync`. Needs a migration test that runs both paths on a fixture vault and asserts byte-identical output during the overlap window before deletion. | cmd_config | ~2h | Medium |

Total estimated effort: **~31.5 hours**. Items 1–7 are the core cleanup
and should land before further feature work. Item 7 (error capture) is
safe to land early and independently; it cannot regress user-visible
behavior and it de-risks every subsequent refactor by ensuring any
latent failure surfaces in the log.

---

## Phasing

Refactors are not fully independent. Suggested order:

1. **Land items 1, 7, 9 in parallel first.** All are pure cleanups with
   no structural risk, and item 7 buys visibility into any failure the
   rest of the sprint might introduce.
2. **Then item 2** (config.toml orchestrator) — small blast radius;
   depends on item 1 for clean slug-validation imports.
3. **Then items 3, 4, 8 in parallel.** Each is a single-area
   refactor; error logging from item 7 is now in place to catch
   regressions.
4. **Then item 5** (template Executor) — largest refactor; best done
   after 3 + 4 so the seam between `commands` and `reconcile` has
   already been cleaned.
5. **Item 6 (user-journey tests) runs concurrently with every other
   item** — it should be a separate parallel workstream from day one,
   because it defines the acceptance bar for the refactored system.
6. **Item 10 (deprecate legacy `vp config upgrade`)** — last; do not
   delete until the journey tests cover the replacement path.

---

## UX Improvements

Lower-effort, user-visible wins not counted above:

1. **Harmonize `--only` semantics** between `commands upgrade` and
   `skills upgrade` (currently skills requires a separate `--granular`
   flag).
2. **Single `vp upgrade`** command that runs config-sync →
   commands-upgrade → skills-upgrade in order with a unified progress
   report.
3. **`.bak` policy** — pick always-on or never, document, add
   `--no-backup` opt-out if always-on. Today the behavior differs
   silently between commands and skills upgrades.
4. **Suppress dirty-vault warning** when `--dry-run` or `--overwrite` —
   nothing is at risk.
5. **Silent auto-wire of agent files during init** unless something is
   missing — don't emit a "run vp init first" message to a user who is
   running `vp init`.

---

## What's Working Well (Keep Doing)

- **Shared core, separate entry points** for bootstrap, command
  resolution, skill resolution — CLI and MCP delegate to the same
  functions.
- **ONNX test gating** (`testing.Short()`) is consistent.
- **Mock embedder** is honest about making no semantic claims.
- **`testHarness`** in `internal/integration/helpers_test.go` is reused
  by every MCP protocol test — excellent DRY.
- **Logger lifecycle** — one-shot init, graceful fallback, never blocks
  startup. The framework is correct; the discipline around using it
  (Finding #3) needs work.
- **`slug/slug.go:44-47` in-code documentation of intentional
  divergence** — this is exactly the style of comment that prevents
  well-meaning "unification" refactors from silently changing behavior.
- **Man-page generation** wired into `make build`.
- **Agent-file single writer** (`agentfile.Wire`) with atomic write
  semantics — the right shape; just need orchestration extraction.

---

## Closing Recommendation

Pause net-new features for one focused cleanup sprint on items 1–7 above
(~23h).

These seven refactors will:

- Cut ~300 LOC of duplication.
- Collapse two orchestrators of `config.toml` into one and three
  orchestrators of agent-file wiring into one, without touching the
  writers themselves.
- Introduce the error-capture discipline that makes every subsequent
  refactor safer by ensuring failures leave a trace.
- Establish the user-journey test discipline that matches the project's
  stated value ("best possible end-user experience").

After that sprint, the project will be meaningfully simpler to extend,
noticeably easier for a new maintainer to hold in their head, and
observable at the seams where feature accretion has historically
created drift.
