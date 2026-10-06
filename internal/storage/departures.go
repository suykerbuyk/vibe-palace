// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The generic record writers RecordDeparture (requireAbsent) and
// RecordDepartureForPurge (write-before-removal) were retired in U12 with the
// split/merge tools that were their only production callers. The surviving
// writers are RecordDepartureForDelete and RecordDepartureForRename below, and
// the lower-level writeDeparture they funnel through.

// DepartureFacts are what a project delete verified and records beside the
// departure: the destination's copy commit (moved-to-vault only, and
// informational), the departing footprint digest, "v1:<hex>", and the
// generation of the destination's own record for the slug (moved-to-vault
// only; 0 when the destination holds none), read from its snapshot.
type DepartureFacts struct {
	CopyCommit            string
	Footprint             string
	DestinationGeneration int
}

// RecordDepartureForDelete is RecordDepartureForPurge for `vp vault project
// delete`: written before the footprint is removed, committed with that
// removal, and carrying the lifecycle fields. kind is departure.MovedToVault
// (to = the destination's remote URL) or departure.Deleted (to = "").
//
// THE WRITER DERIVES THE GENERATION, and it is monotonic for the project
// across every vault it has lived in: one more than the highest generation of
// ANY version of slug's record in HEAD's history (a record from before the
// field existed counts as 1), and, for a moved-to-vault departure, one more
// than the destination's own record (f.DestinationGeneration) when that is
// higher. History, not HEAD's tree, so a revert that removed the record and a
// re-delete never write the same generation twice. It reads committed history,
// never the working tree, so a re-run over a crashed run's own uncommitted
// record derives the same number. See DepartureGeneration for what refuses and
// what is only a warning; the warnings are returned for the caller to print.
//
// held is the delete's own root-lock token (it holds the vault for the whole
// run): the record is written through vaultfs.WriteDepartureRecord under it.
func (v *Vault) RecordDepartureForDelete(held *vaultlock.Held, slug string, kind departure.Kind, to string, f DepartureFacts) (rel string, created bool, warnings []string, err error) {
	if held == nil {
		return "", false, nil, fmt.Errorf("a project delete records its departure under its own root-lock token")
	}
	gen, warnings, err := v.DepartureGeneration(slug, kind, f.DestinationGeneration)
	if err != nil {
		return "", false, nil, err
	}
	rel, created, err = v.writeDeparture(held, departure.Record{
		Slug: slug, Kind: kind, To: to,
		Generation: gen, CopyCommit: f.CopyCommit, Footprint: f.Footprint,
	}, false)
	return rel, created, warnings, err
}

// RecordDepartureForRename is RecordDepartureForPurge for `vp vault rename`:
// the renamed slug's record (kind departure.Renamed, to = the new slug) is
// written before the move's commit and committed with it, in the same commit
// (commitRenameLocked). requireAbsent is false — by the time the rename
// commits, Projects/<old>/ has been moved to Projects/<new>/, so the old tree
// is already gone, exactly as a purge records after its removal.
//
// THE WRITER DERIVES THE GENERATION, monotonic for the project across every
// vault it has lived in: one more than the highest generation of any version of
// the slug's record in HEAD's history (highestDepartureGeneration). A rename is
// in-vault, so there is no destination generation. It reads committed history,
// never the working tree, so a re-run over a crashed run's own uncommitted
// record derives the same number — and a revert of the rename followed by a
// re-rename never writes the same generation twice. warnings name older
// unreadable versions skipped during the walk.
//
// It calls highestDepartureGeneration directly, not DepartureGeneration: the
// latter's kind guard admits only MovedToVault/Deleted, and widening it is
// unnecessary (highestDepartureGeneration has no kind guard).
//
// held is the rename's own root-lock token, held for the whole run.
func (v *Vault) RecordDepartureForRename(held *vaultlock.Held, slug, to string) (rel string, created bool, warnings []string, err error) {
	if held == nil {
		return "", false, nil, fmt.Errorf("a rename records its departure under its own root-lock token")
	}
	highest, warnings, err := v.highestDepartureGeneration(slug)
	if err != nil {
		return "", false, nil, err
	}
	rel, created, err = v.writeDeparture(held, departure.Record{
		Slug: slug, Kind: departure.Renamed, To: to, Generation: highest + 1,
	}, false)
	return rel, created, warnings, err
}

