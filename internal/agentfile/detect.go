// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
)

// Target describes one agent file to wire. Multiple candidate filenames that
// resolve (via symlink) to the same underlying file collapse into a single
// Target; the primary candidate becomes DisplayName and the rest land in
// Aliases for the status row.
type Target struct {
	// Path is the canonical (symlink-resolved) absolute path. Writes happen here.
	Path string
	// DisplayName is the first-detected candidate's relative path from the project root.
	DisplayName string
	// Aliases lists other candidate relative paths that resolved to the same canonical path.
	Aliases []string
}

// Skip describes a candidate file that is intentionally not wired, with a
// user-facing reason. The only current case is
// `.github/copilot-instructions.md` when `.github/` does not exist.
type Skip struct {
	DisplayName string
	Reason      string
}

// candidate is a file we look for in the project root. requireDir, if set,
// is a subdirectory (relative to root) that must exist before we'll wire the
// file; missing dir produces a Skip entry rather than silent omission.
type candidate struct {
	rel        string
	requireDir string
}

// candidates is ordered: the first detected (by realpath) wins DisplayName.
var candidates = []candidate{
	{rel: "CLAUDE.md"},
	{rel: "AGENTS.md"},
	{rel: ".cursorrules"},
	{rel: ".rules"},
	{rel: ".github/copilot-instructions.md", requireDir: ".github"},
}

// Detect enumerates agent files present in projectRoot, canonicalizing via
// realpath so a symlink loop produces one Target with aliases. Returns the
// wirable targets and any deliberate skips (currently only the copilot case).
func Detect(projectRoot string) (targets []Target, skips []Skip) {
	type group struct {
		canonical string
		members   []string
	}
	byCanonical := map[string]*group{}
	var order []string

	for _, c := range candidates {
		abs := filepath.Join(projectRoot, c.rel)
		if c.requireDir != "" {
			info, err := os.Stat(filepath.Join(projectRoot, c.requireDir))
			if err != nil || !info.IsDir() {
				// Report the skip unconditionally so users see the hint on how
				// to enable copilot wiring (create .github/ then re-run init).
				skips = append(skips, Skip{
					DisplayName: c.rel,
					Reason:      c.requireDir + "/ not present; create the dir to wire",
				})
				continue
			}
		}
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		canonical, err := filepath.EvalSymlinks(abs)
		if err != nil {
			// Non-fatal: treat the literal path as canonical. Write will fail
			// later with a clear error if the file is genuinely broken.
			canonical = abs
		}
		if g, ok := byCanonical[canonical]; ok {
			g.members = append(g.members, c.rel)
		} else {
			byCanonical[canonical] = &group{canonical: canonical, members: []string{c.rel}}
			order = append(order, canonical)
		}
	}

	for _, c := range order {
		g := byCanonical[c]
		targets = append(targets, Target{
			Path:        g.canonical,
			DisplayName: g.members[0],
			Aliases:     append([]string(nil), g.members[1:]...),
		})
	}
	return targets, skips
}
