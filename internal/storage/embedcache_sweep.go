// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// The embed cache used to live at palace/<slug>/.local/embed-cache/, inside the
// project's synced directory, while the operator's gitignore keeps
// palace/*/.local/ out of the repository. A pull that deleted a project
// therefore removed every tracked file and left the ignored cache — and the
// palace/<slug>/ directory around it — on every host that had ever embedded
// something for that project. It now lives at palace/.local/embed-cache/<slug>/
// (EmbedCacheDir). SweepEmbedCaches is the one-time migration from the old
// layout, plus the two pieces of upkeep the new layout needs.
//
// # Why raw os.Rename / os.Remove, and no lock or stamp
//
// Everything this file mutates is machine-local, regenerable cache state under
// palace/.local/ or a legacy palace/<slug>/.local/embed-cache/ that git does not
// track. The first is covered by the canonical gitignore. The second is ignored
// only where the vault's gitignore carries palace/*/.local/, which the canonical
// set does not, so the sweep does not assume it: when legacy caches exist it
// asks git (TrackedPalaceLocalFiles, one read-only `git ls-files`) and leaves
// every slug with a tracked .local/ file exactly as it is. Moving a tracked file
// would make a read-only vp_search leave deletions in the working tree that no
// tidy rule matches, which blocks sync. The surface stamp does not apply to
// untracked state (and stamping palace/.local would itself be wrong), and the
// vaultlock discipline protects synced content from lost updates, which a
// vector that is rebuilt on a cache miss cannot suffer. Every operation is
// idempotent and safe to run from several processes at once.
//
// The source audit's funnel rule does not see these calls — their destinations
// are parameters with no vault signal in the enclosing function, a limit the
// rule documents — so this paragraph is the record of why they are raw.
//
// # What it can remove
//
// Directories only through a non-recursive os.Remove, which refuses anything
// that is not empty: a drawer store, a kg/, a .surface or an
// imported-sessions.jsonl beside the cache keeps its directory in place. Files
// only when they are regular *.vec files — the legacy copy of a vector once the
// new layout holds that ID, or a vector under palace/.local/embed-cache/<slug>/
// for a slug that exists nowhere on this host — or the copy fallback's own
// .sweep-*.tmp temporaries once a crash has left them long enough. Anything else in a legacy cache
// stays and keeps the directory. Symlinks are never followed: a symlinked
// palace/<slug>, .local, legacy cache, target or orphan directory is left alone.
//
// # Known races and limits, accepted
//
//   - vibe-vault migration (migrate/vibevault.go, markSessionWithReason) calls
//     EnsureDir(.local) and then OpenFile. A sweep from another process landing
//     exactly between the two removes the empty .local, and the marker write
//     fails with ENOENT. The migrate command returns that error and is
//     idempotent on re-run.
//   - A capture into a slug whose palace/<slug>/ is a legacy husk, racing the
//     first sweep's Remove(palace/<slug>), can see MkdirAll fail with ENOENT
//     when the parent vanishes between its stat and its mkdir. The capture
//     reports the session as not searchable; no content is lost.
//   - A git checkout or pull that revives a husked slug, racing that same
//     Remove(palace/<slug>), can die with "cannot create directory": git
//     remembers palace/<slug> as existing and does not retry the mkdir beneath
//     it on the checkout path. Re-running the pull finishes it; no content is
//     lost. Both windows exist only for the first sweep on a host that still has
//     legacy husks.
//   - A Put racing the whole-directory rename: see migrateLegacyEmbedCache.
//   - The orphan reap judges each slug once, against the live tree: a slug whose
//     palace/<slug> or Projects/<slug> exists at that moment keeps its vectors,
//     and that moment is after the cache directory was read. A project absent
//     from both trees at that moment — created a moment later, or briefly gone
//     (a rebase replaying the commit that created it, a checkout of an older
//     commit, `git stash -u`) — loses its vectors. There is no grace window.
//     The cost is a re-embed on the next miss; no content is lost.
//   - Two vaults sharing one palace/.local through a symlink reap each other's
//     caches on every start, for the same reason and at the same cost.

