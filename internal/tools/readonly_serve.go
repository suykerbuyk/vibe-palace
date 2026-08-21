// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import "sort"

// ReadOnlyServeToolNames is the affirmative allow-list of tools that may be
// exposed on a read-only `vp mcp serve`. It answers ONE question — *is this
// tool safe to serve to a client that must not be able to change anything?* —
// and it is the ONLY input to that decision.
//
// # Why this is a separate declaration from MutatingToolNames
//
// Until this split, one boolean answered two different questions: the surface
// gate's *"must a stale binary refuse this?"* and this one. They agree today —
// TestReadOnlyServeAgreesWithSurfaceGateToday pins that — but they are not the
// same question and they are already known to diverge under the work ADR-010
// sets up: a derived mutating predicate computes FALSE for vp_vault_sync and
// vp_vault_tidy, because those reach no vault-write sink (they act through
// git). Both of them commit and push vault history, and neither may ever be
// served read-only. One flag cannot hold both answers, and the moment it tried
// the security answer would have been the one that silently changed.
//
// # 🔴 The asymmetry, which is the whole point — DO NOT MERGE THESE BACK
//
// The two predicates have OPPOSITE failure modes, so they must fail in opposite
// directions:
//
//   - A false negative on the SURFACE GATE is an ungated write. Bad, bounded,
//     and detectable after the fact: the vault carries stamps, and the audit
//     ratchet finds the damage.
//   - A false negative HERE is a write tool published on a bearer-authed HTTP
//     surface an operator believes is read-only. That is a SECURITY failure and
//     it is NOT detectable after the fact — nothing records the exposure, only
//     its consequences.
//
// So this list is an ALLOW-LIST and the filter is FAIL-CLOSED: a tool is served
// only if it is named here, and anything unrecognised — a newly registered
// tool, a renamed one, a misclassified one — is stripped. The surface gate can
// afford a deny-list keyed on a flag. This cannot.
//
// A "strip everything flagged mutating" filter, which is what this replaced,
// fails OPEN: a new tool that nobody flagged is served. That is the wrong
// direction for this question, however right it is for the other one.
//
// # Known limit, stated rather than hidden
//
// Fail-closed protects against tools nobody classified. It does NOT protect
// against a tool affirmatively misclassified INTO this list. There is one live
// example: vp_refresh_index is registered non-mutating while Rebuild writes
// .vec files, so it appears here and is served read-only today. That is the
// open defect `refresh-index-reports-rebuilt-while-writing-nothing`; it is not
// created by this split and is deliberately not fixed here, because changing
// the flag moves internal/mcp/tool_surface.golden.json and that is a surface
// change, not a refactor.
var ReadOnlyServeToolNames = []string{
	"vp_bootstrap_context",
	"vp_check",
	"vp_cmd",
	"vp_collect_wrap_state",
	"vp_find_tunnels",
	"vp_get_command",
	"vp_get_doctrine",
	"vp_get_effectiveness",
	"vp_get_friction_trends",
	"vp_get_iteration",
	"vp_get_knowledge",
	"vp_get_learning",
	"vp_get_project_context",
	"vp_get_resume",
	"vp_get_session_detail",
	"vp_get_skill",
	"vp_get_skill_section",
	"vp_get_task",
	"vp_get_workflow",
	"vp_health",
	"vp_kg_query",
	"vp_kg_stats",
	"vp_kg_timeline",
	"vp_list_commands",
	"vp_list_learnings",
	"vp_list_projects",
	"vp_list_rooms",
	"vp_list_skills",
	"vp_list_tasks",
	"vp_list_wings",
	"vp_manual",
	"vp_memory_list",
	"vp_memory_read",
	"vp_palace_status",
	"vp_preflight_wrap",
	"vp_read_resource",
	// See the "Known limit" note above: this one is here because it is
	// registered non-mutating, not because it has been shown to be read-only.
	"vp_refresh_index",
	"vp_scan_plans",
	"vp_search",
	"vp_search_cross_project",
	"vp_search_sessions",
	"vp_skill",
	"vp_surface_check",
	"vp_traverse",
	"vp_vault_exists",
	"vp_vault_list",
	"vp_vault_read",
	"vp_vault_sha256",
	"vp_vault_status",
}

// ToolsToStripForReadOnlyServe returns, sorted, every name in registered that is
// NOT affirmatively allow-listed in ReadOnlyServeToolNames.
//
// This is the fail-closed half made executable: the caller passes what the
// registry actually holds, and everything unrecognised comes back to be
// deleted. It deliberately does not consult MutatingToolNames, the Mutating
// flag, or any property of the tool — an unclassified tool is stripped BECAUSE
// it is unclassified, which is the only rule that stays safe when someone adds
// a tool and forgets everything else.
func ToolsToStripForReadOnlyServe(registered []string) []string {
	allowed := make(map[string]bool, len(ReadOnlyServeToolNames))
	for _, n := range ReadOnlyServeToolNames {
		allowed[n] = true
	}

	var strip []string
	for _, n := range registered {
		if !allowed[n] {
			strip = append(strip, n)
		}
	}
	sort.Strings(strip)
	return strip
}
