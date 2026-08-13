// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
