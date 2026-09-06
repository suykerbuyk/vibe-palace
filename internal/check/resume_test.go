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

func TestResumeRefBreaches(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantCount  int
		wantSubstr []string // substrings that must appear across the breaches
	}{
		{
			name:       "home-relative plan ref is a breach",
			body:       "## Current State\n\nSee ~/.claude/plans/foo.md for the plan.\n",
			wantCount:  1,
			wantSubstr: []string{"line 3", "~/.claude/plans/foo.md"},
		},
		{
			name:       "absolute plan ref is a breach",
			body:       "# Resume\n\nPlan lives at /home/x/.claude/plans/bar.md today.\n",
			wantCount:  1,
			wantSubstr: []string{"line 3", "/home/x/.claude/plans/bar.md"},
		},
		{
			name:      "home-relative ref inside a code fence is NOT a breach",
			body:      "## Notes\n\n```\n~/.claude/plans/foo.md\n```\n",
			wantCount: 0,
		},
		{
			name:      "absolute ref inside a code fence is NOT a breach",
			body:      "## Notes\n\n```sh\ncat /home/x/.claude/plans/bar.md\n```\n",
			wantCount: 0,
		},
		{
			name:      "clean resume has no breach",
			body:      "# Resume\n\n## Current State\n\n- all good\n",
			wantCount: 0,
		},
		{
			name:      "legitimate vault-relative doc pointer is NOT a breach",
			body:      "See doc/PLANS.md and tasks/done/task-7.md for the record.\n",
			wantCount: 0,
		},
		{
			name:       "home-relative ref is counted once, not double-flagged as absolute",
			body:       "~/.claude/plans/foo.md\n",
			wantCount:  1,
			wantSubstr: []string{"line 1", "~/.claude/plans/foo.md"},
		},
		{
			name:       "both patterns on separate lines each breach",
			body:       "a: ~/.claude/plans/a.md\nb: /home/y/.claude/plans/b.md\n",
			wantCount:  2,
			wantSubstr: []string{"~/.claude/plans/a.md", "/home/y/.claude/plans/b.md"},
		},
		{
			name:       "ref mid-line after a real fence has closed is still a breach",
			body:       "```\nsample\n```\n\nlive: ~/.claude/plans/live.md\n",
			wantCount:  1,
			wantSubstr: []string{"line 5", "~/.claude/plans/live.md"},
		},
		{
			name:       "CRLF line ending does not bleed into the matched path",
			body:       "ref: /home/z/.claude/plans/c.md\r\n",
			wantCount:  1,
			wantSubstr: []string{"/home/z/.claude/plans/c.md"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resumeRefBreaches([]byte(tc.body))
			if len(got) != tc.wantCount {
				t.Fatalf("breach count = %d, want %d: %v", len(got), tc.wantCount, got)
			}
			joined := strings.Join(got, "\n")
			for _, want := range tc.wantSubstr {
				if !strings.Contains(joined, want) {
					t.Errorf("breaches %q missing %q", joined, want)
				}
			}
		})
	}
}

func TestCheckResumeRefs_EmptyVaultRootSkips(t *testing.T) {
	r := CheckResumeRefs(storage.NewVault(""))
	if r.Status != Skip {
		t.Errorf("status = %v, want Skip on an empty vault root", r.Status)
	}
}

func TestCheckResumeRefs_NoProjectsDir(t *testing.T) {
	r := CheckResumeRefs(storage.NewVault(t.TempDir()))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "no Projects/ directory" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckResumeRefs_CleanIsPass(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "tidy", "# tidy\n\n## Current State\n\nSee tasks/done/task-1.md.\n")
	r := CheckResumeRefs(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v (%s), want Pass", r.Status, r.Summary)
	}
	if r.Summary != "1 resume.md free of host-local plan refs" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckResumeRefs_MissingResumeIsNotAViolation(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckResumeRefs(storage.NewVault(vault))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass (missing resume.md is not a violation)", r.Status)
	}
	if r.Summary != "0 resume.md free of host-local plan refs" {
		t.Errorf("summary = %q, want the project to be skipped, not scanned", r.Summary)
	}
}

func TestCheckResumeRefs_SkipsDotAndUnderscoreDirs(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, ".hidden", "~/.claude/plans/x.md\n")
	writeResume(t, vault, "_archive", "/home/x/.claude/plans/y.md\n")
	r := CheckResumeRefs(storage.NewVault(vault))
	if r.Status != Pass {
		t.Errorf("status = %v (%v), want Pass — dot/underscore dirs are not projects", r.Status, r.Details)
	}
}

