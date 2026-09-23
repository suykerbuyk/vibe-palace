// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// RecordDeparture writes the tracked record that slug LEFT this vault —
// renamed to another slug (kind departure.Renamed, to = the new slug) or moved
// to another vault (departure.MovedToVault, to = an optional label, never a
// host path) — at Audits/departures/<slug>.json, and returns that
// vault-relative path.
//
// 🔴 IT DOES NOT COMMIT, AND THE CALLER MUST. The record belongs in the SAME
// commit as the departure itself (a rename's move commit, a split purge's
// deletions): committed alone it would claim a departure that another host
// has not seen happen, and left uncommitted it is dirt that tidy reports
// rather than sweeps. The returned path is for the caller's path list.
//
// It refuses while Projects/<slug>/ still exists: a departure is recorded
// after the tree is gone, never before, so a record can never describe a
// project that is still here. An existing record is overwritten — the slug was
// re-created (vp init) and has now departed again; git keeps the history.
//
// THE WRITER OWNS THE CLOCK (clock.go): the date is this process's calendar
// day, and base_commit is the vault's HEAD at the time of writing (the commit
// the departure is made against — a commit cannot name its own SHA, and
// `git log -- <path>` recovers the departing one). Neither is a parameter.
func (v *Vault) RecordDeparture(slug string, kind departure.Kind, to string) (string, error) {
	rec := departure.Record{Slug: slug, Kind: kind, To: to, Date: v.CalendarDay(time.Now())}
	if err := rec.Validate(); err != nil {
		return "", err
	}
	projDir, err := v.ProjectDir(slug)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(projDir); err == nil {
		return "", fmt.Errorf("refusing to record a departure of %q: Projects/%s/ still exists", slug, slug)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("refusing to record a departure of %q: cannot inspect Projects/%s/: %w", slug, slug, err)
	}
	// base_commit only from the vault's OWN repository: an enclosing repo's
	// HEAD would be a fact about a different history.
	if state, _ := InspectVaultGit(v.Root); state == VaultGitOK {
		if head, err := gitCmd(v.Root, 10*time.Second, "rev-parse", "HEAD"); err == nil {
			rec.BaseCommit = head
		}
	}
	data, err := rec.Encode()
	if err != nil {
		return "", err
	}
	rel := departure.RelPath(slug)
	abs := filepath.Join(v.Root, filepath.FromSlash(rel))
	release, err := vaultlock.Acquire(v.Root, abs)
	if err != nil {
		return "", fmt.Errorf("lock %s: %w", rel, err)
	}
	defer release()
	if err := atomicfile.Write(v.Root, abs, data, atomicfile.WithFsync()); err != nil {
		return "", fmt.Errorf("write %s: %w", rel, err)
	}
	return rel, nil
}
