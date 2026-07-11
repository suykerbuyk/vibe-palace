// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// TestSkillsShowDirBody exercises runSkillsShow against the embedded
// startup-analyst skill: SKILL.md body + sorted references list.
func TestSkillsShowDirBody(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{Name: "startup-analyst"})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "# skill: startup-analyst | source: embedded") {
		t.Errorf("missing header, got %q", s[:min(120, len(s))])
	}
	if !strings.Contains(s, "References (fetch with --section=<name>):") {
		t.Error("missing references block")
	}
	for _, r := range []string{"capex-opex", "competitive-landscape", "funding-sources",
		"reality-validation", "strategic-partnerships"} {
		if !strings.Contains(s, "  - "+r+"\n") {
			t.Errorf("missing ref %q", r)
		}
	}
}

// TestSkillsShowSection verifies --section fetches just the reference
// body (no references list, no wrapping).
func TestSkillsShowSection(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{
		Name:    "startup-analyst",
		Section: "capex-opex",
	})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "section: capex-opex | source: embedded") {
		t.Error("missing section header")
	}
	if strings.Contains(s, "References (fetch with") {
		t.Error("--section output should not emit references block")
	}
}

// TestSkillsShowMissingSkill returns a user-tier error.
func TestSkillsShowMissingSkill(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{Name: "no-such-skill"})
	if rc == 0 {
		t.Fatalf("expected non-zero rc")
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Errorf("stderr=%q", errOut.String())
	}
}

// TestSkillsShowProjectOverride proves runSkillsShow uses the project
// tier — a project-scoped SKILL.md should shadow the embedded copy.
func TestSkillsShowProjectOverride(t *testing.T) {
	vaultRoot := t.TempDir()
	target := filepath.Join(vaultRoot, "Projects", "myproj", "skills", "startup-analyst", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# Overridden\n\nproject body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := vpctx.NewResolver(vaultRoot)

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{
		Name:    "startup-analyst",
		Project: "myproj",
	})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "source: project") {
		t.Errorf("want source: project, got %q", s)
	}
	if !strings.Contains(s, "project body") {
		t.Error("missing project-tier body")
	}
	// Embedded references should still fall through.
	if !strings.Contains(s, "  - capex-opex\n") {
		t.Error("references should fall through from embedded even when project overrides SKILL.md")
	}
}

// TestSkillsListContainsEmbedded exercises the `vp skills list`
// plumbing: the resolver must surface the embedded startup-analyst
// skill in the Summary output printed by printSkillsTable.
func TestSkillsListContainsEmbedded(t *testing.T) {
	// We drive the command object directly because its Run closure
	// calls openProjectVault(); instead we call the shared library
	// used inside that closure.
	resolver := vpctx.NewResolver(t.TempDir())

	// Reuse the `commands.List` call path (exercised inside Run).
	// The printer is also exercised to ensure formatting doesn't crash.
	var buf bytes.Buffer
	summaries, err := commands.List(resolver, "skill", "", "", "", 60)
	if err != nil {
		t.Fatal(err)
	}
	printSkillsTable(&buf, summaries, "")
	s := buf.String()
	if !strings.Contains(s, "startup-analyst") {
		t.Errorf("missing startup-analyst in table, got %q", s)
	}
	if !strings.Contains(s, "embedded") {
		t.Error("missing embedded source tag")
	}
}

// TestSkillsCommandMetadata touches the three *cli.Command factory
// functions so their top-level metadata (Name, Synopsis, Subcommands)
// is exercised. Run closures themselves require a real project vault
// and are covered via the end-to-end integration suite.
func TestSkillsCommandMetadata(t *testing.T) {
	cs := cmdSkills()
	if cs.Name != "skills" {
		t.Errorf("name = %q", cs.Name)
	}
	if cs.Run != nil {
		t.Error("cmdSkills Run should be nil — parent help is rendered by the dispatcher")
	}
	if len(cs.Subcommands) == 0 {
		t.Error("cmdSkills should declare its Subcommands so the dispatcher can render help")
	}
	cl := cmdSkillsList()
	if cl.Name != "skills list" {
		t.Errorf("list name = %q", cl.Name)
	}
	if len(cl.Flags) == 0 {
		t.Error("cmdSkillsList missing flags")
	}
	cshow := cmdSkillsShow()
	if cshow.Name != "skills show" {
		t.Errorf("show name = %q", cshow.Name)
	}
	// Run with no args should fail on missing positional.
	if rc := cshow.Run(nil); rc == 0 {
		t.Error("cmdSkillsShow with no args should fail")
	}
	// Run with unknown flag should fail parsing.
	if rc := cshow.Run([]string{"--nope"}); rc == 0 {
		t.Error("cmdSkillsShow with bad flag should fail")
	}
}

// TestSkillsShowEmptyBody covers the edge case where SKILL.md is empty
// so the trailing-newline logic in runSkillsShow skips the append.
func TestSkillsShowEmptyBody(t *testing.T) {
	vaultRoot := t.TempDir()
	target := filepath.Join(vaultRoot, "Templates", "skills", "empty", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := vpctx.NewResolver(vaultRoot)
	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{Name: "empty"})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
}

// TestSkillsShowSectionMissing covers the --section error path in
// runSkillsShow; happy-path already covered above.
func TestSkillsShowSectionMissing(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())
	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{
		Name: "startup-analyst", Section: "not-there",
	})
	if rc == 0 {
		t.Fatal("expected non-zero rc")
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Errorf("stderr=%q", errOut.String())
	}
}

// TestPrintSkillsTableEmpty + TestPrintSkillsTableProject round out
// the formatter branches so printSkillsTable coverage stays tight.
func TestPrintSkillsTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	printSkillsTable(&buf, nil, "")
	if !strings.Contains(buf.String(), "No skills available.") {
		t.Errorf("got %q", buf.String())
	}
}

func TestPrintSkillsTableProject(t *testing.T) {
	var buf bytes.Buffer
	printSkillsTable(&buf, []commands.Summary{{Name: "a", Source: "project", Brief: "b"}}, "myproj")
	s := buf.String()
	if !strings.Contains(s, `project "myproj"`) {
		t.Errorf("missing project header: %q", s)
	}
}
