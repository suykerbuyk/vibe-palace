# ADR 014: The Vault Holds Authored Artifacts; Each Host Compiles Its Search Index from Them

**Status:** Accepted (2026-10-02)
**Deciders:** Project owner
**Context:** Epic `host-local-hnsw-index-rebuilt-from-vault-artifacts` (this ADR is its unit 0),
including its sections *Rulings on the 2026-10-02 adversarial review*, *Ruling on unarchived
content*, *Rulings on the HNSW track after the children review* and *Rulings during the spec and
children reviews*. Where two of those sections disagree, the later one wins. Also:
- the adversarial review of that epic (implementor2, 2026-10-02);
- the children review (implementor3, 2026-10-02);
- operator memory `search-indexes-are-per-host-derived-data-not-vault-content`;
- the measurements in task `measure-backfill-drawer-file-sizes-and-cap-room-files`.

Every code citation below is at `a32a2d4`, except citations prefixed `coder/hnsw@36cab60`, which
are upstream library code.

**What this ADR supersedes in the PRD.** This is the one complete list; the PRD points here. As
they stood at `a32a2d4`, it supersedes:
- PRD §7.1, §7.2, §7.3, §7.4, §7.8, §7.9 and §7.10;
- the "deferred" clause of PRD Decision D5.

It amends no earlier ADR's decision.

## Context

The April 2026 PRD was half right. It kept the vector index machine-local and out of git, chose
`coder/hnsw` (D5), and made rooms a filter on a hybrid search. It was wrong to treat
`palace/<p>/drawers/<wing>/<room>/drawers.jsonl` as the **source** of a project's knowledge, and
to size a room at 10–500 drawers and 10–500 KB. Capture then wrote every transcript chunk into
those files (`internal/capture/indexer.go:129`) and every extracted knowledge-graph triple into its
own tracked file (`indexer.go:232`, `:253`). Tidy commits all of it as capture artifacts
(`internal/storage/vaulttidy.go:84`, `:92`, `:96`), untracked files included (`:200-202`). The
"source" was a cache, and the cache outgrew git:

- **Blob limits.** The MDH GitLab refuses blobs over 20 MiB; GitHub refuses them over 100 MB. The
  quantum split's copy of `qa-metabuild-system` was refused for one 21.4 MiB room file. A partial
  full backfill on a copy wrote room files of 223 MiB and 204.7 MiB.
- **File counts.** A full backfill would add 0.7 to 2.2 million tracked triple files.
- **Churn.** Any change to chunking, room classification, extraction or the embedder rewrites all
  of it in the vault's history.

Search is the other half. The shipped index is an exact brute-force scan
(`internal/search/vector_index.go:25-30`). A cold search builds a project's index by embedding
every drawer on disk (`ensureIndex` → `Rebuild`, `internal/search/engine.go:101`, `:504`). A
project with nothing indexed answers with a successful empty list: the handler turns nil into `[]`
(`internal/tools/search_tools.go:155-157`), which reaches the client as `{"items":[]}`
(`internal/mcp/tools.go:764-766`).

What sits under `palace/` today, measured at the live vault's HEAD:

| records | count | source |
|---|---|---|
| transcript drawers whose session has a tracked archive | 65,814 (60.9 MiB) | the archive |
| transcript drawers whose session has **no** tracked archive | 23,632 (22.5 MiB) | none tracked |
| decision drawers | 5,841 (2.3 MiB) | session-note `decisions:` frontmatter |
| extracted triples whose session has a tracked archive | about 18,500 | the archive |
| extracted triples whose session has **no** tracked archive | 41,364 (69%) | none tracked |
| `vp_kg_add` triples | 1 | authored |
| `entities.jsonl` lines | 18,148, of which 2 from `vp_kg_add` | mixed |

The no-archive records come from more than the 2026-05-12 vibevault import. They also include
`zed:` and `zed-mcp:` sessions, uuid sessions with no manifest, and vibe-palace drawers dated as
late as 2026-09-08.

## Decision

**1. The vault holds authored and immutable artifacts. Each host compiles its search index from
them**, the way a build compiles a binary from source.

Tracked, and the sources of search:

- `Projects/<p>/sessions/*.md`, with their `decisions:` frontmatter, `iterations.md`, tasks and
  learnings;
- `Projects/<p>/transcripts/*.jsonl.zst` and their manifests;
- authored knowledge-graph facts (decision 5);
- `palace/<p>/.surface`. The surface gate reads it as one of its stamp directories
  (`CheckCompatible`, `internal/surface/version.go:807-812`).
  - It also keeps `palace/<p>/` present, because presence and the departure guard key on any file
    outside `.local/`, not on `.surface` by name (`internal/storage/projects.go:178`, `:201`;
    `internal/storage/departure_guard.go:73-90`);
- `palace/<p>/iteration-summaries/`. These are derived, but non-deterministic and paid for with an
  LLM call, so they stay tracked as today (`internal/storage/paths.go:376`).

Never again written into the tracked tree:

- transcript drawers and decision drawers;
- extracted entities and triples;
- the ingest ledger.

**2. The compiled form lives under the already-ignored `palace/.local/`** (canonical ignore line
at `internal/storage/git.go:30`):

```
palace/.local/index/<p>/chunks.jsonl       chunk text and metadata
palace/.local/index/<p>/kg/                extracted triples and entities
palace/.local/index/<p>/ledger.jsonl       per session or import batch: live source, chunk count,
                                           UTC start day, failures, generation; the baseline set
palace/.local/index/<p>/chunks.fingerprint indexer version, chunker, room keywords, extractor
palace/.local/index/<p>/completeness.json  what is built, and the persistent stale flag (decision 8)
palace/.local/index/<p>/hnsw.idx           HNSW graph and its graph fingerprint (library version,
                                           dims, M, EfSearch), in one envelope (decision 6)
palace/.local/index/.generation/<p>        the store's change counter (decision 7); outside <p>/,
                                           so a discard never resets it
palace/.local/embed-cache/<p>/             per-chunk vectors, now written atomically (decision 7)
palace/.local/locks/                       the index run lock and the index commit locks, per host
                                           per vault (decision 7)
```

Wings, halls and rooms remain as classification metadata on chunks (the `internal/palace`
classifier). They are not directories that git has to carry.

The ingest ledger is now kept here. The legacy `palace/<p>/ingested-archives.jsonl`
(`internal/storage/ingested_archives.go:44-48`) is deleted as described below. That legacy file is
not tracked, not ignored and not swept.
Its own comment assumes it is deleted with the drawer store (`:36-43`). If a ledger survives
after its drawers are gone, the backfill skips every archive it lists and never rebuilds them
(`internal/tools/system_tools.go:1050-1059`).

A new binary deletes a legacy ledger when either of these holds:
- no tracked drawer is left under `palace/<p>/drawers/`;
- the vault carries the migration marker.

It is never keyed on the host-local store being absent. That would delete every host's ledger
before capture is redirected.

**The ledger's baseline is a set, not a time** (*Automatic ingest scope and two cancellations*, as
refined by *Rulings on spec review round 5*, B1).
- **What it holds.** When a host's ledger is created, it records the `source_sha256` of every
  tracked archive present at that moment. The archive named by the trigger that created the ledger
  (the one the hook or `vp_capture_session` just created) is left out of the set.
- **What it means.** That set is the historical backlog. Automatic triggers ingest every pending
  archive that is not in the set (decision 7). No clock or date is involved.
- **Additions.** Copy, merge and import add the archives they bring in to the set explicitly.
  - They do so on the host that runs the command, and only when that host already has a ledger for
    the project. A ledger created later records those archives anyway.
  - Other hosts receive the archives by pull, outside their own sets, and ingest them
    automatically within each run's budget.
  - Copy and merge: `split-and-merge-exclude-derived-palace-paths`, after the copy is published or
    the merge's copy loop ends. A failed addition warns and never fails the command.
  - Import: `importers-write-the-frozen-tracked-corpus`, for the archives a vibevault import
    writes.
- **Emptying.** A completed `vp index rebuild` empties the set.
- **Lifecycle.**
  - The set lives in the ledger file and survives every rewrite of it.
  - A discard of the ledger (decision 3) recreates it with a fresh set.
  - Deleting a legacy ledger does not touch the new one.
- **Owner.** The set and its API belong to `host-local-index-store-ledger-and-fingerprint`.

**`completeness.json`** records, per project:
- each tier that has been built, with the count of sources it was built from;
- the `chunks.fingerprint` and the embed-cache fingerprint it was built under. The graph
  fingerprint is not recorded here: its one copy is inside `hnsw.idx` (decision 3);
- the `stale` flag and its reason.

"Built" means recorded there, never inferred from an in-memory index or from a file being present.
A fresh process therefore reads the same coverage as the process that built the tiers. Owner:
`search-index-completeness-and-build-serialization`.

**3. Fingerprints: a mismatch never embeds a transcript archive on the search path.** Owners:
`chunks.fingerprint` belongs to `index-fingerprints-project-lifecycle-and-migration-marker`, the
embed-cache fingerprint's handling to `search-index-completeness-and-build-serialization`, and the
graph fingerprint to `hnsw-graph-file-envelope-and-warm-start`.


- **`chunks.fingerprint`** records the recipe for *what is indexed*: an indexer version constant
  hand-bumped in the binary, the chunker settings, a hash of the effective room keywords, and the
  extractor version.
- **The graph fingerprint** records *how it is indexed*, for a project that uses HNSW (decision
  6): the library version, the vector dimensions, `M` and `EfSearch`.
  - It is recorded in exactly one place: inside `hnsw.idx`'s checksummed envelope. There is no
    sidecar and no copy in `completeness.json`, so the graph and the record of how it was built
    are always replaced together and can never disagree
    (`hnsw-graph-file-envelope-and-warm-start`).
  - **A mismatch rebuilds only the graph, with no embedding** (*Rulings on children review round
    3*). The next run of the ingester or of `vp index rebuild` replaces `hnsw.idx`, built from the
    cached vectors and the local chunks. It is not a `stale` reason: `stale` is set only when
    vectors are missing, and then for the missing-vector reason.
  - **A corrupt `hnsw.idx`** (a bad checksum, or a structure that fails the load-time walk) is
    deleted under the index commit lock (`Tx.DeleteGraph`). The project answers from brute force
    until a converting process writes a new file. That is not a store discard and sets no `stale`.
  - **The graph-heal hook.** The ingester and the rebuild driver are upstream of
    `hnsw-graph-file-envelope-and-warm-start`, so they reach the graph through a seam.
    - `pending-archive-ingester-and-per-archive-commit-step` declares `ingest.GraphHealer`, which
      returns `ingest.HealResult{Rebuilt, Deleted, Reason}`.
    - `hnsw-graph-file-envelope-and-warm-start` implements it.
    - The ingester and `explicit-resumable-index-rebuild-with-disk-watchdog` call it at the end of
      a run. Until the HNSW child lands it is nil, and they skip the call.
- **The embed cache keeps its own fingerprint** (`internal/search/cache.go:183-235`).
  - At `a32a2d4` a mismatch removes every vector of the old regime at once (`:203-218`).
  - Under this ADR the cache never discards on its own. A mismatch sets `stale`. Until
    `vp index rebuild` discards the old vectors, the project's cache reads as all misses and
    refuses `Put`.
  - At `a32a2d4` that fingerprint records the model, the behaviour version and `max_seq_len`
    (`internal/embedder/fingerprint.go:30-31`). It does not record the backend; Phase 11 adds it.
  - An index therefore never mixes vectors from two embedding regimes.
  - The embed-cache fingerprint is not part of `chunks.fingerprint`, so after an embedder change
    the chunk store survives with no vectors.
- **Shared properties.** All three follow the embed cache's pattern
  (`internal/embedder/fingerprint.go:23-31`). None stamps a tracked file, gates a vault write, or
  appears in an MCP tool's input.
- **A mismatch means "present and different".** A missing fingerprint means "not built" (decision
  8), never a mismatch. Today the embed cache treats a missing sidecar as a mismatch
  (`internal/search/cache.go:184-202`); the index must not, or every first build on a fresh host
  would read `stale`.
  - An embed-cache directory with no sidecar **and no vectors** is not built. The first build
    writes the sidecar.
  - One with **vectors but no sidecar** cannot be attributed to a regime, so it counts as a
    mismatch. The project is marked `stale` for a fingerprint reason, and its cache reads as all
    misses. The next `vp index rebuild` removes those vectors and writes the sidecar.
