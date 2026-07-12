# ADR 003: Vault Write Locking

**Status:** Accepted (2026-06-07); amended 2026-07-11 (see *Amendment: the
resume.md lost-update hole*, then *Amendment: compare-and-set on blind
whole-file overwrites*, then *Amendment: the surgical editors and `EditResume`
are deleted* — *read that one first if you are here to write to `resume.md`*)
**Deciders:** Project owner
**Context:** Vibe-palace vault-write-concurrency — serializing vault read-modify-write

## Context

The `mcp-surface-handshake` Phase 2 write-gate audit surfaced a lost-update
hole in the vault write path. Vibe-palace has two whole-file write disciplines
— `internal/atomicfile.Write` (temp-file + `os.Rename`) for whole-file
replacement, and seek+append for JSONL drawers/entities/iterations — but
neither provides mutual exclusion between concurrent writers.

`atomicfile.Write` gives **crash-atomicity**: a reader never sees a torn file
because the rename is atomic. It does **not** serialize two writers. Both can
read the same base, each compute a new whole-file body, and each rename its
temp over the target — the second rename silently discards the first writer's
update.

This is not hypothetical. Two real contention paths exist:

- **Cross-process** — the `vp` CLI and the `vp mcp serve` MCP server are separate
  OS processes that can edit the same vault file at the same time (a human
  running `vp vault edit` while an agent drives `vp_vault_write`).
- **In-process** — `vp mcp serve` handles MCP calls concurrently, so two
  goroutines can read-modify-write the same file simultaneously.

The pre-existing `DeleteDrawer` bug was the canonical demonstration: it flocked
the target file descriptor, but `atomicfile.Write` renames a fresh temp over
the target, so the inode the lock was held on was swapped out — the lock
guarded a file that no longer existed at that path, and a concurrent
`AppendDrawer` (which flocked the *new* inode) never contended with it.

`expected_sha256` (the MCP CAS guard) does not close the hole. It is a
TOCTOU optimistic check across two client calls (Read → Write), not a
serializer: between its pre-read and the write there is still an unguarded
window, and it is optional, so writers that omit it race freely.

## Decision

Introduce a per-path **exclusive advisory lock** that serializes the entire
read→modify→write of a vault file, shared by every writer of that path.

### Sidecar lockfile, not a lock on the target

`internal/vaultlock` exposes a single entry point:

```go
func Acquire(vaultRoot, targetAbsPath string) (release func() error, err error)
```

It `flock`s (`LOCK_EX`, exclusive, blocking) a **sidecar** file at:

```
<vaultRoot>/.vp-locks/<sha256(canonicalKey(targetAbsPath))>.lock
```

The lock is held on the sidecar, never on the target, precisely because the
whole-file writer renames a temp over the target. A lock on the target's inode
would be swapped out by that rename (the `DeleteDrawer` failure above); the
sidecar is a stable inode for the life of the read-modify-write.

### The lock wraps the caller's read, not just the write

The lost-update window opens at the **read**, not at the write, so the lock is
acquired by the caller around its whole read→compute→write sequence — not
buried inside `atomicfile.Write`, which only sees the final bytes and has
already lost the race by then. In `internal/vaultfs`, `Write`/`Edit`/`Delete`
acquire the lock after path validation (so a rejected path never creates a
sidecar) and hold it across the SHA pre-read and the write; `Move` locks the
destination only.

### One lock object per path, shared by all writers

Append writers, whole-file writers, and RMW writers of the same file must
contend on the **same** lock object or they do not interlock. `internal/storage`
routes them all through `vaultlock.Acquire(v.Root, path)`:

- A `lockedWrite` helper wraps blind whole-file writes.
- RMW sites (`DeleteDrawer`, `InvalidateTriple`, `UpdateTaskStatus`,
  `WriteScoringConfig`) acquire the lock at the top, spanning their read, then
  call raw `atomicfile.Write` under it.
- Append writers (`AppendIteration`, `AppendDrawer`, `AddEntity`) acquire the
  sidecar lock instead of flocking the target fd — so `AppendDrawer` and
  `DeleteDrawer` finally interlock on the same lock object.

The `canonicalKey` policy mirrors `vaultfs.ResolveSafePath`'s
`EvalSymlinks` → parent-fallback → lexical-clean cascade, so a path supplied by
`vaultfs` (already symlink-resolved) and the same file supplied by `storage`
(a lexical `filepath.Join` of root and relative path) hash to the same key and
contend on the same sidecar.

