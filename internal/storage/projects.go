// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// A vault records a project in TWO independent trees, and nothing keeps them in
// step:
//
//   - palace/<slug>/   — the drawer + knowledge-graph store (what capture indexes)
//   - Projects/<slug>/ — sessions, tasks, resume (the project's history)
//
// A project can exist in either without the other, and in the live vault many
// do: a project whose sessions were captured before it was ever drawer-indexed
// has no palace/ dir, and a palace/ dir survives a project whose history was
// never written. Neither tree is a superset of the other, so NEITHER ALONE
// ENUMERATES THE VAULT.
//
// ListAllProjects is therefore the only enumerator. A palace/-only one
// (ListProjects) existed for search, and every caller that reached for it to ask
// "what is in this vault?" got a subset and could not tell — which is how a
// vault-global audit reports a clean bill of health for a corpus it never looked
// at. Cross-project search was its last caller; it now enumerates the union too,
// because two of search's three corpora live under Projects/.
//
// # What counts as a palace store
//
// A palace/<slug>/ directory is a STORE only if it holds at least one regular
// file outside its top-level .local/. palace/ is enumerated only through the
// one predicate that says so (listPalaceStores), so no caller can disagree.
//
// The reason is git. .local/ is machine-local state the vault's gitignore
// keeps out of the repository, and git cannot carry an empty directory at all.
// A palace/<slug>/ holding only those is therefore a fact about THIS host: a
// pull that deletes a project removes every tracked file and leaves the ignored
// .local/ — and the directory around it — behind, on every host that ever
// cached anything for it. Counting such a directory as a store made the
// enumeration, and every audit built on it, give different answers on
// different hosts for the same commit. The complement is not hidden: it is
// what PalaceNonStores returns and what `vp check --check palace-local-only`
// reports.

// ProjectPresence is one project and the trees it actually appears in. The
// asymmetry is not an error to be resolved in favour of one side — it is a
// finding: a project in one tree and not the other is real vault drift, and the
// union enumerator is the only thing positioned to notice it.
type ProjectPresence struct {
	Slug       string
	InPalace   bool // palace/<slug>/ is a store — it holds a file outside .local/
	InProjects bool // Projects/<slug>/ exists — sessions, tasks, resume
}

// Complete reports whether the project appears in BOTH trees. A false result is
// the drift; the caller decides whether to report or repair it.
func (p ProjectPresence) Complete() bool { return p.InPalace && p.InProjects }

// ListAllProjects enumerates the UNION of palace/ and Projects/, sorted by slug,
// recording which trees each project appears in.
//
// This is the enumerator a vault-global caller wants. A missing tree is not an
// error — a vault with no palace/ at all is simply one where nothing has been
// indexed yet — so an absent directory contributes nothing rather than failing
// the sweep. An unreadable one that EXISTS is a real error and is reported: "I
// could not look" is not "there was nothing there."
func (v *Vault) ListAllProjects() ([]ProjectPresence, error) {
	palace, err := listPalaceStores(filepath.Join(v.Root, "palace"))
	if err != nil {
		return nil, fmt.Errorf("list palace projects: %w", err)
	}
	projects, err := listProjectDirs(filepath.Join(v.Root, "Projects"))
	if err != nil {
		return nil, fmt.Errorf("list Projects entries: %w", err)
	}

	byslug := make(map[string]ProjectPresence, len(palace)+len(projects))
	for _, s := range palace {
		p := byslug[s]
		p.Slug, p.InPalace = s, true
		byslug[s] = p
	}
	for _, s := range projects {
		p := byslug[s]
		p.Slug, p.InProjects = s, true
		byslug[s] = p
	}

	out := make([]ProjectPresence, 0, len(byslug))
	for _, p := range byslug {
		out = append(out, p)
	}
	// Sorted so the enumeration is deterministic: an audit report that reorders
	// its own rows between runs is not diffable, and week-over-week drift is the
	// point of writing it down.
	slices.SortFunc(out, func(a, b ProjectPresence) int {
		switch {
		case a.Slug < b.Slug:
			return -1
		case a.Slug > b.Slug:
			return 1
		}
		return 0
	})
	return out, nil
}

