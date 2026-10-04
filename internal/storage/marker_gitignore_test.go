// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Lines never precede the marker (XC2). On an unmigrated vault neither the
// create nor the top-up path writes a derived line, and a dirty tracked drawer
// still tidies with no error. Mutant: the lines made canonical; or only the
// create path gated.
func TestVaultGitignore_LinesNeverPrecedeTheMarker(t *testing.T) {
	dir := layUnmigratedDrawerVault(t)
	if missing, err := MissingVaultGitignorePatterns(dir); err != nil || len(missing) != 0 {
		t.Fatalf("Missing = %q, %v; want none on an unmigrated vault", missing, err)
	}
	if n, err := TopUpVaultGitignore(dir); err != nil || n != 0 {
		t.Fatalf("TopUp added %d, %v", n, err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if hasDerivedLines(t, dir) {
		t.Fatal("an unmigrated vault got derived ignore lines")
	}
	writeFile(t, dir, fixtureDrawer, `{"id":"d1"}`+"\n"+`{"id":"d2"}`+"\n")
	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("tidy of a dirty tracked drawer: %v", err)
	}
	if !slices.Contains(res.Swept, fixtureDrawer) {
		t.Errorf("drawer not swept: %+v", res)
	}
}

// A migrated vault gets exactly the two lines, through both paths.
func TestVaultGitignore_MigratedVaultGetsTheLines(t *testing.T) {
	dir := layMigratedVault(t)
	// Drop the lines the fixture's migration wrote, to see the top-up add them.
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := appendMissingGitignoreLines(nil, CanonicalGitignorePatterns)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	missing, err := MissingVaultGitignorePatterns(dir)
	if err != nil || !slices.Equal(missing, MigratedVaultGitignorePatterns) {
		t.Fatalf("Missing = %q, %v; want exactly %q", missing, err, MigratedVaultGitignorePatterns)
	}
	if n, err := TopUpVaultGitignore(dir); err != nil || n != len(MigratedVaultGitignorePatterns) {
		t.Fatalf("TopUp added %d, %v", n, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("top-up gave\n%s\nwant the migration's\n%s", got, data)
	}
}

// Reverted vault stays clean: no marker, so the lines never come back, and
// tidy of a dirty tracked drawer exits 0. Mutant: lines regained without the
// marker.
func TestVaultGitignore_RevertedVaultStaysClean(t *testing.T) {
	dir := layMigratedThenRevertedVault(t)
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := TopUpVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if hasDerivedLines(t, dir) {
		t.Fatal("a reverted vault regained the derived ignore lines")
	}
	if st := gitRun(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("reconcile dirtied a reverted vault: %q", st)
	}
	writeFile(t, dir, fixtureDrawer, `{"id":"d1"}`+"\n"+`{"id":"d9"}`+"\n")
	if _, err := TidyVault(dir, false); err != nil {
		t.Fatalf("tidy on a reverted vault: %v", err)
	}
	if st := gitRun(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("tidy left %q", st)
	}
}

// A marker that cannot be read fails every .gitignore writer before it writes
// (R3, writer gates fail closed). Mutant: the error read as "unmigrated".
func TestVaultGitignore_MalformedMarkerFailsBeforeAnyWrite(t *testing.T) {
	dir := layUnmigratedDrawerVault(t)
	writeMalformedMarker(t, dir)
	before, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# hand-trimmed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = MissingVaultGitignorePatterns(dir)
	wantMarkerErr(t, "Missing", err)
	_, err = TopUpVaultGitignore(dir)
	wantMarkerErr(t, "TopUp", err)
	wantMarkerErr(t, "Reconcile", ReconcileVaultGitignore(dir))
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# hand-trimmed\n" {
		t.Errorf(".gitignore was written: %q (was %q before the trim)", got, before)
	}
}

// R2: vp init's commit of its own top-up recognises the derived lines on a
// migrated vault, so the vault is left clean. Mutant: the check computed from
// the canonical list alone, which keeps the file and leaves it dirty.
func TestCommitVaultInit_MigratedTopUpIsCommitted(t *testing.T) {
	dir := layMigratedVault(t)
	canonical, _ := appendMissingGitignoreLines(nil, CanonicalGitignorePatterns)
	writeFile(t, dir, ".gitignore", string(canonical))
	gitRun(t, dir, "commit", "-q", "-am", "an older host trimmed the lines")
	if n, err := TopUpVaultGitignore(dir); err != nil || n == 0 {
		t.Fatalf("TopUp added %d, %v", n, err)
	}
	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	if !vc.Committed || !slices.Equal(vc.Paths, []string{".gitignore"}) {
		t.Fatalf("got %+v, want a commit of .gitignore", vc)
	}
	if st := gitRun(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("vault left dirty: %q", st)
	}
}

// R2 with a malformed marker: the helper fails, nothing is committed.
func TestCommitVaultInit_MalformedMarkerFailsAndCommitsNothing(t *testing.T) {
	dir := layUnmigratedDrawerVault(t)
	writeMalformedMarker(t, dir)
	gitRun(t, dir, "commit", "-q", "-am", "a hand edit broke the marker")
	writeFile(t, dir, ".gitignore", "# trimmed\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	_, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true})
	wantMarkerErr(t, "CommitVaultInit", err)
	if gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
}
