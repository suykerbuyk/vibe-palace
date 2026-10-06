// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guard tests for the rewrite helpers in project_rename_rewrite.go, relocated
// in U12 from the retired one-shot's guard suite
// (project_slug_migration_guards_test.go). Each is written so that REVERTING
// its guard makes it fail. Only the guards over KEPT helpers are here; the
// one-shot's journal, cache-marker, K0/make-room, held-row, process-scan and
// disk-budget guards went with the machinery they pinned.

const (
	slugFrom = "quantum-ng"
	slugTo   = "qa-metabuild-system"
)

// drawerRow renders one drawer JSONL row with the given id and fields.
func drawerRow(id, hall, content, sourceType, ref string) string {
	return fmt.Sprintf(`{"id":"%s","hall":"%s","content":"%s","source_type":"%s","source_ref":"%s","filed_at":"2026-08-22T00:00:00Z","added_by":"capture"}`,
		id, hall, content, sourceType, ref)
}

// A drawer whose id is not DrawerID(from, content) means the store is not what
// the rename assumes, so the re-hash must refuse rather than invent.
func TestSlugGuardDrawerIDInvariant(t *testing.T) {
	good := drawerRow(DrawerID(slugFrom, "A fact."), "facts", "A fact.", "session", "s1")
	if _, _, _, err := slugRehashRoom([]byte(good+"\n"), slugFrom, slugTo, "r"); err != nil {
		t.Fatalf("a consistent row must rehash: %v", err)
	}
	bad := drawerRow("deadbeef", "facts", "A fact.", "session", "s1")
	_, _, _, err := slugRehashRoom([]byte(bad+"\n"), slugFrom, slugTo, "r")
	if err == nil || !strings.Contains(err.Error(), "deadbeef") {
		t.Fatalf("a row whose id does not match its content must be refused, got %v", err)
	}
}

// The applied rewrite must equal the counted plan, class for class: it is the
// engine's own answer to "did the rewrite do what the report promised".
func TestSlugGuardAppliedClassesMustEqualPlan(t *testing.T) {
	planned := map[string]int{"W1": 3, "W2": 1}
	if err := slugAssertClasses(map[string]int{"W1": 3, "W2": 1}, planned); err != nil {
		t.Fatalf("equal counts must pass: %v", err)
	}
	if err := slugAssertClasses(map[string]int{"W1": 4, "W2": 1}, planned); err == nil || !strings.Contains(err.Error(), "W1") {
		t.Fatalf("a class that rewrote more than planned must be refused, got %v", err)
	}
	if err := slugAssertClasses(map[string]int{"W1": 3, "W2": 1, "W9": 2}, planned); err == nil || !strings.Contains(err.Error(), "W9") {
		t.Fatalf("a class the plan did not count must be refused, got %v", err)
	}
}

// slugRemoveEmptyTree refuses a tree that still holds a file, and removes a
// genuinely empty one.
func TestSlugGuardRemoveEmptyTreeRefusesLeftover(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "tree/sub/leftover.txt", "not empty\n")
	err := slugRemoveEmptyTree(root, "tree")
	if err == nil || !strings.Contains(err.Error(), "leftover.txt") {
		t.Fatalf("a leftover file must be refused, got %v", err)
	}
	if !slugExists(root, "tree/sub/leftover.txt") {
		t.Fatal("the refusal must leave the file in place")
	}
	if err := os.Remove(filepath.Join(root, "tree", "sub", "leftover.txt")); err != nil {
		t.Fatal(err)
	}
	if err := slugRemoveEmptyTree(root, "tree"); err != nil {
		t.Fatalf("an empty tree must be removed: %v", err)
	}
	if slugExists(root, "tree") {
		t.Fatal("the empty tree is still there")
	}
}

// A baseline entry that is not a unique token cannot be rewritten by a textual
// replace without risking the wrong occurrence.
func TestSlugGuardBaselineTokenMustBeUnique(t *testing.T) {
	one := `{"dimensions":{"archive-roundtrip":{"accepted":["Projects/` + slugFrom + `/transcripts/m.json"]}}}`
	if _, n, err := slugRewriteBaseline([]byte(one), slugFrom, slugTo); err != nil || n != 1 {
		t.Fatalf("a unique entry must rewrite: n=%d err=%v", n, err)
	}
	// The same path listed twice: the token is no longer unique, so a textual
	// replace could rewrite the wrong one.
	dup := `{"dimensions":{"archive-roundtrip":{"accepted":["Projects/` + slugFrom + `/transcripts/m.json",` +
		`"Projects/` + slugFrom + `/transcripts/m.json"]}}}`
	_, _, err := slugRewriteBaseline([]byte(dup), slugFrom, slugTo)
	if err == nil || !strings.Contains(err.Error(), "unique token") {
		t.Fatalf("a repeated token must be refused, got %v", err)
	}
}

// A move's source must be a regular file: a symlink would be followed, and the
// thing at the other end is not what the plan counted. (The journal-free
// slugMove of U12 takes no journal argument.)
func TestSlugGuardMoveRefusesANonRegularSource(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "real/file.md", "content\n")
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := slugMove(root, "adir", "moved-dir")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a non-regular source must be refused, got %v", err)
	}
	if slugExists(root, "moved-dir") {
		t.Fatal("the refusal still moved something")
	}
	if err := slugMove(root, "real/file.md", "moved.md"); err != nil {
		t.Fatalf("a regular file must move: %v", err)
	}
	if !slugExists(root, "moved.md") {
		t.Fatal("a regular file move left nothing at the destination")
	}
}

// slugScanClasses (through PlanRename) refuses an `archive:` line whose target
// does not exist: rewriting it would produce a dangling link.
func TestRenameGuard_ArchiveTargetMustExist(t *testing.T) {
	f := newRenFix(t)
	writeFile(t, f.V, "Projects/old/sessions/2026-10-05-gone-02.md",
		"---\nproject: old\nnote_path: Projects/old/sessions/2026-10-05-gone-02.md\n"+
			"archive: Projects/old/transcripts/missing.manifest.json\n---\n\nbody\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "a session naming a missing archive")
	if _, err := PlanRename(f.req()); err == nil || !strings.Contains(err.Error(), "archive target") {
		t.Fatalf("a missing archive target must be refused, got %v", err)
	}
}

// slugScanClasses (through PlanRename) requires a manifest to carry exactly one
// project_slug: zero means the rewrite had nothing to do, two means one of them
// would survive unrewritten.
func TestRenameGuard_ProjectSlugAppearsExactlyOnce(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"twice", "{\n  \"project_slug\": \"old\",\n  \"project_slug\": \"old\",\n  \"session_id\": \"zzzz\"\n}\n"},
		{"never", "{\n  \"session_id\": \"zzzz\"\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRenFix(t)
			writeFile(t, f.V, "Projects/old/transcripts/2026-08-22-zzzz.manifest.json", tc.body)
			writeFile(t, f.V, "Projects/old/transcripts/2026-08-22-zzzz.jsonl.zst", "z")
			gitRun(t, f.V, "add", "-A")
			gitRun(t, f.V, "commit", "-q", "-m", "a manifest with "+tc.name+" project_slug")
			if _, err := PlanRename(f.req()); err == nil || !strings.Contains(err.Error(), "project_slug appears") {
				t.Fatalf("project_slug %s must be refused, got %v", tc.name, err)
			}
		})
	}
}
