// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// personaKinds are the shim kinds that carry skillFallback.
var personaKinds = []TargetKind{ClaudeSkill, CursorRule, GrokSkill}

// embeddedSkillItems resolves every built-in skill into the SkillItem the
// producers build, from a resolver with no vault (embedded tier only).
func embeddedSkillItems(t *testing.T) []SkillItem {
	t.Helper()
	r := vpctx.NewResolver("")
	names, err := r.ListResourcesScoped("skill", "", "", "")
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	var items []SkillItem
	for _, ri := range names {
		sd, _, err := r.ResolveSkillDir(ri.Name, "", "", "")
		if err != nil {
			t.Fatalf("resolve %s: %v", ri.Name, err)
		}
		items = append(items, SkillItem{Name: ri.Name, Frontmatter: sd.Frontmatter})
	}
	if len(items) == 0 {
		t.Fatal("no embedded skills")
	}
	return items
}

// backtickToken matches one inline-code span.
var backtickToken = regexp.MustCompile("`([^`\n]*)`")

// absPathToken matches an inline-code span that starts with an absolute or
// home-relative path: /…, ~…, or a Windows drive (C:\ or C:/).
var absPathToken = regexp.MustCompile(`^(/|~|[A-Za-z]:[\\/])`)

// TestSkillFallbackNamesNoHostPath pins the fix itself. Every persona shim —
// ClaudeSkill, CursorRule and persona GrokSkill — must carry the fallback that
// names `vp skills show <name>`, gated on a search for vp_skill, and must name
// no host path at all. At 22f0d3f the Cursor and Grok shims said "If MCP tools
// are not available in this session, read `<vault>/Templates/skills/<name>/
// SKILL.md`", a file override-only Templates/ never creates, and the Claude
// shim had no fallback.
func TestSkillFallbackNamesNoHostPath(t *testing.T) {
	const searchClause = "search your deferred or MCP tools for `vp_skill` first"
	items := append(embeddedSkillItems(t), sampleItem())
	for _, kind := range personaKinds {
		for _, item := range items {
			out := RenderSkill(kind, item)
			where := kind.String() + "/" + item.Name
			if !strings.Contains(out, skillFallback(item.Name)) {
				t.Errorf("%s: missing the skillFallback text:\n%s", where, out)
			}
			if !strings.Contains(out, "`vp skills show "+item.Name+"`") {
				t.Errorf("%s: fallback does not name `vp skills show %s`", where, item.Name)
			}
			if !strings.Contains(out, searchClause) {
				t.Errorf("%s: fallback lacks the search-first trigger %q", where, searchClause)
			}
			for _, bad := range []string{"If MCP tools are not available", "Templates/", "{vault}"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s: still carries %q", where, bad)
				}
			}
			for _, m := range backtickToken.FindAllStringSubmatch(out, -1) {
				if absPathToken.MatchString(m[1]) {
					t.Errorf("%s: backticked absolute path %q", where, m[1])
				}
			}
		}
	}
}

// TestRenderedSkillShaVerifiesAgainstBlankedRender is the independent verifier
// of operator decision O1(a): a skill shim's sha is the first 7 hex characters
// of sha256 over "kind=<kind>\x00" plus the file as rendered with its marker
// sha blanked. It uses no renderer internals — only RenderSkill's output and
// the markerRegexp ScanShim uses — so it pins "every rendered byte keys the
// sha" rather than restating the implementation.
//
// The hub subtest is the one that fails under the rejected alternatives: a
// narrow key over the hub's description and body never covers its frontmatter
// or marker line, and a skillShimVersion bump keeps the key input-derived.
// This check is also the primitive a future on-disk tamper check would use.
func TestRenderedSkillShaVerifiesAgainstBlankedRender(t *testing.T) {
	cases := []struct {
		name, kindLabel string
		kind            TargetKind
		item            SkillItem
	}{
		{"claude-skill", "claude-skill", ClaudeSkill, sampleItem()},
		{"cursor-rule", "cursor-rule", CursorRule, sampleItem()},
		{"grok-persona", "grok-skill", GrokSkill, sampleItem()},
		{"grok-hub", "grok-skill", GrokSkill, GrokHubItem()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderSkill(tc.kind, tc.item)
			m := markerRegexp.FindStringSubmatch(out)
			if m == nil {
				t.Fatalf("no marker in:\n%s", out)
			}
			blanked := markerRegexp.ReplaceAllString(out, "<!-- vibe-palace:shim v=${1} sha= -->")
			sum := sha256.Sum256([]byte("kind=" + tc.kindLabel + "\x00" + blanked))
			if got := hex.EncodeToString(sum[:])[:7]; got != m[2] {
				t.Errorf("marker sha=%s, but the blanked render hashes to %s", m[2], got)
			}
			if ExpectedSkillSha(tc.kind, tc.item) != m[2] {
				t.Errorf("ExpectedSkillSha disagrees with the rendered marker")
			}
		})
	}
}

