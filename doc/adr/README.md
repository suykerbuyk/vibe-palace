# Architecture Decision Records

Each ADR records one decision and the reasoning behind it, as it stood when it was accepted.
ADRs are not edited to match later code. Corrections are added as dated amendments or in-place
markers, and a superseded ADR says what superseded it on its status line. For how the system
works today, see [doc/ARCHITECTURE.md](../ARCHITECTURE.md); the code is authoritative.

| ADR | Title | Status |
|---|---|---|
| [001](001-transcript-archive.md) | Transcript Archive Format | Accepted 2026-04-15; extended by ADR-007; amended 2026-09-28 |
| [002](002-wrap-state-anchors.md) | Wrap-State Anchors | Accepted 2026-06-06; amended 2026-08-29 |
| [003](003-vault-write-locking.md) | Vault Write Locking | Accepted 2026-06-07; six amendments 2026-07-11 to 2026-09-27; corrections 2026-09-28 |
| [004](004-mcp-native-memory.md) | MCP-Native Memory | Accepted 2026-06-19 |
| [005](005-llm-enrichment-synthesis.md) | LLM-Enriched Session Synthesis | Accepted 2026-06-21; template claims amended 2026-09-28 |
| [006](006-derive-dont-ask.md) | Derive, Don't Ask | Accepted 2026-07-14; amended 2026-09-28 (the `vp:pin` specimen is historical) |
| [007](007-vault-audit-and-archive-backfill.md) | The Vault Audit and the Archive Backfill | Accepted 2026-07-17; amended 2026-09-28 |
| [008](008-instruction-manual-lives-in-the-binary.md) | The Agent Instruction-Manual Lives in the Binary, Not in Vault Files | Accepted 2026-07-21; implemented in full (Phases 1–4); amended 2026-09-11, 2026-09-14, 2026-09-28 |
| [009](009-inviolable-core-delivered-whole-or-fail-loud.md) | Deliver the Inviolable Core Whole, or Fail Loud — Never Silently Truncate Operating Instructions or Active State | **Superseded in full** by [PRD §1.10–§1.11](../PRD-vibe-palace.md) (2026-08-18); historical record |
| [010](010-surface-gate-at-the-dispatch-seam.md) | The Surface Gate Stays at the Dispatch Seam; the Mutating Predicate Becomes Derived | Accepted 2026-08-20; implemented; amended 2026-08-20, 2026-08-22, 2026-09-28 |
| [011](011-open-task-header-schema-and-format-axis.md) | Open Task-Header Schema and the RequiredDataFormat/Release-Versioning Coupling | Accepted 2026-09-15; implemented; amended 2026-09-28 |
| [012](012-vault-resolution-precedence-and-host-project-bindings.md) | Vault Resolution Precedence and Host-Local Project Bindings | Accepted 2026-09-27; amended 2026-09-28 (rule 7 corrected by ADR-013) |
| [013](013-vault-project-lifecycle-and-departure-records.md) | Vault Project Lifecycle Commands and Departure Records | Accepted 2026-09-28 |
| [014](014-search-index-compiled-per-host-from-vault-artifacts.md) | The Vault Holds Authored Artifacts; Each Host Compiles Its Search Index from Them | Accepted 2026-10-02 |

The Status column summarises each ADR's own **Status:** line; the ADR's line is authoritative.
To list the status lines: `grep -n '^\*\*Status:\*\*' doc/adr/0*.md`.
