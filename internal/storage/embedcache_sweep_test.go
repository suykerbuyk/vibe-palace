// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sweepFile writes body at rel under root, creating parents.
func sweepFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sweepMkdir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
}

func sweepExists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func readRel(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func mustSweep(t *testing.T, v *Vault) EmbedCacheSweep {
	t.Helper()
	res, err := v.SweepEmbedCaches()
	if err != nil {
		t.Fatalf("SweepEmbedCaches: %v", err)
	}
	return res
}

// keeper gives the vault one project, so the orphan reaper's zero-projects guard
// does not decline to run.
func keeper(t *testing.T, root string) {
	t.Helper()
	sweepMkdir(t, root, "Projects/keep")
}

func TestSweepEmbedCaches_MovesLegacyCacheByteIdentical(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepFile(t, root, "palace/real/drawers/w/r/drawers.jsonl", "{}\n")
	sweepFile(t, root, "palace/real/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/real/.local/embed-cache/b.vec", "BBBBBBBB")

	res := mustSweep(t, v)
	if res.Moved != 1 || res.Healed != 0 || len(res.Errors) != 0 {
		t.Fatalf("res = %+v, want one move, no heal (the store is real), no errors", res)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/real/a.vec"); got != "AAAA" {
		t.Errorf("a.vec = %q", got)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/real/b.vec"); got != "BBBBBBBB" {
		t.Errorf("b.vec = %q", got)
	}
	if sweepExists(root, "palace/real/.local") {
		t.Error("the emptied legacy .local must be removed")
	}
	if !sweepExists(root, "palace/real/drawers/w/r/drawers.jsonl") {
		t.Error("the real store's drawers must survive")
	}
}

// TestSweepEmbedCaches_MergeNeverOverwrites: when the new-layout directory
// already exists, legacy vectors are linked in one by one, and a vector the new
// layout already holds wins — a Put that landed after the move is never
// clobbered by the older copy.
func TestSweepEmbedCaches_MergeNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepFile(t, root, "Projects/p/sessions/n.md", "x")
	sweepFile(t, root, "palace/p/.local/embed-cache/same.vec", "OLD!")
	sweepFile(t, root, "palace/p/.local/embed-cache/only-legacy.vec", "LEG!")
	sweepFile(t, root, "palace/.local/embed-cache/p/same.vec", "NEW!")

	res := mustSweep(t, v)
	if res.Merged != 1 || res.Dropped != 1 || res.Healed != 1 || len(res.Errors) != 0 {
		t.Fatalf("res = %+v, want merged 1, dropped 1, healed 1", res)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/p/same.vec"); got != "NEW!" {
		t.Errorf("same.vec = %q — the merge overwrote the new-layout vector", got)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/p/only-legacy.vec"); got != "LEG!" {
		t.Errorf("only-legacy.vec = %q", got)
	}
	if sweepExists(root, "palace/p") {
		t.Error("palace/p held only the legacy cache and must be healed away")
	}
}

// TestSweepEmbedCaches_HealsAHusk: the incident's exact shape — a pull removed
// the project's tracked files and left the ignored cache.
func TestSweepEmbedCaches_HealsAHusk(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepFile(t, root, "palace/stub/.local/embed-cache/eb021dd8.vec", "VEC!")
	sweepFile(t, root, "Projects/stub/sessions/n.md", "x") // known, so its cache is kept

	res := mustSweep(t, v)
	if res.Moved != 1 || res.Healed != 1 {
		t.Fatalf("res = %+v, want one move and one heal", res)
	}
	if sweepExists(root, "palace/stub") {
		t.Error("palace/stub must be gone")
	}
	if got := readRel(t, root, "palace/.local/embed-cache/stub/eb021dd8.vec"); got != "VEC!" {
		t.Errorf("vector = %q", got)
	}
}

// TestSweepEmbedCaches_CrashAfterRename: a process killed between the rename
// and the removals leaves an empty .local. The next run heals it even though it
// moves nothing.
func TestSweepEmbedCaches_CrashAfterRename(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepMkdir(t, root, "palace/crash/.local")
	sweepFile(t, root, "palace/.local/embed-cache/crash/x.vec", "XXXX")
	keeper(t, root)

	res := mustSweep(t, v)
	if res.Moved != 0 || res.Healed != 1 {
		t.Fatalf("res = %+v, want no move and one heal", res)
	}
	if sweepExists(root, "palace/crash") {
		t.Error("palace/crash must be healed away")
	}
}