### `expected_sha256` is kept as an orthogonal CAS guard

The lock provides serialization. `expected_sha256` is retained as the
independent cross-call optimistic guard (a client that reads in one MCP call
and writes in a later one can assert the file did not change in between). It is
**not** made mandatory — the lock makes single-call writes safe on its own, and
forcing CAS would break the common blind-write path.

> **Superseded for `resume.md` only** (see *Amendment: compare-and-set on blind
> whole-file overwrites*). The reasoning above still holds for CAS *as a
> serializer* — that is the lock's job, and it remains the lock's job. But for
> the one path that is a blind full-body overwrite, `expected_sha256` is now
> **required**: there is no "common blind-write path" worth preserving on
> `resume.md`, because a blind full-body write there is not a valid operation.
> Every other writer of every other path is unchanged.

### Platform scope

Unix uses a real `syscall.Flock` advisory lock; Windows is a no-op stub
(best-effort, unprotected). The implementation is build-tagged
(`flock_unix.go` / `flock_windows.go`). Advisory `flock` over NFS / network
filesystems is out of scope — vaults are assumed to live on a local
filesystem.

**Every locking claim in this ADR is a NO-OP ON WINDOWS.** `flock_windows.go`
returns `nil` from both `flockExclusive` and `funlock`, so `vaultlock.Acquire`
succeeds having locked nothing: it creates the sidecar, hands back a `release`
that unlocks nothing, and every writer "holds" the lock simultaneously. The
locking discipline therefore makes `resume.md` safe on unix and leaves it
**completely unprotected on Windows** — two concurrent writers there still lose
updates exactly as they did before the fix.

**This applies to the compare-and-set guard too, and CAS does not rescue it.**
Both surviving `resume.md` writers (`storage.WriteResume` and `vaultfs.Edit`)
compare the digest *inside* the lock — but on Windows that lock orders nothing,
so two writers can pass the same compare and one still loses. CAS **narrows** the
window on Windows; it does not close it. Do not read this ADR as "`resume.md` is
safe everywhere." Real Windows locking (`LockFileEx`) is the separate
`windows-lockfileex` task; until it lands, Windows is unprotected on *every*
vault path, not just this one.

## Consequences

**Positive:**

- Concurrent writers of the same vault file — CLI vs MCP, or two `vp mcp serve`
  goroutines — can no longer lose updates; the read-modify-write is
  serialized end to end.
- Append, whole-file, and RMW writers of one path share a single lock object,
  so the long-standing `AppendDrawer` / `DeleteDrawer` non-interlock is closed.
- The lock is anchored to a stable sidecar inode, immune to the rename-swap
  that defeated the old target-fd flock.
- `flock` is released automatically by the OS on process exit, so a `.lock`
  marker left behind by a crash is harmless: the next `Acquire` reopens and
  locks it cleanly.

**Negative / trade-offs:**

- A new `.vp-locks/` directory appears at the vault root. It is registered in
  `storage.CanonicalGitignorePatterns` (host-local, never synced) and refused
  by `vaultfs.IsRefusedWritePath` (the generic surface must never write content
  into it); it is not indexed (the search engine is project-scoped and never
  reads the vault root).
- `.lock` marker files accumulate (one per distinct path ever written) and are
  not garbage-collected; they are empty and harmless litter.
- **RMW callers must not double-acquire.** `flock` locks the open-file
  description, so a same-path second `Acquire` in the same process blocks
  forever. RMW sites therefore call raw `atomicfile.Write` under the held lock,
  never `lockedWrite` (which would re-acquire the same path and deadlock).
- Protection is unix-only. On Windows the lock is a no-op, so concurrent
  writers there remain unprotected.

## Alternatives considered

- **Flock the target file directly.** Rejected: `atomicfile.Write` renames a
  temp over the target, swapping the inode out from under any lock held on the
  target — the exact failure the pre-existing `DeleteDrawer` bug demonstrated.
  A sidecar with a stable inode is required.
- **Make `expected_sha256` mandatory (compare-and-set as the serializer).**
  Rejected: CAS is a TOCTOU optimistic check across two calls, not a
  serializer; it leaves an unguarded window and would break the blind
  single-call write path. CAS is kept as an orthogonal cross-call guard, not a
  replacement for the lock.
