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

	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestMemoryWriteReadRoundTrip(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	wt := MemoryWriteTool(vault)
	wp, _ := json.Marshal(memoryWriteParams{
		Project:     "test-proj",
		Rel:         "pref-foo.md",
		Name:        "Prefers tabs",
		Description: "User prefers tabs.",
		Type:        "feedback",
		Body:        "The user prefers tabs over spaces.",
	})
	wres, err := wt.Handler(context.Background(), wp)
	if err != nil {
		t.Fatalf("write handler: %v", err)
	}
	if wres.(map[string]string)["status"] != "written" {
		t.Errorf("write status = %v", wres)
	}

	rt := MemoryReadTool(vault)
	rp, _ := json.Marshal(memoryReadParams{Project: "test-proj", Rel: "pref-foo.md"})
	rres, err := rt.Handler(context.Background(), rp)
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	got := rres.(memoryReadResult)
	if got.Meta.Name != "Prefers tabs" {
		t.Errorf("name = %q", got.Meta.Name)
	}
	if got.Meta.Type != "feedback" {
		t.Errorf("type = %q", got.Meta.Type)
	}
	if got.Meta.Rel != "pref-foo.md" {
		t.Errorf("rel = %q", got.Meta.Rel)
	}
	if !strings.Contains(got.Body, "prefers tabs") {
		t.Errorf("body = %q", got.Body)
	}
}

func TestMemoryWriteRoutesToMemoryDir(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	wt := MemoryWriteTool(vault)
	wp, _ := json.Marshal(memoryWriteParams{
		Project: "test-proj",
		Rel:     "pref-foo.md",
		Name:    "Prefers tabs",
		Type:    "user",
		Body:    "body",
	})
	if _, err := wt.Handler(context.Background(), wp); err != nil {
		t.Fatalf("write handler: %v", err)
	}

	want := filepath.Join(root, "Projects", "test-proj", "memory", "pref-foo.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected memory file at %s: %v", want, err)
	}
}

