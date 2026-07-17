// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// ---------------------------------------------------------------------------
// vp_archive_link
// ---------------------------------------------------------------------------

// The MCP half of the Phase-4 backfill applier (capture-note-archive-link-
// never-closes). The REPORT side needs no tool of its own: the recoverable
// pairs ride in vp_audit_vault's findings, annotated with this tool's exact
// invocation. Like the CLI sibling `vp archive link`, one call applies ONE
// pair — invoking it is the human approval for that pair, and there is
// deliberately no bulk mode.
var archiveLinkSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug the session's notes and transcripts live under."},
		"session_id": {"type": "string", "description": "Harness session id of the recoverable pair, as listed by the vault audit or vp archive backfill."}
	},
	"required": ["project", "session_id"]
}`)

type archiveLinkParams struct {
	Project   string `json:"project"`
	SessionID string `json:"session_id"`
}

// ArchiveLinkTool backfills the note<->transcript link for one recoverable
// session: it stamps archive_session_id (provenance: backfilled) onto the
// session's caller-keyed notes, points their archive: field at the newest
// STRANDED manifest, and back-links that manifest to the canonical note.
func ArchiveLinkTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_archive_link",
		Description: "Backfill the note<->transcript link for ONE recoverable session (a stranded manifest " +
			"whose session id matches a note's caller-pushed session_key). Stamps the note(s) with provenance " +
			"'backfilled' and back-links the newest stranded manifest to the canonical note. Calling this IS " +
			"the human approval for the pair — apply only pairs the operator has reviewed; no bulk mode exists.",
		Schema:   archiveLinkSchema,
		Mutating: true, // rewrites session notes and a manifest
		Handler:  archiveLinkHandler(vault),
	}
}

func archiveLinkHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, raw json.RawMessage) (any, error) {
		var p archiveLinkParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.SessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}

		res, err := vaultaudit.ApplyBackfill(vault, p.Project, p.SessionID)
		if err != nil {
			return nil, fmt.Errorf("archive link: %w", err)
		}
		return res, nil
	}
}
