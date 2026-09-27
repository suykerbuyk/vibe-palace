// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// Task embed-cache-sweep-honours-slug-departures (F2a), the search side: a
// project that moved to another vault is neither served from the in-memory
// index a long-lived engine built before it left, nor left on disk once any
// cache operation runs. Every embedder here is embedder.MockEmbedder.

func departProject(t *testing.T, root, slug string) {
	t.Helper()
	for _, tree := range []string{"palace", "Projects"} {
		if err := os.RemoveAll(filepath.Join(root, tree, slug)); err != nil {
			t.Fatal(err)
		}
	}
	b, err := (departure.Record{Slug: slug, Kind: departure.MovedToVault,
		To: "git@example.test:quantum/vibe-palace-vault.git", Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, filepath.FromSlash(departure.RelPath(slug)))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// T14 (R6). A long-lived engine indexed "gone" before it moved away. Afterwards
// neither the cross-project search nor SearchReady (bootstrap's path) may serve
// it, and the live project's results are unchanged.
func TestSearch_DepartedProjectIsNotServedFromMemory(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	addDrawer(t, v, "gone", "gone", "general", "turbine blade inspection notes", "facts")
	addDrawer(t, v, "live", "live", "general", "turbine blade inspection notes too", "facts")

	if _, err := eng.Search(ctx, "turbine blade", SearchFilters{}); err != nil {
		t.Fatal(err)
	}
	if !eng.HasIndex("gone") {
		t.Fatal("precondition: the engine holds an in-memory index for gone")
	}
	departProject(t, v.Root, "gone")

	t.Run("cross_project", func(t *testing.T) {
		results, err := eng.Search(ctx, "turbine blade", SearchFilters{})
		if err != nil {
			t.Fatal(err)
		}
		live := 0
		for _, r := range results {
			if r.Project == "gone" {
				t.Errorf("cross-project search served the departed project: %+v", r)
			}
			if r.Project == "live" {
				live++
			}
		}
		if live == 0 {
			t.Error("the live project's results disappeared")
		}
	})
	t.Run("search_ready", func(t *testing.T) {
		ready, err := eng.SearchReady(ctx, "turbine blade", SearchFilters{Project: "gone"})
		if err != nil || len(ready) != 0 {
			t.Errorf("SearchReady served the departed project: %d result(s), err %v", len(ready), err)
		}
		if ready, err := eng.SearchReady(ctx, "turbine blade", SearchFilters{Project: "live"}); err != nil || len(ready) == 0 {
			t.Errorf("SearchReady for the live project: %d result(s), err %v", len(ready), err)
		}
	})
}

// T12d. Trigger C: the first cache operation of a search process removes a
// moved-to-vault project's cache before it returns, even with residue that
// keeps Projects/<slug>/ on disk.
func TestEmbedCache_FirstOperationRemovesADepartedCache(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	v := testVault(t)
	cmd := exec.Command("git", "-C", v.Root, "init", "-q")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	write := func(rel, body string) {
		p := filepath.Join(v.Root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "palace/.local/\n*.bak\n")
	mkProject(t, v, "keep")
	departProject(t, v.Root, "gone")
	write("Projects/gone/transcripts/x.manifest.json.0.bak", "residue\n")
	write("palace/.local/embed-cache/gone/aaaaaaaa.vec", "VVVV")

	c := NewEmbedCache(v)
	if _, err := c.Get("keep", "00000000"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(v.Root, "palace", ".local", "embed-cache", "gone")); !os.IsNotExist(err) {
		t.Fatalf("the departed cache survived the first cache operation (lstat err %v)", err)
	}
}