// DepartureGeneration is the generation RecordDepartureForDelete would write
// for slug, without writing: what the dry run shows, with its warnings.
//
// HEAD's version of the record must parse, or it refuses. An OLDER version
// that cannot be parsed is skipped with a warning ("skipped unreadable record
// at <sha>"), never a refusal: history is immutable, so refusing on it would
// block every later delete of the project with no recovery.
func (v *Vault) DepartureGeneration(slug string, kind departure.Kind, destinationGeneration int) (gen int, warnings []string, err error) {
	if kind != departure.MovedToVault && kind != departure.Deleted {
		return 0, nil, fmt.Errorf("a project delete records kind %q or %q, not %q", departure.MovedToVault, departure.Deleted, kind)
	}
	if destinationGeneration < 0 || (destinationGeneration > 0 && kind != departure.MovedToVault) {
		return 0, nil, fmt.Errorf("a destination generation (%d) is only for a %q departure with a destination record",
			destinationGeneration, departure.MovedToVault)
	}
	highest, warnings, err := v.highestDepartureGeneration(slug)
	if err != nil {
		return 0, nil, err
	}
	return max(highest, destinationGeneration) + 1, warnings, nil
}

// highestDepartureGeneration is the highest generation of any version of
// slug's record in HEAD's history, 0 when there has never been one.
// --full-history, so a version that only lived on a merged side branch counts.
// HEAD's version must parse; an older unreadable one is a warning.
func (v *Vault) highestDepartureGeneration(slug string) (int, []string, error) {
	if err := (departure.Record{Slug: slug, Kind: departure.Deleted}).Validate(); err != nil {
		return 0, nil, err
	}
	if _, err := gitCmd(v.Root, 10*time.Second, "rev-parse", "--verify", "-q", "HEAD^{commit}"); err != nil {
		return 0, nil, nil // an unborn vault has no history
	}
	rel := departure.RelPath(slug)
	if blob, found, err := ReadCommittedBlob(v.Root, rel); err != nil {
		return 0, nil, fmt.Errorf("read the committed %s: %w", rel, err)
	} else if found {
		if rec := departure.Parse(slug, blob); rec.Malformed != "" {
			return 0, nil, fmt.Errorf("refusing to record a departure of %q: HEAD's %s cannot be read (%s), so its generation is unknown; repair it in a commit first",
				slug, rel, rec.Malformed)
		}
	}
	out, err := gitCmd(v.Root, 60*time.Second, "log", "--full-history", "--format=%H", "HEAD", "--", rel)
	if err != nil {
		return 0, nil, fmt.Errorf("walk the history of %s: %w", rel, err)
	}
	var warnings []string
	highest := 0
	for sha := range strings.SplitSeq(out, "\n") {
		if sha = strings.TrimSpace(sha); sha == "" {
			continue
		}
		blob, found, err := committedBlobAt(v.Root, sha, rel)
		if err != nil {
			return 0, nil, fmt.Errorf("read %s at %s: %w", rel, shortSHA(sha), err)
		}
		if !found {
			continue // this commit removed the record
		}
		rec := departure.Parse(slug, blob)
		if rec.Malformed != "" {
			warnings = append(warnings, fmt.Sprintf("skipped unreadable record at %s (%s)", sha, rec.Malformed))
			continue
		}
		highest = max(highest, rec.Generation, 1)
	}
	return highest, warnings, nil
}

// writeDeparture stores rec, adding the date and base commit. The caller sets
// every other field.
// writeDeparture stores rec through vaultfs.WriteDepartureRecord, the one
// entry point for records, under held — the caller's root-lock token, or, when
// held is nil, one taken here (the caller must then not hold the root lock).
func (v *Vault) writeDeparture(held *vaultlock.Held, rec departure.Record, requireAbsent bool) (string, bool, error) {
	slug := rec.Slug
	rec.Date = v.CalendarDay(time.Now())
	if err := rec.Validate(); err != nil {
		return "", false, err
	}
	projDir, err := v.ProjectDir(slug)
	if err != nil {
		return "", false, err
	}
	if requireAbsent {
		if _, err := os.Lstat(projDir); err == nil {
			return "", false, fmt.Errorf("refusing to record a departure of %q: Projects/%s/ still exists", slug, slug)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("refusing to record a departure of %q: cannot inspect Projects/%s/: %w", slug, slug, err)
		}
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
		return "", false, err
	}
	rel := departure.RelPath(slug)
	if held == nil {
		h, err := vaultlock.AcquireHeld(v.Root, v.Root)
		if err != nil {
			return "", false, fmt.Errorf("acquire vault commit lock: %w", err)
		}
		defer h.Release()
		held = h
	}
	created, err := vaultfs.WriteDepartureRecord(held, slug, data)
	if err != nil {
		return "", false, err
	}
	return rel, created, nil
}

// committedBlobAt is ReadCommittedBlob at rev instead of HEAD.
func committedBlobAt(vaultPath, rev, rel string) ([]byte, bool, error) {
	oid, found, err := treeEntryOID(vaultPath, rev, rel)
	if err != nil || !found {
		return nil, found, err
	}
	blob, err := gitBlob(vaultPath, oid)
	return blob, err == nil, err
}
