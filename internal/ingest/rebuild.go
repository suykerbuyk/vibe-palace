// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Rebuild is the explicit, resumable index rebuild
// (explicit-resumable-index-rebuild-with-disk-watchdog). It is the only path
// that empties a project's baseline set, the only path that discards, and the
// only path that clears a fingerprint-reason stale flag on completion.
//
// It orchestrates primitives that already exist rather than re-implementing
// them: the index run lock (indexstore.TryRunLock, KindRebuild), the per-archive
// commit step and the explicit archive pass (RunHeld with Explicit), the
// per-project discard (Tx.Discard), the full engine rebuild (Engine.Rebuild*),
// the graph-heal seam (GraphHealer), and the completion proof
// (RunLock.CompletedRebuild).
//
// For every target project, with the run lock held for the whole run:
//
//  3. discard, only on a present-and-different fingerprint;
//  4. the explicit archive pass for THAT project alone (Only), run lock held;
//  5. the explicit/lazy-mode engine rebuild over every tier, then the graph heal;
//  8. completion: when the run was not stopped, embedding ran and no local
//     vector is missing, clear stale, empty the baseline set and reset failure
//     counts (RunLock.CompletedRebuild).
//
// Then, once, across the vault's other (non-skipped) projects:
//
//  7. an automatic-scope rescan, then release with a filtered recheck, so a
//     trigger that arrived during the rebuild is served before the lock goes.
func Rebuild(ctx context.Context, d Deps, o RebuildOptions) (RunResult, error) {
	var res RunResult
	if d.Vault == nil {
		return res, ErrNoTarget
	}
	eng, ok := d.Engine.(rebuildEngine)
	if !ok {
		return res, errors.New("ingest: the engine cannot run an explicit rebuild")
	}
	targets, err := rebuildTargets(d, o)
	if err != nil {
		return res, err
	}

	// The watchdog's start refusal, over the whole run's estimate, before any
	// lock is taken or any archive opened.
	wd := newWatchdog(o.FreeSpace, d.Vault.VaultLocalDir(), o.Reserve)
	total, err := totalEstimate(d, targets)
	if err != nil {
		return res, err
	}
	if err := wd.startCheck(total); err != nil {
		return res, err
	}

	// The run lock, held for the whole run (steps 3-8), named for the first
	// target; the holder's project is updated per project through SetProject.
	held, ok, err := indexstore.TryRunLock(d.Vault, indexstore.KindRebuild, targets[0])
	if err != nil {
		return res, err
	}
	if !ok {
		return res, holderError(d.Vault)
	}
	defer held.Release()

	for _, p := range targets {
		if err := ctx.Err(); err != nil {
			res.Stopped = "cancelled"
			return res, nil
		}
		if err := held.SetProject(p); err != nil {
			return res, err
		}
		r, err := rebuildProject(ctx, d, held, eng, wd, o, p)
		res.add(r)
		res.Passes++
		if err != nil {
			return res, err
		}
		// A watchdog, checkpoint, cancel or --max-archives stop ends the whole
		// run cleanly: no completion, and no rescan (the next trigger is served
		// by the next run). The ledger is left consistent.
		if stopEndsRun(r.Stopped) {
			res.Stopped = r.Stopped
			return res, nil
		}
	}

	// Step 7: the automatic-scope rescan of the vault's OTHER projects, then
	// the filtered release. The targets are already fully rebuilt, healed and
	// (if they completed) cleared, so the rescan excludes them: re-scanning a
	// target would re-heal it and, worse, its automatic-scope repair pass would
	// re-embed a --no-embed target's missing vectors. A trigger that arrived
	// for another project during the rebuild is served here.
	if err := rescanAndRelease(ctx, d, held, wd, o, targets, &res); err != nil {
		return res, err
	}
	return res, nil
}

// rebuildEngine is what the rebuild driver needs of the search engine beyond
// CacheEngine: the explicit and no-embed full rebuilds. *search.Engine provides
// them (RebuildExplicit, RebuildNoEmbed).
type rebuildEngine interface {
	RebuildExplicit(ctx context.Context, project string) (search.RebuildStats, error)
	RebuildNoEmbed(ctx context.Context, project string) (search.RebuildStats, error)
}

