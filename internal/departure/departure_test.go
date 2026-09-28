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

// G2 (reversed by the U15 ruling). The record wins over the directory: a
// re-scaffolded project, content and all, is still departed while its record
// exists; a slug with no record never is.
func TestRead_RecordWinsOverTheDirectory(t *testing.T) {
	root := t.TempDir()
	record(t, root, Record{Slug: "old", Kind: Renamed, To: "new", Date: "2026-09-23"})
	put(t, root, "Projects/old/commands/README.md", "re-inited\n")

	if rec, ok := Find(root, "old"); !ok || rec.To != "new" {
		t.Error("a record over real content must still read as departed")
	}
	if got := List(root); len(got) != 1 || got[0].Slug != "old" {
		t.Errorf("List = %+v, want old", got)
	}
	if _, ok := Find(root, "never"); ok {
		t.Error("no record is never a departure")
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

// Parse is Read without the file: a pull reads a record straight out of a git
// object before it is merged. Same verdicts as Read for every shape.
func TestParse_SameVerdictsAsRead(t *testing.T) {
	good, err := (Record{Slug: "old", Kind: Renamed, To: "new", Date: "2026-09-23"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if rec := Parse("old", good); rec.Malformed != "" || rec.Kind != Renamed || rec.To != "new" {
		t.Errorf("Parse(good) = %+v", rec)
	}
	for name, body := range map[string]string{
		"garbled":   "{not json",
		"wrongname": `{"format":"vp-departure/1","slug":"other","kind":"renamed","to":"x"}`,
		"oddkind":   `{"format":"vp-departure/1","slug":"oddkind","kind":"exploded","to":"x"}`,
	} {
		if rec := Parse(name, []byte(body)); rec.Malformed == "" || rec.Slug != name {
			t.Errorf("Parse(%s) = %+v, want Malformed set and the slug kept", name, rec)
		}
	}
}

// A binary that predates kind "deleted" still treats the slug as departed. Its
// Parse had only renamed and moved-to-vault, so it marks the record Malformed
// — which every consumer reads as "departed, destination unreadable" — and its
// Find reports found for any record file that exists, whatever the kind.
func TestAnOlderBinaryReadsADeletedRecordAsDeparted(t *testing.T) {
	data, err := (Record{Slug: "old", Kind: Deleted, Date: "2026-09-27", Generation: 1,
		Footprint: "v1:" + strings.Repeat("ab", 32)}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	preDeleted := func(k Kind) bool { return k == Renamed || k == MovedToVault }
	old := parseKinds("old", data, preDeleted)
	if old.Slug != "old" || old.Kind != Deleted || !strings.Contains(old.Malformed, `unknown kind "deleted"`) {
		t.Fatalf("an older binary's parse = %+v; want slug old, kind deleted, Malformed set", old)
	}
	if cur := Parse("old", data); cur.Malformed != "" {
		t.Fatalf("this binary must understand kind deleted: %+v", cur)
	}

	root := t.TempDir()
	p := filepath.Join(root, filepath.FromSlash(RelPath("old")))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// A kind no binary knows yet goes down the same tolerant path.
	future := strings.Replace(string(data), `"deleted"`, `"merged-away"`, 1)
	if err := os.WriteFile(p, []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, departed := Find(root, "old")
	if !departed || rec.Malformed == "" {
		t.Fatalf("an unknown kind must read as departed and Malformed: departed=%v rec=%+v", departed, rec)
	}
}

// The lifecycle fields round-trip, are omitted when unset (so an existing
// writer's bytes do not change), and are validated.
func TestLifecycleFieldsRoundTripAndValidate(t *testing.T) {
	sha := strings.Repeat("0123456789", 4)
	fp := "v1:" + strings.Repeat("cd", 32)
	in := Record{Slug: "old", Kind: MovedToVault, To: "git@example.invalid:team/b.git", Date: "2026-09-27",
		Generation: 3, CopyCommit: sha, Footprint: fp}
	data, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"generation": 3`, `"copy_commit": "` + sha + `"`, `"footprint": "` + fp + `"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("encoded record lacks %s:\n%s", key, data)
		}
	}
	in.Format = Format
	if got := Parse("old", data); got != in {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, in)
	}

	plain, err := (Record{Slug: "old", Kind: MovedToVault, To: "q", Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generation", "copy_commit", "footprint"} {
		if strings.Contains(string(plain), key) {
			t.Errorf("an unset %s must be omitted:\n%s", key, plain)
		}
	}

	for name, r := range map[string]Record{
		"deleted names a destination": {Slug: "old", Kind: Deleted, To: "git@example.invalid:team/b.git"},
		"negative generation":         {Slug: "old", Kind: Deleted, Generation: -1},
		"copy commit on deleted":      {Slug: "old", Kind: Deleted, CopyCommit: sha},
		"copy commit on renamed":      {Slug: "old", Kind: Renamed, To: "new", CopyCommit: sha},
		"short copy commit":           {Slug: "old", Kind: MovedToVault, CopyCommit: "abc1234"},
		"upper-case copy commit":      {Slug: "old", Kind: MovedToVault, CopyCommit: strings.ToUpper("abcdef" + sha[6:])},
		"footprint without v1":        {Slug: "old", Kind: Deleted, Footprint: strings.Repeat("cd", 32)},
		"short footprint":             {Slug: "old", Kind: Deleted, Footprint: "v1:abcd"},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, r)
		}
	}
	if err := (Record{Slug: "old", Kind: Deleted, Generation: 1, Footprint: fp}).Validate(); err != nil {
		t.Errorf("a well-formed deleted record refused: %v", err)
	}
}
