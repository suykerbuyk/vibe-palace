// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT: deleted with authored_only_migration.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// aoGitRecorder puts a `git` first on PATH that appends its argv to the returned
// log file and execs the real git, so a test can assert which git commands a
// code path ran (the plan's "git-runner spy"). Every spawn — gitCmd and every
// raw exec — resolves "git" through PATH on each call, so all land in the log.
func aoGitRecorder(t *testing.T) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git recorder is a POSIX shell script")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "git-argv.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nexec \"" + real + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// aoFloorReads counts the recorded maxSurfaceAt invocations against rev — the
// floor read's distinctive argv is `ls-tree -r -z --name-only <rev> --
// Projects palace Templates Audits`. rev == "" counts every floor read.
func aoFloorReads(t *testing.T, logPath, rev string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read recorder log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "ls-tree") || !strings.HasSuffix(line, "-- Projects palace Templates Audits") {
			continue
		}
		if rev == "" || strings.Contains(line, " "+rev+" ") {
			n++
		}
	}
	return n
}

// writeTripleFixture writes a tracked triple file under palace/<p>/kg/triples/.
func writeTripleFixture(t *testing.T, root, p, name string, tr Triple) {
	t.Helper()
	data, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "palace/"+p+"/kg/triples/"+name+".json", string(data)+"\n")
}