// RebuildOptions are `vp index rebuild`'s arguments.
type RebuildOptions struct {
	// Project is the single project to rebuild; empty with All.
	Project string
	// All rebuilds every project of the vault in slug order, under one hold of
	// the run lock.
	All bool
	// Skip names projects left out of the run entirely: not rebuilt, not
	// discarded, not served by the step-7 rescan, no archive opened. Their
	// ledger, baseline set and chunks stay byte-identical.
	Skip []string
	// MaxArchives, when > 0, stops the explicit archive pass after that many
	// archives; the run then does not complete.
	MaxArchives int
	// NoEmbed runs chunk/classify/extract and writes the chunk store, the local
	// KG and the ledger, but embeds nothing, and ends with a cache-hits-only
	// engine rebuild; it never completes (a missing vector stays stale).
	NoEmbed bool
	// Reserve is the watchdog's free-space floor; a zero value means the
	// placeholder defaults.
	Reserve Reserve
	// FreeSpace is the free-space reader; nil means the platform reader. Tests
	// inject one.
	FreeSpace FreeSpaceReader
}

// skipped reports whether p is named by Skip.
func (o RebuildOptions) skipped(p string) bool { return slices.Contains(o.Skip, p) }

// rebuildTargets resolves and validates the projects the run covers: Project
// alone, or every project of the vault in slug order for All, each minus Skip.
// A Skip that names the positional project, or an unknown project, is a usage
// error (Scope 1).
func rebuildTargets(d Deps, o RebuildOptions) ([]string, error) {
	all, err := d.Vault.ListAllProjects()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, p := range all {
		known[p.Slug] = true
	}
	for _, s := range o.Skip {
		if !known[s] {
			return nil, fmt.Errorf("vp index rebuild: --skip names an unknown project %q", s)
		}
	}
	if o.All {
		if o.Project != "" {
			return nil, errors.New("vp index rebuild: a project and --all are mutually exclusive")
		}
		var out []string
		for s := range known {
			if !o.skipped(s) {
				out = append(out, s)
			}
		}
		sort.Strings(out)
		if len(out) == 0 {
			return nil, errors.New("vp index rebuild: --all left no project to rebuild")
		}
		return out, nil
	}
	if o.Project == "" {
		return nil, errors.New("vp index rebuild: a project or --all is required")
	}
	if o.skipped(o.Project) {
		return nil, fmt.Errorf("vp index rebuild: --skip names the project being rebuilt, %q", o.Project)
	}
	if !known[o.Project] {
		return nil, fmt.Errorf("vp index rebuild: unknown project %q", o.Project)
	}
	return []string{o.Project}, nil
}

// stopEndsRun reports whether a pass's stop reason ends the whole run without a
// rescan (as opposed to a clean exhausted pass, whose Stopped is "").
func stopEndsRun(stopped string) bool {
	switch stopped {
	case "checkpoint", "cancelled", "max archives", "budget", "wall clock":
		return true
	}
	return false
}

// rebuildProject is one project's rebuild: discard (step 3), the explicit
// archive pass (step 4), the engine rebuild and graph heal (step 5/5b), the
// rename-pending cleanup (1b), and completion (step 8). It returns the pass's
// RunResult; the caller aggregates it and reads its Stopped reason.
func rebuildProject(ctx context.Context, d Deps, held *indexstore.RunLock, eng rebuildEngine, wd *watchdog, o RebuildOptions, p string) (RunResult, error) {
	var res RunResult

	// Step 3: discard, only here, only what a present-and-different fingerprint
	// covers, in one commit step. A missing fingerprint discards nothing.
	if err := discardOnMismatch(ctx, d, p); err != nil {
		return res, err
	}

	// Step 4: the explicit archive pass for THIS project alone (Only), entered
	// through RunHeld with the run lock already held (never Run, whose second
	// try-lock on a new descriptor would fail on Linux), SkipHeal so the heal is
	// this driver's, the watchdog as the per-archive/every-500-chunk Checkpoint,
	// and Embed=!NoEmbed.
	ro := RunOptions{
		VaultRoot:   d.Vault.Root,
		Project:     p,
		Only:        p,
		Explicit:    true,
		SkipHeal:    true,
		NoEmbed:     o.NoEmbed,
		MaxArchives: o.MaxArchives,
		Checkpoint:  wd.checkpoint,
	}
	r, err := RunHeld(ctx, d, held, ro)
	res.add(r)
	if err != nil {
		return res, err
	}
	stopped := r.Stopped

	// A cancelled run leaves the context dead, so the engine rebuild cannot run;
	// stop here and let the next run rebuild the tiers (the archive pass is
	// resumable from the ledger). A watchdog or --max-archives stop leaves the
	// context alive, so the engine rebuild below still runs for coherence.
	if stopped == "cancelled" || ctx.Err() != nil {
		return res, nil
	}

	// Step 5: the explicit-mode (or, under --no-embed, the cache-hits-only)
	// engine rebuild over every tier. Even after a watchdog or --max-archives
	// stop the tiers are rebuilt from what is ledgered, so search stays
	// coherent; completion below is what the stop prevents.
	var stats search.RebuildStats
	if o.NoEmbed {
		stats, err = eng.RebuildNoEmbed(ctx, p)
	} else {
		stats, err = eng.RebuildExplicit(ctx, p)
	}
	if err != nil {
		return res, err
	}

	// Step 5b: the graph heal, once, after the engine rebuild (the archive pass
	// ran with SkipHeal, so it did not heal this project). Nil until
	// hnsw-graph-file-envelope-and-warm-start provides the seam.
	healGraph(ctx, d, p)

	// Step 8: completion. Only when the archive pass was not stopped, embedding
	// ran, the final rebuild left no local-tier vector missing, and every live
	// archive is ledgered with its full chunk count or carries a failure record
	// written in this run.
	if stopped == "" && !o.NoEmbed && stats.LocalMisses == 0 {
		accounted, err := everyLiveArchiveAccounted(d, p, res.FailedSHAs)
		if err != nil {
			return res, err
		}
		if accounted {
			if err := completeRebuild(ctx, d, held, p, res.FailedSHAs); err != nil {
				return res, err
			}
			// Requirement from 1b (round 3): only AFTER the rebuild of p
			// completes, remove every rename-pending record that names p as its
			// target, so the renaming host's index/<old>/ can be reaped and 1b's
			// sweep stops warning. A stopped or --no-embed rebuild does not
			// complete and leaves those records in place.
			if err := clearRenamePendingTo(d, p); err != nil {
				slog.Warn("vp index rebuild: could not clear rename-pending records targeting the rebuilt project",
					"project", p, "error", err)
			}
		}
	}
	return res, nil
}