// listProjectDirs returns the valid project slugs directly under dir. An absent
// dir yields nothing; an existing-but-unreadable one is an error.
//
// The filter is the naming rule both trees share — directories only, .local
// excluded (machine-local state, not a project), and the name must be a valid
// slug — so the two trees cannot disagree about what counts as a project name.
// If they could, the union would report drift that is really just a naming rule
// applied unevenly. palace/ is never enumerated through this alone: it goes
// through listPalaceStores, which adds the presence rule on top.
func listProjectDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var slugs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".local" {
			continue
		}
		if slug.Validate(name) != nil {
			continue
		}
		slugs = append(slugs, name)
	}
	return slugs, nil
}

// listPalaceStores returns the valid slugs under palaceDir that are palace
// STORES: listProjectDirs' naming rule, then the presence rule of
// palaceDirHoldsFileOutsideLocal. It is the only way palace/ is enumerated, so
// every caller applies one rule and none can disagree with another.
//
// A directory the predicate cannot decide COUNTS AS A STORE. "I could not look"
// is never allowed to mean "absent": a store dropped from the enumeration
// silently leaves every audit and cross-project search short by one, whereas a
// counted one reaches callers that already report an unreadable store as
// unknown (palace-store-drawers' countDrawerRecords). One unreadable directory
// also must not fail the whole enumeration.
func listPalaceStores(palaceDir string) ([]string, error) {
	dirs, err := listProjectDirs(palaceDir)
	if err != nil {
		return nil, err
	}
	stores := dirs[:0]
	for _, s := range dirs {
		holds, err := palaceDirHoldsFileOutsideLocal(filepath.Join(palaceDir, s))
		if err != nil || holds {
			stores = append(stores, s)
		}
	}
	return stores, nil
}

