// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// ---------------------------------------------------------------------------
// vp_audit_vault
// ---------------------------------------------------------------------------

// 🔴 THIS TOOL TAKES NO `project` PARAMETER, AND THAT IS THE DESIGN.
//
// The thing being audited is the VAULT. Scoping it to whichever project the caller
// happened to invoke from is exactly how a project nobody has opened in three months
// escapes scrutiny forever — the failure the vault-global decision was written to
// prevent. And a required parameter that does nothing is theatre: that was defect #4
// of vp_health, deleted at 201.
//
// Its CLI sibling `vp audit rooms` IS per-project and takes --project. That asymmetry
// is correct and deliberate: rooms audits a project's classification; vault audits the
// vault.
var auditVaultSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"write": {"type": "boolean", "description": "Write the report to Audits/<date>-vault-audit.md (vault tidy sweeps it). Default false: the report is returned inline."},
		"date": {"type": "string", "description": "Report date, YYYY-MM-DD. The SERVER has no business inventing this from its own clock when the agent knows the session date; required when write is true."}
	},
	"required": []
}`)

type auditVaultParams struct {
	Write bool   `json:"write"`
	Date  string `json:"date"`
}

// AuditVaultTool is the MCP half of the vault audit. The parent task's settled
// decision #3 requires BOTH surfaces — CLI and MCP — because an audit that only one
// host can run is an audit that reaches one host.
func AuditVaultTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_audit_vault",
		Description: "Audit the WHOLE VAULT against design intent — transcript round-trips, project-tree " +
			"coherence, KG portability, resume discipline, iteration headings — reporting pass/fail/unknown " +
			"per dimension against the accepted-debt baseline (Audits/baseline.json). Takes NO project " +
			"parameter: it is vault-global by design. ADVISORY: it reports, it never blocks. `unknown` " +
			"means the auditor could not read something and is NOT a pass.",
		Schema:   auditVaultSchema,
		Mutating: true, // write=true writes a report and stamps the surface
		Handler:  auditVaultHandler(vault),
	}
}

func auditVaultHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, raw json.RawMessage) (any, error) {
		var p auditVaultParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
		}

		report, err := vaultaudit.Run(vault)
		if err != nil {
			return nil, fmt.Errorf("audit vault: %w", err)
		}

		out := map[string]any{
			"failed":     report.Failed(),
			"dimensions": summarize(report),
		}

		if !p.Write {
			out["report"] = report.Render(p.Date, vault.Root)
			return out, nil
		}

		// The date is REQUIRED to write, and the server does not fall back to its own
		// clock. A report filename and the date stamped inside it must agree, and a
		// server that guesses can disagree with the agent that asked — across midnight,
		// or across a timezone. Ask, do not invent.
		if p.Date == "" {
			return nil, fmt.Errorf("date is required when write is true (YYYY-MM-DD): the server will " +
				"not invent a date the caller already knows")
		}

		rel := vaultaudit.ReportRelPath(p.Date)
		abs := filepath.Join(vault.Root, filepath.FromSlash(rel))
		// atomicfile.Write with the REAL vault root, so the surface stamp fires. Pass
		// anything else and the write silently skips the stamp — which is how the vault
		// loses its version floor (see private-atomicwrite-copies-skip-surface-stamp).
		if err := atomicfile.Write(vault.Root, abs, []byte(report.Render(p.Date, vault.Root))); err != nil {
			return nil, fmt.Errorf("write report: %w", err)
		}
		out["report_path"] = rel
		return out, nil
	}
}

// summarize renders the per-dimension verdict for the tool result.
//
// `unknown` is carried explicitly and separately from `pass`. An audit that cannot see
// something and says so quietly is the vp_health bug in a new costume — and this is the
// payload an AGENT reads, where a soft signal is skimmed fastest.
func summarize(r vaultaudit.Report) []map[string]any {
	out := make([]map[string]any, 0, len(r.Dimensions))
	for _, d := range r.Dimensions {
		row := map[string]any{
			"name":     d.Name,
			"status":   string(d.Status),
			"new":      len(d.New),
			"stale":    len(d.Stale),
			"accepted": d.Accepted,
			"evidence": d.Evidence,
		}
		if len(d.Unknowns) > 0 {
			row["unknown"] = d.Unknowns
		}
		out = append(out, row)
	}
	return out
}
