// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// noteOnlyProjectFixture builds a vault holding one project whose entire
// history is SESSION NOTES: no palace store, no iterations.md, no transcript
// archives. A notes-only project is NOT truly empty, so vp_refresh_index starts
// a rebuild for it. With an empty notes map it is truly empty, and the tool
// refuses.
func noteOnlyProjectFixture(t *testing.T, project string, notes map[string]string) (*storage.Vault, *search.Engine) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())

	dir := filepath.Join(vault.Root, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for stem, body := range notes {
		content := strings.Join([]string{
			"---",
			"session_id: " + stem,
			"project: " + project,
			"date: 2026-08-12",
			"title: wrap",
			"---",
			body,
		}, "\n")
		if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { eng.Close() })
	return vault, eng
}

// TestRefreshIndexNothingToRefreshStaysReachable (7-S2): a truly empty project
// is refused with zero launches; a notes-only project (indexable content, no
// store and no archive) is started with one launch.
func TestRefreshIndexNothingToRefreshStaysReachable(t *testing.T) {
	t.Run("truly empty refuses, 0 launches", func(t *testing.T) {
		const project = "truly-empty"
		vault, eng := noteOnlyProjectFixture(t, project, nil)
		rec := &recLauncher{}
		tool := RefreshIndexTool(eng, vault, rec.launch)
		params, _ := json.Marshal(refreshIndexParams{Project: project})
		_, err := tool.Handler(context.Background(), params)
		if err == nil {
			t.Fatal("handler did not refuse a truly empty project")
		}
		if !strings.Contains(err.Error(), "nothing to refresh") {
			t.Errorf("refusal %q is not the nothing-to-refresh refusal", err)
		}
		if rec.count() != 0 {
			t.Fatalf("a refused project launched %d rebuilds, want 0", rec.count())
		}
	})

	t.Run("notes-only starts, 1 launch", func(t *testing.T) {
		const project = "notes-only"
		vault, eng := noteOnlyProjectFixture(t, project, map[string]string{
			"2026-08-12-1111aaaa-01": "We discussed the ledger design and the chunk store in detail.",
		})
		rec := &recLauncher{}
		tool := RefreshIndexTool(eng, vault, rec.launch)
		params, _ := json.Marshal(refreshIndexParams{Project: project})
		res, err := tool.Handler(context.Background(), params)
		if err != nil {
			t.Fatalf("handler refused a notes-only project: %v", err)
		}
		if m, ok := res.(map[string]any); !ok || m["started"] != true {
			t.Fatalf("result = %v, want {started:true}", res)
		}
		if rec.count() != 1 {
			t.Fatalf("a notes-only project launched %d rebuilds, want 1", rec.count())
		}
	})
}
