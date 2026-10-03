// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"os/exec"
	"strings"
	"testing"
)

// internal/indexstore never depends on internal/search, directly or through
// another package: the search engine will import this package (its reaper and
// its freshness check), so the reverse edge would be an import cycle.
func TestIndexstoreDoesNotImportSearch(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/suykerbuyk/vibe-palace/internal/search" || dep == "github.com/suykerbuyk/vibe-palace/internal/capture" {
			t.Fatalf("internal/indexstore depends on %s", dep)
		}
	}
}
