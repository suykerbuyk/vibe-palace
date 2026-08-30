// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

func mkdirs(t *testing.T, root string, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(append([]string{root}, parts...)...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProjectTreeCoherence_FindsBothDirections: a project in one tree and not the other
// is drift, and NOTHING ELSE in the system would ever report it.
func TestProjectTreeCoherence_FindsBothDirections(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "palace", "both")
	mkdirs(t, vault.Root, "Projects", "both")
	mkdirs(t, vault.Root, "palace", "phantom")             // store, no history
	mkdirs(t, vault.Root, "Projects", "history-only", "s") // history, no store

	findings, _, err := auditProjectTreeCoherence(vault)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, f := range findings {
		got[f.Artifact] = f.Detail
	}
	if len(got) != 2 {
		t.Fatalf("findings = %+v, want exactly the two single-tree projects", got)
	}
	if !strings.Contains(got["history-only"], "UNSEARCHABLE") {
		t.Errorf("a project with history and no palace/ store is unsearchable; detail = %q",
			got["history-only"])
	}
	if _, ok := got["phantom"]; !ok {
		t.Error("a palace/ store with no history is a leftover and must be reported")
	}
	if _, wrong := got["both"]; wrong {
		t.Error("a project in BOTH trees is coherent and must not be flagged")
	}
}

// TestKGPortability_KeysOnTheDirectoryNotTheFile pins the granularity decision. The fix
// is ONE migration, so the accepted-debt unit is one entry per project — not 21,700.
func TestKGPortability_KeysOnTheDirectoryNotTheFile(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "palace", "p")
	writeFile(t, vault.Root, "palace/p/kg/triples/a/foo:bar--uses--baz.json", "{}")
	writeFile(t, vault.Root, "palace/p/kg/triples/a/second:one--uses--x.json", "{}")
	writeFile(t, vault.Root, "palace/p/kg/triples/a/clean--uses--y.json", "{}")

	findings, _, err := auditKGPortability(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want ONE per triples dir — a per-file baseline would be a "+
			"two-megabyte list recording a single migration", len(findings))
	}
	f := findings[0]
	if f.Artifact != "palace/p/kg/triples" {
		t.Fatalf("artifact = %q, want the triples directory", f.Artifact)
	}
	if !strings.Contains(f.Detail, "2 of 3") {
		t.Errorf("the count belongs in the detail (recomputed every run, so it cannot rot): %q", f.Detail)
	}
	if strings.HasPrefix(f.Artifact, "/") {
		t.Error("NEVER write an absolute vault path into a vault document — it is a fact about " +
			"the host that wrote it and false everywhere else")
	}
}

func TestKGPortability_CleanVaultIsClean(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "palace", "p")
	writeFile(t, vault.Root, "palace/p/kg/triples/a/clean--uses--y.json", "{}")

	findings, _, err := auditKGPortability(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a portable KG produced findings: %+v", findings)
	}
}

// TestResumeDiscipline_FlagsOnlyOverCap — and reads RAW bytes, which is the point:
// the resolver would have expanded the tokens before an audit could see them.
func TestResumeDiscipline_FlagsOnlyOverCap(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "fat")
	mkdirs(t, vault.Root, "Projects", "thin")
	writeFile(t, vault.Root, "Projects/fat/resume.md", strings.Repeat("x", check.ResumeMaxBytes+1))
	writeFile(t, vault.Root, "Projects/thin/resume.md", "# thin\n")

	findings, _, err := auditResumeDiscipline(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Artifact != "Projects/fat/resume.md" {
		t.Fatalf("findings = %+v, want only the over-cap resume", findings)
	}
}

// 🔴 TestIterationHeadings_IsFenceAware is the one that would have shipped a wall of
// false findings. This project's own documents quote iteration headers inside code
// fences as SAMPLE TEXT — the wrap template does it while explaining the rule. A naive
// scan counts those, and an auditor that invents findings teaches you to wave off the
// real ones.
func TestIterationHeadings_IsFenceAware(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	writeFile(t, vault.Root, "Projects/p/iterations.md", strings.Join([]string{
		"## Iteration 1 — real, and correctly H2",
		"",
		"Explaining the rule, with a counter-example quoted as sample text:",
		"",
		"```markdown",
		"### Iteration 999 — WRONG LEVEL, this is a fenced EXAMPLE, not a real heading",
		"```",
		"",
		"## Iteration 2 — also real",
		"",
	}, "\n"))

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("an H3 heading inside a CODE FENCE is sample text, not a finding: %+v", findings)
	}
}

// TestIterationHeadings_FlagsRealH3 — the regression guard 191 earned. The live vault is
// clean of these, and this dimension exists to keep it that way.
func TestIterationHeadings_FlagsRealH3(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	writeFile(t, vault.Root, "Projects/p/iterations.md",
		"## Iteration 1 — fine\n\n### Iteration 2 — WRONG: non-canonical H3 (191)\n")

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want the one real H3 heading", findings)
	}
	detail := findings[0].Detail
	if !strings.Contains(detail, "canonical level is H2") {
		t.Errorf("finding must name the canonical level: %q", detail)
	}
	if !strings.Contains(detail, "191") {
		t.Errorf("finding must cite the 191 history: %q", detail)
	}
	if strings.Contains(detail, "INVISIBLE") {
		t.Errorf("finding must not claim H3 is invisible to the counter (reader is H2+H3 tolerant): %q", detail)
	}
}

// 🔴 TestIterationHeadings_DoesNotFlagDuplicateNumbers pins a rule that was DELETED after
// its first live run, so that nobody re-adds it.
//
// The first version flagged 17 "duplicates" across 6 projects. Every one was deliberate:
// a session with several work units writes several narratives under one iteration number
// ("## Iteration 177 — addendum: ..."). The operator has done this 15 times. Flagging it
// is an auditor inventing findings — and an auditor that invents findings is worse than
// one that misses them, because it teaches you to wave off the real ones.
func TestIterationHeadings_DoesNotFlagDuplicateNumbers(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	writeFile(t, vault.Root, "Projects/p/iterations.md", strings.Join([]string{
		"## Iteration 177 — the feature shipped",
		"",
		"## Iteration 177 — addendum: commit 1 landed; the sweep staged as commit 2",
		"",
	}, "\n"))

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a repeated iteration number is a DELIBERATE pattern (multi-work-unit sessions "+
			"write addendum narratives), not a defect. Do not re-add this rule: %+v", findings)
	}
}

// iterEntry composes an entry exactly as storage.(*Vault).AppendIterationOwned
// does. Fixtures go through the WRITER so they cannot describe a frame the
// writer no longer emits.
func iterEntry(n int, title, body string) string {
	return "\n---\n" + wrapstate.FormatIterationHeader(n, title) + "\n\n" + body + "\n"
}