// TestSweepEmbedCaches_CrashMidMerge: a legacy directory half drained by a
// killed merge is finished by the next run.
func TestSweepEmbedCaches_CrashMidMerge(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepFile(t, root, "Projects/half/resume.md", "x")
	sweepFile(t, root, "palace/.local/embed-cache/half/done.vec", "DONE")
	sweepFile(t, root, "palace/half/.local/embed-cache/rest1.vec", "RST1")
	sweepFile(t, root, "palace/half/.local/embed-cache/rest2.vec", "RST2")

	res := mustSweep(t, v)
	if res.Merged != 2 || res.Healed != 1 {
		t.Fatalf("res = %+v, want two merged and one heal", res)
	}
	for id, want := range map[string]string{"done": "DONE", "rest1": "RST1", "rest2": "RST2"} {
		if got := readRel(t, root, "palace/.local/embed-cache/half/"+id+".vec"); got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
}

// TestSweepEmbedCaches_NeverRemovesContent: os.Remove refuses a non-empty
// directory, so a palace/<slug>/ holding anything real keeps its directory.
func TestSweepEmbedCaches_NeverRemovesContent(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	keep := map[string]string{
		"drawers": "palace/d/drawers/w/r/drawers.jsonl",
		"kg":      "palace/k/kg/entities.jsonl",
		"surface": "palace/s/.surface",
		"marker":  "palace/m/.local/imported-sessions.jsonl",
	}
	for _, rel := range keep {
		sweepFile(t, root, rel, "content")
		slugDir := strings.Join(strings.Split(rel, "/")[:2], "/")
		sweepFile(t, root, slugDir+"/.local/embed-cache/v.vec", "VVVV")
	}

	res := mustSweep(t, v)
	if res.Healed != 0 || len(res.Errors) != 0 {
		t.Fatalf("res = %+v, want no heal and no errors", res)
	}
	for name, rel := range keep {
		if got := readRel(t, root, rel); got != "content" {
			t.Errorf("%s: %s = %q", name, rel, got)
		}
	}
	if !sweepExists(root, "palace/m/.local/imported-sessions.jsonl") {
		t.Error("the migrate ledger must survive")
	}
}

// TestSweepEmbedCaches_NeverTouchesADirWithoutLocal: a palace/<slug>/ with no
// .local/ is out of scope even when it is empty, so a new store's MkdirAll can
// never be raced.
func TestSweepEmbedCaches_NeverTouchesADirWithoutLocal(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepMkdir(t, root, "palace/fresh")
	sweepMkdir(t, root, "palace/half/drawers/w")

	res := mustSweep(t, v)
	if res.Changed() {
		t.Fatalf("res = %+v, want nothing", res)
	}
	if !sweepExists(root, "palace/fresh") || !sweepExists(root, "palace/half/drawers/w") {
		t.Error("a directory without .local must never be removed")
	}
}

func TestSweepEmbedCaches_SecondRunIsNoOp(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepFile(t, root, "palace/real/drawers/w/r/drawers.jsonl", "{}\n")
	sweepFile(t, root, "palace/real/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/stub/.local/embed-cache/b.vec", "BBBB")

	if first := mustSweep(t, v); !first.Changed() {
		t.Fatalf("first run changed nothing: %+v", first)
	}
	if second := mustSweep(t, v); second.Changed() || len(second.Errors) != 0 {
		t.Fatalf("second run = %+v, want a no-op", second)
	}
}

func TestSweepEmbedCaches_NoPalaceIsNoOp(t *testing.T) {
	res, err := (&Vault{Root: t.TempDir()}).SweepEmbedCaches()
	if err != nil || res.Changed() || len(res.Errors) != 0 {
		t.Fatalf("got %+v, %v; want a clean no-op", res, err)
	}
}

func TestSweepEmbedCaches_UnreadablePalaceIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	sweepMkdir(t, root, "palace")
	if err := os.Chmod(filepath.Join(root, "palace"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "palace"), 0o755) })
	if _, err := (&Vault{Root: root}).SweepEmbedCaches(); err == nil {
		t.Fatal("an unreadable palace/ must be an error")
	}
}

// TestSweepEmbedCaches_ReapsOrphanedCache: a cache whose slug is in neither
// tree is removed, and one whose slug is in either tree survives.
func TestSweepEmbedCaches_ReapsOrphanedCache(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	keeper(t, root)
	sweepFile(t, root, "palace/.local/embed-cache/gone/a.vec", "AAAA")
	sweepFile(t, root, "palace/.local/embed-cache/keep/b.vec", "BBBB")
	sweepFile(t, root, "palace/storeonly/kg/entities.jsonl", "{}")
	sweepFile(t, root, "palace/.local/embed-cache/storeonly/c.vec", "CCCC")
	sweepFile(t, root, "palace/.local/embed-cache/Not A Slug/d.vec", "DDDD")
	sweepFile(t, root, "palace/.local/embed-cache/loose.vec", "EEEE")

	res := mustSweep(t, v)
	if res.Reaped != 1 {
		t.Fatalf("res = %+v, want exactly one reaped directory", res)
	}
	if sweepExists(root, "palace/.local/embed-cache/gone") {
		t.Error("the orphaned cache must be reaped")
	}
	for _, rel := range []string{
		"palace/.local/embed-cache/keep/b.vec",
		"palace/.local/embed-cache/storeonly/c.vec",
		"palace/.local/embed-cache/Not A Slug/d.vec",
		"palace/.local/embed-cache/loose.vec",
	} {
		if !sweepExists(root, rel) {
			t.Errorf("%s must survive", rel)
		}
	}
}