- **Put the lock inside `atomicfile.Write`.** Rejected: the lost-update window
  opens at the caller's read, which `atomicfile.Write` never sees. Locking only
  the final write is too late.

## Amendment (2026-07-11): the resume.md lost-update hole

> **Partly superseded — see the third amendment, *the surgical editors and
> `EditResume` are deleted*.** The diagnosis in this section still stands and is
> why the layering rule exists. The *remedy* does not: the six tools,
> `internal/mdutil`, and `storage.EditResume` have all been deleted. Do not reach
> for `EditResume`; it no longer exists. This text is kept as the record of the
> hole and of why vault I/O must stay in `storage` / `vaultfs`.

The discipline above was correct and the primitive worked — and `resume.md` was
losing updates anyway, because the layer that edits it never took the lock.

### The hole

Six MCP tools — `vp_thread_insert` / `_replace` / `_remove` and
`vp_carried_add` / `_remove` / `_promote_to_task` — did their own lock-free
read-modify-write of `Projects/<slug>/resume.md` from `internal/tools`: a bare
`os.ReadFile`, a mutation via `internal/mdutil`, and an `atomicfile.Write`.
Meanwhile `storage.WriteResume` → `lockedWrite` dutifully took the `vaultlock`
on that same path.

**An advisory lock only excludes the writers who take it.** A writer that
reaches around it is not blocked by it and does not block it; the lock provided
*zero* mutual exclusion for `resume.md`. A regression test firing 32 concurrent
`vp_thread_insert` calls lost 22 of them — every lost insert returning a nil
error and a `bytes_written` payload, so the caller was told it succeeded.

The root cause is **layering**, not a missing `Acquire` call. Every
`vaultlock.Acquire` site lives in `internal/storage` / `internal/vaultfs`; the
tools layer performed vault I/O directly and so sat *outside* the discipline
this ADR describes. A rule that a caller can silently opt out of is not a rule.

### `storage.EditResume` — the sanctioned locked-RMW combinator (DELETED)

> **This combinator no longer exists.** It went callerless when the six surgical
> editors were deleted, and was removed with them. The locked-RMW *shape* it
> describes remains the correct pattern for any future multi-step vault mutation,
> which is why the reasoning is preserved — but there is no `EditResume` to call.
> For `resume.md`, use `vp_vault_read` + `vp_vault_edit` (CAS-chained); see the
> third amendment.

```go
func (v *Vault) EditResume(project string, mutate func(string) (string, error)) error
```

`EditResume` acquires the per-path lock, `defer`s the release, ensures the
project dir, reads the **authoritative** content under the lock, calls `mutate`,
and writes the result — all inside one critical section. A `mutate` error aborts
the edit without writing.

**RULE: a surgical editor must never do its own read or write of a vault file.**
It supplies a pure transform and lets `internal/storage` own the I/O and the
lock. The six tools are now exactly that — pure `mdutil` transforms with zero
I/O and zero lock knowledge, routed through `EditResume`. This makes the
discipline structural: there is no lock to forget, because there is no I/O in
the tools layer to forget it around.

A bonus falls out of reading under the lock: the transform sees current content,
so the surgical editors need no CAS guard. They carry a slug plus a delta, not a
whole-file body.

### Never `lockedWrite` under a held lock

`EditResume` writes with a raw `atomicfile.Write`, **not** `lockedWrite`.
`lockedWrite` re-acquires the same per-path lock, and `vaultlock.Acquire` is a
blocking `LOCK_EX` with no `LOCK_NB` and no timeout — so the re-entry is a
**permanent self-deadlock, not an error**. Nothing times out, nothing returns,
nothing is logged; the goroutine hangs forever. This is the pre-existing
"RMW callers must not double-acquire" trade-off below, restated because it is
easy to reintroduce: *inside a held lock, write with `atomicfile.Write`.*

### Sequential locks, never nested

`vp_carried_promote_to_task` touches two locked files (a task file and
`resume.md`). It is structured so the two locks are held **sequentially and
never nested**:

1. read + validate the carried bullet **unlocked** (a snapshot, never written
   back);
2. `vault.CreateTask` — takes and releases the **task** file's lock;
3. `vault.EditResume` — re-reads `resume.md` under the **resume** lock and
   removes the bullet from the authoritative content.