// palaceDirHoldsFileOutsideLocal reports whether dir holds at least one regular
// file anywhere beneath it, outside its top-level .local/. Empty directories and
// empty subtrees do not count — git cannot carry them — and neither does
// anything under .local/, which the vault's gitignore keeps machine-local.
//
// It is cheap on a real store: the walk stops at the first regular file, and a
// store reaches .surface or its first drawers.jsonl within a few entries.
// Symlinks are not regular files and are not followed.
func palaceDirHoldsFileOutsideLocal(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".local" && filepath.Dir(p) == dir {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// PalaceNonStore is one palace/<slug>/ directory that is NOT a store by the
// presence rule: it holds no regular file outside its top-level .local/. It
// describes what is there and prescribes nothing.
type PalaceNonStore struct {
	Slug string
	// HasLocal reports a palace/<slug>/.local/ directory; Local summarizes its
	// top-level entries, sorted by name, and is empty when .local/ is empty or
	// could not be read.
	HasLocal bool
	Local    []LocalEntry
	// EmptySubtree reports entries OUTSIDE .local/, none of which holds a regular
	// file — in practice empty directories created ahead of a write that never
	// landed, or left behind when a pull removed the files inside them.
	EmptySubtree bool
	// InProjects reports whether Projects/<slug>/ exists on this host.
	InProjects bool
}

// LocalEntry is one top-level entry of a palace/<slug>/.local/ directory.
type LocalEntry struct {
	Name  string
	IsDir bool
	// Files is the number of regular files beneath a directory entry, or -1 when
	// that subtree could not be read. Always 0 for a file entry.
	Files int
}

// PalaceNonStores returns every valid-slug palace/ directory that fails the
// presence rule — exactly the complement, within palace/, of what
// ListAllProjects reports as InPalace. It is read-only.
//
// An absent palace/ yields nothing; an unreadable one is an error. A directory
// the predicate cannot decide is a store (see listPalaceStores), so it is not
// returned here either: the two answers partition palace/ between them.
func (v *Vault) PalaceNonStores() ([]PalaceNonStore, error) {
	palaceDir := filepath.Join(v.Root, "palace")
	dirs, err := listProjectDirs(palaceDir)
	if err != nil {
		return nil, fmt.Errorf("list palace directories: %w", err)
	}

	var out []PalaceNonStore
	for _, s := range dirs {
		dir := filepath.Join(palaceDir, s)
		holds, err := palaceDirHoldsFileOutsideLocal(dir)
		if err != nil || holds {
			continue
		}
		ns := PalaceNonStore{Slug: s}
		entries, err := os.ReadDir(dir)
		if err != nil {
			// The predicate just walked it, so this is a race with a concurrent
			// change; the directory is still not a store, so report what we can.
			entries = nil
		}
		for _, e := range entries {
			if e.Name() == ".local" && e.IsDir() {
				ns.HasLocal = true
				ns.Local = summarizeLocal(filepath.Join(dir, ".local"))
				continue
			}
			ns.EmptySubtree = true
		}
		if info, err := os.Stat(filepath.Join(v.Root, "Projects", s)); err == nil && info.IsDir() {
			ns.InProjects = true
		}
		out = append(out, ns)
	}
	return out, nil
}

// summarizeLocal lists the top-level entries of a .local/ directory with a
// regular-file count for each subdirectory.
func summarizeLocal(localDir string) []LocalEntry {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return nil
	}
	out := make([]LocalEntry, 0, len(entries))
	for _, e := range entries {
		le := LocalEntry{Name: e.Name(), IsDir: e.IsDir()}
		if e.IsDir() {
			n := 0
			werr := filepath.WalkDir(filepath.Join(localDir, e.Name()), func(_ string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Type().IsRegular() {
					n++
				}
				return nil
			})
			if werr != nil {
				n = -1
			}
			le.Files = n
		}
		out = append(out, le)
	}
	return out
}

// trackedPalaceLocalPathspec lists files under any palace/<slug>/.local/. The
// :(glob) magic keeps "*" from crossing a "/", so it names exactly the
// top-level .local of each slug; a plain 'palace/*/.local' pathspec matches no
// file at all, because a wildcard pathspec is not a directory prefix.
const trackedPalaceLocalPathspec = ":(glob)palace/*/.local/**"

// TrackedPalaceLocalFiles returns, per slug, how many files git tracks under
// palace/<slug>/.local/ in this vault. It is read-only: one `git ls-files`
// against the index.
//
// The vault's gitignore normally keeps every .local/ out of the repository, but
// the canonical pattern set ignores only palace/.local/, so a vault that was
// never given palace/*/.local/ can have had these files committed. Anything
// that treats .local/ content as untracked must ask here first.
//
// A vault that is not a git repository tracks nothing, so the answer is an
// empty map. A git failure inside a repository is an error: "I could not look"
// is not "nothing is tracked".
func (v *Vault) TrackedPalaceLocalFiles() (map[string]int, error) {
	dotGit := false
	if _, err := os.Lstat(filepath.Join(v.Root, ".git")); err == nil {
		dotGit = true
	}
	if _, err := exec.LookPath("git"); err != nil {
		if dotGit {
			return nil, fmt.Errorf("the vault is a git repository but git is not on PATH")
		}
		return map[string]int{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z", "--", trackedPalaceLocalPathspec)
	cmd.Dir = v.Root
	// LC_ALL=C so the "not a git repository" test below reads git's own words.
	cmd.Env = append(withoutRepoLocalGitEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		// Outside any repository nothing is tracked. With a .git at the root,
		// the same message means a broken repository, which is a failure.
		if !dotGit && strings.Contains(msg, "not a git repository") {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("git ls-files: %v: %s", err, msg)
	}

	counts := map[string]int{}
	for p := range bytes.SplitSeq(out, []byte{0}) {
		parts := strings.Split(string(p), "/")
		if len(parts) >= 4 && parts[0] == "palace" && parts[2] == ".local" {
			counts[parts[1]]++
		}
	}
	return counts, nil
}

// repoLocalGitEnv is git's own list of variables that redirect a command to a
// different repository, index or work tree (`git rev-parse --local-env-vars`).
// A process that inherits any of them — vp spawned from a git hook, or from a
// shell exporting GIT_DIR — would have `git ls-files` answer for that other
// repository instead of the vault at cmd.Dir.
var repoLocalGitEnv = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT", "GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX",
	"GIT_INTERNAL_SUPER_PREFIX", "GIT_SHALLOW_FILE", "GIT_COMMON_DIR",
}

// withoutRepoLocalGitEnv returns env minus every repoLocalGitEnv variable, so
// the one git call it is used for answers about the directory it runs in. It is
// scoped to TrackedPalaceLocalFiles; gitCmd's environment is unchanged.
func withoutRepoLocalGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(repoLocalGitEnv, name) {
			out = append(out, kv)
		}
	}
	return out
}
