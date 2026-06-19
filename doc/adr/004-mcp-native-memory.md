# ADR 004: MCP-Native Memory

**Status:** Accepted (2026-06-19)
**Deciders:** Project owner
**Context:** Vibe-palace mcp-native-memory — host-agnostic AI memory in the vault

## Context

Claude Code writes a host-local "native memory": typed markdown files (with a
`metadata.type` frontmatter) plus an index, under
`~/.claude/projects/<encoded-cwd>/memory/` (including `MEMORY.md`). That store
has three properties that make it unsuitable as vibe-palace's memory of record:

- **Claude-only.** Grok and Zed — the other MCP hosts vibe-palace supports —
  have no native memory directory at all. Anything written there is invisible
  to them.
- **Not synced.** It lives under `~/.claude/`, host-local and gitignored; it
  does not travel with the vault across machines.
- **Re-injected, not recalled.** The native store is read back by the host's own
  injection, not by vibe-palace, so it never participates in
  `vp_bootstrap_context` and cannot be the single source of truth.

An earlier approach — `memory-link-command`, symlinking the host-local native
dir into the vault so Claude's writes would land in synced storage — was
**cancelled**. A symlink is still Claude-shaped (the other hosts have nothing to
link), it leaks the host-local layout into the vault, and it does not give a
host-agnostic *write* surface.

## Decision

Make memory a first-class MCP capability owned by vibe-palace, stored in the
synced vault, with recall through bootstrap and a one-way drain of Claude's
native store.

### Memory as MCP tools

Four host-agnostic tools form the write/read surface, usable identically from
Claude, Grok, and Zed:

- `vp_memory_write` / `vp_memory_read` / `vp_memory_list` / `vp_memory_delete`

Memory is **project-scoped**, stored at `Projects/<slug>/memory/`. Each file
carries `name` and `description` at the top level and the type nested under
`metadata.type` — one of `user`, `feedback`, `project`, `reference` — matching
Claude's native auto-memory frontmatter so harvested and tool-written files
share one on-disk shape (`internal/storage/memory.go`).

### Recall via bootstrap

`vp_bootstrap_context` returns a `memory` index — `name`/`description`/`type`/
`rel` only, never bodies. Bodies are read on demand with `vp_memory_read`. This
keeps the session-start payload cheap while making every memory discoverable to
every agent.

### One-way harvest of native memory

`vp memory harvest` (CLI), `vp_memory_harvest` (MCP), and an automatic drain
wired into `vp hook` at **SessionEnd only** drain Claude's host-local native
memory into the vault. Harvest routes each typed file into
`Projects/<slug>/memory/`, dedupes identical content, writes same-name/
different-content under a `.harvested-<ts>` suffix, drops the `MEMORY.md` index,
and then **deletes the host-local originals** (including `MEMORY.md` — a
surviving index would re-inject a stale pointer set next session). It is
strictly one-way: the vault becomes the single source of truth. A non-Claude
host with no native dir is a clean no-op, never an error
(`internal/memory/harvest.go`). Stop and PreCompact hook events never harvest.

### Dual commit/sync model (Option A)

Memory is committed by **two** independent paths, by design:

1. **Claude SessionEnd harvest.** When `vp hook` fires `SessionEnd`, harvest
   commits the whole `Projects/<slug>/memory/` dir (plus the `.surface` stamp)
   if it is dirty — covering both native drains **and** direct `vp_memory_write`
   calls made on Claude during the session.
2. **`/wrap`.** Wrap includes `Projects/<slug>/memory/` among its
   `vp_vault_sync` paths — the host-agnostic path that covers Grok and Zed,
   which never run `vp hook` and so never reach the harvest commit.

Both paths are required because each covers a gap the other cannot:

- Harvest covers Claude's native store and Claude's direct writes, at
  SessionEnd, but only Claude runs `vp hook`.
