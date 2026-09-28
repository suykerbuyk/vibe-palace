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
	rel, _, err := v.writeDeparture(nil, departure.Record{Slug: slug, Kind: kind, To: to}, true)
	return rel, err
}

// RecordDepartureForPurge is RecordDeparture for a split purge, the one writer
// that records a departure BEFORE the trees are gone: the purge writes its
// records, then removes the tracked files and commits both in one commit
// (storage.CommitSplitPurge). Written first, an interrupted purge leaves an
// uncommitted record behind, and the commit guard then refuses every other
// vp commit until the purge is finished or undone — a record written last
// would leave a crash's half-removal unguarded. created reports whether the
// file did not exist before, which is what the purge's rollback needs: a
// created record is removed, an overwritten (committed) one is restored from
// HEAD.
func (v *Vault) RecordDepartureForPurge(slug string, kind departure.Kind, to string) (rel string, created bool, err error) {
	return v.writeDeparture(nil, departure.Record{Slug: slug, Kind: kind, To: to}, false)
}

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
