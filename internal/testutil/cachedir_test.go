// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCacheDir_IsDirectory(t *testing.T) {
	dir := ProjectCacheDir(t)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %q: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", dir)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), ".cache/models") {
		t.Errorf("expected path to end with .cache/models, got %q", dir)
	}
}

func TestProjectCacheDir_Idempotent(t *testing.T) {
	a := ProjectCacheDir(t)
	b := ProjectCacheDir(t)
	if a != b {
		t.Errorf("ProjectCacheDir not stable: %q vs %q", a, b)
	}
}
