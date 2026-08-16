// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// writeIterations writes one project's iterations.md into a fixture vault.
func writeIterations(t *testing.T, vault, project, body string) {
	t.Helper()
	dir := filepath.Join(vault, "Projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iterations.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// frame composes an entry exactly as storage.(*Vault).AppendIterationOwned does.
// Fixtures are built through the WRITER, never by hand-typing its output: a
// fixture that types the frame itself stops testing the contract the moment the
// writer changes.
func frame(n int, title, body string) string {
	return "\n---\n" + wrapstate.FormatIterationHeader(n, title) + "\n\n" + body + "\n"
}

// dirtyArchive carries one instance of each of the three defect classes AND the
// legal shapes each rule must stay quiet about. Both halves matter: this
// codebase deleted a heading rule that invented 17 findings across 6 projects,
// and an auditor that invents findings is worse than one that misses them.
func dirtyArchive() string {
	return strings.Join([]string{
		"# p — Iteration Narratives",
		"",
		"## Iteration Narratives", // section title, not frame-adjacent
		"",
		frame(5, "canonical", "prose\n\n## Phase 1\n\nin-body sub-heading, not a boundary"),
		"\n---\n## 2026-06-17 Wrap\n\nframe orphan: no number at all, unaddressable",
		frame(146, "prior", "prose\n\n## Iteration 147\n\nnumbered but titleless, and NOT frame-adjacent"),
		"\n---\n## Iteration 40 — Iteration 40 — Global AI Eric prep addendum\n\ndoubled prefix",
		frame(200, "fenced", "sample text:\n\n```md\n### Iteration 999 — sample\n```"),
		"\n---\nshape: bookkeeping\nsummary: a session\n---\n## Session arc\n\nunder a YAML closer",
		frame(177, "shipped", "body"),
		frame(177, "addendum: the sweep staged as commit 2", "body"),
	}, "\n")
}

// TestCheckIterationHeadings_FlagsEachClassOnce is the producer's end of the
// contract: every defect class reaches the report, each one names the
// vault-RELATIVE path and its 1-indexed line, and the legal shapes stay silent.
func TestCheckIterationHeadings_FlagsEachClassOnce(t *testing.T) {
	root := t.TempDir()
	writeIterations(t, root, "dirty", dirtyArchive())

	r := CheckIterationHeadings(storage.NewVault(root))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info (advisory — repair is a write and a judgement call): %+v",
			r.Status, r)
	}
	joined := strings.Join(r.Details, "\n")

	for _, want := range []struct {
		heading string
		class   wrapstate.HeadingDefectClass
	}{
		{"## 2026-06-17 Wrap", wrapstate.DefectFrameOrphan},
		{"## Iteration 147", wrapstate.DefectNonCanonicalNumbered},
		{"## Iteration 40 — Iteration 40 — Global AI Eric prep addendum", wrapstate.DefectDoubledPrefix},
	} {
		line := lineOfHeading(t, dirtyArchive(), want.heading)
		locator := fmt.Sprintf("Projects/dirty/iterations.md:%d [%s]", line, want.class)
		if !strings.Contains(joined, locator) {
			t.Errorf("report is missing %q for %q. A finding without the file and line is a finding\n"+
				"nobody can act on.\n%s", locator, want.heading, joined)
		}
	}

	for _, quiet := range []string{
		"## Iteration Narratives",
		"## Phase 1",
		"## Session arc",
		"### Iteration 999 — sample",
		"## Iteration 5 — canonical",
		"## Iteration 177 — shipped",
		"## Iteration 177 — addendum: the sweep staged as commit 2",
	} {
		if strings.Contains(joined, quiet+" →") {
			t.Errorf("%q was flagged; it is legal archive content:\n%s", quiet, joined)
		}
	}

	if !strings.Contains(r.Summary, "3 defective headings") {
		t.Errorf("summary = %q, want the three defects counted", r.Summary)
	}
}

// TestCheckIterationHeadings_CleanAndAbsent: a clean archive Passes, a project
// with no archive is not a defect, and no vault at all is Skip — never a bogus
// Pass, which would report "clean" to an agent that never looked at anything.
func TestCheckIterationHeadings_CleanAndAbsent(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		root := t.TempDir()
		writeIterations(t, root, "clean", "# p — Iteration Narratives\n"+
			frame(1, "one", "body")+frame(2, "two", "body\n\n## In-body section\n\nmore"))
		r := CheckIterationHeadings(storage.NewVault(root))
		if r.Status != Pass {
			t.Fatalf("a clean archive = %v, want Pass: %+v\n%s", r.Status, r, strings.Join(r.Details, "\n"))
		}
	})

	t.Run("no_archive", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "Projects", "fresh"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := CheckIterationHeadings(storage.NewVault(root))
		if r.Status != Pass {
			t.Errorf("a project with no iterations.md = %v, want Pass: %+v", r.Status, r)
		}
	})

	t.Run("no_projects_dir", func(t *testing.T) {
		r := CheckIterationHeadings(storage.NewVault(t.TempDir()))
		if r.Status != Pass || r.Summary != "no Projects/ directory" {
			t.Errorf("got %+v, want a Pass naming the absent directory", r)
		}
	})

	t.Run("no_vault", func(t *testing.T) {
		r := CheckIterationHeadings(storage.NewVault(""))
		if r.Status != Skip || r.Summary != "no vault configured" {
			t.Errorf("got %+v, want the shared Skip/no vault configured row", r)
		}
	})
}

// TestIterationHeadingsProducer covers the registry entry — the only path an
// MCP host can reach this check by. A check that exists but is not registered
// reaches nobody, which is the failure the selector registry exists to end.
func TestIterationHeadingsProducer(t *testing.T) {
	root := t.TempDir()
	writeIterations(t, root, "dirty", dirtyArchive())

	rs, err := RunSelected(root, "iteration-headings")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(rs) != 1 || rs[0].Name != "Iteration headings" {
		t.Fatalf("got %+v, want one Iteration headings row", rs)
	}
	if rs[0].Status != Info {
		t.Errorf("status = %v, want Info: %+v", rs[0].Status, rs[0])
	}

	rs, err = RunSelected("", "iteration-headings")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if rs[0].Status != Skip || rs[0].Summary != "no vault configured" {
		t.Errorf("no-vault row = %+v, want the shared Skip contract", rs[0])
	}
}

// lineOfHeading locates a heading in the fixture independently of the scan under
// test, so an off-by-one in the reported line fails instead of agreeing.
func lineOfHeading(t *testing.T, content, heading string) int {
	t.Helper()
	found := 0
	for i, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) != heading {
			continue
		}
		if found != 0 {
			t.Fatalf("heading %q appears twice; the fixture must be unambiguous", heading)
		}
		found = i + 1
	}
	if found == 0 {
		t.Fatalf("heading %q is not in the fixture", heading)
	}
	return found
}