// TestSweepEmbedCaches_ReapGuards pins the ruling's guards: no reaping on an
// enumeration error or on an enumeration of zero projects, and nothing but
// *.vec files is ever removed.
func TestSweepEmbedCaches_ReapGuards(t *testing.T) {
	t.Run("zero_projects", func(t *testing.T) {
		root := t.TempDir()
		sweepFile(t, root, "palace/.local/embed-cache/orphan/a.vec", "AAAA")
		res := mustSweep(t, &Vault{Root: root})
		if res.Reaped != 0 || !sweepExists(root, "palace/.local/embed-cache/orphan/a.vec") {
			t.Fatalf("a vault listing zero projects must reap nothing: %+v", res)
		}
	})

	t.Run("enumeration_error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		root := t.TempDir()
		keeper(t, root)
		sweepFile(t, root, "palace/.local/embed-cache/orphan/a.vec", "AAAA")
		projects := filepath.Join(root, "Projects")
		if err := os.Chmod(projects, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(projects, 0o755) })

		res := mustSweep(t, &Vault{Root: root})
		if res.Reaped != 0 || !sweepExists(root, "palace/.local/embed-cache/orphan/a.vec") {
			t.Fatalf("an enumeration error must reap nothing: %+v", res)
		}
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "reap skipped") {
			t.Errorf("errors = %v, want the skipped reap reported", res.Errors)
		}
	})

	t.Run("unexpected_file_blocks_removal", func(t *testing.T) {
		root := t.TempDir()
		keeper(t, root)
		sweepFile(t, root, "palace/.local/embed-cache/orphan/a.vec", "AAAA")
		sweepFile(t, root, "palace/.local/embed-cache/orphan/notes.txt", "keep me")
		res := mustSweep(t, &Vault{Root: root})
		if res.Reaped != 0 {
			t.Fatalf("a directory holding a non-vector must not be removed: %+v", res)
		}
		if sweepExists(root, "palace/.local/embed-cache/orphan/a.vec") {
			t.Error("the orphaned vector itself is still removed")
		}
		if got := readRel(t, root, "palace/.local/embed-cache/orphan/notes.txt"); got != "keep me" {
			t.Errorf("notes.txt = %q", got)
		}
	})
}

// TestSweepEmbedCaches_NeverFollowsSymlinks: a symlinked palace/<slug>/ can
// point at a real store, and os.Remove unlinks a symlink whatever its target
// holds — and a tracked symlink's deletion would sync. Neither stage acts on
// anything that is not a real directory.
func TestSweepEmbedCaches_NeverFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	keeper(t, root)
	elsewhere := t.TempDir()
	sweepFile(t, elsewhere, "store/drawers/w/r/drawers.jsonl", "{}\n")
	sweepFile(t, elsewhere, "store/.local/embed-cache/x.vec", "XXXX")
	sweepFile(t, elsewhere, "cache/y.vec", "YYYY")
	sweepMkdir(t, root, "palace/.local/embed-cache")
	if err := os.Symlink(filepath.Join(elsewhere, "store"), filepath.Join(root, "palace", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "cache"), filepath.Join(root, "palace", ".local", "embed-cache", "orphanlink")); err != nil {
		t.Fatal(err)
	}
	// A cache for the symlinked store: listProjectDirs drops symlinked project
	// directories, so without the guard this would be reaped on every run.
	sweepFile(t, root, "palace/.local/embed-cache/linked/z.vec", "ZZZZ")
	// A REAL palace/<slug>/ whose .local is a symlink. Following it (Stat
	// instead of Lstat) would move vectors out of the link's target and unlink
	// the .local symlink.
	sweepFile(t, root, "palace/reallink/drawers/w/r/drawers.jsonl", "{}\n")
	sweepMkdir(t, root, "Projects/reallink")
	sweepFile(t, elsewhere, "local/embed-cache/w.vec", "WWWW")
	if err := os.Symlink(filepath.Join(elsewhere, "local"), filepath.Join(root, "palace", "reallink", ".local")); err != nil {
		t.Fatal(err)
	}

	res := mustSweep(t, v)
	if res.Changed() {
		t.Fatalf("res = %+v, want nothing — every candidate here is behind a symlink", res)
	}
	if fi, err := os.Lstat(filepath.Join(root, "palace", "linked")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the palace/linked symlink must survive: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "palace", "reallink", ".local")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the palace/reallink/.local symlink must survive: %v", err)
	}
	if sweepExists(root, "palace/.local/embed-cache/reallink") {
		t.Error("vectors behind a symlinked .local must not be migrated")
	}
	for _, rel := range []string{"store/.local/embed-cache/x.vec", "store/drawers/w/r/drawers.jsonl", "cache/y.vec", "local/embed-cache/w.vec"} {
		if !sweepExists(elsewhere, rel) {
			t.Errorf("symlink target %s was touched", rel)
		}
	}
	if !sweepExists(root, "palace/.local/embed-cache/linked/z.vec") {
		t.Error("the cache of a symlinked store must not be reaped")
	}
}

