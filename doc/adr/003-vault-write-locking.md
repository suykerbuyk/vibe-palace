# ADR 003: Vault Write Locking

**Status:** Accepted (2026-06-07)
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
