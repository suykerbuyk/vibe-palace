// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedVaultProject creates a minimal palace-project vault directory with a
// config.toml so Apply's guard passes. Returns the vault.
func seedVaultProject(t *testing.T, slug string) *storage.Vault {
	t.Helper()
	vroot := t.TempDir()
	v := storage.NewVault(vroot)
	projDir := filepath.Join(vroot, "Projects", slug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "config.toml"),
		[]byte("[project]\nname = \""+slug+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestApply_EndToEnd(t *testing.T) {
	v := seedVaultProject(t, "checkers01")

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(checkersFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	fixedTime, _ := time.Parse("2006-01-02", "2026-04-12")
	report, err := Apply(plan, WriteOptions{
		Vault:       v,
		Project:     "checkers01",
		ProjectRoot: repo,
		Now:         fixedTime,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Architecture file created.
	archPath, _ := v.DocFile("checkers01", "architecture.md")
	if _, err := os.Stat(archPath); err != nil {
		t.Fatalf("architecture.md not created: %v", err)
	}
	archData, _ := os.ReadFile(archPath)
	if !strings.Contains(string(archData), "Move atomicity") {
		t.Errorf("architecture.md missing Move atomicity: %s", archData)
	}
	if !strings.Contains(string(archData), "Architecture (from PRD §4, §7)") {
		t.Errorf("architecture.md missing Architecture heading: %s", archData)
	}

	// Knowledge contains Notation and Rules.
	knowPath, _ := v.KnowledgeFile("checkers01")
	knowData, _ := os.ReadFile(knowPath)
	if !strings.Contains(string(knowData), "Notation") {
		t.Errorf("knowledge.md missing Notation")
	}
	if !strings.Contains(string(knowData), "Rules — quick reference") {
		t.Errorf("knowledge.md missing Rules heading")
	}

	// Workflow contains both Commands and Rules section groupings.
	wfPath, _ := v.WorkflowFile("checkers01")
	wfData, _ := os.ReadFile(wfPath)
	if !strings.Contains(string(wfData), "## Commands") {
		t.Errorf("workflow.md missing Commands section header")
	}
	if !strings.Contains(string(wfData), "## Rules") {
		t.Errorf("workflow.md missing Rules section header")
	}
	if !strings.Contains(string(wfData), "go test") {
		t.Errorf("workflow.md missing commands body")
	}

	// Scope.
	scopePath, _ := v.DocFile("checkers01", "scope.md")
	if _, err := os.Stat(scopePath); err != nil {
		t.Errorf("scope.md not created: %v", err)
	}

	// Testing.
	testingPath, _ := v.DocFile("checkers01", "testing.md")
	if _, err := os.Stat(testingPath); err != nil {
		t.Errorf("testing.md not created: %v", err)
	}

	// Resume scratch created; resume.md itself NOT written.
	if report.ResumeScratchPath == "" {
		t.Errorf("expected resume scratch path in report")
	}
	scratchData, err := os.ReadFile(report.ResumeScratchPath)
	if err != nil {
		t.Fatalf("resume scratch unreadable: %v", err)
	}
	if !strings.Contains(string(scratchData), "Pre-code") {
		t.Errorf("scratch missing Status body: %s", scratchData)
	}
	if !strings.Contains(string(scratchData), "Reference pointers") {
		t.Errorf("scratch missing reference pointers section")
	}
	resumePath, _ := v.ResumeFile("checkers01")
	if _, err := os.Stat(resumePath); err == nil {
		t.Errorf("resume.md should not be auto-written by absorb")
	}

	// CLAUDE.md reduced to preamble + managed block.
	claudeData, _ := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	cs := string(claudeData)
	if !strings.Contains(cs, "vibe-palace:begin") {
		t.Errorf("managed block missing from rewritten CLAUDE.md: %s", cs)
	}
	if strings.Contains(cs, "Move atomicity") || strings.Contains(cs, "Notation") {
		t.Errorf("original body content survived rewrite: %s", cs)
	}

	// Backup exists.
	backups, _ := filepath.Glob(filepath.Join(repo, ".vibe-palace", "CLAUDE.md.bak-*"))
	if len(backups) == 0 {
		t.Errorf("no backup written")
	}
}

func TestApply_Idempotent(t *testing.T) {
	v := seedVaultProject(t, "checkers01")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(checkersFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedTime, _ := time.Parse("2006-01-02", "2026-04-12")

	plan1, _ := BuildPlan(repo)
	if _, err := Apply(plan1, WriteOptions{Vault: v, Project: "checkers01", ProjectRoot: repo, Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	archPath, _ := v.DocFile("checkers01", "architecture.md")
	sizeBefore, _ := os.Stat(archPath)

	// Re-populate CLAUDE.md with the same content and re-run.
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(checkersFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	plan2, _ := BuildPlan(repo)
	report, err := Apply(plan2, WriteOptions{Vault: v, Project: "checkers01", ProjectRoot: repo, Now: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	sizeAfter, _ := os.Stat(archPath)
	if sizeAfter.Size() != sizeBefore.Size() {
		t.Errorf("architecture.md grew on re-run: %d → %d (dedup failed)",
			sizeBefore.Size(), sizeAfter.Size())
	}
	if len(report.DuplicateSkipped) == 0 {
		t.Errorf("expected DuplicateSkipped entries on re-run, got none")
	}
}

func TestApply_RequiresProjectConfig(t *testing.T) {
	vroot := t.TempDir()
	v := storage.NewVault(vroot)
	// Note: no config.toml written.
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# x\n\n## Architecture\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ := BuildPlan(repo)
	_, err := Apply(plan, WriteOptions{Vault: v, Project: "someproj", ProjectRoot: repo})
	if err == nil {
		t.Fatal("expected error when vault project config missing")
	}
	if !strings.Contains(err.Error(), "vp init") {
		t.Errorf("error should reference `vp init`: %v", err)
	}
}

func TestApply_WholeFileCursorrules(t *testing.T) {
	v := seedVaultProject(t, "proj1")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".cursorrules"),
		[]byte("always cite sources"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ := BuildPlan(repo)
	fixedTime, _ := time.Parse("2006-01-02", "2026-04-12")
	if _, err := Apply(plan, WriteOptions{
		Vault: v, Project: "proj1", ProjectRoot: repo, Now: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	knowPath, _ := v.KnowledgeFile("proj1")
	data, err := os.ReadFile(knowPath)
	if err != nil {
		t.Fatalf("knowledge.md missing: %v", err)
	}
	if !strings.Contains(string(data), "always cite sources") {
		t.Errorf("cursorrules body not written: %s", data)
	}
	// Source was rewritten: it should now contain only preamble + block.
	src, _ := os.ReadFile(filepath.Join(repo, ".cursorrules"))
	if !strings.Contains(string(src), "vibe-palace:begin") {
		t.Errorf("rewritten .cursorrules missing managed block: %s", src)
	}
	if strings.Contains(string(src), "always cite sources") {
		t.Errorf("original body leaked into rewritten .cursorrules: %s", src)
	}
}

func TestApply_SymlinkDedup(t *testing.T) {
	v := seedVaultProject(t, "proj1")
	repo := t.TempDir()
	claudePath := filepath.Join(repo, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# X\n\n## Architecture\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(repo, "AGENTS.md")
	if err := os.Symlink(claudePath, agentsPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	plan, _ := BuildPlan(repo)
	if len(plan.Sources) != 1 {
		t.Errorf("symlink dedup failed; got %d sources", len(plan.Sources))
	}
	fixedTime, _ := time.Parse("2006-01-02", "2026-04-12")
	_, err := Apply(plan, WriteOptions{
		Vault: v, Project: "proj1", ProjectRoot: repo, Now: fixedTime,
	})
	if err != nil {
		t.Fatal(err)
	}
}