// TestSweepEmbedCaches_OddShapesAreLeftAlone covers the defensive branches: a
// non-directory where the legacy cache would be, a non-directory where the
// target would be, and a non-regular entry inside a legacy cache.
func TestSweepEmbedCaches_OddShapesAreLeftAlone(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	keeper(t, root)
	sweepFile(t, root, "palace/filecache/.local/embed-cache", "not a dir")
	sweepFile(t, root, "palace/blocked/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/.local/embed-cache/blocked", "a file where the target dir goes")
	sweepMkdir(t, root, "Projects/subdir") // known, so the reaper keeps its cache
	sweepFile(t, root, "palace/subdir/.local/embed-cache/b.vec", "BBBB")
	sweepMkdir(t, root, "palace/subdir/.local/embed-cache/nested")
	sweepFile(t, root, "palace/.local/embed-cache/subdir/c.vec", "CCCC")

	res := mustSweep(t, v)
	if !sweepExists(root, "palace/filecache/.local/embed-cache") {
		t.Error("a file named embed-cache is not ours to touch")
	}
	if !sweepExists(root, "palace/blocked/.local/embed-cache/a.vec") {
		t.Error("a legacy cache whose target is blocked must stay in place")
	}
	if !slices.ContainsFunc(res.Errors, func(e string) bool { return strings.HasPrefix(e, "blocked: ") }) {
		t.Errorf("errors = %v, want the blocked migration reported", res.Errors)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/subdir/b.vec"); got != "BBBB" {
		t.Errorf("b.vec = %q", got)
	}
	if !sweepExists(root, "palace/subdir/.local/embed-cache/nested") {
		t.Error("a non-regular entry in a legacy cache stays, and blocks the heal")
	}
}

