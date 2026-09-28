// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// A departed project whose Projects/<slug>/ survived a pull holding only
// ignored residue is still departed, at Departed and at both of
// RequireKnownProject's "the directory exists" arms. Case B of
// departed-ignores-a-directory-holding-only-ignored-residue.

// residueVault is gitVaultWithHistory("alpha", "rm") plus alpha's
// moved-to-vault record committed, then one ignored backup left behind.
func residueVault(t *testing.T) string {
	t.Helper()
	vault := gitVaultWithHistory(t, "alpha", "rm")
	vaultFile(t, vault, ".gitignore", "*.bak\n.vp-locks/\n")
	writeDeparture(t, vault, departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "git@example.invalid:q/quantum-vault.git", Date: "2026-09-25"})
	vaultGit(t, vault, "add", "-A")
	vaultGit(t, vault, "commit", "-q", "-m", "depart alpha")
	vaultFile(t, vault, "Projects/alpha/transcripts/a.manifest.json.0.bak", "residue\n")
	return vault
}

func TestDeparted_ResidueOnlyDirectoryIsDeparted(t *testing.T) {
	vault := residueVault(t)
	d, ok := Departed(vault, "alpha")
	if !ok || d.Source != "record" || d.Kind != departure.MovedToVault {
		t.Fatalf("Departed = %+v %v, want the record's moved-to-vault departure", d, ok)
	}
	if err := RefuseDeparted(vault, "alpha"); err == nil || !strings.Contains(err.Error(), "moved to another vault") {
		t.Errorf("RefuseDeparted = %v, want the moved-to-another-vault refusal", err)
	}
}

// Reversed by the U15 ruling: the record wins over the directory, so a vp init
// re-scaffold no longer reopens the slug; only a revert of the departure (which
// removes the record) does.
func TestDeparted_BringBackStaysDeparted(t *testing.T) {
	vault := residueVault(t)
	vaultFile(t, vault, "Projects/alpha/commands/README.md", "re-inited\n")
	if _, ok := Departed(vault, "alpha"); !ok {
		t.Error("a vp init scaffold must not reopen a slug whose record exists")
	}
	if err := RequireKnownProject("alpha", vault, ""); err == nil {
		t.Error("a writer must still refuse the re-scaffolded slug")
	}
}

// Both RequireKnownProject arms that accept "Projects/<slug>/ exists" as proof
// must refuse a residue-only departed directory. Called DIRECTLY: over MCP the
// dispatch seam refuses first and would mask a regression here.
func TestRequireKnownProject_ResidueOnlyDirectoryRefuses(t *testing.T) {
	vault := residueVault(t)
	for name, repo := range map[string]string{
		"slug-only (no repo)":          "",
		"marker names another project": markerRepo(t, "other"),
		"marker names the slug":        markerRepo(t, "alpha"),
	} {
		t.Run(name, func(t *testing.T) {
			err := RequireKnownProject("alpha", vault, repo)
			if err == nil || !strings.Contains(err.Error(), "moved to another vault") {
				t.Errorf("RequireKnownProject = %v, want the moved-to-another-vault refusal", err)
			}
		})
	}
	// Overreach guard: the live project beside it still authorizes.
	if err := RequireKnownProject("other", vault, ""); err != nil {
		t.Errorf("a live project must still authorize: %v", err)
	}
}

// The hot path runs NO git: a present project with no record asks neither the
// history seam (removedSlugGit) nor the residue seam (departure's), counted by
// a PATH shim both resolve through. The positive control proves the shim
// counts.
func TestDeparted_NoGitForAPresentProjectWithoutARecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell script as the git shim")
	}
	vault := residueVault(t)
	vaultFile(t, vault, "Projects/other/x.bak", "residue\n") // residue, but no record
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	count := filepath.Join(shimDir, "count")
	script := "#!/bin/sh\necho x >> " + count + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}

	if _, ok := Departed(vault, "other"); ok {
		t.Fatal("other is live")
	}
	if err := RequireKnownProject("other", vault, ""); err != nil {
		t.Fatal(err)
	}
	if n := calls(); n != 0 {
		t.Errorf("a present project with no record ran git %d time(s); want 0", n)
	}
	// A recorded slug is departed on the record alone: no git either.
	if _, ok := Departed(vault, "alpha"); !ok {
		t.Fatal("alpha is departed")
	}
	if n := calls(); n != 0 {
		t.Errorf("a recorded slug ran git %d time(s); want 0 (the record decides)", n)
	}
	departure.OnlyResidue(vault, "alpha")
	if n := calls(); n == 0 {
		t.Error("positive control: the residue probe must run git through the shim, or this test counts nothing")
	}
}
