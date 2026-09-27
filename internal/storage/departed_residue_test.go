// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// Case B of departed-ignores-a-directory-holding-only-ignored-residue in
// storage: a renamed-away slug whose Projects/old-slug/ survived a pull
// holding one ignored backup is not a project (ListAllProjects), and the
// per-host rebind step treats its rename as landed.

func residueRenamedVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	rebindVault(t, root, "old-slug", "new-slug")
	rebindWrite(t, filepath.Join(root, ".gitignore"), "*.bak\n.vp-locks/\npalace/.local/\n")
	rebindGit(t, root, "init", "-q", "-b", "main")
	rebindGit(t, root, "add", "-A")
	rebindGit(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "seed")
	rebindGit(t, root, "rm", "-q", "-r", "Projects/old-slug")
	b, err := (departure.Record{Slug: "old-slug", Kind: departure.Renamed, To: "new-slug", Date: "2026-09-25"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(root, filepath.FromSlash(departure.RelPath("old-slug"))), string(b))
	rebindGit(t, root, "add", "-A")
	rebindGit(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "rename old-slug")
	rebindWrite(t, filepath.Join(root, "Projects/old-slug/transcripts/a.manifest.json.0.bak"), "residue\n")
	return root
}

func TestListAllProjectsDropsAResidueOnlyDepartedSlug(t *testing.T) {
	root := residueRenamedVault(t)
	all, err := NewVault(root).ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	slugs := make([]string, 0, len(all))
	for _, p := range all {
		slugs = append(slugs, p.Slug)
	}
	if slices.Contains(slugs, "old-slug") {
		t.Errorf("ListAllProjects = %v: a residue-only departed slug is not a project", slugs)
	}
	if !slices.Contains(slugs, "new-slug") {
		t.Errorf("ListAllProjects = %v: the live project must stay", slugs)
	}
	// Overreach guard: residue under a slug with NO record is still listed,
	// exactly as before (drift reporting for it is unchanged).
	rebindWrite(t, filepath.Join(root, "Projects/unrecorded/x.bak"), "residue\n")
	all, _ = NewVault(root).ListAllProjects()
	found := false
	for _, p := range all {
		found = found || p.Slug == "unrecorded"
	}
	if !found {
		t.Error("a residue-only directory with no departure record must still be listed")
	}
}

// The per-host rebind step (the migration's Q3.3 / the split's L5): a host
// whose pull left residue under the old slug must still be able to rebind its
// checkout. It was refused at checkout_rebind.go:283 ("the vault rename has
// not landed").
func TestRebindCheckoutRenameLandsOverResidue(t *testing.T) {
	rebindEnv(t)
	vault := residueRenamedVault(t)
	co := t.TempDir()
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), rebindToml)

	if _, err := RebindCheckout(rebindRename(co, vault)); err != nil {
		t.Fatalf("rebind over a residue-only Projects/old-slug/: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(co, ".vibe-palace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `name = "new-slug"`) {
		t.Errorf("the checkout must now name new-slug:\n%s", got)
	}

	// Real content under the old slug is a rename that has NOT landed.
	vault2 := residueRenamedVault(t)
	rebindWrite(t, filepath.Join(vault2, "Projects/old-slug/memory/new-work.md"), "unmoved\n")
	co2 := t.TempDir()
	rebindWrite(t, filepath.Join(co2, ".vibe-palace.toml"), rebindToml)
	if _, err := RebindCheckout(rebindRename(co2, vault2)); err == nil || !strings.Contains(err.Error(), "has not landed") {
		t.Errorf("content under the old slug must still refuse, got %v", err)
	}
}
