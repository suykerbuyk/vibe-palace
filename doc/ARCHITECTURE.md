# Architecture: Vibe-Palace

**Last updated:** 2026-10-02

Vibe-palace is a compiled Go binary that serves as an MCP (Model Context
Protocol) server for AI-assisted development. It provides context injection,
session capture, semantic search, and palace-based knowledge navigation through
its full MCP tool surface (versioned in `internal/mcp/tool_surface.golden.json`,
whose `surface_version` is `MCPSurfaceVersion` in `internal/surface/version.go`)
over stdio JSON-RPC 2.0.

**Design principles:**
- Single binary, zero-CGo, no external services
- Filesystem-native storage (JSONL, markdown, JSON) — all vault data
  human-readable and git-mergeable; the search index is compiled per host from
  vault artifacts and never enters git (ADR-014)
- 5-tier precedence for commands/skills (embedded < vault < project < wing < room)
- LLM-agnostic: works with any editor that speaks MCP

---

## System Components

| Package | Responsibility | Key Types |
|---------|---------------|-----------|
| `internal/cli` | CLI framework: registry, dispatch, help, flags | `Registry`, `Command`, `FlagDef` |
| `internal/storage` | Vault layout, CRUD, config | `Vault`, `Config`, `Drawer`, `SessionMeta` |
| `internal/mcp` | JSON-RPC server, tool registry | `Server`, `Registry`, `Tool` |
| `internal/context` | Precedence-aware template resolution | `Resolver`, `ResourceInfo` |
| `internal/tools` | MCP tool implementations (surface versioned in `internal/mcp/tool_surface.golden.json`) | (see tool table below) |
| `internal/embedder` | ONNX text embedding | `Embedder` interface, `ONNXEmbedder` |
| `internal/search` | Hybrid semantic + structural search | `Engine`, `VectorIndex`, `SearchResult` |
| `internal/capture` | Session ingest, chunking, friction, shared capture pipeline | `Indexer`, `ChunkConfig`, `WriteSession` |
| `internal/hook` | Claude Code hook handler, settings install, claim sentinel | `Run`, `Install`, `WriteClaim` |
| `internal/memory` | Host-agnostic AI memory + one-way SessionEnd harvest of Claude native memory (see ADR-004) | `Harvest`, `Options`, `Result` |
| `internal/palace` | Wing/room/hall classification, graph, audit/tune/discover | `PalaceGraph`, `RoomClassifier`, `AAKResult` |
| `internal/llm` | LLM clients behind a `Completer` (OpenAI-compatible + native Anthropic) for offline analysis and session enrichment | `Client`, `Completer`, `NewCompleter` |
| `internal/enrichment` | LLM session synthesis: truncated transcript extraction → summary/decisions/threads/tag (see ADR-005) | `ExtractPromptInput`, `Enricher`, `LoadSystemPrompt` |
| `internal/project` | Project detection from working dir | `ProjectConfig` |
| `internal/kg` | Entity detection + triple extraction | `DetectedEntity`, `ExtractedTriple` |
| `internal/vplog` | Structured logging (slog to file) | `Init()`, `Close()` |
| `internal/archive` | Transcript archive / copyright-provenance ledger (source adapters `claude-code`, `zed`, `inline`; manifests, signing) + the note↔manifest link (`LinkSessionNote`, `ResolveEntry`) | `Manifest`, `Entry`, `CreateOptions`, `LinkSessionNote` |
| `internal/archive/zed` | Read-only Zed agent-panel thread DB parser → Claude-shape JSONL | `parser`, `messages`, `types` |
| `internal/vaultaudit` | Vault audit (dimension registry `dimensions` in `audit.go`), accepted-debt baseline, staleness nag, and the archive-backfill remediation predicate (ADR-007) | `Run`, `Baseline`, `BackfillCandidates`, `ApplyBackfill` |
| `internal/migrate` | Import VibeVault sessions + agentctx (resume/iterations/workflow/knowledge/tasks/memory + verbatim `migrated/` archive) and MemPalace data into the vault | `ImportVibeVault`, `ImportMemPalace`, `copyAgentctx` |
| `internal/absorb` | Migrate legacy agent-context files (CLAUDE.md, AGENTS.md, .cursorrules) into the vault | `Planner`, `Classifier`, `Writer` |
| `internal/agentfile` | Detect well-known agent instruction files and wire in a managed bootstrap block | `Detect`, `Wire`, `WireAll` |
| `internal/shims` | Emit managed command and skill shims into project host surfaces (`.claude/commands/`, `.claude/skills/`, `.cursor/rules/`, `.grok/`) and user-global host plugin trees (see *Shim system* below) | `Plan`, `Apply`, `PlanSkills`, `PlanGrokCommands`, `InstallGlobalSurfaces` |
| `internal/skills` | Directory-form persona artifacts: SKILL.md frontmatter parser/resolver | `Frontmatter` |
| `internal/commands` | Shared command list/upgrade/reset surface over the Resolver | `List`, `Plan`, `Reset`, `RenderUnified` |
| `internal/reconcile` | Check → Plan → Apply reconcilers for managed config-file tiers | (per-artifact reconcilers) |
| `internal/templates` | Compiled-in template corpus (incl. the agent doctrine, `templates/doctrine.md`) + override-only reconcile, lock and reset-backup helpers | `PreserveBackup`, `BackupName`, `Lock` |
| `internal/worktree` | Git-worktree isolation for plan execution (`vp worktree create\|remove\|list`) | `Create`, `Remove`, `List` |
| `internal/check` | Doctor checks for config, vault, embedder, git, agent drift, resume.md caps, host-rooted paths, template drift and the deleted `vp-surface` merge driver | `Run`, `CheckConfig`, `CheckAgentDrift`, `CheckResumeCaps`, `CheckVaultAbsPaths`, `CheckSurfaceMergeDriver` |
| `internal/slug` | Project-slug validation and normalization | `Slugify`, `Validate` |
| `internal/surface` | MCP tool-surface `.surface` stamps and the vault data-format manifest (`.vibe-palace/vault.toml`); the fail-stop/warn gate primitives (see *Versioning and the surface gate*) | `MCPSurfaceVersion`, `RequiredDataFormat`, `CheckCompatible`, `EnforceFailStop` |
| `internal/vaultfs` | Safe vault-relative file accessors behind the `vp_vault_*` tools and `vp vault read/write/…`: path validation, symlink-resolved containment, CAS | `ResolveSafePath`, `Edit`, `Delete` |
| `internal/vaultlock` | Per-path exclusive advisory locks (sidecars under `.vp-locks/`) for read→modify→write, plus the root-lock token lifecycle commands hold | `Acquire`, `AcquireHeld`, `Held` |
| `internal/atomicfile` | Whole-file temp + rename write primitives every vault writer funnels through; stamps `.surface`, refuses writes into departed projects and under `Audits/departures/` | `Write`, `WriteStream`, `ForDepartureRecord` |
| `internal/departure` | The tracked record that a project slug left a vault (renamed, moved to another vault, or deleted), one file per slug at `Audits/departures/<slug>.json` | `Record`, `Find`, `Resolve`, `List` |
| `internal/departedpath` | The one rule for "has this project departed" (the record wins over the directory) and the write refusal every funnel applies | `RecordExists`, `Refuse`, `RefuseRecord` |
| `internal/taskgraph` | Derives epic grouping, blockers and clearing order from flat task metadata; feeds `vp tasks`, `vp board` and bootstrap's head of queue | `Build`, `BuildFromVault`, `Graph`, `BoardView` |
| `internal/onboard` | The single definition of what onboarding a project means, shared by `vp init` and `vp_init` | `Steps`, `Run`, `Scope` |
| `internal/planscan` | Read-only reporter for orphaned Claude Code plan files | `Scan`, `Report` |
| `internal/summarize` | Host-local background queue and drain plumbing for iteration and session-note summarization jobs (`internal/itersummary`, `internal/notesummary` implement its `Summarizer`) | `Summarizer` |
| `internal/mcphost` | Cross-host registry for registering the MCP server with AI hosts (`vp mcp install/uninstall`) | (per-host registrars) |
| `internal/plugin` | Generate and register vibe-palace as a Claude Code plugin from a local marketplace | (install helpers) |
| `internal/sourceaudit` | Static analysis of this repository's own source for known defect classes (write-only fields, uninvoked functions, ungated vault writers, writes outside the funnel, …) | (rule kinds) |
| `internal/wrapstate` | Wrap-state and anchor machinery behind `/wrap` and `/restart`: next iteration, commits and task deltas since the last anchor, dirty flags | (see `vp_collect_wrap_state`) |

Other packages (`apperr`, `chunk`, `detachlaunch`, `gitenv`, `giterr`, `hostsession`, `jobqueue`, `mdfence`, `scopetoken`, and the test helpers `testinfra`, `testutil`, `memorytestutil` and `integration`) are small; all but `chunk` and `integration` carry a `// Package` comment that describes them. List every package with `find internal -mindepth 1 -maxdepth 1 -type d`.

---

## Where Business Logic Lives (ADR-006)

**For any rule you would write in prose, ask whether the server could simply do it.**
Correctness must not depend on which model read which paragraph of which template —
`vp check` has a full check suite that no template invokes, and `resume.md` carried
"keep this file thin" in three places while growing to 88 KB.

Every rule in this system sits in exactly one of three postures. There is no fourth:
a rule that is none of these is not enforced, it is merely written down.

| Posture | When | Example |
|---|---|---|
| **DERIVE** | the server can see every input, and there is one checkable answer | iteration number; `request_id`; the sync verdict (`storage.RemoteVerdict`); audit churn |
| **DECLARE + ENFORCE** | the answer is authorial INTENT that no parsing recovers; the artifact declares it and the server refuses to guess when it is absent | a task's `Parent:`/`Depends:` lines; every `reason` in the `sourceaudit` baseline |
| **REPORT + DEFER** | the answer is a trade-off or an irreversible act | the vault audit's findings; task retirement |

**Err DOWNWARD.** The postures do not fail symmetrically: a REPORT that should have
been a DERIVE costs a human a minute, while **a DERIVE of something that is not
derivable is a silent wrong guess wearing the face of a measurement.** That is how
`archive_adapter` defaulted to `claude-code` and made the Zed adapter unreachable, and
how a missing audit anchor was read as zero and reported an entire vault's history as
one day's churn. **Absence is not a value.**

Distinct from all three: **DERIVE ≠ VERIFY ≠ TRUST.** The server *computes* it, or the
agent supplies it and the server *checks* it, or nobody checks it — and the third must
be labelled as trust (`approved_by_human` is an attestation, not an authorization).
This is a **correctness boundary, not a security boundary**; "guard" is a banned word.

See `doc/adr/006-derive-dont-ask.md` for the full rationale, the L0–L5 ladder, and
where each open task sits on the line.

---

## Storage Engine (Phase 1)

### Vault Concept

The vault is an out-of-band directory separate from source repositories. It
holds knowledge and workflow state for many projects. A host has a **default
vault**, configured in `~/.config/vibe-palace/config.toml`:

```toml
vault_path = "/home/you/your-vault"
```

Individual projects may be bound to **other** vaults on the same host through
the `[project_vaults]` table of that file (ADR-012), so one host can serve
several vaults, one per project. See *Vault resolution* below for how a command
picks its vault, and *Vault lifecycle and departure records* for how bindings
are written.

### Two Directory Trees

The vault contains two per-project top-level trees with different purposes,
plus a few vault-global directories:

```
{vault}/
├── palace/                         # Authored knowledge, host-local index
│   ├── .local/                     # Vault-wide machine-local state (gitignored)
│   │   ├── models/                 # ONNX model cache
│   │   ├── embed-cache/{project}/  # Embedding vectors, one .vec per chunk
│   │   ├── index/{project}/        # Compiled search index (see Host-Local Index)
│   │   ├── index/.generation/{project}  # Store change counter (v10.2.0, ADR-014)
│   │   ├── locks/                  # Index run and commit locks, per host per vault (v10.2.0)
│   │   └── vp.log                  # Structured log (see Structured Logging)
│   └── {project}/
│       ├── .surface                # MCP surface stamp (see Versioning)
│       ├── kg/
│       │   ├── entities.jsonl      # Authored entity lines
│       │   └── triples/{subj}--{pred}--{obj}.json  # Authored triples
│       ├── iteration-summaries/{n}.json  # Per-iteration LLM summaries
│       └── .local/                 # Machine-local: imported-sessions.jsonl only
├── Projects/                       # Workflow (sessions, tasks)
│   └── {project}/
│       ├── .surface
│       ├── resume.md               # Project state
│       ├── workflow.md             # Thin per-project workflow
│       ├── iterations.md           # Append-only archive
│       ├── knowledge.md
│       ├── commit.msg, commit-log.md, commit-log.anchor
│       ├── sessions/<date>-<fp8>-<NN>.md   # see Session Identity
│       ├── transcripts/*.{manifest.json,jsonl.zst}
│       ├── memory/                 # AI memory files (ADR-004)
│       ├── tasks/
│       │   ├── {slug}.md           # Open tasks (any non-terminal status, incl. icebox)
│       │   ├── done/{slug}.md
│       │   └── cancelled/{slug}.md
│       ├── commands/               # Project/wing/room commands
│       │   ├── {name}.md          # Project scope
│       │   └── {wing}/
│       │       ├── .wing/{name}.md   # Wing scope
│       │       └── {room}/{name}.md  # Room scope
│       └── skills/                 # Same layout as commands/
├── Templates/                      # Operator overrides of built-ins only (ADR-008)
│   ├── .surface
│   ├── commands/
│   └── skills/
├── Knowledge/                      # Cross-project notes (e.g. learnings/)
├── Audits/                         # Vault-global, walked by no project enumerator
│   ├── .surface
│   ├── baseline.json               # Vault-audit accepted-debt baseline
│   ├── <date>-vault-audit.md       # Dated vault-audit reports
│   └── departures/{project}.json   # Departure records (see Vault lifecycle)
├── .vibe-palace/
│   ├── vault.toml                  # Data-format manifest (format = N) and the ADR-014 migration marker
│   └── remotes.toml                # The vault's own remotes (written by vp vault init)
└── .vp-locks/                      # Host-local lock sidecars (gitignored)
```

`remotes.toml` is written by `vp vault init`; a vault created before it has
none, and every reader treats that as "records nothing". Each path above has
one definition in code: `internal/storage/paths.go` for most per-project files
(`internal/archive` for `transcripts/`, `cmd/vp/bootstrap.go` for `vp.log`),
`internal/departure` (`Dir`) for `Audits/departures/`,
`internal/surface/format.go` for `vault.toml`, and the `remotesFile` constant in
`internal/storage/lifecycle_publish.go` (written by `vault_init.go`) for
`remotes.toml`.

