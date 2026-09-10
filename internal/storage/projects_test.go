// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// newDivergentVault builds a vault whose two trees DISAGREE, which is the whole
// point: palace-only, Projects-only, and both-trees projects all coexist in the
// live vault, and an enumerator that reads one tree cannot see the other.
func newDivergentVault(t *testing.T) *Vault {
	t.Helper()
	root := t.TempDir()

	mk := func(parts ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(append([]string{root}, parts...)...), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A palace/ store holds at least one regular file outside .local/ — a bare
	// directory is not one (see the presence rule in projects.go).
	seed := func(parts ...string) {
		t.Helper()
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// In BOTH trees.
	seed("palace", "vibe-palace", "drawers", "w", "r", "drawers.jsonl")
	mk("Projects", "vibe-palace")

	// palace/ only — indexed, but no history was ever written.
	seed("palace", "mandelbulb", "kg", "entities.jsonl")

	// Projects/ only — history captured, never drawer-indexed. This is the
	// class a palace/-only enumerator is blind to (live vault: 73 session notes).
	mk("Projects", "rusty-can", "sessions")

	// Noise that must be filtered identically in both trees.
	mk("palace", ".local")
	mk("Projects", ".local")
	mk("palace", "Not A Slug")
	if err := os.WriteFile(filepath.Join(root, "palace", "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	return &Vault{Root: root}
}

func TestListAllProjects_UnionOfBothTrees(t *testing.T) {
	v := newDivergentVault(t)

	got, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}

	want := []ProjectPresence{
		{Slug: "mandelbulb", InPalace: true, InProjects: false},
		{Slug: "rusty-can", InPalace: false, InProjects: true},
		{Slug: "vibe-palace", InPalace: true, InProjects: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListAllProjects() = %+v\nwant %+v", got, want)
	}
}

// TestListAllProjects_SeesWhatAPalaceOnlyEnumeratorCannot is the regression that
// matters: it pins the exact blind spot REVERSAL 1 found. A Projects/-only project
// is invisible to a palace/-only enumeration (the since-deleted ListProjects), and a
// vault-global caller that reaches for one audits a corpus it never looked at.
func TestListAllProjects_SeesWhatAPalaceOnlyEnumeratorCannot(t *testing.T) {
	v := newDivergentVault(t)

	palaceOnly, err := listPalaceStores(filepath.Join(v.Root, "palace"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(palaceOnly, "rusty-can") {
		t.Fatal("premise broken: a palace/-only enumeration is supposed to be blind to Projects/-only projects")
	}

	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(all, func(p ProjectPresence) bool { return p.Slug == "rusty-can" }) {
		t.Fatal("ListAllProjects missed a Projects/-only project — it is blind to exactly the " +
			"notes the union enumerator exists to reach")
	}
}

// TestListAllProjects_PalaceHalfIsListPalaceStores pins the palace/ half of the
// union to ONE filter. If two paths disagreed about what counts as a project (a
// slug rule applied in one tree and not the other), the union would report drift
// that is really just an inconsistent filter -- an auditor inventing findings is
// worse than one that misses them, because it trains you to wave off the real ones.
func TestListAllProjects_PalaceHalfIsListPalaceStores(t *testing.T) {
	v := newDivergentVault(t)

	fromListProjects, err := listPalaceStores(filepath.Join(v.Root, "palace"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(fromListProjects)

	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	var fromUnion []string
	for _, p := range all {
		if p.InPalace {
			fromUnion = append(fromUnion, p.Slug)
		}
	}

	if !slices.Equal(fromListProjects, fromUnion) {
		t.Fatalf("the two paths disagree about palace/: listPalaceStores=%v union(InPalace)=%v",
			fromListProjects, fromUnion)
	}

	// And the noise is gone from both: .local, a loose file, and an invalid slug.
	for _, bad := range []string{".local", "loose.txt", "Not A Slug"} {
		if slices.Contains(fromUnion, bad) {
			t.Errorf("%q must not be enumerated as a project", bad)
		}
	}
}

func TestListAllProjects_Complete(t *testing.T) {
	cases := []struct {
		name string
		p    ProjectPresence
		want bool
	}{
		{"both trees", ProjectPresence{InPalace: true, InProjects: true}, true},
		{"palace only", ProjectPresence{InPalace: true}, false},
		{"projects only", ProjectPresence{InProjects: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.Complete(); got != c.want {
				t.Fatalf("Complete() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestListAllProjects_MissingTreesAreNotErrors: an absent tree contributes
// nothing. A fresh vault with no palace/ yet is not a broken vault.
func TestListAllProjects_MissingTreesAreNotErrors(t *testing.T) {
	v := &Vault{Root: t.TempDir()}

	got, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("empty vault should enumerate cleanly, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty vault enumerated %+v, want none", got)
	}
}

// TestListAllProjects_LiveVaultCanary runs the enumerator against a REAL vault
// and reports what a palace/-only enumeration cannot see. It is opt-in:
//
//	VP_LIVE_VAULT=~/obsidian/vibe-palace-vault go test ./internal/storage/ -run LiveVaultCanary -v -count=1
//
// -count=1 matters: the vault is OUTSIDE the module, so `go test` cannot see its
// contents change and will serve a CACHED verdict for a vault it never re-read.
//
// It exists because every bug this enumerator was built to fix survived a green
// unit suite. Fixtures are built by the same person holding the same wrong
// mental model; the live vault is not. It asserts the INVARIANT (the union is a
// superset, and it is strictly larger whenever the trees diverge) and PRINTS the
// counts rather than pinning them -- a hard-coded census rots by the next run,
// so record the grep, never the count.
func TestListAllProjects_LiveVaultCanary(t *testing.T) {
	root := os.Getenv("VP_LIVE_VAULT")
	if root == "" {
		t.Skip("set VP_LIVE_VAULT=<vault root> to run the live canary")
	}
	v := &Vault{Root: root}

	palaceOnly, err := listPalaceStores(filepath.Join(root, "palace"))
	if err != nil {
		t.Fatalf("listPalaceStores: %v", err)
	}
	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}

	if len(all) < len(palaceOnly) {
		t.Fatalf("the union (%d) is smaller than palace/ alone (%d) — it is not a union",
			len(all), len(palaceOnly))
	}

	var invisible, phantom int
	for _, p := range all {
		switch {
		case !p.InPalace:
			invisible++
			notes, _ := filepath.Glob(filepath.Join(root, "Projects", p.Slug, "sessions", "*.md"))
			t.Logf("INVISIBLE to palace/ alone: %-20s %d session notes", p.Slug, len(notes))
		case !p.InProjects:
			phantom++
			t.Logf("phantom (palace/ only, no history): %s", p.Slug)
		}
	}
	t.Logf("union=%d  palace-only-enumerator=%d  invisible=%d  phantom=%d",
		len(all), len(palaceOnly), invisible, phantom)

	// Every palace/ store must still be in the union. A union that
	// drops a project is worse than the blind spot it replaces.
	for _, s := range palaceOnly {
		if !slices.ContainsFunc(all, func(p ProjectPresence) bool { return p.Slug == s }) {
			t.Errorf("union dropped %q, which palace/ holds", s)
		}
	}
}

// TestListAllProjects_UnreadableTreeIsAnError: the auditor's own invariant --
// "I could not look" is NOT "there was nothing there." An existing but
// unreadable tree must fail loudly rather than silently enumerate a subset.
func TestListAllProjects_UnreadableTreeIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	palaceDir := filepath.Join(root, "palace")
	if err := os.MkdirAll(palaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(palaceDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(palaceDir, 0o755) })

	v := &Vault{Root: root}
	if _, err := v.ListAllProjects(); err == nil {
		t.Fatal("an unreadable palace/ must be an error, not an empty enumeration — " +
			"silently returning a subset is how an audit reports a clean bill of health " +
			"for a corpus it could not read")
	}
}

// --- presence rule ---

// writeTree creates each rel path under root: a trailing "/" makes a directory,
// anything else a regular file with the given body.
func writeTree(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if rel[len(rel)-1] == '/' {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPresenceRule_PalaceStoreNeedsAFileOutsideLocal pins which palace/<slug>/
// shapes are stores. Every excluded shape is one git cannot carry to another
// host — ignored .local/ content, empty directories, empty subtrees — so
// counting it made the enumeration host-dependent.
func TestPresenceRule_PalaceStoreNeedsAFileOutsideLocal(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		// excluded
		"palace/husk/.local/embed-cache/abc.vec",
		"palace/marker/.local/imported-sessions.jsonl",
		"palace/bare/",
		"palace/hollow/drawers/w/r/",
		"palace/hollowkg/kg/triples/",
		"palace/mixed/.local/embed-cache/x.vec",
		"palace/mixed/drawers/",
		// included
		"palace/kgonly/kg/entities.jsonl",
		"palace/stamped/.surface",
		"palace/zerolen/drawers/w/r/drawers.jsonl",
		"palace/withlocal/.local/embed-cache/y.vec",
		"palace/withlocal/drawers/w/r/drawers.jsonl",
		// a nested .local is NOT the top-level one, so its file counts
		"palace/nested/drawers/.local/f",
		// never a project
		"palace/.local/embed-cache/p/z.vec",
	)
	v := &Vault{Root: root}

	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range all {
		if p.InPalace {
			got = append(got, p.Slug)
		}
	}
	want := []string{"kgonly", "nested", "stamped", "withlocal", "zerolen"}
	if !slices.Equal(got, want) {
		t.Fatalf("stores = %v, want %v", got, want)
	}

	lp, err := listPalaceStores(filepath.Join(root, "palace"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(lp)
	if !slices.Equal(lp, want) {
		t.Fatalf("listPalaceStores = %v, want the same stores as ListAllProjects %v", lp, want)
	}
}

// TestPresenceRule_UnreadableDirCountsAsStore: a directory the predicate cannot
// walk is counted, never dropped. "I could not look" is not "absent".
func TestPresenceRule_UnreadableDirCountsAsStore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	writeTree(t, root, "palace/sealed/drawers/w/r/drawers.jsonl")
	dir := filepath.Join(root, "palace", "sealed")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	v := &Vault{Root: root}
	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("one unreadable directory must not fail the enumeration: %v", err)
	}
	if len(all) != 1 || all[0].Slug != "sealed" || !all[0].InPalace {
		t.Fatalf("an unreadable palace/ directory must count as a store, got %+v", all)
	}
	ns, err := v.PalaceNonStores()
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 0 {
		t.Fatalf("an undecidable directory is a store, so it is not a non-store: %+v", ns)
	}
}

// TestPalaceNonStores_IsTheComplement: PalaceNonStores and ListAllProjects'
// InPalace partition the valid-slug directories of palace/ between them, and
// each non-store carries a description of what is actually there.
func TestPalaceNonStores_IsTheComplement(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"palace/husk/.local/embed-cache/a.vec",
		"palace/husk/.local/embed-cache/b.vec",
		"palace/husk/.local/imported-sessions.jsonl",
		"palace/bare/",
		"palace/hollow/drawers/w/",
		"palace/emptylocal/.local/",
		"palace/real/.surface",
		"palace/Not A Slug/.local/x",
		"palace/.local/embed-cache/q/z.vec",
		"Projects/husk/",
	)
	v := &Vault{Root: root}

	ns, err := v.PalaceNonStores()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]PalaceNonStore{}
	var slugs []string
	for _, n := range ns {
		by[n.Slug] = n
		slugs = append(slugs, n.Slug)
	}
	if want := []string{"bare", "emptylocal", "hollow", "husk"}; !slices.Equal(slugs, want) {
		t.Fatalf("non-stores = %v, want %v", slugs, want)
	}

	husk := by["husk"]
	if !husk.HasLocal || husk.EmptySubtree || !husk.InProjects {
		t.Errorf("husk = %+v, want HasLocal, no empty subtree, InProjects", husk)
	}
	wantLocal := []LocalEntry{
		{Name: "embed-cache", IsDir: true, Files: 2},
		{Name: "imported-sessions.jsonl"},
	}
	if !slices.Equal(husk.Local, wantLocal) {
		t.Errorf("husk.Local = %+v, want %+v", husk.Local, wantLocal)
	}
	if b := by["bare"]; b.HasLocal || b.EmptySubtree || b.InProjects {
		t.Errorf("bare = %+v, want nothing at all", b)
	}
	if h := by["hollow"]; h.HasLocal || !h.EmptySubtree {
		t.Errorf("hollow = %+v, want an empty subtree and no .local", h)
	}
	if e := by["emptylocal"]; !e.HasLocal || len(e.Local) != 0 {
		t.Errorf("emptylocal = %+v, want an empty .local", e)
	}

	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if _, dup := by[p.Slug]; dup && p.InPalace {
			t.Errorf("%s is both a store and a non-store", p.Slug)
		}
	}
	if !slices.ContainsFunc(all, func(p ProjectPresence) bool { return p.Slug == "real" && p.InPalace }) {
		t.Error("the real store must be enumerated")
	}
}

// TestPalaceNonStores_NoPalaceTree: a vault with no palace/ has nothing to
// report, and that is not an error.
func TestPalaceNonStores_NoPalaceTree(t *testing.T) {
	ns, err := (&Vault{Root: t.TempDir()}).PalaceNonStores()
	if err != nil || len(ns) != 0 {
		t.Fatalf("PalaceNonStores on an empty vault = %+v, %v; want nothing, nil", ns, err)
	}
}

// TestPalaceNonStores_UnreadablePalaceIsAnError mirrors ListAllProjects.
func TestPalaceNonStores_UnreadablePalaceIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := (&Vault{Root: root}).PalaceNonStores(); err == nil {
		t.Fatal("an unreadable palace/ must be an error")
	}
}

// TestPalaceNonStores_UnreadableLocalSubtree: a .local/ subdirectory that
// cannot be counted is reported as -1, not as empty.
func TestPalaceNonStores_UnreadableLocalSubtree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	writeTree(t, root, "palace/husk/.local/embed-cache/sub/a.vec")
	sub := filepath.Join(root, "palace", "husk", ".local", "embed-cache", "sub")
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	ns, err := (&Vault{Root: root}).PalaceNonStores()
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || len(ns[0].Local) != 1 || ns[0].Local[0].Files != -1 {
		t.Fatalf("got %+v, want one husk whose embed-cache count is -1", ns)
	}
}

// tpGit runs git hermetically in dir, skipping the test when git is absent.
func tpGit(t *testing.T, dir string, args ...string) {
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

// TestTrackedPalaceLocalFiles counts tracked files under each slug's top-level
// .local/ and nothing else: not an untracked one, not palace/.local, not a
// nested .local, not a file merely named like it.
func TestTrackedPalaceLocalFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"palace/a/.local/embed-cache/x.vec",
		"palace/a/.local/embed-cache/y.vec",
		"palace/b/.local/imported-sessions.jsonl",
		"palace/c/.local/untracked.vec",
		"palace/.local/embed-cache/a/z.vec",
		"palace/d/drawers/.local/n",
		"palace/e/.localx",
	)
	tpGit(t, root, "init", "-q")
	tpGit(t, root, "add", "palace/a", "palace/b", "palace/.local", "palace/d", "palace/e")

	got, err := (&Vault{Root: root}).TrackedPalaceLocalFiles()
	if err != nil {
		t.Fatalf("TrackedPalaceLocalFiles: %v", err)
	}
	if len(got) != 2 || got["a"] != 2 || got["b"] != 1 {
		t.Fatalf("tracked = %v, want a:2 b:1", got)
	}
}

// TestTrackedPalaceLocalFiles_NotARepository: outside any repository nothing
// is tracked, and that is an answer, not an error.
func TestTrackedPalaceLocalFiles_NotARepository(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "palace/a/.local/embed-cache/x.vec")
	got, err := (&Vault{Root: root}).TrackedPalaceLocalFiles()
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want an empty map and no error", got, err)
	}
}

// TestTrackedPalaceLocalFiles_BrokenRepositoryIsAnError: a .git that git cannot
// read is a failure to look, never "nothing tracked".
func TestTrackedPalaceLocalFiles_BrokenRepositoryIsAnError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	writeTree(t, root, "palace/a/.local/embed-cache/x.vec")
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /nonexistent/vp-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Vault{Root: root}).TrackedPalaceLocalFiles(); err == nil {
		t.Fatal("a broken repository must be an error")
	}
}

// TestTrackedPalaceLocalFiles_IgnoresInheritedRepoEnv: a vp spawned from a git
// hook, or from a shell exporting GIT_DIR, inherits variables that point git at
// another repository and index. The ls-files call must still answer for the
// vault it runs in.
func TestTrackedPalaceLocalFiles_IgnoresInheritedRepoEnv(t *testing.T) {
	other := t.TempDir()
	writeTree(t, other, "palace/a/.local/embed-cache/x.vec")
	tpGit(t, other, "init", "-q")
	tpGit(t, other, "add", "palace")

	vault := t.TempDir()
	writeTree(t, vault, "palace/b/.local/embed-cache/y.vec")
	tpGit(t, vault, "init", "-q")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))

	got, err := (&Vault{Root: vault}).TrackedPalaceLocalFiles()
	if err != nil {
		t.Fatalf("TrackedPalaceLocalFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("tracked = %v; the inherited GIT_DIR answered for another repository", got)
	}
}