- **What a mismatch does.**
  - A `chunks.fingerprint` or embed-cache mismatch found on the search path or by the ingester sets
    the persistent `stale` flag (decision 8). A graph fingerprint mismatch does not (above).
  - The search path then embeds only what the tier table (decision 7) allows: notes and
    iterations, and, on a vault without the marker, the tracked drawers. It never re-embeds a
    chunk-store vector or an archive. Until the rebuild, search keeps answering from what is there.
  - **The pending-archive ingester does not run on a project that is `stale` for a fingerprint
    reason.** It exits for that project, and the coverage reason names `vp index rebuild`.
    Otherwise every archive would fall out of the ledger after a discard, and the ingester would
    perform a full rebuild in the background.
  - **Only `vp index rebuild` discards, and only what the mismatched fingerprint covers.**
    - A `chunks.fingerprint` mismatch discards that project's chunks, extracted KG, ledger and
      graph.
    - A graph fingerprint mismatch replaces only the graph, from cached vectors, and the ingester
      may do it too, so changing `EfSearch` discards no chunks and embeds nothing.
    - An embed-cache mismatch, or vectors with no sidecar, leaves chunks and ledger alone. The
      rebuild removes the old vectors and re-embeds.
  - **How the rebuild locks.** The rebuild takes the index commit lock once for the discard, then
    once for each archive's commit step, like the ingester (decision 7). It embeds outside the lock,
    and on completion clears `stale`.
  - **The `stale` flag records its reason.** There are two kinds:
    - a `chunks.fingerprint` or embed-cache mismatch, which only a completed `vp index rebuild`
      clears;
    - missing vectors for chunks of ledgered sources (archives or import batches). The ingester's
      repair pass (decision 7) clears this kind once no such miss remains, and so does a completed
      rebuild.

    The ruling that the ingester repairs missing vectors (spec review round 4, B1) is later than the
    one that only a rebuild clears `stale`, so it wins for this kind.

**4. Content without a tracked archive is dropped, not frozen** (operator ruling, 2026-10-02:
*Ruling on unarchived content*).

- **What is dropped.** The migration removes every derived drawer and every extracted triple and
  entity line from the tracked tree, whether or not its source has an archive. The no-archive
  classes leave with the rest: the vibevault import, the `zed:` and `zed-mcp:` sessions, the uuid
  sessions with no manifest, and the rest.
- **The loss is from search, not from history.** The migration tags its parent commit, for example
  `pre-authored-only-<date>`, and pushes the tag. Every dropped byte stays recoverable with
  `git show`.
- **What stays searchable.** Session notes, their decision frontmatter and `iterations.md` stay
  tracked and searchable. A session that never had an archive is still searchable through its note,
  but not through its transcript.
- **Importers** write archives, or index into the host-local store. They never write tracked
  derived files.
  - The importers are `vp migrate vibevault` (`internal/migrate/vibevault.go:262`, `:308`) and
    `vp migrate mempalace` (`internal/migrate/mempalace.go:225`, `:299-350`).
  - An import that writes archives is searchable on every host once the archives are ingested.
  - An import that indexes only locally (mempalace) is single-host, and nothing can regenerate it,
    because it has no archive.
    - A `chunks.fingerprint` mismatch marks the project `stale`. The next `vp index rebuild` then
      discards the import with the rest of the project's chunks.
    - The importer says so in its output and in `doc/MIGRATION.md`. The remedy is to re-run the
      import after that rebuild.
  - Imports are run from one host at a time (*Rulings during the spec and children reviews*).
- **Records are dated by their session, not the clock.** This makes a rebuild deterministic and
  makes date filters mean what they say.
  - Today one timestamp, taken from the clock when indexing runs
    (`internal/capture/indexer.go:78`), feeds four fields:
    - every drawer's `filed_at` (`:97`);
    - every extracted triple's `extracted_at` (`:237`, `:260`);
    - the `valid_from` of temporal relationship triples (`:191-196`, into `kg.ExtractAll`);
    - each extracted entity's `created_at`.
  - That one timestamp becomes the session date. A rebuild of the same archive then reproduces
    every field.
  - **The session date is the UTC day of the session's start** (*Rulings on spec review round 5*),
    for transcript chunks and decision chunks alike.
    - **Where the start comes from.** It is the timestamp of the first transcript record that
      carries one, as the archive's adapter reads it. The adapter gains that read.
    - **Fallback.** For a transcript with no timestamped record, the start is the manifest's
      `captured_at` (`internal/archive/manifest.go:42`).
    - **Why UTC.** Two hosts in different time zones then date the same archive identically.
    - **For a transcript chunk only immutable archive content is used,** so its date never depends
      on a later step.
    - **Decision chunks** take the session start day that the ledger records for the session
      named by the note's `archive_session_id` (each ledger entry records its session's UTC start
      day). They never read an archive. Until that session is ledgered on this host, or when the
      note names no session, they take the note's own day (`internal/storage/sessions.go:50`). When
      the session is ledgered later, the next build re-dates them: a decision chunk's date is fixed
      only once its session is ledgered. Today they always take the note's local day
      (`internal/capture/decisions.go:157`).
    - **Shared records** (*Rulings on children review round 3*). A chunk or KG record owned by
      several sources takes its date **and its `source_ref`** from the live owner with the earliest
      UTC start day. A tie goes to the owner with the smallest owner key (an archive's
      `source_sha256`, or a batch id). The result then does not depend on the order in which the
      owners were ingested, so a rebuild reproduces it. Whenever ownership changes, for example
      when a supersede removes an owner, both are recomputed from the owners that remain.
  - **Why `captured_at` alone is not the session date.**
    - It is the time the archive was made, in UTC. `vp archive create` passes no time
      (`cmd/vp/cmd_archive.go:93-102`), so an old transcript archived today would be dated today.
    - The linked session note (`vault_rel_session_note`, `:41`) is not used. The hook makes that
      link after archiving (`internal/hook/hook.go:438`, `:459`), and the link can fail. An ingest
      before the link, or after a failed link, would date chunks differently from a later rebuild.
    - The archive filename's `<date>-` prefix is not used either. It is the archiving day, as a
      local calendar day (`internal/archive/archive.go:165-166`).
  - **Importers.** An importer that writes an archive for an old session writes the session's
    original record timestamps when the source has them, so the adapter's read finds the UTC start
    day.
    - Otherwise it passes the session's day, at **12:00 UTC**, as `archive.CreateOptions.Now`, never
      the clock (`internal/archive/archive.go:139-141`). The vibevault importer is that case: its
      notes carry a session-level `date:` and no per-record timestamps.
    - `captured_at` is then `<day>T12:00:00Z`, so the fallback reads that day on every host.
    - **Why noon, not midnight.** `archive.Create` takes the archive filename's day from the same
      `Now`, in the process-local zone (`archive.go:165-166`, through `CalendarDay`,
      `internal/storage/clock.go:43-45`).
      - At midnight UTC, every host west of UTC would name the file for the previous day.
      - A re-import on another host would then compute a different path and miss `Create`'s
        idempotence skip (`archive.go:194-205`). That would add a second tracked archive of the
        same session.
      - Noon keeps the filename day equal to the session day in every zone within ±11 h of UTC,
        without changing `archive.Create`.
  - **Owner.** One helper, `index.SessionDate`, owned by `importers-write-the-frozen-tracked-corpus`
    so that it sits upstream of every caller. It serves the pending-archive ingester and the rebuild
    driver. The decision-chunk writer does not call it: decision chunks take the start day the
    ledger records, and never read an archive (above).
  - **Visible consequence.** Search derives a result's date from the drawer's `filed_at`
    (`internal/search/engine.go:781-784`), shows it (`:347`) and filters on it (`:812-815`). So
    **date filters over transcript and decision chunks filter on the session date, the UTC day of
    the session's start, not the ingest date.** A backfilled session from May is found under May,
    not under the day the backfill ran. The same holds for `as_of` and timeline order on regenerated
    temporal triples.

**5. Authored facts stay on `palace/<p>/kg/triples/`, and every record says where it came from.**

- **Path.** `vp_kg_add` and `vp_kg_invalidate` keep their path (operator ruling 1). `kg/triples/`
  and `kg/entities.jsonl` therefore stay tracked and unignored. After the migration they hold
  authored records only.
- **Every new record carries an explicit `origin` field**, `authored` or `extracted`.
  - Today the two kinds are told apart only by convention: `source_session` together with
    `extracted_at` marks every tracked triple at HEAD, with 59,902 extracted and 1 authored. That
    convention breaks in three places:
    - `InvalidateTriple` rewrites an extracted file in place and keeps both fields
      (`internal/storage/knowledge_graph.go:378-404`);
    - `vp migrate mempalace` can write `extracted_at` with no `source_session`. Under this ADR a
      mempalace triple is extracted (below);
    - `AddTriple` is create-once on an S/P/O path (`knowledge_graph.go:291`, path at
      `internal/storage/paths.go:66`), so `vp_kg_add` reports "already exists" when an extracted
      copy is present.
  - The migration classifies each existing triple and entity line by that convention, once,
    keeps and stamps the authored ones, and drops the extracted ones (decision 4).
    - **One exception (X5).** An extracted triple whose `valid_to` is set is a pre-migration
      invalidation edit. It classifies as authored and stays tracked. That holds for any extracted
      triple, a mempalace one included. The live vault holds no such triple today.
  - **Mempalace triples are extracted** (*Rulings on children review round 3*). The mempalace
    importer writes its triples and entities only to the host-local KG, with `origin: extracted` and
    its import batch as owner (decision 7, "Visibility"). It never writes `kg/triples/`, and a
    mempalace triple is never classified as authored, except through a pre-migration invalidation
    edit (X5, above).
  - A v8 binary reads triples with plain `json.Unmarshal` (`knowledge_graph.go:467-477`), which
    ignores an unknown field. The new field is therefore not an encoding change, and the data
    format stays at 2.
  - A v8 `InvalidateTriple` re-marshals the struct, so it drops `origin` and keeps
    `source_session` and `extracted_at` (`internal/storage/knowledge_graph.go:30-38`). That is
    harmless only because surface 9 gates v8's `vp_kg_invalidate`.
    - A file it rewrites still classifies as it did before.
    - An authored record has no `extracted_at`, so it stays authored.
    - An extracted record keeps its `extracted_at`, so it stays extracted.
    - The authored overlay below therefore never carries `extracted_at`.
- **Invalidation.**
  - Invalidating an authored triple sets `valid_to` on its tracked file, as today.
  - Invalidating an extracted triple writes an authored overlay: a tracked record at the triple's
    path, with `origin: authored` and `valid_to` set.
  - Where a tracked record and a local one share a key, the tracked one wins.
- **`vp_kg_add`'s create-once check** (*Rulings during the spec and children reviews*):
  - it refuses with "already exists" when the path holds a tracked **authored** record;
  - when the path holds a tracked **extracted** record (possible only before the migration), it
    overwrites it as authored;
  - a host-local extracted copy never blocks it.
- **Readers.** KG readers (`QueryEntity`, `Timeline`, `KGStats`, `ListTriples`) answer from the
  union of the tracked authored records and the host-local extracted set. The union stays behind
  the data-format read gate that guards those reads today (`internal/surface/format.go:67-71`).
- **Owner.** `authored-and-extracted-knowledge-graph-records` owns the `origin` field, the
  classifiers, the local extracted-KG store's format, the authored overlay and the readers' union.

**6. The index is chosen per project by size: brute force below a threshold, HNSW at or above it**
(operator ruling 2; *Rulings on the HNSW track after the children review*).

- **One index per project, chosen by chunk count.**
  - Brute force is used below a threshold. HNSW is used at or above it.
  - At `36cab60`, HNSW ties brute force at about 20k vectors and loses below that, while its builds
    take minutes against seconds. Most projects hold fewer than 15k chunks.
  - The threshold is set by measurement in
    `hnsw-parameters-from-real-vector-recall-and-production-wiring`, alongside `M` and `EfSearch`.
  - **The threshold has hysteresis.** A project switches to HNSW at the threshold, and back to brute
    force only below a lower bound, which the same child measures. Without it, a project near the
    threshold would flip from one process to the next and rebuild a graph each time.
- **No dual query.** A project is answered by exactly one index. The exact brute-force index is
  also the exactness oracle in tests.
- **The switch loses no write** (*Rulings on children review round 3*). Writes made while a project
  converts from brute force to HNSW are caught up by the ledger-driven incremental pass before the
  switch completes (`hnsw-graph-file-envelope-and-warm-start`).
