// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "testing"

func TestClassifyProjectPath(t *testing.T) {
	for _, c := range []struct {
		rel  string
		want ProjectPathClass
	}{
		// Content: the project itself, ignored backups included.
		{"Projects/alpha/resume.md", ProjectContent},
		{"Projects/alpha/sessions/2026-09-23-aaaa-01.md", ProjectContent},
		{"Projects/alpha/transcripts/x.manifest.json", ProjectContent},
		{"Projects/alpha/transcripts/x.manifest.json.1234.bak", ProjectContent},
		{"Projects/alpha/tasks/done/t.md", ProjectContent},
		{"Projects/alpha/commit-log.md", ProjectContent},
		{"palace/alpha/kg/entities.jsonl", ProjectContent},
		{"palace/alpha/ingested-archives.jsonl", ProjectContent},
		{"Knowledge/learnings/l.md", ProjectContent},
		// The retired config is matched by depth, never by base name.
		{"Projects/alpha/doc/config.toml", ProjectContent},
		{"palace/alpha/config.toml", ProjectContent},
		{"Projects/alpha/config.toml", ProjectRetired},
		// Vault-bound stamps, at any depth and outside project trees too.
		{"Projects/alpha/.surface", ProjectVaultBound},
		{"Projects/alpha/notes/.surface", ProjectVaultBound},
		{"palace/alpha/.surface", ProjectVaultBound},
		{"Audits/.surface", ProjectVaultBound},
		{"Projects/alpha/commit-log.anchor", ProjectVaultBound},
		{"Projects/alpha/notes/commit-log.anchor", ProjectVaultBound},
		{"Projects/alpha/surface", ProjectContent},
		// Machine-local: any .local or .vp-locks component, and it wins.
		{"palace/alpha/.local/imported-sessions.jsonl", ProjectMachineLocal},
		{"palace/alpha/.local/embed-cache/d1.vec", ProjectMachineLocal},
		{"palace/.local/embed-cache/alpha/d1.vec", ProjectMachineLocal},
		{"Projects/alpha/sessions/.local/scratch.txt", ProjectMachineLocal},
		{"Projects/alpha/.vp-locks/lock", ProjectMachineLocal},
		{"Projects/alpha/.local/.surface", ProjectMachineLocal},
		{"Projects/alpha/.localish/x.md", ProjectContent},
	} {
		if got := ClassifyProjectPath(c.rel); got != c.want {
			t.Errorf("ClassifyProjectPath(%q) = %s, want %s", c.rel, got, c.want)
		}
	}
	if got := ProjectTrees("alpha"); len(got) != 2 || got[0] != "palace/alpha" || got[1] != "Projects/alpha" {
		t.Errorf("ProjectTrees = %v", got)
	}
}
