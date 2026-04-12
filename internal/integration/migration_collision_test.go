// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/migrate"
)

// TestIntegrationMigrateSlugCollisionAutoResolve proves that a real vault
// with two colliding project directories is handled end-to-end:
//   - AutoResolver renames the later-sorted directory to `<slug>-vp`,
//   - dry-run surfaces the remap without touching storage,
//   - a real migration import produces drawers under both slugs.
func TestIntegrationMigrateSlugCollisionAutoResolve(t *testing.T) {
	h := newHarness(t, false) // mock embedder is fine

	projectsDir := filepath.Join(h.Vault.Root, "Projects")

	// Two names that collide on slug.Slugify → "shared-name".
	// "shared-name" sorts before "shared_name" (hyphen 0x2D < underscore 0x5F),
	// so "shared_name" is the later entry and gets the `-vp` suffix.
	first := filepath.Join(projectsDir, "shared-name", "sessions")
	second := filepath.Join(projectsDir, "shared_name", "sessions")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const tmpl = `---
session_id: "%s"
project: x
date: "2026-04-01"
title: "t"
summary: "s"
---
## Transcript
Body content for %s.
`
	writeSession := func(dir, id string) {
		content := []byte(fmtSessionTmpl(tmpl, id, id))
		if err := os.WriteFile(filepath.Join(dir, id+".md"), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSession(first, "sess-one")
	writeSession(second, "sess-two")

	// Dry run first — remap should populate, no storage writes.
	dryResult, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{
			DryRun:   true,
			Resolver: &migrate.AutoResolver{},
		},
	)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dryResult.ProjectsScanned != 2 {
		t.Errorf("dry ProjectsScanned = %d, want 2", dryResult.ProjectsScanned)
	}
	if got := dryResult.SlugRemap["shared-name"]; got != "shared-name-vp" {
		t.Errorf("dry SlugRemap[shared-name] = %q, want shared-name-vp", got)
	}

	// Verify no drawers exist yet.
	for _, proj := range []string{"shared-name", "shared-name-vp"} {
		n, _ := countAllDrawers(t, h.Vault, proj)
		if n != 0 {
			t.Errorf("dry-run left %d drawers under %q", n, proj)
		}
	}

	// Real run — both slugs end up with content.
	result, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{Resolver: &migrate.AutoResolver{}},
	)
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if result.SessionsImported != 2 {
		t.Errorf("SessionsImported = %d, want 2", result.SessionsImported)
	}
	if got := result.SlugRemap["shared-name"]; got != "shared-name-vp" {
		t.Errorf("SlugRemap[shared-name] = %q, want shared-name-vp", got)
	}

	// Verify drawers landed under both slugs (one per session).
	n1, _ := countAllDrawers(t, h.Vault, "shared-name")
	n2, _ := countAllDrawers(t, h.Vault, "shared-name-vp")
	if n1 == 0 {
		t.Error("expected drawers under shared-name")
	}
	if n2 == 0 {
		t.Error("expected drawers under shared-name-vp")
	}
}

// TestIntegrationMigrateSlugCollisionEscalatesPastOnDisk proves that when
// an earlier migration already produced `{slug}-vp/`, a fresh run with
// a new collision escalates to `{slug}-vp2`.
func TestIntegrationMigrateSlugCollisionEscalatesPastOnDisk(t *testing.T) {
	h := newHarness(t, false)

	projectsDir := filepath.Join(h.Vault.Root, "Projects")

	// Pre-existing directory from a "prior migration run":
	if err := os.MkdirAll(filepath.Join(projectsDir, "foo-vp"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Two new colliders:
	for _, name := range []string{"foo", "FOO"} {
		if err := os.MkdirAll(filepath.Join(projectsDir, name, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	result, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{
			DryRun: true,
			// Nil resolver triggers default AutoResolver with on-disk seeded.
		},
	)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SlugRemap["foo"]; got != "foo-vp2" {
		t.Errorf("SlugRemap[foo] = %q, want foo-vp2 (foo-vp is on disk)", got)
	}
}

// fmtSessionTmpl substitutes a simple "%s"-based template without pulling fmt.
func fmtSessionTmpl(tmpl string, args ...string) string {
	out := tmpl
	for _, a := range args {
		idx := indexOfPct(out)
		if idx < 0 {
			break
		}
		out = out[:idx] + a + out[idx+2:]
	}
	return out
}

func indexOfPct(s string) int {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '%' && s[i+1] == 's' {
			return i
		}
	}
	return -1
}
