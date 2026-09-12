// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// TestIntegrationLocalOnlyPalaceDirIsNotAStore drives the presence rule through
// every surface that enumerates projects. A palace/<slug>/ holding only
// machine-local .local/ state — the husk a pulled deletion leaves on a host that
// had cached vectors for the project — or only an empty subtree is not a store:
// git carries none of it, so counting it made each answer depend on the host.
//
// The complement is not hidden: vp_check's palace-local-only row names it, and
// says nothing about what to do with it.
func TestIntegrationLocalOnlyPalaceDirIsNotAStore(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	root := h.Vault.Root

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The legacy husk shape, by hand.
	write("palace/stub/.local/embed-cache/x.vec", "\x00\x00\x80\x3f")
	// An empty subtree: created ahead of a write that never landed.
	if err := os.MkdirAll(filepath.Join(root, "palace", "empty", "drawers", "w", "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real store in both trees.
	h.Seed(t, testinfra.WithDrawer("real", "general", "general", "a real drawer about widgets", "facts", "2026-09-10T10:00:00Z"))
	h.seedProject(t, "real")
	// A notes-only project: history, no palace/ store.
	write("Projects/notesonly/sessions/2026-09-10-aaaa0000-01.md",
		"---\nproject: notesonly\n---\n\n# Session\n\nNotes about the gearbox rebuild.\n")

	t.Run("vp_list_projects_omits_non_stores", func(t *testing.T) {
		var out struct {
			Projects []string `json:"projects"`
			Drift    []struct {
				Slug string `json:"slug"`
			} `json:"drift"`
		}
		if err := json.Unmarshal([]byte(h.callTool(t, "vp_list_projects", map[string]any{})), &out); err != nil {
			t.Fatal(err)
		}
		if want := []string{"notesonly", "real"}; !slices.Equal(out.Projects, want) {
			t.Errorf("projects = %v, want %v", out.Projects, want)
		}
		for _, d := range out.Drift {
			if d.Slug == "stub" || d.Slug == "empty" {
				t.Errorf("drift reports a non-store: %+v", d)
			}
		}
	})

	t.Run("vp_check_palace_local_only_names_both", func(t *testing.T) {
		var out tools.CheckSuiteResult
		raw := h.callTool(t, "vp_check", map[string]any{"checks": []string{"palace-local-only"}})
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode: %v\n%s", err, raw)
		}
		if len(out.Checks) != 1 {
			t.Fatalf("checks = %+v, want exactly the one row", out.Checks)
		}
		row := out.Checks[0]
		if row.Name != "Palace local-only" || row.Status != "info" {
			t.Fatalf("row = %+v, want an Info Palace local-only row", row)
		}
		all := strings.Join(row.Details, "\n")
		for _, want := range []string{
			"palace/stub/ — .local/: embed-cache/ (1 file) — Projects/stub/: absent",
			"palace/empty/ — (empty subtree only) — Projects/empty/: absent",
		} {
			if !strings.Contains(all, want) {
				t.Errorf("details missing %q:\n%s", want, all)
			}
		}
		if strings.Contains(all, "palace/real/") {
			t.Errorf("a real store is reported:\n%s", all)
		}
		banned := regexp.MustCompile(`(?i)\brm\b|\bdelete\b|\bleftover\b`)
		if m := banned.FindString(row.Summary + "\n" + all); m != "" {
			t.Errorf("row prescribes a disposition (%q):\n%s", m, all)
		}
	})

	t.Run("vault_audit_is_silent_on_non_stores", func(t *testing.T) {
		rep, err := vaultaudit.Run(h.Vault)
		if err != nil {
			t.Fatal(err)
		}
		reported := map[string][]string{}
		for _, d := range rep.Dimensions {
			for _, f := range d.New {
				reported[d.Name] = append(reported[d.Name], f.Artifact)
			}
		}
		for _, dim := range []string{vaultaudit.DimPalaceStoreDrawers, vaultaudit.DimProjectTreeCoherence} {
			for _, a := range reported[dim] {
				if a == "stub" || a == "empty" {
					t.Errorf("%s reports non-store %q", dim, a)
				}
			}
		}
		if !slices.Contains(reported[vaultaudit.DimProjectTreeCoherence], "notesonly") {
			t.Errorf("project-tree-coherence must still report the notes-only project (Direction A); got %v",
				reported[vaultaudit.DimProjectTreeCoherence])
		}
	})
}
