# Architecture: Vibe-Palace

**Last updated:** 2026-07-28

Vibe-palace is a compiled Go binary that serves as an MCP (Model Context
Protocol) server for AI-assisted development. It provides context injection,
session capture, semantic search, and palace-based knowledge navigation through
its full MCP tool surface (versioned in `internal/mcp/tool_surface.golden.json`,
`surface_version: 2`) over stdio JSON-RPC 2.0.

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
| `internal/vaultaudit` | Vault audit (5 dimensions), accepted-debt baseline, staleness nag, and the archive-backfill remediation predicate (ADR-007) | `Run`, `Baseline`, `BackfillCandidates`, `ApplyBackfill` |
| `internal/migrate` | Import VibeVault sessions + agentctx (resume/iterations/workflow/knowledge/tasks/memory + verbatim `migrated/` archive) and MemPalace data into the vault | `ImportVibeVault`, `ImportMemPalace`, `copyAgentctx` |
| `internal/absorb` | Migrate legacy agent-context files (CLAUDE.md, AGENTS.md, .cursorrules) into the vault | `Planner`, `Classifier`, `Writer` |
| `internal/agentfile` | Detect well-known agent instruction files and wire in a managed bootstrap block | `Detect`, `Wire`, `WireAll` |
| `internal/shims` | Emit Claude Code slash-command shims into `.claude/commands/` | `Plan`, `Apply`, `Shim` |
| `internal/skills` | Directory-form persona artifacts: SKILL.md frontmatter parser/resolver | `Frontmatter` |
| `internal/commands` | Shared command list/upgrade surface over the Resolver | `List`, `Upgrade`, `Diff` |
| `internal/reconcile` | Check → Plan → Apply reconcilers for managed config-file tiers | (per-artifact reconcilers) |
| `internal/templates` | Compiled-in template corpus (incl. the agent doctrine, `templates/doctrine.md`) + materialize/reconcile lifecycle | `Executor`, `Lock` |
| `internal/worktree` | Git-worktree isolation for plan execution (`vp worktree create\|remove\|list`) | `Create`, `Remove`, `List` |
| `internal/check` | Doctor checks for config, vault, embedder, git, agent drift, resume.md caps, host-rooted paths, template drift and the deleted `vp-surface` merge driver | `Run`, `CheckConfig`, `CheckAgentDrift`, `CheckResumeCaps`, `CheckVaultAbsPaths`, `CheckSurfaceMergeDriver` |
| `internal/slug` | Project-slug validation and normalization | `Slugify`, `Validate` |

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
lock is unix-only (Windows is a no-op stub); `flock` auto-releases on process
exit, so a leftover `.lock` marker after a crash is harmless. See ADR-003
(`doc/adr/003-vault-write-locking.md`) for the full rationale.

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

## MCP Server (Phase 2)

### Protocol Layer

Vibe-palace communicates via stdio JSON-RPC 2.0, using `mark3labs/mcp-go` as
the protocol implementation. The `Server` wraps `mcp-go`'s `MCPServer` and
injects the vault reference into every request context.

```
cmd/vp/main.go
├── storage.OpenVaultFromCwd(cwd) # resolve vault (honors cwd .vibe-palace.toml vault_path override)
├── embedder.NewLazy(NewONNX...)  # DEFER the ONNX model load — no I/O here
├── search.NewEngine(emb, v, cfg) # create search engine (no indexes built yet)
├── context.NewResolver(v.Root)   # template resolver
├── mcp.NewServer(v)              # create MCP server
├── tools.RegisterAll(...)        # register the full tool surface
└── srv.Serve(ctx)                # start stdio transport
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
  projects the session never searched. It is gone; indexes are built lazily
  (below).

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
    Name:        "vp_search",
    Description: "Semantic search within a project's knowledge base.",
    Schema:      searchSchema,       // JSON Schema for params
    Handler:     searchHandler,      // func(ctx, params) → (any, error)
})
```

On dispatch, the registry validates incoming params against the compiled
schema before calling the handler. Handlers extract the vault from context
and operate on storage directly.

### MCP Tools

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
| `vp_get_doctrine` | context_query_tools.go | Context |
| `vp_manual` | manual_tool.go | Context |
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
| `vp_get_iteration` | get_iteration_tool.go | Workflow |
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
| `vp_audit_vault` | audit_tools.go | Integrity |
| `vp_archive_link` | archive_tools.go | Integrity |

All tools except the search-dependent ones are always registered. The nine
search-gated tools — `vp_search`, `vp_search_cross_project`,
`vp_capture_session`, `vp_get_project_context`, `vp_search_sessions`,
`vp_get_session_detail`, `vp_get_effectiveness`, `vp_get_friction_trends`, and
`vp_refresh_index` — require a search *engine* (`engine != nil`). They do **not**
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

