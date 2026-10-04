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

// The marker is a key in vault.toml and is never inferred from the ignore lines.
// A malformed marker is an error, never a silent "unmigrated".
func TestVaultMigratedReadsTheKeyOnly(t *testing.T) {
	root := t.TempDir()
	sweepFile(t, root, ".gitignore", "palace/*/drawers/\npalace/*/ingested-archives.jsonl\n")
	if ok, err := VaultMigrated(root); err != nil || ok {
		t.Fatalf("no vault.toml, both derived ignore lines: VaultMigrated = %v, %v; want false", ok, err)
	}
	sweepFile(t, root, ".vibe-palace/vault.toml", "format = 2\n")
	if ok, err := VaultMigrated(root); err != nil || ok {
		t.Fatalf("no marker: VaultMigrated = %v, %v; want false", ok, err)
	}
	sweepFile(t, root, ".vibe-palace/vault.toml", "format = 2\nauthored_only = \"2026-10-03\"\n")
	if ok, err := VaultMigrated(root); err != nil || !ok {
		t.Fatalf("marker present: VaultMigrated = %v, %v; want true", ok, err)
	}
	sweepFile(t, root, ".vibe-palace/vault.toml", "format = 2\nauthored_only = 3\n")
	if ok, err := VaultMigrated(root); err == nil || ok || !strings.Contains(err.Error(), "authored_only") {
		t.Fatalf("malformed marker: VaultMigrated = %v, %v; want false and an error naming authored_only", ok, err)
	}
}

// IndexReapable is !ProjectExists plus the rename-pending keep. It is NOT the
// embed cache's Lstat keep rule: palace/<p>/ holding only .local/ is no store, and
// a departed slug whose palace/<p>/ still holds an untracked regular file (the
// legacy ingested-archives.jsonl) is one.
func TestIndexReapableIsProjectExistsAndThePendingKeep(t *testing.T) {
	root := departedVault(t)
	v := &Vault{Root: root}
	sweepFile(t, root, "Projects/live/resume.md", "x\n")
	sweepFile(t, root, "palace/localonly/.local/thing", "x\n")
	sweepFile(t, root, "palace/residue/ingested-archives.jsonl", "{}\n")
	writeRecord(t, root, "movedres", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "Projects/movedres/resume.md", "x\n")
	sweepFile(t, root, "palace/movedres/ingested-archives.jsonl", "{}\n")
	writeRecord(t, root, "movedbare", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "Projects/movedbare/resume.md", "x\n")
	sweepFile(t, root, "palace/movedbare/.local/x", "x\n")
	writeRecord(t, root, "renamed", departure.Renamed, "live")

	for slug, want := range map[string]bool{
		"live":      false, // a project
		"nothing":   true,  // nothing anywhere
		"localonly": true,  // palace/<p>/.local/ only: not a store
		"residue":   false, // a regular file outside .local/: a store
		"movedres":  false, // departed, but palace residue makes it a store
		"movedbare": true,  // departed, Projects/ side dropped, palace holds only .local/
		"renamed":   true,  // renamed away, no record on this host
	} {
		ok, reason, err := v.IndexReapable(slug)
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		exists, _ := v.ProjectExists(slug)
		if ok != want || ok == exists {
			t.Errorf("%s: reapable=%v (%q), ProjectExists=%v; want reapable=%v and never both", slug, ok, reason, exists, want)
		}
	}

	pending, err := v.IndexRenamePendingPath("renamed")
	if err != nil {
		t.Fatal(err)
	}
	sweepFile(t, root, mustRel(t, root, pending), "live\n")
	ok, reason, err := v.IndexReapable("renamed")
	if err != nil || ok || !strings.Contains(reason, "rename pending to live") {
		t.Fatalf("with this host's rename-pending record: reapable=%v (%q), %v; want kept, naming the target", ok, reason, err)
	}
}