Because no lock is ever held while another is acquired, **no lock order exists
to invert**, and there is no deadlock to document or defend. Given that
`Acquire` is a blocking `LOCK_EX` with no timeout, a future inversion would be a
permanent hang rather than a detectable error — so the invariant is: do not
hoist the resume lock across the `CreateTask` call.

The two-file operation is still not atomic across a crash (a task can exist
while its bullet remains). Locking never claimed to fix that.

### `CreateTask` TOCTOU

`CreateTask`'s "already exists" `os.Stat` ran **outside** the lock: two
concurrent creates of the same slug both passed the check, and one silently
overwrote the other, breaking the already-exists contract the promote path
relies on. The check now runs **inside** the per-path lock, and the write is the
raw `atomicfile.Write` the section above requires.

### What this does NOT fix

Nothing here addresses the **stale-read** mode: an agent that reads `resume.md`,
thinks for several minutes, computes a full-file body from that stale snapshot,
and blind-writes it through `vp_update_resume` is not racing anyone. It is
simply wrong — and it now holds the lock while being wrong. Serialization cannot
help a writer whose input is out of date; that needs a compare-and-set default
on blind whole-file overwrites, which is the separate
`default-cas-for-blind-overwrites` task. `expected_sha256` remains the
(currently optional) guard for that class.

> **Since closed** — `default-cas-for-blind-overwrites` shipped on 2026-07-11.
> `expected_sha256` is no longer optional on `resume.md`. See the next
> amendment.

And, per *Platform scope* above: on Windows this fix protects nothing.

### Consequences of the amendment

**Positive:**

- `resume.md` is finally covered by the discipline this ADR claims to describe
  (on unix): concurrent surgical editors, `WriteResume`, and the CLI all contend
  on the same lock object.
- The tools layer can no longer reach around the lock, because it no longer does
  vault I/O at all.
- `CreateTask`'s already-exists contract holds under concurrency.

**Negative / trade-offs:**

- `EditResume` is resume-specific. Other files still get their locked-RMW
  ad hoc inside `internal/storage`; if a second surgical-edit surface appears,
  the combinator should be generalized rather than copied.
- Every surgical edit now serializes on one lock per project. Wrap-time edits are
  small and infrequent, so the contention cost is irrelevant — but a future
  high-volume editor would feel it.

## Amendment (2026-07-11): compare-and-set on blind whole-file overwrites

The lock closed the *race*. It could not close the *stale read* — and the
previous amendment said so explicitly ("What this does NOT fix"). This closes
it, for the one path where a blind full-body overwrite exists.

### `WriteResume` is now compare-and-set; there is no blind path left

```go
func (v *Vault) WriteResume(project, content, expectedSha256 string) error
```

- **`expectedSha256` non-empty** — `resume.md`'s current on-disk SHA-256 must
  equal it, or the write is refused and the file is left untouched.
- **`expectedSha256 == ""`** — *assert-absent*. The write succeeds only if no
  `resume.md` exists yet (first-wrap / bootstrap create). If one **does** exist,
  that is a conflict and is refused.

There is no third case, and that is the whole point: **an omitted guard can
never silently degrade to last-writer-wins.** `expected_sha256` is a REQUIRED
argument on `vp_update_resume`, which returns a machine-parseable conflict
carrying the actual current digest so a caller can re-read and retry without
scraping an error string. The refusal is `*storage.ResumeConflictError`, which
unwraps to `vaultfs.ErrShaConflict` — so `errors.Is(err, vaultfs.ErrShaConflict)`
holds uniformly across both CAS writers in the repo rather than forking a second
conflict concept.

The compare runs **inside** the held per-path lock. A compare outside it is a
TOCTOU — the same mistake the original Context section rejects CAS for. CAS here
is not a replacement for the lock; it is a second, orthogonal guard *stacked on
top of* the lock, and it only works because the lock is underneath it.

The cleanup that fell out: `WriteWorkflow`, `WriteKnowledge`, `WriteDoc`, and
`WriteAbsorbed` (blind whole-file `lockedWrite`s of `project_dirs.go`, reachable
only from tests) are **deleted**, and the dead `resume.md` branch in
`internal/absorb`'s `resolveDestPath` is **deleted**. That branch was
unreachable — every resume-bound absorb item is `DestResumeScratch` and is
diverted to `absorbed/resume-suggestions.md` for human merge — but it was a live
trap: absorb's private `atomicWrite` takes no `vaultlock`, so wiring it up would
have blind-overwritten `resume.md` outside *both* the lock and the new CAS,
reopening from the CLI exactly the hole this amendment closes in the MCP surface.

