// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// portabilityViolations reports, for an already-encoded component, one message
// per forbidden token it carries. Each message must name the CONSEQUENCE of
// admitting that token, not just the token: a failure reading "contains
// forbidden \":\"" is true and explains nothing.
//
// It returns messages rather than calling t.Errorf so the table can be exercised
// on the green path — see the pin at the end of
// TestEncodeTripleComponentIsFlatAndPortable.
func portabilityViolations(enc string) []string {
	forbidden := []struct{ tok, why string }{
		{"/", "breaks FLATNESS — triple files must sit DIRECTLY under the triples dir, or the single-level globs in QueryEntity/KGStats/ListTriples stop matching and silently UNDERCOUNT"},
		{"\\", "breaks flatness the same way the moment the vault is read on Windows, where this is the separator"},
		{":", "NTFS and exFAT REJECT ':' in a filename outright — one colon here makes the entire vault un-clonable on those filesystems, which is the portability the flat encoding exists to buy"},
		{"\n", "a control character corrupts every line-oriented tool pointed at the triples dir, and is not portable"},
		{"\r", "a control character corrupts every line-oriented tool pointed at the triples dir, and is not portable"},
		{"--", "'--' is the triple-component DELIMITER in the filename — a component containing it makes subject/predicate/object ambiguous to the globs that split on it"},
	}
	var msgs []string
	for _, f := range forbidden {
		if strings.Contains(enc, f.tok) {
			msgs = append(msgs, fmt.Sprintf("contains forbidden %q — %s", f.tok, f.why))
		}
	}
	return msgs
}

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
		for _, msg := range portabilityViolations(enc) {
			t.Errorf("encode(%q) = %q %s", raw, enc, msg)
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

	// PHASE 1 CLAUSE 3, pinned on the GREEN path. The loop above only prints a
	// why when the encoder is already broken, so on a healthy tree every reason
	// string is dead text: reverting the table to a bare token list would leave
	// the whole suite green and quietly return the deleted Behavioral Note to
	// prose nobody enforces. These assertions drive the message table DIRECTLY
	// with hostile input — no encoder mutation — so stripping a why goes red here.
	//
	// The two consequences are distinct and both have to survive: ':' costs
	// PORTABILITY only, while '/' additionally breaks flatness and makes the
	// single-level globs undercount.
	for _, tc := range []struct{ hostile, want string }{
		{"foo:bar", "NTFS"},
		{"foo:bar", "exFAT"},
		{"a/b", "FLATNESS"},
		{"a/b", "UNDERCOUNT"},
		{"a--b", "DELIMITER"},
	} {
		msgs := portabilityViolations(tc.hostile)
		if len(msgs) == 0 {
			t.Errorf("portabilityViolations(%q) reported nothing — the token table no longer detects it", tc.hostile)
			continue
		}
		if !strings.Contains(strings.Join(msgs, " "), tc.want) {
			t.Errorf("violation for %q does not explain %q — the deleted Behavioral Note has no need-to-know path left: %s",
				tc.hostile, tc.want, strings.Join(msgs, " "))
		}
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
