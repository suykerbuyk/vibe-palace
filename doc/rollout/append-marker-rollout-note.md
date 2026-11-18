# Rollout note: the index counter's append marker (restart after upgrading)

<!-- TODO(operator): fold this note into the next release-notes file under
doc/rollout/ (the latest present is v10.2.0-release-notes.md) once the version
is named, and repeat it in the release commit body. It is a standalone file
until then. -->

Task `append-only-tx-kill-leaves-append-unannounced`; ADR-014 decision 7,
"The in-flight flag", amendment of 2026-11-18.

**What changed.** An append-only index commit now sets a third field, `a`, in
the per-project counter file (`palace/.local/index/.generation/<project>`) before
its first write. A search that finds `a` left by a process that died rebuilds and
serves the append with no new writer. A live append costs a search nothing.

**The rule.** The guarantee holds only once EVERY process that can take a
project's index commit lock runs the new binary: every `vp mcp` server and every
hook-spawned `vp` (ingest, rebuild, capture).

**After upgrading:**

1. Restart every `vp mcp` server (each client session holds one).
2. Let in-flight hook-spawned `vp` processes finish (or restart them).
3. Until both are done, do not rely on the killed-append guarantee.

**What an old binary does with the new counter.** It cannot parse `a`:

- as a reader it errors on the counter (the KG read fails; a search rebuilds)
  for the duration of each new writer's append;
- as a writer its `Lock` rewrites the counter to generation 0 with a new epoch.
  Every engine reloads; **no data is lost**. It also erases the marker of a dead
  new-binary writer, and its own commits are not marked, so its own killed
  appends stay unannounced, which is how it behaved before.

**Cost of the new binary.** One extra fsynced counter write per appending commit
(about 3 ms on a virtio disk) and a non-blocking lock probe per read while a
marker is seen.

## Claim directory

Session claim sentinels (`claimed-<session-id>`) now live in the `.vibe-palace/`
of the nearest `.vibe-palace.toml` root, not the raw cwd, so `vp_capture_session`
and the SessionEnd hook agree wherever each ran inside the project. Upgrade when
no session is mid-flight outside its project root; at worst one duplicate
session note per such in-flight session.
