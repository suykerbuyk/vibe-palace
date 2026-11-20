// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// The status report must carry everything the one TidyScan classified. Before
// this, DirtJSON dropped Deferred, Orphaned and LeftDeparted, so a tree whose
// only dirt was an orphaned transcript read as clean.

const freshDeferredZst = "Projects/p/transcripts/fresh.jsonl.zst"

func TestBuildStatusReport_CarriesOrphanedDeferredAndLeftDeparted(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.MovedToVault, "git@example.invalid:q/v.git", nil)
	gitRun(t, b, "pull", "-q", "origin", "main")

	const stray = "Projects/old/sessions/2026-09-27-aaaa-01.md"
	writeFile(t, b, orphanZst, "zst\n")
	ageFile(t, b, orphanZst, staleAge)
	writeFile(t, b, freshDeferredZst, "zst\n") // mtime = now
	writeFile(t, b, stray, "a stale session\n")
	for _, p := range []string{orphanZst, freshDeferredZst, stray} { // non-vacuity
		if !untrackedListed(t, b, p) {
			t.Fatalf("fixture: %s is not an untracked entry", p)
		}
	}

	rep, err := BuildStatusReport(b, false)
	if err != nil {
		t.Fatal(err)
	}
	d := rep.Dirt
	if !hasPath(d.Orphaned, orphanZst) {
		t.Errorf("Dirt.Orphaned = %v, want %s", d.Orphaned, orphanZst)
	}
	if !hasPath(d.Deferred, freshDeferredZst) {
		t.Errorf("Dirt.Deferred = %v, want %s", d.Deferred, freshDeferredZst)
	}
	if len(d.LeftDeparted) != 1 || !strings.HasPrefix(d.LeftDeparted[0], stray+":") {
		t.Errorf("Dirt.LeftDeparted = %v, want the departed stray %s", d.LeftDeparted, stray)
	}
	if d.OrphanedNotice == "" || d.OrphanedNotice != OrphanedNotice(d.Orphaned) || !strings.Contains(d.OrphanedNotice, orphanZst) {
		t.Errorf("Dirt.OrphanedNotice must be OrphanedNotice(Orphaned) and name the path:\n%s", d.OrphanedNotice)
	}
	if d.Clean() {
		t.Error("a tree holding orphaned, deferred and left-departed entries reported Clean()")
	}

	// One classifier: the report carries exactly what TidyScan returns.
	scan, err := TidyScan(b)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2][]string{
		"Swept":        {d.Swept, scan.Swept},
		"Reported":     {d.Reported, scan.Reported},
		"Deferred":     {d.Deferred, scan.Deferred},
		"Orphaned":     {d.Orphaned, scan.Orphaned},
		"LeftDeparted": {d.LeftDeparted, scan.LeftDeparted},
	} {
		if !slices.Equal(pair[0], pair[1]) {
			t.Errorf("%s: report %v != TidyScan %v", name, pair[0], pair[1])
		}
	}
}

// The case in the task: nothing swept, nothing reported, one orphan.
func TestBuildStatusReport_OrphanOnlyTreeIsNotClean(t *testing.T) {
	dir := initTestRepo(t)
	if rep, err := BuildStatusReport(dir, false); err != nil || !rep.Dirt.Clean() {
		t.Fatalf("control: a clean repo must report Clean() (err=%v, dirt=%+v)", err, rep.Dirt)
	}
	writeFile(t, dir, orphanZst, "zst\n")
	ageFile(t, dir, orphanZst, staleAge)
	if !untrackedListed(t, dir, orphanZst) {
		t.Fatal("fixture: the orphan is not an untracked entry")
	}
	rep, err := BuildStatusReport(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Dirt.Swept) != 0 || len(rep.Dirt.Reported) != 0 {
		t.Fatalf("fixture must be orphan-ONLY: swept=%v reported=%v", rep.Dirt.Swept, rep.Dirt.Reported)
	}
	if rep.Dirt.Clean() || !hasPath(rep.Dirt.Orphaned, orphanZst) {
		t.Errorf("orphan-only tree: Clean()=%v Orphaned=%v", rep.Dirt.Clean(), rep.Dirt.Orphaned)
	}
}

// Clean() table. Each of the five terms alone makes a tree not clean.
//
// ReportedUserContent has no row ON PURPOSE: it is a SUBSET of Reported
// (TidyResult documents "every path in ReportedUserContent is also present in
// Reported"), so a tree with user-memory dirt always has Reported non-empty.
// Adding a ReportedUserContent term to Clean() would be redundant, and removing
// the subset rule would make this table incomplete — change both together.
func TestDirtJSONClean(t *testing.T) {
	one := []string{"x"}
	for name, d := range map[string]DirtJSON{
		"swept":         {Swept: one},
		"reported":      {Reported: one},
		"deferred":      {Deferred: one},
		"orphaned":      {Orphaned: one},
		"left_departed": {LeftDeparted: one},
	} {
		if d.Clean() {
			t.Errorf("%s alone: Clean() = true, want false", name)
		}
	}
	if !(DirtJSON{}).Clean() {
		t.Error("the zero DirtJSON must be clean")
	}
}
