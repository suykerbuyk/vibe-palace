// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package departure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func record(t *testing.T, root string, r Record) {
	t.Helper()
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, RelPath(r.Slug), string(b))
}

// T1. A record with Projects/<slug>/ absent is a departure, read back whole.
func TestRead_RecordWithDirAbsentIsDeparted(t *testing.T) {
	root := t.TempDir()
	record(t, root, Record{Slug: "old", Kind: Renamed, To: "new", Date: "2026-09-23", BaseCommit: "abc"})

	rec, ok := Find(root, "old")
	if !ok {
		t.Fatal("a recorded slug with no Projects/old/ must be departed")
	}
	if rec.Kind != Renamed || rec.To != "new" || rec.Date != "2026-09-23" || rec.BaseCommit != "abc" || rec.Malformed != "" {
		t.Errorf("record read back wrong: %+v", rec)
	}
	if got := List(root); len(got) != 1 || got[0].Slug != "old" {
		t.Errorf("List = %+v, want the one departure", got)
	}
}

// G2. The directory wins: a re-scaffolded project is not departed, whatever
// its old record says, and List leaves it out.
func TestRead_DirPresentIsNotDeparted(t *testing.T) {
	root := t.TempDir()
	record(t, root, Record{Slug: "old", Kind: Renamed, To: "new", Date: "2026-09-23"})
	put(t, root, "Projects/old/commands/README.md", "re-inited\n")

	if _, ok := Find(root, "old"); ok {
		t.Error("Projects/old/ exists: the slug is not departed")
	}
	if got := List(root); len(got) != 0 {
		t.Errorf("List = %+v, want none", got)
	}
	if _, ok := Find(root, "never"); ok {
		t.Error("no record and no directory is not a departure")
	}
}

// T2. A record that cannot be parsed, names another slug, or has an unknown
// kind still means departed — the file exists only because something did —
// and says why it could not be read.
func TestRead_MalformedRecordStillDeparts(t *testing.T) {
	root := t.TempDir()
	put(t, root, RelPath("garbled"), "{not json")
	put(t, root, RelPath("wrongname"), `{"format":"vp-departure/1","slug":"other","kind":"renamed","to":"x"}`)
	put(t, root, RelPath("oddkind"), `{"format":"vp-departure/1","slug":"oddkind","kind":"exploded","to":"x"}`)
	put(t, root, Dir+"/README.txt", "not a record\n")

	for _, s := range []string{"garbled", "wrongname", "oddkind"} {
		rec, ok := Find(root, s)
		if !ok {
			t.Errorf("%s: a malformed record must still mean departed", s)
			continue
		}
		if rec.Malformed == "" {
			t.Errorf("%s: Malformed must say why the record could not be used", s)
		}
	}
	if got := List(root); len(got) != 3 {
		t.Errorf("List must hold the three malformed departures and skip the stray file, got %+v", got)
	}
}

// T3. A rename chain resolves to its live end; a cycle stops instead of
// looping, and still answers with the last hop reached.
func TestResolve_FollowsChainAndStopsOnCycle(t *testing.T) {
	root := t.TempDir()
	record(t, root, Record{Slug: "a", Kind: Renamed, To: "b"})
	record(t, root, Record{Slug: "b", Kind: Renamed, To: "c"})
	put(t, root, "Projects/c/resume.md", "live\n")

	chain, ok := Resolve(root, "a")
	if !ok || len(chain) != 2 || chain[len(chain)-1].To != "c" {
		t.Fatalf("Resolve(a) = %+v, want a->b, b->c", chain)
	}

	record(t, root, Record{Slug: "x", Kind: Renamed, To: "y"})
	record(t, root, Record{Slug: "y", Kind: Renamed, To: "x"})
	chain, ok = Resolve(root, "x")
	if !ok || len(chain) != 2 || chain[1].To != "x" {
		t.Fatalf("Resolve(x) on a cycle = %+v, want it to stop after one lap", chain)
	}
}

// A writer's record must never carry a host path, and must be a real
// departure.
func TestValidate_RefusesHostPathsAndSelfRenames(t *testing.T) {
	for _, r := range []Record{
		{Slug: "a", Kind: MovedToVault, To: "/home/x/vault"},
		{Slug: "a", Kind: MovedToVault, To: "~/vault"},
		{Slug: "a", Kind: MovedToVault, To: "~"},
		{Slug: "a", Kind: Renamed, To: "a"},
		{Slug: "a", Kind: MovedToVault, To: strings.Repeat("v", MaxLabelLen+1)},
		{Slug: "a", Kind: MovedToVault, To: "two\nlines"},
		{Slug: "a", Kind: MovedToVault, To: "a&b"},
		{Slug: "a", Kind: Renamed, To: "Not A Slug"},
		{Slug: "a", Kind: "exploded"},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("Validate(%+v) must refuse", r)
		}
	}
	for _, r := range []Record{
		{Slug: "a", Kind: MovedToVault},
		{Slug: "a", Kind: MovedToVault, To: "git@github.com:me/quantum-vault.git"},
		{Slug: "a", Kind: MovedToVault, To: "quantum vault"},
		{Slug: "a", Kind: MovedToVault, To: strings.Repeat("v", MaxLabelLen)},
		{Slug: "a", Kind: Renamed, To: "b"},
	} {
		if err := r.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want ok", r, err)
		}
	}
	b, err := (Record{Slug: "a", Kind: Renamed, To: "b"}).Encode()
	if err != nil || !strings.Contains(string(b), `"format": "`+Format+`"`) {
		t.Errorf("Encode must stamp the format: %s %v", b, err)
	}
}
