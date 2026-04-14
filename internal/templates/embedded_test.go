// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

var hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestWalkEmbedded_MinimumCount(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	if len(resources) < 3 {
		t.Fatalf("expected >= 3 embedded resources, got %d", len(resources))
	}
}

func TestWalkEmbedded_ResourceInvariants(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	seen := make(map[string]bool)
	for _, r := range resources {
		if len(r.Bytes) == 0 {
			t.Errorf("resource %q has empty Bytes", r.RelPath)
		}
		if !hexSHA.MatchString(r.SHA256) {
			t.Errorf("resource %q SHA256 %q is not 64 hex chars", r.RelPath, r.SHA256)
		}
		sum := sha256.Sum256(r.Bytes)
		want := hex.EncodeToString(sum[:])
		if r.SHA256 != want {
			t.Errorf("resource %q SHA256 mismatch: got %s want %s", r.RelPath, r.SHA256, want)
		}
		if strings.HasPrefix(r.RelPath, "/") {
			t.Errorf("resource %q has leading slash", r.RelPath)
		}
		if strings.HasPrefix(r.RelPath, "templates/") {
			t.Errorf("resource %q still has templates/ prefix", r.RelPath)
		}
		if strings.Contains(r.RelPath, "..") {
			t.Errorf("resource %q contains ..", r.RelPath)
		}
		if !strings.HasSuffix(r.RelPath, ".md") {
			t.Errorf("resource %q does not end in .md", r.RelPath)
		}
		if seen[r.RelPath] {
			t.Errorf("resource %q appears more than once", r.RelPath)
		}
		seen[r.RelPath] = true
	}
}

func TestEmbeddedSHA_KnownAndUnknown(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("no embedded resources to pivot from")
	}
	known := resources[0]
	got, ok := EmbeddedSHA(known.RelPath)
	if !ok {
		t.Fatalf("EmbeddedSHA(%q) returned not-found for a known resource", known.RelPath)
	}
	if got != known.SHA256 {
		t.Errorf("EmbeddedSHA(%q) = %s, want %s", known.RelPath, got, known.SHA256)
	}

	got, ok = EmbeddedSHA("definitely/not/a/real/resource.md")
	if ok {
		t.Errorf("EmbeddedSHA for unknown path returned ok=true, got sha=%q", got)
	}
	if got != "" {
		t.Errorf("EmbeddedSHA for unknown path returned non-empty sha %q", got)
	}
}

// lookupThroughHook calls EmbeddedSHA indirectly so we exercise the
// package-level function-variable seam — an override installed by a
// test must be visible to arbitrary callers in the same process.
func lookupThroughHook(relPath string) (string, bool) {
	return EmbeddedSHA(relPath)
}

func TestEmbeddedSHA_OverrideHook(t *testing.T) {
	original := EmbeddedSHA
	defer func() { EmbeddedSHA = original }()

	const sentinel = "deadbeef"
	EmbeddedSHA = func(relPath string) (string, bool) {
		if relPath == "virtual/resource.md" {
			return sentinel, true
		}
		return "", false
	}

	got, ok := lookupThroughHook("virtual/resource.md")
	if !ok || got != sentinel {
		t.Fatalf("override not honored: got (%q, %v) want (%q, true)", got, ok, sentinel)
	}
	if _, ok := lookupThroughHook("commands/wrap.md"); ok {
		t.Fatalf("override should have hidden real resources")
	}

	// Restore via defer and confirm real lookups work again via a
	// secondary assertion inside this test (defer fires after).
	EmbeddedSHA = original
	if _, ok := lookupThroughHook("commands/wrap.md"); !ok {
		t.Fatalf("restored EmbeddedSHA failed to resolve a known resource")
	}
}

func TestFS_ContainsTemplatesRoot(t *testing.T) {
	fsys := FS()
	// Reading through the exported FS should locate at least one
	// known embedded file under the templates/ root.
	entries, err := fsys.ReadDir("templates")
	if err != nil {
		t.Fatalf("ReadDir(templates): %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("templates/ is empty in exported FS")
	}
}
