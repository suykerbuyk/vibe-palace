// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The departed-cache pass (task embed-cache-sweep-honours-slug-departures, F2a).
// Every fixture is a real git vault: departure.Find asks git whether a surviving
// Projects/<slug>/ holds only residue, and outside a repository it never says
// departed.

const departedLabel = "git@example.test:quantum/vibe-palace-vault.git"

func departedVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sweepGit(t, root, "init", "-q")
	sweepFile(t, root, ".gitignore", "palace/.local/\n*.bak\n.vp-locks/\n")
	keeper(t, root)
	sweepFile(t, root, "Projects/keep/resume.md", "keep\n")
	return root
}

func writeRecord(t *testing.T, root, slug string, kind departure.Kind, to string) {
	t.Helper()
	b, err := (departure.Record{Slug: slug, Kind: kind, To: to, Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	sweepFile(t, root, departure.RelPath(slug), string(b))
}

func seedCache(t *testing.T, root, slug string) {
	t.Helper()
	sweepFile(t, root, "palace/.local/embed-cache/"+slug+"/aaaaaaaa.vec", "VVVV")
	sweepFile(t, root, "palace/.local/embed-cache/"+slug+"/"+EmbedCacheFingerprintFile, "fp\n")
}

func cacheExists(root, slug string) bool {
	return sweepExists(root, "palace/.local/embed-cache/"+slug)
}

// T1. Moved-to-vault with an ignored *.bak left under Projects/<s>/: the cache
// goes. Today's reaper keeps it, because the directory exists.
func TestDepartedCaches_MovedToVaultWithIgnoredResidue(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "Projects/qms/transcripts/x.manifest.json.0.bak", "residue\n")
	seedCache(t, root, "qms")

	res := mustSweep(t, &Vault{Root: root})
	if cacheExists(root, "qms") || res.Departed != 1 {
		t.Fatalf("a moved-to-vault project's cache must go despite ignored residue: %+v", res)
	}
	if !sweepExists(root, "Projects/qms/transcripts/x.manifest.json.0.bak") {
		t.Error("the residue itself is not the cache pass's to remove")
	}
}

// T2. Moved-to-vault with only palace/<s>/.local/ left (case C).
func TestDepartedCaches_MovedToVaultWithPalaceLocalResidue(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "palace/qms/.local/imported-sessions.jsonl", "{}\n")
	seedCache(t, root, "qms")

	mustSweep(t, &Vault{Root: root})
	if cacheExists(root, "qms") {
		t.Fatal("a moved-to-vault project's cache must go when only palace/<s>/.local/ survives")
	}
}

// T3. Moved-to-vault, nothing left: gone (green before F2a too; pins the refactor).
func TestDepartedCaches_MovedToVaultNoResidue(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	seedCache(t, root, "qms")
	mustSweep(t, &Vault{Root: root})
	if cacheExists(root, "qms") {
		t.Fatal("cache kept")
	}
}

// D1 (split-and-sweep-reporting-defects-found-by-rehearsal-a2): a departed
// cache that also holds a file which is not a cache file — the live specimen is
// the one-shot migration's .project-slug-cache-done — loses every vector but
// keeps its directory. The pass must still report it as removed (and say why
// the directory stayed), not stay silent about the largest deletion on the host.
func TestDepartedCaches_ReportsVectorsRemovedFromAKeptDirectory(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	writeRecord(t, root, "orch", departure.MovedToVault, departedLabel)
	seedCache(t, root, "qms")
	seedCache(t, root, "orch")
	marker := "palace/.local/embed-cache/qms/.project-slug-cache-done"
	sweepFile(t, root, marker, "done\n")

	r := (&Vault{Root: root}).DepartedCaches(true)
	if len(r.Errors) != 0 || r.Undecidable != "" {
		t.Fatalf("errors %v undecidable %q", r.Errors, r.Undecidable)
	}
	if !slices.Equal(r.Removed, []string{"orch", "qms"}) {
		t.Errorf("Removed = %q, want both: every cache that lost a vector", r.Removed)
	}
	if !slices.Equal(r.Kept, []string{"qms"}) {
		t.Errorf("Kept = %q, want [qms]: only its directory stayed", r.Kept)
	}
	if n, err := countCacheFiles(filepath.Join(root, "palace/.local/embed-cache/qms")); err != nil || n != 0 {
		t.Errorf("qms still holds %d cache files (%v)", n, err)
	}
	if !sweepExists(root, marker) {
		t.Error("the non-cache marker must stay (reaping it is not this pass's job)")
	}
	if cacheExists(root, "orch") {
		t.Error("a cache of vectors only must lose its directory")
	}
}

