// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// testVault builds a vault with test-proj initialised and its palace structure
// present. It is the shared fixture for the capture package's session tests;
// the transcript-indexer it was first written for is gone (ADR-014 decision 7:
// capture no longer indexes transcripts), but the fixture it provides is still
// what every session test builds on.
func testVault(t *testing.T) *storage.Vault {
	t.Helper()
	dir := t.TempDir()
	v := storage.NewVault(dir)
	// test-proj is the slug most tests here write; the vault's write
	// primitives refuse a project the vault has not initialised.
	testutil.InitProject(t, dir, "test-proj")
	// Ensure palace structure exists.
	if err := storage.EnsureDir(dir + "/palace/test-proj/drawers/test-proj/general"); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"file-internal/capture/indexer.go", "file-internal-capture-indexer-go"},
		{"url-https://example.com", "url-https-example-com"},
		{"UPPER Case", "upper-case"},
		{"---multi---dashes---", "multi-dashes"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slug.Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
