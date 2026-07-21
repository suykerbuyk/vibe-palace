// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
)

func TestMemoryDirPath(t *testing.T) {
	v := testVault(t)
	got, err := v.MemoryDir("proj")
	if err != nil {
		t.Fatalf("MemoryDir: %v", err)
	}
	want := filepath.Join(v.Root, "Projects", "proj", "memory")
	if got != want {
		t.Errorf("MemoryDir = %q, want %q", got, want)
	}

	if _, err := v.MemoryDir("Bad Slug"); err == nil {
		t.Errorf("MemoryDir accepted invalid slug")
	}
}

func TestMemoryFilePath(t *testing.T) {
	v := testVault(t)
	got, err := v.MemoryFile("proj", "pref-foo.md")
	if err != nil {
		t.Fatalf("MemoryFile: %v", err)
	}
	want := filepath.Join(v.Root, "Projects", "proj", "memory", "pref-foo.md")
	if got != want {
		t.Errorf("MemoryFile = %q, want %q", got, want)
	}

	cases := []struct {
		name string
		rel  string
	}{
		{"empty", ""},
		{"traversal", "../escape.md"},
		{"nested traversal", "sub/../../escape.md"},
		{"absolute", "/etc/passwd"},
		{"git segment", ".git/config"},
		{"git segment nested", "sub/.git/hooks"},
		{"git segment uppercase", ".GIT/config"},
		{"reserved colon", "team:notes.md"},
		{"reserved star", "a*b.md"},
		{"reserved backslash", `a\b.md`},
		{"device name", "aux.md"},
		{"device name bare", "NUL"},
		{"trailing space", "foo "},
		{"trailing dot", "foo.md."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := v.MemoryFile("proj", c.rel); err == nil {
				t.Errorf("MemoryFile(%q) accepted invalid rel", c.rel)
			}
		})
	}

	if _, err := v.MemoryFile("Bad Slug", "ok.md"); err == nil {
		t.Errorf("MemoryFile accepted invalid slug")
	}
}

func TestWriteReadMemoryRoundTrip(t *testing.T) {
	v := testVault(t)
	meta := MemoryMeta{
		Name:        "Prefers tabs",
		Description: "User prefers tabs over spaces.",
		Type:        "feedback",
	}
	body := "The user prefers tabs over spaces for indentation.\n"

	if err := v.WriteMemory("proj", "pref-foo.md", meta, body); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}

	gotMeta, gotBody, err := v.ReadMemory("proj", "pref-foo.md")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if gotMeta.Name != meta.Name {
		t.Errorf("Name = %q, want %q", gotMeta.Name, meta.Name)
	}
	if gotMeta.Description != meta.Description {
		t.Errorf("Description = %q, want %q", gotMeta.Description, meta.Description)
	}
	if gotMeta.Type != meta.Type {
		t.Errorf("Type = %q, want %q", gotMeta.Type, meta.Type)
	}
	if gotMeta.Rel != "pref-foo.md" {
		t.Errorf("Rel = %q, want %q", gotMeta.Rel, "pref-foo.md")
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
}

func TestWriteMemoryValidation(t *testing.T) {
	v := testVault(t)

	if err := v.WriteMemory("proj", "x.md", MemoryMeta{Name: "n", Type: "bogus"}, "b"); err == nil {
		t.Errorf("WriteMemory accepted bad type")
	}
	if err := v.WriteMemory("proj", "x.md", MemoryMeta{Name: "", Type: "user"}, "b"); err == nil {
		t.Errorf("WriteMemory accepted empty name")
	}
	if err := v.WriteMemory("proj", "x.md", MemoryMeta{Name: "  ", Type: "user"}, "b"); err == nil {
		t.Errorf("WriteMemory accepted whitespace-only name")
	}
	for typ := range memoryTypes {
		if err := v.WriteMemory("proj", typ+".md", MemoryMeta{Name: "n", Type: typ}, "b"); err != nil {
			t.Errorf("WriteMemory rejected valid type %q: %v", typ, err)
		}
	}
}