// D1, the search-process stage: SweepEmbedCaches counts the kept-directory
// cache as departed too.
func TestSweepEmbedCaches_CountsAKeptDirectoryAsDeparted(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	seedCache(t, root, "qms")
	sweepFile(t, root, "palace/.local/embed-cache/qms/.project-slug-stray.list", "x\n")
	if res := mustSweep(t, &Vault{Root: root}); res.Departed != 1 {
		t.Errorf("Departed = %d, want 1", res.Departed)
	}
}

// T4. A chain renamed -> moved-to-vault, with residue under the FIRST slug so the
// orphan reap cannot mask a revert: the first slug's cache goes.
func TestDepartedCaches_ChainEndingMovedToVault(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qng", departure.Renamed, "qms")
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "Projects/qng/transcripts/y.bak", "residue\n")
	seedCache(t, root, "qng")

	mustSweep(t, &Vault{Root: root})
	if cacheExists(root, "qng") {
		t.Fatal("a chain ending moved-to-vault must remove the first slug's cache")
	}
}

// T5. A renamed departure is left exactly as today: with residue the cache of
// <from> is KEPT, with none the orphan reap takes it; <to> is never touched.
func TestDepartedCaches_RenamedIsLeftAsToday(t *testing.T) {
	t.Run("with_residue_kept", func(t *testing.T) {
		root := departedVault(t)
		writeRecord(t, root, "old", departure.Renamed, "new")
		sweepFile(t, root, "Projects/old/transcripts/z.bak", "residue\n")
		sweepFile(t, root, "Projects/new/resume.md", "new\n")
		seedCache(t, root, "old")
		seedCache(t, root, "new")
		before := dirDigest(t, filepath.Join(root, "palace/.local/embed-cache/new"))

		mustSweep(t, &Vault{Root: root})
		if !cacheExists(root, "old") {
			t.Error("a renamed slug's cache must be kept while its directory survives (F2b owns the carry)")
		}
		if dirDigest(t, filepath.Join(root, "palace/.local/embed-cache/new")) != before {
			t.Error("embed-cache/<to> changed")
		}
	})
	t.Run("no_residue_orphan_reaped", func(t *testing.T) {
		root := departedVault(t)
		writeRecord(t, root, "old", departure.Renamed, "new")
		sweepFile(t, root, "Projects/new/resume.md", "new\n")
		seedCache(t, root, "old")
		res := mustSweep(t, &Vault{Root: root})
		if cacheExists(root, "old") || res.Reaped != 1 || res.Departed != 0 {
			t.Errorf("with both trees gone the orphan reap takes it, as today: %+v", res)
		}
	})
}

// T6. A malformed record, a cycle, or a moved-to-vault record that fails
// Validate: no departure action, and with residue under Projects/<s>/ the cache
// is KEPT, exactly as today.
func TestDepartedCaches_UntrustedChainEndsAreLeftAsToday(t *testing.T) {
	cases := map[string]func(root string){
		"malformed": func(root string) { sweepFile(t, root, departure.RelPath("qms"), "{not json\n") },
		"cycle": func(root string) {
			writeRecord(t, root, "qms", departure.Renamed, "qms2")
			writeRecord(t, root, "qms2", departure.Renamed, "qms")
		},
		"invalid_label": func(root string) {
			sweepFile(t, root, departure.RelPath("qms"),
				`{"format":"vp-departure/1","slug":"qms","kind":"moved-to-vault","to":"/home/host/path","date":"2026-09-27"}`+"\n")
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			root := departedVault(t)
			write(root)
			sweepFile(t, root, "Projects/qms/transcripts/x.bak", "residue\n")
			seedCache(t, root, "qms")
			if res := mustSweep(t, &Vault{Root: root}); !cacheExists(root, "qms") || res.Departed != 0 {
				t.Errorf("an untrusted chain end must get no departure action: %+v", res)
			}
		})
	}
}

// T7. A re-created slug (record plus scaffold content git would carry) is live.
// Reversed by the U15 ruling: the record wins over the directory, so a
// re-created Projects/qms/ does not reopen the slug, and its moved-to-vault
// cache is dropped like any departed project's.
func TestDepartedCaches_RecreatedSlugIsStillDeparted(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "Projects/qms/resume.md", "re-created\n")
	seedCache(t, root, "qms")
	mustSweep(t, &Vault{Root: root})
	if cacheExists(root, "qms") {
		t.Fatal("a re-created Projects/qms/ must not keep the departed slug's cache")
	}
}

// T8. No record: both trees absent -> reaped as today; palace/<s>/.local only ->
// kept, as today (a husk is not F2a's to judge).
func TestDepartedCaches_UnrecordedSlugsAreJudgedAsToday(t *testing.T) {
	root := departedVault(t)
	seedCache(t, root, "gone")
	seedCache(t, root, "husk")
	sweepFile(t, root, "palace/husk/.local/imported-sessions.jsonl", "{}\n")
	res := mustSweep(t, &Vault{Root: root})
	if cacheExists(root, "gone") || !cacheExists(root, "husk") || res.Departed != 0 || res.Reaped != 1 {
		t.Fatalf("unrecorded slugs must be judged by the orphan rule alone: %+v", res)
	}
}

