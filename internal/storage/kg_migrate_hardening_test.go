// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// writeRawTriple writes a triple JSON file at an arbitrary (possibly nested,
// old-encoding) relative path under a project's triples dir, with the RAW
// subject/predicate/object in the body.
func writeRawTriple(t *testing.T, root, project, relName, subj, pred, obj string) string {
	t.Helper()
	path := filepath.Join(root, "palace", project, "kg", "triples", relName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(Triple{Subject: subj, Predicate: pred, Object: obj}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRawBytes(t *testing.T, root, project, relName string, body []byte) string {
	t.Helper()
	path := filepath.Join(root, "palace", project, "kg", "triples", relName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// (a) A pre-existing flat target holding a DIFFERENT triple must abort the
// migration and leave the source in place — the collision fix that stops silent
// data loss.
func TestApplyMigration_CollisionDifferingBodiesErrors(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	// Nested source triple.
	src := writeRawTriple(t, root, "proj", filepath.Join("a", "b--m--o.json"), "a/b", "m", "o")
	// A FOREIGN file already sitting at the source's flat target, with a wholly
	// different (subject,predicate,object).
	target, err := v.KGTriplePath("proj", "a/b", "m", "o")
	if err != nil {
		t.Fatal(err)
	}
	writeRawBytes(t, root, "proj", filepath.Base(target),
		[]byte(`{"subject":"DIFFERENT","predicate":"p2","object":"o2"}`))

	m, err := v.ApplyTripleFilenameMigration()
	if err == nil {
		t.Fatal("expected a collision error, got nil")
	}
	if len(m.Collisions) != 1 {
		t.Fatalf("Collisions = %d, want 1", len(m.Collisions))
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("error %q should name the collision", err)
	}
	// The source was NOT deleted.
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("source triple was deleted on a collision (must never happen): %v", statErr)
	}
	// The foreign target was NOT deleted either.
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("foreign target was deleted on a collision: %v", statErr)
	}
}

// (b) Resume where the flat target exists but its body is CORRUPT (unparseable):
// still a collision — refuse, never drop the source.
func TestApplyMigration_ResumeCorruptTargetErrors(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	src := writeRawTriple(t, root, "proj", filepath.Join("x", "y--m--o.json"), "x/y", "m", "o")
	target, err := v.KGTriplePath("proj", "x/y", "m", "o")
	if err != nil {
		t.Fatal(err)
	}
	writeRawBytes(t, root, "proj", filepath.Base(target), []byte(`{ this is not json`))

	m, err := v.ApplyTripleFilenameMigration()
	if err == nil {
		t.Fatal("expected a collision error on a corrupt target, got nil")
	}
	if len(m.Collisions) != 1 {
		t.Fatalf("Collisions = %d, want 1", len(m.Collisions))
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("source was dropped despite a corrupt target: %v", statErr)
	}
}

// (c) A non-Triple json and an unparseable json in a triples dir → the pre-scan
// refuses and mutates NOTHING (the good nested triple is not moved).
func TestApplyMigration_PreScanRefusesBadFiles(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	good := writeRawTriple(t, root, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")
	writeRawBytes(t, root, "proj", "notriple--m--o.json", []byte(`{"foo":"bar"}`)) // parses, not a Triple
	writeRawBytes(t, root, "proj", "broken--m--o.json", []byte(`{ nope`))          // unparseable

	m, err := v.ApplyTripleFilenameMigration()
	if err == nil {
		t.Fatal("expected a pre-scan error, got nil")
	}
	if len(m.BadFiles) != 2 {
		t.Fatalf("BadFiles = %d, want 2: %+v", len(m.BadFiles), m.BadFiles)
	}
	// Nothing was mutated: the good triple is still at its ORIGINAL nested path.
	if _, statErr := os.Stat(good); statErr != nil {
		t.Errorf("pre-scan abort still moved the good triple: %v", statErr)
	}
	// And no flat target for it was created.
	flat, _ := v.KGTriplePath("proj", "src/main.rs", "m", "s1")
	if _, statErr := os.Stat(flat); statErr == nil {
		t.Errorf("pre-scan abort created a flat target %s", flat)
	}
}

// (d) A true partial state (some flat, some nested) re-runs to completion,
// preserves the count, and a further run is idempotent.
func TestApplyMigration_ResumePartialStateIdempotent(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	// One already-flat (correctly-encoded) triple + two still-nested distinct ones.
	flat1, _ := v.KGTriplePath("proj", "already/flat", "m", "o1")
	writeRawBytes(t, root, "proj", filepath.Base(flat1), []byte(`{"subject":"already/flat","predicate":"m","object":"o1"}`))
	writeRawTriple(t, root, "proj", filepath.Join("deep", "a", "b--m--o2.json"), "deep/a/b", "m", "o2")
	writeRawTriple(t, root, "proj", filepath.Join("http:", "x--m--o3.json"), "http://x", "m", "o3")

	m, err := v.ApplyTripleFilenameMigration()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.Renamed != 2 || m.AlreadyOK != 1 {
		t.Fatalf("first run: Renamed=%d AlreadyOK=%d, want 2/1", m.Renamed, m.AlreadyOK)
	}
	stats, err := v.KGStats("proj")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TripleCount != 3 {
		t.Fatalf("post count = %d, want 3 (none lost)", stats.TripleCount)
	}

	m2, err := v.ApplyTripleFilenameMigration()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if m2.Renamed != 0 || m2.Collapsed != 0 {
		t.Errorf("second run mutated: Renamed=%d Collapsed=%d, want 0/0", m2.Renamed, m2.Collapsed)
	}
	if m2.AlreadyOK != 3 {
		t.Errorf("second run AlreadyOK = %d, want 3 (idempotent)", m2.AlreadyOK)
	}
}

// (e) Concurrent access: while another holder owns the migration lock, the
// migration REFUSES rather than racing.
func TestApplyMigration_RefusesWhenLockHeld(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)
	writeRawTriple(t, root, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")

	release, ok, err := vaultlock.TryAcquire(root, v.kgMigrationLockPath())
	if err != nil || !ok {
		t.Fatalf("test could not pre-acquire the migration lock: ok=%v err=%v", ok, err)
	}
	defer release()

	if _, err := v.ApplyTripleFilenameMigration(); err == nil {
		t.Fatal("expected the migration to REFUSE while the lock is held, got nil")
	} else if !strings.Contains(err.Error(), "exclusive") && !strings.Contains(err.Error(), "lock") {
		t.Errorf("refusal error %q should mention the lock/exclusive access", err)
	}

	// The nested source is untouched — the migration never started mutating.
	nested := filepath.Join(root, "palace", "proj", "kg", "triples", "src", "main.rs--m--s1.json")
	if _, statErr := os.Stat(nested); statErr != nil {
		t.Errorf("migration mutated despite the held lock: %v", statErr)
	}
}

// (f) Post-migration invariant: KGStats.TripleCount preserved and every triple
// queryable BOTH subject- and object-side, across the hostile input classes
// (URL, file path, colon, multi-line).
func TestApplyMigration_BothSideQueryableAndCountPreserved(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	type tr struct{ rel, s, p, o string }
	cases := []tr{
		{filepath.Join("https:", "api.github.com", "repos--m--sess1.json"), "https://api.github.com/repos", "mentioned_in", "sess1"},
		{filepath.Join("cando-messages", "src", "j1939", "mod.rs--m--sess2.json"), "cando-messages/src/j1939/mod.rs", "mentioned_in", "sess2"},
		{"foo--m--zed:abc.json", "foo", "mentioned_in", "zed:abc"},
		{"multi--m--o.json", "line one\nline two", "mentioned_in", "o4"},
	}
	for _, c := range cases {
		writeRawTriple(t, root, "proj", c.rel, c.s, c.p, c.o)
	}

	if _, err := v.ApplyTripleFilenameMigration(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	stats, err := v.KGStats("proj")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TripleCount != len(cases) {
		t.Fatalf("TripleCount = %d, want %d", stats.TripleCount, len(cases))
	}
	for _, c := range cases {
		out, err := v.QueryEntity("proj", c.s, "", "out")
		if err != nil {
			t.Fatalf("subject-side query %q: %v", c.s, err)
		}
		if !containsSPO(out, c.s, c.p, c.o) {
			t.Errorf("subject-side lookup missed (%q,%q,%q)", c.s, c.p, c.o)
		}
		in, err := v.QueryEntity("proj", c.o, "", "in")
		if err != nil {
			t.Fatalf("object-side query %q: %v", c.o, err)
		}
		if !containsSPO(in, c.s, c.p, c.o) {
			t.Errorf("object-side lookup missed (%q,%q,%q)", c.s, c.p, c.o)
		}
	}
}

// (g) An already-flat vault (the other-host case): the migration renames 0,
// collapses 0, verifies clean.
func TestApplyMigration_AlreadyFlatRenamesZero(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	// Seed already-current flat triples by using the real writer (AddTriple does
	// not gate on the data format, so it works on a format-0 vault).
	for _, o := range []string{"one", "two", "three"} {
		if err := v.AddTriple("proj", Triple{Subject: "kai", Predicate: "knows", Object: o}); err != nil {
			t.Fatalf("seed AddTriple: %v", err)
		}
	}

	m, err := v.ApplyTripleFilenameMigration()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.Renamed != 0 || m.Collapsed != 0 {
		t.Errorf("already-flat vault: Renamed=%d Collapsed=%d, want 0/0", m.Renamed, m.Collapsed)
	}
	if m.AlreadyOK != 3 {
		t.Errorf("AlreadyOK = %d, want 3", m.AlreadyOK)
	}
}

// containsSPO reports whether ts holds a triple with the given s/p/o.
func containsSPO(ts []Triple, s, p, o string) bool {
	for _, t := range ts {
		if t.Subject == s && t.Predicate == p && t.Object == o {
			return true
		}
	}
	return false
}

// countTripleFilesRecursive counts every *.json file under dir, recursively.
func countTripleFilesRecursive(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && filepath.Ext(path) == ".json" {
			n++
		}
		return nil
	})
	return n
}

func hasNestedDir(dir string) bool {
	nested := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != dir {
			nested = true
		}
		return nil
	})
	return nested
}