// dirtyIterations carries one instance of each of the three defect classes plus
// the legal shapes the dimension must stay quiet about.
func dirtyIterations() string {
	return strings.Join([]string{
		"# p — Iteration Narratives",
		"",
		"## Iteration Narratives", // a section title, not frame-adjacent
		"",
		iterEntry(5, "canonical", "prose\n\n## Phase 1\n\nan in-body sub-heading"),
		"\n---\n## 2026-06-17 Wrap\n\nframe orphan: a real boundary with no number",
		iterEntry(146, "prior", "prose\n\n## Iteration 147\n\ntitleless, and NOT frame-adjacent"),
		"\n---\n## Iteration 40 — Iteration 40 — Global AI Eric prep addendum\n\ndoubled prefix",
		iterEntry(200, "fenced", "sample text:\n\n```md\n### Iteration 999 — sample\n```"),
		"\n---\nshape: bookkeeping\nsummary: a session\n---\n## Session arc\n\nunder a YAML closer",
		iterEntry(177, "shipped", "body"),
		iterEntry(177, "addendum: the sweep staged as commit 2", "body"),
	}, "\n")
}

// TestIterationHeadings_FlagsAllThreeClasses covers the widening: the dimension
// used to test the H3 LEVEL only, which is one of three conditions and not the
// one that was doing live damage. The frame rule cannot see a malformed heading
// mid-body and the canonicity rule cannot see an unnumbered orphan, so neither of
// those subsumes the other. The doubled-prefix rule was once equally independent
// — the round-trip oracle was idempotent over that corruption — and is now kept
// because it NAMES the defect, reported in preference to the generic class.
func TestIterationHeadings_FlagsAllThreeClasses(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	writeFile(t, vault.Root, "Projects/p/iterations.md", dirtyIterations())

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %+v, want exactly the three defect classes", findings)
	}

	cases := []struct {
		heading string
		cite    string
	}{
		{"## 2026-06-17 Wrap", "TAIL of the previous entry"},
		{"## Iteration 147", "not what the writer emits"},
		{"## Iteration 40 — Iteration 40 — Global AI Eric prep addendum", "prefix TWICE"},
	}
	for i, c := range cases {
		f := findings[i]
		wantArtifact := "Projects/p/iterations.md:" + strconv.Itoa(iterLineOf(t, dirtyIterations(), c.heading))
		if f.Artifact != wantArtifact {
			t.Errorf("finding %d artifact = %q, want %q — a finding without the file and 1-indexed "+
				"line is one nobody can act on", i, f.Artifact, wantArtifact)
		}
		if !strings.Contains(f.Detail, c.heading) {
			t.Errorf("finding %d must quote the offending heading %q: %q", i, c.heading, f.Detail)
		}
		if !strings.Contains(f.Detail, c.cite) {
			t.Errorf("finding %d must explain WHY this shape is a defect (%q): %q", i, c.cite, f.Detail)
		}
	}
}

// TestIterationHeadings_MustNotFlag names, one by one, the live-vault shapes that
// are legal. Every entry here would be an INVENTED finding — the failure mode
// this dimension already lost one rule to.
func TestIterationHeadings_MustNotFlag(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	writeFile(t, vault.Root, "Projects/p/iterations.md", dirtyIterations())

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, f := range findings {
		joined += f.Detail + "\n"
	}
	for _, quiet := range []string{
		"## Iteration Narratives",    // a section title (recmeet L8)
		"## Phase 1",                 // in-body sub-heading; 14 live in vibe-palace
		"## Session arc",             // under a YAML closer; 5 live in recmeet
		"### Iteration 999 — sample", // inside a code fence
		"## Iteration 5 — canonical", // exactly what the writer emits
		"## Iteration 177 — shipped", // duplicate N is DELIBERATE...
		"## Iteration 177 — addendum: the sweep staged as commit 2", // ...and legal
	} {
		if strings.Contains(joined, quiet) {
			t.Errorf("%q was flagged; it is legal archive content, not a defect:\n%s", quiet, joined)
		}
	}
}

// TestIterationHeadings_ReportsADoubleBreakerOnce pins the dedup. "## Iteration
// 93: Live capture" sits on the writer's frame AND fails canonicity, so it
// satisfies two rules and must still produce ONE finding. Two findings for one
// line inflates every count the operator reads.
func TestIterationHeadings_ReportsADoubleBreakerOnce(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	content := "# p — Iteration Narratives\n" +
		iterEntry(92, "prior", "body") +
		"\n---\n## Iteration 93: Live capture\n\nbody-93\n"
	writeFile(t, vault.Root, "Projects/p/iterations.md", content)

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want ONE finding for a heading that breaks two rules", findings)
	}
	wantArtifact := "Projects/p/iterations.md:" + strconv.Itoa(iterLineOf(t, content, "## Iteration 93: Live capture"))
	if findings[0].Artifact != wantArtifact {
		t.Errorf("artifact = %q, want %q", findings[0].Artifact, wantArtifact)
	}
	if !strings.Contains(findings[0].Detail, "entry frame") {
		t.Errorf("the FRAME class is the worse fact about the line and must be the one reported: %q",
			findings[0].Detail)
	}
}

// TestIterationHeadings_CleanArchiveIsSilent — the state the live vault is meant
// to reach. A dimension whose value is that it stays at zero is worth having only
// if zero is actually reachable.
func TestIterationHeadings_CleanArchiveIsSilent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	writeFile(t, vault.Root, "Projects/p/iterations.md", "# p — Iteration Narratives\n"+
		iterEntry(1, "one", "body")+
		iterEntry(2, "two", "body\n\n## In-body section\n\nmore")+
		iterEntry(2, "two, addendum", "body"))

	findings, _, err := auditIterationHeadings(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a clean archive produced findings: %+v", findings)
	}
}

