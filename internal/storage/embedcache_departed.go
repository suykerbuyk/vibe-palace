// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// A project that MOVED TO ANOTHER VAULT leaves its host-local embed cache
// behind on every host but the one that ran the purge. The orphan reap removes
// it only once both Projects/<slug> and palace/<slug> are gone, and a pull
// cannot remove an ignored *.bak under Projects/<slug> or an untracked
// palace/<slug>/.local/imported-sessions.jsonl — so on such a host the cache is
// kept forever. The departure record says what happened, so the departed pass
// decides by the record instead of by what a pull happened to leave.
//
// # Scope: moved-to-vault only
//
// A slug whose departure chain ends RENAMED is left exactly as the orphan reap
// treats it today, and embed-cache/<to> is never touched. Carrying a renamed
// project's vectors needs re-keying (drawer vectors are named
// DrawerID(slug, content), note./iter. vectors carry the slug as a prefix), and
// that is task embed-cache-carry-on-rename-rekeys-vectors, not this code.

// cacheScan is one read of the cache root and the project listing, taken once
// and shared by the departed pass and the orphan reap, so the two judge the same
// snapshot and pay for one ListAllProjects.
type cacheScan struct {
	root    string
	entries []os.DirEntry
	known   map[string]bool
	// undecidable names the guard that stopped the scan. When set, nothing may
	// be removed and a reporter must not say "none".
	undecidable string
	// err is an I/O failure behind undecidable, for callers that report errors.
	err error
}

// scanCaches reads the cache root FIRST and lists projects AFTER, so a project
// created between the two keeps its vectors (TestSweepEmbedCaches_ReapKeepsA
// ProjectBornDuringTheSweep). Its guards are the orphan reap's, shared by every
// caller of either pass:
//
//   - a missing or empty cache root is nothing to do (undecidable stays "");
//   - Projects/ must be a real directory: absent or a dangling symlink to an
//     unmounted drive reads as zero projects;
//   - the listing must succeed and must not be empty, because a vault in that
//     state is far likelier mis-resolved or transiently unreadable than empty.
func (v *Vault) scanCaches(cacheRoot string) cacheScan {
	s := cacheScan{root: cacheRoot}
	entries, err := sweepReadCacheDir(cacheRoot)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.undecidable = "the embed cache could not be read"
			s.err = fmt.Errorf("read embed cache: %w", err)
		}
		return s
	}
	if len(entries) == 0 {
		return s
	}
	// Removal goes through the cache root's path, so a symlinked root (or a
	// symlinked palace/.local above it) would delete *.vec files wherever it
	// points, outside this vault. Such a vault is judged by nobody.
	for _, p := range []string{filepath.Dir(cacheRoot), cacheRoot} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
			s.undecidable = filepath.Base(p) + " is a symlink or cannot be inspected"
			return s
		}
	}
	s.entries = entries
	if fi, err := os.Stat(filepath.Join(v.Root, "Projects")); err != nil || !fi.IsDir() {
		s.undecidable = "Projects/ is not a readable directory"
		return s
	}
	presence, err := v.ListAllProjects()
	if err != nil {
		s.undecidable = "the project listing failed"
		s.err = err
		return s
	}
	if len(presence) == 0 {
		s.undecidable = "the project listing is empty"
		return s
	}
	s.known = make(map[string]bool, len(presence))
	for _, p := range presence {
		s.known[p.Slug] = true
	}
	return s
}

// DepartedCacheSweep reports one departed-cache pass.
type DepartedCacheSweep struct {
	// Departed names every slug whose cache still holds vectors (or the
	// fingerprint sidecar) and whose departure chain ends moved-to-vault.
	Departed []string
	// Removed names the caches this pass removed at least one vector from (act
	// only), whether or not their directory could go too: a directory holding
	// anything that is not a cache file stays, and its vectors are gone all the same.
	Removed []string
	// Kept names the Removed caches whose directory stayed: it holds files that
	// are not cache files (removeCacheDir never removes those), or a vector's
	// removal failed (that failure is in Errors).
	Kept []string
	// Undecidable is set when a guard stopped the pass before any slug was
	// judged. A reporter must show it, never "0".
	Undecidable string
	Errors      []string
}

// DepartedCaches judges every embed cache against its departure record. With
// act it removes the cache of each slug that moved to another vault; without it
// it only reports, which is what `vp check` does. It is the one definition of
// the rule: stage 2 of SweepEmbedCaches, the pull triggers and the check row
// all call it.
func (v *Vault) DepartedCaches(act bool) DepartedCacheSweep {
	var res DepartedCacheSweep
	scan := v.scanCaches(filepath.Join(v.VaultLocalDir(), "embed-cache"))
	v.departedPass(scan, act, &res)
	return res
}

// gitPathTimeout bounds each `git rev-parse --git-path` of the in-progress
// guard; it is a local lookup and never touches the network.
const gitPathTimeout = 10 * time.Second