The table above enumerates the primary tool surface; for brevity it omits
several always-registered tools — the five `vp_memory_*` tools
(`memory_tools.go`), `vp_read_resource` (`resource_read_tool.go`),
`vp_archive_commit_log`, and the read-only probes documented in their own
sections (`vp_check`, `vp_scan_plans`). The authoritative
enumeration is the full tool surface versioned in
`internal/mcp/tool_surface.golden.json` (`surface_version: 2`), pinned by
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
| `vault-filesystem` | `Vault filesystem` | Whole vault — does the filesystem accept `:` in filenames (NTFS/exFAT) |
| `stray-scaffolds` | `Stray scaffolds` | Whole vault — scaffold-only orphan projects under `Projects/` |
| `resume-caps` | `Resume caps` | Whole vault — every `Projects/*/resume.md` |
| `resume-refs` | `Resume refs` | Whole vault — host-local plan refs in every `Projects/*/resume.md` |
| `vault-abs-paths` | `Vault abs paths` | Whole vault — host-rooted absolute paths in every project's `resume.md` + `workflow.md` |

The table is ordered as `check.ProducerOrder` declares, which is the order a
default (unfiltered) run emits. Re-derive it from that slice rather than trusting
this table: it went stale once already, when `vault-filesystem` and
`stray-scaffolds` joined the registry and nothing updated it here.

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

#### Whole delivery, and the one axis that remains (310)

The payload is **unconditional**: `resume` and `workflow` are inlined whole, and
there is no token budget, no shed ladder, no workflow digest and no pin/disposable
marker vocabulary. All of it was deleted in `first-principles` Phase 2 — see
ADR-009, which is superseded in full and kept only as a historical record.

What survives is the transport contract, because the remaining risk is not vp's:

- **`complete`** is the last field of the payload and carries no `omitempty`, so
  it arrives on every whole result and on no cut one. An agent that does not see
  it knows its HOST truncated the result.
- **`resume_uri`, `workflow_uri`, `resume_sha256`** lead the payload, ahead of the
  bulk they point at — a recovery handle that arrives only when the body already
  fit is not a recovery handle.
- **Instruments before bulk.** Health, vault staleness, friction and alerts sit in
  the region a host preview keeps, and all of them are silent when healthy.

`TestBootstrapLiveVaultStillRestoresASession` asserts this against the real vault,
including the negative half: no `budget`, `shed_core` or `max_tokens` on the wire.

#### Wire order and the `complete` sentinel (transport contract)

The budget above is vp's report about *its own* reduction. It says nothing
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
  cut order. `project`, `budget`, `resume_uri`, `workflow_uri`,
  `resume_sha256`, `active_task_count`, the compact alerts and
  `post_bootstrap_instructions` are declared **before** the bulk
  (`workflow`, `resume`, `active_tasks`, …). Every bulk field is re-fetchable
  through a handle declared above it. `resume_sha256` sits with the URIs
  rather than beside `resume` because an agent rehydrating from the URI needs
  the digest to CAS-verify what it pulled.
