// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kg

import (
	"strings"
	"testing"
)

func TestExtractAllEmpty(t *testing.T) {
	got := ExtractAll("", "", ExtractAllOptions{})
	if len(got.Entities) != 0 || len(got.Triples) != 0 {
		t.Fatalf("expected empty result, got %+v", got)
	}

	got = ExtractAll("   \n\t  ", "", ExtractAllOptions{})
	if len(got.Entities) != 0 || len(got.Triples) != 0 {
		t.Fatalf("expected empty result for whitespace, got %+v", got)
	}
}

func TestExtractAllFrequencyFilterDropsSingletons(t *testing.T) {
	// Default threshold is 2; a file mentioned once must be filtered out.
	text := "We touched internal/foo/bar.go briefly."
	got := ExtractAll(text, "", ExtractAllOptions{})
	for _, e := range got.Entities {
		if e.Name == "internal/foo/bar.go" {
			t.Fatalf("expected singleton file to be dropped by frequency filter, got %+v", e)
		}
	}
}

func TestExtractAllFrequencyFilterPromotesRepeats(t *testing.T) {
	// Two mentions of the same file should survive the default threshold.
	text := "Edit internal/foo/bar.go now. Then retest internal/foo/bar.go."
	got := ExtractAll(text, "", ExtractAllOptions{})
	found := false
	for _, e := range got.Entities {
		if e.Name == "internal/foo/bar.go" && e.Type == "file" {
			found = true
			if e.MentionCount < 2 {
				t.Errorf("MentionCount = %d, want >= 2", e.MentionCount)
			}
		}
	}
	if !found {
		t.Fatalf("expected internal/foo/bar.go to be promoted, got entities=%+v", got.Entities)
	}
}

func TestExtractAllDedupAcrossExtractors(t *testing.T) {
	// "Next" is a known tool in knownTools; also "next" appears twice.
	// kg.DetectEntities can tag it as a tool. The file/URL regex will NOT
	// match it (no .ext). But we want to verify dedup semantics explicitly:
	// use a path-like token AND a kg-detector-visible capitalized word.
	//
	// Strategy: craft a transcript where a tool name ("Docker") is
	// mentioned >=2 times so the kg-detector promotes it, and where
	// there is also a file "Dockerfile.go" (synthetic) to make sure
	// (type,name) dedup works when types differ — different types must
	// NOT be deduped together.
	text := "We use Docker everywhere. Docker runs our containers. " +
		"Our build uses build/docker.go heavily. See build/docker.go again."

	got := ExtractAll(text, "", ExtractAllOptions{})

	// Count entities by normalized (type,name).
	type key struct{ t, n string }
	counts := map[key]int{}
	for _, e := range got.Entities {
		counts[key{e.Type, strings.ToLower(e.Name)}]++
	}
	for k, c := range counts {
		if c != 1 {
			t.Errorf("(type=%s,name=%s) appears %d times, want 1 (dedup failed)", k.t, k.n, c)
		}
	}

	// Specific: the file "build/docker.go" should appear exactly once as
	// type=file, and "Docker" should appear exactly once as type=tool.
	haveFile, haveTool := false, false
	for _, e := range got.Entities {
		if e.Type == "file" && e.Name == "build/docker.go" {
			haveFile = true
		}
		if e.Type == "tool" && strings.EqualFold(e.Name, "Docker") {
			haveTool = true
		}
	}
	if !haveFile {
		t.Error("expected file entity build/docker.go")
	}
	if !haveTool {
		t.Error("expected tool entity Docker")
	}
}

func TestExtractAllSingleExtractorOnly(t *testing.T) {
	// Text has only URL hits, no kg-detector-visible entities, no triples.
	text := "Go to https://example.com/docs right now. Again: https://example.com/docs."
	got := ExtractAll(text, "", ExtractAllOptions{})

	found := false
	for _, e := range got.Entities {
		if e.Type == "url" && strings.HasPrefix(e.Name, "https://example.com") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected URL entity, got %+v", got.Entities)
	}
}

func TestExtractAllCustomThreshold(t *testing.T) {
	// Threshold=1 should admit singletons.
	text := "Touched internal/one/two.go once."
	got := ExtractAll(text, "", ExtractAllOptions{MinMentions: 1})
	found := false
	for _, e := range got.Entities {
		if e.Name == "internal/one/two.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected singleton admitted with MinMentions=1, got %+v", got.Entities)
	}

	// Threshold=5 should drop even doubletons.
	text2 := "Edit internal/one/two.go now. Then retest internal/one/two.go."
	got2 := ExtractAll(text2, "", ExtractAllOptions{MinMentions: 5})
	for _, e := range got2.Entities {
		if e.Name == "internal/one/two.go" {
			t.Fatalf("expected doubleton dropped with MinMentions=5, got %+v", e)
		}
	}
}

