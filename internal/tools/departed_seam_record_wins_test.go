// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// U15 send-back, R1: the record wins over the directory at the MCP dispatch
// seam too. With a moved-to-vault record for p, every project-scoped writer
// refuses — whether Projects/p is absent or holds a stray file, which under
// the old "directory wins" rule re-opened p to all of them.
func TestProjectToolsRefuseADepartedProjectWhateverItsDirectoryHolds(t *testing.T) {
	tools := map[string]map[string]any{
		"vp_capture_session":  {"project": "p", "summary": "stale session", "task": "t", "tag": "implementation"},
		"vp_append_iteration": {"project": "p", "title": "stale", "narrative": "stale iteration"},
		"vp_memory_write":     {"project": "p", "rel": "stale.md", "name": "n", "description": "d", "type": "project", "body": "b"},
		"vp_manage_task":      {"project": "p", "action": "create", "task": "stale-task", "title": "stale", "content": strings.Repeat("a real plan body. ", 20)},
	}
	for name, args := range tools {
		for _, stray := range []bool{false, true} {
			root := t.TempDir()
			putFile(t, root, "Projects/keep/resume.md", "live\n")
			b, err := (departure.Record{Slug: "p", Kind: departure.MovedToVault, To: "git@example.invalid:q/v.git", Date: "2026-09-27"}).Encode()
			if err != nil {
				t.Fatal(err)
			}
			putFile(t, root, departure.RelPath("p"), string(b))
			if stray {
				putFile(t, root, "Projects/p/stray.md", "a stray file\n")
			}
			text, isErr := callTool(t, seamServer(t, storage.NewVault(root)), name, args)
			if !isErr {
				t.Errorf("%s (stray %v): not refused: %.160s", name, stray, text)
			}
			// Not even a directory: the seam refuses before the writer runs,
			// where the storage funnel (the backstop below it) would still let
			// a writer create its directories before refusing the file.
			var written []string
			for _, tree := range []string{"Projects/p", "palace/p"} {
				_ = filepath.WalkDir(filepath.Join(root, tree), func(p string, d os.DirEntry, err error) error {
					if err == nil && p != filepath.Join(root, tree) && filepath.Base(p) != "stray.md" {
						written = append(written, p)
					}
					return nil
				})
			}
			if len(written) > 0 {
				t.Errorf("%s (stray %v) wrote into p: %v", name, stray, written)
			}
		}
	}
}

// Item 1 at the MCP surface: vp_vault_write cannot forge a departure record
// (which would lock the live project out), and vp_vault_delete cannot remove
// one (which would reopen a departed project).
func TestRawFileToolsCannotForgeOrRemoveADepartureRecord(t *testing.T) {
	root := t.TempDir()
	putFile(t, root, "Projects/keep/resume.md", "live\n")
	b, err := (departure.Record{Slug: "p", Kind: departure.MovedToVault, To: "git@example.invalid:q/v.git", Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, root, departure.RelPath("p"), string(b))
	srv := seamServer(t, storage.NewVault(root))
	if text, isErr := callTool(t, srv, "vp_vault_write", map[string]any{"path": departure.RelPath("keep"), "content": `{"slug":"keep","kind":"deleted"}`}); !isErr {
		t.Errorf("vp_vault_write forged a record: %.160s", text)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(departure.RelPath("keep")))); err == nil {
		t.Error("keep.json exists")
	}
	if text, isErr := callTool(t, srv, "vp_vault_delete", map[string]any{"path": departure.RelPath("p")}); !isErr {
		t.Errorf("vp_vault_delete removed a record: %.160s", text)
	}
	if _, found := departure.Find(root, "p"); !found {
		t.Error("p.json is gone")
	}
}