func mustRel(t *testing.T, root, abs string) string {
	t.Helper()
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// The candidate scan lists only reapable, valid, non-dot directories, and under
// the embed-cache sweep's guards it lists nothing at all.
func TestIndexReapCandidates(t *testing.T) {
	root := departedVault(t)
	v := &Vault{Root: root}
	sweepFile(t, root, "Projects/live/resume.md", "x\n")
	for _, s := range []string{"live", "gone", ".generation", ".tomb-gone-1a", ".rename-pending"} {
		sweepFile(t, root, "palace/.local/index/"+s+"/f", "x\n")
	}
	sweepFile(t, root, "palace/.local/index/a-file", "x\n")
	got, err := v.IndexReapCandidates()
	if err != nil || !slices.Equal(got, []string{"gone"}) {
		t.Fatalf("candidates = %v, %v; want [gone]", got, err)
	}
	tombs, err := v.IndexTombstones()
	if err != nil || len(tombs) != 1 || filepath.Base(tombs[0]) != ".tomb-gone-1a" {
		t.Fatalf("tombstones = %v, %v", tombs, err)
	}

	// An empty project listing is far likelier a mis-resolved vault than an
	// empty one: nothing is a candidate.
	empty := t.TempDir()
	sweepFile(t, empty, "Projects/.keep", "")
	sweepFile(t, empty, "palace/.local/index/gone/f", "x\n")
	if got, err := (&Vault{Root: empty}).IndexReapCandidates(); err != nil || len(got) != 0 {
		t.Fatalf("empty listing: candidates = %v, %v; want none", got, err)
	}

	// A symlinked index root would remove wherever it points.
	link := departedVault(t)
	sweepFile(t, link, "Projects/live/resume.md", "x\n")
	elsewhere := t.TempDir()
	sweepFile(t, elsewhere, "gone/f", "x\n")
	if err := os.MkdirAll(filepath.Join(link, "palace", ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(link, "palace", ".local", "index")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if got, err := (&Vault{Root: link}).IndexReapCandidates(); err != nil || len(got) != 0 {
		t.Fatalf("symlinked index root: candidates = %v, %v; want none", got, err)
	}
}

// The rename-pending records are read by a (*Vault) method with slug validation,
// and a damaged record is an error, never silently dropped.
func TestRenamePendingRecords(t *testing.T) {
	v := &Vault{Root: t.TempDir()}
	if _, err := v.IndexRenamePendingPath("Not A Slug"); err == nil {
		t.Fatal("IndexRenamePendingPath accepted an invalid slug")
	}
	if got, err := v.ListRenamePending(); err != nil || len(got) != 0 {
		t.Fatalf("no records: %v, %v", got, err)
	}
	p, _ := v.IndexRenamePendingPath("old")
	sweepFile(t, v.Root, mustRel(t, v.Root, p), "new\n")
	if to, ok, err := v.RenamePending("old"); err != nil || !ok || to != "new" {
		t.Fatalf("RenamePending = %q, %v, %v", to, ok, err)
	}
	if got, err := v.ListRenamePending(); err != nil || len(got) != 1 || got["old"] != "new" {
		t.Fatalf("ListRenamePending = %v, %v", got, err)
	}
	sweepFile(t, v.Root, mustRel(t, v.Root, p), "Not A Slug\n")
	if _, err := v.ListRenamePending(); err == nil {
		t.Fatal("a record naming an invalid target was accepted")
	}
}

// A dot-file in the records directory (an atomic write's temp) is not a record:
// it is skipped, and the real records still list.
func TestListRenamePendingSkipsDotFiles(t *testing.T) {
	v := &Vault{Root: t.TempDir()}
	p, _ := v.IndexRenamePendingPath("old")
	sweepFile(t, v.Root, mustRel(t, v.Root, p), "new\n")
	sweepFile(t, v.Root, mustRel(t, v.Root, filepath.Join(v.IndexRenamePendingDir(), ".vp-atomic-123")), "garbage")
	if got, err := v.ListRenamePending(); err != nil || len(got) != 1 || got["old"] != "new" {
		t.Fatalf("ListRenamePending = %v, %v; want {old: new}", got, err)
	}
}