// departedPass is DepartedCaches over a scan the caller already holds.
func (v *Vault) departedPass(scan cacheScan, act bool, res *DepartedCacheSweep) {
	if scan.undecidable != "" {
		res.Undecidable = scan.undecidable
		if scan.err != nil {
			res.Errors = append(res.Errors, scan.err.Error())
		}
		return
	}
	if len(scan.entries) == 0 {
		return
	}
	// A tree mid-merge or mid-rebase can show a slug's directory absent for a
	// moment; judging it then is the dangerous direction, so do nothing.
	if why := v.gitOperationInProgress(); why != "" {
		res.Undecidable = why
		return
	}
	for _, e := range scan.entries {
		name := e.Name()
		if !e.Type().IsDir() || slug.Validate(name) != nil || scan.known[name] {
			continue
		}
		chain, ok := departure.Resolve(v.Root, name)
		if !ok || !movedToVault(chain[len(chain)-1]) {
			continue
		}
		dir := filepath.Join(scan.root, name)
		if n, err := countCacheFiles(dir); err != nil || n == 0 {
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: read departed cache: %v", name, err))
			}
			continue
		}
		res.Departed = append(res.Departed, name)
		if !act {
			continue
		}
		vectors, dirGone, errs := removeCacheDir(dir, name)
		res.Errors = append(res.Errors, errs...)
		if vectors > 0 {
			res.Removed = append(res.Removed, name)
			if !dirGone {
				res.Kept = append(res.Kept, name)
			}
		}
	}
}

// movedToVault is the only chain end the departed pass acts on: a well-formed
// moved-to-vault record. A malformed record still means departed elsewhere, but
// it names nothing this pass can trust, so it is left to the orphan reap.
func movedToVault(r departure.Record) bool {
	return r.Malformed == "" && r.Kind == departure.MovedToVault && r.Validate() == nil
}

// isCacheFile is what belongs to a cache: regular *.vec files and the
// fingerprint sidecar. Nothing else counts and nothing else is removed.
func isCacheFile(e os.DirEntry) bool {
	return e.Type().IsRegular() && (strings.HasSuffix(e.Name(), ".vec") || e.Name() == EmbedCacheFingerprintFile)
}

func countCacheFiles(dir string) (int, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if isCacheFile(f) {
			n++
		}
	}
	return n, nil
}

// removeCacheDir removes a cache directory's own files and then the directory,
// non-recursively: anything that is not a cache file stays and keeps the
// directory in place. It is the orphan reap's removal and the departed pass's.
// It returns how many cache files it removed and whether the directory went.
func removeCacheDir(dir, name string) (int, bool, []string) {
	var errs []string
	files, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Sprintf("%s: read cache: %v", name, err))
		}
		return 0, false, errs
	}
	n := 0
	for _, f := range files {
		if !isCacheFile(f) {
			continue
		}
		switch err := os.Remove(filepath.Join(dir, f.Name())); {
		case err == nil:
			n++
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, fmt.Sprintf("%s: remove vector: %v", name, err))
		}
	}
	err = os.Remove(dir)
	switch {
	case err == nil:
		return n, true, errs
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrExist):
	default:
		errs = append(errs, fmt.Sprintf("%s: remove cache: %v", name, err))
	}
	return n, false, errs
}

// departedOperationInProgress is operationInProgress, the package's one probe
// for a merge, cherry-pick, revert or rebase in flight. A variable only so a
// test can make it fail.
var departedOperationInProgress = operationInProgress

// gitOperationInProgress names why the vault's git state forbids judging a
// departure now, or returns "". A tree mid-merge, mid-cherry-pick, mid-revert
// or mid-rebase can show a slug's directory absent for a moment, and so can a
// fast-forward still writing the tree (it holds index.lock). Every doubt is
// the safe direction, which is doing nothing: a probe that fails, or a vault
// git cannot read, is undecidable — never "no operation". A vault that is not
// a repository, or is nested inside another one, has no such state of its own
// and is not guarded.
func (v *Vault) gitOperationInProgress() string {
	switch state, _ := InspectVaultGit(v.Root); state {
	case VaultNotGit, VaultGitNested:
		return ""
	case VaultGitOK:
	default:
		return "git cannot read the vault's repository"
	}
	op, err := departedOperationInProgress(v.Root)
	if err != nil {
		return "the vault's git state could not be read: " + err.Error()
	}
	if op != "" {
		return "a git operation is in progress (" + op + ")"
	}
	lock, err := gitCmd(v.Root, gitPathTimeout, "rev-parse", "--git-path", "index.lock")
	if err != nil {
		return "the vault's git state could not be read: " + err.Error()
	}
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(v.Root, lock)
	}
	if _, err := os.Lstat(lock); err == nil {
		return "a git operation holds index.lock"
	}
	return ""
}

// sweepDepartedAfterPull is the pull trigger: after a pull has merged, remove
// the caches of slugs it (or an earlier pull) moved to another vault. It never
// fails the pull; what it did or could not do is logged. A variable so a test
// can tell whether a pull ran it.
var sweepDepartedAfterPull = func(vaultPath string) {
	res := NewVault(vaultPath).DepartedCaches(true)
	if len(res.Removed) > 0 {
		// kept: caches whose vectors went but whose directory stays, because it
		// holds files that are not cache files.
		slog.Info("embed cache: removed the caches of projects that moved to another vault",
			"projects", res.Removed, "kept", res.Kept)
	}
	for _, e := range res.Errors {
		slog.Warn("embed cache: departed cache", "err", e)
	}
}