// iterLineOf locates a heading in the fixture independently of the scan under
// test, so an off-by-one in the reported line fails rather than agrees.
func iterLineOf(t *testing.T, content, heading string) int {
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

// TestMemoryPortability_FlagsUnportableAndCollisions: reserved chars, Windows
// device names, and case-insensitive collisions in Projects/*/memory/ are each a
// finding; a clean name is not. The portable-name test is the SAME predicate the
// write path enforces (vaultfs.ValidatePortableSegment), so the two cannot drift.
func TestMemoryPortability_FlagsUnportableAndCollisions(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeFile(t, vault.Root, "Projects/p/memory/team:notes.md", "x") // reserved char
	writeFile(t, vault.Root, "Projects/p/memory/aux.md", "x")        // device name
	writeFile(t, vault.Root, "Projects/p/memory/Foo.md", "x")        // collision pair...
	writeFile(t, vault.Root, "Projects/p/memory/foo.md", "x")        // ...second is flagged
	writeFile(t, vault.Root, "Projects/p/memory/clean-note.md", "x") // clean

	findings, _, err := auditMemoryPortability(vault)
	if err != nil {
		t.Fatal(err)
	}
	byArtifact := map[string]string{}
	for _, f := range findings {
		if f.Dimension != DimMemoryPortability {
			t.Errorf("finding on wrong dimension: %q", f.Dimension)
		}
		if strings.HasPrefix(f.Artifact, "/") {
			t.Errorf("absolute artifact path leaked: %q", f.Artifact)
		}
		byArtifact[f.Artifact] = f.Detail
	}
	if _, ok := byArtifact["Projects/p/memory/team:notes.md"]; !ok {
		t.Error("reserved-char memory filename must be flagged")
	}
	if _, ok := byArtifact["Projects/p/memory/aux.md"]; !ok {
		t.Error("device-name memory filename must be flagged")
	}
	_, flaggedLower := byArtifact["Projects/p/memory/foo.md"]
	_, flaggedUpper := byArtifact["Projects/p/memory/Foo.md"]
	if flaggedLower == flaggedUpper {
		t.Errorf("exactly one of the case-collision pair must be flagged (Foo=%v foo=%v)",
			flaggedUpper, flaggedLower)
	}
	if _, ok := byArtifact["Projects/p/memory/clean-note.md"]; ok {
		t.Error("a clean memory filename must not be flagged")
	}
}

func TestMemoryPortability_CleanVaultIsClean(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeFile(t, vault.Root, "Projects/p/memory/pref-foo.md", "x")
	writeFile(t, vault.Root, "Projects/p/memory/prefs/style.md", "x")

	findings, _, err := auditMemoryPortability(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a clean memory tree produced findings: %+v", findings)
	}
}

// --- task-heading-markers ---

// seedTask writes an active task file for the marker dimension. It also creates the
// palace/ side so ListAllProjects reports the project as present under Projects/.
func seedTask(t *testing.T, vault *storage.Vault, project, slug, body string) {
	t.Helper()
	mkdirs(t, vault.Root, "Projects", project)
	writeFile(t, vault.Root, "Projects/"+project+"/tasks/"+slug+".md", body)
}

func markerArtifacts(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Artifact)
	}
	return out
}

// TestTaskHeadingMarkers_FindsThePositiveFixture is the defect test: the shape that
// actually stranded eight headings in first-principles.md.
func TestTaskHeadingMarkers_FindsThePositiveFixture(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "stale", "# T\n\n## PHASE 1 — landed in the working tree 2026-08-20, UNCOMMITTED\n\nbody\n")

	findings, unknowns, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the UNCOMMITTED heading", markerArtifacts(findings))
	}
	if want := "Projects/p/tasks/stale.md:3"; findings[0].Artifact != want {
		t.Errorf("artifact = %q, want %q", findings[0].Artifact, want)
	}
	if !strings.Contains(findings[0].Detail, "landed <sha>") {
		t.Errorf("detail must name the proven conversion; got %q", findings[0].Detail)
	}
	// The declared list must reach the reader, or a zero cannot be interpreted.
	if !strings.Contains(findings[0].Detail, "EXTENSIBLE") {
		t.Errorf("detail must frame the marker list as extensible; got %q", findings[0].Detail)
	}
}

