// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLearning writes a learning fixture into the vault-wide learnings dir.
func writeLearning(t *testing.T, v *Vault, slug, content string) {
	t.Helper()
	dir := v.LearningsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir learnings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write learning %s: %v", slug, err)
	}
}

// validLearning builds a well-formed learning file body.
func validLearning(name, desc, typ, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\ntype: " + typ + "\n---\n\n" + body
}

func TestLearningsDir(t *testing.T) {
	v := testVault(t)
	got := v.LearningsDir()
	want := filepath.Join(v.Root, "Knowledge", "learnings")
	if got != want {
		t.Errorf("LearningsDir = %q, want %q", got, want)
	}
}

func TestListLearningsMissingDir(t *testing.T) {
	v := testVault(t)
	got, err := v.ListLearnings("")
	if err != nil {
		t.Fatalf("ListLearnings (missing dir): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListLearnings (missing dir) = %d entries, want 0", len(got))
	}
}

func TestListLearningsSortedAndFiltered(t *testing.T) {
	v := testVault(t)
	writeLearning(t, v, "c-third", validLearning("Third", "third desc", "reference", "body c\n"))
	writeLearning(t, v, "a-first", validLearning("First", "first desc", "feedback", "body a\n"))
	writeLearning(t, v, "b-second", validLearning("Second", "second desc", "user", "body b\n"))

	got, err := v.ListLearnings("")
	if err != nil {
		t.Fatalf("ListLearnings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListLearnings = %d entries, want 3 (%+v)", len(got), got)
	}
	wantSlugs := []string{"a-first", "b-second", "c-third"}
	for i, m := range got {
		if m.Slug != wantSlugs[i] {
			t.Errorf("entry %d Slug = %q, want %q", i, m.Slug, wantSlugs[i])
		}
	}
	// Metadata fields populated.
	if got[0].Name != "First" || got[0].Description != "first desc" || got[0].Type != "feedback" {
		t.Errorf("entry 0 metadata = %+v", got[0])
	}

	// Filter by type returns only matching entries.
	filtered, err := v.ListLearnings("user")
	if err != nil {
		t.Fatalf("ListLearnings(user): %v", err)
	}
	if len(filtered) != 1 || filtered[0].Slug != "b-second" {
		t.Errorf("ListLearnings(user) = %+v, want only b-second", filtered)
	}
}

func TestListLearningsSkipsMalformed(t *testing.T) {
	v := testVault(t)
	// Valid entry that must survive.
	writeLearning(t, v, "good", validLearning("Good", "good desc", "reference", "body\n"))
	// Missing opening fence.
	writeLearning(t, v, "no-opener", "name: X\ndescription: d\ntype: user\nbody\n")
	// Unterminated fence.
	writeLearning(t, v, "no-closer", "---\nname: X\ndescription: d\ntype: user\nbody\n")
	// Missing required field (description).
	writeLearning(t, v, "no-desc", "---\nname: X\ntype: user\n---\n\nbody\n")
	// Rejected type: project.
	writeLearning(t, v, "is-project", validLearning("P", "d", "project", "body\n"))
	// Unknown type.
	writeLearning(t, v, "bad-type", validLearning("B", "d", "bogus", "body\n"))

	got, err := v.ListLearnings("")
	if err != nil {
		t.Fatalf("ListLearnings: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "good" {
		t.Errorf("ListLearnings = %+v, want only good", got)
	}
}

func TestListLearningsIgnoresNonMarkdownAndDirs(t *testing.T) {
	v := testVault(t)
	writeLearning(t, v, "real", validLearning("Real", "d", "user", "body\n"))

	dir := v.LearningsDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir.md"), 0o755); err != nil {
		t.Fatalf("mkdir subdir.md: %v", err)
	}

	got, err := v.ListLearnings("")
	if err != nil {
		t.Fatalf("ListLearnings: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "real" {
		t.Errorf("ListLearnings = %+v, want only real", got)
	}
}

func TestGetLearningValid(t *testing.T) {
	v := testVault(t)
	writeLearning(t, v, "alpha", validLearning("Alpha", "alpha desc", "feedback", "line one\nline two\n"))

	got, err := v.GetLearning("alpha")
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if got.Slug != "alpha" {
		t.Errorf("Slug = %q, want alpha", got.Slug)
	}
	if got.Name != "Alpha" || got.Description != "alpha desc" || got.Type != "feedback" {
		t.Errorf("metadata = %+v", got)
	}
	// Leading blank line trimmed.
	if got.Content != "line one\nline two\n" {
		t.Errorf("Content = %q, want trimmed body", got.Content)
	}
}

func TestGetLearningUnknownSlugListsAvailable(t *testing.T) {
	v := testVault(t)
	writeLearning(t, v, "alpha", validLearning("Alpha", "d", "user", "body\n"))
	writeLearning(t, v, "beta", validLearning("Beta", "d", "user", "body\n"))

	_, err := v.GetLearning("missing")
	if err == nil {
		t.Fatalf("GetLearning(missing) succeeded, want error")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("error %q does not list available slugs", err)
	}
}

func TestGetLearningEmptyAndTraversal(t *testing.T) {
	v := testVault(t)
	cases := []string{"", "../escape", "a/b", "..", "sub/../x"}
	for _, slug := range cases {
		t.Run(slug, func(t *testing.T) {
			if _, err := v.GetLearning(slug); err == nil {
				t.Errorf("GetLearning(%q) accepted invalid slug", slug)
			}
		})
	}
}
