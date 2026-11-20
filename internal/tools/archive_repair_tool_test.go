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
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

func repairToolOrphan(t *testing.T) (*storage.Vault, string, string) {
	t.Helper()
	root := t.TempDir()
	testutil.InitProject(t, root, "p")
	res, err := archive.Create(archive.CreateOptions{
		Adapter: archive.InlineAdapterName, SessionID: "sess-tool-repair",
		SourceContent: []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"),
		VaultRoot:     root, ProjectSlug: "p", Now: time.Now().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(res.ManifestPath); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(res.ArchivePath, old, old); err != nil {
		t.Fatal(err)
	}
	return storage.NewVault(root), strings.TrimSuffix(filepath.Base(res.ArchivePath), ".jsonl.zst"), res.ManifestPath
}

func callRepairTool(t *testing.T, v *storage.Vault, params map[string]any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	return ArchiveRepairManifestTool(v).Handler(context.Background(), raw)
}

// dry_run lists, read-only; apply rejects an omitted captured_at or adapter and
// writes nothing; apply with both writes the manifest; a repeat is a no-op.
func TestArchiveRepairManifestToolListsAndRepairs(t *testing.T) {
	v, name, manifest := repairToolOrphan(t)

	out, err := callRepairTool(t, v, map[string]any{"project": "p", "dry_run": true})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), name) || !strings.Contains(string(b), `"repairable":true`) {
		t.Errorf("dry_run did not list the orphan: %s", b)
	}
	if _, err := os.Stat(manifest); err == nil {
		t.Fatal("dry_run wrote a manifest")
	}

	if _, err := callRepairTool(t, v, map[string]any{"project": "p", "archive": name, "adapter": "claude-code"}); err == nil ||
		!strings.Contains(err.Error(), "--captured-at is required") {
		t.Errorf("an omitted captured_at was not rejected: %v", err)
	}
	if _, err := callRepairTool(t, v, map[string]any{"project": "p", "archive": name, "captured_at": "2026-11-01T12:00:00Z"}); err == nil ||
		!strings.Contains(err.Error(), "--adapter is required") {
		t.Errorf("an omitted adapter was not rejected: %v", err)
	}
	if _, err := callRepairTool(t, v, map[string]any{"project": "p", "adapter": "claude-code", "captured_at": "2026-11-01T12:00:00Z"}); err == nil {
		t.Error("an apply with no archive was accepted")
	}
	if _, err := os.Stat(manifest); err == nil {
		t.Fatal("a manifest was written without the operator's claims")
	}

	out, err = callRepairTool(t, v, map[string]any{"project": "p", "archive": name, "adapter": "claude-code", "captured_at": "2026-11-01T05:31:54-07:00"})
	if err != nil {
		t.Fatal(err)
	}
	if res, ok := out.(*archive.RepairResult); !ok || res.Manifest == nil || res.Manifest.CapturedAt != "2026-11-01T12:31:54Z" {
		t.Fatalf("result = %#v (an offset input must be stored in UTC)", out)
	}
	out, err = callRepairTool(t, v, map[string]any{"project": "p", "archive": name, "adapter": "claude-code", "captured_at": "2026-11-01T12:31:54Z"})
	if err != nil || out.(*archive.RepairResult).NothingToDo == "" {
		t.Errorf("a repeat = %#v, %v; want nothing to do", out, err)
	}
}

// Only an EXPLICIT dry_run:true is read-only to the surface gate; an omitted or
// false dry_run, or an apply, is a writer (never inferred from a field nobody sent).
func TestArchiveRepairManifestReadOnlyPredicateAdmitsOnlyAnExplicitDryRun(t *testing.T) {
	tool := ArchiveRepairManifestTool(storage.NewVault(t.TempDir()))
	for params, want := range map[string]bool{
		`{"project":"p","dry_run":true}`:  true,
		`{"project":"p"}`:                 false,
		`{"project":"p","dry_run":false}`: false,
		`{"project":"p","archive":"x","adapter":"claude-code","captured_at":"2026-01-01T00:00:00Z"}`: false,
		`not json`: false,
	} {
		if got := tool.ReadOnlyWhen(json.RawMessage(params)); got != want {
			t.Errorf("ReadOnlyWhen(%s) = %v, want %v", params, got, want)
		}
	}
	if !tool.Mutating {
		t.Error("the tool is not declared Mutating")
	}
	if !strings.Contains(tool.Description, "DO NOT USE until every host, hooks included, is on v11 or later") {
		t.Error("the description does not state the rollout rule")
	}
}
