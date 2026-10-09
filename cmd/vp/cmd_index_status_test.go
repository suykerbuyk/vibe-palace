// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestCoverageFailureLimitMatchesIngest guards the coverage path's local copy
// of the ingester's failure limit against ingest.DefaultFailureLimit. The
// search package cannot import ingest (ingest imports search), so this test —
// in a package that imports both — is where the two are pinned together.
func TestCoverageFailureLimitMatchesIngest(t *testing.T) {
	if search.CoverageFailureLimit() != ingest.DefaultFailureLimit {
		t.Fatalf("coverage failure limit = %d, ingest.DefaultFailureLimit = %d — they must match, "+
			"or coverage counts the wrong archives as failing",
			search.CoverageFailureLimit(), ingest.DefaultFailureLimit)
	}
}

// TestCLIAndMCPAgree (SF3): the CLI `vp index status --json` struct and the
// vp_index_status MCP tool result are the SAME search.Coverage on one fixture,
// because both derive it over the shared no-embedder engine (one derivation).
func TestCLIAndMCPAgree(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	const proj = "agree-proj"
	testutil.InitProject(t, vault.Root, proj)
	// A session note makes the project non-empty (unbuilt coverage).
	sdir, err := vault.SessionDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "2026-05-13-aaaa0000-01.md"), []byte("# note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := vault.LoadConfig(proj)
	if err != nil {
		t.Fatal(err)
	}

	// CLI path.
	cliCov, err := indexCoverage(vault, cfg, proj)
	if err != nil {
		t.Fatal(err)
	}

	// MCP path.
	res, err := tools.IndexStatusTool(vault).Handler(context.Background(), json.RawMessage(`{"project":"`+proj+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	cliJSON, _ := json.Marshal(cliCov)
	mcpJSON, _ := json.Marshal(res)
	if string(cliJSON) != string(mcpJSON) {
		t.Fatalf("CLI and MCP disagree:\n CLI: %s\n MCP: %s", cliJSON, mcpJSON)
	}
}
