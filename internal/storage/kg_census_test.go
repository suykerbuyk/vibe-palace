// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The census classifies every tracked triple and entity line of a copy, keeps
// rule 3's hits in their own column, and matches an independent count of HEAD
// exactly; a working tree that differs from HEAD is reported, and a file it
// cannot classify fails the census rather than being skipped.
func TestKGCensus(t *testing.T) {
	dir := initTestRepo(t)
	v := NewVault(dir)
	for _, tr := range []Triple{
		{Subject: "a", Predicate: "likes", Object: "b", Confidence: 1},                                               // authored (rule 2)
		{Subject: "a", Predicate: "uses", Object: "c", SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"},     // extracted
		{Subject: "a", Predicate: "ended", Object: "d", ExtractedAt: "2026-01-01T00:00:00Z", ValidTo: "2026-02-01"},  // authored (rule 3)
		{Subject: "a", Predicate: "made", Object: "e", ExtractedAt: "2026-01-01T00:00:00Z", Origin: OriginExtracted}, // extracted (rule 1)
	} {
		path, err := v.KGTriplePath("proj-a", tr.Subject, tr.Predicate, tr.Object)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeTripleFile(dir, path, tr); err != nil {
			t.Fatal(err)
		}
	}
	ents := `{"id":"a","name":"a","type":"unknown","created_at":""}
{"id":"person-x","name":"X","type":"person","created_at":"2026-01-01T00:00:00Z"}
{"id":"person-y","name":"Y","type":"person","created_at":"2026-01-01T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "palace", "proj-a", "kg", "entities.jsonl"), []byte(ents), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "kg")

	rows, err := KGCensus(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := KGCensusRow{Project: "proj-a", TriplesAuthored: 2, TriplesExtracted: 2, TriplesRule3: 1, TriplesInGit: 4,
		EntitiesAuthored: 1, EntitiesExtracted: 2, EntitiesInGit: 3}
	if len(rows) != 1 || rows[0] != want || !rows[0].Matches() {
		t.Fatalf("census = %+v, want %+v matching HEAD", rows, want)
	}

	// A working tree that has drifted from HEAD no longer matches.
	extra, _ := v.KGTriplePath("proj-a", "z", "p", "q")
	if err := writeTripleFile(dir, extra, Triple{Subject: "z", Predicate: "p", Object: "q"}); err != nil {
		t.Fatal(err)
	}
	rows, err = KGCensus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Matches() {
		t.Errorf("a working tree with an uncommitted triple still matches HEAD: %+v", rows[0])
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}

	// An unparseable file fails the census: there is no unclassified bucket.
	bad := filepath.Join(dir, "palace", "proj-a", "kg", "triples", "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := KGCensus(dir); err == nil || !strings.Contains(err.Error(), "proj-a") {
		t.Errorf("an unparseable triple file: err = %v, want a census error naming the project", err)
	}
}