**palace/** holds the tracked knowledge a search index is compiled from, and
the host-local index itself. Tracked under `palace/<p>/`: `.surface`,
`iteration-summaries/`, authored knowledge-graph facts (`vp_kg_add`,
`vp_kg_invalidate`) (ADR-014 decisions 1, 5). On a migrated vault no derived
drawer and no extracted triple or entity line is tracked, whether or not its
source has a transcript archive: the migration drops them all from the tracked
tree, after
tagging its parent commit `pre-authored-only-<date>` and pushing the tag, so
every dropped byte stays recoverable with `git show` — the loss is from search,
not from history (ADR-014 decision 4). A session with no archive stays
searchable through its note, not its transcript. Transcript and decision
chunks, extracted triples and entities, and the ingest ledger are derived: each
host compiles them into `palace/.local/index/<p>/` from the tracked notes,
iterations and transcript archives, and git never carries them (ADR-014
decisions 2, 7; see *Host-Local Index*). A vault that does not yet carry the
migration marker, or whose migration was reverted, still tracks the drawers and
extracted records written before the migration; search reads them through the
glide path, together with the host-local chunks, deduplicated by a wide content
hash (ADR-014 decisions 7, 11; see *Index Construction*).

**What counts as a palace store.** A `palace/<slug>/` directory is a store only
if it holds at least one regular file outside its top-level `.local/`. Every
project enumerator applies that one predicate (`storage.listPalaceStores`,
behind `Vault.ListAllProjects`, the only project enumerator), so
`vp_list_projects`, the vault audit and cross-project search agree. The reason is git: `.local/` is
gitignored and git cannot carry an empty directory, so a directory holding only
those is a fact about one host. A pull that deletes a project removes its tracked
files and leaves the ignored `.local/` — and the directory around it — on every
host that had cached anything for it; counting that husk as a store made the
enumeration and every audit built on it host-dependent. A directory the predicate
cannot read counts as a store, never as absent. The complement is reported, not
hidden: `Vault.PalaceNonStores` returns it and the `palace-local-only` check row
names each directory, what its `.local/` holds, whether `Projects/<slug>/` exists
and — from one read-only `git ls-files` — how many of its `.local/` files git
tracks; it prescribes nothing. `.local/` is ignored only where the vault's
gitignore says so: the canonical patterns cover `palace/.local/` but not
`palace/*/.local/`, so a vault can have committed such a file, and then it is
synced. The row claims "not tracked" only when git was asked and agreed.

**Projects/** stores workflow artifacts — session markdown files and task
plans. This is the collaboration layer between human and AI. It holds no
configuration: per-project config is host-local (see Configuration below),
and the retired `Projects/<slug>/config.toml` is read by nothing.

### Storage Formats

- **Drawers** (content chunks): JSONL (one JSON object per line), deduped by ID.
  Never tracked on a migrated vault: every chunk lives in the host-local
  `chunks.jsonl` (ADR-014 decisions 2, 4). The importers (`vp migrate
  vibevault`, `vp migrate mempalace`) write transcript archives or index into
  the host-local store, never tracked derived files. A mempalace import indexes
  only locally, so it is single-host and nothing can regenerate it: a
  `chunks.fingerprint` mismatch marks the project `stale`, the next
  `vp index rebuild` discards the import with the project's chunks, and the
  remedy is to re-run the import after that rebuild (ADR-014 decision 4)
- **Sessions**: Markdown with YAML frontmatter (date, tag, friction, decisions)
- **KG Triples**: Individual JSON files keyed by `{subj}--{pred}--{obj}`. On a
  migrated vault, tracked under `palace/<p>/kg/triples/` only when authored;
  extracted triples live in the host-local `kg/`. New records carry `origin`: `authored` or
  `extracted` (ADR-014 decision 5)
- **KG Entities**: JSONL (append-only, name + type + properties); same split as triples
- **Config**: TOML at all three precedence levels

### Session Identity (host-qualified IDs)

Session notes are named and keyed by a **host-qualified** id of the form
`<date>-<fp8>-<NN>` (for example `2026-06-23-a1b2c3d4-01`), where `<fp8>` is the
8-hex-char `surface.WriterFingerprint(vaultPath)` — a `sha256(hostname +
vaultPath)` prefix. The fingerprint stamps both the filename
(`<date>-<fp8>-<NN>.md`) and `meta.ID`; `meta.Iteration` remains the bare
numeric `NN` (the last hyphen segment), so iteration parsing is unchanged.

The fingerprint exists to keep two machines from colliding while offline. The
previous scheme globbed `<date>-*.md` host-agnostically, so two hosts capturing
on the same date both minted `NN=01` — producing identical filenames **and**
identical `meta.ID` values that collided as an add/add conflict the moment the
vaults synced. Scoping the `NextIteration` glob to `<date>-<fp8>-*.md` makes
each host number its own sessions independently, and the raw hostname is never
written into the vault (only its hash prefix).

The fingerprint is threaded through the session read/write/rewrite paths,
`NextIteration`, the capture pipeline, and the enrichment queue (whose item
filenames are likewise host-scoped). Resolution stays **back-compatible**: legacy
`<date>-<NN>.md` files and ids still parse and read (the fingerprint segment is
simply empty), and no existing notes are migrated.

One consequence: per-host iteration numbers are no longer globally monotonic, so
the analytics sort comparators tiebreak by `(Date, Fingerprint, Iteration)`,
with the fingerprint recovered from `meta.ID` via `capture.ParseFingerprint`
rather than from any new persisted field.

### Vault Write Serialization

Vault writes pass through two distinct disciplines that solve two distinct
problems. `internal/atomicfile.Write` (temp-file + `os.Rename`) gives
**crash-atomicity** — a reader never sees a torn file. On Windows that rename is
retried on the transient sharing failures a scanner or indexer causes (see *The
Windows rename retry* below). `internal/vaultlock`
gives **mutual exclusion** — concurrent writers cannot lose each other's
updates. Atomicity alone is not enough: two writers can each read the same
base, compute a whole-file body, and rename over the target, with the second
rename silently discarding the first update. That race is real both
cross-process (CLI vs the `vp mcp serve` MCP server) and in-process (concurrent
`vp mcp serve` goroutines).

`vaultlock.Acquire(vaultRoot, targetAbsPath)` takes an exclusive advisory
`flock` and returns a `release` func. The flock is held on a **sidecar** file
at `<vaultRoot>/.vp-locks/<sha256(canonicalKey)>.lock`, not on the target —
because the whole-file writer renames a temp over the target, which would swap
the inode out from under a target-held lock. Callers acquire the lock around
their entire read→modify→write (the lost-update window opens at the read), and
every writer of a given path — append, whole-file, and read-modify-write —
shares one lock object so they actually interlock. The **canonical-key
contract** is that `canonicalKey` mirrors `vaultfs.ResolveSafePath`
(EvalSymlinks → parent-fallback → lexical-clean), so a symlink-resolved path
from `vaultfs` and a lexical `filepath.Join` from `storage` hash to the same
lock file.

The interlock claim above holds only while every writer of a path takes the
lock — an advisory lock excludes nobody else. So **vault I/O stays in
`internal/storage` / `internal/vaultfs`, where nearly every `Acquire` site
lives**: a higher layer must never read-and-write a vault file itself, and the
few packages that own a file class outright (`internal/absorb`, and
`internal/archive` for transcript manifests — see *Archive manifest locking*
below) take the same per-path lock rather than an exception. The surgical
`resume.md` editors (`vp_thread_*`, `vp_carried_*`) once did exactly that and
silently lost updates; they were routed through a locked combinator
(`storage.EditResume`) and then **deleted outright**, along with that combinator
and `internal/mdutil` — see *Resume write paths* below. Inside a held lock, write
with raw `atomicfile.Write`, never `lockedWrite` — re-acquiring the same path is a
blocking `LOCK_EX` with no timeout, i.e. a permanent self-deadlock.

### Resume write paths

`resume.md` has exactly **two** writers, and both take the lock **and** a
compare-and-set guard:

- `storage.WriteResume` — whole-file regeneration and migrations, behind
  `vp_update_resume`. `expected_sha256` is **required**; the empty string means
  *assert-absent* (first write), never *skip the check*. The compare happens
  **inside** the lock.
- `vaultfs.Edit` — the surgical, one-section-at-a-time path behind
  `vp_vault_edit`. This is the routine wrap path: every ordinary resume update
  goes through it.

`vaultlock.canonicalKey` normalizes both path spellings to a single key, so the
two writers genuinely interlock rather than merely appearing to.

**The CAS digest is over the RAW, pre-expansion bytes.** The resolver runs
`expandScoped()` over everything it returns (`{{PROJECT}}`, `{{DATE}}`), so
`vp_get_resume` / `vp_bootstrap_context` serve *expanded* text while their
`sha256` is computed over the *raw* bytes. Compose an edit from the expanded body
and one of two things happens: an `old_string` spanning a placeholder fails to
match disk (loud, harmless), or a whole-file body passes CAS and **silently bakes
the expanded values onto disk**, destroying the live tokens. Source any text you
intend to write back from `vp_vault_read`, never from the context tools. CAS is
therefore the *primary* discipline here, not a backstop. See ADR-003.

`.vp-locks/` is host-local: registered in `storage.CanonicalGitignorePatterns`
(never synced), refused by `vaultfs.IsRefusedWritePath`, and not indexed. The
lock is **real on both platforms**: unix uses `syscall.Flock`, Windows uses a
`LockFileEx` byte-range lock over byte 0 of the sidecar (`flock_unix.go` /
`flock_windows.go`). Both auto-release when the handle closes or the process
exits, so a leftover `.lock` marker after a crash is harmless. See ADR-003
(`doc/adr/003-vault-write-locking.md`) for the full rationale — including the
2026-08-18 amendment that retired the "Windows is a no-op stub" scope.

### The Windows rename retry

`atomicfile.Write` finishes with a rename of the temp file over the target. On
unix that is POSIX `rename(2)`, which succeeds even while another process holds
the destination open — the directory entry is replaced and the old inode lives
until the last descriptor closes. There is nothing to retry.

On Windows the same call is `MoveFileEx(..., MOVEFILE_REPLACE_EXISTING)`, and it
**fails** when any other process holds a handle to the destination that was not
opened with `FILE_SHARE_DELETE`. On CI runners and developer desktops that is
routinely a virus scanner or the search indexer holding the file for a few
milliseconds; Windows reports `ERROR_ACCESS_DENIED` (5) or
`ERROR_SHARING_VIOLATION` (32), and the identical rename succeeds a moment
later. This is what made the `windows-lock` CI job flake: one child of sixteen
died in the rename, losing its update, while the lock had serialized every child
correctly. **It was never a lock miss** — see ADR-003's 2026-08-18 amendment.

`renameWithRetry` (`internal/atomicfile/rename.go`) therefore retries those two
errno values with backoff — 7 attempts, 785ms of worst-case sleep, bounded under
a second because a vault write is on an interactive path and an unrecoverable
rename must surface as an error rather than a multi-second hang. Any other
failure returns immediately and unwrapped. The classifier is build-tagged
(`rename_windows.go` / `rename_other.go`) and returns false unconditionally off
Windows, so unix behaviour is byte-for-byte what it was.

The retry loop reaches `os.Rename`, the classifier and `time.Sleep` through
unexported package vars, so the whole policy is exercised on any platform by
substituting fakes — the retry, the give-up bound and the no-retry path are unit
tests that run on the Linux CI box, not claims that only Windows could check.


### Memory write locking

The per-path lock discipline extends to the AI memory files
(`Projects/<slug>/memory/…`). `WriteMemory` and `DeleteMemory` acquire the
per-path lock around their write. The harvest per-file loop
(`internal/memory/harvest.go`) is a read→decide→write sequence, so locking only
the final write would leave the lost-update window open at the read: it instead
holds `LockMemory(rel)` across the whole per-file decision and writes through
`WriteMemoryUnlocked` (`internal/storage/memory.go`) — the same function as
`WriteMemory` minus the acquire, which under a held lock would be a blocking,
timeout-free self-deadlock (see the `lockedWrite` rule above).

### Archive manifest locking, and the hook's posture split

Transcript manifests (`Projects/<slug>/transcripts/*.manifest.json`) have
exactly **two** read-modify-write sites, and both take the per-path lock keyed
on the **manifest path** (`internal/archive/lock.go`):

- `archive.Create` — across `ReadManifest` → the three-field idempotency
  compare → the `<name>.manifest.json.<prev-hash>.bak` rename → `compressFile` →
  `WriteManifest` (and on through `Sign`). Unserialized, two writers racing one
  session with a changed source computed the *same* `.bak` name, so the second
  silently replaced the first's preservation — and a racer that read *after* the
  first's rename found no manifest, took no `.bak` arm at all, and overwrote the
  record preserving nothing.
- `archive.LinkSessionNote` / `TryLinkSessionNote` — across
  `ReadManifest` → mutate → `WriteManifest`. The window that matters is a linker
  reading *before* a concurrent `Create`'s rename and writing its stale body back
  afterwards, rolling that `Create`'s record out from under the archive actually
  on disk.

The lock is **not** inside `WriteManifest`. That primitive is shared by both
paths and sits under `vaultaudit.ApplyBackfill`'s documented *sessions-directory
lock, release, then manifest link — sequential, never nested* sequence; a
non-reentrant, timeout-free `LOCK_EX` inside the shared primitive would convert
that ordering into a live lock-order constraint. The `NoLock` rename sink
(`vaultfs.RenameNoLock`) likewise still acquires nothing — the exclusion lives
in the caller.

**The posture differs by entry point, and this is the load-bearing part.**
`archive.CreateOptions.LockPosture` defaults (zero value) to `LockBlocking`, and
`LinkSessionNote` is the blocking form, because the MCP server, `vp archive
create`, `vp archive link` and `internal/capture` are all fail-stop and can
afford to wait. `internal/hook` is not: `cmdHook` is registered **unwrapped** in
`cmd/vp/commands.go` and `cmd/vp/cmd_hook.go` passes `context.Background()` into
`hook.Run`, so nothing on that path has a timeout. A blocking acquire there
would not degrade a capture — it would hang `SessionEnd` with no error and no
log, which is worse than losing the archive. The hook therefore passes
`LockPosture: archive.LockNonBlocking` and calls `TryLinkSessionNote`, and
routes the resulting `archive.ErrManifestLocked` into the non-fatal warn branches
it already had. A future caller that copies the blocking default onto a
timeout-free path reintroduces that hang.

### Repo-root commit lock

Per-path locks serialize content writers, but the vault has a second shared
mutable resource: the git index. Four independent committers funnel through
`CommitAndPushPaths` (`internal/storage/vaultsync.go`) — the memory-harvest
tail, vault tidy, the `vp_vault_sync` MCP tool, and `vp vault commit` — and two
of them running concurrently race `git add`/`git commit`, with one hard-failing
on `index.lock: File exists` (exit 128). `CommitAndPushPaths` therefore takes
one repo-root advisory lock, `vaultlock.Acquire(vaultPath, vaultPath)`, around
the reconcile + stage + commit critical section. The lock is released **before**
the network push — the index race is the only correctness hazard, and holding
across push would serialize every remote. Keying the lock on the vault root
itself keeps it distinct from the per-path keys the content writers take, so a
committer that already holds a per-path lock cannot self-deadlock (the paths
hash to different sidecar files).

### Vault resolution: 3 tiers (ADR-012)

Which vault a command reads and writes is resolved separately from the settings
below, by `storage.ResolveVaultBinding` (`ResolveVaultPath` wraps it):

1. **Checkout override** — the nearest `.vibe-palace.toml` at or above the
   working directory (the walk stops at `$HOME`), if it sets a top-level
   `vault_path`. Source `cwd:<file>`. For untracked trees only: a committed
   `.vibe-palace.toml` is identity only and never carries `vault_path`
   (`vp check` reports one that does).
2. **Host binding** — the global config's `[project_vaults].<slug>`, keyed on
   the `[project].name` of that same file. Source `binding:<config>#<slug>`.
3. **Host default** — the global config's `vault_path`. Source `global:<config>`.

Resolution fails closed, with one error type (`storage.ErrVaultBindingRejected`)
that `vp hook` treats as "capture nothing", never as "fall back to the global
vault". It refuses when:
- a found `.vibe-palace.toml` cannot be read or parsed;
- tiers 1 and 2 name different vaults;
- a binding's target is not an absolute path to an existing vault;
- the table is malformed;
- a checkout names no project while its git-origin slug is bound, or while git
  cannot be asked on a host that binds anything.

An unreadable global config fails with `ErrHostConfigUnreadable` (a broken host
config, which `vp check` reports as a row). The marker is found and read by
`project.LocateMarker`, the one walk and reader project detection also uses.

The host menu shims and the vault git family (`vp vault pull/push/…`, which
take `--vault`) resolve tier 3 only. `vp status` and `vp check` print the
resolved path and its source.

### Configuration: 3-Tier TOML Precedence

Configuration follows a 3-tier override chain:

1. **Embedded defaults** — compiled into the binary via `//go:embed config/defaults.toml`
2. **Host-level** — `~/.config/vibe-palace/config.toml`
3. **Project-level, host-local** — `~/.config/vibe-palace/projects/{project}.toml`
   — **where per-project settings belong.** Per-project config is machine-local
   and does not belong in a vault shared across machines. Only the
   `palace.scoring` subtree is honoured at this tier; it is written by
   `vp tune rooms --apply` and `vp discover rooms --apply`.

Until v7.2.0 there was a vault tier between 2 and 3,
`{vault}/Projects/{project}/config.toml`, which could override any section. It
is retired: nothing reads or writes it, `vaultfs` refuses to create it, and
`vp check --check vault-project-config` lists any that survive. Every section
except `palace.scoring` is now host-level only.

Each level overlays the previous. `vp status` reports which per-project file a
project actually reads. Key sections:

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

[palace.rooms.custom]          # host-level (Tier 1, unweighted)
keywords = ["keyword1", "keyword2"]

[palace.scoring]               # weighted scoring overrides (Phase 12)
min_score = 0.5                # an override; the built-in default is 0.6
[palace.scoring.rooms.testing]
high = ["integration test", "e2e test"]
medium = ["spec"]
low = ["check"]

[palace.llm]                   # offline LLM for tune/discover (Phase 12)
endpoint = "https://api.x.ai/v1"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
max_tokens = 4096
```

Key type: `storage.Config` — a flat struct populated by decoding TOML at each
level in sequence. `GetConfigValue(project, key)` returns value + source level.

### Absent means "inherit" — a key present at its zero value does not

Each tier's on-disk file only overrides the keys it actually contains: `LoadConfig`
decodes tier N into the same `tomlConfig` used for tier N-1, so a key **absent**
from tier N's TOML source leaves the prior tier's decoded value untouched, while a
key **present** — even at the type's zero value (`git_enabled = false`,
`http_port = 0`, an empty `[palace.llm]` block) — overwrites it. This is why
the shipped config templates keep their optional overrides commented out: an
uncommented `# vault_path = ""` would pin an empty vault path rather than
leaving it to inherit.

Any writer that reads a config file, needs to change only one section of it,
and re-encodes the result must preserve this distinction — a
full decode into `tomlConfig` (a plain, non-pointer struct) followed by a
full re-encode turns every field the file never mentioned into an explicit,
present zero value, silently breaking inheritance for that project from then
on. The scoring writer, `writeScoringConfigAt` behind `WriteHostScoringConfig`
(`internal/storage/config.go`), is the one writer of
this shape; it sidesteps the problem structurally by decoding into
`map[string]any` instead of `tomlConfig` — a Go map decoded from TOML only
ever contains the keys the source text actually had, so a key this function
never touches (anything outside `[palace.scoring]`) cannot become present at
a zero value no matter how large the schema grows. A new writer that follows
the decode-mutate-reencode pattern against the full `tomlConfig` struct
should use the same map-based approach rather than reintroducing this bug.

---

## Vault Housekeeping (tidy)

### The problem: capture churn nobody commits

The hook path (`vp hook` on SessionEnd, Stop and PreCompact) and the MCP capture tools write
session summaries and transcript archives with their manifests across **every**
project. The hook passes a nil indexer to `capture.WriteSession`
(`internal/hook/hook.go:581`) and marks the note `needs_indexing`. Its last
step spawns the pending-archive ingester as a detached background process and
returns; the hook itself never embeds, and the ingester writes only the
host-local index, never the tracked tree. `vp_capture_session` indexes no
transcript text: its `transcript` parameter only creates an archive on a host
without a hook, after which capture triggers the same ingester. The tracked KG
changes only through authored facts (ADR-014 decisions 1, 7, 10; see
*Host-Local Index*). (Historically the hook
also bumped a `.surface` provenance stamp on every write; stamps are now
byte-stable per surface version and no longer churn per session — see "The
`.surface` status gate" below.)
Nothing in the routine workflow ever commits this churn: `/wrap` only
commits the *current* project's explicit narrative paths (resume, iterations,
the active task, `commit.msg`). The machine-generated artifacts pile up
uncommitted, and because `vp vault push` refuses to push a dirty tree, the
backlog eventually blocks multi-machine sync. The historical fix was to run raw
git in the vault by hand — eyeball `git status`, build a comma-separated
`--paths` list, and `vp vault commit --push`. The goal of tidy is that a normal
end user **never runs raw git in the vault**.

### Principle: classify, don't `git add -A`

Tidy is *not* "commit everything dirty." It commits a precisely-classified set
of host-generated capture artifacts and **reports** everything else, preserving
the deliberate never-`git add -A` invariant shared with `vp vault commit`. Each
dirty path is routed into exactly one of two buckets:

- **Swept** — machine-generated capture output that is safe to commit
  unattended. Staged and committed with a hostname-stamped message.
- **Reported** — everything else. Never staged, never committed; surfaced to the
  human so a stray edit or a project directory nobody initialised gets one
  round of eyes. (`vp init` commits its own project scaffold; see the
  `.surface` rule below.)

This split is what lets the feature run automatically without ever committing on
the user's behalf for anything it does not positively recognize.

### The data-driven classifier (`sweepRules`)

The heart of tidy is `sweepRules` in `internal/storage/vaulttidy.go:69` — a table
that is the single source of truth for what gets committed:

| Category | Shape (vault-relative) |
|----------|------------------------|
| Session summaries | `Projects/*/sessions/*.md` |
| Transcript archives | `Projects/*/transcripts/*.{manifest.json,jsonl.zst}` |
| Knowledge-graph entities | `palace/*/kg/entities.jsonl` (marker-gated: on a migrated vault, only when every line added since HEAD is `origin: authored`) |
| Knowledge-graph triples | `palace/*/kg/triples/**/*.json` (deep; marker-gated: on a migrated vault, authored records only) |
| Drawers | marker-gated: on a migrated vault, none; without the marker, `palace/*/drawers/**/*.jsonl` |
| Audit reports | `Audits/*.md` (flat only) |
| Audit baseline | `Audits/baseline.json` |
| Surface stamps | `{Projects/*,palace/*,Templates,Audits}/.surface` (status-gated) |

**On a migrated vault** tidy sweeps no drawers. It stages a triple file only
when that file is `origin: authored`, and reports a tracked extracted, or
unstamped, triple as unexpected dirt; extracted records belong to the
host-local index. `kg/entities.jsonl` is one file, so tidy diffs it against
HEAD: it stages the file only when every added line carries `origin: authored`,
and otherwise reports the whole file; a line with no `origin` counts as not
authored (ADR-014 decision 11). These rules are keyed on the migration marker,
never on the binary's version.

**The migration marker** is an explicit key in the tracked
`.vibe-palace/vault.toml`, `authored_only = "<date>"`, read through a
`VaultManifest` field. Every writer of `vault.toml` round-trips all of its
fields: `WriteFormat` is read-modify-write, so a later format migration keeps
the marker (at `a32a2d4` it re-encodes `VaultManifest{Format: n}` alone,
`internal/surface/format.go:160-166`). The marker is **not** inferred from the
ignore lines, because a hand-edited `.gitignore` does not switch on behaviour
(ADR-014 decision 11).

**The derived-path ignore lines** (`palace/*/drawers/` and
`palace/*/ingested-archives.jsonl`) are **not** unconditional canonical lines
(ADR-014 decision 11). The reconciler appends every canonical line a vault
lacks, on two paths, and an ordinary `vp config sync` reaches both: **create**,
when `.gitignore` is absent (`ReconcileVaultGitignore`,
`internal/storage/git.go:141`, `:161`, applied at
`internal/reconcile/vault.go:304`); and **top-up**, on an existing vault
(`planVaultGitignore` → `MissingVaultGitignorePatterns`,
`internal/reconcile/vault.go:230-240`, `internal/storage/git.go:222` →
`TopUpVaultGitignore`, `internal/reconcile/vault.go:353`,
`internal/storage/git.go:197`). Both paths emit the two derived lines only on a
vault whose `vault.toml` carries the marker, so unconditional lines never reach
an unmigrated vault before the marker. `vp config sync` never writes the
marker itself. Three writers put the marker and the lines in place: the
migration commit; the migration's empty-vault path; and `vp vault init` on a
fresh vault, which is born migrated and also writes the vault-level stamp
`Audits/.surface` at `MCPSurfaceVersion` (`internal/surface/version.go:355`),
never a literal 10: the gate takes the maximum stamp, so a literal 10 written
while the constant was still lower would make the vault refuse the binary that
created it. From v10.2.0 the constant is 10. Only `vp vault init` (`InitVault`,
`internal/storage/vault_init.go:108`) creates a vault born migrated, and it
writes the marker in `InitVault` itself, never in the shared scaffold.
`reconcile.ScaffoldNewVault` is that shared scaffold; on main its only caller is
`vp vault init`, which passes it into `storage.InitVault`
(`cmd/vp/cmd_vault_init.go`, `internal/storage/vault_init.go`). The
split-destination caller it had before U12 is gone with the split tool. The
marker never goes in the shared scaffold, so a destination scaffolded straight
through it starts unmigrated — and copy refuses an unmigrated destination (the
two-marker rule, below).
`vp init`, `vp config sync` and onboarding use the vault reconciler directly,
and a vault they create also starts unmigrated, without marker or lines (ADR-014
decision 11). A reverted vault loses the marker with the revert, so the
reconciler never gives the lines back to it. Every marker-gated behaviour keys
on the marker alone; the lines follow from the marker, never the other way
round. Because `.vibe-palace/` can be ignored on an existing vault, the marker
write force-adds `vault.toml`, as `vp migrate kg-filenames` does
(`cmd/vp/cmd_migrate_kg.go:142`, through `GitAddForce`,
`internal/storage/git.go:617`).

**Which v10.2.0 behaviour waits for the marker** (ADR-014 decision 11):

- *Marker-gated* — the tidy rules above (no drawer sweep, authored-only KG
  staging); the pull self-heal below; and two audit changes:
  `palace-store-drawers` is skipped, and the new `kg-tracked-extracted`
  dimension reports tracked extracted records (see *Vault Audit*). Without the
  marker these behave as at `a32a2d4`, and the derived-path ignore lines are
  absent, since only a marked vault receives them. So before the migration a v8
  binary's drawer appends stay what they are at `a32a2d4`: tracked, unignored
  capture artifacts that tidy commits. They become neither non-artifact dirt,
  which `vp vault sync` refuses (`internal/storage/vaultsyncflow.go:82-99`), nor
  tracked files under an ignored path, which would make tidy fail. The same
  fallback keeps a v9 binary correct on a vault whose migration was reverted.
- *Unconditional, from install on* — capture, decision filing, the
  pending-archive ingester and the backfill write nothing derived into tracked
  paths; explicit staging never re-tracks a derived path (below); the presence
  predicate ignores derived-pattern files, so ignored residue never makes a
  project present on one host only; the rewritten `project-tree-coherence`,
  which uses that predicate; and the glide-path rule itself (decision 7; see
  *Index Construction*).

Two further rules keep a derived path out of git once it is ignored (ADR-014
decision 11). **Self-heal on pull** (marker-gated): a binary that finds an
unmerged `UD`, `DU` or `DD` entry on a now-ignored derived path reads the
incoming marker (from `MERGE_HEAD`, or the rebase's onto commit), resolves the
entry with `git rm --cached` and **deletes the file**, concludes the merge or
continues the rebase, drops an autostash only when it holds nothing but derived
paths, and reports each path it deleted. It runs at every pull path that can
leave an unmerged entry: the merge in `pullCore`
(`internal/storage/vaultpull.go:179`, the merge via `mergeFetchedTip` at `:300`); the rebase in
`reconcileIfAhead` (`internal/storage/vaultsync.go:993`), reached from a
commit-and-push (`:306`) and from the mirror prune
(`internal/storage/vaultsync_verify.go:250`), where the heal runs before the
path's abort on conflict and the abort stays for any non-derived conflict; and
the push-rejection reconcile's rebase (`reconcileRejectedPush`,
`vaultsync.go:1836`). The resumed clone's merge is `--ff-only`
(`internal/storage/vault_clone.go:531`), so it never leaves an unmerged entry
and needs no heal. `UU` on `kg/entities.jsonl` — the migrator rewrote the file
and a lagging host appended to it — is reported by name and never resolved;
only the operator attestation (every writer host synced, pushed and left clean)
prevents it. The heal deletes rather than moves the bytes (ADR-014 decision 11,
*Delete, not move*): every host rebuilds its index from archives, and the
`pre-authored-only-<date>` tag holds the pre-migration tree.
**Explicit staging never re-tracks a derived path** (unconditional):
`stageInBatches` (`internal/storage/vaultsync.go:1171`) never names one,
because `git add -- <path>` on a tracked, ignored file stages it (and exits 1).

The real vault layout requires deep (`**`) matching: triples nest arbitrarily under a source-derived
subpath (e.g. `palace/<p>/kg/triples/.claude/plans/<name>--mentioned_in--<uuid>.json`, an extracted
triple that on a migrated vault lives in the host-local index instead; triple paths legitimately
contain `.claude/` segments, so the classifier **never** excludes them). Go's stdlib
`filepath.Match` has no `**`, and `doublestar` is not a dependency. Rather than add one for ~5
stable rules (decision M1, option B), each `SweepRule` carries an explicit segment-matcher func over
`parts = strings.Split(vaultRelPath, "/")`. The `Pattern` string on each rule is documentation only
— the human-readable shape the `Match` func implements, kept for the test table and audit trail.
`matchRule` returns the first rule whose `Match` accepts a path; the rules are mutually exclusive in
practice.

Everything that matches no rule — `resume.md`, task files, `Knowledge/` notes,
hand-edited content — falls through to Reported and is left untouched.

### The `.surface` status gate

The `.surface` rule is the one case where routing depends on the git status code,
not the path alone. `git status --porcelain -z` encodes status as two columns
`XY` (index, worktree). `classifyDirty` applies the gate only to the
`.surface` rule:

- **Tracked modification** (` M` worktree-modified — a surface-version bump or the
  one-time stamp normalization, plus `M ` / `MM`) → **swept**. Routine per-session
  `.surface` churn was eliminated when stamps became byte-stable per surface
  version (`WriteStamp` is a no-op at the current version and no longer persists
  provenance fields), so this case now fires only on a version bump.
- **Untracked** (`??`) `.surface` → **reported**.

An untracked `.surface` means a project directory git has never seen, written
by something that did not commit it: `vp_memory_write` or `vp_vault_write`/`move`
into a slug nobody initialised, or hook capture into a fresh slug. That is a
stray, and it gets human eyes rather than a commit. `vp init` / `vp_init` is
different: running it is the deliberate act that adopts a project, so it commits
its own scaffold (`storage.CommitProjectScaffold`: the stub READMEs and the
project's `.surface`, path-scoped, local). An accidental `vp init` therefore
leaves a COMMITTED scaffold-only project. Tidy no longer sees it, but the
`stray-scaffolds` check, which reads the directory rather than git, still
reports it. `vp config sync` commits the marker stubs only for a project whose
`.surface` is already tracked, and never the `.surface` itself, so it cannot
adopt a stray. Pure path-globbing would commit `Projects/p/.surface` while
reporting its siblings (the `commands/`/`skills/` README stubs), a split-brain
commit. After a project's first commit its stamps read ` M` and sweep
automatically. All other rules sweep
regardless of status, including `??` for newly created
sessions/transcripts/drawers/triples and `D ` deletes (git stages deletions).

### Porcelain parsing and rename/copy handling

`scanPorcelain` runs `git status --porcelain -z -uall` (the `-uall` surfaces
files inside untracked directories, not just the directory) with the same
prompt-suppressing env as the other git helpers. `parsePorcelainZ` splits the
NUL-delimited output into `PorcelainEntry{Status, Path}`. A rename/copy record
(`R` or `C` in either status column) is followed by a **second** NUL-separated
field holding the old path; that extra field must be consumed or every
subsequent record misaligns. Rename/copy entries report the new path and are
routed to Reported unconditionally — capture artifacts are append-only and
timestamped, so a rename always signals human activity that needs eyes.

### Push policy and remote downgrade

`TidyVault` delegates the actual commit to `CommitAndPushPaths`, inheriting its
batched staging, hostname stamp, and offline tolerance (the commit lands locally
first; per-remote push failures are recorded in `RemoteResults` and never become
a returned error). When push is requested, `TidyVault` first probes for
configured remotes; if there are none it downgrades to a local-only commit and
sets `PushDowngraded` (a remote-less vault is not the same as being offline). An
empty swept set is a no-op — `CommitAndPushPaths` errors on zero paths, so it is
never called, and the result carries `Committed=false` with Reported populated.

Before staging, `CommitAndPushPaths` filters the supplied paths, dropping any that
match nothing in **both** the worktree and the index. The filter is deletion-safe:
a tracked-but-deleted path matches the index, so it is kept and its removal is
staged; only a path absent from both is dropped. Dropped paths are reported in
`PushResult.SkippedPaths`. A non-empty input that filters down to nothing is a
benign no-op (returns an empty `CommitSHA` with the skips reported), distinct from
the zero-**input** case, which still errors. This is what lets `/wrap` list a
never-written `Projects/<slug>/memory/` dir unconditionally without one absent path
making `git add` exit 128 and aborting the whole commit.

### Three layers

| Layer | Entry point | Role |
|-------|-------------|------|
| Core | `storage.TidyVault(vaultPath, push)` / `storage.TidyScan(vaultPath)` | Scan → parse → classify → (commit). `TidyScan` is the read-only classification path (never commits, never probes remotes) that backs `--dry-run` and is shared with `TidyVault` so there is one classification code path. |
| CLI | `vp vault tidy [--dry-run] [--no-push] [--vault PATH]` | The human / cron-able escape hatch. `--dry-run` prints the swept/reported split without committing; `--no-push` commits locally only; bare invocation commits and pushes. `--vault PATH` acts on a named vault (it must be the top level of its own git repository) instead of the configured one, so a throwaway copy can be tidied without redirecting config; the same flag is on `vault pull/push/sync/commit/status`. |
| MCP | `vp_vault_tidy` (mutating; params `dry_run`, `push`) | What the workflow templates call. Returns the `TidyResult` (swept, reported, commit info, per-remote results) as structured content plus a concise human summary. |

### Workflow wiring

Tidy is invoked through the MCP tool by two commands so end users never touch
git directly:

- **`/restart`** sweeps right after the `vp_vault_sync` pull and the
  `vp_surface_check` surface preflight (both MCP calls — the restart template is
  now Bash-free for vault-sync + surface-preflight, so it works on hosts without
  Bash), so residue left by the previous session's hooks, a crash, or another
  machine is healed *before* context loads — and tidy runs against the
  already-merged state.
- **`/wrap`** sweeps after the narrative sync, committing any session/transcript
  artifacts produced during the session (and a `.surface` stamp only if a
  surface-version bump occurred — steady-version writes no longer touch it).

---

## Vault Pull

### The primitive

`storage.Pull(vaultPath, remotes)` in `internal/storage/vaultpull.go` centralizes
the incoming half of vault sync, the way `CommitAndPushPaths` centralizes the
outgoing half. It existed previously only as duplicated inline `git pull <remote>
<branch>` logic in two front-ends — `pullAll` in `cmd/vp/cmd_vault.go` and
`gitPull` in `internal/tools/system_tools.go` — neither of which stashed,
autostashed, or pre-checked the working tree. `Pull` returns a `PullResult` that
mirrors `PushResult` but drops `CommitSHA`, adds `HealedTemplates []string` and
`RemoteOutput map[string]string`, and exposes `AllPulled()` / `AnyPulled()` /
`Stranded()` alongside the per-remote `RemoteResults`.

`Pull` keeps **plain merge semantics** and merges through the same function as
the push path's reconcile (`mergeFetchedTip`), without its fast-forward converge
loop: incoming history is merged, never replayed. It attempts every remote and
records each outcome in `RemoteResults`, leaving each front-end its own policy,
and stops the sweep only on the failures `pullSweepStops` names.

**A conflicted pull is aborted** (`vault-pull-leaves-a-conflicted-merge-in-the-shared-tree`,
2026-10-04). A pull used to leave `MERGE_HEAD` and conflict markers in the shared
vault, and every typed writer then failed with `cannot do a partial commit during
a merge` until a human resolved it (the iteration-413 jam). The rule is now the
reconcile's: vp aborts only a conflicted merge it started, and never touches one
it did not start.

- `pullCore` refuses outright, before the template heal, when someone else's git
  operation is unfinished (`refuseOperationInProgress`): a merge, cherry-pick,
  revert or rebase with its marker file; a multi-commit cherry-pick or revert
  stopped between commits (`.git/sequencer`); or conflicted index entries with no
  marker file at all, which `cherry-pick -n`, `stash pop`, `checkout -m` and
  `apply -3` leave. The template heal would otherwise discard a human's staged
  resolution, and the derived heal would read their conflict as the merge's own
  and commit it. `mergeFetchedTip` refuses the same states again under the
  commit lock.
- A conflict the derived-path heal does not take is aborted. The error
  (`*mergeConflictError`) names the paths, listed before the abort, the ref, the
  incoming tip, and the remedy: merge by hand, resolve, commit, then `vp vault
  sync`. Every abort logs a warning.
- A killed merge is never aborted and removes nothing; a failed abort is
  `*vaultTreeUnsafeError`. Both stop the sweep, as do a conflict and a departure.
  A merge refused before it started, and a failed derived untrack, go on to the
  next remote.

What an abort does **not** restore: template dirt the phantom heal discarded
before the merge (only ever bytes equal to the remote's), and a writer's edit to
a conflicted file landing between the merge and the abort (typed writers lock per
file, not the root key the merge holds). Both are recorded on
`vault-git-stranded-state-is-reported-nowhere`. The CLI `pullAll` is best-effort / continue-all, adds a CLI-only
`--dry-run`, and re-prints each remote's captured output to stderr; the MCP
`gitPull` is fail-fast (returns on the first failing remote) and folds the
captured output into its response payload.

### Phantom-template self-heal

The core fix `Pull` adds is a narrowly-scoped self-heal for a wedged working
tree. Before the merge, for each working-tree-dirty path matching exactly
`Templates/commands/*.md`, `Pull` runs `git diff --quiet <remote>/<branch> --
<path>`. If that exits 0 — the dirty working-tree content is provably identical
to the freshly-fetched remote ref — it runs `git checkout HEAD -- <path>` to
discard the uncommitted dirt, so the subsequent merge cannot abort with "Your
local changes to the following files would be overwritten by merge", and records
the path in `HealedTemplates`. The heal is **fail-open**: any error while
healing a path skips that path and is never fatal, and a genuinely-edited
template (nonzero diff) is left untouched.

This neutralizes the triggering incident, where an older `vp commands upgrade`
wrote stale template bytes over a newer committed copy, leaving the host's tree
dirty in a way that blocked every pull. The dirty-path scan reuses tidy's
`scanPorcelain` / `parsePorcelainZ` parser (see *Porcelain parsing and
rename/copy handling* above) — no new porcelain parser was added.

The heal clears a **dirty-tree obstruction only**; it is not a committed-conflict
resolver. A template that has genuinely diverged at the commit level on two hosts
still produces a merge conflict, which the pull aborts and names.

### The pending-archive ingest trigger

**Shipped in v10.2.0 (ADR-014 decision 7); the ingester lives in `internal/ingest/`.**

A pull is one of the four triggers of the pending-archive ingester. The others are the hook's
last step, hook-less capture and `vp mcp` startup; see *Host-Local Index*.

- **When it fires.** After any pull that brings in new transcript archives, the puller starts the
  ingester for the vault it pulled, passing the vault root and the one project slug it resolved; a
  pull names no archive. The run processes that triggering project first, then every other project
  of that vault with pending archives, within the per-run budget.
- **Which code paths count as a pull.** Every path that merges or rebases remote commits into the
  vault:
  - `storage.Pull` (`internal/storage/vaultpull.go:152`), behind both `pullAll` and `gitPull`;
  - the rebase inside a commit-and-push (`internal/storage/vaultsync.go:993`, in
    `reconcileIfAhead`), and its second caller, the mirror prune
    (`internal/storage/vaultsync_verify.go:250`);
  - the push-rejection reconcile (`internal/storage/vaultsync.go:1836`);
  - the fast-forward merge in a resumed `vp vault clone` (`internal/storage/vault_clone.go:531`).
- **The pull never ingests in-line.** It starts the ingester as a detached process through
  `internal/detachlaunch` and returns at once:
  - setsid on POSIX (`internal/detachlaunch/launch_unix.go:20`);
  - stdout and stderr go to a log file, never the puller's pipes (`launch.go:96-97`);
  - on Windows, a new process group with job breakaway (`launch_windows.go:38`). Breakaway is
    dropped on a fallback retry when the job forbids it (`launch.go:72-77`), and then the child
    can die with the job.

  A pull, and the `vp vault sync` or MCP call that contains it, is never delayed by embedding.
- **When the ingester is already running.** If another ingest or rebuild holds the index run lock,
  the spawned ingester exits at once. The holder's rescan before release, or its recheck after
  release, picks up the pulled archives.
- **What a pull-triggered run ingests.** Every pending archive that is not in the host's baseline
  set, newest first, within the per-run budget. The baseline set, the historical backlog, waits
  for an explicit `vp index rebuild`.

---

## Vault Sync

`storage.SyncVault(vaultPath, remotes)` in `internal/storage/vaultsyncflow.go`
orchestrates the default `vp vault sync` (and the `vp_vault_sync` `sync` action
with no `paths`): it **tidies capture artifacts before pushing** so a bare sync
no longer refuses on the machine-generated churn a manual `vp vault tidy` used to
have to clear first. The order is correctness-critical:

1. **Classify** the whole working tree via `TidyScan` (read-only; the same
   `sweepRules` classifier tidy uses).
2. **Refuse before any network I/O** if there is genuine dirt — `GenuineDirt` is
   `Reported \ ReportedUserContent`, the reported paths that are *not*
   deliberately-pending user memory. Pending memory (`Projects/<slug>/memory/…`)
   is expected content committed later by wrap/SessionEnd and never blocks a sync.
3. **Commit the swept artifacts locally** (`TidyVault(vaultPath, false)` —
   `push=false`, no network), then **pull** each remote, then **push**.
4. An **in-flight transcript** — a `.jsonl.zst` present on disk whose sibling
   `.manifest.json` has not been written yet — is **deferred** by the classifier
   (`TidyScan.Deferred`): left untracked for the next sweep, never committed
   half-complete and never counted as blocking dirt.
5. The pull and push gates read `RemoteVerdict` over `RemoteResults`, not the Go
   error (`Pull`/`PushPlain` return `err == nil` and record outcomes per remote),
   and a post-merge re-assert re-scans for genuine dirt plus unmerged index
   entries before pushing over a conflicted tree.

`--no-tidy` (CLI) / `no_tidy:true` (MCP) bypasses `SyncVault` entirely and
restores the old raw pull+push, which refuses on **any** uncommitted change.
`--dry-run` classifies and previews (via `TidyScan` + dry-run pull/push) without
committing anything.

---

## Vault lifecycle and departure records

Usage and the operator procedure live in `doc/VAULT-LIFECYCLE.md`; the decisions
are recorded in `doc/adr/013-vault-project-lifecycle-and-departure-records.md`.
This section maps the mechanism onto the code.

### The commands

| Command | MCP tool | Code | What it does |
|---|---|---|---|
| `vp vault init <path> --remote name=url…` | — | `cmd/vp/cmd_vault_init.go`, `storage/vault_init.go` | New empty vault: records its remotes in the tracked `.vibe-palace/remotes.toml`, one commit on `main`, published to every remote (each must be reachable and empty) |
| `vp vault clone <url> <path> [--bind <p>…]` | — | `cmd_vault_clone.go`, `storage/vault_clone.go` | Clone a published vault, adding and fetching every remote in `remotes.toml`; `--bind` makes it this host's vault for those projects. No commit, no push |
| `vp vault copy <p>… --from <remote-url> [--at <sha>] [--as <newname>]` | `vp_vault_copy` | `cmd_vault_copy.go`, `storage/lifecycle_copy.go`, `tools/vault_copy.go` | Receiver-run copy from another vault's **published remote** (never a host path), through a private blobless snapshot; one commit with `Vp-Copy-*` trailers, footprint hash checked against the source. `--as` copies one project under a new slug (copy, then rename in this vault) |
| `vp vault rename <old> <new>` | `vp_vault_rename` | `cmd_vault_rename.go`, `storage/project_rename.go`, `storage/project_rename_rewrite.go`, `storage/commit_rename.go`, `indexstore/rename.go`, `tools/vault_rename.go` | In-vault rename to a **fresh** slug: moves the footprint, rewrites every stored identifier, one commit with `Vp-Rename-*` trailers and a `renamed` departure record, then a host-local index-store rename (embed cache rebuilt) |
| `vp vault project delete <p>… (--moved-to <url> \| --discard)` | `vp_vault_project_delete` | `cmd_vault_project_delete.go`, `storage/lifecycle_delete.go`, `tools/vault_project_delete_tool.go` | One published commit that removes the projects and adds their departure records; `--moved-to` refuses unless the destination's remote holds a verified copy |
| `vp config bind <slug>… --vault <path>` | `vp_config_bind` | `cmd/vp/cmd_config_bind.go`, `storage/project_bind.go`, `tools/config_bind_tool.go` | Write `[project_vaults]` lines (below) |

`vp_config_bind` and `vp_vault_project_delete` are registered on the stdio
transport only, never on `vp mcp serve`, because they write the host the
process runs on (`StdioOnlyToolNames`, `internal/tools/register.go`).

### In-vault rename, and copy-as

**`vp vault rename <old> <new>`** (`vp_vault_rename`, U9) renames a project
within one vault. The mechanism is `storage.PlanRename` / `storage.ApplyRename`
(`internal/storage/project_rename.go`): the plan action writes nothing and
returns the move set, the rewrite-class counts and a **digest**
(`renameDigestFormat`, `project_rename.go`); `--expect <digest>` binds it, and
apply refuses on a mismatch. The digest binds the slugs, the served vault's
identity, the move set and the rewrite counts — never a HEAD — so an unrelated
push does not invalidate it. Apply makes **one** commit carrying
`Vp-Rename-From` / `Vp-Rename-To` trailers (`project_rename.go`) plus a departure
record for the old slug, checks that nothing still names it, and publishes
exactly that commit to every remote. It refuses a `<new>` the vault already holds
or records a departure for, and a vault not clean at every remote tip. The
**host-local index store moves with it**, off the published commit:
`internal/indexstore/rename.go` (`AdoptRenamedStore` / `AdoptRenamedProject`)
moves `palace/.local/index/<old>/` to `<new>/` under the index run lock, rewrites
each chunk's `wing`, and bumps the change counter's epoch rather than deleting it
(`internal/indexstore/lifecycle.go`); the embed cache is **not** carried — it is
removed and the new slug re-embeds lazily (operator M0 ruling 2026-10-05). `--undo`
reverses that host-local step after you have git-reverted the rename commit.

**`vp vault copy <p> --from <url> --as <new>`** (U11) composes the two:
`internal/tools/vault_copy_as.go` (`CopyProjectAs`) copies the single project
from a published source vault, then renames it in the receiver to `<new>`, in one
operation. It is resumable — if the copy already landed, only the rename runs
(`CopyAsResumed`) — and the copied archives join this host's baseline set under
the new name.

### Dry run, digest, `--expect`

Copy, clone, project delete and rename share one protocol. `--dry-run` (MCP: the plan
action, which the tool's `ReadOnlyWhen` predicate classifies as a read) runs
every check, writes nothing, and prints the plan, its **digest** and the exact
real-run command line. That printed line carries `--expect <digest>`. Given
it, the real run re-plans and refuses on any mismatch, because what would move
changed since the human looked. `--expect` is optional (`if req.Expect != ""`
in `lifecycle_copy.go`, `lifecycle_delete.go` and `vault_clone.go`), so a
hand-typed run without it re-plans but binds nothing. The real runs of init,
copy, clone and project delete each print their own undo lines, to paste as
printed. Only copy's and delete's are a `git revert` plus one push per remote.
Init's is `rm -rf <path>` plus comments naming the branch to delete on each
remote's git host; clone's is `rm -rf <path>` plus, after `--bind`, a `cp` that
restores the global config's backup. On another host, `vp vault pull`
the default vault first, so it holds the moved-to-vault records that
`vp vault clone --bind` checks. Init, copy and project delete each
take the vault's root lock (`vaultlock.AcquireHeld`). Before its first write,
each writes a host-local pending marker, `.git/vp-lifecycle-pending`
(`storage/lifecycle_marker.go`). While the marker stands, every other vp
commit and pull refuses (`ErrLifecyclePending`). Re-running the same command
finishes or redoes the run. Clone keeps a marker of its own in the directory
it is cloning into.

### Exact publish

A lifecycle commit is published by `storage/lifecycle_publish.go`, never by
`CommitAndPushPaths`: no rebase, no realignment, and every remote the vault
has (in `remotes.toml` order, then any other configured remote). Each push's
outcome is decided by reading the live remote tip, not by git's exit code. A
publish that stops is classified (`remote-moved`, `transport`,
`mirror-diverged`, `state-unknown`). Only `remote-moved` (the first remote
refused, so nothing was published) resets the commit, rolls the run back and
asks for a fresh dry run. A later remote's failure does not roll back: the
commit stays published where it landed and the pending marker is kept.
`transport` and `state-unknown` say to re-run the same command, which pushes
the same commit. `mirror-diverged` also keeps the marker, but it is an
operator matter: the mirror has a writer other than vp. Project delete
removes ignored leftovers (which `git revert` cannot restore) only after every
remote holds the commit.

### Departure records: the record wins over the directory

A departure record, `Audits/departures/<slug>.json` (`internal/departure`), says
a slug left this vault: kind `renamed` (to another slug here), `moved-to-vault`
(with an optional destination label, never a host path) or `deleted`. It is one
file per slug so two hosts only conflict when they record the same slug, and it
sits under `Audits/` because no project enumerator walks that directory.

`internal/departedpath` holds the one rule: **while a record exists, of any kind,
readable or not, the project is departed, whatever `Projects/<slug>/` holds.**
A re-scaffold does not reopen the slug. The only way back is to revert the
departure commit: in v1 a project cannot be copied back over its own
departure record (`copyDestinationRefusals`, `lifecycle_copy.go`). Every
write funnel refuses a write under a departed project's trees: `vaultfs` (the raw file tools and CLI), `atomicfile.Write`/`WriteStream`,
storage's append writer and the commit backstop. At the MCP seam
`gateIfMutating` refuses any mutating tool naming a departed project, and
bootstrap reports it in its `departed` field. The records are protected in turn:
an ordinary write, edit, delete or move under `Audits/departures/` is refused
(`departedpath.ErrRecordPath`). Only `vp vault project delete` and
`vp vault rename` write or remove one (copy, init and clone write none;
the `departure-record-writer` source-audit rule pins every caller), through
`vaultfs.WriteDepartureRecord`/`RemoveDepartureRecord`, which pass
`atomicfile.ForDepartureRecord` with the live root-lock token.

### Writing `[project_vaults]`

*Vault resolution* above covers how the table is **read**. Binds are written
by `storage.BindProjectVaults`, behind `vp config bind` (one or more slugs),
`vp_config_bind` and `vp vault clone --bind`. `storage.RebindCheckout`, which
no command calls yet, binds through it for its split kind; its rename kind
instead adds `[project_vaults].<to>` with `<from>`'s value by its own
compare-and-set (`checkout_rebind.go`) and never removes `<from>` (ADR-012).
For a bind, every
precondition for every slug is checked first. All lines go in one
compare-and-set write of the global config, so the bind is all-or-nothing. It
refuses a target vault that holds any departure record for the slug. It never
re-points an existing binding, never writes a checkout and never creates the
global config. The result is verified through `ResolveVaultBinding` from every
`--checkout`, and any failure restores the file byte for byte. A running MCP
server resolved its vault at startup. After a rebind, its mutating tools
refuse with a stale-binding error (`Registry.staleBinding`) until the host is
reloaded, and its reads carry a drift banner.

---

## MCP Server (Phase 2)

### Protocol Layer

Vibe-palace communicates via stdio JSON-RPC 2.0, using `mark3labs/mcp-go` as
the protocol implementation. The `Server` wraps `mcp-go`'s `MCPServer` and
injects the vault reference into every request context.

```
cmd/vp/cmd_mcp.go serveMCP → bootstrap() in cmd/vp/bootstrap.go
├── storage.OpenVaultFromCwd(cwd) # resolve vault (3 tiers, ADR-012: cwd vault_path, [project_vaults], global)
├── embedder.NewLazy(NewONNX...)  # DEFER the ONNX model load — no I/O here
├── search.NewEngine(emb, v, cfg) # create search engine (no indexes built yet)
├── context.NewResolver(v.Root)   # template resolver
├── mcp.NewServer(v)              # create MCP server
├── tools.RegisterAll(...)        # register the full tool surface
└── srv.Listen(ctx, stdin, stdout) # start stdio transport (back in serveMCP)
```

### Cold Start: Nothing Expensive Before the Handshake

Bootstrap performs **no embedding, no model load, and no indexing**. Both of
the expensive things it used to do happened *before* the MCP `initialize`
handshake was answered, and both of them could — and did — blow past the host's
initialize timeout, leaving the session alive with zero tools:

- **The ONNX model load.** `embedder.NewONNX` takes tens of seconds and
  downloads ~90MB on a cold model cache. `cmd/vp/bootstrap.go` now wraps that
  constructor in `embedder.NewLazy` (`internal/embedder/lazy.go`), a `sync.Once`
  proxy that constructs the real embedder on the first call that actually needs
  a vector. Construction happens at most once, concurrent callers share it, a
  construction *failure* is memoized rather than retried in a hot loop, and
  `Close()` on a never-used embedder does not force the load. The consequence:
  a model-load failure now surfaces at first search, not at startup.
- **The full-vault reindex.** Bootstrap used to spawn a goroutine that called
  `Engine.Rebuild` for every project in the vault. Most of that work was for
  projects the session never searched. It is gone. A search builds the one
  project it touches from its notes, decision chunks, iterations and local
  chunks (embed-cache hits only) — plus, while the vault has no migration
  marker, its tracked drawers; archived transcripts enter the index only
  through the pending-archive ingester or the explicit `vp index rebuild`
  (ADR-014 decision 7; see *Index Construction* below).

Because `Dimensions()` is a property of the loaded model, it returns
`(int, error)` — the dimensionality cannot be known before the model exists.

Integration coverage: `internal/integration/lazy_startup_test.go` drives a real
JSON-RPC `initialize` + `tools/list` against the production tool surface and
asserts the embedder was **constructed zero times**, then exactly once after the
first search.

### Tool Registration

`tools.RegisterAll` registers all tools with the `Registry`. Each tool
provides a JSON Schema for parameter validation:

```go
Registry.Register(Tool{
    Name: "vp_search",
    Description: "Semantic search within a project's knowledge base. Returns ranked results " +
        "with text, metadata, and relevance scores. project must name a project already " +
        "present in the vault; an unknown slug is a tool error, not an empty result.",
    Schema:      searchSchema,       // JSON Schema for params
    Handler:     searchHandler,      // func(ctx, params) → (any, error)
})
```

On dispatch, the registry validates incoming params against the compiled
schema before calling the handler. Handlers extract the vault from context
and operate on storage directly.

### MCP Tools

The authoritative list is the golden file, not this section:

```sh
jq -r '.tools[].name' internal/mcp/tool_surface.golden.json               # every tool
jq -r '.tools[] | select(.mutating) | .name' internal/mcp/tool_surface.golden.json   # the mutating subset
```

It holds **80 tools as of v10.2.0 (surface 10)**: relative to v8.2.0 (surface 8),
surface 10 adds `vp_index_status` and `vp_vault_rename`, and removes
`vp_palace_backfill_decisions` (ADR-014 decision 10) and the retired
`vp_vault_split` / `vp_vault_merge` (U12). Source files are under
`internal/tools/`; `grep -l 'Name: *"vp_<tool>"' internal/tools/*.go` finds any one.
The table groups them by category.

| Category | Tools | Source files | Purpose |
|---|---|---|---|
| Context and instructions | `vp_bootstrap_context`, `vp_get_command`, `vp_get_skill`, `vp_list_commands`, `vp_list_skills`, `vp_cmd`, `vp_skill`, `vp_get_skill_section`, `vp_get_doctrine`, `vp_manual`, `vp_read_resource` | context_tools.go, command_tools.go, cmd_tools.go, skill_section_tool.go, context_query_tools.go, manual_tool.go, resource_read_tool.go | Session bootstrap index, command/skill resolution, doctrine, paging of `vibe-palace://` resources |
| Workflow documents | `vp_get_workflow`, `vp_get_resume`, `vp_update_resume`, `vp_get_knowledge`, `vp_list_projects`, `vp_append_iteration`, `vp_get_iteration` | context_query_tools.go, project_tools.go, get_iteration_tool.go | resume / workflow / iterations read and write |
| Tasks | `vp_list_tasks`, `vp_get_task`, `vp_manage_task` | task_tools.go | Task queue, epics, lifecycle (see *Tasks and the board*) |
| Sessions and analytics | `vp_capture_session`, `vp_get_project_context`, `vp_search_sessions`, `vp_get_session_detail`, `vp_get_effectiveness`, `vp_get_friction_trends` | session_tools.go, session_query_tools.go, friction_tools.go | Capture and session history |
| Search | `vp_search`, `vp_search_cross_project`, `vp_refresh_index`, `vp_index_status` | search_tools.go, system_tools.go | Hybrid semantic search; explicit index rebuild; read-only `index_coverage` report |
| Palace | `vp_palace_status`, `vp_list_wings`, `vp_list_rooms`, `vp_traverse`, `vp_find_tunnels`, `vp_palace_query` | palace_tools.go, palace_query_tools.go | Wing/room navigation over the host-local chunk store |
| Knowledge graph | `vp_kg_query`, `vp_kg_add`, `vp_kg_invalidate`, `vp_kg_timeline`, `vp_kg_stats` | kg_tools.go | Entity/triple facts |
| Learnings | `vp_list_learnings`, `vp_get_learning` | learning_tools.go | Cross-project learnings under `Knowledge/` |
| Memory | `vp_memory_list`, `vp_memory_read`, `vp_memory_write`, `vp_memory_delete`, `vp_memory_harvest` | memory_tools.go | Host-agnostic AI memory (ADR-004) |
| Vault files | `vp_vault_read`, `vp_vault_list`, `vp_vault_exists`, `vp_vault_sha256`, `vp_vault_write`, `vp_vault_edit`, `vp_vault_delete`, `vp_vault_move` | vault_file_tools.go | Vault-relative CRUD through `vaultfs` |
| Vault git and freshness | `vp_vault_sync`, `vp_vault_tidy`, `vp_vault_status`, `vp_repo_freshness` | system_tools.go, repo_tools.go | Pull/push/tidy, sync state of the vault, and of the project checkout |
| Vault lifecycle | `vp_vault_copy`, `vp_vault_rename`, `vp_vault_project_delete`, `vp_config_bind` | vault_copy.go, vault_copy_as.go, vault_rename.go, vault_project_delete_tool.go, config_bind_tool.go | See *Vault lifecycle and departure records* (`vp_vault_split` / `vp_vault_merge` retired, U12) |
| Onboarding | `vp_init` | system_tools.go | Project onboarding over `internal/onboard` |
| Wrap and commit | `vp_collect_wrap_state`, `vp_stamp_iter`, `vp_preflight_wrap`, `vp_ingest_commit_msg`, `vp_archive_commit_log` | wrapstate_tools.go, commit_msg_tools.go, commit_log_tools.go | `/wrap` mechanics |
| Summarization | `vp_enqueue_iteration_summary`, `vp_check_summarization_queue`, `vp_trigger_summarization_drain` | summarize_tools.go | Host-local summarization queue |
| Diagnostics and integrity | `vp_health`, `vp_check`, `vp_surface_check`, `vp_audit_vault`, `vp_archive_link`, `vp_scan_plans` | health_tools.go, check_tool.go, surface_tools.go, audit_tools.go, archive_tools.go, scan_plans_tool.go | Runtime health, checks, audits, repair |

All tools except the search-dependent ones are always registered. The
search-gated tools — among them `vp_search`, `vp_search_cross_project`,
`vp_capture_session`, `vp_get_project_context`, `vp_search_sessions`,
`vp_get_session_detail`, `vp_get_effectiveness`, `vp_get_friction_trends`, and
`vp_refresh_index` — require a search *engine* (`engine != nil`,
`internal/tools/register.go:162`). They do **not**
require a loaded model: the engine holds a lazy embedder, so registration never
touches ONNX and a model that fails to load fails the first search rather than
the tool surface. The vault-CRUD, commit, wrap-state, and surface-check tools are
filesystem operations and are always registered.

`vp_surface_check` (`surface_tools.go`) is a read-only probe that returns the
same whole-vault surface-compatibility verdict a mutating write is gated
against (`check.CheckSurface(vault.Root)`), so restart/wrap templates can run a
surface preflight without shelling out to `vp check`.

`vp_check` (`check_tool.go`) exposes the named, embedder-free diagnostic checks
over MCP. It dispatches the **same registry** `vp check --check NAME[,NAME…]`
does — `check.Producers` / `check.RunSelected` in `internal/check/selector.go` —
so the CLI's selectable names and the tool's advertised names cannot drift; the
registry was moved down out of `package main` for exactly that reason, and
`RunSelected` is pure (the caller supplies the vault root, so the long-lived
`vp mcp` process never re-resolves a vault from its launch directory). The
`checks` argument is optional: omitted, every producer runs in the declared
`check.ProducerOrder`, which is what makes repeat runs reproducible. The result
is `{status, summary, checks[{name, status, summary, details[]}]}`; the
top-level status is an **advisory** worst-of roll-up, because the checks
legitimately disagree about an absent vault — consumers key off the rows.
Nothing on this path reaches `check.Run`, so the embedder is never loaded.
`vp_check` **subsumed** the former per-check `vp_check_resume_refs` wrapper
(now removed — one shared registry beats one hand-written tool per check);
`vp_surface_check` stays because it is the preflight the surface gate itself
depends on and carries gate-specific fields the uniform envelope does not.

`vp_check_summarization_queue` (`summarize_tools.go`) wraps
`check.CheckSummarizationQueue` directly, outside `vp_check`'s registry: it is
read-only (never drains, claims, or mutates a queue file) but, unlike every
row `vp_check` dispatches, it is **project-path-scoped, not vault-scoped** — it
needs the caller's `project_path` to find `<project_path>/.vibe-palace/
summarization-queue/`, which `check.Producers`' vault-rooted signature has no
way to carry. It returns `{status: "empty"}` for a fully drained queue, or
`{status, summary, details[]}` otherwise; it never launches the detached
`vp drain summaries` subprocess `vp_trigger_summarization_drain` does.

The authoritative enumeration is the full tool surface versioned in
`internal/mcp/tool_surface.golden.json` (its `surface_version` is
`MCPSurfaceVersion` in `internal/surface/version.go`), pinned by
`internal/tools/register_test.go` — the registry
(`internal/tools/register.go`) exposes that surface with a search engine and
the search-gated subset stripped without one.

### Remote Transport: Streamable HTTP (`vp mcp serve`)

Besides the stdio transport (`vp mcp`), the binary can expose the same tool
backend over a **Streamable-HTTP MCP** transport via `vp mcp serve`
(`internal/mcp/transport_streamable.go`, `cmd/vp/cmd_mcp_serve.go`). This is a
*dedicated* MCP server instance — never the stdio one — so its tool surface can
be filtered independently of the local server:

- **Bearer authentication.** The handler is wrapped in middleware that requires
  `Authorization: Bearer <token>`, compared in constant time (both sides reduced
  to a SHA-256 digest before `subtle.ConstantTimeCompare`). The token *value* is
  read at runtime from the environment variable named by `--bearer-token-env`
  (default `VP_MCP_BEARER_TOKEN`); only the variable *name* is configurable — the
  secret is never written to config. When the variable is unset the server runs
  unauthenticated and prints a loud startup warning, on the assumption that the
  operator fronts it with a tunnel or network ACL they control.
- **Read-only by default, and FAIL-CLOSED.** Unless `--allow-writes` is passed,
  this instance serves only the tools affirmatively named in
  `tools.ReadOnlyServeToolNames`; everything else the registry holds is stripped
  via `Server.DeleteTools`, so it is absent from both `tools/list` and
  `tools/call`. Exposing writes prints a second startup warning.

  This is a SEPARATE declaration from `tools.MutatingToolNames`, which answers
  only the surface gate's question. The two agree today and are pinned to say
  so, but they are independent on purpose: a false negative on the surface gate
  is a detectable ungated write, while a false negative here publishes a write
  tool on a surface an operator believes is read-only — so this one is an
  allow-list and strips anything unclassified. See the asymmetry note on
  `ReadOnlyServeToolNames` before touching either.
- **Vault-in-context (surface-gate parity).** The handler installs
  `server.WithHTTPContextFunc` to put the `*storage.Vault` on every request
  context, exactly as the stdio transport does via `Server.contextFunc`. This is
  load-bearing rather than cosmetic: the surface gate (`gateIfMutating`) reads the
  vault root from the context, so before this existed `VaultFromContext` returned
  nil here, the gate saw `root == ""`, and — because `CheckCompatible` then treated
  an empty path as "nothing to check" — **every mutating tool served under
  `--allow-writes` bypassed the surface gate entirely**. A gate that depends on
  per-transport context plumbing has one silent-bypass mode per transport; both
  transports (and `Server.HandleMessage`, the test seam) now inject the vault.
- **No CORS.** Both real clients connect server-side, so no CORS headers are
  emitted; browser preflight is out of scope.
- **Binding.** Defaults to `127.0.0.1:7423` (`--addr` / `--port`, the port
  falling back to `cfg.HTTPPort`). The `StreamableHTTPServer` handler is mounted
  directly as the `http.Server` handler, so it speaks MCP at the root path `/`
  (the library's default `/mcp` mux applies only to its own `Start`, which is not
  used here). Public exposure is expected to go through an explicit tunnel that
  terminates TLS — there is no in-binary TLS.

The mutating-tool filtering lives in `cmd/vp`, not `internal/mcp`: `internal/tools`
imports `internal/mcp` to register handlers, so `internal/mcp` cannot import
`internal/tools` to learn which tools mutate without an import cycle. The
composition root in `cmd/vp` (`buildMCPServeHandler`) sees both packages and
applies the filter there.

---

## Versioning and the surface gate

Two independent version axes live in `internal/surface`, and they answer
different questions (the header comment of `format.go`):

| Axis | Constant | On disk | Fires when | Hazard |
|---|---|---|---|---|
| Tool surface | `MCPSurfaceVersion` (`version.go`) | `.surface` stamps in `Projects/<p>/`, `palace/<p>/`, `Templates/`, `Audits/` | the vault is **ahead** of the binary (a newer binary wrote it) | write |
| Data format | `RequiredDataFormat` (`format.go`) | `.vibe-palace/vault.toml` (`format = N`) | the vault is **behind** the binary (data not yet migrated) | read |

**Stamps.** Every whole-file write through `internal/atomicfile` best-effort
stamps the `.surface` of the stamp root the write falls under (`Projects/<p>/`,
`palace/<p>/`, `Templates/` or `Audits/`; `surface.StampForPath` →
`ResolveStampDir`). Writes outside those roots stamp nothing.
A stamp is byte-stable per surface version, so it changes only when the
version rises. `surface.CheckCompatible` scans every stamp root and takes the
**maximum**. The first write by a newer binary anywhere in the vault therefore
raises the floor for every host. `vp check --check surface` and
`vp_surface_check` report the verdict.

**Data format.** `vault.toml` is written only by a scaffold (a fresh vault is born current; in
v10.2.0 a vault made by `vp vault init` is also born migrated, with the migration marker, the
derived-path ignore lines and the vault-level stamp `Audits/.surface` at `MCPSurfaceVersion`, 10 from
v10.2.0, which `InitVault` writes itself, never the shared scaffold `reconcile.ScaffoldNewVault`; a
vault made by `vp init`, `vp config sync` or onboarding, which use the vault reconciler directly,
starts unmigrated, and copy refuses an unmigrated destination, ADR-014
decision 11), by
a migration that advances its format, or by the ADR-014 migration and its empty-vault path, which
add the migration marker key without changing the format (ADR-014 decision 11) — never as a side
effect of a write. Every one of these writers round-trips every field of `vault.toml`, so a format
migration keeps the marker. The KG-storage reads call `checkFormatGate`
(`internal/storage/format_gate.go:29`), which applies `surface.EnforceFormatFailStop`
(`internal/surface/format.go:342`). Format 2 exists because `vp board` needs task
creation/modification times and the widened status vocabulary to be trustworthy vault-wide
(`vp migrate task-board-fields` backfills them). The lifecycle commands check the data format of the
vaults they read (clone refuses a vault that is not at this binary's format).

**Release tags** are `v<MCPSurfaceVersion>.<RequiredDataFormat>.<build>`
(ADR-011). `.github/workflows/release.yml` enforces the scheme on a pushed
tag, and `vp check --check release-version` checks it on a built binary.
Derive the current pair from the two constants rather than from this document.

**The gate.** On the MCP side the only surface gate is `Registry.gateIfMutating`
(`internal/mcp/tools.go`). Both dispatch paths route through it, and
ADR-010 records why it stays at the dispatch seam rather than in the write
primitives. For a tool marked `Mutating`, unless its `ReadOnlyWhen`
predicate says this invocation writes nothing, it refuses in this order:
1. The server's startup vault binding is stale (see *Writing
   `[project_vaults]`*).
2. `surface.EnforceFailStop` fails: the vault is ahead of the binary, or there
   is no reachable vault.
3. The call names a departed project.

The surface gate never refuses a read; the data-format gate above can still
refuse KG reads on an unmigrated vault. There is no MCP startup gate, so the server stays up
and the remediation arrives as a tool error. On the CLI, `surfaceGate`
(`cmd/vp/main.go`, run from `preRun`) fail-stops commands registered as vault
mutating and only warns for everything else. It gates only the configured
vault; a mutating command writing to a root named by `--vault` gates that root
itself (`enforceSurfaceOnRoot`, `cmd/vp/vault_root_flag.go`). `VP_SURFACE_GATE=warn` downgrades
a version mismatch, and nothing else, to a warning.

---

## CLI Framework (`internal/cli`)

`Registry.Dispatch` routes argv to a registered `Command`. Two-word
subcommands (e.g. `vault pull`) are looked up first; single-word
lookups fall through when the two-word combo is unknown.

### Parent-command contract

A parent command is one that declares `Subcommands`. Dispatch handles
these uniformly so each parent doesn't need a hand-rolled usage
string:

- `vp <parent>` (no arguments) → framework renders parent help on
  **stdout**, exit `0`.
- `vp <parent> <unknown>` (non-flag token that doesn't match any
  registered two-word subcommand) → framework writes
  `"vp <parent>: unknown subcommand \"<token>\""` plus the parent
  help to **stderr**, exit `ExitUser` (1).
- `vp <parent> --help` / `-h` → parent help on stdout, exit `0`
  (unchanged from the pre-gate behavior).
- `vp <parent> <known-sub> …` → two-word lookup hits first; the
  parent gate never fires.

`Command.Run` is optional when `len(Subcommands) > 0`: pure parents
delegate rendering to the dispatcher.

`Command.BareInvocation = true` opts a parent out of the auto-help
path for empty / flag-only invocations, routing them back to `Run`.
Only `vp hook` sets this — it doubles as a Claude Code stdin handler
and must receive bare `vp hook` calls with no args. Non-flag unknown
tokens still take the unknown-subcommand error path; `BareInvocation`
is not an escape hatch for typo detection.

A CI-level invariant (`TestAllCommandsRegisterValidly` in
`cmd/vp/main_test.go`) asserts every registered command has either
`Run != nil` or non-empty `Subcommands`, and that `BareInvocation`
implies `Run != nil`.

### `vp check` and selective execution

`vp check` renders an ordered list of `check.Result` rows (`Pass` / `Fail` /
`Skip` / `Info`), either as a human table or — with `--json` — as the stable
`check.JSONReport`. `gatherCheckResults` (`cmd/vp/cmd_check.go`) runs the full
set; the five reconciled artifacts come from their reconcilers' `Check()`
methods so `vp check` and `vp config sync --dry-run` see the same world.

`--check NAME[,NAME...]` runs only the named check(s) via the `check.Producers`
map (`internal/check/selector.go`) — a selective-execution path that skips the
expensive embedder load and tool-registry build. The `vp_check` MCP tool
dispatches the same map. Registered names:

| Name | Row | Scope |
|------|-----|-------|
| `surface` | `Surface` | Whole vault — binary MCP surface vs. max `.surface` stamp |
| `vault-filesystem` | `Vault filesystem` | Whole vault — does the filesystem accept `:` in filenames (NTFS/exFAT) |
| `stray-scaffolds` | `Stray scaffolds` | Whole vault — scaffold-only orphan projects under `Projects/` |
| `palace-local-only` | `Palace local-only` | Whole vault, this host only — `palace/<slug>/` directories holding no file outside machine-local `.local/` |
| `vault-project-config` | `Vault project config` | Whole vault — retired `Projects/<slug>/config.toml` files still on disk |
| `surface-merge-driver` | `Surface merge driver` | Whole vault — a `.gitattributes` naming the deleted `vp-surface` merge driver |
| `resume-caps` | `Resume caps` | Whole vault — every `Projects/*/resume.md` |
| `resume-refs` | `Resume refs` | Whole vault — host-local plan refs in every `Projects/*/resume.md` |
| `vault-abs-paths` | `Vault abs paths` | Whole vault — host-rooted absolute paths in every project's `resume.md` + `workflow.md` |
| `iteration-headings` | `Iteration headings` | Whole vault — non-canonical iteration H2s in every `Projects/*/iterations.md` |
| `template-drift` | `Template drift` | Whole vault — vault `Templates/` copies vs. the embedded templates |
| `host-surfaces` | `Host surfaces` | This host — plugin trees under `$HOME` |
| `writer-identity` | `Writer identity` | This host — the writer fingerprint it writes under |
| `stale-mcp` | `Stale MCP` | This host — running `vp mcp` processes whose image was replaced |
| `release-version` | `Release version` | This binary — its stamped release version vs. `MCPSurfaceVersion`.`RequiredDataFormat` (ADR-011) |

The table is ordered as `check.ProducerOrder` declares, which is the order a
default (unfiltered) run emits. Re-derive it from that slice rather than trusting
this table (`awk '/ProducerOrder *=/,/}/' internal/check/selector.go`): it has
gone stale three times as producers joined without a row here. The rows
match the slice in v10.2.0.

This table enumerates only the **vault-rooted, selector-registry** checks —
the ones `check.Producers` can dispatch by name, because their signature takes
just a vault root. `vp check`'s full CLI suite also runs a small number of
**cwd-scoped, project-repo-rooted** rows outside that registry and therefore
not selectable via `--check NAME`: `CheckAgentDrift`, `CheckProjectGitignore`,
`CheckGitPostCommitHook`, and `CheckSummarizationQueue`. Each of these needs an
actual project repo path (to walk `.claude/agents/`, read `.gitignore`, stat
`.git/hooks/post-commit`, or scan `.vibe-palace/summarization-queue/`) that
`check.Producers`' vault-rooted signature structurally has no way to carry, so
`gatherCheckResults` (`cmd/vp/cmd_check.go`) calls each of them directly
against `cwd` instead of registering them.

`palace-local-only` is deliberately **absent from the delivery check lists** in
the restart and wrap commands and the epic-orchestrator skill. A vault copy of
those templates under `Templates/` — an override, or a mirror not yet pruned by
`vp config sync` — is served ahead of the embedded copy, so a selector named there
reaches every host that reads the vault, including one on an older binary, where
`vp_check` refuses an unknown name. It runs in the full `vp check` suite and on an explicit
`vp_check {checks:["palace-local-only"]}`.

An unknown name exits `ExitUser` with an `unknown check` diagnostic.

### resume.md cap detection (`check.CheckResumeCaps`)

`resume.md` is a **gateway, not an archive**: `vp_bootstrap_context` pays for
every byte at session start, and the full record already lives in
`iterations.md`, `tasks/done/` and `tasks/cancelled/`. The `/vpc-wrap` Step 3
contract therefore caps its growing sections — but with the typed resume
editors retired, routine edits go through the generic `vp_vault_read` +
`vp_vault_edit` pair and **no typed write path exists on which a cap could be
mechanically enforced**. Any agent holding Bash could bypass one anyway.
Prevention is unachievable in-process; **detection is achievable**, so the caps
are surfaced as a warning, never a gate:

| Cap | Threshold | Constant |
|-----|-----------|----------|
| Total size | > 25 KB | `check.ResumeMaxBytes` |
| `## Project History` data rows | > 15 | `check.ResumeMaxHistoryRows` |
| `## Completed Plans` data rows | > 12 | `check.ResumeMaxCompletedRows` |