// TestTaskHeadingMarkers_MutationEmptyListReportsNothing is the mutation proof: the
// matcher, not the harness, is what produces the finding. Empty the declared list and
// the positive fixture must go unreported.
//
// A dimension whose test passes with the rule removed is testing its own scaffolding.
func TestTaskHeadingMarkers_MutationEmptyListReportsNothing(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "stale", "# T\n\n## PHASE 1 — landed in the working tree 2026-08-20, UNCOMMITTED\n\nbody\n")

	if findings, _, err := auditTaskHeadingMarkers(vault); err != nil || len(findings) != 1 {
		t.Fatalf("precondition: want 1 finding before the mutation, got %v (err %v)",
			markerArtifacts(findings), err)
	}

	saved := taskHeadingMarkerRE
	taskHeadingMarkerRE = buildMarkerRE(nil)
	t.Cleanup(func() { taskHeadingMarkerRE = saved })

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("with an empty marker list the dimension must report nothing; got %v",
			markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_ClassBProvenanceIsNotFlagged pins the exemption. These
// headings record WHEN, not what is now, so they cannot go stale — and the amend
// schema itself teaches "Decision (iter 205)" as the worked example of a section
// name, so a scanner that flags them would eat the convention the tool instructs
// every agent to use.
func TestTaskHeadingMarkers_ClassBProvenanceIsNotFlagged(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "# T\n\n" +
		"## Decision (iter 205)\n\nb\n\n" +
		"## Review (2026-07-20)\n\nb\n\n" +
		"## Phase 1 progress — real root (2026-08-16, landed abcdef0)\n\nb\n\n" +
		"## The finding\n\nb\n\n" +
		"## Open questions\n\nb\n\n" +
		"## Decision (2026-08-01) — options 1 and 2 are REFUTED\n\nb\n"
	seedTask(t, vault, "p", "clean", body)

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("provenance and topic headings must not be flagged; got %v", markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_ProvenancePrefixDoesNotBlessATokenAfterIt is the ruling that
// a prefix-exemption implementation would silently break. The Class B exemption is
// PREFIX-only: it identifies provenance, it does not license everything after the dash.
func TestTaskHeadingMarkers_ProvenancePrefixDoesNotBlessATokenAfterIt(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "collision", "# T\n\n## Decision (2026-08-01) — still UNCOMMITTED\n\nb\n")

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("a provenance prefix must not exempt a marker after it; got %v",
			markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_IsFenceAware: this project's task files quote H2-shaped lines
// inside code fences (which is why amend itself is fence-aware). Flagging sample text
// is inventing findings, and an auditor that invents findings is worse than one that
// misses them.
func TestTaskHeadingMarkers_IsFenceAware(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "# T\n\n```\n## PHASE 1 — UNCOMMITTED\n```\n\n## The finding\n\nb\n"
	seedTask(t, vault, "p", "fenced", body)

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a heading quoted inside a fence is sample text; got %v", markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_H2Only: the rule is scoped to the level amend is keyed on.
// An H3 sits inside a section an amend can replace wholesale, so it is not stranded.
func TestTaskHeadingMarkers_H2Only(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "levels", "# Title with UNCOMMITTED\n\n### Sub — UNCOMMITTED\n\nb\n")

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("only H2 is in scope; got %v", markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_WordBoundary: "UNBLOCKED" is not "BLOCKED". A substring match
// would report the exact opposite of what the heading says.
func TestTaskHeadingMarkers_WordBoundary(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "words", "# T\n\n## Direction B is UNBLOCKED and mostly mechanical\n\nb\n")

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("word-bounded match must not fire on 'UNBLOCKED'; got %v", markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_LiveFalsePositivesStaySilent pins the chair ruling of
// 2026-08-23. Both fixtures are VERBATIM live headings from Projects/quantum-ng that
// the first implementation reported, because it folded case over the whole
// alternation.
//
// They are two different mistakes and both must stay silent:
//
//   - "blocked" here is ADJECTIVAL — it names a section of the build, it does not
//     assert that the section is in a blocked state.
//   - "deliberately not decided here" is a SCOPE statement, not a pending one: the
//     decision belongs to a child task, so the heading is permanently true and can
//     never go stale. A pending state and a delegation read alike in prose and are
//     opposites in fact.
//
// If either of these starts firing again, the matcher has been case-folded or the
// list has been widened past what a specimen justified.
func TestTaskHeadingMarkers_LiveFalsePositivesStaySilent(t *testing.T) {
	for _, tc := range []struct{ name, heading string }{
		{"adjectival blocked", "## Inventory (live `make` blocked section + BUILDABLE RED)"},
		{"delegated decision", "## The open architectural choice — child 1 decides it, deliberately not decided here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := storage.NewVault(t.TempDir())
			seedTask(t, vault, "p", "fp", "# T\n\n"+tc.heading+"\n\nb\n")
			findings, _, err := auditTaskHeadingMarkers(vault)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 0 {
				t.Fatalf("live false positive must stay silent: %q; got %v",
					tc.heading, markerArtifacts(findings))
			}
		})
	}
}

// TestTaskHeadingMarkers_MatchingIsCaseSensitive pins the ruling directly rather than
// only through its consequences. Every token matches as DECLARED.
//
// The under-firing this accepts is real and deliberate: a lowercase "blocked on the
// operator" is not reported. That is the recorded trade — inventing findings is the
// worse failure, and the list is extensible when a specimen appears.
func TestTaskHeadingMarkers_MatchingIsCaseSensitive(t *testing.T) {
	// Build locally rather than dereferencing the package var: a nil regexp (the
	// mutation seam) must fail this test, not PANIC it. A panic aborts the whole test
	// binary, which hides every other failure the mutation was supposed to expose —
	// so the mutation proof would have reported four reds instead of sixteen and the
	// gap would have looked like coverage.
	re := buildMarkerRE(unresolvedStatusMarkers)
	if re == nil {
		t.Fatal("marker regexp is nil: the declared list is empty")
	}
	if strings.Contains(re.String(), "(?i)") {
		t.Fatal("the marker regexp must not case-fold: (?i) is what made 'blocked section' a finding")
	}
	for _, heading := range []string{
		"## Phase 1 is uncommitted",
		"## Scope todo",
		"## blocked on the operator",
	} {
		vault := storage.NewVault(t.TempDir())
		seedTask(t, vault, "p", "lower", "# T\n\n"+heading+"\n\nb\n")
		findings, _, err := auditTaskHeadingMarkers(vault)
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 0 {
			t.Errorf("tokens match as declared; %q must not fire, got %v",
				heading, markerArtifacts(findings))
		}
	}
}

// TestTaskHeadingMarkers_TerminalDirectoriesAreOutOfScope pins the scope ruling. A
// finding under done/ or cancelled/ is unrepairable by amend, vp tasks edit and
// overwrite alike — all three are active-only — so reporting it would be permanent,
// un-actionable red.
func TestTaskHeadingMarkers_TerminalDirectoriesAreOutOfScope(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	stale := "# T\n\n## THE GIT CHANNEL — UNRULED, operator's call\n\nb\n"
	writeFile(t, vault.Root, "Projects/p/tasks/done/retired.md", stale)
	writeFile(t, vault.Root, "Projects/p/tasks/cancelled/dropped.md", stale)

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("done/ and cancelled/ are out of scope; got %v", markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_CoversTheWholeCensusVocabulary: every marker the 2026-08-23
// census had to add is matched. The original four-marker set missed UNRESOLVED, which
// sits on the positive-control file — this is the regression pin for that.
func TestTaskHeadingMarkers_CoversTheWholeCensusVocabulary(t *testing.T) {
	for _, tc := range []struct{ name, heading string }{
		{"uncommitted", "## PHASE 1 (2026-08-20, UNCOMMITTED)"},
		{"unruled", "## THE GIT CHANNEL — UNRULED, operator's call"},
		{"unresolved", "## Review findings — the strategic fork is UNRESOLVED"},
		{"undecided", "## Migration flavour — UNDECIDED"},
		{"blocked", "## Iteration 5c — confirm-run BLOCKED on credits"},
		{"wip", "## Phase 2 WIP"},
		{"todo", "## Scope TODO"},
		{"Blocked on", "## Blocked on"},
		{"not yet decided", "## Open questions for the plan — not yet decided"},
		{"NOT decided", "## Candidate directions — NOT decided, settle before coding"},
		{"none of these is decided", "## Options — none of these is decided"},
		{"awaiting human commit", "## Promotion — vault to embedded, awaiting human commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := storage.NewVault(t.TempDir())
			seedTask(t, vault, "p", "v", "# T\n\n"+tc.heading+"\n\nb\n")
			findings, _, err := auditTaskHeadingMarkers(vault)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 1 {
				t.Fatalf("marker %q must be matched in %q; got %v",
					tc.name, tc.heading, markerArtifacts(findings))
			}
		})
	}
}

// TestTaskHeadingMarkers_EvidenceNamesEveryDeclaredMarker: the evidence string is
// composed from the same slice the matcher is built from, so the printed rule and the
// applied rule cannot drift. Two hand-maintained copies of one list is the defect
// class this dimension exists to report.
func TestTaskHeadingMarkers_EvidenceNamesEveryDeclaredMarker(t *testing.T) {
	for _, m := range unresolvedStatusMarkers {
		if !strings.Contains(EvidenceTaskHeadingMarkers, m) {
			t.Errorf("evidence does not name declared marker %q", m)
		}
	}
	if strings.Contains(EvidenceTaskHeadingMarkers, "REFUTED") {
		t.Error("REFUTED must not be admitted: it is the token the correction idiom is written in")
	}
}

// TestTaskHeadingMarkers_IceboxedTasksAreInScope: iceboxed tasks sit in the ACTIVE
// directory and are amendable, so they are in scope. This pins the reason the
// dimension walks the directory instead of a task listing — vp_list_tasks hides the
// icebox by default, and a census driven off it reported 25 where the directory held
// 34.
func TestTaskHeadingMarkers_IceboxedTasksAreInScope(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "parked",
		"# T\n\n**Status:** icebox\n\n## Candidate directions — NOT decided, settle before coding\n\nb\n")

	findings, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("an iceboxed task is active and amendable; got %v", markerArtifacts(findings))
	}
}

// TestTaskHeadingMarkers_CleanProjectIsSilent: the dimension must be silent when
// healthy, and must not fall over on a project with no tasks directory at all.
func TestTaskHeadingMarkers_CleanProjectIsSilent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "notasks")
	seedTask(t, vault, "p", "ok", "# T\n\n## The finding\n\nb\n\n## The fix\n\nb\n")

	findings, unknowns, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unknowns) != 0 {
		t.Fatalf("clean vault must be silent; findings %v unknowns %v",
			markerArtifacts(findings), unknowns)
	}
}

// TestTaskHeadingMarkers_IsRegistered: a dimension nobody runs is the disease this
// vault is named after. Run() must actually carry it.
func TestTaskHeadingMarkers_IsRegistered(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "stale", "# T\n\n## PHASE 1 — UNCOMMITTED\n\nb\n")

	rep, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.Dimensions {
		if d.Name != DimTaskHeadingMarkers {
			continue
		}
		if len(d.New) != 1 {
			t.Fatalf("registered dimension reported New = %+v, want the one stale heading", d.New)
		}
		if d.Evidence != EvidenceTaskHeadingMarkers {
			t.Error("registered dimension must carry its composed evidence string")
		}
		return
	}
	t.Fatalf("%s is not registered in Run's dims table", DimTaskHeadingMarkers)
}

// --- palace-store-drawers ---

// seedPalaceProject creates BOTH trees for a project, which is the shape the defect
// lives in: present in palace/ and in Projects/, so ProjectPresence.Complete() is true
// and project-tree-coherence stays silent about it.
func seedPalaceProject(t *testing.T, vault *storage.Vault, project string) {
	t.Helper()
	mkdirs(t, vault.Root, "palace", project)
	mkdirs(t, vault.Root, "Projects", project)
}

// seedDrawer appends one real Drawer RECORD to a room's drawers.jsonl. It writes the
// record rather than an empty file on purpose — the dimension counts records, and a
// helper that only made files could not tell the two apart.
func seedDrawer(t *testing.T, vault *storage.Vault, project, wing, room, content string) {
	t.Helper()
	d := storage.Drawer{
		ID:         storage.DrawerID(wing, content),
		Hall:       "sessions",
		Content:    content,
		SourceType: "session",
		FiledAt:    "2026-08-30T00:00:00Z",
	}
	line, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, vault.Root, "palace/"+project+"/drawers/"+wing+"/"+room+"/drawers.jsonl", string(line)+"\n")
}

