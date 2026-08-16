// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// Iteration headings — the archive's headings against the header the WRITER
// would have emitted.
//
// The defect this exists for is not cosmetic. storage.(*Vault).AppendIterationOwned
// composes every entry as "\n---\n" + FormatIterationHeader(n, title) + body, and
// the reader half only ever recognised "## Iteration N". A heading that sits on
// that frame but is not that shape is a REAL entry boundary the reader cannot
// see, so the narrative under it is unaddressable by number AND is silently
// served as the tail of the previous entry: measured live, vp_get_iteration for
// N=108, 110, 125, 128, 145 and 154 each over-returned exactly that way and
// reported success. A reader that over-returns while claiming to be exact is
// worse than one that fails, because nothing downstream can tell.
//
// # Why this is a `check` producer and not only an audit dimension
//
// vaultaudit.auditIterationHeadings covers the same ground, but the vault audit
// is a deliberate, occasional, human-driven pass. The check registry is what
// vp_check and the restart/wrap flows reach on every session, on every host,
// including the shell-less ones — so a rule that lives only in the audit reaches
// the operator and nobody else. Both call ONE scan
// (wrapstate.ScanHeadingDefects); a second private copy of the conditions is the
// exact defect class the contract file exists to prevent.
//
// # Advisory, never Fail
//
// Repairing a heading is a WRITE to the archive and a judgement call — an
// unnumbered framed orphan has no recoverable N, and deciding which iteration
// its narrative belongs to is a human's call, not a check's. This check never
// writes, moves or repairs anything.

// CheckIterationHeadings scans every project's iterations.md and reports the
// headings that are not what FormatIterationHeader would have emitted, in three
// independent classes (see wrapstate.ScanHeadingDefects): frame orphans,
// non-canonical numbered headings, and doubled "Iteration N —" prefixes.
//
// All three are required and none subsumes the others. The frame rule cannot see
// a malformed numbered heading that sits mid-body; the canonicity rule cannot
// see an unnumbered orphan; and NEITHER can see a doubled prefix, because
// titleFromHeader strips exactly one prefix and FormatIterationHeader re-adds
// exactly one, making the corruption a fixed point of the round trip.
//
// It is strictly READ-ONLY: Pass when every scanned archive is clean, Info when
// one or more carry a defective heading, Skip when no vault root is configured.
// Absence is never a violation — a missing Projects/ dir and a project with no
// iterations.md both report nothing.
//
// It does NOT report duplicate iteration numbers. That rule existed, invented 17
// findings on its first live run against a deliberate operator pattern
// (multi-work-unit sessions write addendum narratives under one number), and was
// deleted. An auditor that invents findings is worse than one that misses them,
// because it teaches you to wave off the real ones.
func CheckIterationHeadings(v *storage.Vault) Result {
	r := Result{Name: "Iteration headings"}

	if v.Root == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}

	projectsDir := filepath.Join(v.Root, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	type projectDefects struct {
		slug    string
		defects []wrapstate.HeadingDefect
	}

	scanned := 0
	total := 0
	var dirty []projectDefects
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(projectsDir, name, "iterations.md"))
		if rerr != nil {
			// A project with no narrative archive yet is normal, not a defect.
			continue
		}
		scanned++
		defects := wrapstate.ScanHeadingDefects(string(data))
		if len(defects) == 0 {
			continue
		}
		total += len(defects)
		dirty = append(dirty, projectDefects{slug: name, defects: defects})
	}

	if len(dirty) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d iteration archives match the writer's header contract", scanned)
		return r
	}

	sort.Slice(dirty, func(i, j int) bool { return dirty[i].slug < dirty[j].slug })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d defective headings across %d of %d iteration archives",
		total, len(dirty), scanned)
	for _, p := range dirty {
		for _, d := range p.defects {
			// vault-RELATIVE path: the vault syncs to every machine and lives
			// somewhere different on each, so an absolute path in a report is a
			// fact about the host that printed it and false everywhere else.
			r.Details = append(r.Details, fmt.Sprintf("  Projects/%s/iterations.md:%d [%s] %s → expected %s",
				p.slug, d.Line, d.Class, d.Text, d.Want))
		}
	}
	r.Details = append(r.Details,
		"An H2 on the writer's \"---\" frame that FormatIterationHeader would not emit is a REAL entry",
		"boundary the reader cannot see: its narrative is unaddressable by number and is served as the",
		"TAIL of the previous entry (vp_get_iteration over-returned exactly that way for 108, 110, 125,",
		"128, 145 and 154, reporting success). A non-canonical numbered heading is addressable but not",
		"what the writer emits; a doubled \"Iteration N —\" prefix passes the round-trip oracle and is",
		"invisible to both other rules, which is why all three are checked.",
		"Repair is a WRITE and a judgement call — an unnumbered orphan has no recoverable N — so this",
		"check reports and never rewrites. Duplicate iteration numbers are DELIBERATE (addendum",
		"narratives) and are not reported: that rule invented 17 findings once and was deleted.")
	return r
}