- **`complete: true` is the last field, and carries no `omitempty`.** Its
  ABSENCE is the signal: present ⇒ every byte arrived; absent ⇒ the transport
  cut the payload, whatever the host did or did not say. It asserts nothing
  about *content* — shedding is reported by `budget` — only that this JSON
  document is the whole document vp emitted. Reordering alone could not do
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
tool result. A survey against the live vault
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
the bootstrap payload: the doctrine is generic and stable, so paying its bytes
on every session start would tax exactly the budget ADR-009 protects. The
embedded `workflow.md` template is correspondingly **thin** — project-specific
patterns plus a pointer at the doctrine — rather than carrying the full manual
inline. Resolution follows the normal precedence tiers, so a project may
override the embedded doctrine with its own copy; the tool's result always
carries the `doctrine_uri` so a host whose channel truncates the inline body
can page the full text.

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
1. Embed query text → query vector          (forces the lazy model load, once)
2. Ensure index    → build this project's index if it has never been built
3. Vector search → top limit×3 candidates (over-fetch for filtering)
4. Metadata filter → remove non-matching wing/room/hall/date
5. Score conversion → 1 / (1 + cosine_distance)
6. Structural boost → multiply score for matching filters
7. Deduplicate → keep highest-scored per SourceRef
8. Return top-N
```

### Lazy Index Construction

There is **no reindex on spawn**. A project's `VectorIndex` is built by
`ensureIndex` on the first search that touches it, and the result is memoized:
subsequent searches for that project reuse it. Concurrent searches for the same
project join one in-flight build (`projectBuild`) rather than each running their
own `Rebuild`; builds for *different* projects run concurrently, because the
build mutex is never held across a `Rebuild`. A project whose index was already
populated incrementally (`IndexDrawers`, from the capture pipeline) is marked
built and never rebuilt from disk.

A cross-project search — one with no `Project` filter — iterates every index in
the engine, so it calls `ensureAllIndexes`, which builds every project the vault
knows about. That is the expensive case, and it is paid only by callers who
actually ask for it.

A build **failure is returned to the caller**, never swallowed. This is
load-bearing: before `ensureIndex` existed, a search against a project with no
index took a `return nil, nil` branch — an empty result and a nil error. With no
eager rebuild left to hide it, every agent on every fresh session would have been
told, plausibly and silently, that the vault contains nothing.
`internal/integration/lazy_startup_test.go` pins the positive case: a
never-rebuilt project returns real hits through a real JSON-RPC `vp_search`.

`Rebuild` embeds cache-miss drawers with `EmbedBatch` in chunks of
`EmbedderBatchSize` (default 32), writing each vector to the embed cache as it
lands, so a rebuild killed partway through leaves durable progress behind.
Per-drawer embedding — the old behavior — is roughly 7x slower on the ONNX
backend.

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
  transcript indexing (chunking + embedding + KG extraction). When the host
  session id was **derived**, writes a claim sentinel so the hook path skips
  this session (a minted inline id gets no claim — no hook will ever query it).
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
4. Archive transcript to {vault}/Projects/{project}/transcripts/ (hook path,
   or the MCP path's inline archive — default for derived hook-less hosts;
   see below)
5. Cross-link archive manifest ↔ session note (bidirectional): the note is
   stamped with `archive_session_id` and linked to the manifest at SessionEnd,
   after `archive.Create`; the manifest back-links the canonical note. A link
   that never closes leaves a *stranded* transcript — detected by the vault
   audit's `archive-roundtrip` dimension and recovered by `vp archive backfill`
   / `vp archive link` (see ADR-007). The inline path never strands: its
   archive is created *before* the note, so both link directions exist at birth.
6. Write claim sentinel to {cwd}/.vibe-palace/ (idempotency; derived host
   session ids only)
```

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

Hook-less MCP hosts (Grok Build, the Zed assistant pane, and other clients
without a SessionEnd hook) have no on-disk host transcript for `vp hook` to
archive. Durability for those hosts is **MCP capture + inline archive** — not
an alternate "shim path." Shims (`.grok/plugins/...`, `/vpc-wrap`) are UX onto
`vp_cmd` / `vp_capture_session`; the archive is created inside the capture
handler.

**Default (auto-on).** When all of the following hold, `vp_capture_session`
creates an inline archive pair even if the caller omits `archive_transcript`:

1. The server cannot derive a host session id (empty archive session id).
2. The caller supplied a **non-empty** `transcript`.
3. The MCP initialize handshake **derived** a host in the known hook-less set
   (`grok` / `xai` / `zed` — case-insensitive substring match; Claude-shaped
   names never match).

Unknown or declared-only hosts do **not** auto-on (declared is spoofable;
unknown is Claude-miss-shaped). Empty transcript never archives.

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
  proceeds and the note is written unlinked.
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
and a drained note converge to byte-identical bodies. Hook-less hosts never run
that drain — see the Strategy A limitation under inline archive above. See
ADR-005 (`doc/adr/005-llm-enrichment-synthesis.md`) for the full rationale.

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
make test       # fast unit tests (-race -short, no model download) — depends on live-canary
make live-canary # bootstrap canary, uncached (-count=1); skips cleanly with no vault
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

### Room Audit (`internal/palace/audit.go`)

`vp audit rooms` re-scores every drawer against the current weight table,
flags mismatches and borderline classifications, and reports keyword coverage.
`--apply` uses `MoveDrawer` (atomic temp-file + rename) to reclassify and
rebuild the search index. This audits a *project's drawer classification* and
takes `--project`; it is a different thing from the vault audit below.

### Vault Audit (`internal/vaultaudit`)

`vp audit vault` (and the MCP `vp_audit_vault`) scans the WHOLE VAULT against
design intent and reports **pass / fail / unknown per dimension**. Full rationale
in ADR-007; the mechanics:

- **Five dimensions:** `archive-roundtrip` (every transcript manifest back-links
  to a session note that exists), `project-tree-coherence` (every project appears
  in both `palace/` and `Projects/`), `kg-portability` (KG triple filenames are
  NTFS/exFAT-safe), `resume-discipline` (no `resume.md` over the size cap),
  `iteration-headings` (canonical H2 so the iteration counter derives correctly).
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
