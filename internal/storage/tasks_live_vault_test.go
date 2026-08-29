// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
)

// These tests run amend's splicer over the REAL task corpus and skip when the
// vault is not reachable. That is not a shortcut — it is the whole point.
//
// A fixture cannot catch amend's actual failure mode, which is SPLICING INTO A
// SECTION NOBODY WROTE. Only real task bodies carry the fenced markdown samples,
// the prose that quotes the header schema at itself, and the accumulated
// weirdness of a hundred files written over months. The task file that SPECIFIED
// this feature quotes "## Decision" inside a fence as an example — a naive
// matcher would have found that one first and spliced a replacement into the
// middle of a code block.
//
// This project has been bitten by exactly this gap three times: note_path was
// empty on every session for six months with a green suite; the 191 fence bug
// passed every unit test its author wrote because all the fixtures had balanced
// fences; and the 204 header parser would have read a task's own body as
// metadata. Fixtures agree with whoever wrote them.
//
// Nothing here writes to the vault. upsertSection is a pure function, so the
// corpus is read, spliced in memory, and asserted on.
func liveTaskCorpus(t *testing.T) map[string]string {
	t.Helper()

	v, err := OpenVaultGlobal()
	if err != nil {
		t.Skipf("no vault configured on this host: %v", err)
	}
	if _, err := os.Stat(v.Root); err != nil {
		t.Skipf("configured vault is not present on this host: %v", err)
	}

	corpus := make(map[string]string)
	for _, dirFn := range []func(string) (string, error){v.TasksDir, v.TaskDoneDir, v.TaskCancelledDir} {
		dir, err := dirFn("vibe-palace")
		if err != nil {
			continue
		}
		paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
		if err != nil {
			continue
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			corpus[p] = string(data)
		}
	}
	if len(corpus) == 0 {
		t.Skip("no task files in the live vault")
	}
	return corpus
}

// headingsIn splits a body's H2 headings into those that are real section
// headings (outside a fence) and those that are merely quoted sample text.
func headingsIn(content string) (real, fencedOnly []string) {
	outside := map[int]bool{}
	for _, l := range mdfence.OutsideFences(content) {
		outside[l.Num] = true
	}

	realSet := map[string]bool{}
	fencedSet := map[string]bool{}
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !isH2Line(trimmed) {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
		if outside[i+1] {
			realSet[text] = true
		} else {
			fencedSet[text] = true
		}
	}
	for h := range realSet {
		real = append(real, h)
	}
	// A heading that appears BOTH outside and inside a fence is a real section;
	// only the ones that exist purely as sample text belong here.
	for h := range fencedSet {
		if !realSet[h] {
			fencedOnly = append(fencedOnly, h)
		}
	}
	return real, fencedOnly
}

// TestLiveVaultAmendNeverDisturbsTheHeaderBlock is the canary that matters.
//
// Amend must be incapable of changing a task's status, priority, title or edges.
// The guarantee is structural (sections start at an H2; the header block ends
// before the first one) — this asserts it against every real file rather than
// against a body someone wrote to make it pass.
func TestLiveVaultAmendNeverDisturbsTheHeaderBlock(t *testing.T) {
	corpus := liveTaskCorpus(t)

	checked := 0
	for path, content := range corpus {
		slug := strings.TrimSuffix(filepath.Base(path), ".md")
		before := parseTaskMeta(slug, content, false)

		real, _ := headingsIn(content)
		// Exercise both paths on every real file: replace each existing section,
		// and append one that cannot already exist.
		for _, section := range append(real, "Canary Section That Cannot Exist") {
			out, _ := upsertSection(content, section, "CANARY BODY")
			after := parseTaskMeta(slug, out, false)

			if after.Status != before.Status {
				t.Errorf("%s: amending %q changed Status %q -> %q", slug, section, before.Status, after.Status)
			}
			if after.Priority != before.Priority {
				t.Errorf("%s: amending %q changed Priority %q -> %q", slug, section, before.Priority, after.Priority)
			}
			if after.Title != before.Title {
				t.Errorf("%s: amending %q changed Title %q -> %q", slug, section, before.Title, after.Title)
			}
			if after.Parent != before.Parent {
				t.Errorf("%s: amending %q changed Parent %q -> %q", slug, section, before.Parent, after.Parent)
			}
			if strings.Join(after.Depends, ",") != strings.Join(before.Depends, ",") {
				t.Errorf("%s: amending %q changed Depends %v -> %v", slug, section, before.Depends, after.Depends)
			}
			checked++
		}
	}
	t.Logf("amended %d sections across %d real task files", checked, len(corpus))
}

