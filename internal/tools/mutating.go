// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// MutatingToolNames is the canonical, single-source list of MCP tools that
// write to the vault (or project-root state) and therefore must be surface-
// gated: the dispatch choke-point refuses them when the vault's MCP surface
// version exceeds this binary's (see internal/mcp Registry.gateIfMutating).
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
	"vp_stamp_iter",
	"vp_kg_add",
	"vp_kg_invalidate",
	"vp_init",
	"vp_vault_sync",
	"vp_vault_tidy",
}