func TestExtractAllTriplesForwarded(t *testing.T) {
	// A "X works on Y" pattern where Alice is a detected person.
	text := "Alice said something important. Alice works on vibe-palace."
	got := ExtractAll(text, "2026-04-14", ExtractAllOptions{MinMentions: 1})
	if len(got.Triples) == 0 {
		t.Fatalf("expected at least one triple, got none; entities=%+v", got.Entities)
	}
}

func TestExtractFilesAndURLsRegexParity(t *testing.T) {
	// Sanity: the re-homed regexes produce the same canonical output shape.
	text := "Edit internal/foo/bar.go and visit https://example.com for details."
	got := ExtractFilesAndURLs(text)
	var haveFile, haveURL bool
	for _, r := range got {
		if r.Type == "file" && r.Name == "internal/foo/bar.go" {
			haveFile = true
		}
		if r.Type == "url" && r.Name == "https://example.com" {
			haveURL = true
		}
	}
	if !haveFile || !haveURL {
		t.Fatalf("parity check failed, got %+v", got)
	}
}

func TestExtractFilesAndURLsFilePaths(t *testing.T) {
	text := `We modified internal/capture/chunker.go and also updated
the config at config/settings.toml. See also main.py for reference.`

	files := filterByType(ExtractFilesAndURLs(text), "file")
	if len(files) < 2 {
		t.Fatalf("expected at least 2 file entities, got %d: %+v", len(files), files)
	}

	found := map[string]bool{}
	for _, e := range files {
		found[e.Name] = true
	}
	for _, want := range []string{"internal/capture/chunker.go", "config/settings.toml"} {
		if !found[want] {
			t.Errorf("expected file entity %q, found: %v", want, found)
		}
	}
}

func TestExtractFilesAndURLsURLs(t *testing.T) {
	text := `Check https://github.com/suykerbuyk/vibe-palace for details.
Also see http://example.com/api/v1 for the API docs.`

	urls := filterByType(ExtractFilesAndURLs(text), "url")
	if len(urls) < 2 {
		t.Fatalf("expected at least 2 URL entities, got %d: %+v", len(urls), urls)
	}
}

func TestExtractFilesAndURLsDedup(t *testing.T) {
	text := `File internal/storage/vault.go is important.
We also read internal/storage/vault.go again.`

	files := filterByType(ExtractFilesAndURLs(text), "file")
	count := 0
	for _, e := range files {
		if e.Name == "internal/storage/vault.go" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 deduplicated entry for vault.go, got %d", count)
	}
}

func TestExtractFilesAndURLsEmpty(t *testing.T) {
	if got := ExtractFilesAndURLs(""); len(got) != 0 {
		t.Errorf("expected 0 entities from empty text, got %d", len(got))
	}
}

func TestExtractFilesAndURLsShortPathsSkipped(t *testing.T) {
	// Very short matches like "a.go" should be skipped.
	files := filterByType(ExtractFilesAndURLs("See a.go for details."), "file")
	for _, e := range files {
		if e.Name == "a.go" {
			t.Error("expected short path 'a.go' to be skipped")
		}
	}
}

func TestExtractFilesAndURLsDependencyFilesSkipped(t *testing.T) {
	// Dependency manifests and lock files across languages should be skipped.
	depFiles := []string{
		"go.mod", "go.sum",
		"package.json", "package-lock.json",
		"requirements.txt",
		"cargo.toml", "cargo.lock",
		"gemfile", "gemfile.lock",
		"pom.xml",
		"composer.json", "composer.lock",
		"pubspec.yaml", "pubspec.lock",
		"mix.exs", "mix.lock",
		"conanfile.txt", "conanfile.py", "conan.lock",
		"vcpkg.json",
	}
	for _, dep := range depFiles {
		files := filterByType(ExtractFilesAndURLs("Check "+dep+" for the dependency."), "file")
		for _, e := range files {
			if e.Name == dep {
				t.Errorf("expected dependency file %q to be skipped", dep)
			}
		}
	}
}

func TestExtractFilesAndURLsSourceFilesKept(t *testing.T) {
	// Real source files should NOT be skipped regardless of language.
	sourceFiles := []string{
		"internal/capture/chunker.go",
		"src/components/App.tsx",
		"lib/parser.py",
		"src/main.rs",
		"app/models/user.rb",
	}
	for _, src := range sourceFiles {
		files := filterByType(ExtractFilesAndURLs("Modified "+src+" for the feature."), "file")
		found := false
		for _, e := range files {
			if e.Name == src {
				found = true
			}
		}
		if !found {
			t.Errorf("expected source file %q to be extracted", src)
		}
	}
}

func filterByType(entities []FileOrURL, typ string) []FileOrURL {
	var result []FileOrURL
	for _, e := range entities {
		if e.Type == typ {
			result = append(result, e)
		}
	}
	return result
}