func drawerArtifacts(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Artifact)
	}
	return out
}

// TestPalaceStoreDrawers_AbsentDrawersDirIsFound is the day-one specimen: the live
// vault's atlassian-vault has palace/<slug>/kg and no drawers directory at all, sits
// in BOTH trees, and is therefore invisible to project-tree-coherence.
func TestPalaceStoreDrawers_AbsentDrawersDirIsFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "hollow")
	writeFile(t, vault.Root, "palace/hollow/kg/entities.jsonl", "")

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the store with no drawers directory",
			drawerArtifacts(findings))
	}
	if findings[0].Artifact != "hollow" {
		t.Errorf("artifact = %q, want the project slug (one finding per project, matching "+
			"project-tree-coherence's granularity)", findings[0].Artifact)
	}
	if !strings.Contains(findings[0].Detail, "is absent") {
		t.Errorf("the ABSENT detail must say the drawers directory is absent, or a reader "+
			"cannot tell it from the present-but-empty case; got %q", findings[0].Detail)
	}
	// The wording ruling: iterations.md is an independent ingest source for
	// search.Rebuild, so an empty drawer store does NOT prove the project unsearchable.
	if strings.Contains(findings[0].Detail, "UNSEARCHABLE") {
		t.Errorf("this dimension may not claim UNSEARCHABLE — Projects/<slug>/iterations.md "+
			"is a separate corpus; got %q", findings[0].Detail)
	}
}

// TestPalaceStoreDrawers_PresentButEmptyIsFound pins the OTHER half of the
// discriminator. ListWings returns (nil, nil) for both "absent" and "present and
// empty", so without HasPalaceStore these two findings would be indistinguishable.
func TestPalaceStoreDrawers_PresentButEmptyIsFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "empty")
	mkdirs(t, vault.Root, "palace", "empty", "drawers")
	// A wing and a room that exist as directories, plus a zero-length drawers.jsonl.
	// Files are not records: this store still holds nothing to search.
	writeFile(t, vault.Root, "palace/empty/drawers/architecture/decisions/drawers.jsonl", "")

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 || findings[0].Artifact != "empty" {
		t.Fatalf("findings = %v, want exactly the empty store", drawerArtifacts(findings))
	}
	if !strings.Contains(findings[0].Detail, "PRESENT BUT EMPTY") {
		t.Errorf("the EMPTY detail must distinguish itself from the absent case; got %q",
			findings[0].Detail)
	}
	if strings.Contains(findings[0].Detail, "is absent") {
		t.Errorf("the empty case must not report the directory as absent; got %q",
			findings[0].Detail)
	}
	if strings.Contains(findings[0].Detail, "UNSEARCHABLE") {
		t.Errorf("this dimension may not claim UNSEARCHABLE; got %q", findings[0].Detail)
	}
}

// TestPalaceStoreDrawers_PopulatedStoreIsSilent: one real drawer record is enough.
func TestPalaceStoreDrawers_PopulatedStoreIsSilent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "full")
	seedDrawer(t, vault, "full", "architecture", "decisions", "a real drawer")

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unknowns) != 0 {
		t.Fatalf("a populated store must be silent; findings %v unknowns %v",
			drawerArtifacts(findings), unknowns)
	}
}

// TestPalaceStoreDrawers_NoPalaceTreeIsNotOurs: a project with history and no palace/
// store is project-tree-coherence's finding. Reporting it here too would be a
// duplicate rather than the inverse.
func TestPalaceStoreDrawers_NoPalaceTreeIsNotOurs(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "history-only")
	writeFile(t, vault.Root, "Projects/history-only/iterations.md", "## Iteration 1 — x\n")

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unknowns) != 0 {
		t.Fatalf("!InPalace is project-tree-coherence's finding, not ours; findings %v unknowns %v",
			drawerArtifacts(findings), unknowns)
	}
	// And the OTHER dimension must still own it, or the hole moved rather than closed.
	coherence, _, err := auditProjectTreeCoherence(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(coherence) != 1 {
		t.Fatalf("project-tree-coherence must still report the palace-less project; got %+v", coherence)
	}
}