`CheckResumeCaps` walks `<vault>/Projects/*/resume.md` and emits one `Info` row
naming each over-cap project and which caps it broke; every resume within its
caps yields `Pass`. It is strictly read-only — it never writes, never "fixes",
never touches `resume.md` — and it is never `Fail`: pruning is a wrap-time
judgement call, and a fat resume is a tax, not a breakage. **Absence is never a
violation**: a missing `resume.md`, a missing section, and an empty or
header-only table all report nothing.

Row counting is deliberately line-oriented rather than a markdown parse. Resume
cells carry escaped pipes (`\|`), inline code spans and bold runs, all of which
defeat a cell-splitting parser; only three structural facts are needed, and each
is decidable from the line alone. Fenced code blocks are tracked and skipped; a
section runs from its `##` heading to the next H1/H2 (a `###` sub-heading does
not close it); and within a section each contiguous run of pipe-leading lines
counts only the lines *after* its `|---|---|` delimiter, so header and delimiter
rows are excluded by construction and a run with no delimiter — which GFM does
not render as a table at all — counts zero.

### resume.md host-local plan refs (`check.CheckResumeRefs`)

`resume.md` is **committed and shared** — it travels in the vault git history and
`vp_bootstrap_context` reads it on every host. A plan reference under a
host-local path is therefore dead weight anywhere but the machine that wrote it:
the path does not resolve on another host and it leaks a local home layout into a
shared artifact. `CheckResumeRefs` flags exactly two patterns:

