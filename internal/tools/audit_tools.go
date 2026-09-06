// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
		"date": {"type": "string", "description": "IGNORED. Accepted for wire compatibility and never used. The WRITER stamps the report's calendar day from its own clock, in the vault's timezone (UTC until that config exists) — a client-supplied date is a second clock, and a client does not choose the calendar day of a transaction. Supplying this changes nothing; omit it."}
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
		// The dimension list is DERIVED from vaultaudit's registry at construction
		// time, never typed out here. It used to name five dimensions against a
		// registry of ten, and it was wrong from the moment the sixth landed —
		// nothing could have caught that, because the surface golden covers a tool's
		// name, mutating flag and schema digest and NOT its Description. That gap is
		// real and stays open in general; for THIS tool it is closed by construction,
		// because there is no longer a second copy of the list to disagree with the
		// first.
		Description: fmt.Sprintf(
			"Audit the WHOLE VAULT against design intent, reporting pass/fail/unknown per dimension "+
				"against the accepted-debt baseline (Audits/baseline.json). Dimensions: %s. Takes NO "+
				"project parameter: it is vault-global by design. ADVISORY: it reports, it never "+
				"blocks. `unknown` means the auditor could not read something and is NOT a pass.",
			strings.Join(vaultaudit.DimensionNames(), ", ")),
		Schema:   auditVaultSchema,
		Mutating: true, // write=true writes a report and stamps the surface
		// An explicit write:false renders inline and persists nothing — see
		// auditVaultReadOnly, including why an OMITTED write still gates.
		ReadOnlyWhen: auditVaultReadOnly,
		Handler:      auditVaultHandler(vault),
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

		// 🔴 THE WRITER STAMPS THE DAY. p.Date is parsed off the wire and then
		// deliberately DISCARDED — see storage.Vault.CalendarDay for why a client clock
		// is not an authority, and TestAuditVault_ClientDateDoesNotWin for the pin.
		// Read once, here, so the filename and the date rendered inside the file cannot
		// disagree.
		day := vault.CalendarDay(time.Now())

		if !p.Write {
			// The inline render carries the same writer day as a written one. It is
			// never persisted, so it cannot touch the staleness anchor: CheckStaleness
			// globs Audits/*-vault-audit.md off disk, and an inline verdict never lands
			// there.
			out["report"] = report.Render(day, vault.Root)
			return out, nil
		}

		rel := vaultaudit.ReportRelPath(day)
		abs := filepath.Join(vault.Root, filepath.FromSlash(rel))
		// atomicfile.Write with the REAL vault root, so the surface stamp fires. Pass
		// anything else and the write silently skips the stamp — which is how the vault
		// loses its version floor (see private-atomicwrite-copies-skip-surface-stamp).
		if err := atomicfile.Write(vault.Root, abs, []byte(report.Render(day, vault.Root))); err != nil {
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