func TestListMemories(t *testing.T) {
	v := testVault(t)

	// Missing dir → empty, nil error.
	got, err := v.ListMemories("proj", 0)
	if err != nil {
		t.Fatalf("ListMemories (missing dir): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMemories (missing dir) = %d entries, want 0", len(got))
	}

	mustWrite := func(rel, name, typ string) {
		t.Helper()
		if err := v.WriteMemory("proj", rel, MemoryMeta{Name: name, Description: "d", Type: typ}, "body of "+rel); err != nil {
			t.Fatalf("WriteMemory %s: %v", rel, err)
		}
	}
	mustWrite("c-third.md", "Third", "user")
	mustWrite("a-first.md", "First", "feedback")
	mustWrite("b-second.md", "Second", "project")

	// Add a MEMORY.md index that should be skipped.
	dir, _ := v.MemoryDir("proj")
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# index\n"), 0o644); err != nil {
		t.Fatalf("write MEMORY.md: %v", err)
	}
	// Add an unparseable file that should be skipped, not error.
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatalf("write broken.md: %v", err)
	}

	got, err = v.ListMemories("proj", 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListMemories = %d entries, want 3 (got %+v)", len(got), got)
	}
	// Sorted by Rel.
	wantRels := []string{"a-first.md", "b-second.md", "c-third.md"}
	for i, m := range got {
		if m.Rel != wantRels[i] {
			t.Errorf("entry %d Rel = %q, want %q", i, m.Rel, wantRels[i])
		}
	}

	// Limit caps the result.
	got, err = v.ListMemories("proj", 2)
	if err != nil {
		t.Fatalf("ListMemories(limit=2): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListMemories(limit=2) = %d entries, want 2", len(got))
	}
}

// TestListMemoriesNestedRel guards the regression where ListMemories globbed
// only the top level and set Rel to the basename, so a memory written with a
// nested rel was invisible to recall and could not be read back.
func TestListMemoriesNestedRel(t *testing.T) {
	v := testVault(t)
	if err := v.WriteMemory("proj", "prefs/style.md",
		MemoryMeta{Name: "Style", Description: "d", Type: "feedback"}, "tabs not spaces\n"); err != nil {
		t.Fatalf("WriteMemory nested: %v", err)
	}

	got, err := v.ListMemories("proj", 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 nested memory, got %+v", got)
	}
	// Rel must be the slash path relative to the memory dir, so it round-trips.
	if got[0].Rel != "prefs/style.md" {
		t.Fatalf("Rel = %q, want prefs/style.md", got[0].Rel)
	}
	meta, body, err := v.ReadMemory("proj", got[0].Rel)
	if err != nil {
		t.Fatalf("ReadMemory(%q): %v", got[0].Rel, err)
	}
	if meta.Name != "Style" || body != "tabs not spaces\n" {
		t.Errorf("round-trip mismatch: meta=%+v body=%q", meta, body)
	}
}