// TestLiveVaultAmendNeverMatchesAFencedHeading is the fence canary, run against
// the only corpus that actually contains the hazard.
//
// A "## Decision" quoted inside a ```markdown fence is sample text. If
// sectionBounds ever regresses to a naive scan, it will match one of these and
// splice a replacement into the middle of a code block — corrupting the fence and
// silently orphaning whatever followed it.
func TestLiveVaultAmendNeverMatchesAFencedHeading(t *testing.T) {
	corpus := liveTaskCorpus(t)

	hazards := 0
	for path, content := range corpus {
		slug := strings.TrimSuffix(filepath.Base(path), ".md")
		_, fencedOnly := headingsIn(content)

		for _, section := range fencedOnly {
			hazards++
			if _, _, found := sectionBounds(content, section); found {
				t.Errorf("%s: sectionBounds matched %q, which exists ONLY inside a code fence — "+
					"an amend would splice into the fence", slug, section)
			}

			// And the append path must leave the fenced sample byte-identical.
			out, _ := upsertSection(content, section, "CANARY BODY")
			if !strings.Contains(out, "## "+section) {
				t.Errorf("%s: amend of fenced-only heading %q produced no section at all", slug, section)
			}
			if strings.Count(out, "CANARY BODY") != 1 {
				t.Errorf("%s: amend of %q did not append exactly one section body", slug, section)
			}
		}
	}
	if hazards == 0 {
		t.Skip("no fenced-only H2 headings in the live corpus — this canary has nothing to guard yet")
	}
	t.Logf("verified %d fenced-only headings across %d real task files were never matched", hazards, len(corpus))
}

// TestLiveVaultAmendIsIdempotentOnRealBodies: a second identical amend of a real
// file must be a no-op, not a duplication.
func TestLiveVaultAmendIsIdempotentOnRealBodies(t *testing.T) {
	corpus := liveTaskCorpus(t)

	for path, content := range corpus {
		slug := strings.TrimSuffix(filepath.Base(path), ".md")
		real, _ := headingsIn(content)

		for _, section := range append(real, "Canary Section That Cannot Exist") {
			once, _ := upsertSection(content, section, "CANARY BODY")
			twice, _ := upsertSection(once, section, "CANARY BODY")
			if once != twice {
				t.Errorf("%s: amending %q twice is not idempotent — a retry would duplicate the section", slug, section)
			}
		}
	}
}

// TestLiveVaultRetitleNeverDisturbsAnythingElse runs the retitle splicer over the
// real corpus.
//
// replaceTitleLine is a whole-file FIRST-WINS scan and is fence-UNAWARE, which is
// safe only because CreateTask always writes "# Title" as line 1 and
// validateTaskBody refuses an unfenced H1 in any body. That is an invariant about
// the CORPUS, not about the function — so it has to be checked against the corpus.
// Real task files contain fenced shell comments ("# rebuild the index") that are
// H1-shaped; if any of them ever preceded the real title, retitle would rewrite a
// line inside a code block.
func TestLiveVaultRetitleNeverDisturbsAnythingElse(t *testing.T) {
	corpus := liveTaskCorpus(t)

	for path, content := range corpus {
		slug := strings.TrimSuffix(filepath.Base(path), ".md")
		before := parseTaskMeta(slug, content, false)

		out := replaceTitleLine(content, "CANARY TITLE")
		after := parseTaskMeta(slug, out, false)

		if after.Title != "CANARY TITLE" {
			t.Errorf("%s: retitle did not take: Title = %q", slug, after.Title)
		}
		if after.Status != before.Status {
			t.Errorf("%s: retitle changed Status %q -> %q", slug, before.Status, after.Status)
		}
		if after.Priority != before.Priority {
			t.Errorf("%s: retitle changed Priority %q -> %q", slug, before.Priority, after.Priority)
		}
		if after.Parent != before.Parent {
			t.Errorf("%s: retitle changed Parent %q -> %q", slug, before.Parent, after.Parent)
		}
		if strings.Join(after.Depends, ",") != strings.Join(before.Depends, ",") {
			t.Errorf("%s: retitle changed Depends %v -> %v", slug, before.Depends, after.Depends)
		}
		// Exactly one line changed, and it was the title.
		if diff := countChangedLines(content, out); diff != 1 {
			t.Errorf("%s: retitle changed %d lines, want exactly 1 — it spliced somewhere it should not have", slug, diff)
		}
	}
}

func countChangedLines(a, b string) int {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(la) != len(lb) {
		return -1 // a line was added or removed: never correct for a retitle
	}
	n := 0
	for i := range la {
		if la[i] != lb[i] {
			n++
		}
	}
	return n
}
