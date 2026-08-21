// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// MutatingToolNames is the canonical, single-source list of MCP tools that
// write to the vault (or project-root state) and therefore must be surface-
// gated: the dispatch choke-point refuses them when the vault's MCP surface
// version exceeds this binary's (see internal/mcp Registry.gateIfMutating).
//
// 🔴 THIS LIST ANSWERS EXACTLY ONE QUESTION: *must a stale binary refuse this
// tool?* It is the surface gate's declaration and nothing else's.
//
// It used to answer a second one — *may this tool be served on a read-only
// `vp mcp serve`?* — because `vp mcp serve` filtered on it. That question now
// has its own declaration, ReadOnlyServeToolNames in readonly_serve.go, and
// the two are deliberately independent. Read the asymmetry note there before
// changing either: the two predicates have OPPOSITE failure modes, this one
// tolerates a deny-list keyed on a flag and that one does not, and they are
// already known to diverge under the derivation work ADR-010 sets up.
//
// DO NOT re-point the serve filter at this list to "remove the duplication".
// The duplication is the safety property.
//
// This list is the CLI/MCP analogue of the storage stamp-enumeration test: it
// exists so the gate set is declared in ONE auditable place and pinned by a
// completeness test (TestMutatingToolNamesMatchRegistry) that fails the build
// if a registered tool's Mutating flag drifts from this list. A new mutating
// tool that forgets either the constructor flag or an entry here breaks the
// test — the mechanical safeguard the hand-maintained approach lacked.
//
// Note the gate set is a SUPERSET of the stamp set: vp_vault_delete and
// vp_vault_move are listed here (they are destructive mutations a stale binary
// must refuse) even though they do not write content and so do not stamp.
var MutatingToolNames = []string{
	"vp_capture_session",
	// vp_audit_vault writes a report into Audits/ (and stamps the surface) when
	// write=true. It is ADVISORY — it never blocks — but it is still a WRITER, and a
	// stale binary must refuse it like any other.
	"vp_audit_vault",
	// vp_archive_link rewrites session notes and a transcript manifest (the Phase-4
	// backfill applier). One call = one human-approved pair.
	"vp_archive_link",
	"vp_manage_task",
	"vp_update_resume",
	"vp_append_iteration",
	"vp_vault_write",
	"vp_vault_edit",
	"vp_vault_delete",
	"vp_vault_move",
	"vp_memory_write",
	"vp_memory_delete",
	"vp_memory_harvest",
	"vp_ingest_commit_msg",
	// vp_archive_commit_log appends landed commit messages to the vault
	// commit-log.md permanent history and advances the last-archived anchor.
	"vp_archive_commit_log",
	"vp_stamp_iter",
	"vp_kg_add",
	"vp_kg_invalidate",
	"vp_init",
	"vp_vault_sync",
	"vp_vault_tidy",
}