### Why CAS belongs here and was correctly rejected on `EditResume`

The two rejections are not in tension; they are about two different operations.

**The surgical editors carry a delta, not a body.** `vp_thread_replace(slug,
body)`, `vp_carried_add(bullet)`, and their siblings supply a slug plus one
block. `EditResume` reads the authoritative content *under the lock* and the
transform recomputes the new body from **that fresh read** — never from whatever
the caller last saw. They are **stale-read-immune by construction**: there is no
stale snapshot in the write path to be stale about. Bolting a whole-file CAS
onto them would only manufacture **spurious conflicts** — two agents editing two
*disjoint* blocks of `resume.md` would collide on a whole-file digest and one
would be refused, despite neither having lost anything. The original rejection
stands, unamended.

**A blind full-body overwrite carries no delta and has no fresh read.**
`vp_update_resume` hands over an entire replacement body computed from a snapshot
the caller took at some arbitrary earlier time. Reading under the lock buys it
*nothing* — the content it is about to write was already decided. And for this
operation there is no such thing as a spurious conflict: if the file changed
since the caller read it, the caller's body genuinely **does** revert that
change. Every conflict is a real one. That asymmetry — *delta + fresh in-lock
read* vs. *whole body + stale snapshot* — is the entire criterion for where CAS
belongs.

### Read-path contract: the digest is of the RAW pre-expansion bytes

`vpctx.Resolver.ResolveDigest` hashes the bytes **as they sit on disk**, before
template expansion. `vp_get_resume` / `vp_get_workflow` return that as
`sha256`; `vp_bootstrap_context` returns `resume_sha256` (of the full body,
computed pre-excerpt so a truncated preview still yields a usable guard).

This is not an implementation detail — it is a correctness requirement. The
resolver runs `expandScoped` (`{{PROJECT}}`, `{{DATE}}`, …) over what it
returns, so hashing the **returned string** would produce a digest matching
nothing on disk, and every CAS would fail. Worse, `{{DATE}}` expands from
`time.Now()`, so the returned string's digest would differ **on every call** —
a guard that can never be satisfied. Hash what a writer will compare against:
the file.

### Known, ACCEPTED residual: block-granularity staleness

The surgical editors are stale-read-immune at **file** granularity, not at
**block** granularity. An agent that reads a thread block, thinks for ten
minutes, and calls `vp_thread_replace(slug, body)` with a body derived from that
stale block will clobber a concurrent rewrite of *that same block*. The write is
correctly serialized and the rest of the file is correctly preserved — the
**blast radius is exactly one block**, and only when two agents rewrite the same
block concurrently.

This is accepted, not overlooked. If it is ever closed, the unit is a **per-block
digest compared inside the lock** — the caller asserts the digest of the block it
read, and `EditResume`'s transform refuses if that one block moved. It is
**never** a whole-file CAS on `EditResume`: that would reintroduce precisely the
spurious-conflict failure the section above rejects.

### Platform caveat (unchanged, and it applies to CAS too)

Per *Platform scope*: `vaultlock` is a no-op stub on Windows. The CAS
**pre-read is therefore unprotected there** — the compare still happens and a
genuinely stale write is still refused, but nothing serializes the read against
a concurrent writer, so two writers can both observe the same digest, both pass
the compare, and one can still lose. CAS narrows the window on Windows; it does
not close it. Only `windows-lockfileex` does.

### Consequences of this amendment

**Positive:**

- The stale-read mode the previous amendment left open is closed for
  `resume.md`: an agent writing a body computed from an out-of-date snapshot is
  now **refused with the current digest**, not silently obeyed.
- CAS cannot be forgotten into last-writer-wins: the guard is a required
  argument, and its empty value means *assert-absent*, not *skip the check*.
- Four blind whole-file writers and one unreachable blind-write branch into
  `resume.md` are gone, so the invariant "no blind whole-file resume overwrite
  path exists" is enforced by the absence of code, not by discipline.

**Negative / trade-offs:**

