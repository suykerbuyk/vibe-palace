// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// InitProject makes each slug an initialised project in the vault at
// vaultRoot, the state `vp init` leaves: it writes the two scaffold markers
// (Projects/<slug>/commands/README.md and skills/README.md) that
// projectdir.ClassifyProjectDir reads as ScaffoldOnly. A marker already present
// is left as it is.
//
// TEST-ONLY BY DESIGN. The vault's write primitives refuse a write into a
// project the vault has not initialised (projectdir.RefuseUninitialisedAbs), so
// a fixture that writes project content without running `vp init` first calls
// this. It writes with os.WriteFile, deliberately not through atomicfile: a
// fixture is not a project-creating writer, and the creating-project option is
// owned by the init scaffold and the lifecycle copy alone. The marker bytes are
// a fixed fixture text, not vp's current stub, so nothing mistakes a fixture
// marker for one the scaffold wrote.
func InitProject(t testing.TB, vaultRoot string, slugs ...string) {
	t.Helper()
	for _, slug := range slugs {
		for _, rel := range []string{"commands/README.md", "skills/README.md"} {
			p := filepath.Join(vaultRoot, "Projects", slug, filepath.FromSlash(rel))
			if _, err := os.Lstat(p); err == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("InitProject %s: %v", slug, err)
			}
			if err := os.WriteFile(p, []byte("test fixture: initialised by testutil.InitProject\n"), 0o644); err != nil {
				t.Fatalf("InitProject %s: %v", slug, err)
			}
		}
	}
}
