// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_archive_repair_manifest
// ---------------------------------------------------------------------------

// The MCP half of `vp archive repair-manifest`. Both are thin: every refusal
// (the adapter and captured_at the operator must supply, the pair grace, the
// manifest lock, an existing manifest, the write funnel) lives in
// archive.RepairManifest, so a guard only an agent can trip is not a guard
// (PRD-vibe-palace.md section 6.8; doc/TESTING.md). Like `vp archive link`, one
// apply call repairs ONE archive and IS the human approval; there is no bulk mode.
var archiveRepairManifestSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug the orphaned archive lives under (Projects/<project>/transcripts)."},
		"archive": {"type": "string", "description": "The orphaned archive: a YYYY-MM-DD-<session id> name as listed by dry_run, or its .jsonl.zst path. Required to repair; omit when listing."},
		"adapter": {"type": "string", "description": "REQUIRED to repair: the adapter that captured the archive (claude-code, codex-raw, zed or inline). Never guessed: claude-code and inline archives are the same shape and differ in authenticity."},
		"captured_at": {"type": "string", "description": "REQUIRED to repair: the capture time you vouch for, RFC3339 (stored in UTC). The listing's file mtime and filename day are hints only; mtime is never used."},
		"dry_run": {"type": "boolean", "description": "true lists the orphaned archives (read-only) with their age, mtime and filename day as hints; false or omitted repairs ONE archive."}
	},
	"required": ["project"]
}`)

type archiveRepairParams struct {
	Project    string `json:"project"`
	Archive    string `json:"archive"`
	Adapter    string `json:"adapter"`
	CapturedAt string `json:"captured_at"`
	DryRun     bool   `json:"dry_run"`
}

// archiveRepairGateParams is archiveRepairParams with the discriminator as a
// pointer, so the surface gate can tell an omitted dry_run from an explicit true:
// only an explicit dry_run:true is admitted as read-only, never an inference from
// a field nobody sent (see auditVaultReadOnly for the same rule).
type archiveRepairGateParams struct {
	DryRun *bool `json:"dry_run"`
}

var archiveRepairReadOnly = readOnlyIf(func(p archiveRepairGateParams) bool {
	return p.DryRun != nil && *p.DryRun
})

// ArchiveRepairManifestTool writes the missing manifest for one orphaned
// transcript archive, beside the existing archive, without re-dating it.
func ArchiveRepairManifestTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_archive_repair_manifest",
		Description: "DO NOT USE until every host, hooks included, is on v11 or later: an older hook drops the manifest's " +
			"`regenerated` marker when it next links the manifest, and the marker is the only record that the manifest is not " +
			"an original. Write the missing manifest for ONE orphaned transcript archive (a .jsonl.zst whose capture died before " +
			"its .manifest.json was written), beside the EXISTING archive: the archive is never rewritten and keeps its name and " +
			"date, so the next tidy sweeps both halves. dry_run:true lists orphans read-only (age, file mtime and filename day as " +
			"hints). To repair you must supply `adapter` and `captured_at` (never guessed or taken from the file). The manifest " +
			"records what was derived from the bytes, what came from the filename and path, what you supplied, and what is " +
			"unrecoverable (git_head, captured_by_hostname, vp_version, the session-note link). It refuses an archive younger " +
			"than the pair grace, never overwrites an existing manifest, and takes the capture writer's manifest lock. Calling " +
			"this IS the human approval for that archive; no bulk mode exists.",
		Schema:       archiveRepairManifestSchema,
		Mutating:     true, // writes a transcript manifest (and its signature when configured)
		ReadOnlyWhen: archiveRepairReadOnly,
		Handler:      archiveRepairManifestHandler(vault),
	}
}

func archiveRepairManifestHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		vault := vaultFromCtx(ctx, vault)
		var p archiveRepairParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.DryRun {
			orphans, err := archive.ListOrphans(vault.Root, p.Project, time.Now())
			if err != nil {
				return nil, fmt.Errorf("archive repair: %w", err)
			}
			return map[string]any{"dry_run": true, "orphans": orphans}, nil
		}
		if p.Archive == "" {
			return nil, fmt.Errorf("archive is required to repair (list the orphans with dry_run:true)")
		}
		signOpts, err := archive.SignOptionsForProject(vault.Root, p.Project)
		if err != nil {
			return nil, err
		}
		res, err := archive.RepairManifest(archive.RepairOptions{
			Context:    ctx,
			VaultRoot:  vault.Root,
			Project:    p.Project,
			Archive:    p.Archive,
			Adapter:    p.Adapter,
			CapturedAt: p.CapturedAt,
			VPVersion:  check.BuildVersion,
			Sign:       signOpts,
		})
		if err != nil {
			return nil, fmt.Errorf("archive repair: %w", err)
		}
		return res, nil
	}
}