- `vp_update_resume` callers must now read before they write. The wrap and
  cancel-plan templates thread the sha through; an out-of-band caller that skips
  the read gets a conflict, by design.
- The guard is file-granular, so the block-granularity residual above remains.
- On Windows the compare is unserialized (above).

## Amendment (2026-07-12): the surgical editors and `EditResume` are deleted

**Read this before writing to `resume.md`.** The two amendments above describe a
write path that no longer exists.

### What was removed

The six surgical editors (`vp_thread_insert` / `_replace` / `_remove`,
`vp_carried_add` / `_remove` / `_promote_to_task`), the `internal/mdutil` package
that backed them, and `storage.EditResume` — the locked-RMW combinator the first
amendment introduced — have all been deleted. `EditResume` went callerless the
moment the six tools did; keeping a locked-RMW combinator with no callers would
have been a trap, not a safety net. The MCP surface went 68 → 62 tools, 24 → 18
mutating. `MCPSurfaceVersion` stays **1**: removing tools changes nothing about
what is written on disk.

The tools were not merely unused — they were **broken**. They target `###`
sub-headings, and no `resume.md` in the vault has any; `vp_thread_insert` with
`position: "top"` against a bullet-shaped `## Open Threads` silently reparented
the whole section body under a new `### slug` block, which a later
`vp_thread_remove` would then delete wholesale. That is a two-call silent
data-loss path, and no command template ever named the tools, so the safety
apparatus of the first amendment was protecting code no agent could reach.

### `resume.md` now has exactly two writers, both locked AND CAS'd

- **`storage.WriteResume`** — whole-file regeneration and migrations, behind
  `vp_update_resume`. `expected_sha256` required; `""` means *assert-absent*.
- **`vaultfs.Edit`** — one section at a time, behind `vp_vault_edit`. **This is
  the routine wrap path**: every ordinary resume update now goes through it.

`vaultlock.canonicalKey` normalizes both path spellings to one key, so
`vaultfs.Edit` takes the *same* lock `EditResume` did. The locking discipline of
this ADR is intact; only the combinator is gone.

**CAS is now the primary discipline, not a backstop.** Every routine resume write
is a compare-and-set edit.

### The spurious-conflict objection now bites — and we accepted it

*Why CAS belongs here and was correctly rejected on `EditResume`* argued that a
whole-file CAS would manufacture **spurious conflicts**: two agents editing
*disjoint* blocks would collide on a whole-file digest though neither lost
anything. That objection was sound, and the wrap path now **incurs exactly it** —
`vaultfs.Edit`'s guard is file-granular, so two agents editing different sections
of `resume.md` concurrently will conflict.

This is an accepted trade, made with open eyes: a **loud, recoverable** spurious
conflict (re-read, re-derive the anchor, resubmit) is strictly better than the
**silent data-loss path** the editors actually were. The cure for a spurious
conflict is a retry; the cure for a silent clobber is a restore from git.

### The block-granularity residual is improved, not merely carried forward

`vaultfs.Edit` asserts the *content* it is replacing via `old_string`, which is a
**finer** assertion than the per-block digest the previous amendment proposed as
the eventual fix. If the region moved, the anchor no longer matches and the edit
fails **loudly** (`old_string not found`) instead of clobbering. Ambiguity fails
loudly too (`old_string occurs N times`). The three loud failures — not-found,
ambiguous, sha-mismatch — are the safety net. **Fix a failing anchor by making it
more specific, never by broadening it until something matches.**

### The write path inherits the raw-vs-expanded trap

*Read-path contract* above says the digest is of the RAW pre-expansion bytes.
That is now a **write-path** correctness requirement as well. `vp_get_resume` /
`vp_bootstrap_context` serve placeholder-**expanded** bodies while hashing the
**raw** ones, so text taken from them is poison for a write-back: an `old_string`
spanning a placeholder will not match disk (loud, harmless), and a whole-file body
composed from them **passes CAS and silently bakes the expanded values onto disk**,
destroying the live tokens. `expandScoped` is a blind `strings.ReplaceAll` — it
does not respect code spans, so tokens inside backticks are eaten too.

**`vp_vault_read` is the only legal source of text you intend to write back.**
`vp_get_resume` is for reading. `vp_update_resume`'s description, its schema, and
the `remedy` in its conflict payload all say so — that last one matters most,
because it fires exactly when an agent is recovering from a conflict and is most
likely to do what it is told.

