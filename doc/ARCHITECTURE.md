# Architecture: Vibe-Palace

**Last updated:** 2026-05-31

Vibe-palace is a compiled Go binary that serves as an MCP (Model Context
Protocol) server for AI-assisted development. It provides context injection,
session capture, semantic search, and palace-based knowledge navigation through
58 MCP tools over stdio JSON-RPC 2.0.

**Design principles:**
- Single binary, zero-CGo, no external services
- Filesystem-native storage (JSONL, markdown, JSON) — all data human-readable
  and git-mergeable
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
| `internal/tools` | 58 MCP tool implementations | (see tool table below) |
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
| `internal/archive` | Transcript archive / copyright-provenance ledger (per-IDE adapters, manifests, signing) | `Manifest`, `Entry`, `CreateOptions` |
| `internal/archive/zed` | Read-only Zed agent-panel thread DB parser → Claude-shape JSONL | `parser`, `messages`, `types` |
| `internal/migrate` | Import VibeVault sessions + agentctx (resume/iterations/workflow/knowledge/tasks/memory + verbatim `migrated/` archive) and MemPalace data into the vault | `ImportVibeVault`, `ImportMemPalace`, `copyAgentctx` |
| `internal/absorb` | Migrate legacy agent-context files (CLAUDE.md, AGENTS.md, .cursorrules) into the vault | `Planner`, `Classifier`, `Writer` |
| `internal/agentfile` | Detect well-known agent instruction files and wire in a managed bootstrap block | `Detect`, `Wire`, `WireAll` |
| `internal/shims` | Emit Claude Code slash-command shims into `.claude/commands/` | `Plan`, `Apply`, `Shim` |
| `internal/skills` | Directory-form persona artifacts: SKILL.md frontmatter parser/resolver | `Frontmatter` |
| `internal/commands` | Shared command list/upgrade surface over the Resolver | `List`, `Upgrade`, `Diff` |
| `internal/reconcile` | Check → Plan → Apply reconcilers for managed config-file tiers | (per-artifact reconcilers) |
| `internal/templates` | Compiled-in template corpus + materialize/reconcile lifecycle | `Executor`, `Lock` |
| `internal/check` | Doctor checks for config, vault, embedder, git, agent drift, resume.md caps | `Run`, `CheckConfig`, `CheckAgentDrift`, `CheckResumeCaps` |
| `internal/slug` | Project-slug validation and normalization | `Slugify`, `Validate` |

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
        ├── config.toml             # Project-level config overrides
        ├── resume.md               # Project state
        ├── iterations.md           # Append-only archive
        ├── sessions/YYYY-MM-DD-NN.md
        ├── tasks/
        │   ├── {slug}.md
        │   ├── done/{slug}.md
        │   └── cancelled/{slug}.md
        ├── commands/               # Project/wing/room commands
        │   ├── {name}.md          # Project scope
        │   └── {wing}/
        │       ├── .wing/{name}.md   # Wing scope
        │       └── {room}/{name}.md  # Room scope
        └── skills/                 # Same layout as commands/
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
**crash-atomicity** — a reader never sees a torn file. `internal/vaultlock`
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
`internal/storage` / `internal/vaultfs`, where the `Acquire` sites live**: a
higher layer must never read-and-write a vault file itself. The surgical
`resume.md` editors (`vp_thread_*`, `vp_carried_*`) once did exactly that and
silently lost updates; they are now pure `mdutil` transforms handed to
`storage.(*Vault).EditResume`, the locked read→modify→write combinator that owns
the lock and the I/O on their behalf. Inside a held lock, write with raw
`atomicfile.Write`, never `lockedWrite` — re-acquiring the same path is a
blocking `LOCK_EX` with no timeout, i.e. a permanent self-deadlock.

`.vp-locks/` is host-local: registered in `storage.CanonicalGitignorePatterns`
(never synced), refused by `vaultfs.IsRefusedWritePath`, and not indexed. The
lock is unix-only (Windows is a no-op stub); `flock` auto-releases on process
exit, so a leftover `.lock` marker after a crash is harmless. See ADR-003
(`doc/adr/003-vault-write-locking.md`) for the full rationale.

