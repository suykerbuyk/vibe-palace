// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import "testing"

func TestExtractEntitiesFilePaths(t *testing.T) {
	text := `We modified internal/capture/chunker.go and also updated
the config at config/settings.toml. See also main.py for reference.`

	entities := ExtractEntities(text)
	files := filterByType(entities, "file")
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

func TestExtractEntitiesURLs(t *testing.T) {
	text := `Check https://github.com/suykerbuyk/vibe-palace for details.
Also see http://example.com/api/v1 for the API docs.`

	entities := ExtractEntities(text)
	urls := filterByType(entities, "url")
	if len(urls) < 2 {
		t.Fatalf("expected at least 2 URL entities, got %d: %+v", len(urls), urls)
	}
}

func TestExtractEntitiesDedup(t *testing.T) {
	text := `File internal/storage/vault.go is important.
We also read internal/storage/vault.go again.`

	entities := ExtractEntities(text)
	files := filterByType(entities, "file")
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

func TestExtractEntitiesEmpty(t *testing.T) {
	entities := ExtractEntities("")
	if len(entities) != 0 {
		t.Errorf("expected 0 entities from empty text, got %d", len(entities))
	}
}

func TestExtractEntitiesShortPathsSkipped(t *testing.T) {
	// Very short matches like "a.go" should be skipped.
	text := "See a.go for details."
	entities := ExtractEntities(text)
	files := filterByType(entities, "file")
	for _, e := range files {
		if e.Name == "a.go" {
			t.Error("expected short path 'a.go' to be skipped")
		}
	}
}

func TestExtractEntitiesDependencyFilesSkipped(t *testing.T) {
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
		text := "Check " + dep + " for the dependency."
		entities := ExtractEntities(text)
		files := filterByType(entities, "file")
		for _, e := range files {
			if e.Name == dep {
				t.Errorf("expected dependency file %q to be skipped", dep)
			}
		}
	}
}

func TestExtractEntitiesSourceFilesKept(t *testing.T) {
	// Real source files should NOT be skipped regardless of language.
	sourceFiles := []string{
		"internal/capture/chunker.go",
		"src/components/App.tsx",
		"lib/parser.py",
		"src/main.rs",
		"app/models/user.rb",
	}
	for _, src := range sourceFiles {
		text := "Modified " + src + " for the feature."
		entities := ExtractEntities(text)
		files := filterByType(entities, "file")
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

func filterByType(entities []ExtractedEntity, typ string) []ExtractedEntity {
	var result []ExtractedEntity
	for _, e := range entities {
		if e.Type == typ {
			result = append(result, e)
		}
	}
	return result
}