- `/wrap` covers every host (including Grok/Zed) but only runs when the operator
  wraps.

Because both commit paths exist, the wrap **preflight no longer nags** about
uncommitted memory. `internal/wrapstate` splits vault dirt by category: dirty
memory paths set a `MemoryHasUncommittedWrites` signal and surface as a
`memory_dirty` **NOTE** ("committed automatically by wrap/SessionEnd, not a
blocker"), while non-memory dirt remains the nag-worthy `vault_dirty` warning.
Suppressing the memory nag is safe **only** because both commit paths guarantee
the memory will be committed.

### Memory is outside the tidy sweep

`vp vault tidy` classifies and commits only machine-generated capture artifacts;
it never `git add -A`. Memory is **user-persistent content** — like `resume.md`,
`tasks/`, and `iterations.md` — not a capture artifact, so it is deliberately
outside tidy's `sweepRules` and is reported as user content, never swept. Its
commit is owned by harvest and `/wrap`, not by tidy.

## Consequences

**Positive:**

- A single, host-agnostic write surface for AI memory works identically on
  Claude, Grok, and Zed; memory lives in the synced vault and travels across
  machines.
- Recall is unified through `vp_bootstrap_context`, so memory reaches every
  agent without host-specific re-injection.
- Claude's native store is drained one-way and the originals deleted, so there
  is exactly one source of truth and no stale re-injection.
- The dual commit model guarantees memory is persisted regardless of which host
  produced it, which lets preflight drop the (now redundant) memory nag.

**Negative / trade-offs:**

- Two commit paths touch `Projects/<slug>/memory/`; both are written to no-op
  when the paths are clean (no empty commits), but the dual ownership is more
  moving parts than a single committer.
- Harvest is SessionEnd-only and transcript-driven on the hook path, so memory
  written on a host that never reaches SessionEnd is committed only at `/wrap`.
- Memory is project-scoped; there is no global/cross-project memory yet (see
  deferral below).

## Deferred: global / cross-project memory (v2)

Global, cross-project memory is **deferred to v2**. The natural home — a
top-level `Knowledge/` tree — is not surface-recognized (the project surface is
`Projects/<slug>/`-scoped), so exposing it would require new surface and
recognition work. It also overlaps the `cross-project-learnings-tools` effort
and is best designed together with it rather than bolted onto the project-scoped
memory surface. v1 ships project-scoped memory only.

## Alternatives considered

- **Symlink the native dir into the vault (`memory-link-command`).** Cancelled:
  still Claude-only (Grok/Zed have nothing to link), leaks the host-local layout
  into the vault, and provides no host-agnostic write surface.
- **Two-way sync with the native store.** Rejected: re-injecting from the vault
  back into the host-local dir reintroduces a second source of truth and the
  stale-pointer problem the index-deletion specifically avoids. Harvest is
  deliberately one-way.
- **Commit memory only at `/wrap`.** Rejected: Claude sessions that end without
  a wrap would leave memory uncommitted; the SessionEnd harvest closes that gap
  for the host that has a native store.
- **Sweep memory with `vp vault tidy`.** Rejected: memory is user content, not a
  capture artifact; tidy classifies and would either mis-handle it or require a
  bespoke rule. Harvest and `/wrap` own its commit instead.

## References

- Storage + frontmatter: `internal/storage/memory.go`
- Harvest engine: `internal/memory/harvest.go`; hook wiring:
  `internal/hook/hook.go` (SessionEnd only)
- MCP tools: `internal/tools/memory_tools.go`
  (`vp_memory_write`/`read`/`list`/`delete`/`harvest`); CLI: `cmd/vp/cmd_memory.go`
- Preflight categorization: `internal/wrapstate/` (`collect.go`, `preflight.go`)
- Tidy scope: ADR-003 references and `doc/ARCHITECTURE.md` §Vault Housekeeping
- Deferral overlap: `cross-project-learnings-tools`