### Configuration: 3-Tier TOML Precedence

Configuration follows a 3-tier override chain:

1. **Embedded defaults** — compiled into the binary via `//go:embed config/defaults.toml`
2. **Vault-level** — `~/.config/vibe-palace/config.toml`
3. **Project-level** — `{vault}/Projects/{project}/config.toml`

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

[palace.rooms.custom]          # project-level only (Tier 1, unweighted)
keywords = ["keyword1", "keyword2"]

[palace.scoring]               # weighted scoring overrides (Phase 12)
min_score = 0.5
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

---

## Vault Housekeeping (tidy)

### The problem: capture churn nobody commits

The hook path (`vp hook` on SessionEnd/Stop) and the MCP capture tools write
session summaries, transcript archives, knowledge-graph entities/triples, and
drawer JSONL across **every** project. (Historically the hook also bumped a
`.surface` provenance stamp on every write; stamps are now byte-stable per surface
version and no longer churn per session — see "The `.surface` status gate" below.)
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
  human so a stray edit or an accidental scaffold gets one round of eyes.

This split is what lets the feature run automatically without ever committing on
the user's behalf for anything it does not positively recognize.

### The data-driven classifier (`sweepRules`)

The heart of tidy is `sweepRules` in `internal/storage/vaulttidy.go` — a table
that is the single source of truth for what gets committed:

| Category | Shape (vault-relative) |
|----------|------------------------|
| Session summaries | `Projects/*/sessions/*.md` |
| Transcript archives | `Projects/*/transcripts/*.{manifest.json,jsonl.zst}` |
| Knowledge-graph entities | `palace/*/kg/entities.jsonl` |
| Knowledge-graph triples | `palace/*/kg/triples/**/*.json` (deep) |
| Drawers | `palace/*/drawers/**/*.jsonl` (deep) |
| Surface stamps | `{Projects/*,palace/*,Templates}/.surface` (status-gated) |

The real vault layout requires deep (`**`) matching: drawers nest as
`palace/<p>/drawers/<p>/<room>/drawers.jsonl` and triples nest arbitrarily under
a source-derived subpath (e.g.
`palace/<p>/kg/triples/.claude/plans/<name>--mentioned_in--<uuid>.json` — triple
paths legitimately contain `.claude/` segments, so the classifier must **never**
exclude them). Go's stdlib `filepath.Match` has no `**`, and `doublestar` is not
a dependency. Rather than add one for ~5 stable rules (decision M1, option B),
each `SweepRule` carries an explicit segment-matcher func over
`parts = strings.Split(vaultRelPath, "/")`. The `Pattern` string on each rule is
documentation only — the human-readable shape the `Match` func implements, kept
for the test table and audit trail. `matchRule` returns the first rule whose
`Match` accepts a path; the rules are mutually exclusive in practice.

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

An untracked `.surface` means a project directory git has never seen: either a
genuinely new project (which gets one round of human eyes on its first capture)
or a stray scaffold from an accidental `vp init` (e.g. `Projects/p/`). Pure
path-globbing would commit `Projects/p/.surface` while reporting its siblings
(`config.toml`, the `commands/`/`skills/` README stubs) — a split-brain commit
that breaks the "stray is flagged, not committed" guarantee. After a project's
first commit its stamps read ` M` and sweep automatically. All other rules sweep
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
| CLI | `vp vault tidy [--dry-run] [--no-push]` | The human / cron-able escape hatch. `--dry-run` prints the swept/reported split without committing; `--no-push` commits locally only; bare invocation commits and pushes. |
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

`Pull` keeps **plain merge semantics**. It deliberately does *not* replicate the
push path's rebase / force-with-lease converge loop: incoming history is merged,
not replayed. It attempts **every** remote and records each outcome in
`RemoteResults` rather than aborting internally, leaving each front-end its own
policy. The CLI `pullAll` is best-effort / continue-all, adds a CLI-only
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
still produces a normal merge conflict, exactly as before.

---

## MCP Server (Phase 2)