// 🔴 TestPalaceStoreDrawers_PalaceOnlyProjectDetailTellsTheTruth pins the OTHER shape
// the p.InPalace gate admits: a palace store with no Projects/ tree at all.
//
// The detail must not claim project-tree-coherence is blind here — coherence reports
// this project too, because it is not Complete() — and must not offer iterations.md as
// the corpus that might still cover it, because there is no Projects/<slug>/ tree to
// hold one. Both sentences are true for a two-tree project and false for this one, so
// a single fixed detail string cannot serve both. Assert the words, not just the
// count: a finding that fires with a false explanation is the defect class this
// dimension exists to catch, one layer down.
func TestPalaceStoreDrawers_PalaceOnlyProjectDetailTellsTheTruth(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "palace", "leftover")

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 || findings[0].Artifact != "leftover" {
		t.Fatalf("a palace-only project still has an empty drawer store; findings = %v",
			drawerArtifacts(findings))
	}

	detail := findings[0].Detail
	if strings.Contains(detail, "cannot see this") || strings.Contains(detail, "both trees") {
		t.Errorf("project-tree-coherence is NOT blind to a palace-only project — it reports it "+
			"for being incomplete. Detail must not claim otherwise: %q", detail)
	}
	if strings.Contains(detail, "may still be indexed") {
		t.Errorf("there is no Projects/leftover/iterations.md to index, so the detail must not "+
			"offer it as a corpus that might still cover the project: %q", detail)
	}
	if strings.Contains(detail, "UNSEARCHABLE") {
		t.Errorf("this dimension never claims unsearchability in those terms: %q", detail)
	}

	// The premise the branch rests on: coherence really does own this project too.
	coherence, _, err := auditProjectTreeCoherence(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(coherence) != 1 || coherence[0].Artifact != "leftover" {
		t.Fatalf("precondition: project-tree-coherence must report the palace-only project, "+
			"or this test is asserting against a premise that does not hold; got %+v", coherence)
	}
}

// 🔴 TestPalaceStoreDrawers_IterationsCorpusDoesNotSilenceIt is the Critical the plan
// review raised, pinned. Projects/<slug>/iterations.md is a SEPARATE ingest source for
// search.Rebuild (internal/search/engine.go:467-486) — it is NOT the drawer store, it
// does not fill one, and a populated one must not suppress this finding. The live
// specimen atlassian-vault has a 26KB iterations.md and no drawers directory at all.
func TestPalaceStoreDrawers_IterationsCorpusDoesNotSilenceIt(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "atlassian-vault")
	writeFile(t, vault.Root, "Projects/atlassian-vault/iterations.md",
		strings.Repeat("## Iteration 1 — a real, indexed narrative\n\nbody\n\n", 200))

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 || findings[0].Artifact != "atlassian-vault" {
		t.Fatalf("a populated iterations.md is a DIFFERENT corpus and must not silence an empty "+
			"drawer store; findings = %v", drawerArtifacts(findings))
	}
}

// TestPalaceStoreDrawers_UnreadableStoreIsUnknownNotPass: unknown is not a shade of
// pass (audit.go:21-25). A store the auditor cannot walk is neither reported as a
// finding nor counted as clean.
func TestPalaceStoreDrawers_UnreadableStoreIsUnknownNotPass(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes, so chmod 0 cannot make the walk undecidable")
	}
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "blind")
	seedDrawer(t, vault, "blind", "architecture", "decisions", "a real drawer")

	dir := filepath.Join(vault.Root, "palace", "blind", "drawers")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	// Restore before TempDir cleanup, which cannot remove an unreadable directory.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a store the auditor could not read must not produce a finding; got %v",
			drawerArtifacts(findings))
	}
	if len(unknowns) != 1 || !strings.Contains(unknowns[0], "blind") {
		t.Fatalf("unknowns = %v, want the project the auditor could not look at", unknowns)
	}
}

// TestPalaceStoreDrawers_MutationEmptyingTheStoreProducesTheFinding is the mutation
// proof: the RULE produces the finding, not the harness. Seed a store WITH drawers and
// assert silence; empty the drawer set and assert exactly one finding.
func TestPalaceStoreDrawers_MutationEmptyingTheStoreProducesTheFinding(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "p")
	seedDrawer(t, vault, "p", "architecture", "decisions", "a real drawer")

	findings, _, err := auditPalaceStoreDrawers(vault)
	if err != nil || len(findings) != 0 {
		t.Fatalf("precondition: a populated store must be silent; got %v (err %v)",
			drawerArtifacts(findings), err)
	}

	// Truncate the record set, leaving the file and the whole directory tree in place.
	// Files are not records — this is the exact mutation the dimension must catch.
	writeFile(t, vault.Root, "palace/p/drawers/architecture/decisions/drawers.jsonl", "")

	findings, unknowns, err := auditPalaceStoreDrawers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 || findings[0].Artifact != "p" {
		t.Fatalf("emptying the drawer set must produce exactly one finding; got %v",
			drawerArtifacts(findings))
	}
	if !strings.Contains(findings[0].Detail, "PRESENT BUT EMPTY") {
		t.Errorf("detail = %q, want the present-but-empty wording", findings[0].Detail)
	}
}

// TestPalaceStoreDrawers_IsRegistered: dims is a hand-edited literal, so a dimension
// can be written, unit-tested, and silently never run. Run() must actually carry it.
func TestPalaceStoreDrawers_IsRegistered(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedPalaceProject(t, vault, "hollow")

	rep, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.Dimensions {
		if d.Name != DimPalaceStoreDrawers {
			continue
		}
		if len(d.New) != 1 {
			t.Fatalf("registered dimension reported New = %+v, want the one empty store", d.New)
		}
		if d.Evidence != EvidencePalaceStoreDrawers {
			t.Error("registered dimension must carry its evidence string")
		}
		return
	}
	t.Fatalf("%s is not registered in Run's dims table", DimPalaceStoreDrawers)
}

// --- task-preamble ---

// preambleArtifacts renders a finding set as its artifacts, so a failure message names
// the files rather than dumping every detail paragraph.
func preambleArtifacts(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Artifact)
	}
	return out
}

// TestTaskPreamble_CreateTaskShapeIsNotFlagged is the POSITIVE CONTROL, and it is what
// ties this dimension to a guarantee rather than to a taste.
//
// It does not hand-write the "good" shape — it calls the real CreateTask, which emits
// the conventional first heading unconditionally. If that writer ever starts leaving
// text above the first H2, this test fails and the dimension's premise fails with it.
func TestTaskPreamble_CreateTaskShapeIsNotFlagged(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mkdirs(t, vault.Root, "Projects", "p")
	if err := vault.CreateTask("p", storage.TaskSpec{
		Slug:     "born-clean",
		Title:    "Born clean",
		Content:  "The finding, stated once.",
		Priority: "medium",
	}); err != nil {
		t.Fatal(err)
	}

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unknowns) != 0 {
		t.Fatalf("a task written by CreateTask has an empty preamble and must not be flagged; "+
			"findings %v unknowns %v", preambleArtifacts(findings), unknowns)
	}
}

