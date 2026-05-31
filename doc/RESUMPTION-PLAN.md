# Vibe-Palace Resumption Plan

**Date:** 2026-05-08
**Revised:** 2026-05-09 (Phase C pre-execution review — verified against HEAD after Phases A + B merged in PR #1)
**Status:** Decision made. Ready to resume.
**Authors:** John Suykerbuyk, Claude Opus 4.7 (1M context)

This document captures the decision to resume vibe-palace as the strategic
successor to vibe-vault, the verification work done before that decision,
and the concrete cutover plan with rollback points.

---

## 1. Context & Decision

Vibe-palace was put down at iter 70 (2026-04-16) after a sustained build-out
of Phases 1–10, 12–15 and a 10-item `health-cleanup-sprint` that addressed
feature-accretion drift identified on 2026-04-14. The operator chose to keep
vibe-vault in production and continue making incremental fixes there rather
than continue iterating on vibe-palace, because real software projects had
priority.

In the three weeks since (vibe-vault iters 217–232), the vault ran clean
and shipped real value (grok-as-first-class-provider, classifier idempotency,
session-source pluggability), but the underlying friction did not improve:
restart and wrap operations now consume token budgets comparable to the
work itself, vault management remains brittle (Obsidian-git-plugin
auto-commit races, multi-machine vault-narrative drift, two-wraps-per-iter
patterns), and the architecture is fundamentally Claude-Code-coupled
despite ongoing migration toward MCP prompts and `vv command get` template
serving.

The operator's stated long-term intuition — *mechanical binary manages
the vault, MCP becomes a thin adapter, eventually a client-server model
with one vault manager and many concurrent consumers* — maps almost 1:1
to vibe-palace's PRD design principles 1.1, 1.5, and §2 architecture.

**Decision:** Resume vibe-palace work. Vibe-vault remains running in
parallel until cutover (Phase E below). No vibe-vault data is destroyed
at any point; the migration is idempotent and the shared-vault pattern
in `doc/MIGRATION.md` allows both systems to coexist on the same vault
directory indefinitely.

---

## 2. Pre-Flight Verification (Completed)

### 2.1 HEALTH.md Cleanup-Sprint Verification (2026-05-08)

Audited every "DONE (commit pending)" claim in HEALTH.md's "Top 10
Refactoring Wins" matrix against HEAD via grep + file inspection.

| # | Item | Status | Evidence |
|---|---|---|---|
| 1 | Unify slug validation | ✅ shipped | No `ValidateSlug` references in `internal/`; all callers use `slug.Validate` |
| 2 | Single config.toml orchestrator | ✅ shipped | `internal/migrate/vibevault.go:82` calls `reconcileVaultProject(ctx, vault, projSlug)` |
| 3 | Collapse `IndexDrawer`/`IndexDrawerWithVec` | ✅ shipped | `IndexDrawerWithVec` deleted; `IndexDrawer` is a thin wrapper around `IndexDrawers`; parity test `TestIndexDrawersParityAcrossPaths` |
| 4 | Extract `agentfile.WireAll` | ✅ shipped | `internal/agentfile/wireall.go:80` defines `WireAll(projectRoot, opts...)`; convergence test `TestIntegrationWireAllOrchestratorConvergence` |
| 5 | Template Executor API | ✅ shipped | `internal/templates/executor.go:42` defines `BackupPolicy` enum (Never/Always/Rename) |
| 6 | Tool-orchestration journey tests | ✅ shipped | All 5 `journey_*_test.go` files + `TestTools_SchemaDriftMatrix` at `internal/integration/tools_lifecycle_test.go:253` |
| 7 | Error capture & logging sweep | ✅ shipped | `internal/mcp/tools.go:185-228` has `slog.Debug`/`Warn`/`Error` with `op/tool/request_id/elapsed_ms`; zero `_ = f.Close()` patterns remain |
| 8 | KG entity dedup | ✅ shipped | `internal/kg/extract_all.go:48` defines `DefaultMinMentions = 2` |
| 9 | Extract `DrawerID` + `projectCacheDir` | ✅ shipped | `internal/storage/drawers.go:33` exports `DrawerID`; `internal/testutil/cachedir.go` exists |
| 10 | Delete deprecated `vp config upgrade` | ✅ shipped | `cmd_config.go` is 597 LOC (claim was 601); `TestUpgradeAliasParity` at `cmd_config_test.go:1000` |

**Verdict:** All ten items in the cleanup-sprint matrix are fully shipped at
HEAD — every "DONE (commit pending)" claim is backed by code, and even
Item 9 (which the matrix did not mark DONE) is present. The codebase is
in a clean state for new work to build on; no further architectural cleanup
is required before resuming feature development. **HEALTH.md itself
should be refreshed** since "commit pending" is now stale on every row.

Test suite at HEAD: all packages pass `-short` (`internal/integration`,
`kg`, `llm`, `mcp`, `migrate`, `palace`, `project`, `reconcile`, `search`,
`shims`, `skills`, `slug`, `storage`, `templates`, `testutil`, `tools`,
`vplog`).

### 2.2 Migration Dry-Run on Live Vault (2026-05-08)

Command: `vp migrate vibevault --vault-path /home/johns/obsidian/VibeVault --dry-run --yes`
Source: live VibeVault at `~/obsidian/VibeVault` (1880 notes, 926
session-index entries, 232 vibe-vault iterations).
Log: `/tmp/vp-migrate-dryrun.log` (940 lines).

**Summary:**

```
Would import: 34 projects, 196 sessions imported, 701 skipped, 0 drawers, 0 entities, 0 triples (4 errors)
```

**Project count:** 34 projects (vs. 29 in MIGRATION.md's 2026-04-09 snapshot)
— matches the natural growth from continued vibe-vault use.

**The 701 already-imported markers** live at
`~/obsidian/VibeVault/palace/<project>/.local/imported-sessions.jsonl` —
artifacts of an earlier vibe-palace dogfooding run. The migration code
reads these markers from the same vault root it imports into, and
correctly emits `(skipped)` for any session ID already in the marker
file. This proves migration idempotency is working: re-running the
import only ingests the delta (196 sessions added since the last
dogfood run).

**The 4 YAML parse errors:**

| Project | Error | File location |
|---|---|---|
| `proteus-hvpc-support` | `parse frontmatter YAML: yaml: control characters are not allowed` | unknown filename — error event does not carry the source path |
| `proteus-rs` | `parse frontmatter YAML: yaml: invalid trailing UTF-8 octet` | unknown |
| `proteus-rs` | `parse frontmatter YAML: yaml: invalid trailing UTF-8 octet` | unknown |
| `rezbldr` | `parse frontmatter YAML: yaml: control characters are not allowed` | unknown |

These are pre-existing data-quality issues in the source vault — likely
from terminal-output captures that included non-printable bytes that
landed in a session-summary frontmatter field. Scope is bounded (4 files
out of 897). **Two remediation paths exist:**

1. **Repair the source files** by stripping control characters / fixing
   UTF-8 encoding in the offending sessions. Manual one-time cleanup.
2. **Make `internal/migrate/vibevault.go`'s frontmatter parser tolerant**
   to malformed YAML — fall through to a "skip with warning" path
   instead of an error. Logs the file name so the operator can audit.
   Adds resilience for future data-quality issues.

**Recommended:** path (2) — tolerance — because the same data quality
issue *will* recur in future captures (terminals occasionally emit
control characters, UTF-8 decoding bugs are perennial). Path (1) papers
over the symptom; path (2) addresses the class.

**Phase B below specifies the implementation.**

**`0 drawers, 0 entities, 0 triples` is by design** — `--dry-run`
short-circuits before the embedder/indexer/extractor passes. The real
import will fill these. Verifying real-import drawer/KG counts is
Phase C. **Note (verified 2026-05-09):** the prior 2026-04-16 dogfood
run already populated ~22 projects under `palace/` with drawers, KG
entities/triples, and embed-cache; the live `imported-sessions.jsonl`
files now carry 705 successful-import markers. Phase C is therefore a
**delta migration on top of that state**, not a from-scratch build —
see Phase C pre-flight for the cold-rebuild option.

### 2.3 Vault Layout Confirmation

```
~/obsidian/VibeVault/                       # SHARED — both systems coexist
├── Projects/                               # vibe-vault writes; vibe-palace reads-only
│   └── <project>/
│       ├── sessions/                       # vibe-vault session markdown
│       ├── tasks/, iterations.md, resume.md
│       └── agentctx/, config.toml
├── palace/                                 # vibe-palace exclusive
│   ├── .local/                             # machine-local; gitignored
│   └── <project>/
│       ├── .local/imported-sessions.jsonl  # idempotency markers
│       ├── drawers/                        # chunked + embedded content
│       └── kg/                             # entities + triples
└── .vibe-vault/                            # vibe-vault exclusive

~/obsidian/vibe-palace-vault/               # SEPARATE (currently empty)
└── (Templates/, .vibe-palace/templates.lock, .obsidian/)
```

The `vp` config currently points at `~/obsidian/vibe-palace-vault` (a
separate, empty vault). All real palace data lives in
`~/obsidian/VibeVault/palace/` because the prior dogfooding run was
done with the operator's `--vault-path` pointed there. **For cutover,
vibe-palace's config should be updated to point at the shared vault**
(`~/obsidian/VibeVault`); the separate vault becomes obsolete and can
be archived or removed.

---

## 3. Cutover Plan

Phases are ordered for monotonic risk reduction. **Each phase has a
rollback to the prior phase**; vibe-vault remains the production
session-capture and context system through Phase E.

### Phase A — HEALTH.md Refresh & Vault Path Reconciliation (1–2 hours)

1. Update `~/.config/vibe-palace/config.toml`'s `vault_path` to
   `/home/johns/obsidian/VibeVault` (the shared vault).
2. Refresh `doc/HEALTH.md` to reflect that all 10 cleanup items shipped.
   Replace "DONE (commit pending)" with "shipped (verified 2026-05-08)"
   and link to this document. Mark the doc itself as historical.
3. Run `vp check` against the new vault path; expect all rows to pass
   except possibly model-cache rows that need re-pointing.
4. Commit both changes on a `resume-from-vibe-vault` branch (do NOT
   merge yet — Phase B work also lands on this branch).

**Rollback:** revert the config.toml change (one-line). HEALTH.md edit
is informational; rollback if needed by `git revert`.

### Phase B — Migration-Tolerance Hardening (~3 hours)

Goal: the 4 YAML parse errors observed in the dry-run no longer abort
ingestion of the affected session. Same code path tolerates future
control-character / invalid-UTF-8 frontmatter without operator
intervention.

1. In `internal/migrate/vibevault.go`'s session-parse loop, wrap the
   frontmatter parse in a per-file recovery: on YAML parse error,
   emit a `ProgressError` event with the source file path AND a
   `ProgressSessionSkip` event (so counts are correct), then `continue`
   instead of `return`. The session is marked
   `imported-sessions.jsonl` with a `parse_failed` reason so a future
   re-migration after manual repair can pick it up.
2. Add a `--strict` flag for operators who want the abort-on-error
   behavior (preserves test fixtures' current expectations).
3. Add a fixture-based unit test: malformed-frontmatter input → tolerant
   path emits the right events; `--strict` aborts.
4. Re-run dry-run on the live vault; expect 4 errors → 4 skip events
   with file paths logged.

**Rollback:** revert the commit on the branch.

### Phase C — Real Migration on the Shared Vault (delta or cold-rebuild)

**Pre-flight:** Diff the indexer-pipeline packages since the prior
dogfood run (2026-04-16) to decide whether Phase C is a delta migration
or a full cold rebuild:

```
git log --since=2026-04-16 --oneline -- \
  internal/capture/ internal/kg/ internal/embedder/
```

If the output is empty (no changes since dogfood), Phase C is a **delta
migration** and runs in roughly 5 min on the current state; existing
drawer/KG files for the 22 already-populated projects stay valid.
`isSessionImported` in `internal/migrate/vibevault.go:175-195` gates the
entire `IndexTranscript` pipeline on the marker file, so previously
imported sessions skip without re-embedding.

If the output is non-empty, choose:

- **Accept drift**: run delta migration; existing drawers/KG remain
  whatever schema they had on 2026-04-16. Acceptable for forward
  motion; revisit at Phase E if any drift is observed in the cutover
  acceptance criteria.
- **Cold rebuild**: `rm -rf ~/obsidian/VibeVault/palace/` and re-run
  Phase C from scratch. Wall-time ~30 min, dominated by the ONNX
  embedder (KG extractor at `internal/kg/extract_all.go:138` is
  regex-based and fast). Embed-cache hashes are content-keyed, so a
  from-scratch rebuild reconciles on its own — but a `rm -rf palace/`
  also drops the warm embed-cache, so first-run cost is paid in full.

1. With Phase A + B landed, run from a **fresh terminal with a TTY
   attached** (NOT from inside an AI session — `cmd/vp/cmd_migrate.go:114-125`
   picks `InteractiveResolver` only when stdin is a TTY; non-TTY falls
   back silently to `AutoResolver` and may auto-rename slugs in
   unexpected ways):

   ```
   vp migrate vibevault --vault-path /home/johns/obsidian/VibeVault
   ```

   No `--dry-run`; no `--yes` (use the interactive resolver to surface
   any new slug collisions since the 2026-04-16 dogfood run).

2. Capture the result: drawer count, entity count, triple count, total
   wall-time, model-cache size at completion. Compare against the
   pre-flight baseline (705 markers, 22 populated projects on
   2026-05-09) so the delta is auditable.

3. Validate semantic search returns sensible results. `cmd/vp/cmd_search.go:51-58`
   auto-detects the project from cwd via `project.DetectProject(".")`,
   so either `cd ~/code/vibe-vault` first or pass `-p vibe-vault`
   explicitly:

   ```
   vp search -p vibe-vault "wrap-model-tiering"        # expect iter 161-167 sessions
   vp search -p vibe-vault "Grok provider"             # expect iter 230 + adjacent
   vp search -p vibe-vault "session-source-interface"  # expect iter 223 + adjacent
   ```

4. Validate KG queries via MCP. There is no `vp kg` CLI today; the KG
   surface is exposed only as `vp_kg_stats` and `vp_kg_query` MCP tools
   (`internal/tools/kg_tools.go:111-159`). The HTTP transport is
   REST-style (`internal/mcp/transport_http.go:64-69`): `POST
   /tools/{name}` with the tool arguments as the raw JSON body. Two
   paths:

   **(a) Quick smoke via `vp serve` + curl** (default port 7423):
   ```
   vp serve &
   # Sanity:
   curl -s localhost:7423/health
   curl -s localhost:7423/tools | jq '.[].name' | grep -E 'vp_kg_|vp_bootstrap'

   # KG stats + query:
   curl -s -X POST localhost:7423/tools/vp_kg_stats \
     -H 'content-type: application/json' \
     -d '{"project":"vibe-vault"}'
   curl -s -X POST localhost:7423/tools/vp_kg_query \
     -H 'content-type: application/json' \
     -d '{"project":"vibe-vault","entity":"vibe-vault"}'
   ```
   Expect non-zero `entity_count` / `triple_count` in the stats result;
   expect a non-empty `triples` array referencing related projects and
   decisions in the query result. Responses are wrapped in
   `{"result": ...}` per `transport_http.go:124`.

   **(b) Through Claude Code MCP** — only after vp MCP is registered
   in the IDE, which is a Phase E task. Until then, prefer path (a).

5. Validate `vp_bootstrap_context` returns structured (not blob)
   output. Until vp MCP is registered in an IDE, smoke-test via the
   same `vp serve` + curl path:

   ```
   curl -s -X POST localhost:7423/tools/vp_bootstrap_context \
     -H 'content-type: application/json' \
     -d '{"project":"vibe-vault","max_tokens":4000}' | jq '.result | keys'
   ```

   Expect a structured JSON object with `workflow`, `resume`,
   `active_tasks`, `recent_sessions`, `kg_snapshot` fields — NOT a
   single 57 KB markdown blob. The handler at
   `internal/tools/context_tools.go:84-91` honors `max_tokens` with a
   deterministic shed order (`recent_sessions` → `kg_snapshot` →
   `commands+skills`; `post_bootstrap_instructions` always preserved
   per `context_tools.go:169-198`). Confirm the budget is respected by
   trying both `max_tokens: 4000` and `max_tokens: 16000` and observing
   the size delta:

   ```
   for budget in 4000 16000; do
     curl -s -X POST localhost:7423/tools/vp_bootstrap_context \
       -H 'content-type: application/json' \
       -d "{\"project\":\"vibe-vault\",\"max_tokens\":${budget}}" \
       | wc -c
   done
   ```

**Rollback** (scoped to the failure mode rather than nuke-everything):

- **Single project's import failed mid-run**: delete just that
  project's `~/obsidian/VibeVault/palace/<project>/.local/imported-sessions.jsonl`
  and re-run. Embed-cache hash-keys are content-stable, so existing
  `.vec` files re-attach without re-embedding.
- **Pipeline-wide corruption suspected**: `rm -rf ~/obsidian/VibeVault/palace/`
  and cold-rebuild per the pre-flight section above. Drops the warm
  embed-cache; first run pays the full embedder cost again.
- Either way: vibe-vault remains the production session-capture and
  context system; no data is at risk in the source `Projects/` subtree.

### Phase D — Parallel Operation: Both Systems Live (1 week)

> **Update 2026-05-30:** Phase D was closed via retrospective
> reconciliation rather than a forward 7-day window — vibe-palace reached
> parity-or-better with vibe-vault (98.0% vs 94.9% ground-truth session
> coverage, Jaccard 96.9%, zero vv-only misses). See
> `doc/PHASE-D-OPERATOR-BRIEF.md` and
> `agentctx/dogfood-log.md §Retrospective Reconciliation`. Operator
> decision to advance to Phase E is pending.

Both `vv hook` (Claude Code) and `vp_capture_session` (MCP) capture in
parallel. New sessions land in BOTH systems' subtrees. No data is lost
if either system is killed.

**Acceptance criteria for advancing to Phase E (each must hold for
≥7 days):**

- `vp_bootstrap_context` returns context that subjectively matches or
  beats `vv_bootstrap_context` for restart usefulness.
- `vp_search` finds prior decisions on at least three real "what did
  we decide about X" queries that previously required scrolling
  iterations.md or grepping the vault.
- `vp_capture_session` calls land cleanly with no schema errors.
- The IDE-hook auto-capture (final 2026-04-16 commit) successfully
  observes ≥80% of new Claude Code and Zed sessions automatically,
  without operator intervention.
- Wrap operation tokens-burned drops measurably vs. vibe-vault
  baseline. (Define a baseline first: median wrap is ~28K tokens
  combined per iter 167.)

**Telemetry:** keep a `dogfood-log.md` in this project's `agentctx/`
(once we have one) recording each session's "would I rely on this?"
verdict.

**Rollback:** vibe-vault is still doing all the real work. If
vibe-palace fails any acceptance criterion, defer cutover and fix the
gap before retrying Phase D.

### Phase E — Hook Cutover (~1 hour)

1. Remove `vv hook` from Claude Code `settings.json`.
2. Install vibe-palace's IDE hook in its place (per the
   `auto-capture-ide-hooks` commit's settings install).
3. Verify next session captures only via `vp` paths. Vibe-vault index
   stops growing.
4. Verify all editor MCP configs (Claude Code `.mcp.json`, Zed
   `~/.config/zed/settings.json`) advertise `vp` as the primary
   server.
5. Update `~/code/CLAUDE.md` and per-project `CLAUDE.md` files to call
   `vp_bootstrap_context` instead of `vv_bootstrap_context`. (The
   `agentfile.WireAll` orchestrator in vibe-palace handles this
   pattern.)

**Rollback:** restore the removed `vv hook` line and reverse the
`vp` MCP registration. Both systems' captured data persists.

### Phase F — Vibe-Vault Repository Quiescence (asynchronous)

1. Vibe-vault's git repo at `~/code/vibe-vault` is left intact
   indefinitely. No deletions.
2. The `agentctx/` and `iterations.md` files in
   `~/obsidian/VibeVault/Projects/vibe-vault/` are the canonical
   project history; vibe-palace continues reading them via the shared
   `Projects/` subtree.
3. Mark vibe-vault's `~/obsidian/VibeVault/Projects/vibe-vault/resume.md`
   with a top-of-file note: *"Project archived 2026-MM-DD; new work
   continues at ~/code/vibe-palace."*
4. Optionally: schedule the vibe-vault repo for archive-and-readonly
   on GitHub after a 30-day quiescence period.

---

## 4. Open Questions for Next Session

These are bounded follow-ups that surfaced during this verification
pass. None block resumption; all benefit from being filed as tasks
once vibe-palace task tooling is the primary surface.

1. **HEALTH.md should refresh.** Every "DONE (commit pending)" row is
   stale. Phase A item 2 covers this.

2. **Migration error events do not carry source file paths.** The 4
   YAML errors in the dry-run reported `(rezbldr)` etc. but not the
   filename. Trace the `ProgressError` emit site at
   `internal/migrate/vibevault.go:118-126` and confirm the file path
   is available in the parse-error scope; if so, add it to
   `evt.Message`.

3. **`--vault-path` flag semantics double as source AND palace store.**
   Worked correctly in the dry-run, but documentation in
   `doc/MIGRATION.md` and `vp migrate vibevault --help` should make this
   explicit. A separate `--target-vault` flag, if added, would let
   operators import from one vault into another — useful for testing.

4. **Two `vibe-palace` config rows may need normalization.** The
   currently-configured vault `~/obsidian/vibe-palace-vault` has only
   `Templates/` and `.obsidian/` — it's effectively empty after a stale
   `vp init`. Decide whether to delete it post-cutover or repurpose it.

5. **Wrap and bootstrap token-cost telemetry.** Phase D acceptance
   criteria reference "tokens-burned drops measurably." Vibe-vault has
   no such telemetry today (the `wrap-dispatch.jsonl` records dispatch
   timing but not bootstrap cost). Vibe-palace should ship a
   `vp_bootstrap_context` telemetry event recording payload size and a
   per-tool dispatch metric so the cutover claim can be verified
   numerically. Likely a small addition to `mcp/tools.go:makeHandler`
   given the slog discipline already in place.

6. **`vv-session-guidelines-mid-session-safety` thread (vibe-vault iter
   232).** Carries forward into vibe-palace as `vp_session_guidelines`
   prompt-body review. Same review applies — the prompt body should
   be safe to invoke mid-session.

7. **Multi-client concurrency (vibe-vault iter 232 decision).**
   Documented as an open design question in vibe-vault's
   `doc/ZED-AGENT-PANEL-INTEGRATION.md`. Vibe-palace's MCP server
   already serves multiple concurrent clients (stdio + HTTP). The same
   "two-writer same-vault" risk class applies; the vibe-palace server's
   write-lock discipline (search.Engine internal locks; storage.Vault
   atomic writes) is the structural answer, but a top-to-bottom audit
   of every write path for client-id-tagged ordering would harden
   confidence.

---

## 5. Why Vibe-Palace Will Render Greater Long-Term Value

Recorded for future reference / future-operator context, in case the
strategic question recurs.

The operator's stated long-term intuition was:

> Move most workflow operations to mechanical binary implementations
> that manage the vault, from fetching and injecting context, to
> logging and preserving workflow history. The MCP itself will serve
> more as an adapter layer, the vault management binary will do most
> of the work. We will move from using prompt engineering to calling
> tooling in the vault management binary. It is possible that we end
> up with a client server model, where one binary manages the vault
> and serves many concurrent consumers of the vault resource by way
> of MCPs.

This is exactly vibe-palace's PRD Design Principle 1.1: *"The MCP
interface is the product. File system conventions, symlinks, and
markdown file management are implementation details that should be
invisible to both the AI consumer and the human developer."* And §2's
architecture diagram shows MCP + HTTP + CLI all calling the same
service layer — already a client-server shape.

Vibe-vault's recent trajectory (iters 161–232) is *moving toward* the
same destination — wrap-model-tiering, marker-bounded resume regions,
idempotent `vv_append_iteration`, pluggable SessionSource, MCP-prompts
as Agent-panel slash commands — but every step is incremental against
deeply-baked architectural assumptions: slash commands as
LLM-executed markdown, wrap as multi-stage LLM dispatch with QC
fallback, monolithic resume.md, Obsidian-git-plugin coordination. The
next 50 vibe-vault iterations would be re-deriving vibe-palace's
architectural decisions one DESIGN entry at a time.

Vibe-palace skipped that derivation because it was designed for the
endpoint from day one. The HEALTH.md sprint addressed feature-accretion
drift (verified shipped, §2.1 above). The IDE-hook auto-capture
(2026-04-16) closed the last cross-environment capture gap. What remains
is dogfooding at scale and the cutover itself — Phases C–E above.

The cost of resuming is the focused 2–3 week sprint outlined in §3.
The cost of *not* resuming is years of incremental architectural
migrations in vibe-vault that may never reach the same destination,
plus the iter-velocity tax compounding every iteration.

---

## 6. References

- `doc/PRD-vibe-palace.md` — full architecture and phase specifications
- `doc/HEALTH.md` — 2026-04-14 health audit (refresh in Phase A)
- `doc/MIGRATION.md` — vibe-vault → vibe-palace migration mechanics
- `doc/COMMANDS-AND-SKILLS.md` — vpc-* / vps-* artifact patterns
- `doc/ARCHITECTURE.md` — service layer, storage layout, KG model
- vibe-vault iter 232 (`~/obsidian/VibeVault/Projects/vibe-vault/iterations.md`)
  — context for why this decision was made now
- `/tmp/vp-migrate-dryrun.log` — full dry-run output (940 lines), retained
  during this session; copy to `doc/migration-dryrun-2026-05-08.log` if
  desired for historical reference