// TestSweepEmbedCaches_ConcurrentSweepsConverge runs K sweepers at once with
// writers landing new-layout vectors beside them, under -race. The end state is
// the same as one sweep: every legacy vector at the new path exactly once with
// its bytes, every written vector intact, no legacy cache left, husks gone, real
// stores intact.
func TestSweepEmbedCaches_ConcurrentSweepsConverge(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	want := map[string]string{} // new-layout rel -> bytes
	for _, s := range []string{"husk1", "husk2"} {
		sweepFile(t, root, "Projects/"+s+"/sessions/n.md", "x")
		for i := range 20 {
			rel := fmt.Sprintf("%s/%02d.vec", s, i)
			body := fmt.Sprintf("%s-%02d", s, i)
			sweepFile(t, root, "palace/"+s+"/.local/embed-cache/"+fmt.Sprintf("%02d.vec", i), body)
			want["palace/.local/embed-cache/"+rel] = body
		}
	}
	for _, s := range []string{"real1", "real2"} {
		sweepFile(t, root, "palace/"+s+"/drawers/w/r/drawers.jsonl", "{}\n")
		for i := range 20 {
			body := fmt.Sprintf("%s-%02d", s, i)
			sweepFile(t, root, "palace/"+s+"/.local/embed-cache/"+fmt.Sprintf("%02d.vec", i), body)
			want[fmt.Sprintf("palace/.local/embed-cache/%s/%02d.vec", s, i)] = body
		}
	}
	sweepMkdir(t, root, "palace/crash/.local")

	// No git on PATH, so the tracked-file probe answers "not a repository"
	// without spawning a process. Spawning git would delay every sweeper by a
	// few milliseconds — long enough for the writers to finish first — and the
	// rename-over-an-empty-directory race this test exists to exercise would no
	// longer be reached.
	t.Setenv("PATH", "")

	const sweepers = 6
	var wg sync.WaitGroup
	errs := make([]error, sweepers)
	for i := range sweepers {
		wg.Go(func() { _, errs[i] = v.SweepEmbedCaches() })
	}
	// Writers: exactly what EmbedCache.Put does — MkdirAll, WriteFile, and one
	// retry after re-creating the directory on ENOENT — both for fresh IDs and
	// for IDs a legacy copy also holds, with the same bytes. The retry is what
	// makes every write land: a sweeper's rename can replace the empty
	// directory a writer has just made (rename(2) replaces an empty target), and
	// that writer's open then fails with ENOENT. Every write's error is checked.
	put := func(dir, name string, body []byte) error {
		for attempt := 0; ; attempt++ {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			err := os.WriteFile(filepath.Join(dir, name), body, 0o644)
			if err == nil || attempt == 1 || !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	var putMu sync.Mutex
	var putErrs []error
	for _, s := range []string{"husk1", "real1"} {
		wg.Go(func() {
			dir := filepath.Join(root, "palace", ".local", "embed-cache", s)
			for i := range 10 {
				for name, body := range map[string][]byte{
					fmt.Sprintf("new%02d.vec", i): fmt.Appendf(nil, "new-%02d", i),
					fmt.Sprintf("%02d.vec", i):    fmt.Appendf(nil, "%s-%02d", s, i),
				} {
					if err := put(dir, name, body); err != nil {
						putMu.Lock()
						putErrs = append(putErrs, fmt.Errorf("%s/%s: %w", s, name, err))
						putMu.Unlock()
					}
				}
			}
		})
		for i := range 10 {
			want[fmt.Sprintf("palace/.local/embed-cache/%s/new%02d.vec", s, i)] = fmt.Sprintf("new-%02d", i)
		}
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("sweeper %d: %v", i, err)
		}
	}
	for _, err := range putErrs {
		t.Errorf("a Put-shaped write failed even after its retry: %v", err)
	}
	// One more sweep settles anything a writer's MkdirAll raced into existence.
	if res := mustSweep(t, v); len(res.Errors) != 0 {
		t.Fatalf("settling sweep errors: %v", res.Errors)
	}

	for rel, body := range want {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !bytes.Equal(b, []byte(body)) {
			t.Errorf("%s = %q, want %q", rel, b, body)
		}
	}
	for _, s := range []string{"husk1", "husk2", "crash"} {
		if sweepExists(root, "palace/"+s) {
			t.Errorf("palace/%s must be healed away", s)
		}
	}
	for _, s := range []string{"real1", "real2"} {
		if sweepExists(root, "palace/"+s+"/.local") {
			t.Errorf("palace/%s/.local must be gone", s)
		}
		if !sweepExists(root, "palace/"+s+"/drawers/w/r/drawers.jsonl") {
			t.Errorf("palace/%s lost its drawers", s)
		}
	}
}

// TestSweepEmbedCaches_FailuresLeaveTheLegacyCacheInPlace drives the error
// branches of the migration: each failure is reported per slug and leaves the
// legacy vectors where they were, for the next run and for the
// palace-local-only row.
func TestSweepEmbedCaches_FailuresLeaveTheLegacyCacheInPlace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	chmod := func(t *testing.T, p string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	}
	wantErr := func(t *testing.T, res EmbedCacheSweep, slug, frag string) {
		t.Helper()
		if !slices.ContainsFunc(res.Errors, func(e string) bool {
			return strings.HasPrefix(e, slug+": ") && strings.Contains(e, frag)
		}) {
			t.Errorf("errors = %v, want a %q error mentioning %q", res.Errors, slug, frag)
		}
	}

	t.Run("target_parent_is_a_file", func(t *testing.T) {
		root := t.TempDir()
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		sweepFile(t, root, "palace/.local/embed-cache", "a file where the cache root goes")
		res := mustSweep(t, &Vault{Root: root})
		wantErr(t, res, "p", "")
		if !sweepExists(root, "palace/p/.local/embed-cache/a.vec") {
			t.Error("the legacy vector must stay")
		}
	})

	t.Run("target_unstatable", func(t *testing.T) {
		root := t.TempDir()
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		sweepMkdir(t, root, "palace/.local/embed-cache")
		chmod(t, filepath.Join(root, "palace", ".local", "embed-cache"), 0o000)
		res := mustSweep(t, &Vault{Root: root})
		wantErr(t, res, "p", "stat")
		if !sweepExists(root, "palace/p/.local/embed-cache/a.vec") {
			t.Error("the legacy vector must stay")
		}
	})

	t.Run("legacy_unreadable_during_merge", func(t *testing.T) {
		root := t.TempDir()
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		sweepFile(t, root, "palace/.local/embed-cache/p/b.vec", "BBBB")
		chmod(t, filepath.Join(root, "palace", "p", ".local", "embed-cache"), 0o000)
		res := mustSweep(t, &Vault{Root: root})
		wantErr(t, res, "p", "read legacy embed cache")
	})

	t.Run("target_not_writable", func(t *testing.T) {
		root := t.TempDir()
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		sweepFile(t, root, "palace/.local/embed-cache/p/b.vec", "BBBB")
		chmod(t, filepath.Join(root, "palace", ".local", "embed-cache", "p"), 0o555)
		res := mustSweep(t, &Vault{Root: root})
		wantErr(t, res, "p", "merge a.vec")
		if !sweepExists(root, "palace/p/.local/embed-cache/a.vec") {
			t.Error("a vector that could not be merged must stay")
		}
	})

	t.Run("legacy_not_writable", func(t *testing.T) {
		root := t.TempDir()
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		sweepFile(t, root, "palace/.local/embed-cache/p/b.vec", "BBBB")
		chmod(t, filepath.Join(root, "palace", "p", ".local", "embed-cache"), 0o555)
		res := mustSweep(t, &Vault{Root: root})
		wantErr(t, res, "p", "remove merged legacy vector")
		if got := readRel(t, root, "palace/.local/embed-cache/p/a.vec"); got != "AAAA" {
			t.Errorf("the merge itself must have landed, got %q", got)
		}
	})

	t.Run("heal_blocked_by_permissions", func(t *testing.T) {
		root := t.TempDir()
		sweepMkdir(t, root, "palace/p/.local")
		chmod(t, filepath.Join(root, "palace", "p"), 0o555)
		res := mustSweep(t, &Vault{Root: root})
		wantErr(t, res, "p", "remove .local")
		if !sweepExists(root, "palace/p/.local") {
			t.Error("a heal that failed must leave the directory")
		}
	})
}

// sweepGit runs git hermetically in dir, skipping the test when git is absent.
func sweepGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

