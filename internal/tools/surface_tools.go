// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Surface-compatibility tool (vp_surface_check). This is a read-only probe that
// exposes the SAME surface verdict a mutating vault write would face — binary
// MCP surface vs. the max .surface stamp recorded in the vault — including the
// curated upgrade/override remediation text. It lets command templates run a
// surface preflight host-agnostically (Claude/Grok/Zed) instead of shelling out
// to `vp check --json --check surface`.
//
// All verdict logic lives in internal/check (CheckSurface) and internal/surface
// (CheckCompatible); this layer only resolves the vault root and marshals the
// check.Result into a clean, self-describing JSON struct.

package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// SurfaceCheckResult is the JSON output of vp_surface_check. It is a minimal,
// self-describing projection of check.Result: status is the lower-cased verdict
// (pass/fail/info), details carries the verbatim remediation on fail, and the
// two surface integers make the comparison the verdict rests on explicit.
type SurfaceCheckResult struct {
	Status        string   `json:"status"`                  // "pass" | "fail" | "info"
	Summary       string   `json:"summary"`                 // one-line verdict
	Details       []string `json:"details,omitempty"`       // remediation lines (populated on fail)
	BinarySurface int      `json:"binary_surface"`          // this binary's MCPSurfaceVersion
	VaultSurface  int      `json:"vault_surface,omitempty"` // worst vault stamp (fail only)
	StampDir      string   `json:"stamp_dir,omitempty"`     // dir of the worst stamp (fail only)
}

var surfaceCheckSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. Accepted for parity with other project-scoped read tools; the surface scan always covers the whole vault (the same scope a mutating write is gated against), so this value does not narrow the verdict."}
	}
}`)

// surfaceStatusString maps a check.Status to the lower-cased verdict string used
// in the tool's JSON output. CheckSurface only ever returns Pass/Fail/Info.
func surfaceStatusString(s check.Status) string {
	switch s {
	case check.Fail:
		return "fail"
	case check.Info:
		return "info"
	default:
		return "pass"
	}
}

// SurfaceCheckTool exposes the vault surface-compatibility verdict as a
// non-mutating MCP tool.
func SurfaceCheckTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_surface_check",
		Description: "Read-only surface preflight: report whether this binary's MCP " +
			"surface version is compatible with the vault (binary >= max .surface " +
			"stamp). Returns the SAME verdict a mutating vault write would face, " +
			"including the curated remediation. Returns {status (pass|fail|info), " +
			"summary, details[] (upgrade/override lines on fail), binary_surface, " +
			"vault_surface, stamp_dir}. status=\"fail\" means the vault was written " +
			"by a newer binary; upgrade before writing.",
		Schema: surfaceCheckSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}

			// The surface gate is whole-vault: CheckSurface scans every stamp
			// target under the vault root, mirroring the scope a mutating write
			// is gated against. The project arg does not narrow it.
			r := check.CheckSurface(vault.Root)

			out := SurfaceCheckResult{
				Status:        surfaceStatusString(r.Status),
				Summary:       r.Summary,
				Details:       r.Details,
				BinarySurface: surface.MCPSurfaceVersion,
			}

			var ie *surface.IncompatibleError
			if errors.As(r.Err, &ie) {
				out.VaultSurface = ie.VaultSurface
				out.StampDir = ie.StampDir
			}

			return out, nil
		},
	}
}