// TestTaskPreamble_ProseAboveTheFirstH2IsFlagged is the ANTI-VACUITY test: the
// dimension must actually fire on the shape it exists for, exactly once, on the right
// artifact.
func TestTaskPreamble_ProseAboveTheFirstH2IsFlagged(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "framed",
		"# T\n\n**Status:** pending\n**Priority:** medium\n\n"+
			"Filed 2026-07-30 from the host-parity Option C decision. The anchor set already "+
			"exists — reuse it, do not re-derive it.\n\n## Context\n\nbody\n")

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the one task with a preamble",
			preambleArtifacts(findings))
	}
	if findings[0].Artifact != "Projects/p/tasks/framed.md" {
		t.Errorf("artifact = %q, want the vault-relative task path — it is the baseline's key "+
			"and must be stable", findings[0].Artifact)
	}
	// The detail must be recognisable without opening the file, and must not read as
	// the degenerate no-H2 class.
	if !strings.Contains(findings[0].Detail, "Filed 2026-07-30") {
		t.Errorf("detail must excerpt the preamble's first non-blank line; got %q",
			findings[0].Detail)
	}
	if strings.Contains(findings[0].Detail, "No usable first H2") {
		t.Errorf("a measured preamble must not be reported as the degenerate class; got %q",
			findings[0].Detail)
	}
}

// TestTaskPreamble_NoUnfencedH2IsItsOwnClass pins PATH 1 of PreambleSkippedNoH2: no
// "## " outside a fence anywhere in the file. The region definition degenerates to the
// whole body, so the detail may NOT report a measured preamble.
func TestTaskPreamble_NoUnfencedH2IsItsOwnClass(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "headless",
		"# T\n\n**Status:** pending\n\nA whole body of prose with no heading anywhere in it.\n")

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want the one headless task", preambleArtifacts(findings))
	}
	d := findings[0].Detail
	if !strings.Contains(d, "No usable first H2") || !strings.Contains(d, "ENTIRE task body") {
		t.Errorf("the no-H2 detail must say the region is degenerate — the whole body — rather "+
			"than report a measured preamble; got %q", d)
	}
	if !strings.Contains(d, "anywhere in the file") {
		t.Errorf("path 1 must say there is no unfenced heading anywhere in the file; got %q", d)
	}
	if strings.Contains(d, "A non-empty preamble sits") {
		t.Errorf("the degenerate class must never render as the ordinary non-empty case; got %q", d)
	}
}

// 🔴 TestTaskPreamble_H2AboveTheHeaderBlockIsAlsoDegenerate pins PATH 2 of
// PreambleSkippedNoH2 (preamble_migrate.go:99-102): an unfenced "## " that sits ABOVE
// the end of the header block. The migrator returns the SAME outcome, but the file
// plainly HAS a heading — so a detail saying "no ## heading anywhere" would be false
// about its own artifact, which is the defect class this dimension exists to close.
//
// It asserts the DETAIL TEXT, not the outcome, because the outcome cannot tell the two
// paths apart and the report is what a human reads.
func TestTaskPreamble_H2AboveTheHeaderBlockIsAlsoDegenerate(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// headerBlock anchors on the FIRST H1 and then consumes the contiguous
	// "**Field:**" run below it, so an H2 standing above the title lands above hdrEnd
	// — which is the only way firstH2 < hdrEnd can hold, since a "## " line is not a
	// header field and would otherwise terminate the run rather than sit inside it.
	seedTask(t, vault, "p", "heading-above-title",
		"## Stray\n\n# T\n\n**Status:** pending\n**Priority:** medium\n\nbody\n")

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want the one degenerate task", preambleArtifacts(findings))
	}
	d := findings[0].Detail
	if !strings.Contains(d, "No usable first H2") {
		t.Errorf("path 2 is the degenerate class and must say so; got %q", d)
	}
	if strings.Contains(d, "anywhere in the file") {
		t.Errorf("this file HAS an unfenced \"## \" — claiming otherwise is a finding asserting "+
			"something untrue about its own artifact; got %q", d)
	}
	if !strings.Contains(d, "at line 1") || !strings.Contains(d, "ABOVE the end of the header block") {
		t.Errorf("path 2 must locate the heading it found and say why it is unusable; got %q", d)
	}
}

// TestTaskPreamble_MutationMovingThePreambleDownClearsTheFinding is the MUTATION PROOF:
// the RULE tracks the actual region, not the harness. Seed a preamble, assert exactly
// one finding; rewrite the SAME file with the same prose moved down under "## Context",
// and assert silence. Nothing about the vault, the project or the filename changes.
func TestTaskPreamble_MutationMovingThePreambleDownClearsTheFinding(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	const prose = "Do not absorb this into the sibling slug; it is a standing directive."
	seedTask(t, vault, "p", "same-file",
		"# T\n\n**Status:** pending\n\n"+prose+"\n\n## Context\n\nbody\n")

	findings, _, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("precondition: a preamble must produce exactly one finding; got %v",
			preambleArtifacts(findings))
	}

	// Same file, same prose, moved under the conventional heading.
	writeFile(t, vault.Root, "Projects/p/tasks/same-file.md",
		"# T\n\n**Status:** pending\n\n## Context\n\n"+prose+"\n\nbody\n")

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unknowns) != 0 {
		t.Fatalf("moving the SAME prose under a heading must clear the finding; findings %v "+
			"unknowns %v", preambleArtifacts(findings), unknowns)
	}
}

// TestTaskPreamble_DoneAndCancelledAreOutOfScope pins the SCOPE RULING, not a
// convenience. OverwriteTaskFile — the only writer that can repair a preamble — is
// active-only, so a finding under tasks/done/ or tasks/cancelled/ would be permanent,
// un-actionable red. Subdirectories are never descended.
func TestTaskPreamble_DoneAndCancelledAreOutOfScope(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fat := "# T\n\n**Status:** done\n\nA very fat preamble that nothing can ever repair.\n\n" +
		"## Context\n\nbody\n"
	writeFile(t, vault.Root, "Projects/p/tasks/done/finished.md", fat)
	writeFile(t, vault.Root, "Projects/p/tasks/cancelled/dropped.md", fat)

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unknowns) != 0 {
		t.Fatalf("archived tasks are unrepairable and deliberately out of scope; findings %v "+
			"unknowns %v", preambleArtifacts(findings), unknowns)
	}
}

