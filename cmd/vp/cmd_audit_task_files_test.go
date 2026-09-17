// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedTaskFile writes a task into an archive directory (or the active one when sub
// is "").
func seedTaskFile(t *testing.T, v *storage.Vault, project, sub, slug, body string) string {
	t.Helper()
	dir := filepath.Join(v.Root, "Projects", project, "tasks", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rel := "Projects/" + project + "/tasks/"
	if sub != "" {
		rel += sub + "/"
	}
	return rel + slug + ".md"
}

const (
	validArchived   = "# T\n\n**Status:** done\n**Priority:** medium\n\n## Context\n\nbody\n"
	noSectionFile   = "# T\n\n**Status:** done\n**Priority:** medium\n\nno section\n"
	twoPriorityFile = "# T\n\n**Status:** done\n**Priority:** medium\n**Priority:** high\n\n## C\n\nbody\n"
	fencedH2File    = "# T\n\n**Status:** done\n**Priority:** medium\n\np\n\n```md\n## Fenced\n```\n\ntail\n"
)

// TestTaskFileValidityReportDistinguishesClasses: an undifferentiated list of every
// malformed file is close to useless to whoever does the repair, because the live
// corpus is dominated by a single class and the repair for each class is different.
// The roll-up is what tells them which of the validator's rules they are facing
// before they open anything.
//
// Break: delete the roll-up block from printTaskFileValidityReport, or group by the
// whole message instead of the prefix (every parameterised message then becomes its
// own class and the counts go to 1).
func TestTaskFileValidityReportDistinguishesClasses(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTaskFile(t, vault, "p", "done", "a", noSectionFile)
	seedTaskFile(t, vault, "p", "done", "b", noSectionFile)
	seedTaskFile(t, vault, "p", "done", "c", twoPriorityFile)

	rep, err := runTaskFileValidityReport(vault.Root, "")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printTaskFileValidityReport(&buf, vault.Root, rep)
	out := buf.String()

	if !strings.Contains(out, "By class") {
		t.Fatalf("the report must group findings by class:\n%s", out)
	}
	// Most-populous class first, with its real count.
	if !strings.Contains(out, "   2  missing section") {
		t.Errorf("the dominant class must be counted and listed first:\n%s", out)
	}
	if !strings.Contains(out, "   1  two Priority lines") {
		t.Errorf("each class must carry its own count:\n%s", out)
	}
	// The per-file list survives alongside it — a count is not re-derivable, a path is.
	for _, slug := range []string{"a.md", "b.md", "c.md"} {
		if !strings.Contains(out, slug) {
			t.Errorf("the per-file list must name %s:\n%s", slug, out)
		}
	}
	// And the command must say plainly that it never writes.
	if !strings.Contains(out, "REPORT ONLY") {
		t.Errorf("the report must state that it writes nothing:\n%s", out)
	}
}