// TestSweepEmbedCaches_TrackedLegacyIsLeftAlone: on a vault whose gitignore
// never covered palace/*/.local/, legacy vectors can be tracked. Moving them
// would leave deletions in the working tree that block sync, from a read-only
// search. Such a slug is skipped whole — nothing moved, nothing removed — and
// reported; an untracked slug beside it still migrates.
func TestSweepEmbedCaches_TrackedLegacyIsLeftAlone(t *testing.T) {
	root := t.TempDir()
	keeper(t, root)
	sweepFile(t, root, "palace/committed/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/free/.local/embed-cache/b.vec", "BBBB")
	sweepMkdir(t, root, "Projects/free")
	sweepGit(t, root, "init", "-q")
	sweepGit(t, root, "add", "palace/committed")

	res := mustSweep(t, &Vault{Root: root})
	if !slices.Equal(res.Tracked, []string{"committed"}) {
		t.Fatalf("tracked = %v, want [committed]", res.Tracked)
	}
	if got := readRel(t, root, "palace/committed/.local/embed-cache/a.vec"); got != "AAAA" {
		t.Errorf("a tracked legacy vector was touched: %q", got)
	}
	if sweepExists(root, "palace/.local/embed-cache/committed") {
		t.Error("a tracked legacy cache must not be migrated")
	}
	if got := readRel(t, root, "palace/.local/embed-cache/free/b.vec"); got != "BBBB" {
		t.Errorf("the untracked slug must still migrate, got %q", got)
	}
	if res.Moved != 1 || len(res.Errors) != 0 {
		t.Errorf("res = %+v, want one move and no errors", res)
	}
}

// TestSweepEmbedCaches_OutsideARepositoryNothingIsTracked: a vault that is not
// a git repository tracks nothing, so the migration proceeds; and a repository
// that ignores palace/*/.local/ (the operator's shape) proceeds the same way.
func TestSweepEmbedCaches_OutsideARepositoryNothingIsTracked(t *testing.T) {
	t.Run("not_a_repository", func(t *testing.T) {
		root := t.TempDir()
		sweepMkdir(t, root, "Projects/p")
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		res := mustSweep(t, &Vault{Root: root})
		if res.Moved != 1 || len(res.Tracked) != 0 || len(res.Errors) != 0 {
			t.Fatalf("res = %+v, want the cache moved", res)
		}
	})
	t.Run("repository_that_ignores_local", func(t *testing.T) {
		root := t.TempDir()
		sweepMkdir(t, root, "Projects/p")
		sweepFile(t, root, ".gitignore", "palace/.local/\npalace/*/.local/\n")
		sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
		sweepFile(t, root, "palace/p/drawers/w/r/drawers.jsonl", "{}\n")
		sweepGit(t, root, "init", "-q")
		sweepGit(t, root, "add", "-A")
		res := mustSweep(t, &Vault{Root: root})
		if res.Moved != 1 || len(res.Tracked) != 0 || len(res.Errors) != 0 {
			t.Fatalf("res = %+v, want the cache moved", res)
		}
	})
}

// TestSweepEmbedCaches_GitFailureSkipsTheMigration: inside a repository git
// cannot read, whether anything is tracked is unknown, so stage 1 does nothing
// this run and says why. Reaping, which touches only palace/.local, still runs.
func TestSweepEmbedCaches_GitFailureSkipsTheMigration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	keeper(t, root)
	sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/.local/embed-cache/gone/z.vec", "ZZZZ")
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /nonexistent/vp-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustSweep(t, &Vault{Root: root})
	if !sweepExists(root, "palace/p/.local/embed-cache/a.vec") || res.Moved != 0 || res.Healed != 0 {
		t.Fatalf("res = %+v; the legacy cache must stay when git cannot answer", res)
	}
	if !slices.ContainsFunc(res.Errors, func(e string) bool { return strings.HasPrefix(e, "migration skipped this run") }) {
		t.Errorf("errors = %v, want the skipped migration reported", res.Errors)
	}
	if res.Reaped != 1 {
		t.Errorf("reaping is independent of git and must still run: %+v", res)
	}
}

// withSeam replaces a test seam for one test.
func withSeam[T any](t *testing.T, seam *T, v T) {
	t.Helper()
	old := *seam
	*seam = v
	t.Cleanup(func() { *seam = old })
}

// TestSweepEmbedCaches_CopyFallbackWhenLinkAndRenameAreRefused: where
// palace/.local is its own mount (EXDEV) or the filesystem has no hard links,
// the sweep copies each vector instead, so the husk still heals rather than
// warning on every start.
func TestSweepEmbedCaches_CopyFallbackWhenLinkAndRenameAreRefused(t *testing.T) {
	refused := &os.LinkError{Op: "link", Err: errors.New("cross-device link")}
	withSeam(t, &sweepRenameDir, func(string, string) error { return refused })
	withSeam(t, &sweepLink, func(string, string) error { return refused })

	root := t.TempDir()
	sweepMkdir(t, root, "Projects/p")
	sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/p/.local/embed-cache/b.vec", "BBBB")
	sweepFile(t, root, "palace/.local/embed-cache/q/b.vec", "NEW!")
	sweepMkdir(t, root, "Projects/q")
	sweepFile(t, root, "palace/q/.local/embed-cache/b.vec", "OLD!")

	res := mustSweep(t, &Vault{Root: root})
	if len(res.Errors) != 0 {
		t.Fatalf("errors = %v", res.Errors)
	}
	if res.Merged != 2 || res.Dropped != 1 || res.Healed != 2 {
		t.Fatalf("res = %+v, want 2 copied, 1 dropped, 2 healed", res)
	}
	for rel, want := range map[string]string{
		"palace/.local/embed-cache/p/a.vec": "AAAA",
		"palace/.local/embed-cache/p/b.vec": "BBBB",
		"palace/.local/embed-cache/q/b.vec": "NEW!",
	} {
		if got := readRel(t, root, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "palace", ".local", "embed-cache", "*", ".sweep-*")); len(matches) != 0 {
		t.Errorf("copy temporaries left behind: %v", matches)
	}
	// A copied vector gets the mode Put writes, not CreateTemp's 0600.
	for _, rel := range []string{"palace/.local/embed-cache/p/a.vec", "palace/.local/embed-cache/p/b.vec"} {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, want 0644", rel, fi.Mode().Perm())
		}
	}
}