// TestTaskPreamble_FencedH2IsTheDegenerateClass: fence-awareness is not implemented
// here — it comes from the predicate, which scans mdfence.OutsideFences. A "## " that
// appears ONLY inside a code fence never sets firstH2, so the file falls into the no-H2
// class rather than being treated as having a heading. This asserts the behaviour
// MovePreambleUnderContext actually has.
func TestTaskPreamble_FencedH2IsTheDegenerateClass(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "quoted",
		"# T\n\n**Status:** pending\n\nProse, then a quoted markdown sample:\n\n"+
			"```markdown\n## Context\n\nnot a real heading\n```\n\nmore prose\n")

	// The predicate's own verdict, asserted directly rather than assumed.
	body, err := os.ReadFile(filepath.Join(vault.Root, "Projects", "p", "tasks", "quoted.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome := storage.MovePreambleUnderContext(string(body)); outcome != storage.PreambleSkippedNoH2 {
		t.Fatalf("MovePreambleUnderContext outcome = %v, want PreambleSkippedNoH2 — a fenced "+
			"\"## \" is sample text, not a heading", outcome)
	}

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want the one file whose only \"## \" is fenced",
			preambleArtifacts(findings))
	}
	d := findings[0].Detail
	if !strings.Contains(d, "No usable first H2") || !strings.Contains(d, "anywhere in the file") {
		t.Errorf("a fenced-only \"## \" is path 1 of the degenerate class; got %q", d)
	}
}

// TestTaskPreamble_UnreadableTasksDirIsUnknownNotPass: unknown is not a shade of pass
// (audit.go:21-25). A tasks directory the auditor cannot walk produces neither a
// finding nor a clean bill.
func TestTaskPreamble_UnreadableTasksDirIsUnknownNotPass(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes, so chmod 0 cannot make the walk undecidable")
	}
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "framed", "# T\n\nprose above the heading\n\n## Context\n\nbody\n")

	dir := filepath.Join(vault.Root, "Projects", "p", "tasks")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	// Restore before TempDir cleanup, which cannot remove an unreadable directory.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a directory the auditor could not read must not produce a finding; got %v",
			preambleArtifacts(findings))
	}
	if len(unknowns) != 1 || !strings.Contains(unknowns[0], "p") {
		t.Fatalf("unknowns = %v, want the project the auditor could not look at", unknowns)
	}
}

// TestTaskPreamble_UnreadableTaskFileIsUnknownNotPass: same ruling one level down — an
// individual file the auditor cannot open is UNKNOWN, never a silent pass.
func TestTaskPreamble_UnreadableTaskFileIsUnknownNotPass(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes, so chmod 0 cannot make the read fail")
	}
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "opaque", "# T\n\nprose above the heading\n\n## Context\n\nbody\n")

	file := filepath.Join(vault.Root, "Projects", "p", "tasks", "opaque.md")
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	findings, unknowns, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a file the auditor could not read must not produce a finding; got %v",
			preambleArtifacts(findings))
	}
	if len(unknowns) != 1 || !strings.Contains(unknowns[0], "Projects/p/tasks/opaque.md") {
		t.Fatalf("unknowns = %v, want the unreadable task file", unknowns)
	}
}

// TestTaskPreamble_IsRegistered: a dimension nobody runs is the disease this vault is
// named after, and `dims` is a HAND-EDITED literal — without this the whole dimension
// could be written, unit-tested and silently never executed.
func TestTaskPreamble_IsRegistered(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "framed",
		"# T\n\n**Status:** pending\n\nprose above the first heading\n\n## Context\n\nbody\n")

	rep, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.Dimensions {
		if d.Name != DimTaskPreamble {
			continue
		}
		if len(d.New) != 1 {
			t.Fatalf("registered dimension reported New = %+v, want the one preamble", d.New)
		}
		if d.Evidence != EvidenceTaskPreamble {
			t.Error("registered dimension must carry its own evidence string")
		}
		return
	}
	t.Fatalf("%s is not registered in Run's dims table", DimTaskPreamble)
}

// TestTaskPreamble_IsDisjointFromTaskHeadingMarkers pins the non-duplication claim in
// DimTaskPreamble's doc as a property rather than a paragraph: the two dimensions read
// disjoint regions, so one file can hold both defects and each is reported once, by the
// dimension that owns it.
func TestTaskPreamble_IsDisjointFromTaskHeadingMarkers(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "both",
		"# T\n\n**Status:** pending\n\npreamble prose\n\n## PHASE 1 — UNCOMMITTED\n\nbody\n")

	pre, _, err := auditTaskPreamble(vault)
	if err != nil {
		t.Fatal(err)
	}
	mark, _, err := auditTaskHeadingMarkers(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 1 {
		t.Fatalf("task-preamble must report the region above the first H2; got %v",
			preambleArtifacts(pre))
	}
	if len(mark) != 1 {
		t.Fatalf("task-heading-markers must still report the heading; got %v",
			markerArtifacts(mark))
	}
	if !strings.Contains(pre[0].Detail, "preamble prose") {
		t.Errorf("task-preamble's detail must describe the preamble, not the heading; got %q",
			pre[0].Detail)
	}
	if strings.Contains(pre[0].Detail, "UNCOMMITTED") {
		t.Errorf("task-preamble may not see heading text — the two regions are disjoint by "+
			"construction; got %q", pre[0].Detail)
	}
}

// TestTaskPreambleText_RecoversExactlyWhatTheMigratorWrote pins the ONE inference in
// this dimension: taskPreambleText reads the region back out of the migrator's own
// before/after pair instead of re-deriving storage's header-block rule locally. If that
// inference is wrong, every detail's size and excerpt describe a region nobody moved.
//
// The assertion is byte-exact and independent of the detail wording: whatever is
// recovered must be precisely the text `after` carries under the conventional heading.
// It runs over every preamble SHAPE the migrator distinguishes — a new heading inserted,
// an existing one prepended into, no header fields at all, and a second H1 heading the
// region (the shape five live recmeet tasks actually have).
func TestTaskPreambleText_RecoversExactlyWhatTheMigratorWrote(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"new context heading inserted",
			"# T\n\n**Status:** pending\n**Priority:** medium\n\nframing prose\n\n## The finding\n\nbody\n"},
		{"prepended into an existing context heading",
			"# T\n\n**Status:** pending\n\nframing prose\n\n## Context\n\nalready here\n"},
		{"no header fields at all",
			"# T\n\nframing prose\n\n## The finding\n\nbody\n"},
		{"region opens with a second H1",
			"# Task: t\n\n**Status:** pending\n\n# Imported title\n\nframing prose\n\n## The finding\n\nbody\n"},
		{"multi-paragraph region",
			"# T\n\n**Status:** pending\n\npara one\n\npara two\n\n- a bullet\n\n## The finding\n\nbody\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after, outcome := storage.MovePreambleUnderContext(tc.body)
			if outcome != storage.PreambleMovedIntoNewContext &&
				outcome != storage.PreambleMovedIntoExistingContext {
				t.Fatalf("fixture precondition: outcome = %v, want a MOVE", outcome)
			}
			got := taskPreambleText(tc.body, after)
			if got == "" {
				t.Fatal("recovered nothing from a file the migrator moved text out of")
			}
			want := "## " + storage.ConventionalFirstHeading + "\n\n" + got
			if !strings.Contains(after, want) {
				t.Errorf("recovered region is not what the migrator wrote under the heading.\n"+
					"recovered: %q\nafter:     %q", got, after)
			}
		})
	}
}
