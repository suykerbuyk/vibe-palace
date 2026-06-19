// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package memorytestutil provides dependency-light helpers for materializing
// fake Claude native memory directories in tests. It lives in a normal
// (non-test) package so it can be imported by both internal/storage tests and
// the Phase 3 internal/memory harvest tests.
package memorytestutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// nativeMemoryFile is one file written into a fake native memory directory.
type nativeMemoryFile struct {
	name    string
	content string
}

// nativeFixtureFiles is the canonical set of fixture files: a MEMORY.md index
// plus a few typed memory files using Claude's native metadata.type frontmatter.
var nativeFixtureFiles = []nativeMemoryFile{
	{
		name: "MEMORY.md",
		content: `# Memory

- [Prefers tabs](pref-foo.md)
- [Project layout](proj-bar.md)
- [Editor of choice](pref-baz.md)
`,
	},
	{
		name: "pref-foo.md",
		content: `---
name: Prefers tabs
description: User prefers tabs over spaces for indentation.
metadata:
  type: feedback
---

The user prefers tabs over spaces for indentation in all source files.
`,
	},
	{
		name: "proj-bar.md",
		content: `---
name: Project layout
description: The repository uses a standard internal/ layout.
metadata:
  type: project
---

The repository follows the standard Go internal/ package layout.
`,
	},
	{
		name: "pref-baz.md",
		content: `---
name: Editor of choice
description: User edits with Zed.
metadata:
  type: user
---

The user primarily edits code with the Zed editor.
`,
	},
}

// WriteNativeMemoryFixture materializes a fake Claude native memory directory
// at dir: a MEMORY.md index with "- [Title](file.md)" lines and several typed
// memory files (feedback/project/user) using the native metadata.type
// frontmatter shape. The directory is created if it does not exist.
func WriteNativeMemoryFixture(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create memory fixture dir: %w", err)
	}
	for _, f := range nativeFixtureFiles {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("write fixture %s: %w", f.name, err)
		}
	}
	return nil
}