func TestMemoryListMetaOnly(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	wt := MemoryWriteTool(vault)
	for _, rel := range []string{"a.md", "b.md"} {
		wp, _ := json.Marshal(memoryWriteParams{
			Project: "test-proj", Rel: rel, Name: "n-" + rel, Description: "d", Type: "project", Body: "BODY-" + rel,
		})
		if _, err := wt.Handler(context.Background(), wp); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	lt := MemoryListTool(vault)
	lp, _ := json.Marshal(memoryListParams{Project: "test-proj"})
	lres, err := lt.Handler(context.Background(), lp)
	if err != nil {
		t.Fatalf("list handler: %v", err)
	}
	memories := lres.(memoryListResult).Memories
	if len(memories) != 2 {
		t.Fatalf("memories count = %d", len(memories))
	}
	// Meta-only: serialize the result and assert no body field anywhere.
	blob, _ := json.Marshal(memories)
	if strings.Contains(string(blob), "BODY-") || strings.Contains(string(blob), "\"body\"") {
		t.Errorf("list leaked bodies: %s", blob)
	}
	for _, m := range memories {
		if m.Name == "" || m.Type != "project" || m.Rel == "" {
			t.Errorf("incomplete meta: %+v", m)
		}
	}
}

func TestMemoryListLimit(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	wt := MemoryWriteTool(vault)
	for _, rel := range []string{"a.md", "b.md", "c.md"} {
		wp, _ := json.Marshal(memoryWriteParams{Project: "p", Rel: rel, Name: "n", Type: "project", Body: "x"})
		if _, err := wt.Handler(context.Background(), wp); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	lt := MemoryListTool(vault)
	lp, _ := json.Marshal(memoryListParams{Project: "p", Limit: 2})
	lres, err := lt.Handler(context.Background(), lp)
	if err != nil {
		t.Fatalf("list handler: %v", err)
	}
	if got := len(lres.(memoryListResult).Memories); got != 2 {
		t.Errorf("limited count = %d, want 2", got)
	}
}

func TestMemoryListEmpty(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	lt := MemoryListTool(vault)
	lp, _ := json.Marshal(memoryListParams{Project: "p"})
	lres, err := lt.Handler(context.Background(), lp)
	if err != nil {
		t.Fatalf("list handler: %v", err)
	}
	if got := lres.(memoryListResult).Memories; len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestMemoryDeleteIdempotent(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)
	wt := MemoryWriteTool(vault)
	wp, _ := json.Marshal(memoryWriteParams{Project: "p", Rel: "x.md", Name: "n", Type: "user", Body: "b"})
	if _, err := wt.Handler(context.Background(), wp); err != nil {
		t.Fatalf("write: %v", err)
	}

	dt := MemoryDeleteTool(vault)
	dp, _ := json.Marshal(memoryDeleteParams{Project: "p", Rel: "x.md"})
	dres, err := dt.Handler(context.Background(), dp)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if dres.(map[string]string)["status"] != "deleted" {
		t.Errorf("delete status = %v", dres)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "p", "memory", "x.md")); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
	// Second delete is a no-op (idempotent), not an error.
	if _, err := dt.Handler(context.Background(), dp); err != nil {
		t.Errorf("second delete should be idempotent: %v", err)
	}
}

func TestMemoryWriteMissingRequired(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	wt := MemoryWriteTool(vault)

	cases := []memoryWriteParams{
		{Rel: "x.md", Name: "n", Type: "user", Body: "b"},    // no project
		{Project: "p", Name: "n", Type: "user", Body: "b"},   // no rel
		{Project: "p", Rel: "x.md", Type: "user", Body: "b"}, // no name
		{Project: "p", Rel: "x.md", Name: "n", Body: "b"},    // no type
	}
	for i, c := range cases {
		p, _ := json.Marshal(c)
		if _, err := wt.Handler(context.Background(), p); err == nil {
			t.Errorf("case %d: expected error for missing required field", i)
		}
	}
}

func TestMemoryWriteBadType(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	wt := MemoryWriteTool(vault)
	p, _ := json.Marshal(memoryWriteParams{Project: "p", Rel: "x.md", Name: "n", Type: "bogus", Body: "b"})
	if _, err := wt.Handler(context.Background(), p); err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestMemoryReadMissingRequired(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rt := MemoryReadTool(vault)
	for _, p := range []memoryReadParams{{Rel: "x.md"}, {Project: "p"}} {
		raw, _ := json.Marshal(p)
		if _, err := rt.Handler(context.Background(), raw); err == nil {
			t.Errorf("expected error for %+v", p)
		}
	}
}

func TestMemoryReadNotFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rt := MemoryReadTool(vault)
	p, _ := json.Marshal(memoryReadParams{Project: "p", Rel: "nope.md"})
	if _, err := rt.Handler(context.Background(), p); err == nil {
		t.Fatal("expected error for missing memory")
	}
}

func TestMemoryListMissingProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	lt := MemoryListTool(vault)
	p, _ := json.Marshal(memoryListParams{})
	if _, err := lt.Handler(context.Background(), p); err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestMemoryDeleteMissingRequired(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	dt := MemoryDeleteTool(vault)
	for _, p := range []memoryDeleteParams{{Rel: "x.md"}, {Project: "p"}} {
		raw, _ := json.Marshal(p)
		if _, err := dt.Handler(context.Background(), raw); err == nil {
			t.Errorf("expected error for %+v", p)
		}
	}
}

func TestMemoryScopeGlobalDeferred(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	// write
	wt := MemoryWriteTool(vault)
	wp, _ := json.Marshal(memoryWriteParams{Project: "p", Rel: "x.md", Name: "n", Type: "user", Body: "b", Scope: "global"})
	if _, err := wt.Handler(context.Background(), wp); err == nil || !strings.Contains(err.Error(), "deferred to v2") {
		t.Errorf("write global: err = %v", err)
	}
	// Nothing must have been written.
	if _, err := os.Stat(filepath.Join(root, "Projects", "p", "memory", "x.md")); !os.IsNotExist(err) {
		t.Errorf("global write must not create a file: %v", err)
	}

	rt := MemoryReadTool(vault)
	rp, _ := json.Marshal(memoryReadParams{Project: "p", Rel: "x.md", Scope: "global"})
	if _, err := rt.Handler(context.Background(), rp); err == nil || !strings.Contains(err.Error(), "deferred to v2") {
		t.Errorf("read global: err = %v", err)
	}

	lt := MemoryListTool(vault)
	lp, _ := json.Marshal(memoryListParams{Project: "p", Scope: "global"})
	if _, err := lt.Handler(context.Background(), lp); err == nil || !strings.Contains(err.Error(), "deferred to v2") {
		t.Errorf("list global: err = %v", err)
	}

	dt := MemoryDeleteTool(vault)
	dp, _ := json.Marshal(memoryDeleteParams{Project: "p", Rel: "x.md", Scope: "global"})
	if _, err := dt.Handler(context.Background(), dp); err == nil || !strings.Contains(err.Error(), "deferred to v2") {
		t.Errorf("delete global: err = %v", err)
	}
}

func TestMemoryScopeProjectHonored(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	wt := MemoryWriteTool(vault)
	wp, _ := json.Marshal(memoryWriteParams{Project: "p", Rel: "x.md", Name: "n", Type: "user", Body: "b", Scope: "project"})
	if _, err := wt.Handler(context.Background(), wp); err != nil {
		t.Fatalf("scope=project should be honored: %v", err)
	}
}

func TestMemoryScopeInvalid(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	wt := MemoryWriteTool(vault)
	wp, _ := json.Marshal(memoryWriteParams{Project: "p", Rel: "x.md", Name: "n", Type: "user", Body: "b", Scope: "bogus"})
	if _, err := wt.Handler(context.Background(), wp); err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestMemoryHarvestToolDryRun(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// Point CLAUDE_HOME at a temp dir and materialize the encoded native dir
	// the tool will resolve from cwd.
	home := t.TempDir()
	t.Setenv("CLAUDE_HOME", home)
	cwd := t.TempDir()
	nativeDir, err := memory.NativeDirFromCwd(cwd)
	if err != nil {
		t.Fatalf("NativeDirFromCwd: %v", err)
	}
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	ht := MemoryHarvestTool(vault)
	if !ht.Mutating {
		t.Error("vp_memory_harvest must be Mutating")
	}
	hp, _ := json.Marshal(memoryHarvestParams{Project: "vibe-palace", Cwd: cwd, DryRun: true})
	hres, err := ht.Handler(context.Background(), hp)
	if err != nil {
		t.Fatalf("harvest handler: %v", err)
	}
	res, ok := hres.(*memory.Result)
	if !ok {
		t.Fatalf("result type = %T, want *memory.Result", hres)
	}
	if res.NativeDirMissing {
		t.Fatal("native dir should be present")
	}
	if len(res.Routed) != 3 {
		t.Errorf("dry-run should report 3 routed, got %v", res.Routed)
	}
	if res.Committed {
		t.Error("dry-run must not commit")
	}
	// Dry-run mutates nothing: native files all still present.
	matches, _ := filepath.Glob(filepath.Join(nativeDir, "*.md"))
	if len(matches) != 4 {
		t.Errorf("dry-run mutated native dir, have %d *.md, want 4", len(matches))
	}
}