// T9 (R1). A stale moved-to-vault record, Projects/<s> absent, but a real
// palace/<s> store: the slug is still listed, so its cache is KEPT.
func TestDepartedCaches_ListedStoreKeepsItsCache(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	sweepFile(t, root, "palace/qms/drawers/qms/general/drawers.jsonl", "{}\n")
	seedCache(t, root, "qms")
	mustSweep(t, &Vault{Root: root})
	if !cacheExists(root, "qms") {
		t.Fatal("a slug ListAllProjects still lists must keep its cache, whatever its record says")
	}
}

// T10 (R2). The pass sits behind the reaper's tree guards.
func TestDepartedCaches_TreeGuards(t *testing.T) {
	t.Run("dangling_projects_symlink", func(t *testing.T) {
		root := t.TempDir()
		sweepGit(t, root, "init", "-q")
		sweepFile(t, root, "palace/store/kg/entities.jsonl", "{}")
		writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
		seedCache(t, root, "qms")
		if err := os.Symlink(filepath.Join(root, "unmounted"), filepath.Join(root, "Projects")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		mustSweep(t, &Vault{Root: root})
		if !cacheExists(root, "qms") {
			t.Fatal("no cache may go without a Projects/ tree")
		}
		if r := (&Vault{Root: root}).DepartedCaches(false); r.Undecidable == "" || len(r.Departed) != 0 {
			t.Errorf("the report must be undecidable, never empty-and-decided: %+v", r)
		}
	})
	t.Run("empty_listing", func(t *testing.T) {
		root := t.TempDir()
		sweepGit(t, root, "init", "-q")
		sweepMkdir(t, root, "Projects")
		writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
		seedCache(t, root, "qms")
		mustSweep(t, &Vault{Root: root})
		if !cacheExists(root, "qms") {
			t.Fatal("an empty listing must remove nothing")
		}
		if r := (&Vault{Root: root}).DepartedCaches(false); !strings.Contains(r.Undecidable, "empty") {
			t.Errorf("undecidable = %q", r.Undecidable)
		}
	})
}

// T11 (O2). Nothing is judged while a merge or rebase is in progress.
func TestDepartedCaches_SkipsWhileGitIsMidOperation(t *testing.T) {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "index.lock"} {
		t.Run(marker, func(t *testing.T) {
			root := departedVault(t)
			writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
			seedCache(t, root, "qms")
			p := filepath.Join(root, ".git", marker)
			if marker == "rebase-merge" {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				sweepFile(t, root, ".git/"+marker, "0000000000000000000000000000000000000000\n")
			}
			v := &Vault{Root: root}
			want := marker
			if marker == "rebase-merge" {
				want = "rebase"
			}
			if r := v.DepartedCaches(true); len(r.Removed) != 0 || !strings.Contains(r.Undecidable, want) {
				t.Errorf("mid-operation pass must do nothing and say why: %+v", r)
			}
			if !cacheExists(root, "qms") {
				t.Error("cache removed mid-operation")
			}
		})
	}
}

// T11b (D1). A probe that cannot answer is undecidable, never "no operation".
func TestDepartedCaches_UnreadableGitStateIsUndecidable(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	seedCache(t, root, "qms")
	withSeam(t, &departedOperationInProgress, func(string) (string, error) {
		return "", errors.New("injected rev-parse failure")
	})
	if r := (&Vault{Root: root}).DepartedCaches(true); len(r.Removed) != 0 || !strings.Contains(r.Undecidable, "could not be read") {
		t.Errorf("a failed git probe must stop the pass and say so: %+v", r)
	}
	if !cacheExists(root, "qms") {
		t.Error("cache removed although the git state was unreadable")
	}
}

// T16 (D2). A symlinked cache root — or a symlinked palace/.local — would make
// removal delete *.vec files wherever it points. Nothing outside the vault may
// go, by the departed pass or by the orphan reap.
func TestDepartedCaches_SymlinkedCacheRootIsNeverSwept(t *testing.T) {
	for _, link := range []string{"palace/.local/embed-cache", "palace/.local"} {
		t.Run(strings.ReplaceAll(link, "/", "_"), func(t *testing.T) {
			root := departedVault(t)
			outside := t.TempDir()
			target := outside
			if link == "palace/.local" {
				target = filepath.Join(outside, "local")
				sweepFile(t, outside, "local/embed-cache/qms/aaaaaaaa.vec", "VVVV")
				sweepFile(t, outside, "local/embed-cache/orphan/bbbbbbbb.vec", "OOOO")
			} else {
				sweepFile(t, outside, "qms/aaaaaaaa.vec", "VVVV")
				sweepFile(t, outside, "orphan/bbbbbbbb.vec", "OOOO")
			}
			writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
			sweepMkdir(t, root, filepath.Dir(link))
			if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(link))); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			before := dirDigest(t, outside)
			mustSweep(t, &Vault{Root: root})
			if r := (&Vault{Root: root}).DepartedCaches(true); r.Undecidable == "" {
				t.Errorf("a symlinked %s must be undecidable: %+v", link, r)
			}
			if dirDigest(t, outside) != before {
				t.Errorf("files outside the vault changed through a symlinked %s", link)
			}
		})
	}
}