- **HNSW is decoupled from v9.2.0.**
  - v9.2.0 is the git-health release (decisions 1–5 and 7–11). It may ship with brute force for
    every project.
  - The HNSW children land whenever they are ready, and no release version is tied to them:
    `vector-index-interface-and-coder-hnsw-wrapper`,
    `hnsw-parameters-from-real-vector-recall-and-production-wiring` and
    `hnsw-graph-file-envelope-and-warm-start`.
- **Pin.** `github.com/coder/hnsw` at `36cab6028fed` (pseudo-version
  `v0.6.2-0.20260622133054-36cab6028fed`).
  - All three upstream recall fixes are in it: `c8a3b11` (heap), `bc94177` (replenish, #21) and
    `36cab60` (search bounded by `efSearch`, #22).
  - The earlier floor, `c8a3b11` alone, measured recall@10 of 0.024.
  - No tagged release contains the fixes.
- **Windows: a bounded, temporary exception to "no fork".**
  - At `36cab60` the library does not build for windows/amd64, which is a release target
    (`.goreleaser.yml:10-14`). At `coder/hnsw@36cab60 encode.go:304` it calls
    `renameio.TempFile`, which is `!windows`. Only `SavedGraph` uses that call, and vp never calls
    `SavedGraph`.
  - The remedy has three parts:
    - a small upstream PR that build-tags or replaces that use;
    - until it merges, a `go.mod` `replace` pointing at a minimal vendored copy that drops only
      `SavedGraph`;
    - removing the vendored copy once upstream merges.
  - The wrapper child's acceptance includes a `CGO_ENABLED=0` cross-build of every goreleaser
    target, so CI catches this class of failure before a tag.
- **The wrapper's invariants.** Each answers a library behaviour at `36cab60` that the engine would
  otherwise hit:
  - **One `sync.RWMutex` around every call.** The library has no lock, and a search during an add
    is a fatal concurrent map access.
  - **Tombstones instead of `Delete`, with a rebuild above a tombstone threshold.** The threshold
    is set by measurement in `vector-index-interface-and-coder-hnsw-wrapper`. Library deletes
    leave one-way edges behind; deleted keys come back in results and searches panic. Production
    deletes today in `RemoveDrawer` and the orphan reaper (`internal/search/engine.go:447-450`,
    `:720`).
  - **Upsert as a tombstone plus a new internal key**, with an id→key map. Re-adding a key panics
    upstream (issue #15). The engine relies on upsert:
    - brute force updates in place (`vector_index.go:79-81`);
    - note chunk ids are identity-keyed (`internal/search/notes.go:54-56`);
    - legacy drawer ids are 32 bits (`internal/storage/drawers.go:62-67`), so duplicates are certain
      at scale. Host-local chunk ids are wide content hashes (decision 7), which removes that cause
      for host-local chunks. Upsert is still needed for identity-keyed note chunks, and for legacy
      drawers on the glide path, which keep 32-bit ids.
  - **Validation before every library call:** dimensions, `k ≤ 0`, zero or NaN vectors, and
    duplicate ids within one batch.
  - **Import only into a fresh graph, inside the wrapper's own checksummed envelope,** and
    re-apply `EfSearch` afterwards. Upstream `Import` writes into the receiver before it validates,
    overwrites `M` and `EfSearch` from the file, and carries no checksum.
  - **Writes go to a temp file, then fsync, rename and fsync of the directory.**
  - **Cosine distance, with a NaN guard on the score.** Embeddings are L2-normalised, so cosine
    ranks as today's index does (`vector_index.go:88-90`).
- **Library details.** It has no `efConstruction`: `Add` uses `EfSearch`. Track upstream issue #15
  and PRs #23 and #25, and remove each workaround once its fix merges.
- **Tests must be able to fail.** The constitution recall test
  (`internal/search/recall_test.go:274`) runs five self-queries (`:290`) over about 800 chunks. It
  asserts an exactness distance bound (`:300-307`) but only logs set-overlap recall (`:319`). The
  HNSW recall test therefore needs:
  - a seeded, clustered 384-dim corpus of at least 50k vectors, or a committed fixture of real
    exported vectors;
  - an asserted recall bar;
  - a delete-and-upsert churn phase.

    The ≥50k test cannot run in `make test`, which is `go test -race -short` (`Makefile:98-99`).
  - A 50k build takes minutes, and `-race` makes it about four times slower.
  - It therefore runs in `make hnsw-measure`, behind a build tag.
  - It runs in CI through a new `.github/workflows/hnsw-measure.yml`, with dispatch and nightly
    triggers, because `ci.yml` has neither (`.github/workflows/ci.yml:3-6`).
  - `make test` keeps a 5k regression floor.
  - Both belong to `hnsw-parameters-from-real-vector-recall-and-production-wiring`.

  A persistence test asserts that loading ran no graph build and no `Add`. Each test is broken
  once on purpose to prove it fails.
- No sqlite, usearch or qdrant (`modernc.org/sqlite` is in `go.mod` for other reasons).
- **HNSW is off the critical path to the quantum split.** Git health needs decisions 1–5 and 7–11,
  not the graph.

**7. The index is built from archives only, and routine index work never depends on an LLM**
(operator ruling, 2026-10-02: *Ruling on unarchived content*, items 3 and 4).

- **Capture no longer indexes the transcript text it is given.**
  - `vp_capture_session` keeps its `transcript` parameter only to create an archive on a host
    without a hook (`archive_transcript`).
  - Today it chunks and indexes that text (`internal/tools/session_tools.go:405-407`;
    `internal/capture/session.go:488-489`).
  - A session that never gets an archive is searchable through its note, not its transcript.
- **One pending-archive ingester** (*Rulings during the spec and children reviews*, spec review
  round 3, B1 and B2; *Automatic ingest scope and two cancellations*; *Rulings on spec review
  round 5*).
  - **What it does.** It runs one idempotent, ledger-driven pass over the archives that are
    **pending**. That covers:
    - the hook's archives (SessionEnd and PreCompact; the hook does not archive at Stop,
      `internal/hook/hook.go:307-321`);
    - archives pulled from other hosts;
    - inline archives from hook-less hosts.
  - **What "pending" means** (round 5, B2). An archive is pending when its session is absent from
    the ledger, **or** the ledger records a different `source_sha256` for that session.
    - A pending re-archive of a ledgered session is a supersede (below).
    - This matters because a PreCompact archive and the SessionEnd archive of the same session on
      the same day share one path, and the second overwrites the first
      (`internal/archive/archive.go:165-168`).
  - **Scope of an automatic run** (round 5, B1). An automatic trigger ingests every pending archive
    that is **not in the host's baseline set** (decision 2).
    - No clock or date is involved, so clock skew, offline hosts and same-day archives cannot cause
      a skip.
    - The baseline set is the historical backlog, which only an explicit `vp index rebuild` clears.
      On a host with an empty ledger it is the whole history: 43–65 h of embedding for a
      quantum-ng-sized project.
    - Coverage reads `partial` while the backlog is not empty, and names it (decision 8).
  - **The per-run budget** (round 5 decision). It is counted in **archives per run**, with a
    **wall-clock cap** as a backstop.
    - The repair of missing vectors counts against the same budget.
    - Archives are taken newest first.
    - Both defaults are set by measurement in
      `pending-archive-ingester-and-per-archive-commit-step`.
    - Coverage's reason names separately the **pending** archives (outside the baseline, beyond this
      run's budget) and the **backlog** (the baseline set).
  - **Its arguments.** The trigger passes the vault root and the one project slug it resolved:
    - the hook, from its own vault resolution;
    - a pull, from the vault it pulled.

    The hook and `vp_capture_session` also pass the `source_sha256` of the archive they just
    created, so a ledger they create leaves it out of the baseline set. A pull, a clone and `vp mcp`
    startup name no archive. When `vp mcp` starts with no project it can resolve, it passes the
    vault's first project in slug order; the run then reaches every other project with pending
    archives as usual. The ingester resolves nothing on its own. One run processes the
    triggering project first, then every other project of that vault with pending archives, within
    the budget.
  - **Unmigrated vaults.** The ingester runs whether or not the vault carries the marker.
    - From install on, v9 capture writes no drawers. So on an unmigrated vault a new session's
      transcript is searchable only through the host-local store.
    - Search on a vault without the marker therefore reads both the tracked drawers (the glide
      path) and the host-local chunks.
    - They are deduplicated by a wide content hash. A host-local chunk id is that hash, at least 128
      bits (decision 7, chunk ids). The legacy 32-bit drawer id
      (`internal/storage/drawers.go:62-67`) is never used to match.
    - The coverage reason under `legacy` carries the ingester's progress.
  - **Reading an archive.** The ingester reads the whole archive file, verifies its hash against the
    manifest, and closes it before embedding.
    - That way it never records one version's bytes under another's hash on POSIX.
    - On Windows it never holds the file open while the hook renames a newer archive into place.
    - A hash mismatch counts as a failure for that archive (below).
  - **Embedding happens outside the index commit lock** (round 5, B5). The ingester reads the
    archive, chunks, classifies, extracts and embeds without any lock. It takes the index commit
    lock only to write what it has already computed.
  - **Write order for each archive** (round 4, B1; round 5, B3). Each step is durable before the
    next:
    1. the vectors, each written atomically (temp file, fsync, rename);
    2. then the chunks, appended and fsynced;
    3. then the local KG records, appended and fsynced;
    4. then the ledger entry, which records the archive's `source_sha256` and its **chunk count**:
       the number of distinct chunk ids the archive owns.

    A killed run therefore leaves that archive out of the ledger, and the next run resumes it.
    Chunks are deduplicated by id, so a resumed run leaves no duplicates.
  - **Visibility.** Search loads only chunks, and KG readers load only KG records, whose source is
    in the ledger: an archive, or an import batch.
    - **Chunks with no archive.** A mempalace import has no archive.
      - The importer ledgers each batch under its import batch id, with its chunk count.
      - **A batch's start day** is the earliest drawer `filed_at` in the export, else
        `2000-01-01`. The ledger's batch record marks it `start_day_source: "import"`.
      - A batch id never equals a session id. The store refuses to commit a batch whose id does.
      - The batch's chunks and KG records record that id as their owner, so they are visible like
        a ledgered archive's.
      - A batch is not a session, so coverage's session counts ignore it.
      - The repair pass re-embeds a batch's missing vectors. It reports a chunk-count shortfall,
        because only re-running the import can restore the chunks.
    - **Decision chunks** belong to the notes tier. Each is owned by its session note, never by an
      archive or a batch, and is visible whenever its note is.
    - A miss on a chunk whose source is not yet ledgered never sets `stale`. A resumed run
      deduplicates the KG records it rewrites.
  - **Repair.** The ingester repairs ledgered archives, within the per-run budget:
    - it re-embeds missing vectors, for example after a crash on a host whose filesystem lost
      unsynced writes, or after an embed-cache wipe;
    - it compares the ledger's chunk count with the number of chunks that record this archive as an
      owner, and re-ingests the archive on a shortfall.
  - **Torn lines.** Readers tolerate a torn trailing JSONL line. A writer truncates a torn trailing
    line, under the index commit lock, before it appends.
  - **Failures** (round 5, B4). A failure is non-fatal. The ingester logs a warning to `vp.log`,
    records a failure count for that archive's `source_sha256` in the ledger, and moves on. A
    failure record is not an ingest: it never makes a session ledgered for visibility, pending or
    coverage.
    - Automatic runs skip an archive after N failures, until an explicit rebuild.
    - N is set by `pending-archive-ingester-and-per-archive-commit-step`.
  - **When a run ends** (round 5, B4). A run ends when either:
    - its budget (archive count or wall-clock cap) is spent; or
    - a rescan finds no pending archive that this run has not already attempted.

    A failing archive is retried by the next run, never by the same one, so the index run lock is
    never held indefinitely.
  - **Re-archived sessions: supersede.** A session archived again, for example at PreCompact and
    then at SessionEnd, has its newer archive supersede the older one.
    - **Ownership, not deletion by id.** Each chunk, and each KG record, records the archives that
      own it, keyed by each archive's `source_sha256`, never by its path (*Rulings on children
      review round 3*).
    - Superseding removes the older archive's ownership. A chunk is deleted only when no archive
      owns it any more. The older archive's KG records go the same way.
    - The ledger is keyed by session, and records the live archive and a generation number.
    - **Order, as commit steps under the index commit lock:**
      1. mark the session's ledger entry as superseding;
      2. add the newer archive's vectors, chunks, KG records and ownership. Then remove the older
         archive's ownership, deleting every chunk and KG record that no archive owns any more.
         Each file is rewritten atomically;
      3. record the new live archive and bump the generation.

      A crash leaves the session pending, never "done with half its chunks". Readers detect the
      rewrite through the generation.
    - **Concurrent re-archive.** A supersede that races an ingest of the older archive in another
      run waits for the index commit lock, and re-checks ownership there.
    - An older archive met after the newer one is recorded as superseded and never ingested.
  - **Coverage counts sessions.** `m` in `partial` counts sessions with a live archive, not archive
    files, so a session archived on two days counts once.
- **Two locks** (round 4, B2; round 5 decision). Both are `internal/vaultlock` locks on a named
  file, flock on POSIX and `LockFileEx` on Windows. At `a32a2d4` only `TryAcquireFile`
  (`internal/vaultlock/vaultlock.go:157`) locks a named path; `Acquire` and `AcquireWithTimeout`
  (`:184`) lock a hashed sidecar under `.vp-locks/` (`:215-237`).
  `host-local-index-store-ledger-and-fingerprint` adds the timed form on a named path,
  `vaultlock.AcquireFileWithTimeout(lockPath, timeout)`. It runs `AcquireWithTimeout`'s poll loop
  over `TryAcquireFile`'s named-file open, and returns `ErrLockWaitTimeout` (`:44`) at the deadline.
  A context form, `AcquireFileContext`, serves the ingester and the rebuild. The caller creates
  `palace/.local/locks/`.
  - **Where they live.** Under the host-local `palace/.local/locks/` of the vault they guard: never
    a pidfile, and never inside `index/<p>/`, which a discard deletes. Both locks are therefore
    **per host, per vault**.
  - **Release on death.** The OS releases a lock when its holder dies. On Windows that may happen
    only after the system frees the handle.
  - **The index run lock**, one per host per vault, non-blocking.
    - It is held for a whole ingest run or a whole `vp index rebuild` run.
    - A second ingest trigger that finds it held exits at once.
    - `vp index rebuild` that finds it held also exits at once, with a message naming the holder.
    - **The holder record.** Lock files are empty, so after acquiring the lock the holder writes
      `palace/.local/locks/index-run.holder`: pid, kind (ingest, rebuild or lifecycle), project and
      start time.
      It is advisory: after a kill or a pid reuse it can be stale. A failed try-lock reads it to
      name the holder. The holder removes it just before it releases the lock.
    - **A status probe never takes the run lock.** `vp index status`, `vp_index_status` and
      bootstrap's coverage read the lock state without acquiring it.
      - They read the holder record, and report a run in progress only while the pid it names is
        alive.
      - A try-lock there would hold the run lock for a moment. A trigger that collided with it
        would exit, and that trigger would be lost: the no-lost-trigger rule below covers only a
        real holder's rescan.
      - The probe's answer is advisory, as the record is. Only a try-lock is authoritative, and
        only a run takes one.
    - `vp_refresh_index` checks the index run lock before it spawns the way a status probe does: it
      reads the holder record and checks that the pid it names is alive, never taking the lock. If a
      run is in progress it returns a refusal to its caller. A stale record can let a spawn through;
      the spawned rebuild then takes the lock itself, or exits naming the holder.
    - Holding the lock also bounds embedding to one process, and one loaded model, per vault on the
      host. A host serving two vaults may run two.
    - **No lost trigger.** Before releasing the lock, the holder rescans for archives that arrived
      during the run. After releasing it, the holder checks once more for pending archives that its
      last rescan did not see, and only if one exists does it try the lock again. An archive left
      over because the budget ran out, or a failing archive, waits for the next trigger.
  - **The index commit lock**, one per project per vault per host. It is short and blocking, and is
    held only to write already-computed data. Every one of these is a commit step under it:
    - one archive's commit step (vectors, chunks, KG records, ledger entry);
    - one supersede step;
    - a discard;
    - a reaper pass, and the sweep, departed-project cleanup and split purge of `index/<p>/`;
    - a `completeness.json` write, including the `stale` flag;
    - an embed-cache write: the notes embed, the glide-path lazy embed and the fingerprint sidecar;
    - a torn-line truncation;
    - a decision-chunk write (`decision-chunks-in-the-host-local-store`);
    - the palace relabel by `vp audit rooms --apply`
      (`palace-navigation-over-the-host-local-chunk-store`);
    - one mempalace import batch (`importers-write-the-frozen-tracked-corpus`);
    - a baseline-set addition by copy, merge or import (decision 2);
    - writing or deleting `hnsw.idx` (`hnsw-graph-file-envelope-and-warm-start`). It lives inside
      `index/<p>/`, so it needs the same protection from a concurrent discard.
  - **Lock order.** The index commit lock is a leaf: no process holds two at once. The index run
    lock is taken before an index commit lock, never the reverse.
  - **Searches, the MCP server and the note-time writers take only the index commit lock,** with
    a timeout (`AcquireFileWithTimeout`), and only for their own writes:
    - the `stale` flag;
    - the notes embed into the cache;
    - the glide-path lazy embed;
    - decision chunks, written by capture, the hook and the enrichment drain.

    They embed outside the lock and commit in batches. On a timeout the caller skips its own write.
    A search does not set the flag this time, and does not commit the batch. A skipped
    decision-chunk write is restored by the next notes-tier build or rebuild. None of them waits on
    the index run lock, so a long ingest never stalls a search or a capture.
  - **The embed cache is written under the index commit lock, atomically.** Today `Put` writes in
    place (`internal/search/cache.go:121`), and the fingerprint sidecar uses one fixed `.tmp` name
    (`:220`). Both become temp file, fsync, rename.
- **Go code triggers it, never an LLM.** Every trigger starts the ingester as a **detached process**
  through `internal/detachlaunch` and returns at once. That package:
  - starts a new session with setsid on POSIX (`internal/detachlaunch/launch_unix.go:17`);
  - sends stdout and stderr to a log file, never the parent's pipes (`launch.go:96-97`);
  - on Windows, starts a new process group with job breakaway (`launch_windows.go:31`). Breakaway is
    dropped on a fallback retry when the job forbids it (`launch.go:72-77`), and then the child can
    die with the job.

  The child's working directory is set away from any checkout, and inherited descriptors are closed,
  including any vault-lock descriptor. `detachlaunch` sets no working directory at `a32a2d4`, so
  that part is new work for `capture-and-backfill-write-host-local-index-only`. A cgroup kill
  (teleport, systemd) can still reach the child. That is acceptable, because of the write order
  above. The triggers are:
  - **The hook's last step.** After `WriteSession` (`internal/hook/hook.go:524`), and so after the
    archive, the harvest and the note link, the hook spawns the ingester and returns. The spawn is
    the last step on **every** return path that ran the archive step, including the claimed-session
    early return (`hook.go:496-499`), which returns before `WriteSession`.
    - **The hook never embeds.** The host kills the hook at 30 s (`HookTimeout`,
      `internal/hook/settings.go:20`, for SessionEnd, Stop and PreCompact, `:23`). Embedding runs at
      about 46–48 s per MiB of archive.
    - **The rule.** Session exit is never delayed by indexing: the hook only spawns.
    - Claude Code gives SessionEnd hooks 1.5 s by default, raised to the hook's own timeout (30 s
      here). The spawn-only rule fits either way.
  - **After a vault pull** that brings in new archives. "A pull" means every code path that merges
    remote commits into the vault:
    - `storage.Pull` (`internal/storage/vaultpull.go:144`);
    - the merge inside a commit-and-push (`internal/storage/vaultsync.go:863`), and its second
      caller, the mirror prune (`internal/storage/vaultsync_verify.go:251`);
    - the push-rejection reconcile (`internal/storage/vaultsync.go:1373`). Both reconciles merge
      through `mergeFetchedTip` (`internal/storage/vaultsync.go:1260`);
    - the fast-forward merge in a resumed `vp vault clone` (`internal/storage/vault_clone.go:531`).
  - **After `vp_capture_session` creates an archive** on a hook-less host.
  - **At `vp mcp` startup**, as a backstop. A spawn can be lost if the host kills the hook before
    its last step: the harvest's network calls can take most of the 30 s. So an archive is ingested
    by the next session start or pull at the latest, **if it is outside the baseline set and within
    that run's budget**.
