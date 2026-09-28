// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// The project-tree primitives vp_vault_split and vp_vault_merge were built on,
// moved here so the lifecycle commands (copy, delete, rename) share them with
// the tools rather than reaching into internal/tools. They take only the roots
// they are passed. Behaviour is unchanged by the move: the tools call these
// through thin wrappers, and their tests are the regression gate.

// ProjectTreeEntry is one file that will travel: its vault-relative slash path, the
// sha256 of its content, and its size.
type ProjectTreeEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ProjectTreeReport is the per-tree shape of the manifest: how many files travel
// from palace/{slug} or Projects/{slug}, how many bytes, and how many paths the
// subtract set removed.
type ProjectTreeReport struct {
	Tree       string `json:"tree"`
	Present    bool   `json:"present"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	Subtracted int    `json:"subtracted"`
}

// WalkProjectTree inventories one allow-listed tree.
//
// 🔴 Lstat FIRST, AND A NON-REGULAR FILE REFUSES. Not skip, not follow.
//
//   - FOLLOW is a leak. A symlink under an allow-listed slug can point at a
//     project that is not in the allow-list, or outside the vault entirely.
//     Reading through it files another project's bytes under this slug's name,
//     and the destination-side membership gate only ever sees the allow-listed
//     slug — so the leak passes every check downstream of here. This is why
//     migrate.copyTree/copyOne are not called: they os.Stat and os.ReadFile,
//     which both follow.
//   - SKIP is a silent hole. The path would be absent from the manifest and
//     absent from the destination, and nothing downstream would ever say so.
//
// filepath.WalkDir does not descend through a symlinked directory, but a
// symlinked FILE is still handed over as a DirEntry — so the refusal has to be
// made here, per entry, and not inferred from the walk's own behaviour.
func WalkProjectTree(root, dir string) (ProjectTreeReport, []ProjectTreeEntry, error) {
	var report ProjectTreeReport
	var entries []ProjectTreeEntry

	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A slug present in only one tree is drift, not an error. It is
			// reported as such; the missing side simply contributes nothing.
			return report, nil, nil
		}
		return report, nil, fmt.Errorf("stat %s: %w", VaultRel(root, dir), err)
	}
	if !info.IsDir() {
		return report, nil, fmt.Errorf(
			"%s is not a directory (mode %s): an allow-listed project tree must be a real directory",
			VaultRel(root, dir), info.Mode().Type())
	}
	report.Present = true

	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := VaultRel(root, p)

		// Machine-local trees are pruned, not scanned. They never travel, so
		// their contents are not this tool's business — and scanning them would
		// let a symlink in a cache directory refuse an otherwise clean plan.
		if d.IsDir() {
			if MachineLocalDirNames[d.Name()] {
				report.Subtracted++
				return fs.SkipDir
			}
			return nil
		}

		st, lerr := os.Lstat(p)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", rel, lerr)
		}
		if st.Mode().Type() != 0 {
			return fmt.Errorf(
				"%s is not a regular file (mode %s): split refuses a tree containing "+
					"symlinks, devices or sockets rather than skipping them (a silent hole "+
					"in the manifest) or following them (bytes from outside the allow-list "+
					"filed under an allow-listed slug)",
				rel, st.Mode().Type())
		}

		// The subtract set is applied AFTER the non-regular refusal, on purpose.
		// A symlink named .surface is still a symlink in an allow-listed tree,
		// and the reason to refuse it has nothing to do with whether its path
		// would later be hashed.
		if ClassifyProjectPath(rel) != ProjectContent {
			report.Subtracted++
			return nil
		}

		sum, size, herr := HashFile(p)
		if herr != nil {
			return fmt.Errorf("hash %s: %w", rel, herr)
		}
		entries = append(entries, ProjectTreeEntry{Path: rel, SHA256: sum, Size: size})
		report.Files++
		report.Bytes += size
		return nil
	})
	if err != nil {
		return report, nil, err
	}
	return report, entries, nil
}

// VaultRel renders a host path as a vault-relative, slash-separated path. Every
// manifest row and every error message uses it, so neither leaks the host's
// absolute vault location and neither varies by platform separator.
func VaultRel(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

// HashFile returns the sha256 and size of a regular file. It streams rather
// than reading whole: a transcript archive is large, and a plan over a real
// vault opens thousands of them.
func HashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// CopyProjectTreeEntry copies one manifest row into the destination.
//
// 🔴 Lstat FIRST, AND A NON-REGULAR SOURCE REFUSES — the same contract plan
// enforced, re-enforced here because plan and apply are separate calls and the
// tree can change between them. os.Open follows symlinks, so a check made only
// at plan time is a check made against a different filesystem than the one
// being read.
//
// The content is hashed WHILE it streams, and the result is compared to the
// manifest row. That costs nothing — the bytes are already passing through — and
// it closes the last gap the digest bind cannot: buildSplitManifest hashed this
// file moments ago, and this is the read that proves the bytes landing in the
// destination are those same bytes and not a racing rewrite.
//
// atomicfile.WriteStream is the streaming half of the whole-file primitive: it
// creates parents, writes a temp file beside the target and renames. Passing
// vaultRoot = dest makes the DESTINATION stamp itself (atomicfile.go:166), which
// is why no .surface file ever has to travel.
func CopyProjectTreeEntry(srcRoot, destRoot string, e ProjectTreeEntry) error {
	src := filepath.Join(srcRoot, filepath.FromSlash(e.Path))

	st, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("lstat %s: %w", e.Path, err)
	}
	if st.Mode().Type() != 0 {
		return fmt.Errorf(
			"%s is not a regular file (mode %s): it was regular when the manifest was "+
				"taken, so the source changed under this call", e.Path, st.Mode().Type())
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", e.Path, err)
	}
	defer f.Close()

	h := sha256.New()
	var n int64
	dst := filepath.Join(destRoot, filepath.FromSlash(e.Path))
	if err := atomicfile.WriteStream(destRoot, dst, func(w io.Writer) error {
		var cerr error
		n, cerr = io.Copy(io.MultiWriter(w, h), f)
		return cerr
	}); err != nil {
		return fmt.Errorf("copy %s: %w", e.Path, err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
		return fmt.Errorf(
			"copy %s: content hashed %s, manifest says %s — the source changed while it "+
				"was being read", e.Path, got, e.SHA256)
	}
	if n != e.Size {
		return fmt.Errorf("copy %s: copied %d bytes, manifest says %d", e.Path, n, e.Size)
	}
	return nil
}

// PurgeCleanupEntry is one path step 4 of a committed purge left behind.
type PurgeCleanupEntry struct {
	Path string `json:"path"`
	// Class is "ignored", "machine-local" or "untracked". An untracked
	// (non-ignored) leftover is a resurrection risk: it reads as project
	// content, and tidy would commit it back.
	Class  string `json:"class"`
	Reason string `json:"reason,omitempty"`
}

// CleanupPurgedTrees removes, after the purge commit, exactly the files purge
// COLLECTED that git did not remove (untracked and ignored rows, and the embed
// cache), then the directories bottom-up. A file written after the collect is
// never removed. Nothing here fails the purge: every path left is reported.
func CleanupPurgedTrees(root string, trees []PurgeSet, hashes map[string]string) (files, dirs int, left []PurgeCleanupEntry) {
	collected := map[string]bool{}
	for _, set := range trees {
		for _, rel := range set.Files {
			collected[rel] = true
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				continue // git rm removed it
			}
			if _, err := vaultfs.Delete(root, rel, hashes[rel]); err != nil {
				left = append(left, PurgeCleanupEntry{Path: rel, Reason: err.Error()})
				continue
			}
			files++
		}
		dirAbs := append([]string(nil), set.Dirs...)
		sort.Slice(dirAbs, func(i, j int) bool { return len(dirAbs[i]) > len(dirAbs[j]) })
		for _, d := range dirAbs {
			if err := vaultfs.RemoveNoLock(d); err == nil {
				dirs++
			}
		}
	}
	// Whatever is still under a purged tree — a failed removal above, or a file
	// nobody collected — is reported with its class.
	seen := map[string]bool{}
	for _, l := range left {
		seen[l.Path] = true
	}
	for _, set := range trees {
		if len(set.Dirs) == 0 {
			continue // the tree was absent
		}
		// set.Dirs[0] is the tree root: CollectPurgeTree's walk records it first.
		_ = filepath.WalkDir(set.Dirs[0], func(fp string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if rel := VaultRel(root, fp); !seen[rel] {
				seen[rel] = true
				left = append(left, PurgeCleanupEntry{Path: rel})
			}
			return nil
		})
	}
	for i := range left {
		switch ignored, err := GitPathIgnored(root, left[i].Path); {
		case ClassifyProjectPath(left[i].Path) == ProjectMachineLocal:
			left[i].Class = "machine-local"
		case err == nil && ignored:
			left[i].Class = "ignored"
		default:
			left[i].Class = "untracked"
		}
		if left[i].Reason == "" && !collected[left[i].Path] {
			left[i].Reason = "written under the purged tree after purge collected it"
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i].Path < left[j].Path })
	return files, dirs, left
}

// PurgeSet is one source slug tree as purge found it: every regular file
// (vault-relative) and every directory (absolute), collected before anything is
// removed from any tree.
type PurgeSet struct {
	Files []string
	Dirs  []string
	Bytes int64
}

// CollectPurgeTree walks one source slug tree without changing it.
//
// A non-regular entry ANYWHERE in the tree refuses the whole purge, including
// inside the machine-local subtrees plan prunes rather than scans. Plan may
// ignore a symlink in an embed cache because that cache was never going to
// travel; purge may not, because it is about to remove the directory containing
// it and neither named primitive can classify what it would be removing.
func CollectPurgeTree(root, treeRel string) (PurgeSet, error) {
	var set PurgeSet
	dir := filepath.Join(root, filepath.FromSlash(treeRel))
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Drift: this slug lives in only one of the two trees. Nothing to
			// remove on this side, and that is not an error.
			return set, nil
		}
		return set, fmt.Errorf("stat %s: %w", treeRel, err)
	}
	if !info.IsDir() {
		return set, fmt.Errorf(
			"%s is not a directory (mode %s): purge refuses it", treeRel, info.Mode().Type())
	}

	// Collect first, mutate second. A walk that deleted as it went would be
	// mutating the tree it is enumerating, and the failure mode of that is a
	// partial purge that reports success.
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			set.Dirs = append(set.Dirs, p)
			return nil
		}
		rel := VaultRel(root, p)
		st, lerr := os.Lstat(p)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", rel, lerr)
		}
		if st.Mode().Type() != 0 {
			return fmt.Errorf(
				"%s is not a regular file (mode %s): purge refuses a tree it cannot "+
					"classify entry by entry, because neither vaultfs.Delete nor "+
					"RemoveNoLock is a recursive primitive and there is no third one",
				rel, st.Mode().Type())
		}
		set.Files = append(set.Files, rel)
		set.Bytes += st.Size()
		return nil
	})
	return set, err
}

// RemovePurgeTree removes one collected tree: every regular file through
// vaultfs.Delete, then every directory bottom-up through vaultfs.RemoveNoLock.
// The caller has already proved that every file is accounted for.
func RemovePurgeTree(root string, set PurgeSet, hashes map[string]string) (int, int, error) {
	var files int
	for _, rel := range set.Files {
		// Three classes, and the caller refused the third before any tree was
		// touched. A manifest row is deleted under its hash, the compare-and-set
		// guard for a file that travelled. A subtract-set file (.surface,
		// .local/**, .vp-locks/**, Projects/<slug>/config.toml)
		// was excluded from the manifest by the same predicate, ClassifyProjectPath,
		// so it legitimately has no hash and is removed unguarded because the
		// tree it lives in is going away. Anything else was written after the
		// bind and never copied.
		if _, derr := vaultfs.Delete(root, rel, hashes[rel]); derr != nil {
			return files, 0, fmt.Errorf("purge %s: %w", rel, derr)
		}
		files++
	}

	// Bottom-up: a child's path is always strictly longer than its parent's, so
	// ordering by descending length removes every directory only after its
	// contents. RemoveNoLock is os.Remove, which refuses a non-empty directory —
	// so a miscount here fails loudly instead of removing something unexamined.
	dirAbs := append([]string(nil), set.Dirs...)
	sort.Slice(dirAbs, func(i, j int) bool { return len(dirAbs[i]) > len(dirAbs[j]) })
	var dirs int
	for _, d := range dirAbs {
		if rerr := vaultfs.RemoveNoLock(d); rerr != nil {
			return files, dirs, fmt.Errorf("purge directory %s: %w", VaultRel(root, d), rerr)
		}
		dirs++
	}
	return files, dirs, nil
}