| Pattern | Example |
|---------|---------|
| Home-relative (`resumeRefHomeRe`) | `~/.claude/plans/foo.md` |
| Absolute (`resumeRefAbsRe`) | `/home/dev/.claude/plans/bar.md` |

It walks `<vault>/Projects/*/resume.md` and emits one `Info` row naming each
offending project with the source line number and matched path; a clean vault is
`Pass`. Like the cap check it is strictly read-only and **never `Fail`** — the
fix (rewrite the pointer as vault-relative, e.g. `tasks/done/…`) is a wrap-time
judgement call, and a stale path is a tax, not a breakage. It is **fence-aware**:
a path documented inside a Markdown code fence is a sample, not a live pointer,
and is skipped via the shared `internal/mdfence` scanner (so an inline code run
is never misread as an opening fence). It reads **only** `resume.md` — task files
and everything else are out of scope. The same verdict is exposed
host-agnostically over MCP by the read-only `vp_check` tool, via its
`resume-refs` selector.

### Host-rooted absolute paths in the core (`check.CheckVaultAbsPaths`)

The vault is synced to every machine and lives somewhere different on each, so a
host-rooted absolute path committed into a synced document is a fact about the
**one** host that wrote it. Iter 188 is the specimen: `resume.md` carried
`Vault location: /home/johns/vibe-palace-vault`, true only on the operator's
previous WSL host — and an **empty directory** sits at that path on the current
machine, so the stale answer looked *plausible* instead of failing loudly.

**Scope is `resume.md` + `workflow.md` only** — the ADR-009 inviolable core, and
the only two synced docs read as CURRENT TRUTH rather than as history.
`iterations.md` and `tasks/` are deliberately excluded: they legitimately quote
host paths as specimens of the mistake (the task that commissioned this check
quotes the 188 path twice, once in a blockquote where fence-awareness would not
save it), and a check that fires on the record of a bug being fixed is one
operators learn to skim.

