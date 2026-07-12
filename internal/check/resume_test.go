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
)

// historyTable renders a `## Project History` section with n data rows.
func historyTable(n int) string {
	var b strings.Builder
	b.WriteString("## Project History\n\n| # | Summary |\n|---|---------|\n")
	for i := n; i > 0; i-- {
		fmt.Fprintf(&b, "| %d | did a thing |\n", i)
	}
	return b.String()
}

// completedTable renders a `## Completed Plans` section with n data rows.
func completedTable(n int) string {
	var b strings.Builder
	b.WriteString("## Completed Plans\n\n| Task | Iteration | File |\n|------|-----------|------|\n")
	for i := range n {
		fmt.Fprintf(&b, "| task-%d | %d | `tasks/done/task-%d.md` |\n", i, i, i)
	}
	return b.String()
}

// writeResume creates <vault>/Projects/<slug>/resume.md with the given body.
func writeResume(t *testing.T, vaultRoot, slug, body string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}
}

func TestCountSectionTableRows(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		section string
		want    int
	}{
		{
			name:    "missing section",
			body:    "# Resume\n\n## Current State\n\n- nothing\n",
			section: resumeHistorySection,
			want:    0,
		},
		{
			name:    "section with no table",
			body:    "## Project History\n\nSee `iterations.md`.\n\n## Next\n",
			section: resumeHistorySection,
			want:    0,
		},
		{
			name:    "empty table (header + delimiter only)",
			body:    "## Project History\n\n| # | Summary |\n|---|---------|\n\n## Next\n",
			section: resumeHistorySection,
			want:    0,
		},
		{
			name:    "header only, no delimiter — not a GFM table",
			body:    "## Project History\n\n| # | Summary |\n\n## Next\n",
			section: resumeHistorySection,
			want:    0,
		},
		{
			name:    "three data rows",
			body:    historyTable(3) + "\n## Next\n",
			section: resumeHistorySection,
			want:    3,
		},
		{
			name:    "trailing prose after the table does not count",
			body:    historyTable(2) + "_Older rows are archived in `iterations.md`._\n\n## Next\n",
			section: resumeHistorySection,
			want:    2,
		},
		{
			name: "### sub-heading does not close the section",
			body: "## Project History\n\n### Recent\n\n| # | S |\n|---|---|\n| 1 | a |\n| 2 | b |\n\n## Next\n",

			section: resumeHistorySection,
			want:    2,
		},
		{
			name: "pipe lines inside a fenced code block are ignored",
			body: "## Project History\n\n```\n| not | a | table |\n|-----|---|-------|\n| x | y | z |\n```\n\n| # | S |\n|---|---|\n| 1 | a |\n",

			section: resumeHistorySection,
			want:    1,
		},
		{
			name:    "escaped pipes and code spans inside cells stay one row",
			body:    "## Project History\n\n| # | S |\n|---|---|\n| 1 | a \\| b and `x \\| y` |\n",
			section: resumeHistorySection,
			want:    1,
		},
		{
			name:    "two tables in one section sum",
			body:    "## Project History\n\n| a |\n|---|\n| 1 |\n\ntext\n\n| b |\n|---|\n| 2 |\n| 3 |\n",
			section: resumeHistorySection,
			want:    3,
		},
		{
			name:    "completed plans section counted independently",
			body:    historyTable(20) + "\n" + completedTable(4),
			section: resumeCompletedSection,
			want:    4,
		},
		{
			name:    "table runs to EOF",
			body:    historyTable(5),
			section: resumeHistorySection,
			want:    5,
		},
		{
			name:    "CRLF line endings",
			body:    "## Project History\r\n\r\n| # | S |\r\n|---|---|\r\n| 1 | a |\r\n| 2 | b |\r\n",
			section: resumeHistorySection,
			want:    2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countSectionTableRows([]byte(tc.body), tc.section); got != tc.want {
				t.Errorf("countSectionTableRows(%s) = %d, want %d", tc.section, got, tc.want)
			}
		})
	}
}