// Test seams. Production never replaces them; tests do, to reach the failure
// branches (a rename or hard link the filesystem refuses, a target that
// vanishes mid-merge) and to pin the reap's read-then-list order
// deterministically.
var (
	sweepRenameDir    = os.Rename
	sweepLink         = os.Link
	sweepReadCacheDir = os.ReadDir
)

// EmbedCacheSweep reports what one SweepEmbedCaches run did.
type EmbedCacheSweep struct {
	Moved   int      // legacy caches renamed whole into palace/.local/embed-cache/<slug>/
	Merged  int      // legacy vectors linked or copied one by one into a new-layout cache
	Dropped int      // legacy vectors discarded because the new layout already held that ID
	Healed  int      // palace/<slug>/ directories removed because the sweep left them empty
	Reaped  int      // palace/.local/embed-cache/<slug>/ directories removed for slugs that exist nowhere
	Tracked []string // slugs left untouched because git tracks files under their .local/
	Errors  []string // per-slug failures; each leaves its directory in place for the next run
}

// Changed reports whether the sweep moved, merged, dropped, healed or reaped
// anything.
func (s EmbedCacheSweep) Changed() bool {
	return s.Moved+s.Merged+s.Dropped+s.Healed+s.Reaped > 0
}

// SweepEmbedCaches migrates legacy embed caches into the new layout, removes the
// directories that leaves empty, and reaps caches for slugs that are in neither
// tree. It runs in three stages.
//
// Stage 1, migrate and heal, visits each real directory palace/<slug>/ (never a
// symlink) that holds a real .local/ directory; one without .local/ is never
// touched, so a store being created concurrently cannot be raced. When any
// legacy cache exists, git is asked once which slugs have tracked .local/
// files, and those slugs are skipped entirely; if git cannot answer inside a
// repository, stage 1 is skipped for this run. A legacy .local/embed-cache/ is
// renamed whole into the new layout, or merged file by file when the target
// already exists or the rename cannot be done. The heal then removes,
// non-recursively, the legacy embed-cache/, .local/ and palace/<slug>/ — on
// EVERY run, not only the run that moved something, so a process killed
// between the rename and the removals leaves state the next run finishes.
//
// Stage 2 reaps orphaned caches: see reapOrphanCaches.
//
// Stage 3 is the returned report. An absent palace/ is a no-op; an unreadable
// one is the only error, and every per-slug failure lands in Errors instead.
func (v *Vault) SweepEmbedCaches() (EmbedCacheSweep, error) {
	var res EmbedCacheSweep
	palaceDir := filepath.Join(v.Root, "palace")
	entries, err := os.ReadDir(palaceDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, fmt.Errorf("read palace/: %w", err)
	}

	var slugs []string
	legacy := false
	for _, e := range entries {
		name := e.Name()
		if name == ".local" || !e.Type().IsDir() || slug.Validate(name) != nil {
			continue
		}
		slugs = append(slugs, name)
		if !legacy && isRealDir(filepath.Join(palaceDir, name, ".local")) &&
			isRealDir(filepath.Join(palaceDir, name, ".local", "embed-cache")) {
			legacy = true
		}
	}

	cacheRoot := filepath.Join(v.VaultLocalDir(), "embed-cache")
	stage1 := true
	var tracked map[string]int
	if legacy {
		tracked, err = v.TrackedPalaceLocalFiles()
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("migration skipped this run: %v", err))
			stage1 = false
		}
	}
	for _, name := range slugs {
		if !stage1 {
			break
		}
		if tracked[name] > 0 {
			res.Tracked = append(res.Tracked, name)
			continue
		}
		if err := sweepLegacyEmbedCache(&res, filepath.Join(palaceDir, name), filepath.Join(cacheRoot, name)); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", name, err))
		}
	}

	removeStaleCopyTemps(&res, cacheRoot, time.Now().Add(-staleCopyTemp))
	v.reapOrphanCaches(&res, cacheRoot)
	return res, nil
}