// T12a/b. Trigger A: Pull and SyncVault remove a moved-to-vault project's cache
// after merging its departure.
func TestDepartedCaches_PullAndSyncTrigger(t *testing.T) {
	for _, via := range []string{"pull", "sync"} {
		t.Run(via, func(t *testing.T) {
			b, bare := departureFixture(t)
			departOnRemote(t, bare, departure.MovedToVault, departedLabel, nil)
			writeFile(t, b, "palace/.local/embed-cache/old/aaaaaaaa.vec", "VVVV")
			writeFile(t, b, "palace/.local/embed-cache/keep/bbbbbbbb.vec", "KKKK")
			if via == "pull" {
				if _, err := Pull(b, []string{"origin"}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := SyncVault(b, []string{"origin"}); err != nil {
				t.Fatal(err)
			}
			if sweepExists(b, "palace/.local/embed-cache/old") {
				t.Errorf("%s did not remove the moved project's cache", via)
			}
			if !sweepExists(b, "palace/.local/embed-cache/keep/bbbbbbbb.vec") {
				t.Errorf("%s removed a live project's cache", via)
			}
		})
	}
}

// T15. Trigger A runs after pullCore has released the vault root lock that
// U1's merge takes (vaultpull.go, the per-remote Acquire in pullCore): the pass
// never extends a pull's critical section, and it could not take the lock
// itself without deadlocking. The pass's own git-state probe is the moment
// observed.
func TestDepartedCaches_PullTriggerRunsOutsideTheRootLock(t *testing.T) {
	for _, via := range []string{"pull", "sync"} {
		t.Run(via, func(t *testing.T) {
			b, bare := departureFixture(t)
			departOnRemote(t, bare, departure.MovedToVault, departedLabel, nil)
			writeFile(t, b, "palace/.local/embed-cache/old/aaaaaaaa.vec", "VVVV")
			probed, free := false, false
			withSeam(t, &departedOperationInProgress, func(root string) (string, error) {
				rel, ok, err := vaultlock.TryAcquire(root, root)
				if err == nil && ok {
					free = true
					_ = rel()
				}
				probed = true
				return operationInProgress(root)
			})
			var err error
			if via == "pull" {
				_, err = Pull(b, []string{"origin"})
			} else {
				_, err = SyncVault(b, []string{"origin"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !probed || !free {
				t.Fatalf("the departed pass ran=%v with the root lock free=%v; it must run after pullCore releases it", probed, free)
			}
		})
	}
}

// T12c. The report never mutates, and counts only *.vec and the sidecar.
func TestDepartedCaches_ReportDoesNotMutate(t *testing.T) {
	root := departedVault(t)
	writeRecord(t, root, "qms", departure.MovedToVault, departedLabel)
	writeRecord(t, root, "foreign", departure.MovedToVault, departedLabel)
	seedCache(t, root, "qms")
	sweepFile(t, root, "palace/.local/embed-cache/foreign/notes.txt", "not a cache file\n")
	before := dirDigest(t, filepath.Join(root, "palace", ".local"))

	r := (&Vault{Root: root}).DepartedCaches(false)
	if !slices.Equal(r.Departed, []string{"qms"}) || len(r.Removed) != 0 {
		t.Errorf("report = %+v, want exactly [qms] and nothing removed", r)
	}
	if dirDigest(t, filepath.Join(root, "palace", ".local")) != before {
		t.Error("the report mutated palace/.local")
	}
	if sweepExists(root, "palace/.local/models") {
		t.Error("the report created palace/.local/models")
	}
}

// T13 (R8). The storage triggers cannot embed: internal/storage does not
// depend on the embedder or the search package at all.
func TestDepartedCaches_StorageCannotEmbed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for dep := range strings.FieldsSeq(string(out)) {
		if strings.HasSuffix(dep, "/internal/embedder") || strings.HasSuffix(dep, "/internal/search") {
			t.Fatalf("internal/storage depends on %s: a storage trigger could reach an embedder", dep)
		}
	}
}

func dirDigest(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel)
		if !d.IsDir() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b.WriteString("=")
			b.Write(data)
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b.String()
}