- **`vp index rebuild` stays the explicit full path.** `vp_refresh_index`, its MCP twin, checks the
  index run lock through the holder record (above), then starts the rebuild detached through the
  same launcher and returns at once, so an MCP client never times out. Progress is read through
  `vp_index_status`.
- **Freshness in a running server.** Another process can write the store, so before each search a
  running engine reads the store's change counter, `palace/.local/index/.generation/<p>`.
  - **Owner.** `host-local-index-store-ledger-and-fingerprint`. The counter is read without a lock.
    Every commit that wrote anything writes it atomically.
  - **What it holds.** Two numbers, `gen` and `epoch`.
    - Every such commit bumps `gen`.
    - `epoch` changes on every commit that did more than append: a supersede, a discard, a
      delete, a relabel, a torn-line truncation and a reap.
    - A graph write or delete (`hnsw.idx` replaced or removed) bumps `gen` only. Another engine's
      in-memory graph stays valid after a save, so no full reload is needed. A discard that
      deletes the graph changes `epoch` as a discard.
  - **Where it lives.** Outside `index/<p>/`, so a discard never resets it. A missing counter is
    created with a random `epoch`, so a recreated file never matches an engine's remembered value.
  - **Why not the ledger.** The counter moves on writes the ledger never sees: decision chunks,
    mempalace batches, relabels and `completeness.json`. A ledger-only check would miss all of
    them.
  - **The ledger's per-session generation stays** (*Re-archived sessions*, above). It tells
    per-session readers, such as coverage and the repair pass, that one session was rewritten.
  - **Reload.** When only `gen` grew, the engine loads what was appended. Any other change forces
    a full reload of that project.
  - A project removed mid-ingest is detected under the index commit lock, and the ingester stops for
    it without recreating `index/<p>/`.
- **Chunk ids are wide content hashes** (*Rulings on spec review round 5*).
  - A chunk id in the host-local store is a hash of the chunk content alone, at least 128 bits, not
    the 32-bit drawer id (`internal/storage/drawers.go:62-67`), so equal ids mean equal content.
    Wing and room are excluded, so neither a reclassification nor a project rename changes an id.
    Glide-path dedup hashes a tracked drawer's content the same way.
  - Legacy tracked drawers keep their ids.
  - The change is covered by `chunks.fingerprint` (decision 3).
- **The principle: routine index work never depends on an LLM.**
  - Chunking, classification, embedding, entity extraction and ingest are deterministic Go code.
  - Iteration summaries are the one LLM-derived input (decision 1). They are tracked, and the index
    only reads them.
- **The tier table.** It is owned by `search-index-completeness-and-build-serialization`, and
  every child that builds or reads the index cites it:

  | tier | built on the search path? |
  |---|---|
  | session notes (with decision chunks from their frontmatter, persisted in the store) and iterations | yes |
  | host-local chunks of ledgered sources (archives and import batches) | yes, **embed-cache hits only**; a miss marks the project `stale` (missing-vector reason) and waits for the ingester's repair or an explicit rebuild |
  | the glide-path lazy embed and the notes embed | embedded outside the index commit lock, committed in batches under it |
  | tracked drawers (the glide path) | yes, only while the vault carries no migration marker (decision 11) |
  | transcript archives not yet in the ledger | no: the pending-archive ingester or an explicit `vp index rebuild` only |

- **The glide path keeps today's lazy embed of tracked drawers on any vault without the migration
  marker.** That covers the time before the migration, and any vault whose migration was reverted.
  - This is a stated, temporary exception to operator decision 4 (*Rulings during the spec and
    children reviews*).
  - Today a project's first search embeds every tracked drawer the embed cache misses
    (`internal/search/engine.go:504-560`, embedding at `:600`).
  - That includes the first search after an embedder change, which on a large project can take
    hours.
  - On the glide path, coverage reads `legacy` (decision 8).
- **The orphan reaper must not destroy what the search path skipped.**
  - Today the reaper deletes every `.vec` outside the build's live set
    (`internal/search/engine.go:697-725`).
  - So the reaper's live set is the union of every tier an explicit rebuild would build (the
    owning child's choice; running the reaper only on a full rebuild was the alternative).
  - It runs under the index commit lock and re-reads the store's id set there.