// authoredOnlyFixture builds a populated, committed vault for the migration:
//   - project alpha: one authored triple (no extracted_at), one extracted triple,
//     one mempalace triple carrying valid_to (extracted_at, no source_session),
//     one invalidation edit (extracted_at + valid_to + source_session), a drawer,
//     an ingested-archives ledger, and an entities.jsonl with one authored and one
//     extracted line;
//   - a surface stamp committed under Projects/alpha/.surface (so Audits/.surface
//     is absent before the run, 12-N1).
func authoredOnlyFixture(t *testing.T) string {
	t.Helper()
	root := initTestRepo(t)
	// Initialise the project so vault write primitives accept writes into it.
	writeFile(t, root, "Projects/alpha/resume.md", "# alpha\n")
	// Authored (vp_kg_add shape): no extracted_at.
	writeTripleFixture(t, root, "alpha", "authored", Triple{Subject: "a", Predicate: "uses", Object: "b"})
	// Extracted: extracted_at, no valid_to.
	writeTripleFixture(t, root, "alpha", "extracted", Triple{Subject: "c", Predicate: "uses", Object: "d", ExtractedAt: "2026-01-01", SourceSession: "s1"})
	// Mempalace (X5-narrowed): extracted_at + valid_to, NO source_session → extracted.
	writeTripleFixture(t, root, "alpha", "mempalace", Triple{Subject: "e", Predicate: "uses", Object: "f", ExtractedAt: "2026-01-01", ValidTo: "2026-02-01"})
	// Invalidation edit: extracted_at + valid_to + source_session → authored.
	writeTripleFixture(t, root, "alpha", "invalidation", Triple{Subject: "g", Predicate: "uses", Object: "h", ExtractedAt: "2026-01-01", ValidTo: "2026-02-01", SourceSession: "s2"})
	// entities.jsonl: one authored (type unknown, no created_at), one extracted.
	ea, _ := json.Marshal(Entity{ID: "1", Name: "Auth", Type: "unknown"})
	ee, _ := json.Marshal(Entity{ID: "2", Name: "Ext", Type: "person", CreatedAt: "2026-01-01"})
	writeFile(t, root, "palace/alpha/kg/entities.jsonl", string(ea)+"\n"+string(ee)+"\n")
	// Derived drawer + ledger.
	writeFile(t, root, "palace/alpha/drawers/d1.md", "drawer body\n")
	writeFile(t, root, "palace/alpha/ingested-archives.jsonl", "{}\n")
	// Committed surface stamp under Projects/, not Audits/.
	writeFile(t, root, "Projects/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "fixture")
	return root
}

// tracked reports whether git tracks rel in root's index (ls-files
// --error-unmatch exits 0 when tracked, 1 when not).
func aoTracked(t *testing.T, root, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", rel)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

// TestAuthoredOnlyNormalPath exercises the whole normal-path commit on ONE
// fixture and ONE apply (kept to one to hold down internal/storage's long -race
// suite), covering several Tests-table rows at once:
//   - "Authored records kept", "Invalidation edit kept", "Mempalace triple never
//     kept": only the authored records (invalidation edit included) stay tracked,
//     stamped origin: authored; every extracted and mempalace record is gone;
//   - "Derived files deleted, not moved": no drawer on disk, no migrated/ quarantine;
//   - "The commit holds every removal" and one-commit: git show lists every
//     removal plus vault.toml and .gitignore, in exactly one new commit;
//   - "The migrator's next tidy is a no-op" (mig-S5): the tree is clean after;
//   - "Revert restores the data": git revert restores the tree byte for byte.
func TestAuthoredOnlyNormalPath(t *testing.T) {
	root := authoredOnlyFixture(t)
	beforeTree := gitRun(t, root, "rev-parse", "HEAD^{tree}")
	beforeCount := strings.TrimSpace(gitRun(t, root, "rev-list", "--count", "HEAD"))
	v := NewVault(root)
	plan, err := PlanAuthoredOnlyMigration(v)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := v.ApplyAuthoredOnlyMigration(plan, AuthoredOnlyApplyOptions{
		Date: "2026-10-09", Surface: surface.MCPSurfaceVersion, Message: "m\n",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Classification: authored (incl. invalidation edit) kept, extracted and
	// mempalace dropped; drawer and ledger dropped.
	kept := []string{"palace/alpha/kg/triples/authored.json", "palace/alpha/kg/triples/invalidation.json"}
	gone := []string{
		"palace/alpha/kg/triples/extracted.json",
		"palace/alpha/kg/triples/mempalace.json",
		"palace/alpha/drawers/d1.md",
		"palace/alpha/ingested-archives.jsonl",
	}
	for _, rel := range kept {
		if !aoTracked(t, root, rel) {
			t.Errorf("authored record %s was dropped", rel)
		}
	}
	for _, rel := range gone {
		if aoTracked(t, root, rel) {
			t.Errorf("%s is still tracked; it should be dropped", rel)
		}
	}
	// Delete, not move: no drawer on disk, no migrated/ quarantine.
	if _, err := os.Stat(filepath.Join(root, "palace/alpha/drawers/d1.md")); err == nil {
		t.Error("drawer file still on disk; delete-not-move violated")
	}
	if _, err := os.Stat(filepath.Join(root, "palace/.local/index/alpha/migrated")); err == nil {
		t.Error("a migrated/ quarantine exists; the migration must not move bytes")
	}
	// Authored triple is stamped origin: authored.
	tr, err := readTripleFile(filepath.Join(root, "palace/alpha/kg/triples/authored.json"))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Origin != OriginAuthored {
		t.Errorf("kept triple origin = %q, want %q", tr.Origin, OriginAuthored)
	}
	// entities.jsonl now holds only the authored line, stamped.
	ents, err := os.ReadFile(filepath.Join(root, "palace/alpha/kg/entities.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(ents)), "\n")
	if len(lines) != 1 {
		t.Fatalf("entities.jsonl has %d lines, want 1 (authored only):\n%s", len(lines), ents)
	}
	var e Entity
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Origin != OriginAuthored || e.Name != "Auth" {
		t.Errorf("surviving entity = %+v, want the authored one stamped origin: authored", e)
	}
	// The commit holds every removal, plus vault.toml and .gitignore.
	names := gitRun(t, root, "show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{
		"palace/alpha/drawers/d1.md", "palace/alpha/ingested-archives.jsonl",
		"palace/alpha/kg/triples/extracted.json", "palace/alpha/kg/triples/mempalace.json",
		"palace/alpha/kg/entities.jsonl", ".vibe-palace/vault.toml", ".gitignore",
	} {
		if !strings.Contains(names, want) {
			t.Errorf("migration commit does not touch %s\ncommit touched:\n%s", want, names)
		}
	}
	// Exactly one new commit.
	after := strings.TrimSpace(gitRun(t, root, "rev-list", "--count", "HEAD"))
	var b, a int
	fmt.Sscan(beforeCount, &b)
	fmt.Sscan(after, &a)
	if a != b+1 {
		t.Errorf("the migration added %d commit(s), want exactly 1 (before %s, after %s)", a-b, beforeCount, after)
	}
	// The migrator's next tidy is a no-op: the tree is clean.
	if out := gitRun(t, root, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(out) != "" {
		t.Errorf("the migrator's tree is dirty after the commit:\n%s", out)
	}
	// Revert restores the tree byte for byte.
	gitRun(t, root, "revert", "--no-edit", "HEAD")
	if got := gitRun(t, root, "rev-parse", "HEAD^{tree}"); got != beforeTree {
		t.Errorf("revert did not restore the tree byte for byte: before %s, after %s", beforeTree, got)
	}
}

// TestAuthoredOnlyLargestBlobExcludesDropped is "No blob of 20 MiB or more": the
// predicted largest blob after the commit EXCLUDES the dropped drawer, so a
// large drawer cannot keep the vault over the limit. The fixture uses a drawer
// that is merely the largest tracked blob (a real 20-MiB-plus write is left to
// the rehearsal script, which runs on real data off the -race hot path); the
// mutants killed are the same: a plan that reports the pre-commit maximum, and a
// migration that leaves the large drawer tracked.
func TestAuthoredOnlyLargestBlobExcludesDropped(t *testing.T) {
	root := initTestRepo(t)
	writeFile(t, root, "Projects/alpha/resume.md", "# alpha\n")
	const drawerSize = 64 * 1024 // larger than any authored record, small enough for -race
	writeFile(t, root, "palace/alpha/drawers/big.md", strings.Repeat("x", drawerSize))
	writeTripleFixture(t, root, "alpha", "authored", Triple{Subject: "a", Predicate: "uses", Object: "b"})
	writeFile(t, root, "Projects/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "fixture")

	v := NewVault(root)
	plan, _ := PlanAuthoredOnlyMigration(v)
	// The pre-commit maximum is the drawer.
	preMax, preRel, err := LargestTrackedBlobExcluding(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preMax < drawerSize || preRel != "palace/alpha/drawers/big.md" {
		t.Fatalf("fixture wrong: pre-commit max is %d (%s), want the %d-byte drawer", preMax, preRel, drawerSize)
	}
	// The predicted post-commit maximum EXCLUDES the dropped drawer.
	drop := map[string]bool{}
	for _, p := range plan.DropPaths() {
		drop[p] = true
	}
	predicted, predRel, err := LargestTrackedBlobExcluding(root, drop)
	if err != nil {
		t.Fatal(err)
	}
	if predicted >= drawerSize || predRel == "palace/alpha/drawers/big.md" {
		t.Errorf("predicted largest blob = %d (%s); the dropped drawer must be excluded", predicted, predRel)
	}
	if _, err := v.ApplyAuthoredOnlyMigration(plan, AuthoredOnlyApplyOptions{Date: "2026-10-09", Surface: surface.MCPSurfaceVersion, Message: "m\n"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// After the commit the drawer is gone, so the real max excludes it too.
	actual, actRel, err := LargestTrackedBlobExcluding(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if actual >= drawerSize || actRel == "palace/alpha/drawers/big.md" {
		t.Errorf("after the commit the largest blob is %d (%s); the drawer must be gone", actual, actRel)
	}
}

// TestAuthoredOnlyResidueRemoval is the departed-slug residue row: a departed
// slug whose palace/<slug>/ holds only untracked legacy files reads as gone
// after the migration, and a slug without a departure record is untouched.
func TestAuthoredOnlyResidueRemoval(t *testing.T) {
	root := authoredOnlyFixture(t)
	// A departed slug with a departure record and only untracked residue on disk.
	writeFile(t, root, departure.RelPath("gone"), `{"format":"vp-departure/1","slug":"gone","kind":"deleted","date":"2026-10-01"}`)
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "departure record")
	writeFile(t, root, "palace/gone/ingested-archives.jsonl", "legacy\n") // untracked residue
	writeFile(t, root, "palace/gone/.local/x", "legacy\n")
	// A live slug with no departure record, also with an untracked file.
	writeFile(t, root, "palace/beta/.local/keep", "keep\n")

	v := NewVault(root)
	plan, err := PlanAuthoredOnlyMigration(v)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := v.ApplyAuthoredOnlyMigration(plan, AuthoredOnlyApplyOptions{Date: "2026-10-09", Surface: surface.MCPSurfaceVersion, Message: "m\n"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "palace/gone")); err == nil {
		t.Error("departed slug residue palace/gone/ was not removed")
	}
	if _, err := os.Stat(filepath.Join(root, "palace/beta/.local/keep")); err != nil {
		t.Error("a slug without a departure record was touched")
	}
}

// TestAuthoredOnlyPathSelection is "Path selection judges HEAD and every tip":
// a stampless, palace-free HEAD with a remote tip carrying a v8 Projects stamp
// is NOT empty (normal path), and the floor check names that tip.
func TestAuthoredOnlyPathSelection(t *testing.T) {
	root := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, root, "remote", "add", "origin", bare)
	gitRun(t, root, "push", "-q", "-u", "origin", "main") // tip = HEAD (stampless, palace-free)

	st, err := ReadAuthoredOnlyGitState(root)
	if err != nil {
		t.Fatalf("git state: %v", err)
	}
	empty, _, err := VaultEmptyForAuthoredOnly(root, st)
	if err != nil {
		t.Fatalf("emptiness: %v", err)
	}
	if !empty {
		t.Fatal("a stampless, palace-free HEAD and tip should be empty")
	}

	// Now push a commit adding a v8 stamp, so the tip is no longer empty.
	writeFile(t, root, "Projects/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion-2))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "v8 stamp")
	gitRun(t, root, "push", "-q", "origin", "main")
	st, err = ReadAuthoredOnlyGitState(root)
	if err != nil {
		t.Fatal(err)
	}
	empty, reason, err := VaultEmptyForAuthoredOnly(root, st)
	if err != nil {
		t.Fatal(err)
	}
	if empty {
		t.Error("a v8 stamp at HEAD/tip must make the vault NOT empty (normal path)")
	}
	if !strings.Contains(reason, "stamp") {
		t.Errorf("reason %q should name the stamp that made it not empty", reason)
	}
	// The floor check refuses (tip at surface-2, below the current floor).
	if err := RequireSurfaceFloorAtRemoteTips(root, surface.MCPSurfaceVersion, st); err == nil {
		t.Error("floor check admitted a tip stamped below the floor")
	}
}

// TestAuthoredOnlyEmptyVaultStampsAudits is the "Quantum-shaped vault" row: the
// empty-vault path writes Audits/.surface at MCPSurfaceVersion and gates a
// one-older binary there.
func TestAuthoredOnlyEmptyVaultStampsAudits(t *testing.T) {
	root := initTestRepo(t) // seed commit only: no palace/, no stamp
	v := NewVault(root)
	plan, err := PlanAuthoredOnlyMigration(v)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := v.ApplyAuthoredOnlyMigration(plan, AuthoredOnlyApplyOptions{
		Date: "2026-10-09", EmptyVault: true, Surface: surface.MCPSurfaceVersion, Message: "m\n",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	s, err := surface.ReadStamp(filepath.Join(root, "Audits"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Errorf("Audits/.surface = %d, want %d", s.Surface, surface.MCPSurfaceVersion)
	}
	if !aoTracked(t, root, "Audits/.surface") {
		t.Error("Audits/.surface is not tracked by the empty-vault commit")
	}
	migrated, err := VaultMigrated(root)
	if err != nil || !migrated {
		t.Errorf("empty-vault commit did not mark the vault migrated (err=%v)", err)
	}
}

// aoPushMain clones bare into a throwaway working tree, applies mutate (which
// writes files under the clone), commits and pushes to main — so the fixture's
// remote tip carries content the local HEAD does not. Used to build the "tip
// holds X, HEAD does not" path-selection fixtures.
func aoPushMain(t *testing.T, bare string, mutate func(clone string)) {
	t.Helper()
	clone := t.TempDir()
	gitRun(t, clone, "clone", "-q", bare, ".")
	gitRun(t, clone, "config", "user.email", "other@example.com")
	gitRun(t, clone, "config", "user.name", "Other")
	mutate(clone)
	gitRun(t, clone, "add", "-A")
	gitRun(t, clone, "commit", "-q", "-m", "remote advance")
	gitRun(t, clone, "push", "-q", "origin", "main")
}

// TestAuthoredOnlyPathSelectionTipPalaceAndHeadStamp covers the two path-selection
// fixtures the existing test does not: (c) a remote tip that carries palace/
// content and NO stamp → the vault is NOT empty (normal path), and the floor
// check refuses, naming that tip; (d) HEAD itself carries a v8
// Projects/<slug>/.surface → NOT empty (normal path). It kills the mutant that
// chooses the path from HEAD alone (which would take (c) down the empty-vault
// path and let a tracked drawer through) and the one that treats a v8 stamp as
// "no stamp".
func TestAuthoredOnlyPathSelectionTipPalaceAndHeadStamp(t *testing.T) {
	t.Run("(c) tip has palace content, no stamp", func(t *testing.T) {
		root := initTestRepo(t)
		bare := initBareRemote(t)
		gitRun(t, root, "remote", "add", "origin", bare)
		gitRun(t, root, "push", "-q", "-u", "origin", "main") // tip == HEAD: stampless, palace-free
		// Advance the remote tip with palace/ content and NO stamp.
		aoPushMain(t, bare, func(clone string) {
			writeFile(t, clone, "palace/alpha/drawers/d1.md", "drawer\n")
		})
		st, err := ReadAuthoredOnlyGitState(root) // fetches, so the tip is seen
		if err != nil {
			t.Fatalf("git state: %v", err)
		}
		empty, reason, err := VaultEmptyForAuthoredOnly(root, st)
		if err != nil {
			t.Fatalf("emptiness: %v", err)
		}
		if empty {
			t.Error("a tip with palace/ content must make the vault NOT empty (normal path)")
		}
		if !strings.Contains(reason, "palace") {
			t.Errorf("reason %q should name the tip's palace/ content", reason)
		}
		// The floor check refuses: the tip carries no stamp (0 < floor), naming it.
		err = RequireSurfaceFloorAtRemoteTips(root, surface.MCPSurfaceVersion, st)
		if err == nil || !strings.Contains(err.Error(), "origin/main") {
			t.Errorf("floor check should refuse naming the stampless tip; got %v", err)
		}
	})

	t.Run("(d) HEAD carries a v8 Projects stamp", func(t *testing.T) {
		root := initTestRepo(t)
		bare := initBareRemote(t)
		gitRun(t, root, "remote", "add", "origin", bare)
		// HEAD carries a v8 (MCPSurfaceVersion-2) Projects/<slug>/.surface.
		writeFile(t, root, "Projects/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion-2))
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "v8 stamp at HEAD")
		gitRun(t, root, "push", "-q", "-u", "origin", "main")
		st, err := ReadAuthoredOnlyGitState(root)
		if err != nil {
			t.Fatalf("git state: %v", err)
		}
		empty, reason, err := VaultEmptyForAuthoredOnly(root, st)
		if err != nil {
			t.Fatalf("emptiness: %v", err)
		}
		if empty {
			t.Error("a v8 stamp at HEAD must make the vault NOT empty (normal path)")
		}
		if !strings.Contains(reason, "stamp") {
			t.Errorf("reason %q should name the HEAD stamp", reason)
		}
	})
}

// TestAuthoredOnlyUnmergedEntryRefuses is the "unmerged index entry" precondition
// (12-S7): CheckAuthoredOnlyGitPosture refuses when the index carries an unmerged
// entry, and unmergedPathsZ sees it. Kills a posture check that drops the
// unmerged guard.
func TestAuthoredOnlyUnmergedEntryRefuses(t *testing.T) {
	root := initTestRepo(t)
	writeFile(t, root, "conflict.txt", "base\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	sha := gitRun(t, root, "hash-object", "-w", filepath.Join(root, "conflict.txt"))
	// Inject the three conflict stages directly, with no MERGE_HEAD (which would
	// be caught earlier as an operation in progress).
	info := fmt.Sprintf("100644 %s 1\tconflict.txt\n100644 %s 2\tconflict.txt\n100644 %s 3\tconflict.txt\n", sha, sha, sha)
	if _, _, err := gitCmdStdin(root, 10*time.Second, info, "update-index", "--index-info"); err != nil {
		t.Fatalf("inject unmerged entry: %v", err)
	}
	if u, err := unmergedPathsZ(root); err != nil || len(u) == 0 {
		t.Fatalf("unmergedPathsZ saw %d entries (err %v); want the injected conflict", len(u), err)
	}
	st := &AuthoredOnlyGitState{Branch: "main"} // no remote: isolate the unmerged guard
	if err := CheckAuthoredOnlyGitPosture(root, st); err == nil {
		t.Error("posture check admitted an index with an unmerged entry")
	}
}

// TestAuthoredOnlyNormalPathReadsFloorFromTips is the normal-path half of
// "Surface survives the revert" (12-S4): the floor is read from the remote
// TRACKING ref (the tip), not from HEAD or the working tree. The recorder shows a
// maxSurfaceAt read against refs/remotes/origin/main. This kills a floor check
// that reads the working tree (which, on the empty-vault path, would count the
// migration's own Audits/.surface and pass a vault whose tips are at 8).
func TestAuthoredOnlyNormalPathReadsFloorFromTips(t *testing.T) {
	logPath := aoGitRecorder(t)
	root := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, root, "remote", "add", "origin", bare)
	// A current-surface stamp committed under Projects/alpha and pushed, so the
	// tip carries the floor.
	writeFile(t, root, "Projects/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "stamp")
	gitRun(t, root, "push", "-q", "-u", "origin", "main")

	st, err := ReadAuthoredOnlyGitState(root)
	if err != nil {
		t.Fatalf("git state: %v", err)
	}
	if err := RequireSurfaceFloorAtRemoteTips(root, surface.MCPSurfaceVersion, st); err != nil {
		t.Fatalf("floor check should pass on a tip stamped at the floor: %v", err)
	}
	tip := "refs/remotes/origin/main"
	if n := aoFloorReads(t, logPath, tip); n == 0 {
		t.Errorf("no floor read against the remote tip %s; the floor must be read from the tip, not HEAD/worktree", tip)
	}
}

// TestAuthoredOnlyEmptyPathSkipsFloorRead is the empty-vault spy (mig-B1): the
// empty-vault precondition set — CheckAuthoredOnlyGitPosture and the ancestor
// half alone — reads no surface floor. A floor read on a stampless empty vault
// would find 0, so applying the surface-9 half there would refuse every empty
// vault. The recorder proves no maxSurfaceAt read gated these checks.
func TestAuthoredOnlyEmptyPathSkipsFloorRead(t *testing.T) {
	root := initTestRepo(t) // seed only: stampless, palace-free
	bare := initBareRemote(t)
	gitRun(t, root, "remote", "add", "origin", bare)
	gitRun(t, root, "push", "-q", "-u", "origin", "main")

	st, err := ReadAuthoredOnlyGitState(root)
	if err != nil {
		t.Fatalf("git state: %v", err)
	}
	// Confirm the vault really is empty (so the empty-vault path is the one that
	// would run), using a read done BEFORE the recorder is installed.
	if empty, _, err := VaultEmptyForAuthoredOnly(root, st); err != nil || !empty {
		t.Fatalf("fixture is not an empty vault (empty=%v, err=%v)", empty, err)
	}

	// Now record only the empty-vault precondition set.
	logPath := aoGitRecorder(t)
	if err := CheckAuthoredOnlyGitPosture(root, st); err != nil {
		t.Fatalf("posture check refused a clean, pushed empty vault: %v", err)
	}
	if err := RequireRemoteTipsAreAncestors(root, st); err != nil {
		t.Fatalf("ancestor half refused a pushed tip: %v", err)
	}
	if n := aoFloorReads(t, logPath, ""); n != 0 {
		t.Errorf("the empty-vault precondition set made %d surface-floor read(s); it must make none (the floor half is skipped)", n)
	}
}

// TestAuthoredOnlyOpeningChecksRefuse is the opening-checks half of the
// per-precondition matrix (12-N4): ReadAuthoredOnlyGitState keeps the precedent's
// opening checks, each refusing when only its own condition is broken —
// git-disabled, no commit identity, a detached HEAD, a MERGE_HEAD, and a rebase
// in progress. It kills the mutant that drops the precedent's opening checks from
// the exported helper.
func TestAuthoredOnlyOpeningChecksRefuse(t *testing.T) {
	t.Run("git disabled", func(t *testing.T) {
		root := initTestRepo(t)
		setHostGitEnabled(t, false)
		_, err := ReadAuthoredOnlyGitState(root)
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("git-disabled vault not refused naming the cause; got %v", err)
		}
	})
	t.Run("no commit identity", func(t *testing.T) {
		root := initTestRepo(t)
		isolateIdentity(t, root)
		_, err := ReadAuthoredOnlyGitState(root)
		if err == nil || !strings.Contains(err.Error(), "git config") {
			t.Errorf("no-identity vault not refused naming the fix; got %v", err)
		}
	})
	t.Run("detached HEAD", func(t *testing.T) {
		root := initTestRepo(t)
		gitRun(t, root, "checkout", "-q", "--detach", "HEAD")
		_, err := ReadAuthoredOnlyGitState(root)
		if err == nil || !strings.Contains(err.Error(), "named branch") {
			t.Errorf("detached HEAD not refused naming the cause; got %v", err)
		}
	})
	t.Run("MERGE_HEAD", func(t *testing.T) {
		root := initTestRepo(t)
		head := gitRun(t, root, "rev-parse", "HEAD")
		writeFile(t, root, ".git/MERGE_HEAD", head+"\n")
		_, err := ReadAuthoredOnlyGitState(root)
		if err == nil || !strings.Contains(err.Error(), "in progress") {
			t.Errorf("MERGE_HEAD not refused naming an operation in progress; got %v", err)
		}
	})
	t.Run("rebase in progress", func(t *testing.T) {
		root := initTestRepo(t)
		if err := os.MkdirAll(filepath.Join(root, ".git", "rebase-merge"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := ReadAuthoredOnlyGitState(root)
		if err == nil || !strings.Contains(err.Error(), "in progress") {
			t.Errorf("rebase-in-progress not refused naming it; got %v", err)
		}
	})
}

// TestAuthoredOnlyAncestorAndStashRefuse covers the two remaining normal-path
// preconditions not exercised elsewhere: a remote tip that is not an ancestor of
// HEAD, and a stash entry touching palace/. Each refuses when only its own
// condition is broken, naming its cause.
func TestAuthoredOnlyAncestorAndStashRefuse(t *testing.T) {
	t.Run("remote tip not an ancestor", func(t *testing.T) {
		root := initTestRepo(t)
		bare := initBareRemote(t)
		gitRun(t, root, "remote", "add", "origin", bare)
		gitRun(t, root, "push", "-q", "-u", "origin", "main")
		// Advance the remote with a commit HEAD does not have, so the tip diverges.
		aoPushMain(t, bare, func(clone string) {
			writeFile(t, clone, "remote-only.md", "elsewhere\n")
		})
		// Make the local HEAD diverge too (a different commit), so the tip is not an
		// ancestor of HEAD.
		writeFile(t, root, "local-only.md", "here\n")
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "local only")
		st, err := ReadAuthoredOnlyGitState(root)
		if err != nil {
			t.Fatalf("git state: %v", err)
		}
		if err := RequireRemoteTipsAreAncestors(root, st); err == nil || !strings.Contains(err.Error(), "ancestor") {
			t.Errorf("a diverged tip should refuse naming ancestry; got %v", err)
		}
	})
	t.Run("stash touches palace/", func(t *testing.T) {
		root := initTestRepo(t)
		writeFile(t, root, "palace/alpha/drawers/d1.md", "drawer\n")
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "drawer")
		// A stash entry that touches palace/: modify the drawer, then stash it. The
		// tree is clean afterwards, so only the stash condition is broken. (This is
		// an isolated per-test repo, not the shared worktree, so a plain stash here
		// is safe.)
		writeFile(t, root, "palace/alpha/drawers/d1.md", "dirty\n")
		gitRun(t, root, "stash", "push", "-q", "-m", "wip")
		st := &AuthoredOnlyGitState{Branch: "main"} // no remote: isolate the stash guard
		if err := CheckAuthoredOnlyGitPosture(root, st); err == nil || !strings.Contains(err.Error(), "stash") {
			t.Errorf("a stash entry touching palace/ should refuse naming it; got %v", err)
		}
	})
}
