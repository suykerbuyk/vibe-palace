// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testutil provides shared test helpers for the vibe-palace project.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// ProjectCacheDir returns the project-local .cache/models directory,
// walking up from the test's working directory to find the project root.
// Checks for common project root markers (go.mod, package.json, Cargo.toml,
// pyproject.toml) and falls back to .git as a universal marker.
// This cache persists between test runs and is gitignored. It is only
// cleared by `make dist-clean` or `git clean -fxd`.
func ProjectCacheDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	markers := []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", ".git"}

	for {
		for _, marker := range markers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				cache := filepath.Join(dir, ".cache", "models")
				if err := os.MkdirAll(cache, 0755); err != nil {
					t.Fatalf("create cache dir: %v", err)
				}
				return cache
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (no go.mod, package.json, Cargo.toml, pyproject.toml, or .git)")
		}
		dir = parent
	}
}