Detection works from an **allowlist of host-rooted prefixes**, never a denylist
of exemptions: `/home/<user>`, `/Users/<user>`, `/mnt/<drive>`, `/root`, a
Windows drive root (`C:\`), and the extended-length prefix (`\\?\`). Everything
else is silent by construction — `/proc/<pid>/exe`, `/usr`, `/etc`, `/tmp` and
`/var` never match because they are not in the set, so there is no exemption list
to maintain and no way for a new machine-independent path to start firing when
someone forgets to add one. Tilde paths (`~/.local/bin/vp`) are host
*conventions* that resolve everywhere, and repo-relative paths have no root at
all; neither is matched.

It deliberately does **not** consult `os.UserHomeDir()`. Flagging the running
host's literal `$HOME` expansion would make the verdict depend on which machine
ran the check — the exact bug class the check exists to detect — and the case is
already covered structurally by the prefixes above.

Like its neighbours it is **fence-aware** (shared `internal/mdfence` scanner),
strictly read-only, and **Info, never Fail**: the remedy is *resolve, don't
recall* — `vp status` prints the path the binary resolved — and choosing what the
document meant to say is a human judgement, not a mechanical rewrite. For that
reason `/vpc-wrap` reports this row and does **not** auto-fix it, unlike the
`resume-refs` row it sits beside. Exposed host-agnostically over MCP by
`vp_check`'s `vault-abs-paths` selector.

### Orphaned-plan reporter (`internal/planscan`)

Claude Code drops each plan's markdown under a **flat** `~/.claude/plans/*.md`
directory (honoring `CLAUDE_HOME`) — with **no** cwd encoding, unlike the
cwd-encoded `~/.claude/projects/` tree. A stray plan therefore carries no
structural signal about which project it belonged to; the only cwd evidence it
leaves is the absolute filesystem paths its prose happens to mention.
`planscan.Scan(claudeHome, vaultRoot)` is a strictly **read-only** "detect-and-
report" reporter over that directory. For each plan file it:

1. greps every absolute path from the body (URL-safe, fence-tolerant regex),
2. reduces them to candidate directory roots ranked by **frequency, then depth**
   (a deeper dir is the better cwd guess on a tie),
3. resolves each candidate's owner and folds the result into one verdict.

| Resolution `kind` | Meaning |
|-------------------|---------|
| `managed` | A candidate dir has a `.vibe-palace.toml` (`project.DetectSignal == SignalVibeConfig`) **and** its detected slug has a matching `<vault>/Projects/<slug>` directory. `project` holds the slug. |
| `unmanaged` | Candidate dirs resolve to real directories but none is a vault-managed project (no marker up to `$HOME`, or a marker with no vault project). `candidate_dir` is the top-ranked candidate as evidence. |
| `none` | The body contained no absolute path — unattributable. |

Attribution gates on `DetectSignal` **first**: `project.DetectProject` falls back
to git-remote/basename for *any* directory, so it is never trusted on its own;
only after a `.vibe-palace.toml` is found is the slug taken and confirmed against
a real `Projects/<slug>` directory. A plan whose candidates resolve to **more
than one distinct owner** sets `ambiguous: true` and lists every ranked candidate
rather than collapse a multi-root plan to a single guessed owner.

An **absent plans dir is normal** — it returns an empty report with a nil error,
which is exactly what happens on **Grok and Zed hosts** (they have no plans
directory at all). The reporter is **Claude-only** in practice and **never
promotes, deletes, or writes** anything: it reads the plans dir, reads the
referenced directories' markers, and Stats the vault. All logic lives in
`internal/planscan`; the `vp plans scan [--json]` CLI subcommand and the
read-only `vp_scan_plans` MCP tool are thin wrappers that resolve the Claude home
(`archive.ClaudeHome`) and vault root and marshal the `Report`.

Both command templates consume this reporter, but with **different mandates**
(prose enforcement, ADR-006 — the templates ask the executor to honor the split;
the embedded-template tests pin that the ask survives edits). `/restart`'s
session-start sweep **promotes** this-project strays into vault tasks and deletes
the scratch copy. `/wrap`'s Step 6b sweep is **narrower**: it runs pre-commit
under Rule 0, so it may delete a scratch plan **only** when that plan was promoted
to a task *during this session* (the scratch copy is now pure redundancy); every
other stray — other-project, `unmanaged`, `none`, `ambiguous`, or an unpromoted
this-project plan — is **reported to the human, never acted on**. Wrap also carries
a companion resume guardrail: `resume.md` is committed and synced, so it must not
reference a host-local `~/.claude/plans/…` (or the project-root `commit.msg`) —
`vp_check` (selector `resume-refs`) / `vp check --check resume-refs` flags that.

### Plan worktree isolation (`internal/worktree`)

`vp worktree create|remove|list` (`cmd/vp/cmd_worktree.go`) gives
`/vpc-execute-plan` an isolated tree per plan. `Create` cuts a `plan/<slug>`
branch from a base branch (`main` by default) and checks it out into a
**sibling** worktree at `../wt/<slug>` — outside the primary tree, so multiple
plans can run concurrently without stepping on each other's working state. The
package operates on the **project repo, never the vault**: the vault has its
own write disciplines (locks, tidy, sync) and no worktrees.

Removal is deliberately safe: `Remove` detaches the worktree and, when asked to
delete the branch, uses `git branch -d` — the non-forcing form, which refuses
while the branch carries unmerged commits — so an unlanded plan cannot be
destroyed by cleanup. Landing is a human act: the human merges `plan/<slug>`
into the base branch with `merge --ff-only`, mirroring the epic-orchestrator's
`../wt/<epic>` + ff-only convention one level down, at single-plan granularity.
`List` enumerates the repo's worktrees, filtered to `plan/*` by default.

### Tasks and the board (`internal/taskgraph`)

A task is one markdown file under `Projects/<p>/tasks/`, written by
`internal/storage` under the per-path lock (`vp_manage_task`: create, amend,
overwrite, set_meta, update_status, set_relations, retire, cancel, move). Its
header is a run of `**Field:** value` lines, an open schema (ADR-011). It
carries only its own outbound links: `**Parent:**`, `**Depends:**` and, on a
cancelled task, an optional `**SupersededBy:**` (set by `cancel` with
`superseded_by`). Everything else, including the reverse views (children,
supersedes), is **derived, never stored**, by `internal/taskgraph`. An
epic is not a field. It is any task something names as its parent (a root
epic heads its own chain, a story sits under one). Blockers, clearing order
(a dependency always above what it blocks), orphans and cycles are computed
from the set, and bad data is reported as findings rather than errors. The
dependency is one-way: `internal/storage` never imports `taskgraph`, so a
write is never validated against the whole vault and a child can be written
before its epic.

**Statuses** (`internal/storage/tasks.go`). The writable ones are
`planning`, `reviewed`, `in_progress`, `blocked` and `icebox`. The terminal
`done` and `cancelled` are reached only by **moving** the file into
`tasks/done/` or `tasks/cancelled/` (retire / cancel), and the directory
is authoritative. `icebox` means known but not scheduled: the file stays in
`tasks/`, and the work-queue readers (`vp tasks`, `vp_list_tasks`, bootstrap's
head of queue) hide it by default. Only the grouped `vp tasks` view says how
many it hid; `vp board` always shows the icebox.

**Readers**, all over one `taskgraph.Graph`:
- `vp tasks` / `vp_list_tasks`: the work queue, grouped by epic, with
  `--epic`, `--standalone`, `--flat`, `--all` (MCP: `epic`, `standalone`,
  `epics_only`, `include_icebox`, `include_done`).
- `vp tasks epics`: the root epics, with transitive open/total counts.
- `vp board`: a history view (`Graph.Board()` → Active / Icebox / History
  buckets, each decided by the root's own status alone; history is listed
  most recent first, and nothing is filtered by default).
- The bootstrap `head_of_queue` (`internal/tools/bootstrap_rank.go`):
  unblocked work, in progress first, then priority, then topological order.

The board's chronology needs each task's creation and modification days and
the widened status vocabulary to be trustworthy across the vault. That is why
data format 2 exists (see *Versioning and the surface gate*).

---

## Context Injection (Phase 3)

### 5-Tier Palace-Scoped Resolution

Commands and skills support palace-scoped resolution with 5 tiers. First
match wins — no merging across tiers:

1. **Room override**: `{vault}/Projects/{project}/commands/{wing}/{room}/{name}.md`
2. **Wing override**: `{vault}/Projects/{project}/commands/{wing}/.wing/{name}.md`
3. **Project override**: `{vault}/Projects/{project}/commands/{name}.md`
4. **Vault template**: `{vault}/Templates/commands/{name}.md`
5. **Embedded default**: Compiled-in `templates/commands/{name}.md`

The `.wing/` sentinel directory distinguishes wing-level resources from room
subdirectories. The `Resolver` exposes `ResolveScoped(resource, project,
wing, room)` for full palace-scoped lookup, while the legacy
`Resolve(resource, project)` delegates with empty wing/room.

Other templates (workflow.md, resume.md, config) use 3-tier resolution
(project > vault > embedded) with project files at
`{vault}/Projects/{project}/{path}`.

Skills follow the same 5-tier precedence but treat the **directory as
the unit of override**. A skill is `skills/<name>/SKILL.md` plus an
optional `references/*.md` tree; the tier that supplies `SKILL.md`
wins for the persona entry-point, but each reference file falls
through independently via `ResolveSkillSection`. This lets a project
shadow only the persona while inheriting every reference from vault
or embedded tiers — overriding a skill does not oblige you to
re-author its reference corpus. See `doc/COMMANDS-AND-SKILLS.md` for
the `SkillFrontmatter` schema and the `ResolveSkillDir` /
`ResolveSkillSection` contract.

Skill **persistence within a session** is emergent, not enforced by
runtime re-injection. `vp_skill` is called once per `vps-<name>`
trigger and returns the persona body in a single response; we do not
re-inject the persona into every subsequent model turn. Instead, the
managed block in `CLAUDE.md` / `AGENTS.md` / `.cursorrules` teaches
the model to treat that one returned persona as STANDING instruction
for the rest of the session, and to recognize `vps-clear` /
`vps-replace:<other>` as lifetime-control prefixes it parses locally
(no second tool round-trip to "end" or "swap" a skill). The result is
a system that looks stateful from the user's seat while the server
stays stateless — the model carries the posture across turns via its
own context window, and the session boundary is the garbage
collector. When the contract itself changes (e.g. v1→v2), the
content-hashed managed block detects the stale copy on the next `vp
init` and rewrites it in place, preserving user content outside the
delimiters byte-for-byte.

### Embedded Templates

Templates are compiled into the binary via `//go:embed templates`. The
directive and the embedded `templates/` tree live in the
`internal/templates/` package (moved from `internal/context/` so the
filesystem lives next to the code that manages its lifecycle):

```
templates/
├── workflow.md                    # Thin per-project workflow (project-specific patterns + doctrine pointer)
├── doctrine.md                    # Generic agent operating manual (ADR-008, served on demand)
├── resume.md                      # Project state template
├── enrichment.md                  # Session-enrichment system prompt (ADR-005)
├── commands/{name}.md             # Built-in commands (capture, restart, wrap, …)
└── skills/{name}/SKILL.md         # Built-in skills, each with optional references/*.md
```

The command and skill sets change between releases. List them with
`vp commands list` / `vp skills list`, or
`find internal/templates/templates -maxdepth 2`.

Templates support `{{PROJECT}}`, `{{DATE}}`, `{{WING}}`, and `{{ROOM}}`
variable expansion. `internal/context/precedence.go` consumes the
embedded tree via `internal/templates.FS()`.

### Materialize-and-reconcile loop

The embedded tier is the *floor*, and on a healthy vault it is the only
copy of any built-in. Vault `Templates/` is **override-only**
(ADR-008, Design B): it holds files an operator wrote, never a
reconciler-owned mirror. Templates flow through four stations:

1. **Embedded-in-binary.** `internal/templates/templates/` is baked into
   every `vp` build. `internal/templates.WalkEmbedded()` enumerates
   every resource as `(RelPath, Bytes, SHA256)`, and the resolver serves
   each one directly from its embedded tier.
2. **`vp init` does not reconcile `Templates/`.** It never writes, prunes
   or reconciles `<vault>/Templates/`, and a fresh install creates
   neither that directory nor any host-local template state. (Onboarding's shim steps
   do read it, through the resolver's vault tier, which is how a
   vault-wide new command reaches every project.) So a first install onto
   a vault that already holds overrides cannot fail on them, and the CLI
   matches the MCP `vp_init` tool, which never had a Templates pass. What
   init does do: the Vault reconciler writes `<vault>/.gitignore` with
   `storage.CanonicalGitignorePatterns` (including `*.bak` and `*.new`)
   on a new vault, and tops up a present one with any canonical line it
   lacks through `storage.TopUpVaultGitignore` — a locked
   read-modify-write (`storage.LockedUpdate`). Onboarding scaffolds
   `<vault>/Projects/<slug>/{commands,skills}/` with a README stub
   (`TemplateTreeReconciler` in `Scaffold` mode — directory + README, no
   per-file copy). It also reconciles the *consuming project's* repo-root
   `.gitignore` via `storage.ReconcileProjectGitignore`, appending the
   host-local AI artifacts vp writes into the project tree
   (`storage.CanonicalProjectGitignorePatterns`: `/CLAUDE.md`,
   `/AGENTS.md`, `/commit.msg`, `/.claude/`, `/.grok/`, `/.vibe-palace/`)
   so they are never committed. `AGENTS.md` is a host-local vp-managed
   bootstrap shim — `vp init` creates and wires it (the cross-host
   `agents.md` baseline that teaches `vp_bootstrap_context` + the
   `vpc-*`/`vps-*` triggers), exactly as it treats `CLAUDE.md`. That project-root reconcile is append-only and
   idempotent — it never removes or reorders user lines, and a run where
   every canonical line is already present touches nothing. It also runs
   on `vp commands upgrade` so existing projects self-heal, and
   `vp check` surfaces an advisory (`Info`, never `Fail`) when canonical
   entries are missing.
3. **Users override at either tier.** `<vault>/Templates/` is the
   vault-wide override tier and `<vault>/Projects/<slug>/{commands,skills}/`
   the per-project one. No reconciler and no upgrade command changes an
   override in either: `vp config sync` keeps one (below), the upgrade
   commands list it as `[keep]`, and only a named `vp commands reset` /
   `vp skills reset` removes one. "Safe" is scoped to exactly that —
   vp's reconcilers and upgrade commands; the generic vault tools
   (`vp_vault_write`, `vp_vault_edit`, `vp_vault_move`,
   `vp_vault_delete`, and `vp vault commit --paths .`) are direct edits
   and reach any tier (`internal/vaultfs/write.go`). What an override of
   a built-in costs is its shadow: it applies to every project and
   misses binary-contract changes (`commands/wrap.md` supplies the
   `expected_sha256` `vp_update_resume` demands), which is why
   `template-drift` reports each one as `Info`. An unedited copy of the
   current version of a built-in, or of any version in `shipped.txt`
   (every version reachable from `1f3bb62`, plus the two rows of tag
   `pre-rebase-501c96e`), is vp's bytes, and is pruned.
4. **Reconcile on `vp config sync`.** The `TemplateTreeReconciler` in
   `Materialize` mode classifies each resource's vault copy by
   provenance — the binary alone, no host-local state
   (`templates.ClassifyVaultCopy`) — and the table below resolves each
   one. Manual copy-back to the vibe-palace source checkout is how an
   override becomes the next release's embedded floor — `vp` cannot
   automate this because at runtime it does not know where the user's
   source checkout lives.

#### The provenance decision table

`templates.ClassifyVaultCopy(relPath, content)` keys `content` by
`templates.ProvenanceKey` — sha256 after one CRLF→LF pass — and returns
**current** when the key is the embedded copy's (`templates.EmbeddedSHA`),
**earlier** when it is a row of the frozen shipped-version manifest for
the same relpath (`templates.ShippedVersion`), and **operator** otherwise
(the zero value, so an unhandled branch keeps the file). It is
relpath-scoped: `wrap.md`'s bytes at `Templates/commands/restart.md` are
operator content. For each embedded resource, with
`K = Templates/<relpath>`, `planMaterialize` plans:

| Vault file | Action | Summary |
|---|---|---|
| absent, not pending | **Unchanged** | `K served from embedded floor` |
| absent, pending removal, HEAD copy current/earlier | **Delete** | `prune K (removed from the worktree before this sync and not committed; the committed copy is …)` |
| absent, pending removal, HEAD copy operator | **Unchanged** | names `vp commands reset NAME` / `vp skills reset NAME` and the `git checkout HEAD -- K` restore |
| not reached directly (`vaultfs.CheckDirectPath` fails) | **Unchanged** | `K is not reached directly (…); kept, never followed` |
| current | **Delete** | `prune K (byte-identical, line endings aside, to the current embedded copy)` |
| earlier | **Delete** | `prune K (matched an earlier shipped version of <rel>; recoverable from vibe-palace history)` |
| operator | **Unchanged** | `K operator override of a built-in (kept)` |
| unreadable (not ENOENT) | Plan error | |

A Delete's `Details` carry `embedded_relpath=`, `provenance=`
(`current`/`earlier`), `vault_sha=` (raw sha256 of the worktree bytes,
empty when absent), `key=` (the `ProvenanceKey` a manifest row matches)
and, on a pending row, `pending=true`. No row plans a Create, an Update
or a prompt, and `applyMaterialize` reports either write kind as an
error rather than executing it: **the Templates reconcile never writes a
template**, and it writes no lock and no `.gitignore` (the Vault
reconciler's locked top-up owns that file). The reconcile's one
destructive operation is the prune, and it removes only bytes provably
vp's. `reconcile.PruneAccepts(action, content)` — `embedded_relpath` set
and the content classifying as current or earlier there — is the one
accept rule; every check below calls it. No `.bak` is written: the bytes
removed are the embedded copy or a shipped version recoverable from
vibe-palace's history. A `.bak` already beside the file is left
byte-for-byte: the bare `<file>.bak` an older binary's overwrite or
upgrade reset wrote, or a `<file>.<sha12>.bak` a named reset wrote. An
earlier-version prune that no commit records prints one line after it
removes the file (`pruned K (earlier shipped version of <rel>; no
backup)`), so the outcome, not only the plan, is the audit record.

Pruning an earlier version on a host with no lock is **new policy**
(ADR-008 amendment, 2026-09-11): a pinned old template is exactly the
stale copy the binary contract forbids, and a deliberate pin belongs at
the project tier or in an edited copy. Restoring a pruned earlier
version into `Templates/` defeats itself — the next sync prunes it again.

Who removes the file depends on how git sees the vault
(`storage.InspectVaultGit`, which looks for a `.git` directory *or*
file at the vault and every directory above it, then asks git):

- **Unversioned vault.** Apply re-checks the path is reached directly,
  re-reads the file and re-applies `PruneAccepts` immediately before the
  removal, then removes it through `vaultfs.Delete` — a compare-and-set
  on the bytes just accepted, under the path's advisory lock — so an
  edit landing in between is kept (`<path> changed since plan; kept`).
- **Git vault** (the top level of its own repository: an ordinary
  clone, a linked worktree or a submodule). The reconciler runs with
  `ExternalPrune`: Apply removes nothing, and `pruneOnGitVault` hands
  the paths to `storage.PruneMirrorsVerifiedWithDowngrade`. **Nothing is removed
  until git has answered.** Under the vault commit lock, after the
  already-ahead reconcile may have moved HEAD, each path is checked: the
  worktree bytes; the index against HEAD (a staged change keeps the
  path); HEAD's copy; and, after a fresh fetch, each remote tip's copy.
  HEAD's and each tip's copy are read **as git would check them out**
  (`gitCheckoutContent`: `git -c filter.<d>.required=true … cat-file
  --filters <rev>:./<rel>`, every configured smudge/process driver
  forced `required`, any stderr or non-zero exit an error), so a
  git-crypt, LFS or other filtered vault is compared like with like, and
  a filter that cannot run defers the path instead of restoring cleaned
  bytes over it. `git ls-tree` first tells "absent" from a git error. A
  remote that cannot be fetched, or whose tracking ref does not resolve,
  defers every tracked prune (`prune deferred: remote not verified`,
  exit 2) — a stale tracking ref is never trusted. A HEAD copy that is
  operator content — the state an old binary's overwrite or an upgrade
  reset leaves — is restored in place with `git checkout HEAD --` (with
  the same forced filter flags), and the file is never removed. A
  remote tip holding operator content keeps the path (`prune deferred —
  pull first, and keep that copy`). Because every remote was just
  fetched (or the prune deferred), a prune commit cannot strand behind,
  or delete in a later merge, an override that was on a remote when the
  sync ran; an override pushed in the seconds between that fetch and
  the push makes the push reject, which is reported (below). The
  committer identity is checked before the first tracked removal. Any
  git error defers the path (kept, printed as a `[Skip]` row) and the
  command exits non-zero. Only then is the file removed, through
  `vaultfs.Delete` with the accepted bytes' SHA as its compare-and-set;
  HEAD is re-read before staging, and removed paths are re-judged (and
  restored) if it moved. The commit message states what was checked and
  names each path's basis (`current embedded copy` / `earlier shipped
  version of <rel>`).

  **Pending removals.** On such a vault `runConfigSync` also reads
  `storage.UncommittedRemovals(vault, "Templates", …)`: tracked built-in
  copies removed from the worktree whose removal is not committed
  (`git ls-files -v --deleted`, dropping assume-unchanged and
  skip-worktree entries, index-only entries and entries whose index
  differs from HEAD), each with HEAD's checkout-form copy. One whose
  committed copy is vp-shipped plans a pending Delete, and the verified
  prune commits it. This replaces the lock-entry retry of the previous
  design: a prune whose commit failed is finished by the next sync. The
  prune commit's grant is **widened, dated 2026-09-11 (operator)**: on a
  vault that is its own repository it may also carry a tracked removal
  already pending in the worktree whose HEAD copy classifies as
  vp-shipped, and the message lists such paths in a paragraph of their
  own ("vp found these removals already pending in the worktree …").
  A pending removal of operator content is never committed.

  **The retired lock.** On the same vaults a `.vibe-palace/templates.lock`
  that is untracked (in neither the index nor HEAD) and not ignored is
  removed (`storage.RetiredTemplatesLock`): it is planned as a Delete
  beside the prunes, so `--dry-run` shows it, and `removeRetiredLock`
  re-checks it and removes it through `vaultfs.Delete`'s compare-and-set
  after the Templates Apply. A tracked or ignored lock, and any lock on
  a non-git, nested or broken vault, is left, and `template-drift`
  reports it as `Info`.
- **Vault nested in another repository** (a project or dotfiles repo
  whose top level is above the vault — `git rev-parse --show-toplevel`
  is not the vault root). That repository is not the vault's: vp never
  fetches, rebases, stages into, commits or pushes it
  (`storage.PruneMirrorsInEnclosingRepo`). It reads HEAD and the index
  and restores a vault path from HEAD exactly as above, and removes an
  untracked mirror; a tracked mirror is kept, with a `[Skip]` row naming
  the enclosing repository. No pending removal is read and no lock is
  removed there.
- **Git vault git cannot read** (`git` not on PATH, a dangling `.git`
  file, "dubious ownership"): every prune is planned as `Skip` (`prune
  deferred`). Without git on PATH the run succeeds; a repository git
  refuses is an error.

What is guaranteed on a git vault, exactly: a file whose committed copy
is operator content is never removed; no file is removed unless every
check passed, including a fresh fetch of every remote; and no
repository other than the vault's own is ever written. A removal can
stay uncommitted only through a stage or commit failure — whose staging
is undone (`git reset -q -- <paths>`), so vp's deletion never sits in
the index as if it were someone else's — or a failed re-check after HEAD
moved. Such a path is printed with its manual
`git -C <vault> checkout HEAD -- <path>` and the command exits non-zero;
the next sync finds the removal pending and commits it. A push the
remote rejects, or a reconcile merge that is refused or aborted, after the
prune commit is reported with the commit, the paths and the rule — a
remote's copy of these paths is operator content, keep it — and the
command exits non-zero. `pruned=N` counts only files this run removed.

The vault commit lock serialises against every vp committer; `storage.Pull`
does not take it, which is safe because a concurrent merge fails on
git's own index guards rather than losing anything. The remaining
unlocked windows are named, not closed: a symlink swapped into a path
between `vaultfs.CheckDirectPath` and `vaultfs.Delete`'s resolve (owned
by `template-tree-raw-vault-writes-bypass-the-lock-funnel`); a non-vp
editor save in the instant between the check and a `git checkout HEAD
--` restore is overwritten; and a `vp vault commit --paths .` racing the
sync can still commit whatever the worktree holds.

**Known limitations** (recorded, not fixed here):

- **Manifest gaps**, all failing toward keep: versions that existed
  only in rebased-away commits, fixups or dirty `make install` builds
  before the boundary; mirrors written by pre-vibe-palace (vibe-vault)
  binaries, whose history starts at `f9f5b17`. Such a copy is kept as
  an override and reported by `template-drift`.
- **Post-boundary copies.** An operator-placed, unedited copy of a
  version released after `1f3bb62` is kept as an override and shown as
  `Info` with its shadow: no vp binary can have written it.
- **A built-in later removed from the corpus.** `planMaterialize` walks
  only the current relpaths, so a stale copy of a removed built-in would
  become a vault-wide command with stale bytes; its manifest rows are
  never consulted. None exists today (the five `commands/vp-*.md`
  predate the first writer); a future corpus removal must decide this
  explicitly.
- **Remote tips read with the worktree's attributes.** `cat-file
  --filters` resolves attributes from the worktree `.gitattributes`,
  never the revision's; a tip whose attributes differ is read with the
  local ones, which fails toward keep (`--attr-source` needs git 2.40
  and is not used).
- **A symlinked `Templates/`.** Everything under a symlinked
  `Templates/` or `Templates/commands` — or, on Windows, a path whose
  letter case or short name differs from the disk — is kept and never
  followed, where the previous release pruned a mirror through the
  link. It fails safe; the shadow remains and `template-drift` reports
  it.
- **Git processes under the lock.** About five git processes run per
  path while the vault commit lock is held (`ls-tree`, `ls-files -s`,
  `cat-file --filters`, plus the remote-tip reads), and the remote fetch
  runs under it too, only when a tracked prune is pending. Batching them
  is left for when prune volume makes it matter.

#### Shipped-version manifest

`internal/templates/shipped.txt` (embedded with its own `//go:embed`)
lists every version of every built-in that a vp binary could ever have
written into a vault, in `sha256sum` format:
`<ProvenanceKey>  <relpath>`, sorted by relpath then key, `#` header
lines allowed. It is **frozen**:

- **The boundary is `1f3bb62`**, the last commit on `main` before
  `a4179a5` removed the last writer of template bytes into a vault (the
  upgrade commands' reset). The writers were TemplateTree materialize
  (`7e5b96d`..`807bae7`), config sync's `o`/`--yes` overwrite (until
  `5aa68ee`), `vp commands upgrade` (`44df171`..`a4179a5`) and
  `vp skills upgrade` (`a7e24c9`..`a4179a5`). After it no vp binary
  writes a template into `Templates/`, so no later version can reach a
  vault from vp.
- **The rows** are every version reachable from `1f3bb62`, plus the two
  rows of tag `pre-rebase-501c96e` (annotated `# extra:` with their blob
  OIDs; the tag was on the `github` remote from 2026-09-11 until the
  operator deleted it on 2026-09-14 — the two rows are pinned by blob
  hash alone and are no longer re-derivable or recoverable from
  published history). The current embedded copy is matched live, not as
  a row. `TestShippedManifestWellFormed` pins the shape and
  `TestShippedManifestIsFrozen` the content hash.
- **It is never regenerated.** A template edit needs nothing beyond the
  Go-embedded copy. A change to the file is a deliberate, reviewed edit
  that updates `frozenManifestSHA256` in the same commit.
- **Line endings.** The root `.gitattributes` keeps
  `internal/templates/**` LF on every checkout, and the parser strips a
  trailing `\r` anyway.

A reviewer reproduces the 172 non-tag rows once, in a full clone with
tags fetched (`git fetch --tags`). The command fails closed — a
revision that does not resolve is a non-zero exit, never zero rows —
and its output equals the file's rows minus the two
`# extra:`-annotated rows, which are carried verbatim and pinned by
blob hash alone (the tag they came from, `pre-rebase-501c96e`, was
deleted from `github` on 2026-09-14 and no longer resolves).

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

The retired `templates.lock` (`<vault>/.vibe-palace/templates.lock`,
TOML) recorded, per host, the embedded SHA a vault file was last written
from. It was host-local in practice — vp's fixed-path committers never
staged it — so a host without it prompted on every override at every
interactive sync, could not recognise a stale copy an old release left,
and on a canonically configured git vault left untracked dirt that made
`vp vault sync` refuse. No vp from this release reads or writes it.

#### Two upgrade entry points

The codebase exposes **two** reconcile surfaces with deliberately
different UX contracts, and `vp init` is neither. A third, named verb is
the only thing that removes an override:

1. **Provenance reconcile** (`vp config sync`) —
   `internal/reconcile/template_tree.go`. Runs the table above over
   every embedded resource (commands + skills): prunes vp-shipped copies,
   keeps overrides, never prompts and never writes a template. (Vault
   split no longer scaffolds a destination at all: it copies into an
   existing, migrated `vp vault init` vault.)
2. **Report** (`vp commands upgrade`, `vp skills upgrade`) —
   `commands.Plan` in `internal/commands/upgrade.go`. Classifies an
   *existing* vault copy with the same `templates.ClassifyVaultCopy` (an
   absent one is `unneeded`, never created) and only reports it: the
   current copy, line endings aside, is `unchanged`; an earlier shipped
   version is `ChangeStale`, listed as `[stale] … an earlier shipped
   version of the built-in; vp config sync prunes it` and not counted as
   kept; an override (`ChangeOverride`) is listed as `[keep]` with the
   reset that removes it. Neither command writes or removes a
   `Templates/` file, in any mode. `vp skills upgrade` is report-only as
   a whole — one line per skill directory unless `--granular` is passed,
   it never reads stdin, and every mode exits 0. `vp commands upgrade`
   still prompts `[a]ccept / [s]kip / [A]ccept-all / [q]uit`, but only
   for vp-owned project files: the command, Grok and skill shims, the
   agent-file blocks, the project `.gitignore` and the hook.
   `--overwrite` accepts those and nothing else. Neither command reaches
   a vault-write sink, so both are registered without the surface gate's
   mutating wrapper.
3. **Named reset** (`vp commands reset NAME...`, `vp skills reset
   NAME...`) — `cmd/vp/template_reset.go` over `commands.Reset` in
   `internal/commands/reset.go`. Removes the named overrides (and any
   named stale copy) so the embedded floor serves them; it never writes
   embedded bytes into `Templates/`. Every name is validated and every
   path checked (`vaultfs.CheckDirectPath`: a symlink in any component
   refuses the whole call) before anything is written. Every copy that
   is not vp-shipped is backed up to a content-named `<file>.<sha12>.bak`
   (`templates.PreserveBackup`, never overwritten) before any removal; a
   mirror or an earlier shipped version needs no backup, and the dry run,
   the report line and the commit message all say so. Each removal is
   compare-and-set on the read bytes through `vaultfs.Delete`. On a
   vault that is its own git repository the removal is committed locally
   through `storage.CommitRemovals`, never pushed; a vault nested in
   another repository is never committed. Both verbs are registered as
   mutating, so the surface gate covers them.

The three converge: path 2 writes nothing, and path 3 removes the file
instead of leaving a mirror, so the next `vp config sync` classifies a
reset path as gone and never restores it. (Before, path 2's
`--overwrite` reset left a mirror that path 1 pruned, or — for a
committed override — restored from HEAD in place, undoing the reset.)

### Bootstrap Context

`vp_bootstrap_context` is the primary entry point for AI context restoration.
A single call returns an **index plus instruments**, not documents. The index is
the head of queue, a ranked session index, the memory index, a KG snapshot and
the command and skill lists. The resume and workflow bodies are fetched through
`resume_uri` / `workflow_uri` with `vp_read_resource`. Among the instruments are
`departed` (the project named has left this vault, so every write for it is
refused) and `project_repo_freshness` (opt-in: present only when the call
passes the `project_repo_path` parameter and that checkout is behind, diverged
from or unverified against its remote), alongside surface,
vault-dirt, vault-staleness, health, audit and friction alerts. The field list
is `BootstrapResult` in `internal/tools/context_tools.go`.

#### The payload is an index (313, PRD §1.9)

**No document body is inlined.** `resume` and `workflow` are not fields of
`BootstrapResult`; they are reached through `resume_uri` and `workflow_uri`, and
`restart.md` Step 2 fetches both on every restart. There is no token budget, no
shed ladder, no workflow digest and no pin/disposable marker vocabulary — all
deleted in `first-principles` Phase 2 (see ADR-009, superseded in full and kept
only as a historical record) — and nothing in the assembly path reads, computes
or compares a payload size.

The two phases are opposite halves of one contract and it is worth keeping them
straight: Phase 2's gate was *nothing may reduce the bodies*; Phase 3's is *no
body is inlined at all*. The live canary asserted the first until 313 and asserts
the second now.

What the payload carries instead:

- **Head of queue**, derived from the task graph — unblocked work, in-progress
  first, then priority, then topological order — each row with its `vibe-palace://task/…`
  handle. `active_task_count` is the whole open backlog, so a count larger than
  the rows is the reader's signal to call `vp_list_tasks`.
- **A session index**, ranked against the head of queue. Default is the
  deterministic `structural` ranker (lexical overlap on the queue's own terms,
  recency as the tie-break). When the process already has a warm embedder and an
  in-memory project index, `semantic` reorders the same session rows from
  `SearchReady` hits with `source_type=session` (iteration and session-note
  chunks densify the corpus for `vp_search` but do not become bootstrap index
  rows). Bootstrap
  never builds an index or forces a lazy ONNX construct — it asks only
  `HasIndex` (`internal/tools/bootstrap_rank.go:249`), so cold paths stay
  `structural` and set `fallback_reason`. Rows carry date, iteration, title, tag
  and a session URI, and no summary body.
- **`ranking`**, the one instrument that is never silent: which ranker ran, the
  head-of-queue slug it ranked against, candidates-versus-returned, and
  `fallback_reason` when semantic could not run without blocking. An ordered
  list that does not say what ordered it is indistinguishable from recency order.
- The memory index, KG snapshot, and the command and skill lists, all of which
  were already indexes rather than bodies.
- **`index_coverage`** per project — `absent`, `stale`, `legacy`, `unbuilt`, `notes`, `partial` or
  `current`, tested in that order, over notes, iterations and the transcript archives (see
  *Host-Local Index*) — so an incomplete search index says so before anyone searches, and a KG
  snapshot for a project whose graph is absent on this host is not a silent zero (ADR-014 decision
  8; see *Host-Local Index*).

The transport contract is unchanged, because the remaining risk is not vp's:

- **`complete`** is the last field of the payload and carries no `omitempty`, so
  it arrives on every whole result and on no cut one. An agent that does not see
  it knows its HOST truncated the result.
- **`resume_uri`, `workflow_uri`, `resume_sha256`** lead the payload — and are now
  the ONLY route to those documents, not merely a recovery path.
- **Instruments before bulk.** Health, vault staleness, friction, ranking and the
  alerts sit in the region a host preview keeps; the condition alerts are silent
  when healthy.

`TestBootstrapLiveVaultStillRestoresASession` asserts this against the real vault,
including both negative halves: no `budget` / `shed_core` / `max_tokens` on the
wire, and no `resume` / `workflow` key — plus a distinctive line of the live
resume absent from the whole marshalled payload, since a key check alone would
pass a renamed field.

#### Wire order and the `complete` sentinel (transport contract)

`ranking` above is vp's report about *its own* ordering. It says nothing
about what the **host** delivered, and hosts truncate. Measured 2026-08-12:
a Grok pane cut three MCP results of 60.3 KB, 53.4 KB and 32.7 KB at exactly
19.5 KiB each — a **flat** cap, not a ratio — and Claude Code performs the same
truncation without narrating it.

Two properties of `BootstrapResult` (`internal/tools/context_tools.go`) make a
cut payload survivable, and **both are enforced by field declaration order**:

- **Instruments and recovery handles lead.** `encoding/json` emits struct
  fields in declaration order, and nothing on the response path re-serializes
  through a `map` (`mcplib.NewToolResultJSON` marshals the value directly;
  `vp inject` encodes it directly), so declaration order *is* wire order *is*
  cut order. `project`, `resume_uri`, `workflow_uri`,
  `resume_sha256`, `active_task_count`, `ranking`, the compact alerts and
  `post_bootstrap_instructions` are declared **before** the index
  (`head_of_queue`, `recent_sessions`, …). `head_of_queue` carries no
  `omitempty` so it marks that boundary on every payload, including an empty
  project's. Every index row is re-fetchable through its own URI. `resume_sha256` sits with the URIs
  rather than beside `resume` because an agent rehydrating from the URI needs
  the digest to CAS-verify what it pulled.
- **`complete: true` is the last field, and carries no `omitempty`.** Its
  ABSENCE is the signal: present ⇒ every byte arrived; absent ⇒ the transport
  cut the payload, whatever the host did or did not say. It asserts nothing
  about *content* — which rows were selected and by what is reported by
  `ranking` — only that this JSON document is the whole document vp emitted. Reordering alone could not do
  this job: it makes a cut *recoverable* on a host that announces the cut,
  but leaves "vp sent none" and "it was cut off" indistinguishable on one that
  does not. Per ADR-006 the agent DERIVES its delivery state rather than being
  asked in prose to remember it.

**Declaring any new field after `complete` re-opens the hole**, and appending
to the end of a struct is exactly how it will be broken.
`TestBootstrapCompleteSentinelAlwaysEmitted` asserts the last declared field
via reflection for that reason; `TestBootstrapTruncatedPrefixIsDetectable` and
the live-vault canary assert the property on real marshalled bytes.

##### The same contract across the rest of the surface

The cap is a property of the **host**, not of bootstrap, so it applies to every
tool result. The rule: **every result that can be large leads with its recovery
handle and ends with a terminal `complete`.** The survey figures and struct
lists below are a dated record (2026-08-12). Tools added since, such as copy,
split, merge and the palace-query tools, follow the rule but are not in the
lists; `grep -ln 'json:"complete"' internal/tools/*.go` gives the current set.
`vp_vault_project_delete` does not follow it yet: its plan lists every tracked
file before its digest and has no terminal `complete` (`storage.DeletePlan`).
A survey against the live vault
(`survey-mcp-surface-for-results-over-the-host-inline-cap`, 2026-08-12) measured
all 47 non-mutating tools and found **19 over the 19,968-byte cap**, up to
`vp_vault_read` at 189×. It also found that **every URI escape hatch on the
surface was declared *after* the payload it rescues** — reachable exactly when
it was not needed and gone exactly when it was. `vp_get_task` returned 192,060
bytes with `content_uri` at byte **191,956**, 172 KB past the cut. Where a hatch
appeared to work it worked by coincidence: the body happened to fit.

Two consequences, both mechanical, both landed:

- **Handles lead their bulk.** `content_uri`/`content_size` precede `content`
  in `getTaskResult` and `getLearningResult`; `doctrine_uri` precedes the
  embedded body in `doctrineResult` (which also fixes `vp_manual`, where it
  nested behind 56 KB of tool inventory); `session_uri` precedes `body`;
  `content_uri` precedes `content` for `vp_get_command`/`vp_get_skill`; and
  `resolveResult` now declares `source`/`sha256` **above** `content`, so the
  digest an agent needs to compare-and-set after re-paging is on the near side
  of the cut. `vp_read_resource` needed no change — its
  `uri, mime_type, offset, length, total_size, eof, content` layout is where
  the pattern came from.
- **Three URIs that were minted and never emitted are now emitted.**
  `mcp.ResumeURI`, `mcp.SessionURI` and `mcp.CommandURI` (plus `mcp.SkillURI`,
  which shares a handler) all existed, had registered resource templates and
  were served by `vp_read_resource` — and no tool response ever handed one to a
  client. `internal/sourceaudit` had been carrying all four as accepted debt;
  emitting them removed the entries. `mcp.KnowledgeURI` stays uninvoked on
  purpose: it addresses a Knowledge *markdown file* while `vp_get_knowledge`
  returns *KG triples*, so emitting it would be a pointer to a different
  document, not a recovery handle.

The terminal `complete` sentinel now ends `getTaskResult`, `resumeResult`,
`getResourceResult`, `sessionDetailResult`, `readResourceResult`,
`ManualResult`, `ProjectContext`, `knowledgeResult`, `kgTripleListResult` and
`vaultListResult` — every result struct in `internal/tools` for a tool measured
over the cap. Two structural exceptions, both deliberate: `doctrineResult`
carries none because it **nests** inside `ManualResult`, and a sentinel in the
middle of a document survives the cut it exists to detect; `getLearningResult`
carries none because it measures 1,237 bytes, two orders under the cap.

`complete` on `vp_read_resource` is **not** a duplicate of `eof`: `eof` is about
the *resource* (you reached its end), `complete` is about the *document* (every
byte of this page reached you). A page can be `eof: true` and still arrive cut.

**Not fixed here, and needing a paging design rather than a field move:**
`vp_get_knowledge`, `vp_get_project_context`, `vp_kg_query`/`vp_kg_timeline`,
`vp_vault_list` and `vp_manual` have a sentinel but still **no hatch** — no URI
addresses a result computed per call. `vp_search`, `vp_search_cross_project`,
`vp_search_sessions` return bare JSON **arrays** and `vp_list_tasks` returns a
`map`, whose keys `encoding/json` sorts alphabetically — neither shape can host a
*terminal* field at all, so both need a wrapper struct before a sentinel means
anything. `vp_vault_read`, `vp_health` and `vp_collect_wrap_state` return domain
structs owned by other packages and shared with the CLI. `vp_read_resource`'s
`limit` remains unclamped.

### Served doctrine (ADR-008 Phase 1)

The generic agent operating manual — the doctrine — is embedded in the binary
at `internal/templates/templates/doctrine.md` and served **on demand** via the
`vp_get_doctrine` MCP tool (`context_query_tools.go`) and the
`vibe-palace://doctrine/<project>` resource. It is deliberately **not** part of
the bootstrap payload: the doctrine is generic and stable, and the payload is an
index of what is specific to THIS project and this moment (PRD §1.9). The
embedded `workflow.md` template is correspondingly **thin** — project-specific
patterns plus a pointer at the doctrine — rather than carrying the full manual
inline. Resolution follows the normal precedence tiers, so a project may
override the embedded doctrine with its own copy; the tool's result always
carries the `doctrine_uri` so a host whose channel truncates the inline body
can page the full text.

### Onboarding a project (`internal/onboard`)

`internal/onboard.Steps()` is the single definition of what `vp init` MEANS,
and both surfaces that onboard a project — the `vp init` CLI command and the
`vp_init` MCP tool — drive that one table. Neither owns a step list of its own,
so "the MCP tool does less than the CLI" can only ever be a statement about
scope, never an accident of two code paths drifting.

Each `Step` carries two tags and nothing else that a surface may reason about:

- **`Side`** — which surface the step WRITES: the vault, the project working
  tree, or the RUNNING host's user globals (`~/.claude/settings.json`). Over
  MCP the last of those is the *server operator's* machine, never the caller's.
- **`ReadsHostGlobal`** — the step writes some other side, but decides WHAT to
  write by inspecting the running host's home. `command-shims` is the whole
  reason the tag exists: it emits project-local shims, but skips a host whose
  user-global command surface is already healthy.

**Those two tags are the only legitimate reasons a surface may skip a step.**
A surface declares a `Scope` over `Side`, and every step the scope excludes is
reported as an `Omission` carrying a `Reason` and a `Remedy` that names the
verbatim command, the host it must run on, and the artifact that is missing.
`onboard.Run` refuses to return a `Result` in which any step is neither an
outcome nor an omission — a surface cannot quietly do less and report success.
There is deliberately no mechanism for a caller to declare a step
already-done: `vp init` once had exactly that (a marker gate that fired on the
presence of `.vibe-palace.toml`), and it is what left every MCP-initialized
project permanently half-scaffolded.

**A narrowed scope pins `Complete` false, so it is not a failure signal.**
`Result.Complete` answers "did this surface do everything the tool can do", and
`ScopeForMCP` always omits `hook-wiring` and `command-shims` — so over MCP the
answer is a constant `false` whatever happened, and `vp_init`'s `status` is
correspondingly always `"partial"`. `Result.Failed` (and its one-bit form
`Result.OK()`) answers the question a caller actually has — "did any step I was
allowed to run go wrong" — and is what the `vp_init` result exposes as `ok` and
`failed[]`. Deriving a verdict from `Complete` or `Omitted` instead
reconstructs the unconditional `{"status": "initialized"}` this tool's rewrite
exists to delete.

**`vp commands upgrade`, `vp skills upgrade` and the reset verbs are NOT
callers of `internal/onboard`.** The upgrade commands are a separate
*upgrade* policy layered over the shared writers in `internal/shims` and
`internal/commands` — not over the `Templates/` reconcile
(`reconcile.NewTemplateTree` rooted at `Templates`), which only `vp config sync`
drives: onboarding reconciles toward the
current schema and is additive, while `vp commands upgrade` presents
changes interactively and may REMOVE a stale shim. Neither upgrade command
changes a vault `Templates/` file; only a named `vp commands reset` /
`vp skills reset` removes an override of a built-in. Onboarding never
writes, prunes or reconciles `Templates/` (its shim steps only read it
through the resolver), and ends with an advisory naming each command
against what it actually owns — stale shims are `vp commands upgrade`'s;
`Templates/commands` resets are `vp commands reset`'s; `Templates/skills`
resets are `vp skills reset`'s.

### Commands and Skills

**Commands** are instructions for the AI to execute immediately (e.g.,
"capture this session"). **Skills** are behavioral guidelines applied
throughout a session (e.g., "pair programming mode"). Both resolve via
the 5-tier palace-scoped precedence system (room > wing > project > vault >
embedded) and can be listed or invoked by name. When wing/room are not
specified, resolution falls back to the 3-tier project > vault > embedded
chain.

### Shim system (`internal/shims/`)

vibe-palace emits native shim files into the editor's own surfaces so
users can invoke commands and skills without leaving the tool they
already know. One `TargetKind` enum (`internal/shims/target.go`) has four
kinds — `ClaudeCommand`, `ClaudeSkill`, `CursorRule`, `GrokSkill` — all
sharing the managed-hash atomic-write protocol (tmp + fsync + rename
with a `<!-- vibe-palace:shim v=N sha=7hex -->` … `<!-- vibe-palace:shim-end -->`
region that identifies vibe-palace-owned content; files without the
marker are "custom" and never touched).

| Target          | Location                             | Body                                           |
|-----------------|--------------------------------------|------------------------------------------------|
| `ClaudeCommand` | `.claude/commands/vpc-<name>.md`     | Delegates to `vp_cmd` MCP tool                 |
| `ClaudeSkill`   | `.claude/skills/vps-<name>/SKILL.md` | Delegates to `vp_skill`; teaches additive-stack contract; `vp skills show` CLI fallback when `vp_skill` cannot be loaded |
| `CursorRule`    | `.cursor/rules/vps-<name>.mdc`       | Delegates to `vp_skill`, with a `vp skills show` CLI fallback when `vp_skill` cannot be loaded |
| `GrokSkill`     | `.grok/skills/vps-<name>/SKILL.md`, and the `/vpc` hub at `.grok/skills/vpc/SKILL.md` | Persona: as `ClaudeSkill`, in Grok frontmatter. Hub: lists and dispatches commands through `vp_cmd`; no fallback |

Two further emission paths reuse the same renderers. `PlanGrokCommands`
(`plan.go`) writes command shims under `.grok/plugins/vibe-palace/commands/`
with bodies byte-identical to the Claude ones. `InstallGlobalSurfaces`
(`user_install.go`) writes user-global command and skill shims into host
plugin trees under `$HOME` (the Claude Code plugin and its cache, and
`~/.grok/plugins/vibe-palace`). `vp mcp install` drives it through
`internal/plugin` and `internal/mcphost`, and the `host-surfaces` check row
reports on those trees.

- **Plan / Apply** for commands (`Plan` + `Apply`) and for skill-class
  targets (`PlanSkills` + `ApplySkills`) each classify on-disk files as
  New / Modified / Unchanged / Stale / Custom and compute the minimal
  rewrite set. A skill shim's `sha=` is taken over the file as rendered
  with the sha blanked (prefixed by the target kind), so every rendered
  byte keys it: any change to what a renderer writes — a description, the
  fallback text, the hub body — re-renders the affected shims once, with
  no version bump. `skillShimVersion` versions only the marker format and
  the `ScanShim` contract. Command shims (`ClaudeCommand`) are still keyed
  on render inputs (name, brief, project, argument hint, template
  version), so an edit to their static text does not reach an existing
  shim.
- **Cursor detection** (`shims.CursorPresent`) is strict: emission is
  triggered by `.cursor/rules/` (primary) or `.cursor/` (weaker) at the
  project root. A flat `.cursorrules` file is deliberately **not** a
  trigger — `agentfile.Detect()` already owns that surface, and
  bootstrapping `.cursor/rules/` from a `.cursorrules`-only project
  would presume a Cursor directory surface the user never opted into.
- **Stale removal** is opt-in (`ApplyOptions.AllowStaleRemoval`) so
  `vp init` is strictly additive; interactive upgrade flows pass the
  flag after the user accepts per-file.

### Skills pipeline: resolver → shims → upgrade

The three preceding subsections each describe one stage of the skills
pipeline. Stitched together:

**Resolver.** A skill is a directory — `skills/<name>/SKILL.md` plus
an optional `references/*.md` tree — and every file inside is
resolved independently through the same 5-tier palace-scoped
precedence as commands (room > wing > project > vault > embedded).
`ResolveSkillDir` locates the tier that owns `SKILL.md` (the persona
entry point); `ResolveSkillSection` walks each reference through the
full tier chain on its own, so a project can override the persona
while inheriting every reference — or vice versa — without having to
clone the whole directory. The `SkillFrontmatter` parser (see
`doc/COMMANDS-AND-SKILLS.md`) reads the `name`, `description`,
`paths` and `lifetime` fields. The shim renderers derive each shim's
one-line label from `description`; the description itself never
reaches a host.

**Shims.** On top of the resolver sits `internal/shims/`, which
emits native artifacts into the editors that expose a first-class
skill surface. `ClaudeSkill` writes
`.claude/skills/vps-<name>/SKILL.md` — a short delegation to
`vp_skill` wrapped in the managed-hash shim marker — so Claude Code
offers `/vps-<name>`. The shim sets `disable-model-invocation: true`,
so on Claude Code only the user can invoke it. Every persona shim's
description is a `Vibe-palace skill — …` label rather than the skill's
trigger text. On Cursor and Grok that label is the only safeguard: it
makes the host matching the conversation against the description
unlikely but does not rule it out, and Cursor still auto-attaches a
rule whose `globs` (rendered from the skill's `paths:`) match.
`CursorRule` writes
`.cursor/rules/vps-<name>.mdc` (only when `.cursor/rules/` or
`.cursor/` already exists at the project root), giving Cursor's
Rules panel a native entry, and `GrokSkill` writes the same persona
shims under `.grok/skills/`. Every persona shim ends with a
`vp skills show` fallback for MCP-less setups: when `vp_skill` is not
in the agent's tool list and cannot be loaded after a search, it runs
`vp skills show <name>` from the project directory, which resolves the
same tiers as `vp_skill` and prints references with `--section`. No
shim names a vault file or any other host path. Every shim carries a
`sha=` token in its marker so drift detection is exact — taken over the
rendered bytes for skill shims — and files without the marker are
"custom" and never touched. Editors without a native surface rely on
the managed-block trigger phrase (`vps-<name>`) plus `vp_skill` over
MCP, which is the universal fallback documented in
`doc/verify-skill-delivery.md`.

**Upgrade.** Skills flow through both upgrade entry points
described above. The provenance reconcile (`vp config sync`) reconciles
vault skill overrides under `<vault>/Templates/skills/` override-only:
it prunes a copy of the current or an earlier shipped version of a
skill file and keeps every other copy, deciding by the bytes alone
(`templates.ClassifyVaultCopy`). The report (`vp skills upgrade`)
classifies an existing vault copy the same way and only reports: one
`[keep]` line per skill directory with an override, naming the files
that differ (one line per file with `--granular`), and one `[stale]`
line per earlier shipped version. It writes nothing and
prompts for nothing. `vp skills reset NAME` — a skill, meaning every
built-in file under it, or one file such as `chair/references/x.md` —
removes an override on request, keeping a content-named backup. The
shim side is kept in lockstep via `vp commands upgrade`'s `PlanSkills` /
`ApplySkills` pair, which re-renders `.claude/skills/` and
`.cursor/rules/` entries whenever the SHA-token inputs change. The
resolver is the source of truth, and the shims are the IDE-native
surfaces. Neither upgrade command commits to the vault repo; `vp config
sync` commits exactly the deletions it prunes — on a vault that is its
own repository, including a removal of vp-shipped bytes it found pending
in the worktree — and a named reset
commits exactly the removals it made, locally, on a vault that is its
own repository.

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
    Dimensions() (int, error)
    Close() error
}
```

Properties:
- 384-dimensional vectors, L2-normalized (cosine = dot product)
- ~90MB model, downloaded on first use, cached at `{vault}/palace/.local/models/`
- Single embed: ~66ms; batch of 32: ~290ms; 17MB binary contribution

### Host-Local Index

The search index is compiled on each host from tracked vault artifacts — session notes,
`iterations.md` and transcript archives — the way a build compiles a binary from source. It lives
under the already-ignored `palace/.local/` (`internal/storage/git.go:31`) and git never carries it
(ADR-014 decisions 1, 2):

```
palace/.local/index/{project}/
├── hnsw.idx            # HNSW graph and its graph fingerprint (library version,
│                       #   dims, M, EfSearch), in one envelope (once HNSW lands)
├── chunks.jsonl        # chunk text and metadata (wing, room, hall, source)
├── kg/                 # extracted triples and entities
├── ledger.jsonl        # per session or import batch: live source, chunk count,
│                       #   UTC start day, failures, generation; the baseline set
├── chunks.fingerprint  # indexer version, chunker, room-keyword hash, extractor
└── completeness.json   # what is built, and the persistent stale flag
palace/.local/index/.generation/{project}
                        # the store's change counter; outside {project}/, so a
                        #   discard never resets it
```

In v10.2.0 the directory holds no `hnsw.idx`: the runtime index is brute force,
built in memory from the chunk store, and the graph file arrives with the HNSW
children (see *Vector Index*). The graph fingerprint has no file of its own: it
lives inside `hnsw.idx`'s checksummed envelope, so the graph and the record of
how it was built are always replaced together.

Wings, halls and rooms are classification metadata on chunks, not directories
git has to carry. The ingest ledger lives here, no longer at
`palace/<p>/ingested-archives.jsonl` (`internal/storage/ingested_archives.go:21-25`).
A new binary deletes a legacy ledger when no tracked drawer is left under
`palace/<p>/drawers/`, or when the vault carries the migration marker, so a
surviving ledger cannot make the rebuild skip archives it never indexed. The
deletion is never keyed on the host-local store being absent, which would
delete every host's ledger before capture is redirected (ADR-014 decision 2).

**The ledger's baseline is a set, not a time.** When a host's ledger is created, it records the
`source_sha256` of every tracked archive present at that moment, leaving out the archive named by
the trigger that created the ledger (the one the hook or `vp_capture_session` just created). That
set is the historical backlog: automatic triggers ingest every pending archive that is not in the
set, and no clock or date is involved. Copy (including `copy --as`) and import add the archives they bring in to the
set explicitly, on the host that runs the command, and only when that host already has a ledger for
the project; a ledger created later records those archives anyway. Other hosts receive them by pull,
outside their own sets, and ingest them automatically within each run's budget. Copy adds
them after the copy is published (`AddIncomingArchivesToBaseline`), and a failed addition warns and
never fails the command; import adds the archives a
vibevault import writes (`importers-write-the-frozen-tracked-corpus`). A completed
`vp index rebuild` empties the set. The set lives in the ledger file and survives every rewrite of
it; a discard of the ledger recreates it with a fresh set; deleting a legacy ledger does not touch
the new one. The set and its API belong to `host-local-index-store-ledger-and-fingerprint`
(ADR-014 decisions 2, 7).

**`completeness.json`** records, per project, each tier that has been built
with the count of sources it was built from; the `chunks.fingerprint` and
embed-cache fingerprint it was built under; and the `stale` flag with its
reason. The graph fingerprint is not recorded there: its one copy is inside
`hnsw.idx`. "Built" means recorded there, never
inferred from an in-memory index or from a file being present, so a fresh
process reads the same coverage as the process that built the tiers (ADR-014
decision 2).

**Two fingerprints** (ADR-014 decision 3), on the embed cache's pattern. `chunks.fingerprint`
records *what* is indexed. The graph fingerprint (once HNSW lands) records *how*, inside
`hnsw.idx`'s envelope: a mismatch rebuilds only the graph, with no embedding. The next run of the
ingester or of `vp index rebuild` replaces `hnsw.idx`, built from the cached vectors and the local
chunks, so changing `EfSearch` discards no chunks and embeds nothing. The ingester and the rebuild
driver reach the graph through a seam, since both are upstream of
`hnsw-graph-file-envelope-and-warm-start`: `pending-archive-ingester-and-per-archive-commit-step`
declares `ingest.GraphHealer`, which returns `ingest.HealResult{Rebuilt, Deleted, Reason}`; the
HNSW child implements it; the ingester and `explicit-resumable-index-rebuild-with-disk-watchdog`
call it at the end of a run. In v10.2.0 it is nil, and they skip the call. A corrupt `hnsw.idx` (a
bad checksum, or a structure that fails the load-time walk) is deleted under the index commit lock
(`Tx.DeleteGraph`), and the project answers from brute force until a converting process writes a
new file; that is not a store discard and sets no `stale`. A graph fingerprint mismatch
is not a `stale` reason: `stale` is set only when vectors are missing, and then for the
missing-vector reason. Neither stamps a tracked file, gates a vault write, or appears in a tool's
input. A mismatch means "present and different": a **missing** fingerprint means "not built"
(coverage `unbuilt`), never a mismatch and never `stale`. At `a32a2d4` the embed cache treats a
missing sidecar as a mismatch (`internal/search/cache.go:184-202`); the index does not. An
embed-cache directory with no sidecar and no vectors is not built, and the first build writes the
sidecar. One with vectors but no sidecar cannot be attributed to a regime, so it counts as a
mismatch: the project is marked `stale` for a fingerprint reason, its cache reads as all misses, and
the next `vp index rebuild` removes those vectors and writes the sidecar. A `chunks.fingerprint` or
embed-cache mismatch found on the search path or by the ingester sets the persistent `stale` flag
with a fingerprint reason (decision 8); a graph fingerprint mismatch does not. The search path then
embeds only what the tier table below allows: notes and iterations and, on a vault without the
marker, the tracked drawers. It never re-embeds a chunk-store vector or an archive, and until the
rebuild search keeps answering from what is there. The pending-archive ingester does not run on a
project that is `stale` for a fingerprint reason: it exits for that project, and the coverage reason
names `vp index rebuild`. Only `vp index rebuild` discards, and only what the mismatched fingerprint
covers: a `chunks.fingerprint` mismatch discards that project's chunks, extracted KG, ledger and
(once HNSW lands) graph; a graph fingerprint mismatch replaces only the graph, from cached vectors,
and the ingester may do it too; an embed-cache mismatch, or vectors with no sidecar, leaves chunks
and ledger alone, and the rebuild removes the old vectors and re-embeds. The rebuild takes the index
commit lock once for the discard, then once for each archive's commit step, like the ingester; it
embeds outside the lock, and on completion clears `stale`. The flag's other reason, missing vectors
for chunks of ledgered sources, is also cleared by the ingester's repair pass once no such miss
remains (ADR-014 decision 3).

**An embedder change** is caught by a third fingerprint, the embed cache's own
(`internal/search/cache.go:183-235`). At `a32a2d4` a mismatch there removes
every vector of the old regime at once (`:203-218`); in v10.2.0 the cache never
discards on its own. A mismatch sets `stale`, and until `vp index rebuild`
discards the old vectors the project's cache reads as all misses and refuses
`Put`. The fingerprint records the model, the behaviour version and
`max_seq_len` (`internal/embedder/fingerprint.go`). The **default** released
binary is zero-CGO pure-Go (`BackendGo`, hugot), and its fingerprint **omits**
the backend field, so a routine upgrade of the default binary never invalidates a
cache. v10.2.0 also ships an **optional** native ONNX-Runtime backend
(`BackendORT`), off by default and reachable only in a binary built with
`-tags ORT` (`CGO_ENABLED=1`; `internal/embedder/backend.go`); only that regime
appends ` backend=ort` to the fingerprint (`FingerprintBackend`), giving
ORT-produced vectors their own namespace so a Go cache never accepts them, and
vice versa. The embed-cache fingerprint is not part of `chunks.fingerprint`, so
after an embedder change the chunk store survives with no vectors. On the search path a changed
(present and different) embed-cache fingerprint marks the project `stale` like
a `chunks.fingerprint` mismatch: notes and iterations are
re-embedded, and the host-local chunks drop out of answers until
`vp index rebuild`. On an unmigrated or reverted vault the glide tier is the
exception: the first search after the change re-embeds every tracked drawer the
cache misses, which on a large project can take hours (decision 7; see *Index
Construction*). An index therefore never mixes vectors from two embedding
regimes; an embedder change without a rebuild gives a narrower answer and a
`stale` reading, never wrong results (ADR-014 decision 3).

**The index is built from archives only** (ADR-014 decision 7).
`vp_capture_session` indexes none of the transcript text it is given: its
`transcript` parameter only creates an archive on a host without a hook
(`archive_transcript`), and a session that never gets an archive is searchable
through its note, not its transcript.

**One pending-archive ingester** brings archives into this store (ADR-014 decision 7). Each run is
one idempotent, ledger-driven pass over the **pending** archives — chunking, classifying, embedding
and extracting their triples. An archive is pending when its session is absent from the ledger, or
the ledger records a different `source_sha256` for that session; a pending re-archive of a ledgered
session is a supersede (below). This matters because a PreCompact archive and the SessionEnd archive
of the same session on the same day share one path, and the second overwrites the first
(`internal/archive/archive.go:181-184`). That one pass covers the hook's archives (SessionEnd and
PreCompact), archives pulled from other hosts, and inline archives from hook-less hosts.

- **Scope of an automatic run.** An automatic trigger ingests every pending
  archive that is not in the host's baseline set. No clock or date is involved,
  so clock skew, offline hosts and same-day archives cannot cause a skip. The
  baseline set is the historical backlog, which only an explicit
  `vp index rebuild` clears; on a host with an empty ledger it is the whole
  history. While the backlog is not empty, coverage reads `partial` and names it.
- **The per-run budget** is counted in archives per run, with a wall-clock cap
  as a backstop; both defaults are set by measurement in
  `pending-archive-ingester-and-per-archive-commit-step`.
  Archives are taken newest first, and the repair of missing vectors counts
  against the same budget. Coverage's reason names separately the pending
  archives (outside the baseline set, beyond this run's budget) and the backlog
  (the baseline set).
- **Arguments.** The trigger passes the vault root and the one project slug it resolved (the hook
  from its own vault resolution, the pull from the vault it pulled), and, from the hook and
  `vp_capture_session`, the `source_sha256` of the archive just created; a pull, a clone and
  `vp mcp` startup name none. When `vp mcp` starts with no project it can resolve, it passes the
  vault's first project in slug order; the run then reaches every other project with pending
  archives as usual. The ingester resolves nothing on its own. One run processes the
  triggering project first, then every other project of that vault with pending archives, within the
  budget.
- **Unmigrated vaults.** The ingester runs whether or not the vault carries the
  marker: from install on, v9 capture writes no drawers, so on an unmigrated
  vault a new session's transcript is searchable only through the host-local
  store. Search on a vault without the marker reads both the tracked drawers
  (the glide path) and the host-local chunks, deduplicated by a wide content
  hash: a host-local chunk id is that hash, and the legacy 32-bit drawer id
  (`internal/storage/drawers.go:62-67`) is never used to match. The coverage
  reason under `legacy` carries the ingester's progress.
- **Reading an archive.** The ingester reads the whole archive file, verifies
  its hash against the manifest and closes it before embedding, so it never
  records one version's bytes under another's hash, and on Windows never holds
  the file open while the hook renames a newer archive into place. A hash
  mismatch counts as a failure for that archive (below).
- **Embedding happens outside the index commit lock.** The ingester reads the
  archive, chunks, classifies, extracts and embeds without any lock, and takes
  the index commit lock only to write what it has already computed.
- **Write order for each archive.** Each step is durable before the next: (1) the vectors, each
  written atomically (temp file, fsync, rename); (2) then the chunks, appended and fsynced; (3) then
  the local KG records, appended and fsynced; (4) then the ledger entry, which records the archive's
  `source_sha256` and its chunk count (the number of distinct chunk ids the archive owns). A killed
  run leaves that archive out of the ledger and the next run resumes it; chunks are deduplicated by
  id, so a resumed run leaves no duplicates.
- **Visibility.** Search loads only chunks, and KG readers load only KG
  records, whose source is in the ledger: an archive, or an import batch. A
  mempalace import has no archive, so the importer ledgers each batch under its
  import batch id with its chunk count, and the batch's chunks and KG records
  record that id as their owner. A batch's start day is the earliest drawer
  `filed_at` in the export, else `2000-01-01`, and the ledger's batch record
  marks it `start_day_source: "import"`. A batch id never equals a session id:
  the store refuses to commit a batch whose id does. A batch is not a session,
  so coverage's session counts ignore it. Decision chunks belong to the notes
  tier: each is owned by
  its session note, never by an archive or a batch, and is visible whenever its
  note is. A miss on a chunk whose source is not yet ledgered never sets
  `stale`; a resumed run deduplicates the KG records it rewrites.
- **Repair.** Within the per-run budget, the ingester repairs ledgered archives:
  it re-embeds missing vectors (after a crash on a host whose filesystem lost
  unsynced writes, say, or after an embed-cache wipe), and it compares the
  ledger's chunk count with the number of chunks that record this archive as
  an owner and re-ingests the archive on a shortfall. It re-embeds an import
  batch's missing vectors too, but only reports a batch's chunk-count
  shortfall, because only re-running the import restores the chunks. That
  repair clears a `stale` flag whose reason is missing vectors
  once no such miss remains.
- **Torn lines.** Readers tolerate a torn trailing JSONL line, and a writer
  truncates a torn trailing line, under the index commit lock, before it
  appends.
- **Failure.** A failure is non-fatal: the ingester logs a warning to `vp.log`, records a failure
  count for that archive's `source_sha256` in the ledger, and moves on. A failure record is not an
  ingest: it never makes a session ledgered for visibility, pending or coverage. Automatic runs skip
  an archive after N failures (N set by `pending-archive-ingester-and-per-archive-commit-step`)
  until an explicit rebuild.
- **When a run ends.** A run ends when its budget (archive count or wall-clock
  cap) is spent, or when a rescan finds no pending archive that this run has
  not already attempted. A failing archive is retried by the next run, never by
  the same one, so the index run lock is never held indefinitely.
- **Re-archived sessions: supersede.** When a session is archived again (at PreCompact and then at
  SessionEnd, say), the newer archive supersedes the older one. Each chunk, and each KG record,
  records the archives that own it, keyed by each archive's `source_sha256`, never by its path, and
  superseding removes the older archive's ownership rather than deleting by id. A chunk is deleted
  only when no archive owns it any more, and the older archive's KG records go the same way. The
  ledger is keyed by session and records the live archive and a generation number. The supersede
  runs as commit steps under the index commit lock, in this order: (1) mark the session's ledger
  entry as superseding; (2) add the newer archive's vectors, chunks, KG records and ownership, then
  remove the older archive's ownership, deleting every chunk and KG record that no archive owns any
  more, each file rewritten atomically; (3) record the new live archive and bump the generation. A
  crash leaves the session pending, never "done with half its chunks", and readers detect the
  rewrite through the generation. A supersede that races an ingest of the older archive in another
  run waits for the index commit lock and re-checks ownership there. An older archive met after the
  newer one is recorded as superseded and never ingested. Coverage counts sessions with a live
  archive, not archive files.
- **Chunk ids are wide content hashes.** A chunk id in the host-local store is a hash of the chunk
  content alone (wing and room excluded), at least 128 bits, not the 32-bit drawer id
  (`internal/storage/drawers.go:62-67`, `md5(wing+content)[:8]`), so equal ids mean equal content.
  Legacy tracked drawers keep their ids. The change is covered by `chunks.fingerprint`.
- **Two locks.** Both are `internal/vaultlock` locks on a named file, flock on
  POSIX and `LockFileEx` on Windows. At `a32a2d4` only `TryAcquireFile`
  (`internal/vaultlock/vaultlock.go:157`) locks a named path; `Acquire` and
  `AcquireWithTimeout` (`:184`) lock a hashed sidecar under `.vp-locks/`
  (`:215-237`). The timed form on a named path is
  `vaultlock.AcquireFileWithTimeout(lockPath, timeout)`: it runs
  `AcquireWithTimeout`'s poll loop over `TryAcquireFile`'s named-file open and
  returns `ErrLockWaitTimeout` (`:44`) at the deadline. A context form,
  `AcquireFileContext`, serves the ingester and the rebuild. Both belong to
  `host-local-index-store-ledger-and-fingerprint`, and the caller creates
  `palace/.local/locks/`. Both locks
  live under the host-local `palace/.local/locks/` of the vault they guard, so
  both are per host, per vault. The OS releases a lock when its holder dies (on
  Windows, possibly only after the system frees the handle); neither is a
  pidfile, and neither lives inside `index/<p>/`, which a discard deletes.
  - The **index run lock**, one per host per vault, is non-blocking and held for
    a whole ingest run or a whole `vp index rebuild` run. A second ingest
    trigger that finds it held exits at once; `vp index rebuild` that finds it
    held also exits at once, with a message naming the holder. Lock files are
    empty, so after acquiring the lock the holder writes
    `palace/.local/locks/index-run.holder` (pid, kind — ingest, rebuild or lifecycle —
    project and start time). It is advisory, since after a kill or a pid reuse
    it can be stale; a failed try-lock reads it to name the holder, and the
    holder removes it just before it releases the lock. A status probe —
    `vp index status`, `vp_index_status` and bootstrap's coverage — never takes
    the run lock: it reads the holder record and reports a run in progress only
    while the pid it names is alive. A try-lock there would hold the run lock
    for a moment, and a trigger that collided with it would exit and be lost,
    since the no-lost-trigger rule below covers only a real holder's rescan. The
    probe's answer is advisory, as the record is; only a run takes a try-lock.
    `vp_refresh_index` checks the index run lock before it spawns the way a
    status probe does: it reads the holder record and checks that the pid it
    names is alive, never taking the lock, and if a run is in progress it
    returns a refusal to its caller. A stale record can let a spawn through; the
    spawned rebuild then takes the lock itself, or exits naming the holder.
    Holding the lock bounds embedding to one process, and
    one loaded model, per vault on the host; a host serving two vaults may run
    two. Before releasing the lock the holder rescans for archives that arrived
    during the run; after releasing it, the holder checks once more for pending
    archives its last rescan did not see and, only if one exists, tries the lock
    again (an archive left over by the budget, or a failing one, waits for the
    next trigger), so a trigger that exited while the lock was held is not
    lost.
  - The **index commit lock**, one per project per vault per host, is short and
    blocking, and is held only to write already-computed data. Each of these
    is a commit step under it: one archive's commit step (vectors, chunks, KG
    records, ledger entry); one supersede step; a discard; a reaper pass, and
    the sweep, departed-project cleanup, index reap and lifecycle rename of `index/<p>/`; a
    `completeness.json` write, including the `stale` flag; an embed-cache write
    (the notes embed, the glide-path lazy embed and the fingerprint sidecar); a
    torn-line truncation; a decision-chunk write; the palace relabel by
    `vp audit rooms --apply`; one mempalace import batch; a baseline-set
    addition by copy or import; and writing or deleting `hnsw.idx`, which
    lives inside `index/<p>/` and so needs the same protection from a
    concurrent discard.
  - **Lock order.** The index commit lock is a leaf: no process holds two at
    once. The index run lock is taken before an index commit lock, never the
    reverse.
  - Searches, the MCP server and the note-time writers take only the index
    commit lock, with a timeout (`AcquireFileWithTimeout`), and only for their
    own writes: the `stale` flag, the notes embed into the cache, the
    glide-path lazy embed, and decision chunks, written by capture, the hook and
    the enrichment drain. They embed outside the lock and commit in batches. On
    a timeout the caller skips its own write: a search does not set the flag
    this time and does not commit the batch, and a skipped decision-chunk write
    is restored by the next notes-tier build or rebuild. None of them waits on
    the index run lock, so a long ingest never stalls a search or a capture.
  - The embed cache is written under the index commit lock, atomically. At
    `a32a2d4` `Put` writes in place (`internal/search/cache.go:121`) and the
    fingerprint sidecar uses one fixed `.tmp` name (`:220`); both become temp
    file, fsync, rename.
- **Triggers, all Go code, never an LLM.** Every trigger starts the ingester as
  a **detached process** through `internal/detachlaunch` and returns at once:
  setsid on POSIX (`internal/detachlaunch/launch_unix.go:20`), stdout and
  stderr to a log file, never the parent's pipes (`launch.go:96-97`), and on
  Windows a new process group with job breakaway (`launch_windows.go:38`).
  Breakaway is dropped on a fallback retry when the job forbids it
  (`launch.go:72-77`), and then the child can die with the job. The child's
  working directory is set away from any checkout, and inherited descriptors,
  including any vault-lock descriptor, are closed; `detachlaunch` sets no
  working directory at `a32a2d4`, so that part is new work for
  `capture-and-backfill-write-host-local-index-only`. A cgroup kill
  (teleport, systemd) can still reach the child; the write order above makes
  that harmless. (1) The hook's last step: after `WriteSession`
  (`internal/hook/hook.go:581`), and so after the archive, the harvest and the
  note link, the hook spawns the ingester and returns. The spawn is the last
  step on every return path that ran the archive step, including the
  claimed-session early return (`hook.go:554-555`), which returns before
  `WriteSession`. The hook never embeds: the host kills it at 30 s
  (`HookTimeout`, `internal/hook/settings.go:20`, registered for SessionEnd,
  Stop and PreCompact at `:23`), and embedding runs at about 46–48 s per MiB of
  archive, so session exit is never delayed by indexing. (2) A vault pull that
  brings in new archives — every code path that merges or rebases remote
  commits into the vault: `storage.Pull` (`internal/storage/vaultpull.go:152`),
  the rebase inside a commit-and-push (`internal/storage/vaultsync.go:993`) and
  its second caller, the mirror prune (`internal/storage/vaultsync_verify.go:250`),
  the push-rejection reconcile (`internal/storage/vaultsync.go:1836`), and the
  fast-forward merge in a resumed `vp vault clone`
  (`internal/storage/vault_clone.go:531`). (3) `vp_capture_session`, after it
  creates an inline archive on a hook-less host. (4) `vp mcp` startup, as a
  backstop for a spawn lost when the host kills the hook before its last step.
  So an archive is ingested by the next session start or pull at the latest,
  if it is outside the baseline set and within that run's budget.
- **Freshness in a running server.** Another process can write the store, so
  before each search a running engine reads the store's change counter,
  `palace/.local/index/.generation/<p>`, owned by
  `host-local-index-store-ledger-and-fingerprint`. It is
  read without a lock, and every commit that wrote anything writes it
  atomically. It holds two numbers: `gen`, which every such commit bumps, and
  `epoch`, which changes on every commit that did more than append — a
  supersede, a discard, a delete, a relabel, a torn-line truncation and a
  reap. A graph write or delete (`hnsw.idx` replaced or removed) bumps `gen`
  only, since another engine's in-memory graph stays valid after a save; a
  discard that deletes the graph changes `epoch` as a discard. It lives outside `index/<p>/`, so a
  discard never resets it, and a missing counter is created with a random
  `epoch`, so a recreated file never matches an engine's remembered value. The
  ledger alone would not do: the counter moves on writes the ledger never sees
  (decision chunks, mempalace batches, relabels and `completeness.json`). When
  only `gen` grew, the engine loads what was appended; any other change forces
  a full reload of that project. The ledger's per-session generation stays, for
  per-session readers such as coverage and the repair pass. A project removed
  mid-ingest is detected under the index commit lock, and the ingester stops
  for it without recreating `index/<p>/`.

Routine index work never depends on an LLM: chunking, classification,
embedding, entity extraction and ingest are deterministic Go code, and
iteration summaries, the one LLM-derived input, are tracked and only read.
**`vp index rebuild [project]`** stays the explicit, full, resumable rebuild;
`vp_refresh_index` is its MCP twin, and both call one driver, which shares the
ingester's per-archive commit step. `vp_refresh_index` starts the rebuild
detached, through the same launcher, and returns at once, so an MCP client
never times out; progress is read through `vp_index_status`. The rebuild
embeds outside the index commit lock, and takes the lock once for a discard and
once per archive's commit step. It ingests the baseline set as well as pending
archives, and on completion empties the baseline set and clears `stale`. A
completed rebuild is a run in which every live archive of the project, the
baseline set included, was attempted; each is either ledgered with its live
`source_sha256` or carries a failure record from this run; no local-tier miss
remains among ledgered chunks; and embedding ran. An archive that keeps failing
therefore cannot block completion for ever, and it stays visible: its session is
not ledgered, the project reads `partial`, and coverage names it as failed.
`--no-embed`, `--max-archives` and `--dry-run` never complete a rebuild, so they
leave `stale` set. The proof is defined by
`search-index-completeness-and-build-serialization`. A rebuild holder's rescan
before release also covers other projects of the vault with pending archives.
The driver writes
the ledger after each archive is durable, so a stopped run resumes; checks free
bytes and free inodes before and during the run and stops resumably when either
crosses its floor (set by measurement in
`explicit-resumable-index-rebuild-with-disk-watchdog`); and reports progress
per archive. Until the quantum split lands, every per-host `vp index rebuild`
that the release and the live migration run prescribe names its projects, and it
and the scripted rehearsal in `one-shot-migration-to-authored-only-vault`, which
the live migration run re-runs, leave out both quantum projects,
`qa-metabuild-system` and `orchestrator`; on
those hosts the two keep their backlog, and their coverage reads `partial`,
until the split. Completeness comes from `completeness.json`, the ledger and the
fingerprints, never from a file being present. Decision filing is redirected,
not removed: `fileDecisionDrawers` (`internal/capture/decisions.go:42`) and
the other decision writers, the hook's enrichment drain
(`internal/capture/enrichqueue.go:304`) among them, stop writing tracked
drawers and write decision chunks to the host-local store, under the index
commit lock with a timeout. Every build also rebuilds decision chunks from the
session notes' `decisions:` frontmatter, so no tracked decision drawer is
needed and `vp_palace_backfill_decisions` is removed in v10.2.0. The writers and
the tool's retirement belong to `decision-chunks-in-the-host-local-store`
(ADR-014 decisions 7, 10, 11).

**The tier table** (ADR-014 decision 7), owned by
`search-index-completeness-and-build-serialization` and cited by every child
that builds or reads the index, says what the search path builds:

| tier | built on the search path? |
|---|---|
| session notes (with decision chunks from their frontmatter, persisted in the store) and iterations | yes |
| host-local chunks of ledgered sources (archives and import batches) | yes, **embed-cache hits only**; a miss marks the project `stale` (missing-vector reason) and waits for the ingester's repair or an explicit rebuild |
| the glide-path lazy embed and the notes embed | embedded outside the index commit lock, committed in batches under it |
| tracked drawers (the glide path) | yes, only while the vault carries no migration marker (decision 11) |
| transcript archives not yet in the ledger | no: the pending-archive ingester or an explicit `vp index rebuild` only |

The orphan reaper never destroys what the search path skipped. At `a32a2d4` it
deletes every `.vec` outside the build's live set
(`internal/search/engine.go:697-725`); in v10.2.0 its live set is the union of
every tier an explicit rebuild would build, and it runs under the index commit
lock and re-reads the store's id set there (ADR-014 decision 7).

**Coverage and the empty search** (ADR-014 decision 8). `index_coverage` is
reported per project by `vp_bootstrap_context`, `vp index status` and the
read-only `vp_index_status`:

| state | meaning |
|---|---|
| `absent` | the project is **truly empty** (below), and nothing else |
| `stale` | the persistent `stale` flag is set, with its reason: a `chunks.fingerprint` or embed-cache mismatch (decision 3; "present and different"), which only a completed `vp index rebuild` clears; or an embed-cache miss on a chunk of a ledgered source (decision 7), which the ingester's repair pass or a rebuild clears |
| `legacy` | the vault carries no migration marker, and tracked drawers exist |
| `unbuilt` | the project has content, but `completeness.json` records no built tier on this host, for example a fresh clone before its first search. A missing fingerprint lands here, never in `stale` |
| `notes` | notes and iterations are built, and the project has no archive with a live session (decision 7) |
| `partial` | the project has archives with live sessions, and n of the m sessions are in the ledger with their live archive's `source_sha256`, with n < m. This includes n = 0, a pending supersede, and a live baseline archive not yet ingested |
| `current` | the project has archives, and every live session is in the ledger with its live archive's `source_sha256` |

The states are tested in table order, and the first that matches wins. So `stale` always surfaces; a
glide-path project reads `legacy`, not `notes`; a notes-only project reads `notes`, never `current`;
and a project with archives not yet ingested reads `partial`, never `notes`. `legacy` outranks
`unbuilt` deliberately: on a vault without the marker search answers from the tracked drawers, which
need no local build, so a fresh clone of an unmigrated vault reads `legacy`, and its reason carries
"not built yet on this host" and the ingester's progress. Every state carries a reason; for
`partial` it names, separately, the pending archives (outside the baseline set, not yet reached
within the budget), the backlog (the baseline set, which only `vp index rebuild` clears), and the
failed archives (those automatic runs skip after N failures, which only `vp index rebuild` retries).
`m` counts sessions with a live archive, not archive files, so a session archived on two days counts
once. `absent` means truly empty only, while "not built yet on this host" is `unbuilt`.
`search-index-completeness-and-build-serialization` owns the two shared predicates: the persistent
`stale` flag in `completeness.json`, with its reason, which the search path or the ingester sets on
a `chunks.fingerprint` or embed-cache mismatch or on an embed-cache miss on a chunk of a ledgered
source, which a completed rebuild clears for either reason and the ingester's repair pass clears for
the second; and the "truly empty" predicate, which the search error and `absent` both read (ADR-014
decision 8).

A project is *truly empty* when it has no session notes, no iterations, no
tracked transcript archives, no local chunks and, on a vault without the
marker, no tracked drawers. `vp_search` keeps its result-array shape. A search of a
truly empty project is an **error that names `vp index rebuild`**, from the MCP
tool and from `vp search` alike; a non-empty project with zero hits still
returns an empty list, because that answer is true. Cross-project search skips
truly empty projects instead of failing as a whole, and reports them only
through the coverage instrument, where a skipped project reads `absent`; the
result array carries no skip marker, so its shape is unchanged. Palace
navigation reads the host-local chunk store and, with no local index, returns
empty while `index_coverage` says why. A `kg_snapshot` for a project whose graph
is absent on this host is not a silent zero.

The change ships as MCP surface 10 (v10.2.0) with the data format still at 2,
because a v8 binary's capture and tidy would otherwise write extracted records
back into the tracked tree (ADR-014 decision 10). Each vault converts in one
revertible migration commit (decision 11). The command:

1. tags the parent commit `pre-authored-only-<date>` and pushes the tag
   (decision 4);
2. classifies each triple and entity line as authored or extracted, and stamps
   the authored ones (decision 5);
3. **deletes** every derived drawer and every extracted triple and entity line —
   the no-archive classes included — removing each from the index
   (`git rm --cached`) **and** from the working tree;
4. writes the migration marker;
5. adds the derived-path ignore lines through the gated reconciler, which emits
   them only on a vault that carries the marker; steps 4 and 5 land in the one
   commit of step 7;
6. writes the surface-10 stamp;
7. commits once.

It deletes the derived bytes rather than moving them into the host-local store
(ADR-014 decision 11, *Delete, not move*): every derived byte is either
regenerable from a tracked archive or deliberately dropped, and either way it
is recoverable from the tagged parent commit. Removing the files from the
working tree too means the migrator's next tidy cannot commit them again
(`internal/storage/vaulttidy.go:214-216`) and its disk matches every pulling
host's. Nothing is lost from history: the tag holds the bytes, and every host
rebuilds its index from archives (decision 4).

**The empty-vault path** (ADR-014 decision 11) applies only when, at HEAD
**and** at every remote tip, there is no tracked `palace/` content and no
`.surface` stamp in any stamp directory; the quantum vault has two remotes, so
the local tree alone is not enough. A v8-era stamp anywhere makes the vault
not empty, and it takes the normal path. The empty-vault path skips the
surface floor check, which cannot pass on a vault with no stamps, but keeps the
ancestor check: every remote tip must still be an ancestor of HEAD, or a remote
commit that adds a drawer would merge in tracked. It skips steps 2 and 3. It
writes the marker, the ignore lines and a vault-level surface-10 stamp,
`Audits/.surface`, one of the stamp directories the gate reads
(`CheckCompatible`, `internal/surface/version.go:807-812`). The empty quantum vault
(`~/quantum-vibe-palace-vault`) takes this path; it is not re-initialised.
Without it, a stampless destination would pass the gate for every binary, and
v8 would not be gated there at all.

**Migration preconditions** (ADR-014 decision 11). The command refuses unless
all of these hold (the empty-vault path skips the surface-10 half of the first,
never its ancestor half):

- every remote tip carries a surface-10 `.surface` stamp, and every remote tip
  is an ancestor of HEAD, as in the 6→7 precedent
  (`internal/storage/project_config_retirement.go:254-256`). Each remote is
  fetched once, at the start, and its remote-tracking ref is read immediately,
  with nothing run in between, as the precedent does (`:244-254`); path
  selection and every check read those same tips. Without the
  ancestor check a remote commit that adds a drawer would merge in tracked,
  because ignore rules do not apply to merges;
- the local vault is clean and pushed, with no `MERGE_HEAD`, no rebase in
  progress, no unmerged index entry and no stash entry touching `palace/`;
- the vault's data format is not behind the binary;
- the migration marker is absent, so a second run refuses;
- the operator attests, in the commit message, that every writer host has
  synced, pushed and been left clean and runs v10.2.0 or later, and that the
  quantum-ng migration's per-host rows are closed. Code cannot verify either:
  `vp check --check writer-identity` records `sha256(hostname + vaultPath)[:8]`,
  not a version (`internal/check/writer_identity.go:25`), and the quantum-ng
  census counts tracked drawer rows.

Surface 10 is forward-only: rollback is `git revert` of the migration commit,
which restores the tracked data, not v8. The revert re-tracks the derived files
from history and removes the marker and the ignore lines; host-local stores are
left alone. Surface 10 survives it: the precondition put a surface-10 stamp at
every remote tip before the migration commit, and those stamps stay, while a
stamp the migration commit itself raised (step 6) reverts. v8 stays gated for
writes because the gate reads the vault's maximum stamp. The empty-vault path
is the exception: there the migration commit wrote the vault's only stamp,
`Audits/.surface`, so a revert removes it and v8 is no longer gated. A revert
there re-stamps `Audits/.surface` at `MCPSurfaceVersion` in the same push; the
migration command's printed rollback text says so, and
`live-migration-run-on-the-personal-and-quantum-vaults` re-stamps if it ever
reverts the quantum vault's migration. A v9 binary on a reverted vault — one
without the marker — falls back to the tracked drawers for search and to the
pre-migration tidy and pull rules, while capture still writes nothing derived
into tracked paths. The end-to-end migrate, revert and keep-running-v9 test
lives only in `one-shot-migration-to-authored-only-vault`;
`tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker` tests its own
code against hand-built fixture trees, migrated and migrated then reverted
(ADR-014 decision 11).

**Dates come from the session, not the clock** (ADR-014 decision 4). Ingest and rebuild derive the
one timestamp that feeds every drawer's `filed_at`, every extracted triple's `extracted_at`, the
`valid_from` of temporal relationship triples and each extracted entity's `created_at` from the
source session's date: the **UTC** day of the session's start, for transcript chunks and decision
chunks alike. The start comes from the transcript itself: the timestamp of the first transcript
record that carries one, as the archive's adapter reads it, taken as a UTC day so that hosts in
different time zones date the same archive identically. For a transcript with no timestamped record
the fallback is the manifest's `captured_at` (`internal/archive/manifest.go:42`). For a transcript
chunk only immutable archive content is used, so its date never depends on a later step.
`captured_at` alone is not the session date: it is when the archive was made, and
`vp archive create` passes no time (`cmd/vp/cmd_archive.go:93-102`), so an old transcript archived
today would be dated today. The linked session note (`vault_rel_session_note`, `:41`) is not used:
the hook makes that link after archiving (`internal/hook/hook.go:495`, `:516`), and the link can
fail. The archive filename's `<date>-` prefix is not used either: it is the archiving day, as a
local calendar day (`internal/archive/archive.go:181-182`). An importer that writes an archive for
an old session writes the session's original record timestamps when the source has them. Otherwise —
the vibevault importer, whose notes carry a session-level `date:` and no per-record timestamps — it
passes the session's day at **12:00 UTC** as `archive.CreateOptions.Now`, never the clock
(`sessionNoon`, `internal/migrate/vibevault_archive.go:246-253`), so `captured_at` is `<day>T12:00:00Z` and the fallback
reads that day on every host. Noon, not midnight, because `archive.Create` takes the filename's day
from the same `Now` in the process-local zone (`archive.go:181-182`, through `CalendarDay`,
`internal/storage/clock.go:43-45`): at midnight UTC every host west of UTC would name the file for
the previous day, and a re-import on another host would miss `Create`'s idempotence skip
(`archive.go:211-228`) and add a second tracked archive of the session. Noon keeps the filename day
equal to the session day in every zone within ±11 h of UTC. One helper, `index.SessionDate`, owned
by `importers-write-the-frozen-tracked-corpus` so that it sits upstream of every caller, serves the
ingester and the rebuild driver; the decision-chunk writer does not call it. A rebuild of the same
archive therefore reproduces every field. A chunk or KG record owned by several sources takes its
date **and its `source_ref`** from the live owner with the **earliest** UTC start day; a tie goes to
the owner with the smallest owner key (an archive's `source_sha256`, or a batch id). The result does
not depend on the order in which the owners were ingested, so a rebuild reproduces it; whenever
ownership changes, for example when a supersede removes an owner, both are recomputed from the
owners that remain. Decision chunks take the session start day that the ledger records for the
session named by the note's `archive_session_id` (each ledger entry records its session's UTC start
day); they never read an archive. Until that session is ledgered on this host, or when the note
names no session, they take the note's own day (`internal/storage/sessions.go:50`); when the session
is ledgered later, the next build re-dates them, so a decision chunk's date is fixed only once its
session is ledgered. At `a32a2d4` decision chunks always take the note's local day
(`a32a2d4 internal/capture/decisions.go:157`). Search derives a result's date from `filed_at`
(`internal/search/engine.go:1236`), shows it (`:1247`) and filters on it (`:1546-1549`), so date
filters over transcript and decision chunks match the session date, the UTC day of the session's
start, not the day it was indexed, and `as_of` and timeline order on regenerated temporal triples
follow the session date too.

### Vector Index

```
internal/search/vector_index.go
```

Each project is answered by exactly one index, chosen by its chunk count: brute force below a
threshold, HNSW at or above it, with no dual query (ADR-014 decision 6). At `36cab60` HNSW ties
brute force at about 20k vectors and loses below that, while its builds take minutes against
seconds; most projects hold fewer than 15k chunks. The threshold is set by measurement in
`hnsw-parameters-from-real-vector-recall-and-production-wiring`, alongside `M` and `EfSearch`. The
threshold has hysteresis: a project switches to HNSW at the threshold, and back to brute force only
below a lower bound, which the same child measures, so a project near the threshold does not flip
from one process to the next and rebuild a graph each time. The switch loses no write: writes made
while a project converts from brute force to HNSW are caught up by the ledger-driven incremental
pass before the switch completes (`hnsw-graph-file-envelope-and-warm-start`). In v10.2.0 the index is
a brute-force `VectorIndex` implementation — an exact cosine scan behind a `sync.RWMutex`
(`internal/search/vector_index.go:187`), built in memory from the host-local chunk store; v10.2.0
ships brute force for every project and persists no graph. HNSW is decoupled from v10.2.0 (ADR-014
decision 6): the release is the git-health release, and the HNSW children
(`vector-index-interface-and-coder-hnsw-wrapper`,
`hnsw-parameters-from-real-vector-recall-and-production-wiring`,
`hnsw-graph-file-envelope-and-warm-start`) land when they are ready, with no release version tied to
them. HNSW is off the critical path to the quantum split: git health needs the host-local store, not
the graph.

When those children land, a project that has reached the threshold, and not since fallen below the
lower bound, is answered by HNSW: `github.com/coder/hnsw` pinned at `36cab6028fed` (pseudo-version
`v0.6.2-0.20260622133054-36cab6028fed`, the first commit with all three upstream recall fixes),
behind a thin wrapper persisted as `hnsw.idx`. The wrapper answers the library's behaviour at that
commit: one `sync.RWMutex` around every call; tombstones instead of `Delete`, with a rebuild above a
tombstone threshold; upsert as a tombstone plus a new internal key; validation of dimensions, `k`,
zero or NaN vectors and in-batch duplicate ids before every call; import only into a fresh graph
from its own checksummed envelope, re-applying `EfSearch`; temp-file, fsync, rename and
directory-fsync writes; and cosine distance with a NaN guard. The library has no `efConstruction` —
`Add` uses `EfSearch`. `M` and `EfSearch` are chosen from recall@10 measured on real MiniLM vectors
from a real project at ≥ 50k chunks; until the quantum split lands, that measurement reads neither
quantum project, `qa-metabuild-system` nor `orchestrator`. At `36cab60` the library does not build
for windows/amd64, a release target (`.goreleaser.yml:10-14`): its
`coder/hnsw@36cab60 encode.go:304` calls `renameio.TempFile`, which is `!windows`, and only
`SavedGraph` uses it, which vp never calls. A small upstream PR fixes that use; until it merges, a
`go.mod` `replace` points at a minimal vendored copy without `SavedGraph`, removed once upstream
merges. A `CGO_ENABLED=0` cross-build of every goreleaser target is part of the wrapper child's
acceptance (ADR-014 decision 6).

Search over an HNSW project is approximate, and the exact brute-force index is
also the exactness oracle in tests; a project is never queried through both.
The recall
test HNSW brings uses a seeded, clustered 384-dim corpus of at least 50k
vectors (or a committed fixture of real exported vectors) with an asserted
recall bar and a delete-and-upsert churn phase, and a persistence test asserts
that loading a graph ran no graph build and no `Add`; each test is broken once
on purpose to prove it can fail (ADR-014 decision 6). The ≥ 50k test does not
run in `make test`, which is `go test -race -short` (`Makefile:122`): a 50k
build takes minutes, and `-race` makes it about four times slower. It runs in
`make hnsw-measure`, behind a build tag, and in CI through
`.github/workflows/hnsw-measure.yml`, with dispatch and nightly triggers,
because `ci.yml` has neither (`.github/workflows/ci.yml:3-6`); `make test` keeps a
5k regression floor. Both belong to
`hnsw-parameters-from-real-vector-recall-and-production-wiring`. In v10.2.0 the in-repo
recall harness (`TestConstitutionRecall`, `internal/search/recall_test.go:274`)
runs five self-queries over a constitution corpus embedded with synthetic
TF-IDF vectors (`:290`); it asserts an exactness bound — no returned distance
exceeds the brute-force ground-truth threshold (`:300-307`) — and only logs
set-overlap recall (`:319`).

### Hybrid Search Engine

```
internal/search/engine.go
```

The search pipeline combines vector similarity with structural metadata:

```
1. Embed query text → query vector          (forces the lazy model load, once)
2. Ensure index    → read the store's change counter (.generation/<p>): if
                     only gen grew, load what was appended; any other change
                     forces a full reload; if the project has no in-memory
                     index, build the brute-force index from notes, decision
                     chunks, iterations, local chunks of ledgered sources
                     (archives and import batches; embed-cache hits only)
                     and (no marker) tracked drawers, deduplicated by wide
                     content hash; truly empty is an error.
                     v10.2.0 persists no graph; HNSW persistence comes with
                     the HNSW children
3. Vector search → top limit×3 candidates (over-fetch for filtering)
4. Metadata filter → remove non-matching wing/room/hall/date
5. Score conversion → 1 / (1 + cosine_distance)
6. Structural boost → multiply score for matching filters
7. Deduplicate → keep highest-scored per SourceRef
8. Return top-N
```

### Index Construction

There is **no reindex on spawn**. A project's index is materialized by
`ensureIndex` (`internal/search/engine.go:156`) on the first search that touches
it, and the result is memoized. Concurrent searches for the same project join
one in-flight build (`projectBuild`, `engine.go:102`) rather than each running
their own `Rebuild`; builds for *different* projects run concurrently, because
the build mutex is never held across a `Rebuild`.

That first build reads session notes, the decision chunks rebuilt from their `decisions:`
frontmatter, iterations and the chunks already in the host-local store whose source (an archive or
an import batch) is in the ledger (embed-cache hits only), and — while the vault carries no
migration marker — the tracked drawers, read as before the migration and deduplicated against the
local chunks by a wide content hash, never by the 32-bit drawer id (ADR-014 decision 7). That
tracked-drawer read is the glide path: it serves every v9 host between install and the migration,
and every v9 host on a vault whose migration was reverted, so transcript search does not regress on
a host that upgrades first. The glide path keeps the lazy embed of tracked drawers that search has
at `a32a2d4`, on any vault without the migration marker: a project's first search embeds every
tracked drawer the embed cache misses (`internal/search/engine.go:504-560`, embedding in
`embedMisses` at `:600`), transcript drawers included, and so does the first search after an
embedder change, which on a large project can take hours. This is a stated, temporary exception to
operator decision 4 (no lazy full-transcript embed on search), and it applies to any vault without
the marker, before the migration or after a revert. On the glide path coverage reads `legacy`
(ADR-014 decisions 7, 8). The first build never embeds a transcript archive: nothing embeds one
because somebody searched, and archives arrive through the pending-archive ingester and
`vp index rebuild` only. Per-project builds and inserts are serialised within a process and, through
the per-project index commit lock, across processes; a search never waits on the ingester's index
run lock. An index populated incrementally by an ingest does not count as complete: completeness
comes from `completeness.json`, the ledger and the fingerprints, never from an index being present
in memory or a file being present on disk.

A cross-project search — one with no `Project` filter — calls
`ensureAllIndexes` (`engine.go:241`) over every project the vault knows
about: the union of both trees (`Vault.ListAllProjects`,
`internal/storage/projects.go:81`), because notes and iterations live under
`Projects/` and a project captured as notes only has no `palace/` chunks at all.
It is the expensive case, paid only by callers who ask for it. Truly empty
projects are skipped, and reported only through the coverage instrument, where
they read `absent`; the result array is unchanged. Any other project that fails
to build fails the search, naming the project — a result that silently dropped
it would present itself as complete (ADR-014 decision 8).

A build **failure is returned to the caller**, never swallowed. This is
load-bearing: before `ensureIndex` existed, a search against a project with no
index took a `return nil, nil` branch — an empty result and a nil error. With no
eager rebuild left to hide it, every agent on every fresh session would have been
told, plausibly and silently, that the vault contains nothing. The same reasoning
makes a truly empty project an error naming `vp index rebuild`, never `[]`.
`internal/integration/lazy_startup_test.go` pins the positive case: a
never-rebuilt project returns real hits through a real JSON-RPC `vp_search`.

The corpus has these kinds of source. **Chunks** from the host-local
`chunks.jsonl`: transcript chunks written from archives by the
pending-archive ingester and by `vp index rebuild`, and the batches a mempalace
import ledgers (ADR-014 decision 7). **Decision chunks**, persisted in the
host-local store by `fileDecisionDrawers` and the other decision writers
instead of as tracked drawers, and rebuilt from the session notes' `decisions:`
frontmatter on every build (ADR-014 decisions 7, 10, 11). **Tracked drawers**,
read only while the vault has no migration marker (decision 7). **Iterations**:
`Projects/<project>/iterations.md` split on `wrapstate.ParseEntries` (H2
iteration headers; `collectIterationCorpus`,
`internal/search/iterations.go:105`). **Session
notes**: the bodies of `Projects/<project>/sessions/*.md` split with
`storage.ParseFrontmatter` (`collectNoteCorpus`, `internal/search/notes.go:141`).
The iteration and note sources are sub-chunked at the capture defaults
(800/100). Iteration rows use `source_type=iteration` and a per-entry `source_ref`
(`iteration/{n}/m/{matchIndex}`); session-note rows use
`source_type=session-note` — deliberately distinct from the transcript corpus's
`source_type=session` — at the synthetic location `wing=history`,
`room=session-notes`, `hall=narrative`, with a per-note `source_ref`
(`sessions/{stem}.md`). In both, chunk index lives on the cache ID, not the
ref, so search dedup keeps one hit per entry. No synthetic drawers are written
by either.

The note source is what makes a project captured as **notes only** — no transcript, no archive, no
chunks — reachable from `vp search`; the pending-archive ingester indexes the transcript and never
the note, so those projects would otherwise have nothing the index could ingest. Its reach is wider
than that: any project with session notes gains note rows, whether or not it also has transcript
chunks. Note and iteration chunk ids are keyed on the entry's identity — for a note, project, stem
and chunk index — and carry a content hash (ADR-014 decision 7). Identity keeps a note chunk from
ever sharing a key with a transcript chunk, and two notes carrying identical text produce two
separate rows; the content hash gives an edited note a new id, and so a new vector. The note corpus
is **additive** and does not dedup against transcripts. Because nothing routes through
`capture.IndexTranscript`, the note pass extracts **no knowledge-graph facts** from wrap prose; that
is structural, not a flag.

Cache-miss vectors are embedded with `EmbedBatch` in chunks of
`EmbedderBatchSize` (default 32; `engine.go:1447`), writing each vector to the
embed cache as it lands, so a rebuild killed partway through leaves durable
progress behind. Per-item embedding — the old behavior — is roughly 7x slower
on the ONNX backend.

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
`palace/.local/embed-cache/{project}/{drawer_id}.vec` (raw little-endian
float32; `storage.Vault.EmbedCacheDir`). The cache avoids redundant ONNX
inference during `Rebuild()`. Deleting the cache forces re-embedding but loses
no data.

**Why it lives under `palace/.local/`.** It used to live at
`palace/{project}/.local/embed-cache/`, inside the project's synced directory,
while the vault's gitignore keeps `.local/` out of the repository. A pull that
deleted a project removed every tracked file and left the ignored cache — and
the `palace/{project}/` around it — on every host that had ever embedded
something for it, and every enumerator then counted that husk as a store. A
search of a notes-only project also created `palace/{project}/` outright. Under
`palace/.local/`, which no enumerator walks and the canonical gitignore already
covers, a `Put` cannot create or change anything under `palace/{project}/` or
`Projects/{project}/` for any slug, with no existence check on the embedding
path.

**The one-time sweep.** The first cache operation on each `EmbedCache` instance
runs `storage.Vault.SweepEmbedCaches` once (a per-instance `sync.Once`, lazy so
constructing an engine does no I/O):

1. *Migrate and heal.* When any legacy cache exists, git is asked once
   (`Vault.TrackedPalaceLocalFiles`, a read-only `git ls-files`) which slugs have
   tracked files under `.local/`. The canonical gitignore covers `palace/.local/`
   but not `palace/*/.local/`, so a vault can have committed legacy vectors, and
   moving them would leave deletions in the working tree that block sync — from a
   read-only search. Such a slug is left exactly as it is and logged at Warn; a
   vault that is not a repository tracks nothing; and if git cannot answer inside
   a repository, this stage is skipped for the run. Every other real directory
   `palace/{slug}/` (never a symlink) holding a real `.local/` has its legacy
   `.local/embed-cache/` renamed whole into the new layout, or merged file by
   file when the target exists or the rename is impossible (EXDEV when
   `palace/.local` is its own mount) — through `os.Link`, which never
   overwrites, so a vector a newer process already wrote wins, and through a
   temp-file copy (mode 0644, as `Put` writes) where the filesystem cannot
   link, so such a host still heals. A copy temporary a crash left behind is
   collected by a later sweep once it is ten minutes old; a younger one may
   belong to a running sweep and is left.
   Only regular `*.vec` files move; anything else stays, and keeps its
   directory. A target that is a symlink is never followed. Then the legacy
   `embed-cache/`, `.local/` and `palace/{slug}/` are removed with
   non-recursive `os.Remove`, on every run, so a crash between the rename and
   the removals is finished next time. Anything real beside the cache —
   drawers, `kg/`, `.surface`, `imported-sessions.jsonl` — keeps its directory.
2. *Reap orphaned caches.* `palace/.local/embed-cache/{slug}/` for a slug that
   exists nowhere on this host loses its `*.vec` files and then the directory.
   Each slug is judged against the live tree after the cache directory was read,
   and kept whenever `palace/{slug}` or `Projects/{slug}` exists at all (a
   symlink, a junction, a case-insensitive match). Nothing is reaped when the
   listing fails, lists zero projects, or `Projects/` itself is absent;
   symlinked cache directories are never followed.
3. *Report* moved, merged, dropped, healed and reaped counts (logged at Info),
   tracked slugs and per-slug errors (Warn). A failure leaves the legacy file in
   place, where the `palace-local-only` check row shows it.

`EmbedCache.Put` retries its write once, after re-creating the directory, when
it fails with ENOENT: `rename(2)` onto an empty directory succeeds by replacing
it, so a sweep's whole-directory rename can pull the directory a Put has just
made out from under its open. The retry lands the vector in the renamed
directory.

The sweep runs from a read-only `vp_search` too. Operator decision 2026-09-10
rules it exempt from the read-only-serve contract: everything it touches is
host-local, gitignored, regenerable derived state (`internal/tools/readonly_serve.go`).

**The index directory gets the same treatment** (ADR-014, implementation notes to
decision 2). `palace/.local/index/<p>/` is host-local, gitignored, regenerable
derived state like the cache beside it, so it has a counterpart for each of the
cache's mechanisms: the embed-cache sweep (`SweepEmbedCaches`,
`internal/storage/embedcache_sweep.go`), departed-project cleanup (`DepartedCaches`,
`internal/storage/embedcache_departed.go`), the index reap for projects that no
longer exist (`ReapGoneProjects` / `RemoveProject`,
`internal/indexstore/lifecycle.go`) and the read-only-serve exemption. Each runs
under the index commit lock, and the reap renames `index/<p>/` to a dot-named
tombstone `.tomb-<p>-<epoch>` in one step before deleting, so a crash never leaves
a half-deleted store. `vp vault project delete` purges `palace/.local/index/<p>/`
with the project's trees, beside the embed cache it purges (`collectDeleteTrees` /
`deleteIndexStores`, `internal/storage/lifecycle_delete.go`, through
`indexstore.RemoveGoneProject`). A **lifecycle rename** moves the index store but
**rebuilds** the embed cache: `internal/indexstore/rename.go` (`AdoptRenamedStore` /
`AdoptRenamedProject`) renames `palace/.local/index/<old>/` to the new slug under the
index run lock, rewrites each chunk's `wing` label and the ledger's `archive_path`,
then **removes** `embed-cache/<old>/` rather than carrying it — the new slug
re-embeds lazily on first search (operator **M0 ruling 2026-10-05 = REBUILD**; the
cache is never renamed). It refuses rather than clobber when `index/<to>/` already
exists or the target's embed cache already holds vectors (`vp index rebuild <to>`
is the escape). Host-local chunk ids exclude the wing, so no id changes; the change
counter `.generation/<p>` is never deleted — its `epoch` changes, as for a reap.

**Copy and the two-marker refusal.** `vp vault copy` (and its `--as` composition)
carries only the source's **tracked** files, through a private blobless footprint
snapshot that `walkCopyScratch` proves holds exactly the paths the source tree
lists — so an untracked, ignored derived file (a stray drawer, a
`*.manifest.json.<hash>.bak`) never travels into the destination. Copy moves
projects only from a **migrated source into a migrated destination**: the
two-marker refusal (`copyMarkerRefusals`, `internal/storage/lifecycle_copy.go`)
reads the source's marker from the snapshot's `vault.toml` bytes and the
destination's from the served vault, and refuses every other pairing — an
unmigrated destination, an unmarked source, or a marker it cannot read — before
the lock and before any write. Copy never writes a marker; a born-migrated
`vp vault init` destination satisfies the rule from creation. (The retired
`vp_vault_split` / `vp_vault_merge` tools once carried this derived-path exclusion
and two-marker refusal; both now live on copy — U12.)

**Stale-MCP caveat.** A long-lived `vp mcp` from before the upgrade keeps
reading and writing the legacy path until it is restarted: it cold-misses every
vector a newer process moved, re-embeds it, and writes it back at the legacy
path, which the next new-binary process start merges again. The cost is
bounded but real on a large corpus, so restart long-lived `vp mcp` servers
after installing; `vp check --check stale-mcp` names them.

**Known limitations.**

- `CanonicalGitignorePatterns` ignores `palace/.local/` but not
  `palace/*/.local/`, so on a vault reconciled only by vp an
  `imported-sessions.jsonl` marker is still Reported dirt that blocks sync.
  Out of scope here; the embed cache no longer contributes to it.
- Three narrow races, documented in `embedcache_sweep.go`, none of which loses
  content: a sweep landing between the vibe-vault migrator's
  `EnsureDir(.local)` and its marker write makes that write fail (the command is
  idempotent on re-run); a capture into a legacy husk racing the first sweep's
  `Remove(palace/{slug})` can fail its `MkdirAll` (the session is reported not
  searchable); and a git checkout or pull that revives a husked slug, racing
  that same removal, can die with "cannot create directory", because git's
  checkout path does not retry the mkdir (re-running the pull finishes it). All
  three need the first sweep on a host that still has legacy husks.
- The orphan reap has no grace window. A project absent from both trees at the
  moment its cache is judged — briefly, during a rebase that replays the commit
  creating it, a checkout of an older commit, or `git stash -u` — loses its
  vectors and pays a re-embed on the next miss. Two vaults sharing one
  `palace/.local` through a symlink reap each other's caches on every start.
- The source audit's funnel rule does not see the sweep's raw `os.Rename` /
  `os.Remove`: their destinations are parameters, which the syntactic rule
  cannot resolve. The reason they are raw is recorded in the file's header
  instead, and accepted knowingly.

---

## Session Capture (Phase 5)

### Capture Flow

Sessions are captured via two paths, both using the shared pipeline
`capture.WriteSession`:

- **MCP path**: `vp_capture_session` tool — AI-generated summary. It indexes
  no transcript text: its `transcript` parameter only creates an archive on a
  host without a hook (`archive_transcript`; ADR-014 decision 7). When the host
  session id was **derived**, writes a claim sentinel so the hook path skips
  this session (a minted inline id gets no claim — no hook will ever query it).
- **Hook path**: `vp hook` CLI — Claude Code invokes this on SessionEnd,
  Stop, and PreCompact events. Writes an honest crash-net placeholder
  (`Auto-captured session (no summary yet)`), does **not** friction-score
  `tag:auto-capture` notes, and marks the note `needs_indexing: true` in
  frontmatter. Skips the note if a claim sentinel exists. Its last step spawns
  the pending-archive ingester as a detached background process and returns;
  the hook never embeds (ADR-014 decision 7; see *Host-Local Index*).

`vp hook` auto-capture requires a `.vibe-palace.toml` (run `vp init`); sessions
in un-init'd directories are intentionally skipped.

`WriteSession` writes the note and indexes nothing; archiving and ingest happen
around it:

```
WriteSession (both paths):
1. Resolve the archive the note links to, if one already exists
2. Compute friction score (0–100) from the transcript (never for
   tag:auto-capture notes)
3. Write session markdown to {vault}/Projects/{project}/sessions/
   - YAML frontmatter: date, tag, friction, decisions, files_changed
   - Auto-increments iteration number per day
4. Link note → manifest when the archive already exists (the inline path)

Hook path (vp hook), in order:
1. Archive transcript to {vault}/Projects/{project}/transcripts/ at SessionEnd
   and PreCompact, never at Stop (internal/hook/hook.go:351; archive.Create
   at :331)
2. Memory harvest (SessionEnd) and the enrichment-queue drain
3. Cross-link archive manifest ↔ session note (bidirectional, :438, :459)
4. Claim check: a session already captured over MCP skips the note and goes
   straight to step 7
5. WriteSession (:524) with a nil indexer: the crash-net note
6. Write claim sentinel to {cwd}/.vibe-palace/
7. Last step: spawn the pending-archive ingester, detached, and return

MCP path (vp_capture_session):
1. On a hook-less host, create the inline archive first
2. WriteSession: the note is born linked in both directions
3. Write claim sentinel (derived host session ids only)
4. After creating an inline archive, spawn the pending-archive ingester,
   detached, and return

Pull paths (storage.Pull, the rebase inside commit-and-push and its second
caller the mirror prune, the push-rejection reconcile, the fast-forward in a
resumed vp vault clone) and vp mcp startup spawn the same ingester, detached.

Pending-archive ingester (detached; under the vault's index run lock on this
host, which a second trigger finding held exits on at once), given the vault
root and the triggering project: for each pending archive (session absent from
the ledger, or a different source_sha256) not in the host's baseline set,
newest first, within the per-run budget; the triggering project first, then the
vault's other projects (ADR-014 decisions 4, 7):
   a. Read the whole archive, verify its hash against the manifest, close it
   b. Date it: UTC day of the first timestamped transcript record, as the
      adapter reads it; fallback, the manifest's captured_at. A chunk or KG
      record several sources own takes its date and source_ref from the
      live owner with the earliest start day (a tie goes to the smallest
      owner key), recomputed whenever ownership changes
   c. Detect format (plain text, markdown transcript, JSON-RPC chat)
   d. Chunk transcript (sliding window, sentence-boundary aware)
   e. Classify each chunk: wing (project), room (keyword), hall (memory type)
   f. Extract entities (file paths, URLs) and triples
   g. Batch embed all chunks (EmbedBatch), outside any lock
   Then one commit step under the project's index commit lock, writing only
   computed data, each step durable before the next:
   h. Write the vectors, each atomically (temp file, fsync, rename)
   i. Append the chunks to the host-local chunks.jsonl, ids a wide content
      hash, recording this archive's source_sha256 as an owner (a supersede
      drops an older archive's ownership instead), and fsync
   j. Append the KG records to the host-local KG, and fsync
   k. Record the archive in the ledger, keyed by session, with its
      source_sha256 and chunk count
   Within the same budget: re-embed missing vectors of ledgered archives, and
   re-ingest one whose chunks fall short of its ledgered count. The run ends
   when the budget (archives or wall-clock cap) is spent, or a rescan finds no
   pending archive this run has not attempted; a failure is counted in the
   ledger and retried by the next run. After releasing the index run lock,
   check once more for pending archives the last rescan did not see and, only
   if one exists, try the lock again.
   The only tracked writes on any capture path are the session note, the
   archive and its manifest.
```

The cross-link closes when the hook links the note to the manifest after
`archive.Create`, and the manifest back-links the canonical note. A link that
never closes leaves a *stranded* transcript — detected by the vault audit's
`archive-roundtrip` dimension and recovered by `vp archive backfill` /
`vp archive link` (see ADR-007). The inline path never strands: its archive is
created *before* the note, so both link directions exist at birth. The claim
sentinel is idempotency for derived host session ids only; a minted inline id
gets none.

### Host attribution from the client handshake

The MCP capture path derives the session's host attribution (`claude-code`,
`Zed`, …) from the MCP `initialize` handshake — `mcp.ClientInfoFromContext`
reads the `clientInfo` the client sent at connect — rather than trusting a
caller-supplied `client_info` param. `resolveCaptureHost`
(`internal/tools/session_tools.go`) settles it **derived-wins**: when the
handshake yields a host it is used and stamped `host_source: derived`; a
caller-declared value that loses to the derivation is logged. Only when the
handshake carries nothing does a caller-declared value apply
(`host_source: declared`), and when neither exists the host is recorded as
unknown (`host_source: unknown`) — never fabricated, because absence is not a
value (ADR-006: this is a DERIVE where the server can see the input). The
`HostSource` provenance field on `SessionMeta` records which path established
the host.

### Inline transcript archive on hook-less hosts

Hook-less MCP hosts (the Zed **native** pane, Grok Build where its hook wiring
is absent, and other clients without a SessionEnd hook) have no on-disk host
transcript for `vp hook` to archive. A Claude-shaped **ACP** agent in Zed's
agent panel is *not* in this class — it inherits Claude Code's hook path — and
neither is a Grok install whose hook wiring is present; `vp hook` reads Grok's
own wire dialect. Durability for those hosts is **MCP capture + inline archive** — not
an alternate "shim path." Shims (`.grok/plugins/...`, `/vpc-wrap`) are UX onto
`vp_cmd` / `vp_capture_session`; the archive is created inside the capture
handler. Capture indexes none of the transcript text itself: after it creates
the inline archive, it triggers the pending-archive ingester, which brings that
archive into this host's index (ADR-014 decision 7; see *Host-Local Index*).

**Default (auto-on).** When all of the following hold, `vp_capture_session`
creates an inline archive pair even if the caller omits `archive_transcript`:

1. The server cannot derive a host session id (empty archive session id).
2. The caller supplied a **non-empty** `transcript`.
3. The MCP initialize handshake **derived** a host in the known hook-less set
   (`grok` / `xai` / `zed` — case-insensitive **token** match, never substring;
   Claude-shaped names never match).

   Token, not substring, is load-bearing: the client name is vendor-chosen, and
   English words like `optimized` and `authorized` embed `zed`. The name is split
   on non-alphanumeric runs and each token compared whole, with a `claude` guard
   above the list so a compound name like `claude-grok-bridge` cannot match.

Unknown or declared-only hosts do **not** auto-on (declared is spoofable;
unknown is Claude-miss-shaped). Empty transcript never archives — and on a
**derived hook-less** host it is a loud failure rather than a quiet success:
no SessionEnd will archive that session later, so the capture reports
`transcript_archive_unreachable` instead of letting the note be born
permanently archive-less. Unknown and declared-only hosts stay quiet, because
absence of a derived identity is not evidence the host is hook-less.

**Explicit force.** `archive_transcript: true` still forces inline archive
under the derived-empty + non-empty-transcript gate on *any* host — useful for
unknown clients or operators who want the pair regardless of handshake. On a
derivable host (Claude Code) the flag remains a **no-op**: SessionEnd archives
the authoritative host transcript, and an inline copy of the agent's own
rendition would be a lossy duplicate — never a competitor. Templates
(`wrap` / `capture`) still name `transcript` and `archive_transcript: true` so
agents pass the content the default needs; the force is harmless on Claude.

Mechanism details (unchanged from the native-capture design):

- **Minted id, shared by note and archive.** The server mints the capture
  `session_key` (UUID) and uses it as the archive session id, so a retry with
  the same key converges on the same note *and* the same manifest. The mint is
  server-controlled and collision-free by construction.
- **Born linked.** `archive.Create` runs **before** `WriteSession`, so the
  existing linking machinery (`ResolveEntry` + `LinkSessionNote`) stamps both
  directions when the note is written — no deferred loop, no stranded
  transcript for `archive-roundtrip` to find.
- **Honest provenance.** The note records `archive_session_id_source: inline`
  (distinct from `derived` and `backfilled`) and `session_key_source: minted`
  — a handler-minted key must never masquerade as caller-supplied, and the
  backfill predicate (which keys on `session_key_source: caller`) correctly
  never treats a born-linked inline pair as recoverable debt.
- **The note always lands.** A failed inline `archive.Create` is stashed as a
  `transcript_archive` entry in the incomplete-capture error payload; capture
  proceeds and the note is written unlinked. The same holds for the
  `transcript_archive_unreachable` case above: the note is capture's one
  irreplaceable output, so the loss is reported beside it, never instead of it,
  and the payload carries the `session_key` a retry needs.
- **No claim sentinel.** Claims are keyed by the *host's* session id — the id
  the SessionEnd hook queries — so only a derived id can ever match one. The
  sentinel is written for derived ids only; a minted id would be a sentinel
  with no reader.

The `inline` archive adapter (`internal/archive/inline_adapter.go`) is
**mechanism-named, never host-named**: the manifest records how the bytes
arrived (handed to `Create` in-memory via `CreateOptions.SourceContent`), not
which host produced them — the content is the agent's own rendition of the
session, not host ground truth, and naming a host would launder a self-report
into an authenticity claim. The host is recorded separately by the note's
host provenance (above). `ResolveSource` writes the supplied bytes verbatim
to a temp file (the same synthesize-to-temp shape as the Zed adapter) so the
rest of the Create pipeline stays byte-oriented, and empty `SourceContent` is
a hard error, never a fallback to some on-disk location.

**Strategy A limitation (enrichment queue).** Optional LLM enrichment
(`enrich: true` / `[enrichment]` config) can enqueue failed synthesis jobs
under `<CWD>/.vibe-palace/enrichment-queue/`. `DrainEnrichmentQueue` runs only
from the Claude SessionEnd hook path — hook-less hosts do not drain that queue
automatically. Accept and document: Grok/Zed durability for notes + inline
archives does not include automatic enrichment-queue recovery.

### Hook Installation

`vp hook install` manages entries in `~/.claude/settings.json`, replacing
legacy `vv hook` (vibe-vault) with `vp hook`. `vp init` calls this
automatically. The hook fires on three Claude Code events (SessionEnd,
Stop, PreCompact) with a 30-second timeout (`HookTimeout`,
`internal/hook/settings.go:20`; events at `:23`). That bound is why the hook
never embeds: its last step spawns the pending-archive ingester as a detached
process and returns (ADR-014 decision 7).

### Session Enrichment (LLM synthesis)

The hook path's deterministic auto-summary is an honest crash-net placeholder
(`Auto-captured session (no summary yet)`) — enough to mark that a session
existed, not enough memory for bootstrap, search, or a resuming developer.
When the opt-in `[enrichment]` config block is enabled, an LLM synthesis pass
replaces that heuristic summary/decisions/open-threads/tag with a real
synthesis. It sits between the transcript and `WriteSession`, and is wired only
into the SessionEnd hook and the `vp_capture_session` `enrich` param (default
off — agent-authored `/wrap` notes are already good).

`internal/enrichment.ExtractPromptInput` distills the multi-MB transcript into a
bounded `PromptInput` (user/assistant text capped at 12000 chars each, tool
counts, edited files) — the LLM never sees the raw JSONL. An `Enricher` calls a
`Completer` (`internal/llm`: OpenAI-compatible `*Client` or native Anthropic,
selected by provider via `NewCompleter`, sharing one `retryWithBackoff` helper),
tolerantly parses the JSON reply (strips ```json fences, one corrective
reprompt), and validates the tag against the canonical 7-tag set. The system
prompt is vault-editable (`<vault>/Templates/enrichment.md` > embedded > const)
and loaded **raw** so no `{{DATE}}` expansion leaks nondeterminism.

The pass is synchronous and best-effort: on success the note is written enriched
(`enriched_by`/`enriched_at` frontmatter + an `<!-- enriched -->…<!-- /enriched -->`
fenced body) and capture proceeds; on LLM error/timeout capture **never fails** —
the note is written plain and the extracted input is enqueued to a host-local
`<CWD>/.vibe-palace/enrichment-queue/` (not the vault, so it never trips tidy/wrap
preflight). `DrainEnrichmentQueue` (run in the SessionEnd hook, not in
latency-sensitive bootstrap) claims each job via atomic `.processing` rename and
rewrites the note in place via `storage.RewriteSession`, which shares
`buildSessionBody`/`marshalSessionFile` with the inline path so an inline-enriched
and a drained note converge to byte-identical bodies. Hook-less hosts never run
that drain — see the Strategy A limitation under inline archive above. See
ADR-005 (`doc/adr/005-llm-enrichment-synthesis.md`) for the full rationale.

### Chunking Engine

```
internal/chunk/chunker.go
internal/chunk/detector.go
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

### Friction Analytics Layer

```
internal/capture/analytics.go
internal/capture/effectiveness.go
```

The analytics layer turns the friction history into actionable signal for the
`vp friction`, `vp trends`, and `vp effectiveness` CLI commands. Its functions
are **pure and slice-based**: they take an already-loaded `[]storage.SessionMeta`
rather than a vault handle. The CLI command is a thin wrapper that scans the
project's sessions **once** and calls these functions in sequence — a single
read of disk feeds every metric, and the analytics package never imports
`storage` for I/O (avoiding an import cycle with the capture pipeline that
`storage` already depends on). `ComputeEffectiveness` was extracted here from
`internal/tools`; type aliases (`EffectivenessResult`, `WeeklyEffectiveness`,
`OverallEffectiveness`) remain in `tools` so the `vp_get_effectiveness` MCP tool
and its tests are unchanged.

**Friction breakdown with presence semantics.** Session capture now records a
`FrictionBreakdown` — four capped sub-scores (corrections, retries,
error_density, rework; each 0–25) that sum to the 0–100 composite score. It is
stored as `friction_breakdown` in session frontmatter as a *pointer with
presence semantics*: a `nil` breakdown means friction was never broken down
(the session predates this feature), while a present-but-all-zero breakdown
means a genuinely measured frictionless session. The correction-density series
counts `nil`-breakdown sessions as **missing** and labels them explicitly — it
never conflates "no data" with a measured zero. This is the central design
decision of the layer: every metric distinguishes absence from a measured value.

**Rolling windows vs ISO buckets.** `GetFrictionWindows` reports rolling N-day
windows (7/30/90), where `GetFrictionTrends` (above) reports ISO-calendar-week
buckets. They are complementary, not duplicates: windows answer "how rough was
the last week/month/quarter," buckets answer "which calendar week was rough."

**Model field + regression detection.** The SessionEnd hook extracts the model
from the live Claude JSONL transcript (`archive.InspectClaudeJSONL`) and stores
it as `model` in frontmatter; the MCP capture path leaves it empty unless
supplied. Because old sessions have no model, `DetectModelRegressions` (which
groups consecutive same-model runs and reports the avg-friction delta at each
boundary) is near-empty until new sessions accrue — so its output is labeled
with model coverage (`X of Y sessions`) rather than implying complete data.

**Proactive boot-warning.** `ComputeFrictionTrend` derives a trend direction
(improving/worsening/stable/unknown) by comparing the 7-day window against the
30-day baseline, plus a `warn` flag and message that fire only when friction is
both rising *and* the recent average is elevated. `vp_bootstrap_context` (and
`vp inject`, which shares the path) surfaces this as a `friction_trend` field and,
when the warning fires, appends an actionable nudge (narrow scope / use plan
mode) to the post-bootstrap instructions so the agent acts on it. It is computed
from the **full session history already loaded during bootstrap** — zero extra
I/O.

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
- **Drawer** — individual content chunk (the atomic unit), held in the
  host-local chunk store. Wing, room and hall are classification metadata on
  the chunk, not directories git carries (ADR-014 decision 2)

### Classification

```
internal/palace/metadata.go
```

**Wing detection**: Returns project slug if available, else first path component,
else "unknown".

**Room detection** via `RoomClassifier` (weighted keyword scoring):
1. Custom keywords from `[palace.rooms]` config (Tier 1, unweighted)
2. Filename pattern matching (test files, configs, CI/CD, etc.)
3. Weighted keyword scoring: each room has keywords in three tiers
   (high=1.0, medium=0.6, low=0.3). Content is scored against all rooms;
   the highest-scoring room wins if it exceeds `min_score` (default 0.6).
   Word-boundary matching prevents false positives.
4. Configurable overrides: `[palace.scoring.rooms.*]` in TOML config lets
   users add rooms, override keyword weights, or adjust `min_score`
   without recompiling.
5. Fallback: "general" (when no room scores above threshold)

**Hall detection**: Classifies content into memory type by keyword presence
(decided → decisions, found → discoveries, prefer → preferences, should →
advice, happened → events, default → facts).

### AAAK Compression

```
internal/palace/aaak.go
```

> **PARKED — implemented but not wired into any production path.** AAAK is
> built and unit tested, but no production code invokes it: `Compress` /
> `CompressBatch` have no non-test callers, and no context-loading path in the
> tree produces or consumes an AAAK digest. The code is kept deliberately (it
> may yet earn a caller); the description below is of the format as built, not
> of a capability the running system currently exercises.

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

`BuildGraph` (`internal/palace/graph.go:60`) constructs an in-memory graph from
the host-local chunk store (ADR-014 decision 8):
- **Nodes**: each unique wing/room combination
- **Intra-wing edges**: all rooms in the same wing are adjacent
- **Cross-wing edges** (tunnels): same room slug across different wings

`Traverse(startKey, maxHops)` performs BFS from a starting room,
returning reachable nodes with hop distances. `FindTunnels()` returns
rooms appearing in 2+ wings — these are cross-domain connections.

### Palace Tools

Every palace tool reads the host-local chunk store, so its counts and results
depend on this host's `index_coverage`; with no local index they are empty, and
coverage says why (ADR-014 decision 8).

- `vp_palace_status` — overview: wing/room/drawer counts, tunnel count
- `vp_list_wings` — all wings with room and drawer counts
- `vp_list_rooms` — rooms in a wing with drawer counts and hall distribution
- `vp_traverse` — BFS graph walk from a starting room
- `vp_find_tunnels` — cross-wing room connections
- `vp_palace_query` — drawers filtered by hall / room / `source_type` /
  substring / date range (session dates, ADR-014 decision 4), newest first;
  `source_type` defaults to `decision`

`vp_palace_backfill_decisions` is removed in v10.2.0: every build rebuilds
decision chunks from the session notes' `decisions:` frontmatter, so the tool
has nothing left to do (ADR-014 decision 10).

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
       │   Ingester   │ │  Search    │ │ Context  │
       │  (archives)  │ │  Engine    │ │ Resolver │
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
       │ t │ ┌▼──────────────┐ ┌───────────────┐
       │   │ │ Vault (git)   │ │ Host-local    │
       └───┘ │ sessions      │ │ index         │
             │ transcripts   │ │ chunks        │
             │ authored KG   │ │ vector index  │
             │               │ │ extracted KG  │
             └───────────────┘ └───────────────┘
```

---

## Structured Logging (Phase 7)

### Two Logging Domains

The system distinguishes between **startup crashes** (stderr + exit) and
**runtime degradation** (slog + continue). Fatal startup errors are printed
to stderr by the command that hit them (for example `serveMCP` in
`cmd/vp/cmd_mcp.go`), with a non-zero exit, not only logged. The structured
logger handles runtime best-effort failures.

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
flooding WARN during a bulk re-index.

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
CRUD: `AddEntities`, `AddEntity`, `GetEntity`, `ListEntities`, `AddTriple`,
`QueryEntity`, `InvalidateTriple`, `Timeline`, `KGStats`. Entities are stored
in JSONL, triples as individual JSON files keyed by `{subj}--{pred}--{obj}`.

Two record classes share that format (ADR-014 decision 5). **Authored** facts
(`vp_kg_add`, `vp_kg_invalidate`) stay tracked under `palace/<p>/kg/`, which
after the migration holds authored records only; **extracted** triples and
entities live in the host-local `palace/.local/index/<p>/kg/`. New records
carry an explicit `origin`, `authored` or `extracted`; at migration each
existing record is classified once by the `source_session` plus `extracted_at`
convention, the authored ones are stamped and kept, and the extracted ones are
dropped (ADR-014 decisions 4, 5). The one exception: an extracted triple whose
`valid_to` is set is a pre-migration invalidation edit, so it classifies as
authored and stays tracked, a mempalace triple included. Mempalace triples are
otherwise extracted: the mempalace importer writes its triples and entities only
to the host-local KG, with `origin: extracted` and its import batch as owner,
never to `kg/triples/`, and a mempalace triple is never classified as authored
except through a pre-migration invalidation edit (ADR-014 decision 5).
`AddTriple` is create-once on its S/P/O path
(`internal/storage/knowledge_graph.go:296`, path at
`internal/storage/paths.go:67`): `vp_kg_add` refuses with "already exists"
when the path holds a tracked **authored** record; when it holds a tracked
**extracted** record (possible only before the migration), it overwrites it as
authored; and a host-local extracted copy never blocks it (ADR-014 decision
5). Invalidating an **authored** triple sets `valid_to` on its
tracked file. Invalidating an **extracted** triple writes a tracked authored
overlay at the triple's path, with `origin: authored` and `valid_to` set;
where a tracked and a local record share a key, the tracked one wins.
`QueryEntity`, `Timeline`, `KGStats` and `ListTriples` answer from the
union of tracked and host-local records, behind the data-format read gate that
guards those reads (`checkFormatGate`, `internal/storage/format_gate.go:29`,
called at `internal/storage/knowledge_graph.go:334` and `:420`, and for
`ListTriples` at `internal/storage/project_dirs.go:175`). A v8 binary reads
triples with plain `json.Unmarshal` (`knowledge_graph.go:478`), which ignores
the new field, so the data format stays at 2.

`AddEntities(project, []Entity)` is the entity write path: it takes the
per-path lock, reads and scans `entities.jsonl` **once** to build the dedup
set, and appends only the new lines through `appendUnderLock` (family F4) — so
a batch of N entities costs one read and one write instead of N of each. It
returns the number actually appended and skips duplicates silently, whether
they are already on disk or repeated inside the same batch; an all-duplicate
batch writes nothing at all. `AddEntity` is the n=1 wrapper that turns a skip
back into the `entity %q already exists` error the interactive `vp_kg_add`
tool matches on. This mirrors `AppendDrawers`/`AppendDrawer` in
`internal/storage/drawers.go`, for the same reason.

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

The write-free prepare step, `palace.Prepare` (`internal/palace/prepare.go`), runs entity detection
and triple extraction after chunking, in the pending-archive ingester and in `vp index rebuild`. It
replaced the old capture-time `IndexTranscript` indexer (now removed). Detected entities get `mentioned_in`
triples linking them to the source session. The output is extracted records in the host-local KG,
never the tracked tree (ADR-014 decisions 1, 7). All KG operations are best-effort with slog
logging.

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
make test       # fast unit tests (-race -short, no model download) — then runs live-canary
make live-canary # bootstrap canary, uncached (-count=1); skips cleanly with no vault
make test-full  # full suite including ONNX integration
make integration # integration tests only
make install    # build + install to ~/.local/bin/vp
make cover      # HTML coverage report (short mode)
make clean      # remove build artifacts (preserves model cache)
make dist-clean # remove everything including model cache
```

The list is partial. `make help` or `grep -E '^[a-zA-Z_-]+:' Makefile` lists
every target. Others include `vet`, `fmt-check`, `source-audit`, `model-test`,
`cover-full`, `release`, `snapshot` and `man`.

Binary: `vp` installed to `${PREFIX}/bin/vp` (default `~/.local/bin/vp`).

---

## Adaptive Room Classification (Phase 12)

Phase 12 adds a self-improving classification system built on the
`RoomClassifier` and the `internal/llm` client (see *LLM Client* below).

### Room Audit (`internal/palace/audit.go`)

`vp audit rooms` re-scores every chunk in the host-local store against the current weight table,
flags mismatches and borderline classifications, and reports keyword coverage. On a migrated vault
`--apply` relabels the room on the chunk's metadata in the host-local chunk store and writes nothing
tracked; on an unmigrated vault it refuses. A relabel is not preserved: the next rebuild re-runs the
classifier and undoes it, and the command's help and output say so, so curation lives in the room
keywords (ADR-014 decision 9). This audits a *project's chunk classification* and takes `--project`;
it is a different thing from the vault audit below.

### Vault Audit (`internal/vaultaudit`)

`vp audit vault` (and the MCP `vp_audit_vault`) scans the WHOLE VAULT against
design intent and reports **pass / fail / unknown per dimension**. Full rationale
in ADR-007; the mechanics:

- **The dimensions** (the registry is `dimensions` in `internal/vaultaudit/audit.go`;
  this list is derived from it, and the COUNT is deliberately not restated here —
  ADR-007 is exactly about not storing a value the registry already holds): `archive-roundtrip` (every transcript manifest
  back-links to a session note that exists), `project-tree-coherence` (every project
  appears in both `palace/` and `Projects/`, where a `palace/` directory counts only
  if it is a store, `internal/vaultaudit/dimensions.go:626`; in v10.2.0 it is
  rewritten, unconditionally, on the derived-aware presence predicate, so the
  migrating host and every pulling host answer alike, although a pulled
  migration removes `palace/<p>/` for a project whose only tracked content there
  was derived — ADR-014 decision 11), `kg-tracked-extracted` (new in v10.2.0: on
  a migrated vault it reports tracked extracted KG records — ADR-014 decision
  11), `kg-portability` (unchanged; the filenames of the tracked authored
  KG triples are NTFS/exFAT-safe; ADR-014 decision 5), `resume-discipline`
  (no `resume.md` over the size cap),
  `iteration-headings` (canonical H2 so the iteration counter derives correctly),
  `memory-portability` (no memory filename is unrepresentable on NTFS/exFAT, and none
  collide case-insensitively in one directory), `task-heading-markers` (no H2 in an
  active task file carries an unresolved-status marker `amend` can never revise),
  `task-preamble` (no active task file carries text between its header block and its
  first H2 — the region `vp_manage_task action: overwrite` exists to repair, disjoint
  by construction from `task-heading-markers`, which reads heading TEXT; the predicate
  is `storage.MovePreambleUnderContext`, whose rewritten string is discarded, and a
  file with no usable first H2 is reported as its own degenerate class),
  `task-status-directory` (a task file's `**Status:**` agrees with the directory
  it sits in, which is authoritative: no non-terminal status in `done/` or
  `cancelled/`, and no terminal status in `tasks/`, the signature of a
  rewrite-then-rename crash), `task-file-validity` (every archived task file
  passes the whole-file task validator `storage.ValidateWholeTaskFile`).
  `palace-store-drawers` (registered at `internal/vaultaudit/audit.go:157`;
  it flags a palace store whose drawer store is empty,
  `internal/vaultaudit/dimensions.go:714`) is skipped on a migrated vault in
  v10.2.0: after the migration the tracked tree holds no drawers, and the
  dimension would answer differently on the migrating host and on the pulling
  hosts. The skip is marker-gated, as is `kg-tracked-extracted`; the
  `project-tree-coherence` rewrite is unconditional (ADR-014 decision 11; see
  *Vault Housekeeping*).
- **Advisory — a FAIL exits 0.** It reports; it never blocks. An audit that
  failed the build is an audit people learn to disable.
- **Vault-global — no `project` parameter.** Scoping it per-project is how a
  project nobody has opened escapes scrutiny forever. (Contrast `vp audit rooms`,
  which *is* per-project — the asymmetry is deliberate.)
- **`unknown` ≠ `pass`.** A transcripts dir the auditor could not read is
  `unknown`; `auditArchiveRoundTrip` probes `os.ReadDir` before
  `archive.ListEntries`, whose `filepath.Glob` swallows permission errors.
- **Accepted-debt baseline** (`Audits/baseline.json`): a finding whose
  `(Dimension, Artifact)` pair is accepted is reported as `accepted`, not `new`,
  and does not fail the dimension. It is a DECLARE channel (every entry carries a
  `reason` and an owning task), **may only shrink**, and a fixed-but-still-accepted
  entry goes STALE and fails as loudly as a new finding. Finding identity is
  `(Dimension, Artifact)` only — the message text is never part of the key.
- **Staleness nag** (`staleness.go`): rides in `vp_bootstrap_context`, **silent
  when fresh**, tripping on churn (~50 notes) or age (7 days).

### Archive Backfill (`internal/vaultaudit/backfill.go`, `storage.BackfillArchiveLink`)

The `archive-roundtrip` dimension not only detects a stranded transcript but
distinguishes the **recoverable** ones. A note whose capture key was PUSHED by the
hook (`session_key_source: caller`) carries the harness session id as its
`session_key`, so pairing it with a stranded manifest of the same `session_id` is
an exact **derivation**, not a guess. The candidate predicate lives once in
`backfill.go` and drives three surfaces:

- `vp archive backfill` — read-only; lists each recoverable pair and prints the
  exact `vp archive link <id> -p <project>` that repairs it.
- `vp audit vault` — annotates a recoverable finding's *message* (never its
  identity) with the same command.
- `vp archive link` / `vp_archive_link` — the applier. **Running it IS the human
  approval for that one pair; there is deliberately no bulk mode.** It links the
  newest *stranded* manifest of the session (older siblings stay stranded by
  design), refuses an identity conflict, and stamps provenance
  `archive_session_id_source: backfilled` (distinct from the live path's
  `derived` and the capture-time `inline`). Notes predating `SessionKey` (199)
  never recorded their session and are permanently lost, not backfillable.
  Inline-archive pairs never surface as candidates: their key source is
  `minted`, not `caller`, and they are born linked anyway. See ADR-007.

### LLM-Assisted Tuning (`internal/palace/tune.go`)

`vp tune rooms` samples borderline/mismatched/"general" chunks from the
host-local chunk store, sends them
to an LLM for classification judgment, and proposes keyword weight adjustments
via heuristic rules. Output is a TOML diff. `--estimate` reports token cost
without calling the LLM.

### Keyword Discovery (`internal/palace/discover.go`)

`vp discover rooms` uses an LLM to identify new keywords from unclassified content, then
cross-validates each proposal by scanning every chunk in the host-local chunk store (O(proposals ×
chunks), pure keyword matching). Proposals with negative scores (regressions outweigh captures) are
filtered out.

### LLM Client (`internal/llm`)

LLM clients behind one `Completer` interface (`completer.go`): an
OpenAI-compatible HTTP client (`client.go`, `POST {endpoint}/chat/completions`)
and a native Anthropic backend (`anthropic.go`), with exponential backoff on
transport errors, 429 and 5xx (`retry.go`). No external dependencies beyond stdlib. Used by tune and
discover (configured via `[palace.llm]` in TOML), and also by session
enrichment and the iteration and session-note summarizers.

---

## Roadmap

> **Historical record (marked 2026-09-28).** This is the original phase plan. It
> is not maintained and does not list later work (vault lifecycle, the task
> board, summarization). It is superseded by the project's task files: run
> `vp tasks` for the open backlog.

Phases 7–10, 12–18 are complete (knowledge graph, migration, CLI,
documentation, adaptive room classification, guided onboarding,
palace-scoped resolution, vault template materialization and
reconciliation, transcript-archive provenance ledger, host-level
auto-capture hooks, and the Zed transcript adapter).

| Phase | Goal |
|-------|------|
| 11. Pluggable Embedding Backends | Swap ONNX embedder for API-based alternatives |