// TestSweepEmbedCaches_OneFailureDoesNotStopTheMerge: a vector that can be
// neither linked nor copied is reported, and the rest of the slug still moves.
func TestSweepEmbedCaches_OneFailureDoesNotStopTheMerge(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	withSeam(t, &sweepLink, func(src, dst string) error {
		if filepath.Base(src) == "bad.vec" {
			return &os.LinkError{Op: "link", Old: src, New: dst, Err: errors.New("not supported")}
		}
		return os.Link(src, dst)
	})
	root := t.TempDir()
	sweepMkdir(t, root, "Projects/p")
	sweepFile(t, root, "palace/p/.local/embed-cache/bad.vec", "BAD!")
	sweepFile(t, root, "palace/p/.local/embed-cache/good.vec", "GOOD")
	sweepMkdir(t, root, "palace/.local/embed-cache/p")
	bad := filepath.Join(root, "palace", "p", ".local", "embed-cache", "bad.vec")
	if err := os.Chmod(bad, 0o000); err != nil { // the copy cannot read it either
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	res := mustSweep(t, &Vault{Root: root})
	if got := readRel(t, root, "palace/.local/embed-cache/p/good.vec"); got != "GOOD" {
		t.Errorf("good.vec = %q — one failure stopped the merge", got)
	}
	if !slices.ContainsFunc(res.Errors, func(e string) bool { return strings.Contains(e, "merge bad.vec") }) {
		t.Errorf("errors = %v, want bad.vec reported", res.Errors)
	}
	if !sweepExists(root, "palace/p/.local/embed-cache/bad.vec") {
		t.Error("a vector that could not be merged must stay")
	}
}

// TestSweepEmbedCaches_TargetVanishingMidMergeIsRecreated drives the branch a
// concurrent reap reaches: the target directory disappears between the check
// and the link, and the merge re-creates it and links again.
func TestSweepEmbedCaches_TargetVanishingMidMergeIsRecreated(t *testing.T) {
	root := t.TempDir()
	sweepMkdir(t, root, "Projects/p")
	sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
	target := filepath.Join(root, "palace", ".local", "embed-cache", "p")
	sweepMkdir(t, root, "palace/.local/embed-cache/p")
	first := true
	withSeam(t, &sweepLink, func(src, dst string) error {
		if first {
			first = false
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
		}
		return os.Link(src, dst)
	})

	res := mustSweep(t, &Vault{Root: root})
	if res.Merged != 1 || len(res.Errors) != 0 {
		t.Fatalf("res = %+v, want the vector merged after re-creating the target", res)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/p/a.vec"); got != "AAAA" {
		t.Errorf("a.vec = %q", got)
	}
}

// TestSweepEmbedCaches_MergeLeavesForeignFilesAndSymlinkedTargets: the merge
// moves regular *.vec files and nothing else, and never follows a target that
// is a symlink.
func TestSweepEmbedCaches_MergeLeavesForeignFilesAndSymlinkedTargets(t *testing.T) {
	root := t.TempDir()
	keeper(t, root)
	sweepMkdir(t, root, "Projects/p")
	sweepFile(t, root, "palace/p/.local/embed-cache/a.vec", "AAAA")
	sweepFile(t, root, "palace/p/.local/embed-cache/notes.txt", "mine")
	sweepFile(t, root, "palace/.local/embed-cache/p/notes.txt", "theirs") // exists: forces the merge

	sweepMkdir(t, root, "Projects/s")
	sweepFile(t, root, "palace/s/.local/embed-cache/b.vec", "BBBB")
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "palace", ".local", "embed-cache", "s")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := mustSweep(t, &Vault{Root: root})
	if got := readRel(t, root, "palace/.local/embed-cache/p/a.vec"); got != "AAAA" {
		t.Errorf("a.vec = %q", got)
	}
	if got := readRel(t, root, "palace/p/.local/embed-cache/notes.txt"); got != "mine" {
		t.Errorf("a non-vector in the legacy cache was touched: %q", got)
	}
	if got := readRel(t, root, "palace/.local/embed-cache/p/notes.txt"); got != "theirs" {
		t.Errorf("a non-vector in the target was touched: %q", got)
	}
	if !sweepExists(root, "palace/p/.local/embed-cache") {
		t.Error("a legacy cache holding a non-vector keeps its directory")
	}
	if !sweepExists(root, "palace/s/.local/embed-cache/b.vec") {
		t.Error("a legacy cache whose target is a symlink must stay")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the merge followed a symlinked target into %v", entries)
	}
	if !slices.ContainsFunc(res.Errors, func(e string) bool { return strings.HasPrefix(e, "s: ") }) {
		t.Errorf("errors = %v, want the symlinked target reported", res.Errors)
	}
}