### Protocol Layer

Vibe-palace communicates via stdio JSON-RPC 2.0, using `mark3labs/mcp-go` as
the protocol implementation. The `Server` wraps `mcp-go`'s `MCPServer` and
injects the vault reference into every request context.

```
cmd/vp/main.go
├── storage.OpenVaultFromCwd(cwd) # resolve vault (honors cwd .vibe-palace.toml vault_path override)
├── embedder.NewONNX(...)         # load ONNX model
├── search.NewEngine(emb, v, cfg) # create search engine
├── context.NewResolver(v.Root)   # template resolver
├── mcp.NewServer(v)              # create MCP server
├── tools.RegisterAll(...)        # register all 58 tools
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

### 62 MCP Tools

| Tool | Source File | Category |
|------|-----------|----------|
| `vp_bootstrap_context` | context_tools.go | Context |
| `vp_get_command` | command_tools.go | Context |
| `vp_get_skill` | command_tools.go | Context |
| `vp_list_commands` | command_tools.go | Context |
| `vp_list_skills` | command_tools.go | Context |
| `vp_cmd` | cmd_tools.go | Context |
| `vp_skill` | cmd_tools.go | Context |
| `vp_get_skill_section` | skill_section_tool.go | Context |
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
| `vp_get_workflow` | workflow_tools.go | Workflow |
| `vp_get_resume` | workflow_tools.go | Workflow |
| `vp_update_resume` | workflow_tools.go | Workflow |
| `vp_get_knowledge` | workflow_tools.go | Workflow |
| `vp_list_projects` | workflow_tools.go | Workflow |
| `vp_append_iteration` | workflow_tools.go | Workflow |
| `vp_list_tasks` | task_tools.go | Tasks |
| `vp_get_task` | task_tools.go | Tasks |
| `vp_manage_task` | task_tools.go | Tasks |
| `vp_init` | system_tools.go | Project |
| `vp_vault_sync` | vault_tools.go | Vault |
| `vp_vault_tidy` | system_tools.go | Vault |
| `vp_vault_status` | system_tools.go | Vault |
| `vp_search` | search_tools.go | Search |
| `vp_search_cross_project` | search_tools.go | Search |
| `vp_list_learnings` | learning_tools.go | Learnings |
| `vp_get_learning` | learning_tools.go | Learnings |
| `vp_capture_session` | session_tools.go | Session |
| `vp_get_project_context` | session_query_tools.go | Session |
| `vp_search_sessions` | session_query_tools.go | Session |
| `vp_get_session_detail` | session_query_tools.go | Session |
| `vp_get_effectiveness` | session_query_tools.go | Session |
| `vp_get_friction_trends` | friction_tools.go | Session |
| `vp_refresh_index` | search_tools.go | Search |
| `vp_vault_read` | vault_file_tools.go | Vault CRUD |
| `vp_vault_list` | vault_file_tools.go | Vault CRUD |
| `vp_vault_exists` | vault_file_tools.go | Vault CRUD |
| `vp_vault_sha256` | vault_file_tools.go | Vault CRUD |
| `vp_vault_write` | vault_file_tools.go | Vault CRUD |
| `vp_vault_edit` | vault_file_tools.go | Vault CRUD |
| `vp_vault_delete` | vault_file_tools.go | Vault CRUD |
| `vp_vault_move` | vault_file_tools.go | Vault CRUD |
| `vp_ingest_commit_msg` | commit_msg_tools.go | Commit |
| `vp_collect_wrap_state` | wrapstate_tools.go | Wrap state |
| `vp_stamp_iter` | wrapstate_tools.go | Wrap state |
| `vp_preflight_wrap` | wrapstate_tools.go | Wrap state |
| `vp_surface_check` | surface_tools.go | Surface |

All tools except the search-dependent ones are always registered. The nine
search-gated tools — `vp_search`, `vp_search_cross_project`,
`vp_capture_session`, `vp_get_project_context`, `vp_search_sessions`,
`vp_get_session_detail`, `vp_get_effectiveness`, `vp_get_friction_trends`, and
`vp_refresh_index` — require a search engine (embedder must initialize
successfully). The vault-CRUD, commit, wrap-state, and surface-check tools are
filesystem operations and are always registered.

`vp_surface_check` (`surface_tools.go`) is a read-only probe that returns the
same whole-vault surface-compatibility verdict a mutating write is gated
against (`check.CheckSurface(vault.Root)`), so restart/wrap templates can run a
surface preflight without shelling out to `vp check`.

The table above enumerates the primary tool surface; for brevity it omits the
five `vp_memory_*` tools (`memory_tools.go`) and `vp_read_resource`
(`resource_read_tool.go`), which are also always registered. Counting those,
the registry (`internal/tools/register.go`) exposes **62 tools with a search
engine and 53 without it** — the numbers pinned by `internal/tools/register_test.go`.

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
- **Read-only by default.** The 20 vault-mutating tools (`tools.MutatingToolNames`)
  are stripped from this instance via `Server.DeleteTools` unless `--allow-writes`
  is passed, so they are absent from both `tools/list` and `tools/call`. Exposing
  writes prints a second startup warning.
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

`--check NAME[,NAME...]` runs only the named check(s) via the `checkProducers`
map — a selective-execution path that skips the expensive embedder load and
tool-registry build. Registered names:

| Name | Row | Scope |
|------|-----|-------|
| `surface` | `Surface` | Whole vault — binary MCP surface vs. max `.surface` stamp |
| `resume-caps` | `Resume caps` | Whole vault — every `Projects/*/resume.md` |

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
├── workflow.md                    # Pair programming workflow rules
├── resume.md                      # Project state template
└── commands/
    ├── capture.md                 # Session capture instructions
    ├── restart.md                 # Context restoration instructions
    ├── review-plan.md             # Plan review instructions
    ├── cancel-plan.md             # Plan cancellation instructions
    └── wrap.md                    # Session wrap-up instructions
```

Templates support `{{PROJECT}}`, `{{DATE}}`, `{{WING}}`, and `{{ROOM}}`
variable expansion. `internal/context/precedence.go` consumes the
embedded tree via `internal/templates.FS()`.

### Materialize-and-reconcile loop

The embedded tier is the *floor*, not the authoritative source for a
populated vault. Templates flow through four stations:

1. **Embedded-in-binary.** `internal/templates/templates/` is baked into
   every `vp` build. `internal/templates.WalkEmbedded()` enumerates
   every resource as `(RelPath, Bytes, SHA256)`.
2. **Materialize on `vp init`.** First-run `vp init` walks the embedded
   set and writes every resource into `<vault>/Templates/` (the
   `TemplateTreeReconciler` running in `Materialize` mode). It also
   scaffolds `<vault>/Projects/<slug>/{commands,skills}/` with a README
   stub (`Scaffold` mode — directory + README, no per-file copy), writes
   the `<vault>/.vibe-palace/templates.lock` sidecar, and adds `*.bak`
   and `*.new` to `<vault>/.gitignore` via
   `storage.ReconcileVaultGitignore`. It also reconciles the *consuming
   project's* repo-root `.gitignore` via
   `storage.ReconcileProjectGitignore`, appending the host-local AI
   artifacts vp writes into the project tree
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
3. **User edits freely.** The vault is git-managed. Users treat
   `<vault>/Templates/commands/*.md` as source-of-truth for their
   personal command phrasing.
4. **Reconcile on `vp config sync`.** Default scope is all-projects.
   The reconciler reads the lock, hashes each vault file, and consults
   the current embedded SHA for each resource. Drift resolves via the
   three-SHA decision table below. Manual copy-back to the vibe-palace
   source checkout is how a vault-side edit becomes the next release's
   embedded floor — `vp` cannot automate this because at runtime it
   does not know where the user's source checkout lives.

#### The three-SHA decision table

For each `(lock entry, vault file SHA, embedded file SHA)` triple, the
reconciler picks one action:

| Vault vs lock | Embedded vs lock | Action |
|---|---|---|
| missing       | —                | **Create** (write embedded, record lock) |
| match         | match            | **Unchanged** |
| match         | differs          | **Update** (safe: user never edited) |
| differs       | match            | **Unchanged** (user-edited, embedded stable) |
| differs       | differs          | **Prompt** (skip / overwrite + `.bak` / write `.new`) |
| lock missing  | vault exists (bytes == embedded) | **Silent adopt** (write lock entry, no prompt) |
| lock missing  | vault exists (bytes ≠ embedded)  | Treat as user-edited → **Prompt** |

`ActionPrompt` carries all three SHAs in its `Details` so the
orchestrator can render the three-option menu without re-reading files.
The orchestrator rewrites the user's choice (`s`/`o`/`n` or uppercase
batch variants `S`/`O`/`N`) into a concrete Create / Update /
WriteAsNew action before Apply runs — Apply itself never touches
stdin/stdout.

#### Role of `templates.lock`

`<vault>/.vibe-palace/templates.lock` (TOML, keyed by vault-relative
path) records the embedded SHA each resource was last materialized or
reconciled against. Without it, the reconciler cannot distinguish "user
edited this file" from "embedded default changed under a file the user
never touched" — every binary bump would degrade into a prompt-per-file
UX. The lock makes the auto-Update branch safe: `vault == lock` is
unambiguous evidence the user has not edited the file, so replacing it
with the new embedded bytes (after writing `.bak`) cannot clobber user
intent.

#### Two upgrade entry points

The codebase exposes **two** upgrade surfaces with deliberately
different UX contracts:

1. **Three-SHA reconcile** (`vp config sync`, `vp init`) —
   `internal/reconcile/template_tree.go`. Compares vault SHA, lock SHA,
   and embedded SHA for every materialized file (commands + skills).
   When the vault has drifted and the embedded floor has also shifted,
   it prompts `[s]kip / [o]verwrite (writes .bak) / [n]ew-sidecar`.
   Runs automatically on `vp init` and `vp config sync`.
2. **Two-SHA diff** (`vp commands upgrade`, `vp skills upgrade`) —
   `internal/commands/upgrade.go`. Compares embedded vs vault only,
   renders a unified diff per change, and prompts
   `[a]ccept / [s]kip / [A]ccept-all / [q]uit`. Skills collapse per-file
   changes into one prompt per skill directory unless `--granular` is
   passed. Invoked interactively by the user.

Both paths resolve to the same vault content; they differ in whether
drift is handled silently (path 1 adopts matches, prompts on conflict)
or explicitly (path 2 always shows a diff and asks). The shared prompt
loop — `runUpgradePrompt` in `cmd/vp/upgrade_common.go` — is used by
both `vp commands upgrade` and `vp skills upgrade`; identity `GroupBy`
preserves the per-change prompt for commands while the skills path uses
`skillGroupID` to collapse files under each skill directory.

#### Silent-adopt pre-pass

On the first post-upgrade sync against a vault that predates this
feature (or on any vault with an absent lock), a naive implementation
would emit a Prompt for every vault file that exists but has no lock
entry. To avoid that prompt-storm, `Plan` runs a pre-pass: for every
existing file under the reconciler's `relSubpath`, it compares the
vault SHA to the current embedded SHA. Byte-identical files get a lock
entry written silently — no prompt, no `.bak`. Only genuinely divergent
files fall through to the Prompt path. "Vault bytes == embedded bytes"
is unambiguous evidence the user has not edited the file, so adoption
is safe.

### Bootstrap Context

`vp_bootstrap_context` is the primary entry point for AI context restoration.
A single call assembles: workflow rules, project resume, active tasks, recent
sessions, KG snapshot, and available commands/skills. This replaces the
multi-file CLAUDE.md pattern used by legacy systems.

### Commands and Skills

**Commands** are instructions for the AI to execute immediately (e.g.,
"capture this session"). **Skills** are behavioral guidelines applied
throughout a session (e.g., "pair programming mode"). Both resolve via
the 5-tier palace-scoped precedence system (room > wing > project > vault >
embedded) and can be listed or invoked by name. When wing/room are not
specified, resolution falls back to the 3-tier project > vault > embedded
chain.

### Three-target shim system (`internal/shims/`)

vibe-palace emits native shim files into the editor's own surfaces so
users can invoke commands and skills without leaving the tool they
already know. One `TargetKind` enum drives three emission paths, all
sharing the managed-hash atomic-write protocol (tmp + fsync + rename
with a `<!-- vibe-palace:shim v=N sha=7hex -->` … `<!-- vibe-palace:shim-end -->`
region that identifies vibe-palace-owned content; files without the
marker are "custom" and never touched).

| Target          | Location                             | Body                                           |
|-----------------|--------------------------------------|------------------------------------------------|
| `ClaudeCommand` | `.claude/commands/vpc-<name>.md`     | Delegates to `vp_command` MCP tool             |
| `ClaudeSkill`   | `.claude/skills/vps-<name>/SKILL.md` | Delegates to `vp_skill`; teaches additive-stack contract |
| `CursorRule`    | `.cursor/rules/vps-<name>.mdc`       | Delegates to `vp_skill` with vault-path fallback |

- **Plan / Apply** for commands (`Plan` + `Apply`) and for skill-class
  targets (`PlanSkills` + `ApplySkills`) each classify on-disk files as
  New / Modified / Unchanged / Stale / Custom and compute the minimal
  rewrite set. Per-target content hashes are keyed on render inputs
  (name, description, paths, target kind, template version) so identical
  inputs yield a stable `sha=` token across runs, and changes to any
  input force a rewrite on next apply.
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
`doc/COMMANDS-AND-SKILLS.md`) validates the `name`, `description`,
and `version` fields that the downstream shim renderers and the
Claude Code / Cursor skill pickers all depend on.

**Shims.** On top of the resolver sits `internal/shims/`, which
emits native artifacts into the editors that expose a first-class
skill surface. `ClaudeSkill` writes
`.claude/skills/vps-<name>/SKILL.md` — a three-line delegation to
`vp_skill` wrapped in the managed-hash shim marker — so Claude Code's
skill picker auto-loads it. `CursorRule` writes
`.cursor/rules/vps-<name>.mdc` (only when `.cursor/rules/` or
`.cursor/` already exists at the project root), giving Cursor's
Rules panel a native entry with a vault-path fallback for MCP-less
setups. Every shim carries a render-input-keyed `sha=` token in its
marker so drift detection is exact; files without the marker are
"custom" and never touched. Editors without a native surface rely on
the managed-block trigger phrase (`vps-<name>`) plus `vp_skill` over
MCP, which is the universal fallback documented in
`doc/verify-skill-delivery.md`.

**Upgrade.** Skills flow through both upgrade entry points
described above. Three-SHA reconcile (`vp init`, `vp config sync`)
keeps `<vault>/Templates/skills/` materialized and in sync with the
embedded floor, using the lock sidecar to distinguish user edits
from binary bumps. Two-SHA interactive diff (`vp skills upgrade`)
compares embedded vs vault directly and groups every file under a
skill directory into a single `a`/`s`/`A`/`q` prompt — a six-file
skill like `startup-analyst` becomes one decision, with `--granular`
available when per-file review is actually wanted. The shim side is
kept in lockstep via `vp commands upgrade`'s `PlanSkills` /
`ApplySkills` pair, which re-renders `.claude/skills/` and
`.cursor/rules/` entries whenever the SHA-token inputs change. The
resolver is the source of truth, the shims are the IDE-native
surfaces, and the two upgrade paths keep both sides coherent without
ever committing to the user's behalf.

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

The exactness claim is guarded by an in-repo recall harness
(`internal/search/recall_test.go`) that loads a constitution corpus embedded
with synthetic TF-IDF vectors and asserts every returned distance stays within
the exhaustive ground-truth bound.

The PRD specified HNSW (Hierarchical Navigable Small World), but `coder/hnsw`
had critical recall bugs (Heap.Max/PopLast returning wrong elements, 0–2/10
recall). The three core recall bugs were fixed in our fork and merged upstream
into `coder/hnsw@main` (2026-06-22), then validated against our in-repo recall
harness (recall@10 0.96). Brute-force remains the exact, zero-dependency default
at current scale; HNSW is parked behind the same index boundary for a future
scale trigger.

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

Sessions are captured via two paths, both using the shared pipeline
`capture.WriteSession`:

- **MCP path**: `vp_capture_session` tool — AI-generated summary, full
  transcript indexing (chunking + embedding + KG extraction). Writes a
  claim sentinel so the hook path skips this session.
- **Hook path**: `vp hook` CLI — Claude Code invokes this on SessionEnd,
  Stop, and PreCompact events. Produces a deterministic auto-summary from
  `git log`, runs friction analysis, but defers transcript indexing
  (`needs_indexing: true` in frontmatter). Skips if a claim sentinel exists.

`vp hook` auto-capture requires a `.vibe-palace.toml` (run `vp init`); sessions
in un-init'd directories are intentionally skipped.

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
4. Archive transcript to {vault}/Projects/{project}/transcripts/ (hook path)
5. Cross-link archive manifest ↔ session note (bidirectional)
6. Write claim sentinel to {cwd}/.vibe-palace/ (idempotency)
```

### Hook Installation

`vp hook install` manages entries in `~/.claude/settings.json`, replacing
legacy `vv hook` (vibe-vault) with `vp hook`. `vp init` calls this
automatically. The hook fires on three Claude Code events (SessionEnd,
Stop, PreCompact) with a 30-second timeout.

### Session Enrichment (LLM synthesis)

The hook path's deterministic auto-summary is a `git log` dump — poor memory for
bootstrap, search, and a resuming developer. When the opt-in `[enrichment]`
config block is enabled, an LLM synthesis pass replaces that heuristic
summary/decisions/open-threads/tag with a real synthesis. It sits between the
transcript and `WriteSession`, and is wired only into the SessionEnd hook and the
`vp_capture_session` `enrich` param (default off — agent-authored `/wrap` notes
are already good).

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
and a drained note converge to byte-identical bodies. See ADR-005
(`doc/adr/005-llm-enrichment-synthesis.md`) for the full rationale.

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
- **Drawer** — individual content chunk (the atomic storage unit)

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

## Adaptive Room Classification (Phase 12)

Phase 12 adds a self-improving classification system built on the
`RoomClassifier` and a thin OpenAI-compatible LLM client.

### Audit (`internal/palace/audit.go`)

`vp audit rooms` re-scores every drawer against the current weight table,
flags mismatches and borderline classifications, and reports keyword coverage.
`--apply` uses `MoveDrawer` (atomic temp-file + rename) to reclassify and
rebuild the search index.

### LLM-Assisted Tuning (`internal/palace/tune.go`)

`vp tune rooms` samples borderline/mismatched/"general" drawers, sends them
to an LLM for classification judgment, and proposes keyword weight adjustments
via heuristic rules. Output is a TOML diff. `--estimate` reports token cost
without calling the LLM.

### Keyword Discovery (`internal/palace/discover.go`)

`vp discover rooms` uses an LLM to identify new keywords from unclassified
content, then cross-validates each proposal by scanning all drawers
(O(proposals × drawers), pure keyword matching). Proposals with negative
scores (regressions outweigh captures) are filtered out.

### LLM Client (`internal/llm/client.go`)

Thin OpenAI-compatible HTTP client (`POST {endpoint}/chat/completions`).
Handles 429 rate limiting with exponential backoff. No external dependencies
beyond stdlib. Used by both tune and discover workflows. Configured via
`[palace.llm]` in TOML (independent of vibe-vault's enrichment config).

---

## Roadmap

Phases 7–10, 12–18 are complete (knowledge graph, migration, CLI,
documentation, adaptive room classification, guided onboarding,
palace-scoped resolution, vault template materialization and
reconciliation, transcript-archive provenance ledger, host-level
auto-capture hooks, and the Zed transcript adapter).

| Phase | Goal |
|-------|------|
| 11. Pluggable Embedding Backends | Swap ONNX embedder for API-based alternatives |