- **Explicit rebuild.** `vp index rebuild [project]` rebuilds explicitly and resumably, and
  `vp_refresh_index` is its MCP twin. Both call one driver, which shares the ingester's per-archive
  commit step.
  - It embeds outside the index commit lock, and takes the lock once for a discard and once per
    archive's commit step.
  - It ingests the baseline set as well as pending archives, and on completion empties the baseline
    set and clears `stale`.
  - **A completed rebuild** is a run in which:
    - every live archive of the project, the baseline set included, was attempted;
    - each is either ledgered with its live `source_sha256`, or carries a failure record from this
      run;
    - no local-tier miss remains among ledgered chunks;
    - embedding ran.

    An archive that keeps failing therefore cannot block completion for ever. It stays visible: its
    session is not ledgered, the project reads `partial`, and coverage names it as failed.
    `--no-embed`, `--max-archives` and `--dry-run` never complete a rebuild, so they leave `stale`
    set. The proof is defined by `search-index-completeness-and-build-serialization`.
  - A rebuild holder's rescan before release also covers other projects of the vault with pending
    archives.
  - The driver writes the ledger after each archive is durable, so a stopped run resumes.
  - Before and during the run it checks free bytes and free inodes against floors, and stops
    resumably when one is crossed. The floors are set by measurement in
    `explicit-resumable-index-rebuild-with-disk-watchdog`; this ADR fixes no default.
  - The Part 1 measurement run cost 2.7M inodes and 8 GiB, and nothing in the code watches either
    today. The only `Statfs` is `internal/storage/project_slug_migration_owner_linux.go:17-23`.
  - It reports progress per archive.
  - It absorbs the cancelled task `first-refresh-index-backfills-every-never-ingested-archive`
    (*Automatic ingest scope and two cancellations*).
- **Quantum projects before the split** (*Rulings on children review round 3*, C8; operator ruling
  439: no cold search of a quantum project before the split).
  - Every per-host `vp index rebuild` that the release and live-run children prescribe, and the
    scripted rehearsal in `one-shot-migration-to-authored-only-vault` that the live-run child
    re-runs, excludes **both** quantum projects, `qa-metabuild-system` and `orchestrator`, until
    the split lands.
  - The recall measurement in `hnsw-parameters-from-real-vector-recall-and-production-wiring`
    reads neither project.
  - On those hosts the two projects keep their backlog, and their coverage reads `partial`, until
    the split.
- **Completeness comes from `completeness.json`, the ledger and the fingerprints** (decision 2),
  never from a file being present.
  - Today a capture into a cold engine marks the project fully built (`engine.go:112-119`), so
    the rest of the corpus stays hidden. A persisted graph file would repeat that defect.
  - Note and iteration chunk ids gain a content hash, so an edited note gets a new vector.
  - Per-project builds and inserts are serialised, within a process and, through the index commit
    lock, across processes.

**8. An incomplete index says so, and an empty one is an error. This is a contract change.**

- **Coverage is an instrument.** Bootstrap reports `index_coverage` per project, and `vp index
  status` and the read-only `vp_index_status` report the same.

  | state | meaning |
  |---|---|
  | `absent` | the project is **truly empty** (below), and nothing else |
  | `stale` | the persistent `stale` flag is set, with its reason: a `chunks.fingerprint` or embed-cache mismatch (decision 3; "present and different"), which only a completed `vp index rebuild` clears; or an embed-cache miss on a chunk of a ledgered source (decision 7), which the ingester's repair pass or a rebuild clears |
  | `legacy` | the vault carries no migration marker, and tracked drawers exist |
  | `unbuilt` | the project has content, but `completeness.json` records no built tier on this host, for example a fresh clone before its first search. A missing fingerprint lands here, never in `stale` |
  | `notes` | notes and iterations are built, and the project has no archive with a live session (decision 7) |
  | `partial` | the project has archives with live sessions, and n of the m sessions are in the ledger with their live archive's `source_sha256`, with n < m. This includes n = 0, a pending supersede, and a live baseline archive not yet ingested |
  | `current` | the project has archives, and every live session is in the ledger with its live archive's `source_sha256` |

  - **Order.** The states are tested in table order, and the first that matches wins.
  - **Consequences of the order:**
    - `stale` always surfaces;
    - a glide-path project reads `legacy`, not `notes`;
    - a notes-only project reads `notes`, never `current`;
    - a project with archives that are not yet ingested reads `partial`, never `notes`.
  - **`legacy` outranks `unbuilt` deliberately.** On a vault without the marker, search answers
    from the tracked drawers, which need no local build. A fresh clone of an unmigrated vault
    therefore reads `legacy`. Its reason carries "not built yet on this host" and the ingester's
    progress.
  - **Every state carries a reason.** For `partial`, the reason names, separately:
    - the **pending** archives: outside the baseline set, not yet reached within the budget;
    - the **backlog**: the baseline set, which only `vp index rebuild` clears (decision 7);
    - the **failed** archives: those automatic runs skip after N failures, which only
      `vp index rebuild` retries.
  - **What `absent` means.** It means truly empty only. "Not built yet on this host" is `unbuilt`.
- **One owner for the shared predicates.** `search-index-completeness-and-build-serialization`
  owns both:
  - **the `stale` flag**, which is persistent, in `completeness.json` (decision 2), with its reason.
    - The search path or the ingester sets it on a `chunks.fingerprint` or embed-cache mismatch, or
      on an embed-cache miss on a chunk of a ledgered source.
    - A completed rebuild clears either reason. The ingester's repair pass clears the second
      (decision 3).
  - **the "truly empty" predicate**, which the search error and `absent` both read.
- **Search result shape.** `vp_search` keeps its result-array shape.
- **"Truly empty" is defined** as no session notes, no iterations, no tracked transcript
  archives, no local chunks and, on a vault without the marker, no tracked drawers.
  - A search of a truly empty project is an **error that names `vp index rebuild`**.
  - **This changes the contract.** Today the same call succeeds with `{"items":[]}`, and the CLI
    prints "No results found." and exits 0 (`cmd/vp/cmd_search.go:187`).
  - A non-empty project with zero hits still returns an empty list. That answer is true.
- **Cross-project search** skips truly empty projects instead of failing as a whole.
  - It reports them only through the coverage instrument, where a skipped project reads `absent`.
    `absent` now means truly empty only, so a skipped project cannot be confused with a project
    that is merely `unbuilt`.
  - The result array carries no skip marker, so its shape does not change.
  - Today one failing project fails the whole call (`internal/search/engine.go:161-174`). Two
    scaffold-only projects in the live vault would otherwise make every cross-project search an
    error.
- **Palace navigation** (wings, rooms, traversal, tunnels, palace query and status) reads the
  host-local chunk store. With no local index it returns empty, and `index_coverage` says why.
- **`kg_snapshot` stops reporting a silent zero** for a project whose graph is absent on this
  host (`internal/tools/context_tools.go:665-675`).

**9. Room curation is not preserved across a rebuild** (operator ruling 4).

- A rebuild re-runs the classifier. A room move made by `vp audit rooms --apply` (`MoveDrawer`,
  `internal/storage/drawers.go:284`, called at `cmd/vp/cmd_audit.go:168`) keeps the drawer's id and
  leaves no mark, so the next rebuild undoes it.
- Curation lives in the room keywords, not in moved records.
- **What `vp audit rooms --apply` does in v9.2.0:**
  - on a migrated vault, it relabels room metadata in the host-local chunk store and writes nothing
    tracked;
  - on an unmigrated vault, it refuses;
  - its help and its output say that the next rebuild undoes a relabel.

**10. MCP surface 9, released as v9.2.0. The data format stays at 2** (operator ruling 1;
review B2).

Surface 9 is a **breaking, mandatory, forward-only** binary upgrade. Every host that writes the
vault runs v9.2.0 or later before the migration, and none goes back.

- **Why the gate is needed.** The surface gate is the only mechanism that makes every host upgrade.
  - The policy covers this case: bump "whenever the MCP tool surface changes in a way that affects
    what gets written into the vault" (`internal/surface/version.go:30-33`).
  - So does the 6→7 precedent: "an OLD binary undoes what the new one removed, and only the gate
    stops it" (`:276-277`).
- **What the gate stops.** A v8 binary's capture with a transcript, and its `vp_refresh_index`,
  write extracted triples and `entities.jsonl` lines into the tracked `kg/`. A v8 tidy then commits
  them, because tidy sweeps both KG paths in any git status (`vaulttidy.go:84`, `:92`, `:200-202`).
  The gate stops those writers: every extracted-KG writer is a gated MCP tool or CLI command. The
  hook writes no KG, because it passes a nil indexer (`internal/hook/hook.go:524`). The ingester
  it spawns in v9 writes only the host-local index.
- **What the gate does not stop.** `vp vault sync` is deliberately ungated, because it contains the
  pull (`cmd/vp/commands.go:175-180`). The ignore lines (decision 11) cover it on a migrated vault.
- **No data-format bump.** Nothing that stays tracked changes its encoding
  (`internal/surface/format.go:20-25`); decision 5's `origin` field is additive.
- **The release.**
  - **The tag.** v9.2.0 follows `v<MCPSurfaceVersion>.<RequiredDataFormat>.<build>`
    (`internal/surface/version.go:34-41`).
  - **The rest.** The release notes, the release rehearsal and the install-on-every-host runbook
    belong to `release-v9-2-0-tag-notes-rehearsal-and-rollout`.
  - **The notes cover every contract change.** The release child carries this list:
    - a search of a truly empty project is an error, and cross-project search skips truly empty
      projects (decision 8);
    - `filed_at` becomes the session date, so date filters move (decision 4);
    - content without a tracked archive leaves search: 23,632 transcript drawers and 41,364
      extracted triples, recoverable at the `pre-authored-only-<date>` tag (decision 4);
    - `vp_palace_backfill_decisions` is removed;
    - `vp_capture_session` no longer indexes its `transcript` text;
    - `vp_refresh_index` starts the rebuild detached and returns at once, or returns a refusal when
      the index run lock is held;
    - the new `vp index` command (`rebuild`, `status`) and the read-only `vp_index_status` tool;
    - `index_coverage` in bootstrap, with its seven states in order (decision 8);
    - the baseline set, the historical backlog it holds, and the per-host `vp index rebuild` that
      clears it (decisions 2 and 7);
    - the per-run budget, and the skip of an archive after N failures until a rebuild (decision 7);
    - background embedding after SessionEnd by the detached ingester, and the `vp mcp` startup
      backstop (decision 7);
    - `palace-store-drawers` is skipped on a migrated vault, and `kg-tracked-extracted` reports
      (decision 11);
    - `vp_list_projects` drift gains a row for each project whose `palace/<p>/` held only derived
      files: 12 in the live vault (decision 11);
    - `vp audit rooms --apply` relabels the local store on a migrated vault and refuses on an
      unmigrated one, and the next rebuild undoes a relabel (decision 9);
    - `vp migrate mempalace` requires `--project` naming an existing project. Its import is
      single-host, and is discarded by the first rebuild after a `chunks.fingerprint` change
      (decision 4);
    - `vp_vault_split`, `vp_vault_merge` and `vp vault copy` refuse an unmigrated destination, and
      an unmarked source into a marked destination (decision 11);
    - surface 9 is mandatory and forward-only: a rollback restores the data, not the old binaries.
- **`vp_palace_backfill_decisions` is retired** (*Rulings during the spec and children reviews*,
  Q4). Decision chunks are rebuilt from session notes on every build, so the tool has nothing left
  to do. `decision-chunks-in-the-host-local-store` removes it.
- **The golden file.** Each entry pins `name`, `mutating` and `schema_sha256`, and the manifest pins
  `surface_version` (`cmd/vp/tool_surface_golden_test.go:38-48`; the golden reads
  `"surface_version": 8` today). The golden test does not itself force a surface bump for a schema
  or tool change.
  - **Which children regenerate it:**
    - `decision-chunks-in-the-host-local-store` removes `vp_palace_backfill_decisions`;
    - `capture-and-backfill-write-host-local-index-only` narrows `vp_capture_session`'s
      `transcript` description (decision 7);
    - `explicit-resumable-index-rebuild-with-disk-watchdog` changes `vp_refresh_index`'s schema;
    - `index-coverage-instrument` adds `vp_index_status`;
    - `palace-navigation-over-the-host-local-chunk-store` rewords `vp_palace_query`'s
      `date_from`/`date_to` descriptions for session dates
      (`internal/tools/palace_query_tools.go:86-92`), and removes the hard-coded chunk count from
      its `hall` property (`:75`);
    - `one-shot-migration-to-authored-only-vault` alone changes `surface_version` from 8 to 9.
  - **Rebase rule.** Each child regenerates the golden from its own registry, and a child that
    rebases onto another's golden change regenerates it again. Nobody edits the golden by hand.
  - **A test to update.** The migration child also updates
    `TestManageTaskMoveDoesNotBumpTheSurfaceVersion`, whose `surfaceVersionAtHEAD` is pinned at 8
    (`internal/tools/task_move_tool_test.go:570`).