// discardOnMismatch discards, in one commit step, only what a present-and-
// different fingerprint covers (ADR-014 decision 3): a chunks.fingerprint
// mismatch discards the chunks, ledger, local KG and graph and recreates the
// ledger with a fresh baseline; an embed-cache mismatch discards the vectors.
// A graph-fingerprint mismatch is NOT discarded here (step 5b heals it). A
// missing fingerprint discards nothing, and nothing else in the system
// discards.
func discardOnMismatch(ctx context.Context, d Deps, p string) error {
	_, reasons, err := d.Engine.Stale(p)
	if err != nil {
		return err
	}
	var discardChunks, discardVectors bool
	for _, r := range reasons {
		if r.Kind != indexstore.StaleFingerprint {
			continue
		}
		switch r.Fingerprint {
		case indexstore.FingerprintChunks:
			discardChunks = true
		case indexstore.FingerprintEmbed:
			discardVectors = true
		}
	}
	if !discardChunks && !discardVectors {
		return nil
	}
	ix, err := palace.ProjectIndexing(d.Vault, p)
	if err != nil {
		return err
	}
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	if discardChunks {
		// discardChunks needs the recipe to write the fresh fingerprint.
		tx.UseRecipe(ix.Recipe)
		if err := tx.Discard(indexstore.DiscardChunks); err != nil {
			return err
		}
	}
	if discardVectors {
		if err := tx.Discard(indexstore.DiscardVectors); err != nil {
			return err
		}
	}
	slog.Info("vp index rebuild: discarded on a fingerprint mismatch", "project", p,
		"chunks", discardChunks, "vectors", discardVectors)
	return tx.Commit()
}

// everyLiveArchiveAccounted reports whether every session with an archive on
// disk is ledgered live with its full chunk count, or carries a failure record
// written in this run (whose source_sha256 is in failed). It is the "every live
// archive attempted" half of a completed rebuild (the baseline set included,
// since the explicit pass took it).
func everyLiveArchiveAccounted(d Deps, p string, failed []string) (bool, error) {
	entries, err := archive.ListEntries(d.Vault.Root, p)
	if err != nil {
		return false, err
	}
	snap, err := indexstore.ReadStore(d.Vault, p)
	if err != nil {
		return false, err
	}
	l := snap.Ledger()
	if !l.Exists() {
		return false, nil
	}
	failedSet := map[string]bool{}
	for _, s := range failed {
		failedSet[s] = true
	}
	// The newest archive on disk per session is the live candidate.
	newest := map[string]*archive.Entry{}
	for _, e := range entries {
		if e.Manifest == nil || e.Manifest.SessionID == "" {
			continue
		}
		sid := e.Manifest.SessionID
		if cur, ok := newest[sid]; !ok || newerThan(e.Manifest.CapturedAt, cur.Manifest.CapturedAt) {
			newest[sid] = e
		}
	}
	for sid, e := range newest {
		rec, has := l.Session(sid)
		if has && rec.State == indexstore.StateLive && snap.CountChunks(indexstore.ArchiveOwner(rec.SHA)) >= rec.ChunkCount {
			continue // ledgered live with its full chunk count
		}
		if sha := e.Manifest.SourceSHA256; sha != "" && failedSet[sha] {
			continue // a failure record from this run keeps the session named
		}
		return false, nil
	}
	return true, nil
}

