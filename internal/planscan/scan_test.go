// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package planscan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writePlan creates <claudeHome>/plans/<name>.md with the given body and
// returns its absolute path.
func writePlan(t *testing.T, claudeHome, name, body string) string {
	t.Helper()
	plansDir := filepath.Join(claudeHome, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	p := filepath.Join(plansDir, name+".md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return p
}

// markedProject creates a project tree under a fresh temp dir with a
// .vibe-palace.toml naming the given slug, plus a matching Projects/<slug>
// directory in vaultRoot. It returns the project tree's absolute path.
func markedProject(t *testing.T, vaultRoot, slug string) string {
	t.Helper()
	tree := filepath.Join(t.TempDir(), slug)
	if err := os.MkdirAll(filepath.Join(tree, "internal"), 0o755); err != nil {
		t.Fatalf("mkdir project tree: %v", err)
	}
	cfg := "[project]\nname = \"" + slug + "\"\n"
	if err := os.WriteFile(filepath.Join(tree, ".vibe-palace.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects", slug), 0o755); err != nil {
		t.Fatalf("mkdir vault project: %v", err)
	}
	return tree
}

func TestScan_Managed(t *testing.T) {
	claudeHome := t.TempDir()
	vaultRoot := t.TempDir()
	tree := markedProject(t, vaultRoot, "myproj")

	body := "# Plan\n\nEdit `" + tree + "/internal` then run tests in " + tree + ".\n"
	planFile := writePlan(t, claudeHome, "abc", body)

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Strays) != 1 {
		t.Fatalf("got %d strays, want 1", len(rep.Strays))
	}
	s := rep.Strays[0]
	if s.File != planFile {
		t.Errorf("File = %q, want %q", s.File, planFile)
	}
	if s.Resolution.Kind != "managed" {
		t.Fatalf("Kind = %q, want managed", s.Resolution.Kind)
	}
	if s.Resolution.Project != "myproj" {
		t.Errorf("Project = %q, want myproj", s.Resolution.Project)
	}
	if s.Resolution.Ambiguous {
		t.Errorf("Ambiguous = true, want false")
	}
	if len(s.AbsPaths) == 0 {
		t.Errorf("AbsPaths empty, want the referenced paths")
	}
}

func TestScan_Unmanaged(t *testing.T) {
	claudeHome := t.TempDir()
	vaultRoot := t.TempDir()
	// A real directory with NO .vibe-palace.toml anywhere up the tree.
	work := filepath.Join(t.TempDir(), "loose", "code")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}

	body := "Work happened in " + work + " today.\n"
	writePlan(t, claudeHome, "def", body)

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Strays) != 1 {
		t.Fatalf("got %d strays, want 1", len(rep.Strays))
	}
	s := rep.Strays[0]
	if s.Resolution.Kind != "unmanaged" {
		t.Fatalf("Kind = %q, want unmanaged", s.Resolution.Kind)
	}
	if s.Resolution.Project != "" {
		t.Errorf("Project = %q, want empty", s.Resolution.Project)
	}
	if s.Resolution.CandidateDir != work {
		t.Errorf("CandidateDir = %q, want %q (top candidate as evidence)", s.Resolution.CandidateDir, work)
	}
}

func TestScan_PathFree(t *testing.T) {
	claudeHome := t.TempDir()
	vaultRoot := t.TempDir()

	body := "# Plan\n\nRefactor the parser and add tests. No paths here.\n"
	writePlan(t, claudeHome, "ghi", body)

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Strays) != 1 {
		t.Fatalf("got %d strays, want 1", len(rep.Strays))
	}
	s := rep.Strays[0]
	if s.Resolution.Kind != "none" {
		t.Fatalf("Kind = %q, want none", s.Resolution.Kind)
	}
	if len(s.AbsPaths) != 0 {
		t.Errorf("AbsPaths = %v, want empty", s.AbsPaths)
	}
	if s.Resolution.CandidateDir != "" {
		t.Errorf("CandidateDir = %q, want empty", s.Resolution.CandidateDir)
	}
}

func TestScan_MultiRoot(t *testing.T) {
	claudeHome := t.TempDir()
	vaultRoot := t.TempDir()
	treeA := markedProject(t, vaultRoot, "proja")
	treeB := markedProject(t, vaultRoot, "projb")

	body := "Touched both " + treeA + " and " + treeB + " in this session.\n"
	writePlan(t, claudeHome, "jkl", body)

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	s := rep.Strays[0]
	if !s.Resolution.Ambiguous {
		t.Fatalf("Ambiguous = false, want true for a multi-root plan")
	}
	// Both candidate roots must be present — never collapse to one guess.
	found := map[string]bool{}
	for _, c := range s.Resolution.Candidates {
		found[c] = true
	}
	if !found[treeA] || !found[treeB] {
		t.Errorf("Candidates = %v, want both %q and %q", s.Resolution.Candidates, treeA, treeB)
	}
}