func TestIsTableDelimiter(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"|---|---|", true},
		{"|:---|---:|", true},
		{"| --- | --- |", true},
		{"|   |   |", false}, // no dash: an empty data row, not a delimiter
		{"| 1 | a |", false},
		{"| - a | b |", false},
	}
	for _, tc := range tests {
		if got := isTableDelimiter(tc.line); got != tc.want {
			t.Errorf("isTableDelimiter(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestResumeBreaches(t *testing.T) {
	t.Run("under every cap is silent", func(t *testing.T) {
		body := historyTable(ResumeMaxHistoryRows) + "\n" + completedTable(ResumeMaxCompletedRows)
		if got := resumeBreaches([]byte(body)); len(got) != 0 {
			t.Errorf("at-cap resume should be silent, got %v", got)
		}
	})

	t.Run("size cap alone", func(t *testing.T) {
		body := "## Current State\n\n" + strings.Repeat("x", ResumeMaxBytes+1)
		got := resumeBreaches([]byte(body))
		if len(got) != 1 || !strings.Contains(got[0], "cap 25 KB") {
			t.Fatalf("want a single size breach, got %v", got)
		}
	})

	t.Run("history row cap alone", func(t *testing.T) {
		body := historyTable(ResumeMaxHistoryRows+1) + "\n" + completedTable(ResumeMaxCompletedRows)
		got := resumeBreaches([]byte(body))
		if len(got) != 1 || !strings.Contains(got[0], "Project History 16 rows (cap 15)") {
			t.Fatalf("want a single Project History breach, got %v", got)
		}
	})

	t.Run("completed row cap alone", func(t *testing.T) {
		body := historyTable(ResumeMaxHistoryRows) + "\n" + completedTable(ResumeMaxCompletedRows+1)
		got := resumeBreaches([]byte(body))
		if len(got) != 1 || !strings.Contains(got[0], "Completed Plans 13 rows (cap 12)") {
			t.Fatalf("want a single Completed Plans breach, got %v", got)
		}
	})

	t.Run("over all three caps at once", func(t *testing.T) {
		body := historyTable(30) + "\n" + completedTable(20) +
			"\n## Notes\n\n" + strings.Repeat("y", ResumeMaxBytes)
		got := resumeBreaches([]byte(body))
		if len(got) != 3 {
			t.Fatalf("want 3 breaches, got %d: %v", len(got), got)
		}
		joined := strings.Join(got, "; ")
		for _, want := range []string{"cap 25 KB", "Project History 30 rows", "Completed Plans 20 rows"} {
			if !strings.Contains(joined, want) {
				t.Errorf("breaches %q missing %q", joined, want)
			}
		}
	})

	t.Run("empty file is silent", func(t *testing.T) {
		if got := resumeBreaches(nil); len(got) != 0 {
			t.Errorf("empty resume should be silent, got %v", got)
		}
	})
}

func TestHumanKB(t *testing.T) {
	if got := humanKB(25 * 1024); got != "25.0 KB" {
		t.Errorf("humanKB(25KiB) = %q, want %q", got, "25.0 KB")
	}
	if got := humanKB(1536); got != "1.5 KB" {
		t.Errorf("humanKB(1536) = %q, want %q", got, "1.5 KB")
	}
}

func TestCheckResumeCaps_NoProjectsDir(t *testing.T) {
	r := CheckResumeCaps(storage.NewVault(t.TempDir()))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "no Projects/ directory" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckResumeCaps_MissingResumeIsNotAViolation(t *testing.T) {
	vault := t.TempDir()
	// A project directory with a scaffold but no resume.md at all.
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass (missing resume.md is not a violation)", r.Status)
	}
	if r.Summary != "0 resume.md within caps" {
		t.Errorf("summary = %q, want the project to be skipped, not scanned", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("details = %v, want none", r.Details)
	}
}

func TestCheckResumeCaps_MissingSectionsAreNotAViolation(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "lean", "# lean\n\n## Current State\n\n- fine\n")
	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v (%s), want Pass", r.Status, r.Summary)
	}
	if r.Summary != "1 resume.md within caps" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckResumeCaps_UnderCapIsSilent(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "tidy",
		historyTable(ResumeMaxHistoryRows)+"\n"+completedTable(ResumeMaxCompletedRows))
	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v (%s, %v), want Pass at exactly the caps", r.Status, r.Summary, r.Details)
	}
}