// completedRebuildFn mints the completed-rebuild proof; a test seam counts the
// driver's decision to complete (the mutant "mint the proof regardless").
var completedRebuildFn = func(rl *indexstore.RunLock, project string, failed []string) (indexstore.RebuildProof, error) {
	return rl.CompletedRebuild(project, failed)
}

// completeRebuild clears stale (both reasons), empties the baseline set and
// resets the failure counts of every archive that did not fail in this run, in
// one commit step under the project's commit lock (step 8).
func completeRebuild(ctx context.Context, d Deps, held *indexstore.RunLock, p string, failed []string) error {
	proof, err := completedRebuildFn(held, p, failed)
	if err != nil {
		return err
	}
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	if err := tx.ClearStale(proof); err != nil {
		return err
	}
	if err := tx.ClearBaseline(proof); err != nil {
		return err
	}
	if err := tx.ClearFailures(proof); err != nil {
		return err
	}
	slog.Info("vp index rebuild: completed", "project", p, "failed_archives", len(failed))
	return tx.Commit()
}

// clearRenamePendingTo removes every rename-pending record whose target is p
// (1b's IndexRenamePendingPath / ListRenamePending): after p's rebuild, the
// renaming host's index/<old>/ is reapable and 1b's sweep stops warning.
func clearRenamePendingTo(d Deps, p string) error {
	pending, err := d.Vault.ListRenamePending()
	if err != nil {
		return err
	}
	for old, to := range pending {
		if to != p {
			continue
		}
		path, err := d.Vault.IndexRenamePendingPath(old)
		if err != nil {
			return err
		}
		if err := removeRenamePendingFn(path); err != nil {
			return err
		}
		slog.Info("vp index rebuild: cleared a rename-pending record the rebuild resolves",
			"old", old, "to", to)
	}
	return nil
}

