// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestSkillCmdToolFrameStructure verifies vp_skill on the embedded
// startup-analyst skill returns the expected frame: ACTIVATE SKILL
// header, stripped SKILL.md body (no frontmatter fences), and the
// deduplicated/sorted references list.
func TestSkillCmdToolFrameStructure(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := SkillCmdTool(resolver)

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"startup-analyst"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	s, ok := res.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", res)
	}
	if !strings.Contains(s, "=== ACTIVATE SKILL: startup-analyst ===") {
		t.Error("missing frame header")
	}
	if strings.Contains(s, "---\nname: startup-analyst") {
		t.Error("frontmatter should be stripped from inlined SKILL.md body")
	}
	if !strings.Contains(s, "References (fetch on demand via vp_get_skill_section):") {
		t.Error("missing references header")
	}
	// All 5 embedded references must appear, in sorted order.
	wantRefs := []string{
		"capex-opex", "competitive-landscape", "funding-sources",
		"reality-validation", "strategic-partnerships",
	}
	for _, r := range wantRefs {
		if !strings.Contains(s, "  - "+r+"\n") {
			t.Errorf("missing reference bullet for %q", r)
		}
	}
	// Verify alphabetical order by comparing indices.
	prev := -1
	for _, r := range wantRefs {
		idx := strings.Index(s, "  - "+r+"\n")
		if idx <= prev {
			t.Errorf("reference %q out of order (idx %d, prev %d)", r, idx, prev)
		}
		prev = idx
	}
}

// TestSkillCmdToolMissingSkillErrors asserts ResolveSkillDir's
// "not found" error is surfaced verbatim.
func TestSkillCmdToolMissingSkillErrors(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := SkillCmdTool(resolver)

	_, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"nope-skill"}`))
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want containing 'not found'", err.Error())
	}
}

// TestSkillCmdToolZeroReferences confirms that a skill with no
// references/*.md files gets a frame with NO trailing references
// block. A vault-tier SKILL.md without a siblings directory is the
// cleanest way to isolate this in a test.
func TestSkillCmdToolZeroReferences(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Templates/skills/bare/SKILL.md", "# Bare\n\nNo refs skill body.")

	tool := SkillCmdTool(resolver)
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"bare"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	s := res.(string)
	if strings.Contains(s, "References (fetch on demand") {
		t.Error("empty references should not produce a references section")
	}
	if !strings.Contains(s, "No refs skill body.") {
		t.Error("missing SKILL.md body")
	}
}

// --- vp_get_skill_section ---

// TestGetSkillSectionHappyPath fetches an embedded reference and
// asserts content + source are correct.
func TestGetSkillSectionHappyPath(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := GetSkillSectionTool(resolver)

	res, err := tool.Handler(context.Background(),
		json.RawMessage(`{"name":"startup-analyst","section":"capex-opex"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r, ok := res.(skillSectionResult)
	if !ok {
		t.Fatalf("result type = %T, want skillSectionResult", res)
	}
	if r.Source != "embedded" {
		t.Errorf("source = %q, want embedded", r.Source)
	}
	if len(r.Content) == 0 {
		t.Error("expected non-empty content")
	}
}

// TestGetSkillSectionMissingSkill and MissingSection exercise the two
// failure modes; both must surface an actionable error.
func TestGetSkillSectionMissingSkill(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := GetSkillSectionTool(resolver)

	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{"name":"nope","section":"foo"}`))
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestGetSkillSectionMissingSection(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := GetSkillSectionTool(resolver)

	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{"name":"startup-analyst","section":"not-real"}`))
	if err == nil {
		t.Fatal("expected error for missing section")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
	if !strings.Contains(err.Error(), "vp_skill") {
		t.Errorf("error should point caller at vp_skill for discovery, got %q", err.Error())
	}
}

// TestGetSkillSectionSchemaRejectsMissingParams verifies the schema's
// required: [name, section] is actually enforced by the registry at
// dispatch time (not just documented).
func TestGetSkillSectionSchemaRejectsMissingParams(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	srv := mcp.NewServer(storage.NewVault(t.TempDir()))
	srv.Registry().MustRegister(GetSkillSectionTool(resolver))

	cases := []string{
		`{}`,
		`{"name":"startup-analyst"}`,
		`{"section":"capex-opex"}`,
	}
	for _, params := range cases {
		_, err := srv.Registry().Dispatch(
			context.Background(), "vp_get_skill_section", json.RawMessage(params),
		)
		if err == nil {
			t.Errorf("schema should reject %s", params)
			continue
		}
		var ve *mcp.ValidationError
		if !errorsAs(err, &ve) {
			t.Errorf("params %s: want ValidationError, got %T: %v", params, err, err)
		}
	}
}

// errorsAs checks whether err is (or wraps) a *mcp.ValidationError
// and stores it in target. Inlining avoids pulling the "errors"
// package for a single call.
func errorsAs(err error, target **mcp.ValidationError) bool {
	if ve, ok := err.(*mcp.ValidationError); ok {
		*target = ve
		return true
	}
	return false
}