// removeStaleCopyTemps removes copy-fallback temporaries a crashed sweep left in
// palace/.local/embed-cache/<slug>/ — only regular files named like one and last
// modified before cutoff, which is well before this sweep began. A temporary a
// concurrent sweep is writing is seconds old at most and is never touched. It
// runs before the orphan reap, so a leftover no longer keeps an orphaned
// directory alive.
func removeStaleCopyTemps(res *EmbedCacheSweep, cacheRoot string, cutoff time.Time) {
	dirs, err := os.ReadDir(cacheRoot)
	if err != nil {
		return // the reap that follows reports an unreadable cache root
	}
	for _, d := range dirs {
		if !d.Type().IsDir() {
			continue
		}
		dir := filepath.Join(cacheRoot, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if !f.Type().IsRegular() || !strings.HasPrefix(name, copyTempPrefix) || !strings.HasSuffix(name, copyTempSuffix) {
				continue
			}
			info, err := f.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: remove stale copy temporary %s: %v", d.Name(), name, err))
			}
		}
	}
}

// isRealDir reports whether p is a directory and not a symlink to one.
func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// sweepLegacyEmbedCache migrates and heals one palace/<slug>/ (stage 1).
func sweepLegacyEmbedCache(res *EmbedCacheSweep, storeDir, target string) error {
	local := filepath.Join(storeDir, ".local")
	if fi, err := os.Lstat(local); err != nil || !fi.IsDir() {
		return nil
	}

	legacy := filepath.Join(local, "embed-cache")
	fi, err := os.Lstat(legacy)
	switch {
	case err == nil && fi.IsDir():
		if err := migrateLegacyEmbedCache(res, legacy, target); err != nil {
			return err
		}
	case err == nil:
		// Something that is not a directory sits where the cache was: not ours
		// to move or remove, and .local is not empty, so there is nothing to heal.
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("stat legacy embed cache: %w", err)
	}

	// The heal. os.Remove refuses a non-empty directory, so each step stops at
	// the first directory still holding anything; ENOENT means another process
	// got there first. Only the final removal counts as a heal.
	for _, dir := range []string{legacy, local, storeDir} {
		err := os.Remove(dir)
		switch {
		case err == nil:
			if dir == storeDir {
				res.Healed++
			}
		case errors.Is(err, fs.ErrNotExist):
			continue
		case errors.Is(err, fs.ErrExist): // ENOTEMPTY and EEXIST both map here
			return nil
		default:
			return fmt.Errorf("remove %s: %w", filepath.Base(dir), err)
		}
	}
	return nil
}

// migrateLegacyEmbedCache moves one legacy cache directory into target.
//
// When target does not exist it is renamed there whole. On Linux, rename(2)
// onto an EMPTY directory succeeds by replacing it, so a rename that races a
// Put which has just created the empty target does NOT fail over to the merge:
// it wins, and the Put's open lands in the replaced directory and gets ENOENT.
// EmbedCache.Put retries once for exactly that, so the vector still lands; a
// caller that did not retry would lose the write and pay a re-embed. A rename
// onto a non-empty target fails, as does one the filesystem cannot do (EXDEV
// when palace/.local is a separate mount, and so on); both fall through to the
// file-by-file merge, which copies when it cannot link.
//
// A target that exists but is not a real directory — a symlink, a file — is
// never followed: the legacy cache stays and the failure is reported.
func migrateLegacyEmbedCache(res *EmbedCacheSweep, legacy, target string) error {
	if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		rerr := sweepRenameDir(legacy, target)
		switch {
		case rerr == nil:
			res.Moved++
			return nil
		case errors.Is(rerr, fs.ErrNotExist):
			return nil // another sweeper moved it first
		}
		// Lost the race to a non-empty target, or the rename is impossible
		// here. Either way, merge into a real target directory.
		if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("move legacy embed cache: %v; create %s: %w", rerr, target, err)
		}
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", target, err)
	}
	if !isRealDir(target) {
		return fmt.Errorf("%s is not a directory; legacy embed cache left in place", target)
	}
	return mergeLegacyEmbedCache(res, legacy, target)
}