// TestSkillShaIgnoresUnrenderedInputs: inputs no renderer writes do not key
// the sha, so they cause no rewrite with no byte change; a rendered input
// (the description) does.
func TestSkillShaIgnoresUnrenderedInputs(t *testing.T) {
	base := sampleItem()
	lifetime := base
	lifetime.Frontmatter.Lifetime = "ephemeral"
	paths := base
	paths.Frontmatter.Paths = []string{"cmd/**"}
	desc := base
	desc.Frontmatter.Description = "A different persona"

	for _, kind := range personaKinds {
		if ExpectedSkillSha(kind, lifetime) != ExpectedSkillSha(kind, base) {
			t.Errorf("%s: a Lifetime change re-keyed the shim, but no renderer writes Lifetime", kind)
		}
		if ExpectedSkillSha(kind, desc) == ExpectedSkillSha(kind, base) {
			t.Errorf("%s: a description change did not re-key the shim", kind)
		}
	}
	for _, kind := range []TargetKind{ClaudeSkill, GrokSkill} {
		if RenderSkill(kind, paths) != RenderSkill(kind, base) {
			t.Fatalf("%s: Paths reached the rendered bytes", kind)
		}
		if ExpectedSkillSha(kind, paths) != ExpectedSkillSha(kind, base) {
			t.Errorf("%s: a Paths change re-keyed the shim, but this kind never writes Paths", kind)
		}
	}
}

// TestPlanGrokHubReRendersAStaleBody is the field regression the render-keyed
// sha fixes. Six commits edited grokHubBody after the hub shipped, but the old
// sha keyed only the hub's description, so a hub written by an older binary
// kept its old body forever: HEAD's `vp init` planned it Unchanged.
//
// testdata/grok-hub-55a8eb9.md is the real hub a binary built from 55a8eb9
// writes (sha=7b5690c, the retired "`resume` and `workflow` arrive whole"
// session-start text). Captured in a sandbox with HOME, XDG_CONFIG_HOME and
// XDG_CACHE_HOME in a scratch dir:
//
//	git archive 55a8eb9 | tar -x -C <src> && (cd <src> && go build -o <bin>/vp-55a8eb9 ./cmd/vp)
//	mkdir -p <home>/code/p/.grok && cd <home>/code/p && git init -q && echo 'module x' > go.mod
//	<bin>/vp-55a8eb9 init <home>/code/p --name capfix --vault-path <scratch>/vault --no-git
//	cp .grok/skills/vpc/SKILL.md internal/shims/testdata/grok-hub-55a8eb9.md
func TestPlanGrokHubReRendersAStaleBody(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "grok-hub-55a8eb9.md"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hub := filepath.Join(root, GrokSkillsDir, GrokHubName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(hub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hub, old, 0o644); err != nil {
		t.Fatal(err)
	}

	ch, err := PlanGrokHub(root)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Kind != Modified || ch.PrevSha != "7b5690c" {
		t.Fatalf("plan = %s (prev sha %q), want Modified from 7b5690c — a hub with a stale body must re-render", ch.Kind, ch.PrevSha)
	}
	if _, _, err := ApplySkills([]SkillChange{ch}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(hub)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != RenderSkill(GrokSkill, GrokHubItem()) {
		t.Errorf("hub after apply is not the current render:\n%s", got)
	}
	if again, _ := PlanGrokHub(root); again.Kind != UnchangedChange {
		t.Errorf("second plan = %s, want Unchanged", again.Kind)
	}
}

// TestPlanSkillsReRendersHeadEraCursorRule: a Cursor rule written before this
// fix names a dead vault path and must be re-rendered once, after which no
// Templates/ path remains.
//
// testdata/cursor-rule-chair-1f3bb62.mdc is the real vps-chair.mdc a binary
// built from 1f3bb62 writes (sha=4a98565), captured the same way as the hub
// fixture but with a Cursor layout and a short, neutral vault root that was
// removed afterwards, so the fixture carries no scratch path:
//
//	git archive 1f3bb62 | tar -x -C <src> && (cd <src> && go build -o <bin>/vp-1f3bb62 ./cmd/vp)
//	mkdir -p <home>/code/p/.cursor/rules <home>/code/p/.grok && cd <home>/code/p && git init -q && echo 'module x' > go.mod
//	<bin>/vp-1f3bb62 init <home>/code/p --name capfix --vault-path /tmp/v --no-git
//	cp .cursor/rules/vps-chair.mdc internal/shims/testdata/cursor-rule-chair-1f3bb62.mdc && rm -rf /tmp/v
func TestPlanSkillsReRendersHeadEraCursorRule(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "cursor-rule-chair-1f3bb62.mdc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "/tmp/v/Templates/skills/chair/SKILL.md") {
		t.Fatalf("fixture is not the head-era rule it claims to be:\n%s", old)
	}
	var chair SkillItem
	for _, it := range embeddedSkillItems(t) {
		if it.Name == "chair" {
			chair = it
		}
	}
	if chair.Name == "" {
		t.Fatal("no embedded chair skill")
	}
	root := t.TempDir()
	rule := filepath.Join(root, CursorRulesDir, CursorRuleFilename("chair"))
	if err := os.MkdirAll(filepath.Dir(rule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rule, old, 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := PlanSkills(CursorRule, []SkillItem{chair}, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != Modified || changes[0].PrevSha != "4a98565" {
		t.Fatalf("plan = %+v, want one Modified from 4a98565", changes)
	}
	if _, _, err := ApplySkills(changes, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(rule)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != RenderSkill(CursorRule, chair) {
		t.Errorf("rule after apply is not the current render:\n%s", got)
	}
	if strings.Contains(string(got), "Templates/") || !strings.Contains(string(got), "`vp skills show chair`") {
		t.Errorf("re-rendered rule still names a vault path, or lacks the CLI fallback:\n%s", got)
	}
}
