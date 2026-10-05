// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

// The host-local half of `vp vault rename` (U9 increment 2): after the tracked
// rename commit has landed and its vault root lock is released, the renaming
// host moves its per-project index store from the old slug to the new one and
// rewrites the one slug-derived field on each chunk (wing) plus the ledger's
// display-only archive_path. The embed cache is REMOVED, not carried (operator
// M0 ruling 2026-10-05 = REBUILD): the new slug re-embeds lazily on first
// search. Every other host keeps nothing — it reaps index/<old>/ like any
// departed store and re-ingests <new> from its archives.
//
// Spec: task rename-core-fresh-target-with-digest-bind, the inherited
// "Host-local index rename" section and its round-2/round-3 updates, as amended
// by the M0 ruling and the Chair ruling of 2026-10-05.
//
// Locking (round-2 M3: no root-lock↔index-commit-lock nesting; this runs after
// storage released the vault root lock): the run lock (KindLifecycle, named for
// the OLD slug, so 1b's sweep knows the rename is running) is held across and
// released last; the old store is drained (LockLifecycle + ChangeEpoch) so any
// Tx taken before the commit has finished and no later one serves it; then the
// move and rewrites run under the NEW slug's single commit lock.
//
// Recovery (plan vs. real code): the inherited design routed host-local
// recovery entries through the lc-u3 pending marker. But that marker is cleared
// when the tracked commit publishes — BEFORE this step runs — so it cannot carry
// them. Instead every sub-step here is idempotent (rename-if-present,
// rewrite-only-what-still-names-<old>, remove-if-present), the rename-pending
// record signals the step is owed, and `vp index rebuild <to>` is the escape
// hatch. A crash re-runs to the same end state; this is exactly round-3's model.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// adoptRenamedStoreHook is a TEST SEAM, nil in production: it runs at each named
// step of (*Tx).AdoptRenamedStore ("renamed", "wing", "archive", "cache"); an
// error it returns aborts there, as a crash would.
var adoptRenamedStoreHook func(step string) error

func adoptStep(step string) error {
	if adoptRenamedStoreHook != nil {
		return adoptRenamedStoreHook(step)
	}
	return nil
}