func TestCheckResumeCaps_ReportsEachCapIndependently(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "big", "## Notes\n\n"+strings.Repeat("x", ResumeMaxBytes+1))
	writeResume(t, vault, "histy", historyTable(ResumeMaxHistoryRows+1))
	writeResume(t, vault, "doney", completedTable(ResumeMaxCompletedRows+1))
	writeResume(t, vault, "tidy", historyTable(2)+"\n"+completedTable(2))

	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if r.Summary != "3 of 4 resume.md over cap" {
		t.Errorf("summary = %q, want \"3 of 4 resume.md over cap\"", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	for _, want := range []string{
		"big: 25.0 KB (cap 25 KB)",
		"histy: Project History 16 rows (cap 15)",
		"doney: Completed Plans 13 rows (cap 12)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "tidy:") {
		t.Errorf("under-cap project should be silent:\n%s", joined)
	}
	// Details are sorted by project, and the remediation lines come last.
	if !strings.Contains(r.Details[len(r.Details)-1], "iterations.md") {
		t.Errorf("last detail should be the remediation pointer, got %q", r.Details[len(r.Details)-1])
	}
}

func TestCheckResumeCaps_AllThreeCapsAtOnce(t *testing.T) {
	vault := t.TempDir()
	body := historyTable(16) + "\n" + completedTable(13) +
		"\n## Notes\n\n" + strings.Repeat("z", ResumeMaxBytes)
	writeResume(t, vault, "fat", body)

	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	row := ""
	for _, d := range r.Details {
		if strings.HasPrefix(strings.TrimSpace(d), "fat:") {
			row = d
		}
	}
	if row == "" {
		t.Fatalf("no row for project 'fat': %v", r.Details)
	}
	for _, want := range []string{"cap 25 KB", "Project History 16 rows (cap 15)", "Completed Plans 13 rows (cap 12)"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q missing %q", row, want)
		}
	}
}

func TestCheckResumeCaps_SkipsDotAndUnderscoreDirs(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, ".hidden", historyTable(50))
	writeResume(t, vault, "_archive", historyTable(50))
	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Pass {
		t.Errorf("status = %v (%v), want Pass — dot/underscore dirs are not projects", r.Status, r.Details)
	}
}

func TestCheckResumeCaps_ProjectsIsAFile(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "Projects"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := CheckResumeCaps(storage.NewVault(vault))
	if r.Status != Info {
		t.Errorf("status = %v, want Info on an unreadable Projects/", r.Status)
	}
	if !strings.Contains(r.Summary, "scan Projects/") {
		t.Errorf("summary = %q", r.Summary)
	}
}

// TestCheckResumeCaps_IsReadOnly proves the check never mutates the vault:
// resume.md bytes and mtime are unchanged after a run that flags it.
func TestCheckResumeCaps_IsReadOnly(t *testing.T) {
	vault := t.TempDir()
	body := historyTable(30) + "\n" + completedTable(30)
	writeResume(t, vault, "fat", body)
	path := filepath.Join(vault, "Projects", "fat", "resume.md")

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := CheckResumeCaps(storage.NewVault(vault)); r.Status != Info {
		t.Fatalf("expected a flagged resume, got %v", r.Status)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Errorf("resume.md was mutated: %v/%d -> %v/%d",
			before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("resume.md content changed")
	}
	// No sibling artifacts written either.
	entries, err := os.ReadDir(filepath.Join(vault, "Projects", "fat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("check wrote extra files: %v", entries)
	}
}