// TestScan_DirAndFileBeneathAreOneOwner: a plan naming both a directory and a
// file under it is a single lineage — one candidate, never ambiguous, and the
// candidate is the shallowest (the directory), never the file.
func TestScan_DirAndFileBeneathAreOneOwner(t *testing.T) {
	claudeHome := t.TempDir()
	vaultRoot := t.TempDir()
	// "Files to create" that do not exist on disk — pure string lineage.
	base := filepath.Join(t.TempDir(), "junk-project")
	file := filepath.Join(base, "ttt.py")

	body := "Create " + file + " inside " + base + " and wire it up.\n"
	writePlan(t, claudeHome, "pqr", body)

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	s := rep.Strays[0]
	if s.Resolution.Ambiguous {
		t.Errorf("Ambiguous = true, want false (dir + file beneath it are one owner)")
	}
	if len(s.Resolution.Candidates) != 1 {
		t.Fatalf("Candidates = %v, want exactly 1 (the collapsed dir)", s.Resolution.Candidates)
	}
	if s.Resolution.CandidateDir != base {
		t.Errorf("CandidateDir = %q, want the shallowest %q (never the file)", s.Resolution.CandidateDir, base)
	}
	if s.Resolution.Kind != "unmanaged" {
		t.Errorf("Kind = %q, want unmanaged", s.Resolution.Kind)
	}
}

func TestScan_MissingPlansDir(t *testing.T) {
	claudeHome := t.TempDir() // no plans/ subdir
	vaultRoot := t.TempDir()

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan on absent plans dir: %v", err)
	}
	if len(rep.Strays) != 0 {
		t.Errorf("Strays = %v, want empty", rep.Strays)
	}
	if rep.PlansDir != filepath.Join(claudeHome, "plans") {
		t.Errorf("PlansDir = %q, want %q", rep.PlansDir, filepath.Join(claudeHome, "plans"))
	}
}

func TestScan_Ranking(t *testing.T) {
	claudeHome := t.TempDir()
	vaultRoot := t.TempDir()
	base := t.TempDir()
	frequent := filepath.Join(base, "hot")
	rare := filepath.Join(base, "cold")
	for _, d := range []string{frequent, rare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	// frequent mentioned 3x, rare 1x.
	body := frequent + "\n" + frequent + "\n" + frequent + "\n" + rare + "\n"
	writePlan(t, claudeHome, "mno", body)

	rep, err := Scan(claudeHome, vaultRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	cands := rep.Strays[0].Resolution.Candidates
	if len(cands) < 2 {
		t.Fatalf("Candidates = %v, want at least 2", cands)
	}
	if cands[0] != frequent {
		t.Errorf("top candidate = %q, want the frequently-mentioned %q", cands[0], frequent)
	}
}

func TestExtractAbsPaths(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"plain", "run /home/a/b now", []string{"/home/a/b"}},
		{"backticked", "edit `/home/a/c.go` please", []string{"/home/a/c.go"}},
		{"trailing-period", "see /home/a/d.", []string{"/home/a/d"}},
		{"url-not-matched", "visit https://example.com/x for docs", nil},
		{"none", "no paths at all", nil},
		{"bare-slash", "a / b", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAbsPaths(tt.body)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractAbsPaths(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestRankCandidateDirs(t *testing.T) {
	occ := []string{"/x/a", "/x/a", "/x/b"}
	got := rankCandidateDirs(occ)
	if len(got) != 2 || got[0] != "/x/a" {
		t.Errorf("rankCandidateDirs(%v) = %v, want /x/a first", occ, got)
	}

	// Depth tie-break: equal frequency, deeper wins.
	occ2 := []string{"/x/deep/dir", "/y"}
	got2 := rankCandidateDirs(occ2)
	if got2[0] != "/x/deep/dir" {
		t.Errorf("depth tie-break: got %v, want /x/deep/dir first", got2)
	}
}

func TestIsSegmentPrefix(t *testing.T) {
	for _, tt := range []struct {
		a, p string
		want bool
	}{
		{"/a/b", "/a/b", true},    // equal
		{"/a/b", "/a/b/c", true},  // segment descendant
		{"/a/b", "/a/bc", false},  // NOT a boundary — sibling-ish
		{"/a/b", "/a/bcd/e", false},
		{"/", "/anything/x", true}, // root is ancestor of all
		{"/x/y", "/x", false},      // deeper is not a prefix of shallower
	} {
		if got := isSegmentPrefix(tt.a, tt.p); got != tt.want {
			t.Errorf("isSegmentPrefix(%q, %q) = %v, want %v", tt.a, tt.p, got, tt.want)
		}
	}
}

func TestCollapseLineages(t *testing.T) {
	// A dir and a file beneath it collapse to the dir, counts summed.
	got := collapseLineages(map[string]int{"/a/b": 1, "/a/b/c.py": 1})
	if len(got) != 1 || got["/a/b"] != 2 {
		t.Errorf("collapse dir+file: got %v, want {/a/b:2}", got)
	}
	// Genuinely divergent roots are preserved.
	got2 := collapseLineages(map[string]int{"/x/projA": 1, "/y/projB": 2})
	if len(got2) != 2 {
		t.Errorf("divergent roots collapsed: got %v, want 2 entries", got2)
	}
	// A sibling that only shares a non-boundary prefix stays separate.
	got3 := collapseLineages(map[string]int{"/a/b": 1, "/a/bc": 1})
	if len(got3) != 2 {
		t.Errorf("/a/b vs /a/bc collapsed: got %v, want 2 entries", got3)
	}
}

func TestDepth(t *testing.T) {
	for _, tt := range []struct {
		p    string
		want int
	}{
		{"/x", 1},
		{"/x/y", 2},
		{"/x/y/z", 3},
	} {
		if got := depth(tt.p); got != tt.want {
			t.Errorf("depth(%q) = %d, want %d", tt.p, got, tt.want)
		}
	}
}
