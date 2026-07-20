// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEncodeTripleComponentIsFlatAndPortable pins the encoder's invariants: the
// output is single-level (no separators), carries no ":" / control chars / "..",
// is never a bare "--"-bearing string, and is injective (a slug collision is
// still disambiguated by the content hash).
func TestEncodeTripleComponentIsFlatAndPortable(t *testing.T) {
	raws := []string{
		"https://api.github.com/repos/foo/bar",
		"cando-messages/src/j1939/mod.rs",
		"amend\n\nthe",
		"../../../../etc/passwd",
		"zed-mcp:cdf4dc0c-1234",
		"CON", "NUL", "..", "...",
		"Weird  Mixed__Case--Name",
		"", "   ",
	}
	for _, raw := range raws {
		enc := encodeTripleComponent(raw)
		if enc == "" {
			t.Errorf("encode(%q) is empty", raw)
		}
		for _, bad := range []string{"/", "\\", ":", "\n", "\r", "--"} {
			if strings.Contains(enc, bad) {
				t.Errorf("encode(%q) = %q contains forbidden %q", raw, enc, bad)
			}
		}
		if enc == ".." || strings.HasPrefix(enc, "../") {
			t.Errorf("encode(%q) = %q is a traversal token", raw, enc)
		}
		if filepath.Clean(enc) != enc {
			t.Errorf("encode(%q) = %q is not filepath.Clean-stable", raw, enc)
		}
	}

	// Injective across slug collisions: two raws that slug identically differ in
	// the content-hash suffix.
	a := encodeTripleComponent("Foo Bar")
	b := encodeTripleComponent("foo/bar")
	if a == b {
		t.Errorf("distinct raws collided: %q == %q", a, b)
	}
	// Deterministic: same input, same output.
	if encodeTripleComponent("Foo Bar") != a {
		t.Error("encoder is not deterministic")
	}
}

// TestKGTriplePathContainsTraversal proves the containment assertion: a subject
// carrying a path-traversal payload cannot escape the triples dir. Combined with
// the flat encoder, the written file stays directly under triplesDir.
func TestKGTriplePathStaysContained(t *testing.T) {
	v := testVault(t)
	triplesDir, err := v.KGTriplesDir("proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, subj := range []string{
		"../../../../etc/passwd",
		"https://api.github.com/repos/foo/bar",
		"cando-messages/src/j1939/mod.rs",
		"a\n\nb",
		"C:\\Windows\\System32",
	} {
		path, err := v.KGTriplePath("proj", subj, "mentioned_in", "sess-1")
		if err != nil {
			t.Fatalf("KGTriplePath(%q): unexpected error %v", subj, err)
		}
		if filepath.Dir(path) != triplesDir {
			t.Errorf("subject %q produced %q, not directly under %q", subj, path, triplesDir)
		}
		if !strings.HasPrefix(path, triplesDir+string(filepath.Separator)) {
			t.Errorf("subject %q escaped triples dir: %q", subj, path)
		}
	}
}

// TestKGSanitizedTriplesAreFullyQueryable is the headline regression: for a
// triple whose SUBJECT is hostile (URL / file path / multi-line / traversal),
// after AddTriple the file stays under triplesDir AND is found by BOTH the
// subject-side (out) and object-side (in) queries — the object-side path is the
// one that historically dropped every slash-bearing triple — AND is counted by
// KGStats.TripleCount and returned by ListTriples.
func TestKGSanitizedTriplesAreFullyQueryable(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	triplesDir, err := v.KGTriplesDir(project)
	if err != nil {
		t.Fatal(err)
	}

	subjects := []string{
		"https://api.github.com/repos/foo/bar",
		"cando-messages/src/j1939/mod.rs",
		"line one\nline two\nline three",
		"../../../../etc/passwd",
	}
	// Each triple gets a distinct object so the in-direction query isolates it.
	for i, subj := range subjects {
		obj := "session-" + string(rune('a'+i))
		if err := v.AddTriple(project, Triple{Subject: subj, Predicate: "mentioned_in", Object: obj}); err != nil {
			t.Fatalf("AddTriple(subj=%q): %v", subj, err)
		}

		// Written path stays directly under triplesDir.
		path, err := v.KGTriplePath(project, subj, "mentioned_in", obj)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != triplesDir {
			t.Errorf("subj %q wrote outside triples dir: %q", subj, path)
		}

		// Subject-side lookup finds it.
		out, err := v.QueryEntity(project, subj, "", "out")
		if err != nil {
			t.Fatalf("QueryEntity out (subj=%q): %v", subj, err)
		}
		if len(out) != 1 {
			t.Errorf("subject-side lookup for %q returned %d, want 1", subj, len(out))
		}

		// Object-side lookup finds it — the regression that used to silently
		// drop every slash-bearing subject.
		in, err := v.QueryEntity(project, obj, "", "in")
		if err != nil {
			t.Fatalf("QueryEntity in (obj=%q): %v", obj, err)
		}
		if len(in) != 1 {
			t.Errorf("object-side lookup for object %q (subj %q) returned %d, want 1", obj, subj, len(in))
		}
	}

	// KGStats counts every triple, and ListTriples returns them all — the two
	// single-level-glob sites that also historically undercounted.
	stats, err := v.KGStats(project)
	if err != nil {
		t.Fatalf("KGStats: %v", err)
	}
	if stats.TripleCount != len(subjects) {
		t.Errorf("KGStats.TripleCount = %d, want %d", stats.TripleCount, len(subjects))
	}
	list, err := v.ListTriples(project)
	if err != nil {
		t.Fatalf("ListTriples: %v", err)
	}
	if len(list) != len(subjects) {
		t.Errorf("ListTriples returned %d, want %d", len(list), len(subjects))
	}
}