### What this does NOT deliver

- **Structural validation.** `vaultfs.Edit` will happily accept an edit that
  mangles section structure. That is the one guarantee typed editors would have
  given, and it is deliberately forgone. Loud failure on a bad anchor is the
  mitigation, not a substitute. Cap violations are now **detected**, not
  prevented: `vp check --check resume-caps` (read-only) warns on size and row
  overruns, because with the typed editors gone there is no write path left to
  enforce a cap *at*.
- **Windows protection.** Unchanged and still absent; see *Platform scope*.

### Consequences of this amendment

**Positive:**

- The only two known silent-corruption paths into `resume.md` are gone: the
  editors' reparenting bug (deleted) and the blind whole-file rewrite composed
  from expanded content (now refused, and documented at all three places the tool
  speaks to the agent).
- ~2,360 net lines removed, including a whole package, with zero new tool code.
- The end-to-end guarantee is pinned by
  `TestIntegration_UpdateResumeStaleWriteRefused`
  (`internal/integration/resume_cas_test.go`), which drives the refusal through
  real JSON-RPC dispatch with `vp_vault_edit` as the racing writer, and is
  mutation-proven against the in-lock compare.

**Negative / trade-offs:**

- Spurious file-granular conflicts on concurrent disjoint edits (above).
- Anchors are composed by hand, so a careless `old_string` fails loudly rather
  than doing something clever. This is the intended posture.

## References

- Lock primitive: `internal/vaultlock/` (`vaultlock.go`, `flock_unix.go`,
  `flock_windows.go`)
- Write-path call sites: `internal/vaultfs/write.go`;
  `internal/storage/` (`vaultlock_write.go`, `drawers.go`,
  `knowledge_graph.go`, `tasks.go`, `config.go`, `project_dirs.go`)
- Gitignore registration: `internal/storage/git.go`
  (`CanonicalGitignorePatterns`); path refusal:
  `internal/vaultfs/safety.go` (`IsRefusedWritePath`)
- The write-gate audit that surfaced the hole: `mcp-surface-handshake`
  Phase 2 (see ADR-002 references and PRD §1.1)
- Locked-RMW combinator (1st Amendment) — **DELETED** (3rd Amendment):
  `storage.(*Vault).EditResume`, `internal/tools/thread_tools.go`,
  `internal/tools/carried_tools.go`, and `internal/mdutil/` no longer exist; they
  survive only in git history. The TOCTOU fix in `internal/storage/tasks.go`
  (`CreateTask`) **does** survive — `CreateTask` outlived the editor that was its
  second caller
- Surviving `resume.md` writers (3rd Amendment): `internal/storage/project_dirs.go`
  (`(*Vault).WriteResume` — whole-file, behind `vp_update_resume`) and
  `internal/vaultfs/write.go` (`Edit` — surgical, behind `vp_vault_edit`, the
  routine wrap path). Cap detection: `internal/check/resume.go`
  (`vp check --check resume-caps`)
- Amendment coverage: `internal/storage/project_dirs_test.go`,
  `internal/storage/tasks_test.go`, and the full-stack
  `internal/integration/resume_cas_test.go`
  (`TestIntegration_UpdateResumeStaleWriteRefused` — `vp_vault_edit` as the
  racing writer; it replaces the deleted `resume_editors_concurrent_test.go`).
  See `doc/TESTING.md`
- Compare-and-set writer (2nd Amendment): `internal/storage/project_dirs.go`
  (`(*Vault).WriteResume`, `ResumeConflictError`); the conflict sentinel
  `internal/vaultfs` (`ErrShaConflict`); the required guard in
  `internal/tools/context_tools.go` (`vp_update_resume`); the read-path digest
  in `internal/context` (`Resolver.ResolveDigest`, hashing RAW pre-expansion
  bytes) surfaced by `vp_get_resume` / `vp_get_workflow` (`sha256`) and
  `vp_bootstrap_context` (`resume_sha256`); the templates that thread it
  through: `internal/templates/templates/commands/{wrap,cancel-plan}.md`
- Still open: `windows-lockfileex` (real locking on Windows) and
  `unlocked-rmw-writers-beyond-resume` (absorb's `workflow.md` / `knowledge.md`
  / `doc/*.md` writes still take no `vaultlock`)