func dirExists(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// AdoptRenamedStore moves the index store of `from` into this Tx's project (the
// NEW slug, <to>) and rewrites its slug-derived content, under this Tx's commit
// lock. It is the under-lock core of the host-local rename step; the
// orchestration (run lock, drain, imports carry, record removal) is
// AdoptRenamedProject.
//
// It is idempotent: a re-run after a crash converges to the same end state.
//   - target-absent check: both stores present is an unmergeable collision
//     (refuse, name `vp index rebuild <to>`); an embed cache already at <to>
//     holding vectors is the same hazard;
//   - source store absent (never built here, or already reaped): nothing to
//     move — the new slug re-ingests from archives (round-3 step 3b / M-4);
//   - otherwise rename index/<from>/ → index/<to>/ in one rename(2);
//   - rewrite `wing` <from>→<to> on every chunk that still names <from>, and the
//     ledger's archive_path path segment, both whole-file and epoch-changing;
//   - REMOVE embed-cache/<from>/ (M0 = rebuild; never rename it).
//
// It returns whether it moved a store.
func (tx *Tx) AdoptRenamedStore(from string) (renamed bool, err error) {
	if !tx.Held() {
		return false, errors.New("indexstore: AdoptRenamedStore on a finished Tx")
	}
	to := tx.project
	if from == to {
		return false, fmt.Errorf("indexstore: AdoptRenamedStore: from and to are both %q", from)
	}
	fromDir, err := tx.vault.IndexDir(from)
	if err != nil {
		return false, err
	}
	toDir := tx.files.dir // index/<to>/, as Lock resolved it

	fromHas, toHas := dirExists(fromDir), dirExists(toDir)
	switch {
	case fromHas && toHas:
		return false, fmt.Errorf("indexstore: both index/%s/ and index/%s/ exist; refusing to merge two stores — run `vp index rebuild %s`", from, to, to)
	case fromHas && !toHas:
		// The embed cache of the TARGET must not already hold vectors (a store the
		// rename would clobber/merge).
		if has, err := embedCacheHasVectors(tx.vault, to); err != nil {
			return false, err
		} else if has {
			return false, fmt.Errorf("indexstore: embed-cache/%s/ already holds vectors; refusing to adopt over it — run `vp index rebuild %s`", to, to)
		}
		if err := tx.beginDestructive(); err != nil {
			return false, err
		}
		if err := renameDir(fromDir, toDir); err != nil {
			return false, err
		}
		tx.noteWrite(true)
		renamed = true
		if err := adoptStep("renamed"); err != nil {
			return renamed, err
		}
	default:
		// !fromHas: source never built here or already reaped. index/<to>/ may
		// exist (an earlier run already renamed it) or not (nothing to adopt; the
		// new slug re-ingests). Either way fall through to the idempotent rewrites
		// and the cache removal.
	}

	if dirExists(toDir) {
		if err := tx.rewriteWingLabels(from, to); err != nil {
			return renamed, fmt.Errorf("rewrite wing: %w", err)
		}
		if err := adoptStep("wing"); err != nil {
			return renamed, err
		}
		if err := tx.rewriteLedgerArchivePath(from, to); err != nil {
			return renamed, fmt.Errorf("rewrite archive_path: %w", err)
		}
		if err := adoptStep("archive"); err != nil {
			return renamed, err
		}
	}

	// M0 = REBUILD: remove the old slug's embed cache; the new slug re-embeds
	// lazily. Absent = already handled (round-2 L4: the orphan reap may have
	// taken it first).
	cacheFrom, err := tx.vault.EmbedCacheDir(from)
	if err != nil {
		return renamed, err
	}
	if dirExists(cacheFrom) {
		if err := removeTree(cacheFrom); err != nil {
			return renamed, fmt.Errorf("remove embed-cache/%s/: %w", from, err)
		}
	}
	if err := adoptStep("cache"); err != nil {
		return renamed, err
	}
	return renamed, nil
}

// rewriteWingLabels relabels every chunk whose wing is still `from` to `to`,
// through Tx.Rewrite (whole-file, epoch-changing). Idempotent: once rewritten,
// no chunk names `from` and the change set is empty.
func (tx *Tx) rewriteWingLabels(from, to string) error {
	f, err := tx.readChunkFold()
	if err != nil {
		return err
	}
	changes := map[string]Labels{}
	for id, c := range f.chunks {
		if c.Wing == from {
			changes[id] = Labels{Wing: to}
		}
	}
	return tx.Rewrite(changes)
}

// rewriteLedgerArchivePath rewrites the slug path segment in each ledger line's
// archive_path (display-only; nothing opens an archive through it). It edits RAW
// lines, so every byte of a line without the old segment is preserved, and only
// the moved path changes. Idempotent.
func (tx *Tx) rewriteLedgerArchivePath(from, to string) error {
	lines, _, _, err := readLines(tx.files.ledger)
	if err != nil {
		return err
	}
	repl := []struct{ old, new string }{
		{"/Projects/" + from + "/", "/Projects/" + to + "/"},
		{"/palace/" + from + "/", "/palace/" + to + "/"},
	}
	changed := false
	out := make([][]byte, 0, len(lines))
	for _, l := range lines {
		s := string(l)
		n := s
		for _, r := range repl {
			n = strings.ReplaceAll(n, r.old, r.new)
		}
		if n != s {
			changed = true
		}
		out = append(out, []byte(n))
	}
	if !changed {
		return nil
	}
	var buf bytes.Buffer
	for _, l := range out {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := tx.beginDestructive(); err != nil {
		return err
	}
	if err := writeFile(tx.files.ledger, buf.Bytes()); err != nil {
		return err
	}
	tx.noteWrite(true)
	return nil
}

// embedCacheHasVectors reports whether project's embed cache holds any *.vec.
func embedCacheHasVectors(vault *storage.Vault, project string) (bool, error) {
	dir, err := vault.EmbedCacheDir(project)
	if err != nil {
		return false, err
	}
	var found bool
	werr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".vec") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, werr
}

// RenameHostLocalOutcome is what AdoptRenamedProject / UndoRenamedProject did.
type RenameHostLocalOutcome string

const (
	// RenameHostLocalDone: the store was moved (or there was none to move) and
	// the rewrites and record removal completed.
	RenameHostLocalDone RenameHostLocalOutcome = "done"
	// RenameHostLocalBusy: the run lock or a commit lock was held; the operator
	// re-runs the step (the rename-pending record keeps index/<old>/ meanwhile).
	RenameHostLocalBusy RenameHostLocalOutcome = "busy"
)

// RenameHostLocalResult reports the host-local step.
type RenameHostLocalResult struct {
	Outcome        RenameHostLocalOutcome `json:"outcome"`
	IndexRenamed   bool                   `json:"index_renamed"`
	ImportsCarried bool                   `json:"imports_carried"`
	Holder         string                 `json:"holder,omitempty"` // the run-lock holder, when busy
}

// AdoptRenamedProject is the host-local rename step. It runs on the renaming
// host after storage.ApplyRename returns, from the tool/CLI layer (storage
// cannot import indexstore). It holds no lock when called and holds no vault
// root lock (round-2 M3).
func AdoptRenamedProject(ctx context.Context, vault *storage.Vault, from, to string) (RenameHostLocalResult, error) {
	var res RenameHostLocalResult
	rl, ok, err := TryRunLock(vault, KindLifecycle, from)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Outcome = RenameHostLocalBusy
		if h, herr := ReadHolder(vault); herr == nil {
			res.Holder = h.Project
		}
		return res, nil
	}
	defer func() { _ = rl.Release() }()

	// Drain the old store so any Tx taken before the commit has finished; a later
	// Lock(<from>) returns ErrProjectGone (Projects/<from> is gone).
	if lt, lerr := LockLifecycle(ctx, vault, from, LifecycleRenameTimeout); lerr != nil {
		if errors.Is(lerr, vaultlock.ErrLockWaitTimeout) {
			res.Outcome = RenameHostLocalBusy
			return res, nil
		}
		return res, lerr
	} else if eerr := lt.ChangeEpoch(); eerr != nil {
		_ = lt.Release()
		return res, eerr
	} else if rerr := lt.Release(); rerr != nil {
		return res, rerr
	}

	// Move and rewrite under the NEW slug's commit lock.
	tx, err := Lock(ctx, vault, to, LifecycleRenameTimeout)
	if err != nil {
		if errors.Is(err, vaultlock.ErrLockWaitTimeout) {
			res.Outcome = RenameHostLocalBusy
			return res, nil
		}
		return res, err
	}
	renamed, aerr := tx.AdoptRenamedStore(from)
	if aerr != nil {
		_ = tx.Release()
		return res, aerr
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.IndexRenamed = renamed

	// Carry the importer marker dir (host-local hint; a re-scan is the only cost
	// if it is lost).
	carried, cerr := carryImportsDir(vault, from, to)
	if cerr != nil {
		return res, cerr
	}
	res.ImportsCarried = carried

	// The step is done: drop the rename-pending record so the next sweep reaps
	// nothing and logs no warning.
	if err := vault.RemoveRenamePending(from); err != nil {
		return res, err
	}
	res.Outcome = RenameHostLocalDone
	return res, nil
}

// UndoRenamedProject reverses the host-local step, after the tracked rename
// commit has been reverted (Projects/<from> is back and Projects/<to> is gone).
// It moves the store back from <to> to <from>, rewrites the labels the other
// way, carries the importer dir back, and drops the rename-pending record. An
// absent index/<to>/ (a sweep reaped it in the window) is treated as already
// handled: <from> re-ingests from its archives (round-3 M-4).
func UndoRenamedProject(ctx context.Context, vault *storage.Vault, from, to string) (RenameHostLocalResult, error) {
	var res RenameHostLocalResult
	fromExists, err := vault.ProjectExists(from)
	if err != nil {
		return res, err
	}
	toExists, err := vault.ProjectExists(to)
	if err != nil {
		return res, err
	}
	if !fromExists || toExists {
		return res, fmt.Errorf("indexstore: refusing to undo the host-local rename: revert the rename commit first (Projects/%s must be present and Projects/%s gone)", from, to)
	}
	rl, ok, err := TryRunLock(vault, KindLifecycle, from)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Outcome = RenameHostLocalBusy
		if h, herr := ReadHolder(vault); herr == nil {
			res.Holder = h.Project
		}
		return res, nil
	}
	defer func() { _ = rl.Release() }()

	// Drain the new store (now gone from the vault after the revert).
	if lt, lerr := LockLifecycle(ctx, vault, to, LifecycleRenameTimeout); lerr != nil {
		if errors.Is(lerr, vaultlock.ErrLockWaitTimeout) {
			res.Outcome = RenameHostLocalBusy
			return res, nil
		}
		return res, lerr
	} else if eerr := lt.ChangeEpoch(); eerr != nil {
		_ = lt.Release()
		return res, eerr
	} else if rerr := lt.Release(); rerr != nil {
		return res, rerr
	}

	tx, err := Lock(ctx, vault, from, LifecycleRenameTimeout)
	if err != nil {
		if errors.Is(err, vaultlock.ErrLockWaitTimeout) {
			res.Outcome = RenameHostLocalBusy
			return res, nil
		}
		return res, err
	}
	// Adopt <to>'s store back into <from>: the same under-lock core with the
	// slugs swapped (tx is the <from> Tx).
	renamed, aerr := tx.AdoptRenamedStore(to)
	if aerr != nil {
		_ = tx.Release()
		return res, aerr
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.IndexRenamed = renamed

	carried, cerr := carryImportsDir(vault, to, from)
	if cerr != nil {
		return res, cerr
	}
	res.ImportsCarried = carried

	if err := vault.RemoveRenamePending(from); err != nil {
		return res, err
	}
	res.Outcome = RenameHostLocalDone
	return res, nil
}

// carryImportsDir moves palace/.local/imports/<from>/ to <to>/ in one
// rename(2). Absent source: nothing to carry. Target already present: left
// alone (idempotent, and never merged).
func carryImportsDir(vault *storage.Vault, from, to string) (bool, error) {
	src, err := vault.ImportsDir(from)
	if err != nil {
		return false, err
	}
	dst, err := vault.ImportsDir(to)
	if err != nil {
		return false, err
	}
	if !dirExists(src) || dirExists(dst) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, fmt.Errorf("create imports parent: %w", err)
	}
	if err := renameDir(src, dst); err != nil {
		return false, err
	}
	return true, nil
}

// LifecycleRenameTimeout is how long the host-local rename step waits for a
// commit lock before reporting busy.
const LifecycleRenameTimeout = 30 * time.Second
