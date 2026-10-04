// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyTripleTable(t *testing.T) {
	cases := []struct {
		name string
		t    Triple
		want string
	}{
		{"vp_kg_add shape", Triple{Subject: "a", Predicate: "p", Object: "b", Confidence: 1, ValidFrom: "2026-01-01"}, OriginAuthored},
		{"extractor shape", Triple{Subject: "a", Predicate: "p", Object: "b", Confidence: 0.7, SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"}, OriginExtracted},
		{"mempalace shape, no valid_to", Triple{Subject: "a", Predicate: "p", Object: "b", Confidence: 0.9, ExtractedAt: "2026-01-01T00:00:00Z"}, OriginExtracted},
		{"mempalace shape, valid_to (X5)", Triple{Subject: "a", Predicate: "p", Object: "b", ExtractedAt: "2026-01-01T00:00:00Z", ValidTo: "2026-02-01"}, OriginAuthored},
		{"origin authored wins", Triple{ExtractedAt: "x", Origin: OriginAuthored}, OriginAuthored},
		{"origin extracted wins", Triple{Origin: OriginExtracted}, OriginExtracted},
	}
	for _, c := range cases {
		if got := ClassifyTriple(c.t); got != c.want {
			t.Errorf("%s: ClassifyTriple = %q, want %q", c.name, got, c.want)
		}
	}
}

// X5: an extracted triple with valid_to set is a pre-migration invalidation
// edit and stays authored; an explicit origin still wins over rule 3.
func TestInvalidationEditStaysAuthored(t *testing.T) {
	ext := Triple{Subject: "a", Predicate: "p", Object: "b", SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"}
	if got := ClassifyTriple(ext); got != OriginExtracted {
		t.Fatalf("extractor shape without valid_to = %q, want extracted", got)
	}
	ext.ValidTo = "2026-03-01"
	if got := ClassifyTriple(ext); got != OriginAuthored {
		t.Errorf("extractor shape with valid_to = %q, want authored", got)
	}
	ext.Origin = OriginExtracted
	if got := ClassifyTriple(ext); got != OriginExtracted {
		t.Errorf("origin extracted with valid_to = %q, want extracted (rule 1 before rule 3)", got)
	}
}

// The oracle is the bytes HEAD's InvalidateTriple wrote at 0eff86f, committed
// as testdata before any writer changed, so the test survives the rewrite.
func TestV8InvalidationClassifiesAuthored(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "kg-v8-invalidated", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("testdata missing: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var tr Triple
		if err := json.Unmarshal(b, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.ExtractedAt == "" || tr.ValidTo == "" {
			t.Fatalf("%s: fixture is not a v8 invalidation of an extracted triple", f)
		}
		if got := ClassifyTriple(tr); got != OriginAuthored {
			t.Errorf("%s: ClassifyTriple = %q, want authored", f, got)
		}
	}
}

func TestClassifyEntityLineTable(t *testing.T) {
	cases := []struct {
		name string
		e    Entity
		want string
	}{
		{"vp_kg_add shape", Entity{ID: "alice", Name: "Alice", Type: "unknown"}, OriginAuthored},
		{"extractor line", Entity{ID: "person-alice", Name: "Alice", Type: "person", CreatedAt: "2026-01-01T00:00:00Z"}, OriginExtracted},
		{"legacy mempalace line of the vp_kg_add shape", Entity{ID: "mp-1", Name: "X", Type: "unknown"}, OriginAuthored},
		{"origin wins", Entity{ID: "a", Type: "unknown", Origin: OriginExtracted}, OriginExtracted},
	}
	for _, c := range cases {
		if got := ClassifyEntityLine(c.e); got != c.want {
			t.Errorf("%s: ClassifyEntityLine = %q, want %q", c.name, got, c.want)
		}
	}
}

func readTriple(t *testing.T, v *Vault, project, s, p, o string) Triple {
	t.Helper()
	path, err := v.KGTriplePath(project, s, p, o)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := readTripleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestAddAuthoredTripleOverTrackedExtracted(t *testing.T) {
	v := testVault(t)
	ext := Triple{Subject: "a", Predicate: "uses", Object: "b", Confidence: 0.6, ValidFrom: "2026-01-01",
		SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"}
	if err := v.AddTriple("proj-a", ext); err != nil {
		t.Fatal(err)
	}
	if err := v.AddAuthoredTriple("proj-a", Triple{Subject: "a", Predicate: "uses", Object: "b", Confidence: 0.9, ValidFrom: "2026-02-02"}); err != nil {
		t.Fatalf("AddAuthoredTriple over a tracked extracted triple: %v", err)
	}
	got := readTriple(t, v, "proj-a", "a", "uses", "b")
	if got.Origin != OriginAuthored || got.ExtractedAt != "" || got.SourceSession != "s1" || got.Confidence != 0.9 || got.ValidFrom != "2026-02-02" {
		t.Errorf("rewritten triple = %+v, want authored, no extracted_at, source_session s1, the caller's 0.9 and 2026-02-02", got)
	}
}

func TestAddAuthoredTripleOverTrackedAuthored(t *testing.T) {
	v := testVault(t)
	if err := v.AddAuthoredTriple("proj-a", Triple{Subject: "a", Predicate: "uses", Object: "b", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	path, _ := v.KGTriplePath("proj-a", "a", "uses", "b")
	before, _ := os.ReadFile(path)
	err := v.AddAuthoredTriple("proj-a", Triple{Subject: "a", Predicate: "uses", Object: "b", Confidence: 0.5})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second AddAuthoredTriple = %v, want already exists", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("the authored file changed")
	}
}

func TestExtractorAddTripleDedupUnchanged(t *testing.T) {
	v := testVault(t)
	if err := v.AddAuthoredTriple("proj-a", Triple{Subject: "a", Predicate: "uses", Object: "b"}); err != nil {
		t.Fatal(err)
	}
	err := v.AddTriple("proj-a", Triple{Subject: "a", Predicate: "uses", Object: "b", ExtractedAt: "x"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("AddTriple over a tracked record = %v, want already exists", err)
	}
}

func TestInvalidateAuthoredTriple(t *testing.T) {
	v := testVault(t)
	if err := v.AddTriple("proj-a", Triple{Subject: "a", Predicate: "uses", Object: "b", SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := v.InvalidateAuthoredTriple("proj-a", "a", "uses", "b", "2026-03-01", nil); err != nil {
		t.Fatal(err)
	}
	got := readTriple(t, v, "proj-a", "a", "uses", "b")
	if got.Origin != OriginAuthored || got.ValidTo != "2026-03-01" || got.SourceSession != "s1" || got.ExtractedAt != "" {
		t.Errorf("tracked invalidation = %+v", got)
	}

	// Local-only: the caller's local record becomes a tracked authored file.
	local := &Triple{Subject: "c", Predicate: "uses", Object: "d", SourceSession: "s9", ExtractedAt: "2026-01-01T00:00:00Z"}
	if err := v.InvalidateAuthoredTriple("proj-a", "c", "uses", "d", "2026-04-01", local); err != nil {
		t.Fatal(err)
	}
	got = readTriple(t, v, "proj-a", "c", "uses", "d")
	if got.Origin != OriginAuthored || got.ValidTo != "2026-04-01" || got.SourceSession != "s9" || got.ExtractedAt != "" {
		t.Errorf("local-only invalidation = %+v", got)
	}
	stripped := got
	stripped.Origin = ""
	if ClassifyTriple(stripped) != OriginAuthored {
		t.Error("an overlay with its origin stripped no longer classifies as authored")
	}

	// Neither: not found.
	err := v.InvalidateAuthoredTriple("proj-a", "x", "uses", "y", "2026-04-01", nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("invalidating a missing triple = %v, want fs.ErrNotExist", err)
	}
}