func TestCheckResumeRefs_ProjectsIsAFile(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "Projects"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := CheckResumeRefs(storage.NewVault(vault))
	if r.Status != Info {
		t.Errorf("status = %v, want Info on an unreadable Projects/", r.Status)
	}
	if !strings.Contains(r.Summary, "scan Projects/") {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckResumeRefs_FlagsEachProjectAndNeverFails(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "homey", "## State\n\nplan: ~/.claude/plans/foo.md\n")
	writeResume(t, vault, "absy", "## State\n\nplan: /home/x/.claude/plans/bar.md\n")
	writeResume(t, vault, "fenced", "## State\n\n```\n~/.claude/plans/ignored.md\n```\n")
	writeResume(t, vault, "clean", "## State\n\nSee tasks/done/task-1.md.\n")

	r := CheckResumeRefs(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if r.Summary != "2 of 4 resume.md hold host-local plan refs" {
		t.Errorf("summary = %q, want \"2 of 4 resume.md hold host-local plan refs\"", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	for _, want := range []string{
		"absy: line 3: /home/x/.claude/plans/bar.md",
		"homey: line 3: ~/.claude/plans/foo.md",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "fenced:") {
		t.Errorf("fenced reference must be ignored:\n%s", joined)
	}
	if strings.Contains(joined, "clean:") {
		t.Errorf("clean project should be silent:\n%s", joined)
	}
	// Details are sorted by project (absy before homey) and end with remediation.
	if !strings.Contains(r.Details[len(r.Details)-1], "vault-relative") {
		t.Errorf("last detail should be the remediation pointer, got %q", r.Details[len(r.Details)-1])
	}
}

// TestCheckResumeRefs_IsReadOnly proves the check never mutates the vault.
func TestCheckResumeRefs_IsReadOnly(t *testing.T) {
	vault := t.TempDir()
	body := "## State\n\nplan: ~/.claude/plans/foo.md\n"
	writeResume(t, vault, "homey", body)
	path := filepath.Join(vault, "Projects", "homey", "resume.md")

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := CheckResumeRefs(storage.NewVault(vault)); r.Status != Info {
		t.Fatalf("expected a flagged resume, got %v", r.Status)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Errorf("resume.md was mutated")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("resume.md content changed")
	}
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

// TestCountSectionTableRowsInlineCodeRun pins a LATENT fail-open in the cap
// check. No resume.md in the vault trips it today — the live files carry no
// fences at all — but the defect is real and is the same one that broke
// wrapstate's iteration counter and storage's task validation.
//
// A line whose first non-space characters are an INLINE code run is prose. The
// naive rule ("the trimmed line starts with ```") reads it as an opening fence
// and treats everything after it as fenced — so the table rows below it are
// never counted, the cap is never breached, and the one check whose entire job
// is to notice quietly reports nothing.
func TestCountSectionTableRowsInlineCodeRun(t *testing.T) {
	const inlineRun = "  ```bash tutorial``` extraction from `doc/TUTORIAL.md` — deferred"

	body := "## Project History\n\n" + inlineRun + "\n\n" +
		"| # | Summary |\n|---|---------|\n| 3 | c |\n| 2 | b |\n| 1 | a |\n"

	if got := countSectionTableRows([]byte(body), "Project History"); got != 3 {
		t.Errorf("FAILS OPEN: got %d data rows, want 3 — an inline code run was misread "+
			"as an opening fence, hiding the table from the cap check", got)
	}
}

// A genuine fence must still hide pipe-leading lines, which is what the fence
// tracking was for in the first place.
func TestCountSectionTableRowsRealFenceStillHidesRows(t *testing.T) {
	body := "## Project History\n\n" +
		"| # | Summary |\n|---|---------|\n| 1 | real |\n\n" +
		"```md\n| 9 | fake, inside a fence |\n| 8 | also fake |\n```\n"

	if got := countSectionTableRows([]byte(body), "Project History"); got != 1 {
		t.Errorf("got %d data rows, want 1 — a fenced table must not inflate the count", got)
	}
}

// TestTypedResumeWriteIgnoresTheCap pins the claim the ResumeMaxBytes doc
// comment makes about its own reach: a typed, compare-and-set-guarded write
// path exists (vp_update_resume, over (*storage.Vault).WriteResume), and it
// does NOT consult the cap.
//
// This is the X16 correction's proof. The comment previously read "there is no
// typed write path left to gate", which was false — vp_update_resume has been
// registered mutating since 52cfa11. The accurate statement is that the one
// typed path does not consult the cap, and that statement is only worth making
// if something reddens when it stops being true. Adding a size refusal to
// WriteResume fails this test, which is the intended signal to revisit the
// comment rather than leave it stale a second time.
func TestTypedResumeWriteIgnoresTheCap(t *testing.T) {
	root := t.TempDir()
	vault := &storage.Vault{Root: root}

	over := "# Over cap\n\n" + strings.Repeat("x", ResumeMaxBytes+1)

	// Empty expectedSha256 is the assert-absent first write.
	if err := vault.WriteResume("fat", over, ""); err != nil {
		t.Fatalf("typed resume write refused an over-cap body: %v", err)
	}

	path, err := vault.ResumeFile("fat")
	if err != nil {
		t.Fatalf("resolve resume path: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back resume: %v", err)
	}
	if len(got) != len(over) {
		t.Fatalf("typed write truncated to the cap: wrote %d bytes, read back %d", len(over), len(got))
	}
	if len(got) <= ResumeMaxBytes {
		t.Fatalf("fixture is not over cap: %d bytes vs cap %d", len(got), ResumeMaxBytes)
	}

	// The reader is the only thing that reacts, and it reacts with Info.
	res := CheckResumeCaps(vault)
	if res.Status != Info {
		t.Fatalf("over-cap resume: got status %v, want Info — the cap is detection, never a gate", res.Status)
	}
}
