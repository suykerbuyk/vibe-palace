// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package context

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestResolveSkillDirEmbedded confirms the embedded startup-analyst
// seed materializes through the 5-tier resolver with parsed
// frontmatter and reference enumeration.
func TestResolveSkillDirEmbedded(t *testing.T) {
	r, _ := testResolver(t)

	sd, src, err := r.ResolveSkillDir("startup-analyst", "", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir: %v", err)
	}
	if src != "embedded" {
		t.Errorf("source = %q, want embedded", src)
	}
	if sd.Frontmatter.Name != "startup-analyst" {
		t.Errorf("frontmatter.Name = %q", sd.Frontmatter.Name)
	}
	if sd.Frontmatter.Lifetime != "postural" {
		t.Errorf("lifetime default = %q, want postural", sd.Frontmatter.Lifetime)
	}
	wantRefs := []string{
		"capex-opex", "competitive-landscape", "funding-sources",
		"reality-validation", "strategic-partnerships",
	}
	if !equalSorted(sd.ReferenceNames, wantRefs) {
		t.Errorf("references = %v, want %v", sd.ReferenceNames, wantRefs)
	}
	if !strings.Contains(string(sd.SkillMDBody), "Startup Business Plan Analyst") {
		t.Errorf("body missing expected heading")
	}
}

func TestResolveSkillDirNotFound(t *testing.T) {
	r, _ := testResolver(t)
	_, _, err := r.ResolveSkillDir("does-not-exist", "", "", "")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestResolveSkillDirProjectOverrideReferenceFallthrough(t *testing.T) {
	r, root := testResolver(t)

	// Project tier: override SKILL.md only — no references supplied.
	skillDir := filepath.Join(root, "Projects", "proj", "skills", "startup-analyst")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "---\nname: startup-analyst\ndescription: project-specific override\n---\nproject body\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	sd, src, err := r.ResolveSkillDir("startup-analyst", "proj", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir: %v", err)
	}
	if src != "project" {
		t.Errorf("source = %q, want project", src)
	}
	if !strings.Contains(string(sd.SkillMDBody), "project body") {
		t.Errorf("body should come from project override, got %q", sd.SkillMDBody)
	}
	// References should still be discoverable from embedded tier.
	if len(sd.ReferenceNames) != 5 {
		t.Errorf("references len = %d, want 5 (embedded fallthrough): %v",
			len(sd.ReferenceNames), sd.ReferenceNames)
	}

	// Per-file fallthrough: project has no references/capex-opex.md, so
	// ResolveSkillSection must fall back to embedded.
	data, sec, err := r.ResolveSkillSection("startup-analyst", "capex-opex", "proj", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillSection: %v", err)
	}
	if sec != "embedded" {
		t.Errorf("section source = %q, want embedded", sec)
	}
	if len(data) == 0 {
		t.Error("empty section body")
	}
}

func TestResolveSkillSectionProjectOverride(t *testing.T) {
	r, root := testResolver(t)
	p := filepath.Join(root, "Projects", "proj", "skills", "startup-analyst", "references", "capex-opex.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("custom capex"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, src, err := r.ResolveSkillSection("startup-analyst", "capex-opex", "proj", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillSection: %v", err)
	}
	if src != "project" {
		t.Errorf("source = %q, want project", src)
	}
	if string(data) != "custom capex" {
		t.Errorf("data = %q", data)
	}
}