func TestListMemoriesMetaOnlyFromFixture(t *testing.T) {
	v := testVault(t)
	dir, err := v.MemoryDir("proj")
	if err != nil {
		t.Fatalf("MemoryDir: %v", err)
	}
	if err := memorytestutil.WriteNativeMemoryFixture(dir); err != nil {
		t.Fatalf("WriteNativeMemoryFixture: %v", err)
	}

	got, err := v.ListMemories("proj", 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	// 3 memory files; MEMORY.md skipped.
	if len(got) != 3 {
		t.Fatalf("ListMemories = %d entries, want 3", len(got))
	}
	// Native metadata.type is resolved.
	byRel := map[string]MemoryMeta{}
	for _, m := range got {
		byRel[m.Rel] = m
		if m.Name == "" {
			t.Errorf("entry %q has empty Name", m.Rel)
		}
	}
	if byRel["pref-foo.md"].Type != "feedback" {
		t.Errorf("pref-foo type = %q, want feedback", byRel["pref-foo.md"].Type)
	}
	if byRel["proj-bar.md"].Type != "project" {
		t.Errorf("proj-bar type = %q, want project", byRel["proj-bar.md"].Type)
	}
}

func TestDeleteMemory(t *testing.T) {
	v := testVault(t)
	if err := v.WriteMemory("proj", "gone.md", MemoryMeta{Name: "n", Type: "user"}, "b"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if err := v.DeleteMemory("proj", "gone.md"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if _, _, err := v.ReadMemory("proj", "gone.md"); err == nil {
		t.Errorf("ReadMemory succeeded after delete")
	}
	// Second delete is a no-op.
	if err := v.DeleteMemory("proj", "gone.md"); err != nil {
		t.Errorf("second DeleteMemory returned error: %v", err)
	}
}

func TestParseMemoryFrontmatterLenient(t *testing.T) {
	// Nested metadata.type.
	nested := []byte("---\nname: A\ndescription: d\nmetadata:\n  type: project\noriginSessionId: abc-123\n---\n\nbody\n")
	meta, body, err := parseMemoryFrontmatter(nested)
	if err != nil {
		t.Fatalf("parse nested: %v", err)
	}
	if meta.Type != "project" {
		t.Errorf("nested type = %q, want project", meta.Type)
	}
	if body != "body\n" {
		t.Errorf("nested body = %q", body)
	}

	// Top-level type takes precedence over metadata.type.
	top := []byte("---\nname: B\ndescription: d\ntype: feedback\nmetadata:\n  type: project\n---\nbody\n")
	meta, _, err = parseMemoryFrontmatter(top)
	if err != nil {
		t.Fatalf("parse top: %v", err)
	}
	if meta.Type != "feedback" {
		t.Errorf("top-level type = %q, want feedback", meta.Type)
	}

	// Missing opening fence is an error.
	if _, _, err := parseMemoryFrontmatter([]byte("no fence")); err == nil {
		t.Errorf("parse accepted missing fence")
	}
}

func TestWriteMemoryConcurrent(t *testing.T) {
	v := testVault(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel := fmt.Sprintf("mem-%02d.md", i)
			errs[i] = v.WriteMemory("proj", rel, MemoryMeta{
				Name: fmt.Sprintf("Mem %d", i),
				Type: "user",
			}, fmt.Sprintf("body %d\n", i))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("WriteMemory %d: %v", i, err)
		}
	}
	got, err := v.ListMemories("proj", 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(got) != n {
		t.Errorf("ListMemories = %d entries, want %d", len(got), n)
	}
}

func TestWriteMemoryRejectsUnportableName(t *testing.T) {
	v := testVault(t)
	meta := MemoryMeta{Name: "n", Type: "user"}
	for _, rel := range []string{"team:notes.md", "aux.md", "foo ", `a\b.md`} {
		err := v.WriteMemory("proj", rel, meta, "body")
		if err == nil {
			t.Errorf("WriteMemory(%q): want error, got nil", rel)
			continue
		}
		// The harvest keys on ErrMemoryNameRejected to skip+log rather than abort.
		if !errors.Is(err, ErrMemoryNameRejected) {
			t.Errorf("WriteMemory(%q): want ErrMemoryNameRejected, got %v", rel, err)
		}
	}
}

func TestWriteMemoryCaseCollision(t *testing.T) {
	v := testVault(t)
	meta := MemoryMeta{Name: "n", Type: "user"}

	if err := v.WriteMemory("proj", "Foo.md", meta, "first"); err != nil {
		t.Fatalf("WriteMemory(Foo.md): %v", err)
	}

	// A differently-cased sibling collides — one file on macOS/Windows.
	err := v.WriteMemory("proj", "foo.md", meta, "second")
	if err == nil {
		t.Fatalf("WriteMemory(foo.md): want collision error, got nil")
	}
	if !errors.Is(err, ErrMemoryNameRejected) {
		t.Errorf("collision: want ErrMemoryNameRejected, got %v", err)
	}

	// Rewriting the SAME exact name is an update, not a collision.
	if err := v.WriteMemory("proj", "Foo.md", meta, "updated"); err != nil {
		t.Errorf("WriteMemory(Foo.md) rewrite: want nil, got %v", err)
	}
}
