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

	// Resume scratch created; resume.md itself NOT written. absorb has no
	// resume destination at all: every resume-bound item is DestResumeScratch
	// and lands in absorbed/resume-suggestions.md for human merge. absorb's
	// atomicWrite holds no vaultlock and carries no expected-sha, so a direct
	// resume.md write from here would bypass both the advisory lock and the
	// WriteResume compare-and-set. Pin the destination, not just its absence.
	if report.ResumeScratchPath == "" {
		t.Errorf("expected resume scratch path in report")
	}
	wantScratch, _ := v.AbsorbedFile("checkers01", "resume-suggestions.md")
	if report.ResumeScratchPath != wantScratch {
		t.Errorf("resume scratch path = %q, want %q", report.ResumeScratchPath, wantScratch)
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

// absorbRepo is a project repo holding one CLAUDE.md section to absorb.
func absorbRepo(t *testing.T) (*Plan, string) {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# x\n\n## Architecture\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	return plan, repo
}

// A project with real history but no config.toml is initialised: the live
// shape of atlassian-vault, qa-metabuild-system, rusty-can and tools, which
// the config.toml-must-exist predicate refused.
func TestApply_AcceptsProjectWithHistoryButNoConfig(t *testing.T) {
	vroot := t.TempDir()
	v := storage.NewVault(vroot)
	dir := filepath.Join(vroot, "Projects", "hist")
	for _, rel := range []string{"resume.md", "iterations.md", "sessions/2026-01-01-01.md", "tasks/t.md"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, repo := absorbRepo(t)
	if _, err := Apply(plan, WriteOptions{Vault: v, Project: "hist", ProjectRoot: repo}); err != nil {
		t.Fatalf("Apply refused a project with history and no config.toml: %v", err)
	}
}

// A phantom directory — only memory/ — is not an initialised project.
func TestApply_RefusesPhantomProject(t *testing.T) {
	vroot := t.TempDir()
	v := storage.NewVault(vroot)
	if err := os.MkdirAll(filepath.Join(vroot, "Projects", "ghost", "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vroot, "Projects", "ghost", "memory", "m.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, repo := absorbRepo(t)
	_, err := Apply(plan, WriteOptions{Vault: v, Project: "ghost", ProjectRoot: repo})
	if err == nil {
		t.Fatal("Apply accepted a phantom project directory")
	}
	for _, want := range []string{"phantom", "vp init", "Projects/ghost/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q: %v", want, err)
		}
	}
}

// A symlinked Projects/<slug> is refused, by intent: ListAllProjects already
// excludes symlinked projects, and absorb must not follow a link out of the
// vault's tree. At 5a2d7bc this was accepted, because os.Stat followed it.
func TestApply_RefusesSymlinkedProject(t *testing.T) {
	vroot := t.TempDir()
	v := storage.NewVault(vroot)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"resume.md", "config.toml"} {
		if err := os.WriteFile(filepath.Join(target, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(vroot, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(vroot, "Projects", "linked")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	plan, repo := absorbRepo(t)
	_, err := Apply(plan, WriteOptions{Vault: v, Project: "linked", ProjectRoot: repo})
	if err == nil || !strings.Contains(err.Error(), "Projects/linked is not a directory") {
		t.Fatalf("err = %v, want a symlinked project refused as not a directory", err)
	}
	if _, serr := os.Stat(filepath.Join(target, "knowledge.md")); !os.IsNotExist(serr) {
		t.Errorf("absorb wrote through the symlink (knowledge.md stat err=%v)", serr)
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