// removeRenamePendingFn removes a rename-pending record file; a seam for tests.
var removeRenamePendingFn = func(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// rescanAndRelease is step 7: with the run lock still held, it ingests, in
// automatic scope, whatever is pending across the vault's other (non-skipped)
// projects, looping while a rescan finds an in-scope archive this run has not
// attempted, then releases through ReleaseAndRecheck with the set the last
// rescan saw. A trigger that exited because this run held the lock is served
// here. The memo is shared with the explicit passes, so the target's archives
// are never re-attempted.
func rescanAndRelease(ctx context.Context, d Deps, held *indexstore.RunLock, wd *watchdog, o RebuildOptions, targets []string, res *RunResult) error {
	memo := newMemo()
	ao := RunOptions{
		VaultRoot: d.Vault.Root,
		// No Project and no Only: projectOrder visits every project in slug
		// order, minus Skip. The targets are added to Skip so the rescan covers
		// only the vault's OTHER projects.
		Skip:       append(slices.Clone(o.Skip), targets...),
		Checkpoint: wd.checkpoint,
	}
	pending := func() ([]string, error) { return inScopePending(d, ao) }
	for {
		st := newState(d, ao, memo)
		var seenList []string
		for {
			before := st.progress()
			r, err := runHeld(ctx, d, held, ao, st)
			res.add(r)
			res.Passes++
			if err != nil {
				_ = held.Release()
				return err
			}
			if st.stopped == "checkpoint" || st.stopped == "cancelled" {
				res.Stopped = st.stopped
				return held.Release()
			}
			seenList, err = pending()
			if err != nil {
				_ = held.Release()
				return err
			}
			unattempted := slices.ContainsFunc(seenList, func(s string) bool { return !memo.attempted[s] })
			if !unattempted || st.stopped != "" || st.progress() == before {
				break
			}
		}
		seen := map[string]struct{}{}
		for _, s := range seenList {
			seen[s] = struct{}{}
		}
		if beforeReleaseFn != nil {
			beforeReleaseFn()
		}
		again, err := held.ReleaseAndRecheck(seen, pending)
		if err != nil || !again {
			return err
		}
	}
}

// ErrRunLockHeld is the refusal when another index run holds the run lock: the
// rebuild exits at once, naming the holder, having opened no archive.
var ErrRunLockHeld = errors.New("ingest: another index run holds the run lock")

// holderError names the run lock's current holder (ADR-014 decision 7): pid,
// kind, project and start time, read without taking the lock.
func holderError(v *storage.Vault) error {
	h, err := indexstore.ReadHolder(v)
	if err != nil {
		return fmt.Errorf("%w (holder unknown)", ErrRunLockHeld)
	}
	return fmt.Errorf("%w: pid %d, kind %s, project %q, started %s",
		ErrRunLockHeld, h.PID, h.Kind, h.Project, h.StartTime.UTC().Format(time.RFC3339))
}

// ProjectEstimate is a dry run's per-project report (Scope 9): the pending
// archives and the baseline set counted separately, their compressed and
// uncompressed bytes, and the estimated consumption.
type ProjectEstimate struct {
	Project                   string
	PendingArchives           int
	PendingCompressedBytes    uint64
	PendingUncompressedBytes  uint64
	BaselineArchives          int
	BaselineCompressedBytes   uint64
	BaselineUncompressedBytes uint64
	Estimate                  Estimate
}

// DryRunReport is what `--dry-run` reports (Scope 9): it writes nothing, takes
// no lock, and reads only the manifests, the ledger and statfs. Refusal is the
// watchdog's start refusal (ErrReserve), empty when the run would be admitted.
type DryRunReport struct {
	Projects []ProjectEstimate
	Reserve  Reserve
	Free     FreeSpace
	Refusal  string
}

// DryRun is the preflight (Scope 9): it reports, per covered project, the
// pending archives and the baseline set separately with their bytes, the
// estimate and the reserve, and the watchdog's start refusal. It takes no lock
// and writes nothing, including the holder record.
func DryRun(d Deps, o RebuildOptions) (DryRunReport, error) {
	var rep DryRunReport
	if d.Vault == nil {
		return rep, ErrNoTarget
	}
	targets, err := rebuildTargets(d, o)
	if err != nil {
		return rep, err
	}
	rep.Reserve = o.Reserve.orDefault()
	var total Estimate
	for _, p := range targets {
		pe, err := projectEstimate(d, p)
		if err != nil {
			return rep, err
		}
		rep.Projects = append(rep.Projects, pe)
		total.Bytes += pe.Estimate.Bytes
		total.Inodes += pe.Estimate.Inodes
	}
	wd := newWatchdog(o.FreeSpace, d.Vault.VaultLocalDir(), o.Reserve)
	fs, err := wd.freeAt()
	if err != nil {
		return rep, err
	}
	rep.Free = fs
	if err := wd.startCheck(total); err != nil {
		rep.Refusal = err.Error()
	}
	return rep, nil
}

// projectEstimate reads p's pending archives and baseline set, split, with
// their bytes, and the consumption estimate from the uncompressed MiB.
func projectEstimate(d Deps, p string) (ProjectEstimate, error) {
	pe := ProjectEstimate{Project: p}
	entries, err := archive.ListEntries(d.Vault.Root, p)
	if err != nil {
		return pe, err
	}
	snap, err := indexstore.ReadStore(d.Vault, p)
	if err != nil {
		return pe, err
	}
	l := snap.Ledger()
	// The explicit plan: every archive a rebuild of p would read.
	for _, it := range plan(entries, l, nil, RunOptions{Explicit: true}, nil, d.Vault.Root) {
		m := it.e.Manifest
		if m == nil {
			continue
		}
		comp := uint64(max64(m.CompressedBytes))
		uncomp := uint64(max64(m.SourceBytes))
		if l.Exists() && l.InBaseline(m.SourceSHA256) {
			pe.BaselineArchives++
			pe.BaselineCompressedBytes += comp
			pe.BaselineUncompressedBytes += uncomp
		} else {
			pe.PendingArchives++
			pe.PendingCompressedBytes += comp
			pe.PendingUncompressedBytes += uncomp
		}
	}
	miB := (pe.PendingUncompressedBytes + pe.BaselineUncompressedBytes) / (1 << 20)
	pe.Estimate = EstimateFor(miB)
	return pe, nil
}

// totalEstimate is the whole run's estimated consumption across targets.
func totalEstimate(d Deps, targets []string) (Estimate, error) {
	var total Estimate
	for _, p := range targets {
		pe, err := projectEstimate(d, p)
		if err != nil {
			return total, err
		}
		total.Bytes += pe.Estimate.Bytes
		total.Inodes += pe.Estimate.Inodes
	}
	return total, nil
}

// max64 clamps a possibly-negative manifest byte count to a non-negative one.
func max64(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
