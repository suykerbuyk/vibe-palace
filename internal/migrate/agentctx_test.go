// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeSourceAgentctx materializes a representative vibe-vault project tree
// under <root>/Projects/<name>/ and returns the project dir. It mirrors the
// real RezBldrVault layout: agentctx/ load-bearing files + tasks + memory,
// the verbatim-archive dirs, a project-level knowledge.md, and host/meta
// noise (.baseline, .gitkeep, config.toml) that must NOT be carried.
func writeSourceAgentctx(t *testing.T, root, name string) string {
	t.Helper()
	projDir := filepath.Join(root, "Projects", name)
	files := map[string]string{
		"knowledge.md":                                        "# knowledge\n",
		"agentctx/resume.md":                                  "# resume\nbody\n",
		"agentctx/iterations.md":                              "# iterations\nlong history\n",
		"agentctx/workflow.md":                                "# workflow\n",
		"agentctx/features.md":                                "# features\n",
		"agentctx/config.toml":                                "name = \"x\"\n", // meta — skip
		"agentctx/commit.msg":                                 "msg\n",          // meta — skip
		"agentctx/.surface":                                   "1\n",            // meta — skip
		"agentctx/resume.md.baseline":                         "stale\n",        // baseline — skip
		"agentctx/tasks/active-one.md":                        "# Active\n**Status:** In Progress\n",
		"agentctx/tasks/active-one.md.baseline":               "stale\n", // baseline — skip
		"agentctx/tasks/done/done-one.md":                     "# Done\n## Status: Done\n",
		"agentctx/tasks/done/done-two.md":                     "# Done2\n**Status:** retired\n",
		"agentctx/memory/MEMORY.md":                           "- index\n",
		"agentctx/memory/fact-one.md":                         "fact one\n",
		"agentctx/memory/.gitkeep":                            "", // meta — skip
		"agentctx/commands/res_build.md":                      "# res_build\n",
		"agentctx/commands/restart.md":                        "# restart\n",
		"agentctx/commands/restart.md.baseline":               "stale\n", // baseline — skip
		"agentctx/skills/startup-analyst/SKILL.md":            "# skill\n",
		"agentctx/skills/startup-analyst/references/capex.md": "ref\n", // nested — preserve
		"agentctx/snippets/snip.md":                           "snippet\n",
	}
	for rel, content := range files {
		abs := filepath.Join(projDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projDir
}

func readFile(t *testing.T, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(data)
}

func TestCopyAgentctx_LayoutAndBytes(t *testing.T) {
	root := t.TempDir()
	projDir := writeSourceAgentctx(t, root, "src-proj")
	dest := storage.NewVault(t.TempDir())
	slug := "src-proj"

	res, err := copyAgentctx(dest, projDir, slug, ImportOptions{WithAgentctx: true})
	if err != nil {
		t.Fatalf("copyAgentctx: %v", err)
	}

	pdir := filepath.Join(dest.Root, "Projects", slug)
	// Tier 1 — load-bearing slots, byte-exact.
	cases := []struct{ path, want string }{
		{filepath.Join(pdir, "resume.md"), "# resume\nbody\n"},
		{filepath.Join(pdir, "iterations.md"), "# iterations\nlong history\n"},
		{filepath.Join(pdir, "workflow.md"), "# workflow\n"},
		{filepath.Join(pdir, "knowledge.md"), "# knowledge\n"},
		{filepath.Join(pdir, "tasks", "active-one.md"), "# Active\n**Status:** In Progress\n"},
		{filepath.Join(pdir, "tasks", "done", "done-one.md"), "# Done\n## Status: Done\n"},
		{filepath.Join(pdir, "tasks", "done", "done-two.md"), "# Done2\n**Status:** retired\n"},
		{filepath.Join(pdir, "memory", "MEMORY.md"), "- index\n"},
		{filepath.Join(pdir, "memory", "fact-one.md"), "fact one\n"},
		// Tier 2 — verbatim archive under migrated/.
		{filepath.Join(pdir, "migrated", "features.md"), "# features\n"},
		{filepath.Join(pdir, "migrated", "commands", "res_build.md"), "# res_build\n"},
		{filepath.Join(pdir, "migrated", "commands", "restart.md"), "# restart\n"},
		{filepath.Join(pdir, "migrated", "skills", "startup-analyst", "SKILL.md"), "# skill\n"},
		{filepath.Join(pdir, "migrated", "skills", "startup-analyst", "references", "capex.md"), "ref\n"},
		{filepath.Join(pdir, "migrated", "snippets", "snip.md"), "snippet\n"},
	}
	for _, c := range cases {
		if got := readFile(t, c.path); got != c.want {
			t.Errorf("%s = %q, want %q", c.path, got, c.want)
		}
	}

	// Meta/baseline/.gitkeep must NOT be carried anywhere. (.surface is
	// deliberately excluded from this list: atomicfile.Write stamps a fresh
	// surface marker in the dest project dir on every write — that stamp is
	// not the copied source .surface, which my copy set never touches.)
	for _, rel := range []string{
		"config.toml", "commit.msg",
		"resume.md.baseline",
		filepath.Join("tasks", "active-one.md.baseline"),
		filepath.Join("memory", ".gitkeep"),
		filepath.Join("migrated", "commands", "restart.md.baseline"),
	} {
		if _, err := os.Stat(filepath.Join(pdir, rel)); !os.IsNotExist(err) {
			t.Errorf("meta file %s should not have been copied (err=%v)", rel, err)
		}
	}

	if res.Copied != len(cases) {
		t.Errorf("Copied = %d, want %d", res.Copied, len(cases))
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", res.Skipped)
	}
	if len(res.CrownJewelSkipped) != 0 {
		t.Errorf("CrownJewelSkipped = %v, want none", res.CrownJewelSkipped)
	}
}

func TestCopyAgentctx_Idempotent(t *testing.T) {
	root := t.TempDir()
	projDir := writeSourceAgentctx(t, root, "src-proj")
	dest := storage.NewVault(t.TempDir())

	first, err := copyAgentctx(dest, projDir, "src-proj", ImportOptions{WithAgentctx: true})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := copyAgentctx(dest, projDir, "src-proj", ImportOptions{WithAgentctx: true})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Copied != 0 {
		t.Errorf("second run Copied = %d, want 0 (copy-if-absent)", second.Copied)
	}
	if second.Skipped != first.Copied {
		t.Errorf("second run Skipped = %d, want %d", second.Skipped, first.Copied)
	}
}

// TestCopyAgentctx_CrownJewelLoudSkip is the regression guard for the HIGH
// review finding: a pre-existing (placeholder) resume.md must be reported as
// a crown-jewel skip, never silently shadowing the real source.
func TestCopyAgentctx_CrownJewelLoudSkip(t *testing.T) {
	root := t.TempDir()
	projDir := writeSourceAgentctx(t, root, "src-proj")
	dest := storage.NewVault(t.TempDir())

	// Seed a scaffold placeholder resume.md before migrating.
	pdir := filepath.Join(dest.Root, "Projects", "src-proj")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "resume.md"), []byte("PLACEHOLDER\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := copyAgentctx(dest, projDir, "src-proj", ImportOptions{WithAgentctx: true})
	if err != nil {
		t.Fatalf("copyAgentctx: %v", err)
	}

	if !contains(res.CrownJewelSkipped, filepath.Join("Projects", "src-proj", "resume.md")) {
		t.Errorf("CrownJewelSkipped = %v, want it to include resume.md", res.CrownJewelSkipped)
	}
	// The placeholder must be untouched (not overwritten).
	if got := readFile(t, pdir, "resume.md"); got != "PLACEHOLDER\n" {
		t.Errorf("resume.md = %q, want placeholder preserved (copy-if-absent)", got)
	}

	// With --force, the crown jewel is re-seeded from source.
	fres, err := copyAgentctx(dest, projDir, "src-proj", ImportOptions{WithAgentctx: true, Force: true})
	if err != nil {
		t.Fatalf("force copyAgentctx: %v", err)
	}
	if len(fres.CrownJewelSkipped) != 0 {
		t.Errorf("with Force, CrownJewelSkipped = %v, want none", fres.CrownJewelSkipped)
	}
	if got := readFile(t, pdir, "resume.md"); got != "# resume\nbody\n" {
		t.Errorf("resume.md after force = %q, want source content", got)
	}
}

func TestCopyAgentctx_DryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	projDir := writeSourceAgentctx(t, root, "src-proj")
	dest := storage.NewVault(t.TempDir())

	res, err := copyAgentctx(dest, projDir, "src-proj", ImportOptions{WithAgentctx: true, DryRun: true})
	if err != nil {
		t.Fatalf("copyAgentctx: %v", err)
	}
	if res.Copied == 0 {
		t.Fatal("dry run should report planned copies")
	}
	// Nothing on disk.
	if _, err := os.Stat(filepath.Join(dest.Root, "Projects")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote to dest (err=%v)", err)
	}
	// Reported paths are vault-relative.
	for _, p := range res.CopiedPaths {
		if filepath.IsAbs(p) || !strings.HasPrefix(p, "Projects"+string(filepath.Separator)) {
			t.Errorf("CopiedPaths entry %q is not a vault-relative Projects/ path", p)
		}
	}
}

func TestCopyAgentctx_NoAgentctxDir(t *testing.T) {
	root := t.TempDir()
	// A project with sessions but no agentctx/ and no knowledge.md.
	projDir := filepath.Join(root, "Projects", "bare")
	if err := os.MkdirAll(filepath.Join(projDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := storage.NewVault(t.TempDir())
	res, err := copyAgentctx(dest, projDir, "bare", ImportOptions{WithAgentctx: true})
	if err != nil {
		t.Fatalf("copyAgentctx: %v", err)
	}
	if res.Copied != 0 || res.Skipped != 0 {
		t.Errorf("bare project: got Copied=%d Skipped=%d, want 0/0", res.Copied, res.Skipped)
	}
}

// TestImportVibeVault_AgentctxOnly proves the agentctx-only path: with
// SkipSessions, ImportVibeVault carries agentctx with NIL engine/embedder
// (no model load) and indexes no sessions.
func TestImportVibeVault_AgentctxOnly(t *testing.T) {
	root := t.TempDir()
	writeSourceAgentctx(t, root, "src-proj")
	// Add a session that must NOT be indexed when SkipSessions is set.
	sessDir := filepath.Join(root, "Projects", "src-proj", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "s1.md"), []byte(testSession1), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(root)
	dest := storage.NewVault(t.TempDir())

	res, err := ImportVibeVault(context.Background(), vault, dest, nil, nil, storage.Config{}, ImportOptions{
		WithAgentctx: true,
		SkipSessions: true,
	})
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}
	if res.SessionsImported != 0 {
		t.Errorf("SessionsImported = %d, want 0 (SkipSessions)", res.SessionsImported)
	}
	if res.Agentctx.Copied == 0 {
		t.Fatal("expected agentctx files to be copied")
	}
	if got := readFile(t, dest.Root, "Projects", "src-proj", "iterations.md"); got != "# iterations\nlong history\n" {
		t.Errorf("iterations.md = %q, want source content", got)
	}
}

// TestImportVibeVault_OnlyProjects proves the scope filter: with two source
// projects, OnlyProjects restricts the run to one, leaving the other's dest
// dir untouched (so a targeted retirement does not fan out across a shared
// source vault).
func TestImportVibeVault_OnlyProjects(t *testing.T) {
	root := t.TempDir()
	writeSourceAgentctx(t, root, "keep-me")
	writeSourceAgentctx(t, root, "leave-me")

	vault := storage.NewVault(root)
	dest := storage.NewVault(t.TempDir())

	res, err := ImportVibeVault(context.Background(), vault, dest, nil, nil, storage.Config{}, ImportOptions{
		WithAgentctx: true,
		SkipSessions: true,
		OnlyProjects: []string{"keep-me"},
	})
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}
	if res.ProjectsScanned != 1 {
		t.Errorf("ProjectsScanned = %d, want 1", res.ProjectsScanned)
	}
	if _, err := os.Stat(filepath.Join(dest.Root, "Projects", "keep-me", "resume.md")); err != nil {
		t.Errorf("keep-me/resume.md should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest.Root, "Projects", "leave-me")); !os.IsNotExist(err) {
		t.Errorf("leave-me should NOT have been processed (err=%v)", err)
	}
}

func contains(s []string, want string) bool {
	i := sort.SearchStrings(s, want)
	return i < len(s) && s[i] == want
}