**11. The migration: one revertible commit per vault, behind enforced preconditions.**

- **Preconditions.** The command refuses unless all of these hold:
  - **surface 9 on every remote tip, and every tip an ancestor of HEAD.** Every remote tip carries
    a surface-9 `.surface` stamp, and every remote tip is an ancestor of HEAD, as in the 6→7
    precedent (`internal/storage/project_config_retirement.go:254-256`).
    - **How a tip is read.** Each remote is fetched once, at the start, and its remote-tracking ref
      is read immediately, with nothing run in between. The precedent does the same
      (`:244-254`). Path selection and every check below read those same tips.
    - Without the ancestor check, a remote commit that adds a drawer would merge in **tracked**,
      because ignore rules do not apply to merges.
    - The empty-vault path below skips the surface-9 half of this check, never the ancestor half;
  - **a clean local vault:** clean and pushed, with no `MERGE_HEAD`, no rebase in progress, no
    unmerged index entry and no stash entry touching `palace/`;
  - **the data format is not behind the binary;**
  - **the marker is absent**, so a second run refuses;
  - **an operator attestation**, recorded in the commit message. It covers the following, none of
    which code can verify:
    - every writer host has synced, pushed and been left clean, and runs v9.2.0 or later.
      `vp check --check writer-identity` records `sha256(hostname + vaultPath)[:8]`, not a
      version (`internal/check/writer_identity.go:25`);
    - the quantum-ng migration's per-host rows are closed. That task's census and parity checks
      count tracked drawer rows.
- **The steps:**
  1. tag the parent commit `pre-authored-only-<date>` and push the tag (decision 4);
  2. classify each triple and entity line as authored or extracted (decision 5), and stamp the
     authored ones;
  3. **delete** every derived drawer and every extracted triple and entity line, removing it from
     the index (`git rm --cached`) **and** from the working tree;
  4. write the migration marker;
  5. add the derived-path ignore lines below, through the gated reconciler, which emits them only on
     a vault that carries the marker. Steps 4 and 5 land in the one commit of step 7;
  6. write the surface-9 stamp (operator ruling 1);
  7. commit once.
- **Delete, not move** (this decision, recording *Rulings during the spec and children reviews*).
  This supersedes three earlier texts:
  - operator ruling 1's "the migrator moves its own extracted KG and drawers into
    `palace/.local/index/<p>/`";
  - the "bytes moved to the local store" part of the B3 self-heal;
  - XC10's `index/<p>/migrated/`.

  Under *Ruling on unarchived content*, every derived byte is either regenerable from a tracked
  archive or deliberately dropped. Either way it is recoverable from the tagged parent commit. The
  local index is rebuilt from archives.
- **Why the files leave the working tree too.** `git rm --cached` alone would leave them on the
  migrator's disk as `??`, and its next tidy would commit them again (`vaulttidy.go:200-202`).
  Deleting them keeps the migrator's disk equal to every pulling host's, so the audits agree across
  hosts. Nothing is lost from history: the tag holds the bytes, and every host rebuilds its index
  from archives.
- **The empty-vault path** (*Rulings during the spec and children reviews*, spec review round 3,
  B4).
  - **What "empty" means.** It applies only when two conditions hold, both at HEAD **and** at every
    remote tip:
    - there is no tracked `palace/` content;
    - there is no `.surface` stamp in any stamp directory.

    The quantum vault has two remotes, so the local tree alone is not enough.
  - **What it skips.** It skips the surface floor check, which cannot pass on a vault with no
    stamps.
  - **What it keeps.** It keeps the ancestor check: every remote tip must be an ancestor of HEAD.
    A remote commit that adds a drawer would otherwise merge in tracked.
  - **A stamp left by v8.** If a v8-era stamp exists anywhere, the vault is not empty and takes the
    normal path. Any v9 write re-stamps it to 9 first.
  - It skips steps 2 and 3.
  - It writes the marker, the ignore lines and a vault-level surface-9 stamp, `Audits/.surface`,
    one of the stamp directories the gate reads (`internal/surface/version.go:807-812`).
  - The empty quantum vault (`~/quantum-vibe-palace-vault`) takes this path. It is not
    re-initialised.
  - Without this path, a stampless destination would pass the gate for every binary, and v8 would
    not be gated there at all.
- **The migration marker is an explicit key in `.vibe-palace/vault.toml`:**
  `authored_only = "<date>"`.
  - **Owner.** `index-fingerprints-project-lifecycle-and-migration-marker` owns:
    - the key;
    - the `VaultManifest` field (`internal/surface/format.go:87-89`, which today holds only
      `Format`);
    - its reader.
  - **Every writer of `vault.toml` must round-trip all fields.**
    - Today `WriteFormat` re-encodes `VaultManifest{Format: n}` (`format.go:160-166`). A later
      format migration would therefore erase the marker while the ignore lines stay.
    - That recreates the "lines without marker" state, and "the marker is absent, so a second run
      refuses" would then fail open.
    - `WriteFormat` becomes read-modify-write. The owner's test is that the marker survives
      `WriteFormat(3)`.
  - **Never inferred.** It is not inferred from the ignore lines, because a hand-edited
    `.gitignore` must not switch on behaviour. Every marker-gated behaviour keys on the marker
    alone.
  - **Staging.** `.vibe-palace/` may be ignored on existing vaults. The live vault's `.gitignore`
    carries it, although the canonical vault set does not; the project set does
    (`internal/storage/git.go:77`). The marker write therefore force-adds `vault.toml`, as
    `vp migrate kg-filenames` does (`cmd/vp/cmd_migrate_kg.go:142`, through `GitAddForce`,
    `internal/storage/git.go:546-552`, whose comment at `:547-551` is stale on this point).
- **The ignore lines are marker-gated.**

  ```
  palace/*/drawers/
  palace/*/ingested-archives.jsonl
  ```

  - **What is not ignored.** `kg/triples/` and `kg/entities.jsonl` are not ignored: authored records
    live there, and the surface gate (decision 10) keeps v8 writers out.
  - **Not unconditional canonical lines** (*Rulings during the spec and children reviews*).
    - Today the reconciler appends every canonical line a vault lacks, on two paths, and an
      ordinary `vp config sync` reaches both:
      - **create**, when `.gitignore` is absent: `ReconcileVaultGitignore`
        (`internal/storage/git.go:102`, `:118`), applied at `internal/reconcile/vault.go:286-288`;
      - **top-up**, on an existing vault: `planVaultGitignore` → `MissingVaultGitignorePatterns`
        (`internal/reconcile/vault.go:230-240`; `internal/storage/git.go:175`) →
        `TopUpVaultGitignore` (`internal/reconcile/vault.go:313-340`;
        `internal/storage/git.go:154`).
    - **Both paths check the marker.** A child that gated only the create path would leave the
      derived lines reachable on every existing unmigrated vault.
    - Made unconditional, the lines would reach an unmigrated vault before the marker. Every room
      file would then be a tracked file under an ignored path, and a v8 hook's decision-drawer
      append would make that host's tidy fail on every run.
  - **Who writes them.**
    - The reconciler emits the two lines only on a vault whose `vault.toml` carries the marker.
    - Three writers put the marker and the lines in place:
      - the migration commit;
      - the empty-vault path;
      - `vp vault init` on a fresh vault, which is born migrated (`InitVault`,
        `internal/storage/vault_init.go:108`). A v9 `vault init` also writes the vault-level
        stamp `Audits/.surface` at `MCPSurfaceVersion` (`internal/surface/version.go:346`), never
        a literal 9.
        - The constant stays 8 until the migration child bumps it, and the gate takes the maximum
          stamp. A literal 9 would make a vault created from `main` in between refuse the binary
          that created it.
        - From v9.2.0 the constant is 9.
    - **Only `vp vault init` creates a vault born migrated,** and it writes the marker in
      `InitVault` itself, never in the shared scaffold.
      - `reconcile.ScaffoldNewVault` has two callers at `a32a2d4`: `InitVault`
        (`internal/storage/vault_init.go:148`) and the split destination
        (`internal/tools/vault_split_apply.go:359`). The marker never goes in the shared
        scaffold, so a destination the split scaffolds itself starts unmigrated. Once
        `split-and-merge-exclude-derived-palace-paths` merges, a split into such a destination is
        refused (below).
      - `vp init`, `vp config sync` and onboarding use the vault reconciler directly. A vault they
        create also starts unmigrated, without marker or lines.
      - A split must never create a destination whose copied tracked drawers sit under ignored
        paths.
    - A reverted vault loses the marker with the revert, so the reconciler never gives the lines
      back to it.
  - **Steady state.** Every migrated vault carries both lines. The quantum vault gets them from the
    empty-vault path.
- **The tidy rule change keys on the marker, never on the binary's version** (*Rulings during the
  spec and children reviews*: the derived-path ignore lines are marker-gated).
  - **On a migrated vault**, tidy sweeps no drawers.
  - It stages a triple file only when that file is `origin: authored`. A tracked extracted, or
    unstamped, triple is reported as unexpected dirt.
  - **`kg/entities.jsonl` is one file**, so tidy diffs it against HEAD.
    - It stages the file only when every added line carries `origin: authored`.
    - Otherwise it reports the whole file.
    - A missing `origin` counts as not authored.
- **Which v9 behaviour waits for the marker, and which does not.** A v9 binary without the marker
  is not `a32a2d4` in every respect.
  - **Gated on the marker:**
    - the tidy rule change above;
    - the pull self-heal below;
    - the audit changes below: `palace-store-drawers` is skipped, and `kg-tracked-extracted`
      reports.
  - **What the gated rules do without the marker.** They behave as at `a32a2d4`. Without the marker
    the derived-path ignore lines are also absent. So installing v9.2.0 before the migration leaves
    v8-written drawer appends what they are today: tracked, unignored capture artifacts that tidy
    commits.
    - They do not become non-artifact dirt, which would make `vp vault sync` refuse
      (`internal/storage/vaultsyncflow.go:82-99`).
    - They do not become tracked files under an ignored path, which would make tidy fail.
  - **Unconditional, from install on:**
    - capture, the pending-archive ingester and the backfill write nothing derived into tracked
      paths (`capture-and-backfill-write-host-local-index-only` and
      `pending-archive-ingester-and-per-archive-commit-step`);
    - explicit staging never re-tracks a derived path (below);
    - the presence predicate ignores derived-pattern files, so ignored residue never makes a
      project present on one host only;
    - the rewritten `project-tree-coherence`, which uses that predicate;
    - the glide-path rule itself (decision 7).
- **Self-heal on pull.** A new binary that finds an unmerged `UD`, `DU` or `DD` entry on a
  now-ignored derived path:
  - reads the incoming marker from `MERGE_HEAD`;
  - resolves the entry with `git rm --cached` and deletes the file;
  - concludes the merge;
  - reports each path it deleted.

  **Where it runs.** At every pull path that can leave an unmerged entry:
  - the merge in `pullCore` (`internal/storage/vaultpull.go:162`, the merge at `:272-287`);
  - the merge in `mergeFetchedTip` (`internal/storage/vaultsync.go:1260`), the one merge both
    commit-and-push reconciles go through: `reconcileIfAhead` (`vaultsync.go:863`), reached from a
    commit-and-push (`:317`) and from the mirror prune (`internal/storage/vaultsync_verify.go:251`),
    and the push-rejection reconcile (`reconcileRejectedPush`, `vaultsync.go:1373`). There the
    heal runs before `mergeFetchedTip`'s `merge --abort` on conflict, and the abort stays for any
    non-derived conflict. `mergeFetchedTip` refuses to start while any merge, cherry-pick, revert
    or rebase is already in progress, so the heal only ever acts on a merge vp itself started.

  The resumed clone's merge is `--ff-only` (`internal/storage/vault_clone.go:531`), so it never
  leaves an unmerged entry and needs no heal.

  **`UU` on `kg/entities.jsonl`** is reported by name and never resolved. It arises when the
  migrator rewrote the file and a lagging host appended to it. Only the operator attestation
  (every writer host synced, pushed and left clean) prevents it; no code does.

  Delete, not move (above): every host rebuilds its index from archives, and the tag holds the
  pre-migration tree.
