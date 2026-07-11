# ADR 003: Vault Write Locking

**Status:** Accepted (2026-06-07); amended 2026-07-11 (see *Amendment: the
resume.md lost-update hole*)
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

### Platform scope

Unix uses a real `syscall.Flock` advisory lock; Windows is a no-op stub
(best-effort, unprotected). The implementation is build-tagged
(`flock_unix.go` / `flock_windows.go`). Advisory `flock` over NFS / network
filesystems is out of scope — vaults are assumed to live on a local
filesystem.

**Everything in this ADR — including the `EditResume` fix in the Amendment
below — is a NO-OP ON WINDOWS.** `flock_windows.go` returns `nil` from both
`flockExclusive` and `funlock`, so `vaultlock.Acquire` succeeds having locked
nothing: it creates the sidecar, hands back a `release` that unlocks nothing,
and every writer "holds" the lock simultaneously. Routing the surgical editors
through `EditResume` therefore makes `resume.md` safe on unix and leaves it
**completely unprotected on Windows** — two concurrent `vp_thread_insert`
calls there still lose updates exactly as they did before the fix. Do not read
this ADR as "`resume.md` is safe everywhere." Real Windows locking
(`LockFileEx`) is the separate `windows-lockfileex` task; until it lands,
Windows is unprotected on *every* vault path, not just this one.

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

### `storage.EditResume` — the sanctioned locked-RMW combinator

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
- Locked-RMW combinator (Amendment): `internal/storage/project_dirs.go`
  (`(*Vault).EditResume`); its callers `internal/tools/thread_tools.go`
  (`editResume`) and `internal/tools/carried_tools.go`; the TOCTOU fix in
  `internal/storage/tasks.go` (`CreateTask`)
- Amendment coverage: `internal/storage/project_dirs_test.go`,
  `internal/tools/thread_tools_test.go`,
  `internal/tools/carried_tools_test.go`,
  `internal/storage/tasks_test.go`, and the full-stack
  `internal/integration/resume_editors_concurrent_test.go` (see `doc/TESTING.md`)
- Still open: `windows-lockfileex` (real locking on Windows) and
  `default-cas-for-blind-overwrites` (the stale-read mode)
