// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"os"
	"path/filepath"
	"testing"
)

// projectCacheDir returns the project-local .cache/models directory,
// walking up from the test's working directory to find the project root
// (identified by go.mod). This cache persists between test runs and is
// gitignored. It is only cleared by `make dist-clean` or `git clean -fxd`.
func projectCacheDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			cache := filepath.Join(dir, ".cache", "models")
			if err := os.MkdirAll(cache, 0755); err != nil {
				t.Fatalf("create cache dir: %v", err)
			}
			return cache
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}
