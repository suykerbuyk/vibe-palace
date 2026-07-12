# ADR 001: Transcript Archive Format

**Status:** Accepted (2026-04-15)
**Deciders:** Project owner
**Context:** Vibe-palace Plan 4 — Transcript Archive / Copyright Provenance Ledger

## Context

AI-assisted coding sessions produce code, designs, and intellectual
property. Under current US copyright law — reaffirmed by the Supreme
Court's March 2026 denial of certiorari in *Thaler v. Perlmutter* —
works of purely AI authorship are ineligible for copyright. Human
creative contributions to AI-assisted work remain copyrightable, but
only to the extent they can be **proved**.

Vibe-palace's session notes (markdown in the vault) are authored after
the fact and can be edited at any time. They are suitable as a working
memory. They are **not** suitable as evidentiary records of human
authorship.

Vibe-vault (`vv`) addressed this with `vv archive`: zstd-compressed
raw JSONL transcripts pulled from Claude Code's on-disk session log.
Vibe-palace inherits the need and must define its own archive format
before implementing adapters.

MCP servers observe tool calls, not user/assistant turns. The archive
cannot be produced by the vibe-palace MCP server itself. It must be
produced by **per-IDE adapters** invoked from host-level hooks
(SessionEnd in Claude Code, equivalent in Zed, etc.).

## Decision

### Directory layout

```
<vault>/Projects/<slug>/transcripts/
  YYYY-MM-DD-<session-id>.jsonl.zst      # compressed transcript
  YYYY-MM-DD-<session-id>.manifest.json  # provenance manifest
  YYYY-MM-DD-<session-id>.manifest.json.sig  # optional signature (Phase 6)
```

- One pair of files per `(session_id, adapter)` tuple. Re-runs are
  idempotent no-ops.
- Date prefix is capture-time UTC for filesystem ordering; the
  authoritative timestamp is inside the manifest.

### Manifest schema (v1)

```json
{
  "schema_version": 1,
  "adapter": "claude-code",
  "adapter_version": "1.0.0",
  "session_id": "abc123...",
  "model": "claude-opus-4-6",
  "turn_count": 47,
  "source_sha256": "e3b0c44...",
  "source_bytes": 184523,
  "compressed_bytes": 18934,
  "git_head": "2869ff5...",
  "project_slug": "vibe-palace",
  "vault_rel_session_note": "Projects/vibe-palace/sessions/2026-04-15-restart.md",
  "captured_at": "2026-04-15T14:32:07Z",
  "captured_by_hostname": "workstation-01",
  "vp_version": "0.X.Y"
}
```

Field notes:

- **`source_sha256`** is the hash of the **pre-compression** JSONL
  byte stream. This is the evidentiary anchor. Post-compression SHAs
  are implementation details — zstd frame headers can vary across
  versions — and would not survive a recompression. The pre-compression
  hash survives extraction, re-examination, and re-archiving.
- **`git_head`** pins the work-in-progress to a point in the repo
  history. Combined with a Phase-7 git-note, this binds the ledger
  to the commit graph.
- **`vault_rel_session_note`** closes the loop with the session
  markdown, which in turn carries an `archive:` frontmatter key
  pointing back to the manifest. Bidirectional traversal.
- **`vp_version`** lets us evolve the manifest schema without breaking
  older archives.

### Idempotency

The tuple `(session_id, adapter)` is the identity key. Calling
`vp archive --adapter=X --session-id=Y` twice produces the same two
files; the second call is a no-op if the manifest already exists and
its `source_sha256` matches the current source.

If the source has changed (e.g., Claude Code appended turns to an
ongoing session), the archive is regenerated; the previous manifest
is written beside the new one as `<...>.manifest.json.<prev-hash>.bak`
so no prior ledger entry is destroyed.

### Compression

zstd, default level. Rationale: ~10:1 ratio on JSONL (observed in
vv), streaming decompression, ubiquitous tooling, well-understood
long-term stability. We deliberately avoid chained or custom
compression — a future forensic examiner must be able to unpack the
archive with stock tools.

### Signing (Phase 6, forward reference)

Detached signature over the manifest (not the compressed transcript —
the manifest already pins the transcript via `source_sha256`). Stored
as `<...>.manifest.json.sig`. Supports gpg and ssh-sig. Without this,
manifest tampering is undetectable; with it, the ledger becomes
attributable rather than merely timestamped.

### Git-notes anchoring (Phase 7, forward reference)

On each archive, `git notes add -m "archive: <manifest-sha256>"` on
the current HEAD. Binds the ledger to the commit graph — forging
provenance requires rewriting history, which is detectable.

## Consequences

**Positive:**

- Evidentiary record of human↔AI turns, hashed pre-compression for
  durability.
- IDE-agnostic format; Claude Code and Zed adapters (and future ones)
  produce identical manifest shapes.
- Bidirectional links between session markdown and archives make
  human auditing straightforward.
- Schema-versioned for forward compatibility.

**Negative / trade-offs:**

- Storage: full raw transcripts are large. Mitigated by zstd (~10:1)
  and a future `vp archive prune` subcommand. For the copyright use
  case, retention is the point — pruning is user-initiated.
- The archive is only as trustworthy as the hook chain. A hostile
  local environment could suppress SessionEnd. Out of scope for this
  ADR; addressed partially by Phases 6–7.
- Adding adapters for new IDEs is ongoing work. The format is stable
  precisely so that adapter churn doesn't invalidate old archives.

## Alternatives considered

- **Compress the manifest + transcript as a single tarball.** Rejected:
  forensic tooling expects atomic files; having the manifest directly
  readable (JSON on disk) is more useful than requiring a tar extract
  before any query.
- **Hash the compressed bytes instead of the source.** Rejected: zstd
  frame headers vary across versions; a re-compression would break
  verification even when the underlying transcript is unchanged.
- **Store only a pointer into the IDE's own log directory.** Rejected:
  IDE log directories are ephemeral, host-specific, and not under
  version control; the whole point is to capture the record into the
  user's vault.
- **Sign the compressed bytes instead of the manifest.** Rejected:
  signing the manifest (which pins the source hash) is equivalent in
  security and lets us re-compress without re-signing.

## References

- *Thaler v. Perlmutter*, SCOTUS cert. denied March 2026:
  https://www.mayerbrown.com/en/insights/publications/2026/03/supreme-court-denies-review-in-ai-authorship-case
- Vibe-vault archive format: `vv archive` command (prior art).
- Task plan: `<vault>/Projects/vibe-palace/tasks/transcript-archive-ledger.md`
  (reached via the MCP task tools; the `agentctx/` segment named in the
  original draft of this ADR no longer exists in the vibe-palace layout)
