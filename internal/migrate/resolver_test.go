// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposeDefault_FirstTry(t *testing.T) {
	got := proposeDefault("resumectl", map[string]bool{}, map[string]bool{})
	if got != "resumectl-vp" {
		t.Errorf("got %q, want resumectl-vp", got)
	}
}

func TestProposeDefault_EscalatesWhenTaken(t *testing.T) {
	taken := map[string]bool{"resumectl-vp": true}
	got := proposeDefault("resumectl", taken, map[string]bool{})
	if got != "resumectl-vp2" {
		t.Errorf("got %q, want resumectl-vp2", got)
	}
}

func TestProposeDefault_EscalatesPastOnDisk(t *testing.T) {
	onDisk := map[string]bool{"resumectl-vp": true, "resumectl-vp2": true}
	got := proposeDefault("resumectl", map[string]bool{}, onDisk)
	if got != "resumectl-vp3" {
		t.Errorf("got %q, want resumectl-vp3", got)
	}
}

func TestAutoResolver_ProducesDefault(t *testing.T) {
	r := &AutoResolver{OnDisk: map[string]bool{}}
	got, err := r.Resolve("/a", "/b", "resumectl", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "resumectl-vp" {
		t.Errorf("got %q, want resumectl-vp", got)
	}
}

func TestAutoResolver_EscalatesWhenVpTaken(t *testing.T) {
	r := &AutoResolver{OnDisk: map[string]bool{"resumectl-vp": true}}
	got, err := r.Resolve("/a", "/b", "resumectl", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "resumectl-vp2" {
		t.Errorf("got %q, want resumectl-vp2", got)
	}
}

func TestAutoResolver_EscalatesPastOnDiskProjectDir(t *testing.T) {
	// Simulate a prior migration leaving `resumectl-vp/` on disk.
	r := &AutoResolver{OnDisk: map[string]bool{"resumectl-vp": true}}
	got, err := r.Resolve("/a", "/b", "resumectl", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "resumectl-vp2" {
		t.Errorf("got %q, want resumectl-vp2", got)
	}
}

func TestInteractiveResolver_AcceptsDefault(t *testing.T) {
	r := &InteractiveResolver{
		In:  strings.NewReader("\n"),
		Out: &bytes.Buffer{},
	}
	got, err := r.Resolve("/later", "/earlier", "resumectl", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "resumectl-vp" {
		t.Errorf("got %q, want resumectl-vp", got)
	}
}

func TestInteractiveResolver_AcceptsUserInput(t *testing.T) {
	r := &InteractiveResolver{
		In:  strings.NewReader("my-custom-slug\n"),
		Out: &bytes.Buffer{},
	}
	got, err := r.Resolve("/later", "/earlier", "dupe", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-custom-slug" {
		t.Errorf("got %q, want my-custom-slug", got)
	}
}

func TestInteractiveResolver_RejectsInvalidThenAccepts(t *testing.T) {
	// First input: INVALID (uppercase not allowed). Second: valid.
	r := &InteractiveResolver{
		In:  strings.NewReader("BadSlug!\nfinal-slug\n"),
		Out: &bytes.Buffer{},
	}
	got, err := r.Resolve("/later", "/earlier", "dupe", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "final-slug" {
		t.Errorf("got %q, want final-slug", got)
	}
}

func TestInteractiveResolver_RejectsTakenThenAccepts(t *testing.T) {
	r := &InteractiveResolver{
		In:  strings.NewReader("already-used\nnew-fresh\n"),
		Out: &bytes.Buffer{},
	}
	got, err := r.Resolve("/later", "/earlier", "dupe", map[string]bool{"already-used": true})
	if err != nil {
		t.Fatal(err)
	}
	if got != "new-fresh" {
		t.Errorf("got %q, want new-fresh", got)
	}
}

func TestInteractiveResolver_RejectsOnDiskThenAccepts(t *testing.T) {
	r := &InteractiveResolver{
		In:     strings.NewReader("on-disk\nok-slug\n"),
		Out:    &bytes.Buffer{},
		OnDisk: map[string]bool{"on-disk": true},
	}
	got, err := r.Resolve("/later", "/earlier", "dupe", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok-slug" {
		t.Errorf("got %q, want ok-slug", got)
	}
}

func TestInteractiveResolver_EOFReturnsAbort(t *testing.T) {
	r := &InteractiveResolver{
		In:  strings.NewReader(""), // immediate EOF
		Out: &bytes.Buffer{},
	}
	_, err := r.Resolve("/later", "/earlier", "dupe", map[string]bool{})
	if !errors.Is(err, ErrResolverAbort) {
		t.Errorf("expected ErrResolverAbort, got %v", err)
	}
}

func TestMapResolver_ExactMatch(t *testing.T) {
	r := &MapResolver{Map: map[string]string{"resumectl": "resumectl-src"}}
	got, err := r.Resolve("/a", "/b", "resumectl", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "resumectl-src" {
		t.Errorf("got %q, want resumectl-src", got)
	}
}

func TestMapResolver_FallsBackOnMiss(t *testing.T) {
	r := &MapResolver{
		Map:      map[string]string{"foo": "foo-x"},
		Fallback: &AutoResolver{},
	}
	got, err := r.Resolve("/a", "/b", "other", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "other-vp" {
		t.Errorf("got %q, want other-vp", got)
	}
}

func TestMapResolver_MissNoFallbackErrors(t *testing.T) {
	r := &MapResolver{Map: map[string]string{"foo": "foo-x"}}
	_, err := r.Resolve("/a", "/b", "missing", map[string]bool{})
	if err == nil {
		t.Fatal("expected error on map miss without fallback")
	}
}

func TestMapResolver_RejectsInvalidTarget(t *testing.T) {
	r := &MapResolver{Map: map[string]string{"foo": "BAD!!"}}
	_, err := r.Resolve("/a", "/b", "foo", map[string]bool{})
	if err == nil {
		t.Fatal("expected error on invalid mapped slug")
	}
}

func TestMapResolver_RejectsTakenTarget(t *testing.T) {
	r := &MapResolver{Map: map[string]string{"foo": "bar"}}
	_, err := r.Resolve("/a", "/b", "foo", map[string]bool{"bar": true})
	if err == nil {
		t.Fatal("expected error when mapped target already taken")
	}
}

func TestParseSlugMap_Empty(t *testing.T) {
	m, err := ParseSlugMap("")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestParseSlugMap_Single(t *testing.T) {
	m, err := ParseSlugMap("foo=bar")
	if err != nil {
		t.Fatal(err)
	}
	if m["foo"] != "bar" {
		t.Errorf("got %v", m)
	}
}

func TestParseSlugMap_Multi(t *testing.T) {
	m, err := ParseSlugMap("foo=bar,baz=qux-2")
	if err != nil {
		t.Fatal(err)
	}
	if m["foo"] != "bar" || m["baz"] != "qux-2" {
		t.Errorf("got %v", m)
	}
}

func TestParseSlugMap_WhitespaceTolerant(t *testing.T) {
	m, err := ParseSlugMap(" foo = bar , baz = qux ")
	if err != nil {
		t.Fatal(err)
	}
	if m["foo"] != "bar" || m["baz"] != "qux" {
		t.Errorf("got %v", m)
	}
}

func TestParseSlugMap_MissingEquals(t *testing.T) {
	_, err := ParseSlugMap("foo-bar")
	if err == nil {
		t.Fatal("expected error for missing =")
	}
}

func TestParseSlugMap_EmptyKeyOrValue(t *testing.T) {
	if _, err := ParseSlugMap("=bar"); err == nil {
		t.Error("expected error for empty key")
	}
	if _, err := ParseSlugMap("foo="); err == nil {
		t.Error("expected error for empty value")
	}
}

func TestParseSlugMap_DuplicateKey(t *testing.T) {
	_, err := ParseSlugMap("foo=bar,foo=baz")
	if err == nil {
		t.Fatal("expected error for duplicate key")
	}
}

func TestParseSlugMap_InvalidValue(t *testing.T) {
	_, err := ParseSlugMap("foo=BAD_SLUG")
	if err == nil {
		t.Fatal("expected error for invalid slug value")
	}
}

func TestScanOnDiskSlugs_MissingDir(t *testing.T) {
	got, err := scanOnDiskSlugs(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestScanOnDiskSlugs_Populated(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"proj-one", "Proj Two", "not-a-dir-file"} {
		if name == "not-a-dir-file" {
			os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644)
			continue
		}
		os.MkdirAll(filepath.Join(root, name), 0o755)
	}
	got, err := scanOnDiskSlugs(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got["proj-one"] || !got["proj-two"] {
		t.Errorf("missing expected slug in %v", got)
	}
	if got["not-a-dir-file"] {
		t.Errorf("file should not appear as slug: %v", got)
	}
}

func TestScanProjects_ResolvesCollisionViaAuto(t *testing.T) {
	root := t.TempDir()
	projectsDir := filepath.Join(root, "Projects")
	// "Test Project" sorts before "Test_Project" in ReadDir order (space = 0x20, _ = 0x5F).
	// Both slugify to "test-project".
	for _, name := range []string{"Test Project", "Test_Project"} {
		if err := os.MkdirAll(filepath.Join(projectsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	resolver := &AutoResolver{OnDisk: map[string]bool{}}
	projects, remap, err := scanProjects(root, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if len(remap) != 1 {
		t.Fatalf("got remap %v, want 1 entry", remap)
	}
	if remap["test-project"] != "test-project-vp" {
		t.Errorf("remap[test-project] = %q, want test-project-vp", remap["test-project"])
	}
}

func TestScanProjects_ResolverMappedBySlugMap(t *testing.T) {
	root := t.TempDir()
	projectsDir := filepath.Join(root, "Projects")
	for _, name := range []string{"resumectl", "resumectl_x"} {
		if err := os.MkdirAll(filepath.Join(projectsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Both slugify to "resumectl-x" and "resumectl"? Let's double-check:
	// "resumectl" → "resumectl", "resumectl_x" → "resumectl-x". Not a collision.
	// Make them collide:
	os.RemoveAll(projectsDir)
	for _, name := range []string{"resumectl", "RESUMECTL"} {
		if err := os.MkdirAll(filepath.Join(projectsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Both → "resumectl". RESUMECTL sorts before resumectl (uppercase R < lowercase r).
	resolver := &MapResolver{
		Map:      map[string]string{"resumectl": "resumectl-src"},
		Fallback: &AutoResolver{},
	}
	_, remap, err := scanProjects(root, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if remap["resumectl"] != "resumectl-src" {
		t.Errorf("remap = %v, want resumectl→resumectl-src", remap)
	}
}

func TestScanProjects_PropagatesResolverError(t *testing.T) {
	root := t.TempDir()
	projectsDir := filepath.Join(root, "Projects")
	for _, name := range []string{"Test Project", "Test_Project"} {
		if err := os.MkdirAll(filepath.Join(projectsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Interactive with EOF stdin aborts.
	resolver := &InteractiveResolver{In: strings.NewReader(""), Out: &bytes.Buffer{}}
	_, _, err := scanProjects(root, resolver)
	if !errors.Is(err, ErrResolverAbort) {
		t.Errorf("expected ErrResolverAbort, got %v", err)
	}
}

func TestScanProjects_NoResolverCollisionErrors(t *testing.T) {
	root := t.TempDir()
	projectsDir := filepath.Join(root, "Projects")
	for _, name := range []string{"Test Project", "Test_Project"} {
		if err := os.MkdirAll(filepath.Join(projectsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := scanProjects(root, nil)
	if err == nil {
		t.Fatal("expected error when no resolver configured and collision present")
	}
	if !strings.Contains(err.Error(), "slug collision") {
		t.Errorf("error should mention slug collision, got: %v", err)
	}
}