- **Explicit staging never re-tracks a derived path.** Explicit staging (`stageInBatches`,
  `internal/storage/vaultsync.go:989-999`) never names a derived path. `git add -- <path>` on a
  tracked, ignored file stages it and exits 1. The comment at `git.go:529`, "Ignored paths are
  silently skipped", is wrong for explicit paths.
- **The audits are rewritten before the migration, not after it.**
  - On every host that pulls the commit, a project whose only tracked `palace/<p>/` content was
    derived loses that directory. Twelve projects in the live vault have no `palace/<p>/.surface`.
  - Today `project-tree-coherence` (`internal/vaultaudit/dimensions.go:600`) and
    `palace-store-drawers` (`:681`) would then answer differently on the migrating host and on the
    pulling hosts. That is the cross-host divergence the presence rule exists to prevent
    (`internal/storage/projects.go:47-56`).
  - **What v9.2.0 changes in the audits:**
    - `palace-store-drawers` is skipped on a migrated vault;
    - a new dimension, `kg-tracked-extracted`, reports tracked extracted records on a migrated
      vault;
    - `project-tree-coherence` is rewritten, unconditionally, on the derived-aware presence
      predicate, so the migrator and the pulling hosts agree;
    - `kg-portability`, the filename-portability check, is unchanged.

    All of these belong to `tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`.
- **Split and merge** subtract a file after their tree walk only when it is untracked, **and**
  ignored, **and** on a derived path: under `palace/<slug>/drawers/`, or
  `palace/<slug>/ingested-archives.jsonl`.
  - The subtraction applies in the split plan, the merge plan, the split destination inventory and
    the split purge's unaccounted check, always against the source vault.
  - They never subtract a tracked file.
  - **Why the path condition.** "Untracked and ignored" alone is too wide. The live vault holds 137
    untracked, ignored `*.manifest.json.<hash>.bak` files under `Projects/<p>/transcripts/`. A
    split would drop them from its manifest, and its purge would then delete the only copy.
  - This ships in v9.2.0 (`split-and-merge-exclude-derived-palace-paths`).
- **Split, copy and merge need two migrated vaults** (*Rulings on children review round 3*, C7;
  *Rulings on spec review round 5*).
  - **The destination.** It must already be a migrated vault: one created by a v9 `vp vault init`,
    which writes the marker, or one the migration has run on. The quantum vault takes the
    empty-vault path. `split-and-merge-exclude-derived-palace-paths` never writes the marker into
    a destination, and refuses a destination without it.
  - **Split into an existing vault.** At `a32a2d4` a split refuses a destination that already
    exists (`internal/tools/vault_split_apply.go:210-216`), and the one it scaffolds starts
    unmigrated. `split-and-merge-exclude-derived-palace-paths` adds splitting into an existing
    migrated vault, so that a split still has a destination it can accept.
  - **The source.** Copying or merging a project from a vault without the marker into one that
    carries it is refused too: its tracked drawers would land as tracked files under ignored
    paths. Migrate the source first.
  - Together these are the two-marker refusal: every pairing but migrated into migrated is
    refused, before any write. So from that child's merge until a vault is migrated, every split,
    copy and merge on it refuses.
  - The quantum split meets it: the personal vault is migrated before the copy, and the quantum
    vault takes the empty-vault path.
  - The refusal is owned by `split-and-merge-exclude-derived-palace-paths`.
- **Tip-only copy.** The personal vault's history keeps the 21.4 MiB blob. That is acceptable
  because `vp vault copy` is tip-only (`internal/storage/lifecycle_copy.go:610-611`), and the
  quantum copy is taken after the migration commit.
- **Rollback restores the data, not the old binaries** (operator ruling, 2026-10-02). It is
  `git revert` of the migration commit, rehearsed on a remote-stripped copy.
  - The revert re-tracks the derived files from history and removes the marker and the ignore
    lines. Host-local stores are left alone.
  - **Surface 9 survives the revert.**
    - The precondition put a surface-9 stamp at every remote tip before the migration commit,
      through earlier v9 writes, and those stamps stay.
    - A stamp that the migration commit itself raised (step 6) does revert.
    - The gate reads the vault's maximum stamp, so v8 stays gated for writes either way. Its
      read-only calls, and the gate's own remediation text, still work.
  - **Except on the empty-vault path.** There the migration commit wrote the vault's only stamp,
    `Audits/.surface`, so a revert removes it and v8 is no longer gated on that vault.
    - A revert there must re-stamp `Audits/.surface` at `MCPSurfaceVersion` in the same push.
    - The migration command's printed rollback text says so.
    - `live-migration-run-on-the-personal-and-quantum-vaults` re-stamps if it ever reverts the
      quantum vault's migration.
- **A v9 binary behaves correctly against a reverted vault**, i.e. one without the marker:
  - the marker-gated tidy and pull behaviour falls back to the pre-migration rules, and the
    unconditional behaviour above stays;
  - search falls back to the tracked drawers through the glide path (decision 7);
  - capture still writes nothing derived into tracked paths.
- **Where the end-to-end test lives.** The migrate, revert and keep-running-v9 test lives in
  `one-shot-migration-to-authored-only-vault` only.
  `tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker` tests its own code against
  hand-built fixture trees: migrated, and migrated then reverted.
- **One-shot code.** The migration code is isolated, so removing it later is a deletion (memory
  `one-shot-migration-code-stays-minimal-and-deletable`).

## Operator decisions this ADR records

From the epic (2026-10-01), as amended by the operator rulings of 2026-10-02:

1. **Content with no tracked archive is dropped from search,** not frozen. The migration tags its
   parent commit and pushes the tag, so the bytes stay in history (*Ruling on unarchived content*;
   decision 4). This replaces the per-record freeze of the earlier ruling 3.
2. **`vp_kg_add` stays on `kg/triples/`,** and the release raises the MCP surface to 9 as v9.2.0
   (ruling 1; decisions 5, 10).
3. **The index is chosen per project by size.** Brute force is used below a measured threshold,
   and HNSW (`coder/hnsw` at `36cab6028fed`, behind a thin wrapper) at or above it. There is one
   index per project, and brute force is the test oracle. HNSW is decoupled from v9.2.0. A
   temporary vendored copy covers the Windows build until upstream merges a fix (ruling 2;
   *Rulings on the HNSW track*; decision 6).
4. **No lazy full-transcript embed on search, on a vault that carries the migration marker**
   (decisions 3 and 7).
   - The glide path is a stated, temporary exception. On any vault without the marker, before the
     migration or after a revert, it keeps today's lazy embed of tracked drawers, including after
     an embedder change (*Rulings during the spec and children reviews*).
5. **The quantum split waits on this landing,** not on the GitLab admins and not on a one-off
   deletion of the 21.4 MiB room file. A `vp vault copy` that drops tracked drawers before the
   migration was proposed and declined as that same deletion.
6. **Room moves are not preserved across a rebuild** (ruling 4; decision 9).
7. **A rebuild dates its records from the source session.** Rebuilds are therefore deterministic,
   and date filters over transcript and decision chunks use the session date (*Rulings during the
   spec and children reviews*, Q1; decision 4).
8. **Surface 9 is a forward-only upgrade.**
   - A rollback restores the data, not the old binaries.
   - A v9 binary must behave correctly against a reverted vault (*Rulings during the spec and
     children reviews*, Q2; decisions 10 and 11).
9. **The index is built from archives only.**
   - One ledger-driven pending-archive ingester indexes the pending archives: a session absent from
     the ledger, or a different `source_sha256` for it.
   - Automatic triggers ingest only pending archives outside the host's baseline set, newest
     first, within a per-run budget counted in archives with a wall-clock cap. The baseline set is
     the historical backlog. Only an explicit `vp index rebuild` clears it, and coverage reads
     `partial` while it remains.
   - It is triggered by Go code, always as a detached process: the hook's last step, a pull,
     hook-less capture, and `vp mcp` startup.
   - The hook never embeds, and routine index work never depends on an LLM.
   - Sources: *Ruling on unarchived content*; *Rulings during the spec and children reviews*;
     *Automatic ingest scope and two cancellations*; *Rulings on spec review round 5*;
     decision 7.

## Options considered