// mergeLegacyEmbedCache moves each regular *.vec file of legacy into target
// without ever overwriting one.
//
// os.Link refuses an existing destination with EEXIST, so a vector a
// concurrent Put wrote after the move always wins; the legacy copy is then
// dropped, because a vector is regenerable and the new layout is authoritative.
// Where the filesystem cannot link (EXDEV, EPERM, ENOTSUP, ...) the vector is
// copied instead (copyLegacyVector), so a host whose palace/.local is its own
// mount still heals rather than warning on every start.
//
// Anything that is not a regular *.vec file is not the cache's: it is neither
// moved nor removed, and it keeps the legacy directory from being healed, so
// the palace-local-only check still shows it. One file that fails does not
// stop the others; every failure is reported.
func mergeLegacyEmbedCache(res *EmbedCacheSweep, legacy, target string) error {
	entries, err := os.ReadDir(legacy)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read legacy embed cache: %w", err)
	}
	var failures []string
	merged := false
	// A merged legacy vector's embedding regime is unknown, so the target
	// directory must re-validate: its fingerprint sidecar goes (see
	// EmbedCacheFingerprintFile).
	defer func() {
		if merged {
			if err := os.Remove(filepath.Join(target, EmbedCacheFingerprintFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				res.Errors = append(res.Errors, fmt.Sprintf("drop fingerprint of %s: %v", target, err))
			}
		}
	}()
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".vec") {
			continue
		}
		src := filepath.Join(legacy, name)
		dst := filepath.Join(target, name)
		err := sweepLink(src, dst)
		if errors.Is(err, fs.ErrNotExist) {
			if _, serr := os.Lstat(src); serr != nil {
				continue // another sweeper took this file
			}
			// The target directory vanished under us (a concurrent reap of a
			// slug that exists nowhere). Recreate it and try once more.
			if merr := os.Mkdir(target, 0o755); merr != nil && !errors.Is(merr, fs.ErrExist) {
				failures = append(failures, fmt.Sprintf("recreate target for %s: %v", name, merr))
				continue
			}
			err = sweepLink(src, dst)
		}
		switch {
		case err == nil:
			res.Merged++
			merged = true
		case errors.Is(err, fs.ErrExist):
			res.Dropped++
		default:
			switch cerr := copyLegacyVector(src, dst); {
			case cerr == nil:
				res.Merged++
				merged = true
			case errors.Is(cerr, fs.ErrExist):
				res.Dropped++
			default:
				failures = append(failures, fmt.Sprintf("merge %s: link: %v; copy: %v", name, err, cerr))
				continue
			}
		}
		if err := os.Remove(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, fmt.Sprintf("remove merged legacy vector %s: %v", name, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// The copy fallback's temporary files: named so nothing else matches them, and
// never a *.vec, so no reader mistakes one for a vector.
const (
	copyTempPrefix = ".sweep-"
	copyTempSuffix = ".tmp"
	// staleCopyTemp is how old a temporary must be before a later sweep treats
	// it as a crash's leftover. Copying one vector takes microseconds; a
	// temporary this old belongs to no running sweep.
	staleCopyTemp = 10 * time.Minute
)

// copyLegacyVector copies src to dst for a filesystem that cannot hard-link. It
// writes a temporary file beside dst and renames it into place only if dst is
// still absent, so a partial copy is never visible under a vector's name. The
// rename replaces a dst that appears between that last check and the rename —
// a vector for the same ID, from the same content — which is the one window the
// link path does not have. It returns fs.ErrExist when dst already exists. A
// process killed between the create and the rename leaves the temporary behind;
// removeStaleCopyTemps collects it on a later sweep.
func copyLegacyVector(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), copyTempPrefix+"*"+copyTempSuffix)
	if err != nil {
		return err
	}
	// CreateTemp makes the file 0600; a vector gets the mode Put writes.
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(0o644)
	}
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		if _, err := os.Lstat(dst); err == nil {
			werr = fs.ErrExist
		} else {
			werr = os.Rename(tmp.Name(), dst)
		}
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// reapOrphanCaches removes palace/.local/embed-cache/<slug>/ for every slug that
// exists nowhere on this host (stage 2). Before the move such a cache sat in a
// visible husk; after it, nothing else would ever enumerate it, so it would grow
// unbounded and unseen.
//
// Guards, each deliberate:
//
//   - The cache directory is read first, and each slug it names is then judged
//     against the live tree, so a project that exists by the time its entry is
//     judged keeps its vectors. (Before the per-slug existence check below, the
//     read-then-list order was what guaranteed this; the check now does, and
//     the listing only feeds the two guards that follow.)
//   - When the listing fails, lists ZERO projects, or Projects/ itself is
//     absent (a dangling symlink to an unmounted drive reads as empty), nothing
//     is reaped. A vault in that state is far likelier to be mis-resolved or
//     transiently unreadable than genuinely empty, and reaping on that answer
//     would wipe every notes-only project's cache.
//   - Only real directories with a valid slug name are considered, and a slug
//     is kept whenever palace/<slug> or Projects/<slug> exists at all — a
//     symlink, a Windows junction, or a case-insensitive match the enumerators
//     drop is still a project here.
//   - Inside a reaped directory only regular *.vec files and the fingerprint
//     sidecar are removed. Anything else stays and keeps the directory in place.
func (v *Vault) reapOrphanCaches(res *EmbedCacheSweep, cacheRoot string) {
	entries, err := sweepReadCacheDir(cacheRoot)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			res.Errors = append(res.Errors, fmt.Sprintf("reap skipped: read embed cache: %v", err))
		}
		return
	}
	if len(entries) == 0 {
		return
	}

	if fi, err := os.Stat(filepath.Join(v.Root, "Projects")); err != nil || !fi.IsDir() {
		return
	}
	presence, err := v.ListAllProjects()
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("reap skipped: %v", err))
		return
	}
	if len(presence) == 0 {
		return
	}
	known := make(map[string]bool, len(presence))
	for _, p := range presence {
		known[p.Slug] = true
	}

	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsDir() || slug.Validate(name) != nil || known[name] || v.projectPathExists(name) {
			continue
		}
		dir := filepath.Join(cacheRoot, name)
		files, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: read orphaned cache: %v", name, err))
			}
			continue
		}
		for _, f := range files {
			// The fingerprint sidecar belongs to the vectors: it goes with them,
			// or it alone would keep the orphaned directory from emptying.
			if !f.Type().IsRegular() || (!strings.HasSuffix(f.Name(), ".vec") && f.Name() != EmbedCacheFingerprintFile) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, f.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: remove orphaned vector: %v", name, err))
			}
		}
		err = os.Remove(dir)
		switch {
		case err == nil:
			res.Reaped++
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrExist):
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("%s: remove orphaned cache: %v", name, err))
		}
	}
}

// projectPathExists reports whether anything at all sits at palace/<name> or
// Projects/<name> — a directory, a symlink (dangling or not), a junction.
func (v *Vault) projectPathExists(name string) bool {
	for _, tree := range []string{"palace", "Projects"} {
		if _, err := os.Lstat(filepath.Join(v.Root, tree, name)); err == nil {
			return true
		}
	}
	return false
}