// TestSweepEmbedCaches_ReapKeepsAProjectBornDuringTheSweep pins the invariant
// the reap's order used to carry: a project and its cache appear just before the
// cache directory is read, so the reap sees the cache — and must keep it,
// because the project exists by the time the entry is judged. With the per-slug
// existence check this no longer depends on reading before listing; swapping
// the two reads is an equivalent mutant now, and this test pins the property
// itself rather than the order.
func TestSweepEmbedCaches_ReapKeepsAProjectBornDuringTheSweep(t *testing.T) {
	root := t.TempDir()
	keeper(t, root)
	sweepFile(t, root, "palace/.local/embed-cache/other/o.vec", "OOOO")
	withSeam(t, &sweepReadCacheDir, func(dir string) ([]os.DirEntry, error) {
		sweepMkdir(t, root, "Projects/born")
		sweepFile(t, root, "palace/.local/embed-cache/born/b.vec", "BBBB")
		return os.ReadDir(dir)
	})

	mustSweep(t, &Vault{Root: root})
	if !sweepExists(root, "palace/.local/embed-cache/born/b.vec") {
		t.Error("a project that existed when the listing ran lost its cache")
	}
	if sweepExists(root, "palace/.local/embed-cache/other") {
		t.Error("precondition: an orphan in the same pass must still be reaped")
	}
}

// TestSweepEmbedCaches_ReapNeedsAProjectsTree: an absent (or dangling)
// Projects/ reads as zero notes-only projects, so one palace store alone must
// not let the reaper run.
func TestSweepEmbedCaches_ReapNeedsAProjectsTree(t *testing.T) {
	root := t.TempDir()
	sweepFile(t, root, "palace/store/kg/entities.jsonl", "{}")
	sweepFile(t, root, "palace/.local/embed-cache/notesonly/n.vec", "NNNN")
	if err := os.Symlink(filepath.Join(root, "unmounted"), filepath.Join(root, "Projects")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := mustSweep(t, &Vault{Root: root})
	if res.Reaped != 0 || !sweepExists(root, "palace/.local/embed-cache/notesonly/n.vec") {
		t.Fatalf("res = %+v; nothing may be reaped without a Projects/ tree", res)
	}
}

// TestSweepEmbedCaches_ReapKeepsAnythingThatExists: a slug is kept whenever
// palace/<slug> or Projects/<slug> exists at all, even as something the
// enumerators drop (here a dangling symlink).
func TestSweepEmbedCaches_ReapKeepsAnythingThatExists(t *testing.T) {
	root := t.TempDir()
	keeper(t, root)
	sweepFile(t, root, "palace/.local/embed-cache/dangling/d.vec", "DDDD")
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "Projects", "dangling")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if res := mustSweep(t, &Vault{Root: root}); res.Reaped != 0 {
		t.Fatalf("res = %+v; the cache of a slug that exists as a symlink must be kept", res)
	}
}

// TestSweepEmbedCaches_RemovesOnlyStaleCopyTemps: a copy temporary a crash left
// behind is collected by a later sweep, and one a concurrent sweep may be
// writing right now is not. Nothing else that merely looks similar is touched.
func TestSweepEmbedCaches_RemovesOnlyStaleCopyTemps(t *testing.T) {
	root := t.TempDir()
	keeper(t, root)
	sweepMkdir(t, root, "Projects/p")
	stale := "palace/.local/embed-cache/p/.sweep-111.tmp"
	fresh := "palace/.local/embed-cache/p/.sweep-222.tmp"
	other := "palace/.local/embed-cache/p/.sweep-333.keep"
	for _, rel := range []string{stale, fresh, other} {
		sweepFile(t, root, rel, "partial")
	}
	sweepFile(t, root, "palace/.local/embed-cache/p/v.vec", "VVVV")
	long := time.Now().Add(-time.Hour)
	for _, rel := range []string{stale, other} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), long, long); err != nil {
			t.Fatal(err)
		}
	}
	// An orphaned cache whose only other content is a stale temporary: once the
	// temporary is collected, the reap can remove the directory.
	sweepFile(t, root, "palace/.local/embed-cache/gone/.sweep-444.tmp", "partial")
	sweepFile(t, root, "palace/.local/embed-cache/gone/g.vec", "GGGG")
	if err := os.Chtimes(filepath.Join(root, "palace", ".local", "embed-cache", "gone", ".sweep-444.tmp"), long, long); err != nil {
		t.Fatal(err)
	}

	res := mustSweep(t, &Vault{Root: root})
	if sweepExists(root, stale) {
		t.Error("a stale copy temporary must be collected")
	}
	if !sweepExists(root, fresh) {
		t.Error("a fresh copy temporary may belong to a running sweep and must stay")
	}
	if !sweepExists(root, other) || !sweepExists(root, "palace/.local/embed-cache/p/v.vec") {
		t.Error("only files named like a copy temporary may be collected")
	}
	if sweepExists(root, "palace/.local/embed-cache/gone") || res.Reaped != 1 {
		t.Errorf("res = %+v; the orphan held only by a stale temporary must be reaped", res)
	}
}