- **Compile the index per host from authored artifacts (this decision).** Full semantic search over
  every archived session stays, the vault stays pushable, and an indexer change costs the vault
  nothing. Costs:
  - every host pays a transcript rebuild (Part 1: about 46–48 s per MiB of archive, plus a cold
    embed; 2 h 56 min measured for one project's embed);
  - a surface bump that makes every host upgrade before it writes;
  - sessions with no archive lose transcript search.
- **Freeze the no-archive content per record, in a tracked layout v8 can read.** This would keep
  every record searchable. It cost a reserved room, an ignore negation whose line order the
  reconciler had to preserve, a size cap, in-place edits to frozen triples, and a per-record
  regeneration comparison. Rejected by the operator: the content stays in history at the tag, and
  its loss from search is accepted.
- **Move authored facts to a new `kg/facts/` and ignore all of `kg/`.** Cleaner directory semantics,
  and it also needs surface 9. The operator kept `vp_kg_add` on its path. Rejected.
- **Keep `vp_kg_add` on its path with no surface bump** (the epic as first filed). A v8 host's
  capture and tidy put extracted triples straight back into git, and nothing can verify that every
  host upgraded. Rejected by the review (B1, B2) and the operator.
- **HNSW for every project.** At `36cab60`, HNSW is no faster than brute force below about 20k
  vectors, and its builds take minutes. Rejected for one index chosen by size.
- **Stop indexing transcripts.** Smallest vault, but it loses full semantic search. Rejected.
- **Per-room size caps.** Each blob stays under 20 MiB, but the vault still grows by gigabytes and
  millions of files, and churns on every indexer change. Kept only as the fallback if this ADR is
  rejected.
- **Git LFS.** Every remote would need LFS with quotas, and it does nothing for file counts or
  churn. Rejected.
- **Pin `coder/hnsw` at `c8a3b11`, or at the tagged `v0.6.1`.** Recall@10 is 0.024 and 0.021, and
  `v0.6.1` panics after deletes. Rejected.

## Consequences

- **What leaves the vault.** The tracked `palace/` shrinks to `.surface`, `iteration-summaries/`
  and authored facts.
- **What leaves search.** 23,632 no-archive transcript drawers (22.5 MiB) and 41,364 no-archive
  extracted triples. Their sessions' notes stay searchable. The bytes stay recoverable at the
  `pre-authored-only-<date>` tag.
- **The quantum split.** After the migration commit, `qa-metabuild-system`'s tracked footprint holds
  no 21.4 MiB blob, so the copy, taken after that commit, passes the 20 MiB limit. The path to split
  step 2:
  1. release v9.2.0 and install it on every host that writes the vault (the release child's
     runbook);
  2. rehearse the migration on a remote-stripped copy, including the revert and the empty-vault
     path;
  3. run the live migration in a maintenance window;
  4. every host pulls;
  5. the empty-vault path runs on the quantum vault;
  6. split step 2.
- **Every host builds its transcript index from archives.**
  - The pending-archive ingester keeps up with new, pulled and inline archives without a manual
    rebuild: every pending archive outside the host's baseline set.
  - The historical backlog, the baseline set, waits for one explicit `vp index rebuild` per host.
  - Until then, `index_coverage` reads `partial` and names the backlog (decisions 7 and 8).
- **Search contract.**
  - A truly empty search is an error, and cross-project search skips empty projects (decision 8).
  - Date filters over transcript and decision chunks match the session date (the UTC day of its
    start) rather than the date the chunk was indexed (decision 4).
  - The tests that pin `[]` and "No results found." are rewritten with that change, not discovered
    by it.
- **Unchanged.** Lifecycle copy and delete read the tracked tree. The departure guard still finds
  `.surface` and `Projects/<p>`. The embed-cache sweep is unaffected.

### Implementation notes: sites owned by the code children

This ADR, the ADR index, the PRD and the spike note are the specification. They land first, on
their own, and the PRD marks every ADR-014 requirement as target behaviour.

The `doc/ARCHITECTURE.md` rewrite describes how the system works once the code has shipped. It
travels on a separate branch, `docs/hnsw-architecture`, and merges with the git-health release,
v9.2.0. It describes v9.2.0's runtime index as brute force over the host-local chunk store, and
the size-chosen HNSW index as what follows when the HNSW children land (decision 6).

Everything below changes with the code that implements it. Each site is assigned to the child task
that owns it.

- **Embedded templates**:
  - `internal/templates/templates/commands/restart.md:53-56` says search "rebuilds lazily on the
    first `vp_search` call", and must read `index_coverage` instead
    (`index-coverage-instrument`);
  - `restart.md:79-81` and `commands/wrap.md:672-676` list drawers and triples as capture artifacts
    (`tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`).
- **Tool descriptions**:
  - `vp_vault_tidy` (`internal/tools/system_tools.go:646-653`)
    (`tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`);
  - `vp_refresh_index` (`:898-919`) and its "nothing to refresh" text (`:1204-1214`)
    (`explicit-resumable-index-rebuild-with-disk-watchdog`);
  - `vp_search` and `vp_search_cross_project` (`internal/tools/search_tools.go:101`, `:113`)
    (`search-first-build-and-empty-corpus-answer`);
  - the palace tools (`internal/tools/palace_tools.go:96-136`; `palace_query_tools.go:291`). The
    hard-coded chunk count is in `vp_palace_query`'s `hall` input-schema property
    (`palace_query_tools.go:75`), not its Description, so removing it moves the golden hash
    (decision 10) (`palace-navigation-over-the-host-local-chunk-store`);
  - `vp_capture_session`, whose `transcript` parameter now only archives (decision 7)
    (`capture-and-backfill-write-host-local-index-only`);
  - `vp_bootstrap_context`, for `index_coverage` (`index-coverage-instrument`);
  - `vp_list_projects` drift (`internal/tools/project_tools.go:30-40`)
    (`palace-navigation-over-the-host-local-chunk-store`).
- **Runtime messages**:
  - `internal/tools/vault_dirt.go:110` and `task_write_commit.go:177` say sync blocks "drawers and
    KG triples" (`tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`);
  - `cmd/vp/cmd_search.go:187`, "No results found." (`search-first-build-and-empty-corpus-answer`).
- **CLI help, and from it the man pages** (generated, not tracked):
  - `vp vault tidy` (`cmd/vp/cmd_vault.go:404-406`)
    (`tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`);
  - `vp search` (`search-first-build-and-empty-corpus-answer`);
  - the new `vp index` command, which must also be added to `knownCommands()`
    (`cmd/vp/gen_man_test.go:87`) (`explicit-resumable-index-rebuild-with-disk-watchdog`);
  - `vp audit rooms --apply`, documenting decision 9
    (`palace-navigation-over-the-host-local-chunk-store`).
- **Code comments that restate the old design:**
  - `internal/search/vector_index.go:25-29` ("HNSW (deferred …)")
    (`vector-index-interface-and-coder-hnsw-wrapper`);
  - `internal/storage/vaulttidy.go:81` ("MUST be swept") and `git.go:529`
    (`tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`).
- **Writers that must stop writing tracked derived data:**
  - these belong to `capture-and-backfill-write-host-local-index-only`:
    - `IndexTranscript`, which it deletes;
    - capture's indexing of its `transcript` parameter;
  - the pending-archive ingester, its per-archive commit step, supersede, the per-run budget and
    failure count, the write order and the vector repair belong to
    `pending-archive-ingester-and-per-archive-commit-step`. The ingester writes only the host-local
    index. It uses the index locks but does not own them;
  - the backfill (`backfillFromArchives`, `internal/tools/system_tools.go:982`), which
    `explicit-resumable-index-rebuild-with-disk-watchdog` deletes;
    `capture-and-backfill-write-host-local-index-only` depends on it and deletes what the backfill
    leaves without a caller;
  - the detached triggers (the hook's last step, every pull path, hook-less capture and `vp mcp`
    startup) and the launched process's set-up stay with
    `capture-and-backfill-write-host-local-index-only`;
  - the index run and index commit locks, the holder record, the store's change counter and the
    ledger baseline belong to `host-local-index-store-ledger-and-fingerprint`; `completeness.json`
    belongs to `search-index-completeness-and-build-serialization`;
  - `fileDecisionDrawers` (`internal/capture/decisions.go:248`) and the other decision writers, the
    hook's enrichment drain (`internal/capture/enrichqueue.go:303`) among them, which now write
    decision chunks to the host-local store (`decision-chunks-in-the-host-local-store`);
  - two hook defects at `a32a2d4` that matter more now the hook spawns the ingester
    (`capture-and-backfill-write-host-local-index-only`): the hook's exit-2 path
    (`cmd/vp/cmd_hook.go:134-138`), and the surface warning that checks the global vault
    (`cmd/vp/main.go:73-86`);
  - `MoveDrawer` (`internal/storage/drawers.go:284`, called at `cmd/vp/cmd_audit.go:168`), which
    `vp audit rooms --apply` replaces with a local relabel
    (`palace-navigation-over-the-host-local-chunk-store`);
  - `vp_palace_backfill_decisions` (`internal/tools/palace_backfill_tools.go:310`), retired
    (decision 10) (`decision-chunks-in-the-host-local-store`);
  - the two importers, which write archives or the host-local index, and add a vibevault import's
    archives to the importing host's baseline set (decision 2)
    (`importers-write-the-frozen-tracked-corpus`; the slug predates the ruling, and the child's
    title is current).
- **The sourceaudit rule is a call-graph rule,** not a path rule. A path rule would trip on the
  authored triples that share `kg/triples/`, and on split, merge and copy, which legitimately write
  under `palace/<slug>/`.
  - **Sinks:** `AppendDrawers`, `AppendDrawer`, `AddEntities`, and the extractor's triple writer.
    That writer gets a function name of its own, distinct from the authored writer's, so the rule
    keys on the function, never on a field value.
  - **Roots:**
    - the pending-archive ingester, `IngestArchive` and `ingest.Run`;
    - `fileDecisionDrawers` (`internal/capture/decisions.go:248`);
    - `backfillFromArchives` (`internal/tools/system_tools.go:982`) and the `vp_refresh_index`
      handler;
    - `WriteSession` (`internal/capture/session.go:228`) and the `vp_capture_session` handler;
    - `DrainEnrichmentQueue` (`internal/capture/enrichqueue.go:115`);
    - the importer commands.
  - **Allow-list:** the migration command, and `cmd/vp/cmd_audit.go` until
    `palace-navigation-over-the-host-local-chunk-store` lands. The importers are roots now, not
    allow-listed, because they no longer write tracked derived data.
  - **Scope:** `cmd/vp` and `internal/palace` are included as well as `internal/capture`,
    `internal/tools` and `internal/search`.
  - **Owner:** `decision-chunks-in-the-host-local-store`. It runs only without `-short`.
- **Host-local counterparts** (`index-fingerprints-project-lifecycle-and-migration-marker`). The
  index directory needs the same treatment the embed cache gets:
  - the sweep (`internal/storage/embedcache_sweep.go:166`);
  - departed-project cleanup (`embedcache_departed.go:130`);
  - the split purge (`internal/tools/vault_split_apply.go:779`);
  - the read-only exemption (`internal/tools/readonly_serve.go:95-103`);
  - `vp vault project delete`, which purges `palace/.local/index/<p>/` with the project's trees,
    beside the embed cache it purges today (`collectDeleteTrees`,
    `internal/storage/lifecycle_delete.go:436`) (*Rulings on children review round 3*);
  - the lifecycle rename. It requires that it holds the index run lock, with kind `lifecycle`,
    and that `palace/.local/index/<to>/` does not exist. It then renames `palace/.local/index/<p>/`
    beside the embed cache
    (`slugCacheRel`, `internal/storage/project_slug_migration.go:2301`) and rewrites each chunk's
    `wing` under the index commit lock. Host-local chunk ids exclude the wing (decision 7), so no
    id changes;
  - in both, the change counter `.generation/<p>` is never deleted; its `epoch` changes, as for a
    reap.
- **The marker-gated ignore lines** (decision 11).
  - The reconciler emits the two derived-path lines only on a vault whose `vault.toml` carries the
    marker, on both the create and the top-up path. On a fresh vault, `vp vault init`
    (`internal/storage/vault_init.go:108`) writes the marker, the lines and the vault-level stamp at
    `MCPSurfaceVersion` (decision 11). Both belong to
    `tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker`.
  - The marker key, the `VaultManifest` field and the round-tripping `WriteFormat` belong to
    `index-fingerprints-project-lifecycle-and-migration-marker`.
  - The migration commit's stamp step and the empty-vault path belong to
    `one-shot-migration-to-authored-only-vault`.
- **Split and merge walk the filesystem**, not the tracked tree (`walkSplitTree`,
  `internal/tools/vault_split.go:135`, called at `:498`, `vault_merge.go:487` and
  `vault_split_apply.go:538`). They must subtract untracked, ignored files on a derived path
  (decision 11), or a stray drawer file travels into the destination vault. The same child,
  `split-and-merge-exclude-derived-palace-paths`, also owns:
  - the two-marker refusal: an unmigrated destination, or an unmarked source into a marked
    destination (decision 11);
  - the baseline-set additions by copy and merge (decision 2).
- **The Windows build.** The upstream PR, the temporary vendored copy and the `CGO_ENABLED=0`
  cross-build of every goreleaser target belong to `vector-index-interface-and-coder-hnsw-wrapper`.
- **Documentation with no code child.** These belong to
  `docs-for-the-host-local-index-and-authored-only-vault` and change with v9.2.0:
  - merging `docs/hnsw-architecture`;
  - the ADR-003 amendment (below);
  - `README.md:125-128`;
  - `doc/TUTORIAL.md:371`;
  - a banner on `doc/FLOWS.html` that marks it historical. It is a snapshot made by the
    VibeVault-era `vv flowdoc gen`, which is not in this repository, so it cannot be regenerated.
    `doc/flows.json` stays byte-identical: JSON cannot carry a banner, so the HTML banner states
    its status too (Chair ruling C9).
- **Documentation a code child changes with its code:**
  - `doc/MIGRATION.md`, for what a new import writes (`importers-write-the-frozen-tracked-corpus`);
  - `doc/TESTING.md:529`, `SessionCaptureToSearch`, which decision 7 invalidates
    (`capture-and-backfill-write-host-local-index-only`);
  - `doc/TESTING.md:535`, `ColdSearchBuildsIndexLazily`
    (`search-first-build-and-empty-corpus-answer`);
  - `doc/TESTING.md`'s recall section
    (`hnsw-parameters-from-real-vector-recall-and-production-wiring`).
- **Earlier ADRs** keep their text, by the convention in `doc/adr/README.md`. Once this ADR is
  accepted:
  - ADR-003 (the drawer lock sites) gets a dated amendment pointing here;
  - ADR-007 and ADR-010 optionally get one too.

## Open questions

The rulings of 2026-10-02 decided every question raised while drafting, including every question
the children deferred to this ADR. Each answer is recorded in a decision:
- the date-source precedence (decision 4);
- the authored/extracted classification and the `origin` field's format status (decision 5);
- the index choice by size and the Windows build (decision 6);
- the pending-archive ingester, its scope, budget, locks, write order and triggers, the tier
  table and the reaper rule (decision 7);
- the ledger baseline and `completeness.json` (decision 2);
- the coverage states and their order, their owner and how cross-project search reports skips
  (decision 8);
- the golden-file rule (decision 10);
- the marker's form and owner, the ignore lines, the stamp step, the empty-vault path,
  delete-not-move and where the end-to-end test lives (decision 11).

What remains are numbers, not decisions. The child that owns the code sets each one by recorded
measurement, and this ADR fixes no default for any of them:

- the tombstone threshold that triggers a graph rebuild: set by measurement in
  `vector-index-interface-and-coder-hnsw-wrapper` (the review suggests 10–20%);
- the chunk-count threshold at which a project switches from brute force to HNSW and its
  hysteresis bound, `M` and
  `EfSearch`, and the recall bar that replaces the unproven 0.90: set by measurement in
  `hnsw-parameters-from-real-vector-recall-and-production-wiring`, on real MiniLM vectors at
  ≥ 50k chunks, read from no quantum project (decision 7, "Quantum projects before the split");
- the watchdog's free-byte and free-inode floors: set by measurement in
  `explicit-resumable-index-rebuild-with-disk-watchdog`;
- the pending-archive ingester's per-run budget (archives per run, and the wall-clock cap) and the
  failure count N after which automatic runs skip an archive: set by measurement in
  `pending-archive-ingester-and-per-archive-commit-step`.