func TestResolveSkillSectionNotFound(t *testing.T) {
	r, _ := testResolver(t)
	_, _, err := r.ResolveSkillSection("startup-analyst", "does-not-exist", "", "", "")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestResolveSkillSectionInvalidName(t *testing.T) {
	r, _ := testResolver(t)
	if _, _, err := r.ResolveSkillSection("../evil", "x", "", "", ""); err == nil {
		t.Error("expected error for traversal name")
	}
	if _, _, err := r.ResolveSkillSection("x", "../evil", "", "", ""); err == nil {
		t.Error("expected error for traversal section")
	}
}

func TestResolveSkillDirEmptyDirectory(t *testing.T) {
	// A project tier directory with neither SKILL.md nor references
	// must not be treated as providing the skill — it should fall
	// through to vault / embedded.
	r, root := testResolver(t)
	empty := filepath.Join(root, "Projects", "proj", "skills", "startup-analyst")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	sd, src, err := r.ResolveSkillDir("startup-analyst", "proj", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir: %v", err)
	}
	if src != "embedded" {
		t.Errorf("source = %q, want embedded (empty project dir should fall through)", src)
	}
	if len(sd.ReferenceNames) != 5 {
		t.Errorf("references = %d, want 5", len(sd.ReferenceNames))
	}
}

func TestResolveSkillDirWingScoping(t *testing.T) {
	r, root := testResolver(t)
	p := filepath.Join(root, "Projects", "proj", "skills", "backend", ".wing", "startup-analyst", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: startup-analyst\n---\nwing body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, src, err := r.ResolveSkillDir("startup-analyst", "proj", "backend", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir: %v", err)
	}
	if src != "wing" {
		t.Errorf("source = %q, want wing", src)
	}
}

func TestListResourcesSkillsMerged(t *testing.T) {
	r, root := testResolver(t)
	// project-tier skill (directory-form)
	writeFile(t, filepath.Join(root, "Projects", "p", "skills", "local-skill", "SKILL.md"), "body")
	// vault-tier skill
	writeFile(t, filepath.Join(root, "Templates", "skills", "vault-skill", "SKILL.md"), "body")

	resources, err := r.ListResourcesScoped("skill", "p", "", "")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	names := make([]string, 0, len(resources))
	bySource := map[string]string{}
	for _, ri := range resources {
		names = append(names, ri.Name)
		bySource[ri.Name] = ri.Source
	}
	sort.Strings(names)
	// Two synthetic skills (project + vault tiers) plus the embedded seeds.
	wantAll := []string{"chair", "code-digger", "epic-orchestrator", "local-skill", "pair-reviewer", "second-opinion", "startup-analyst", "vault-skill"}
	if !equalSorted(names, wantAll) {
		t.Errorf("names = %v, want %v", names, wantAll)
	}
	if bySource["local-skill"] != "project" {
		t.Errorf("local-skill source = %q, want project", bySource["local-skill"])
	}
	if bySource["vault-skill"] != "vault" {
		t.Errorf("vault-skill source = %q, want vault", bySource["vault-skill"])
	}
	for _, name := range []string{"chair", "code-digger", "epic-orchestrator", "pair-reviewer", "second-opinion", "startup-analyst"} {
		if bySource[name] != "embedded" {
			t.Errorf("%s source = %q, want embedded", name, bySource[name])
		}
	}
}

func TestEmbeddedContentSkill(t *testing.T) {
	r, _ := testResolver(t)
	content, err := r.EmbeddedContent("skill:startup-analyst")
	if err != nil {
		t.Fatalf("EmbeddedContent: %v", err)
	}
	if !strings.Contains(content, "name: startup-analyst") {
		t.Error("embedded skill content missing frontmatter")
	}
}

func TestResolveSkillSectionRoomAndWingTiers(t *testing.T) {
	r, root := testResolver(t)

	// Wing tier.
	wingSec := filepath.Join(root, "Projects", "proj", "skills", "backend", ".wing",
		"startup-analyst", "references", "capex-opex.md")
	if err := os.MkdirAll(filepath.Dir(wingSec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wingSec, []byte("wing capex"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, src, err := r.ResolveSkillSection("startup-analyst", "capex-opex", "proj", "backend", "")
	if err != nil {
		t.Fatalf("ResolveSkillSection wing: %v", err)
	}
	if src != "wing" || string(data) != "wing capex" {
		t.Errorf("wing section mismatch: src=%q data=%q", src, data)
	}

	// Room tier shadows wing.
	roomSec := filepath.Join(root, "Projects", "proj", "skills", "backend", "api",
		"startup-analyst", "references", "capex-opex.md")
	if err := os.MkdirAll(filepath.Dir(roomSec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(roomSec, []byte("room capex"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, src, err = r.ResolveSkillSection("startup-analyst", "capex-opex", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ResolveSkillSection room: %v", err)
	}
	if src != "room" || string(data) != "room capex" {
		t.Errorf("room section mismatch: src=%q data=%q", src, data)
	}
}

func TestResolveSkillSectionVaultOverride(t *testing.T) {
	r, root := testResolver(t)
	p := filepath.Join(root, "Templates", "skills", "startup-analyst", "references", "capex-opex.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("vault capex"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, src, err := r.ResolveSkillSection("startup-analyst", "capex-opex", "", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillSection: %v", err)
	}
	if src != "vault" || string(data) != "vault capex" {
		t.Errorf("src=%q data=%q", src, data)
	}
}

func TestResolveSkillDirRoomTier(t *testing.T) {
	r, root := testResolver(t)
	p := filepath.Join(root, "Projects", "proj", "skills", "backend", "api",
		"startup-analyst", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: startup-analyst\n---\nroom body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, src, err := r.ResolveSkillDir("startup-analyst", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ResolveSkillDir: %v", err)
	}
	if src != "room" {
		t.Errorf("source = %q, want room", src)
	}
}

func TestResolveSkillDirInvalidScope(t *testing.T) {
	r, _ := testResolver(t)
	// room without wing → invalid
	if _, _, err := r.ResolveSkillDir("startup-analyst", "proj", "", "api"); err == nil {
		t.Error("expected invalid-scope error")
	}
	// invalid name
	if _, _, err := r.ResolveSkillDir("../bad", "proj", "", ""); err == nil {
		t.Error("expected invalid-name error")
	}
}

func TestResolveSkillDirMalformedFrontmatter(t *testing.T) {
	r, root := testResolver(t)
	p := filepath.Join(root, "Templates", "skills", "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: :::::\n  - bad\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ResolveSkillDir("broken", "", "", ""); err == nil {
		t.Error("expected frontmatter parse error")
	}
}

func TestResolveSkillDirNoFrontmatter(t *testing.T) {
	// A SKILL.md without frontmatter is accepted (fields default); body
	// is returned verbatim.
	r, root := testResolver(t)
	p := filepath.Join(root, "Templates", "skills", "plain", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("just a body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sd, _, err := r.ResolveSkillDir("plain", "", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir: %v", err)
	}
	if sd.Frontmatter.Lifetime != "" && sd.Frontmatter.Lifetime != "postural" {
		t.Errorf("lifetime = %q", sd.Frontmatter.Lifetime)
	}
	if string(sd.SkillMDBody) != "just a body\n" {
		t.Errorf("body = %q", sd.SkillMDBody)
	}
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

// TestResolveSkillEmptyVaultRootIsEmbeddedOnly pins review N1: a resolver
// with no vault root must serve the embedded tier only. filepath.Join("",
// "Templates", …) is relative to the process cwd, so before the guard a
// ./Templates/skills/<name> or ./Projects/<slug>/skills/<name> in the cwd was
// served as the "vault" or "project" tier — on a host with no vault at all.
func TestResolveSkillEmptyVaultRootIsEmbeddedOnly(t *testing.T) {
	cwd := t.TempDir()
	for rel, body := range map[string]string{
		"Templates/skills/startup-analyst/SKILL.md":                       "cwd vault body\n",
		"Templates/skills/startup-analyst/references/capex-opex.md":       "cwd vault section\n",
		"Templates/skills/startup-analyst/references/cwd-only.md":         "cwd-only section\n",
		"Projects/p1/skills/startup-analyst/SKILL.md":                     "cwd project body\n",
		"Projects/p1/skills/startup-analyst/references/capex-opex.md":     "cwd project section\n",
		"Projects/p1/skills/w/.wing/startup-analyst/SKILL.md":             "cwd wing body\n",
		"Projects/p1/skills/w/r/startup-analyst/references/capex-opex.md": "cwd room section\n",
	} {
		p := filepath.Join(cwd, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	r := NewResolver("")

	for _, scope := range [][3]string{{"", "", ""}, {"p1", "", ""}, {"p1", "w", ""}, {"p1", "w", "r"}} {
		sd, src, err := r.ResolveSkillDir("startup-analyst", scope[0], scope[1], scope[2])
		if err != nil {
			t.Fatalf("%v: ResolveSkillDir: %v", scope, err)
		}
		if src != "embedded" || strings.Contains(string(sd.SkillMDBody), "cwd ") || sd.Root != "" {
			t.Errorf("%v: src=%q root=%q — an empty vault root read a cwd-relative tier", scope, src, sd.Root)
		}
		for _, n := range sd.ReferenceNames {
			if n == "cwd-only" {
				t.Errorf("%v: reference list picked up a cwd-relative reference", scope)
			}
		}

		data, src, err := r.ResolveSkillSection("startup-analyst", "capex-opex", scope[0], scope[1], scope[2])
		if err != nil {
			t.Fatalf("%v: ResolveSkillSection: %v", scope, err)
		}
		if src != "embedded" || strings.Contains(string(data), "cwd ") {
			t.Errorf("%v: section src=%q — an empty vault root read a cwd-relative tier", scope, src)
		}
	}

	if _, _, err := r.ResolveSkillSection("startup-analyst", "cwd-only", "", "", ""); err == nil {
		t.Error("a reference present only in the cwd must not resolve with no vault root")
	}
}

// TestListResourcesEmptyVaultRootIsEmbeddedOnly: a resolver with no vault root
// lists the embedded tier only. Before the guard, a ./Templates/skills/<name>
// or ./Templates/commands/<name>.md in the process cwd was listed as a vault
// resource, so the user-global installer on a host with no vault listed a
// skill it then could not resolve.
func TestListResourcesEmptyVaultRootIsEmbeddedOnly(t *testing.T) {
	cwd := t.TempDir()
	for _, rel := range []string{
		"Templates/skills/cwd-skill/SKILL.md",
		"Templates/commands/cwd-command.md",
		"Projects/p1/skills/cwd-project-skill/SKILL.md",
		"Projects/p1/commands/cwd-project-command.md",
		"Projects/p1/skills/w/.wing/cwd-wing-skill/SKILL.md",
		"Projects/p1/commands/w/r/cwd-room-command.md",
	} {
		p := filepath.Join(cwd, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("cwd\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	r := NewResolver("")

	for _, typ := range []string{"skill", "command"} {
		for _, scope := range [][3]string{{"", "", ""}, {"p1", "", ""}, {"p1", "w", ""}, {"p1", "w", "r"}} {
			list, err := r.ListResourcesScoped(typ, scope[0], scope[1], scope[2])
			if err != nil {
				t.Fatalf("%s %v: %v", typ, scope, err)
			}
			if len(list) == 0 {
				t.Fatalf("%s %v: no embedded resources listed", typ, scope)
			}
			for _, ri := range list {
				if ri.Source != "embedded" || strings.HasPrefix(ri.Name, "cwd-") {
					t.Errorf("%s %v: listed %q from %q — an empty vault root read a cwd-relative tier", typ, scope, ri.Name, ri.Source)
				}
			}
		}
	}
}
