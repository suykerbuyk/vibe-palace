// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/skills"
)

// TargetKind identifies one of the three surfaces vibe-palace emits shims
// into. Each kind has its own directory, filename shape, and render body,
// but all three share the managed-hash atomic-write protocol implemented
// in this package.
type TargetKind int

const (
	// ClaudeCommand emits one markdown file per vibe-palace command into
	// .claude/commands/ (prefix "vpc-"). This is the original target, the
	// one that powers /vpc-<name> slash-command surfacing.
	ClaudeCommand TargetKind = iota
	// ClaudeSkill emits one directory per skill under .claude/skills/ —
	// specifically .claude/skills/vps-<name>/SKILL.md. Body delegates to
	// the vp_skill MCP tool and teaches the additive persona contract.
	ClaudeSkill
	// CursorRule emits one .mdc file per skill under .cursor/rules/. Body
	// calls vp_skill with a vault-path fallback so Cursor surfaces without
	// MCP still resolve the persona.
	CursorRule
)

// String gives each kind a short stable name for logs and tests.
func (k TargetKind) String() string {
	switch k {
	case ClaudeCommand:
		return "claude-command"
	case ClaudeSkill:
		return "claude-skill"
	case CursorRule:
		return "cursor-rule"
	default:
		return fmt.Sprintf("target(%d)", int(k))
	}
}

// skillShimVersion bumps when the rendered ClaudeSkill/CursorRule schema
// changes in a way we want to force-refresh existing files. Mirrors the
// per-target-version design of shimVersion for ClaudeCommand, kept in a
// separate constant so command-shim version bumps don't invalidate skill
// shims and vice versa.
const skillShimVersion = 1

// Per-target filename and directory constants. Split from the top-level
// FilePrefix/ShimDir (which are ClaudeCommand-specific by history) so
// callers can switch on TargetKind without pulling in the global state.
const (
	// SkillFilePrefix prefixes both the skill directory under
	// .claude/skills/ and the .mdc under .cursor/rules/.
	SkillFilePrefix = "vps-"
	// ClaudeSkillsDir is the project-relative parent dir for
	// .claude/skills/<dir>/SKILL.md.
	ClaudeSkillsDir = ".claude/skills"
	// CursorRulesDir is the project-relative dir for .cursor/rules/<file>.mdc.
	CursorRulesDir = ".cursor/rules"
)

// SkillDirName returns the directory name (under .claude/skills/) that
// hosts the shim for a given skill — e.g. SkillDirName("pairing") →
// "vps-pairing".
func SkillDirName(name string) string { return SkillFilePrefix + name }

// CursorRuleFilename returns the .mdc filename for a given skill.
func CursorRuleFilename(name string) string { return SkillFilePrefix + name + ".mdc" }

// SkillItem is the Plan/Apply input for ClaudeSkill and CursorRule
// targets. Name drives the filename and persona argument; Frontmatter
// carries description/triggers/paths (parsed by internal/skills.Parse
// via context.ResolveSkillDir).
type SkillItem struct {
	Name        string
	Frontmatter skills.SkillFrontmatter
	// VaultPath is the filesystem path where the SKILL.md is found in
	// the vault (used for CursorRule's vault-fetch fallback). Empty
	// string means no vault fallback will be rendered.
	VaultPath string
}

// TargetDir returns the absolute directory where shims of this kind live
// for the given project root and skill name. For ClaudeCommand and
// CursorRule this is a flat directory; for ClaudeSkill each skill gets
// its own subdirectory so references/ siblings can live alongside
// SKILL.md if a future phase emits them.
func TargetDir(kind TargetKind, projectRoot, name string) string {
	switch kind {
	case ClaudeCommand:
		return filepath.Join(projectRoot, ShimDir)
	case ClaudeSkill:
		return filepath.Join(projectRoot, ClaudeSkillsDir, SkillDirName(name))
	case CursorRule:
		return filepath.Join(projectRoot, CursorRulesDir)
	default:
		return ""
	}
}

// TargetFile returns the absolute path of the shim file itself.
func TargetFile(kind TargetKind, projectRoot, name string) string {
	switch kind {
	case ClaudeCommand:
		return filepath.Join(projectRoot, ShimDir, Filename(name))
	case ClaudeSkill:
		return filepath.Join(TargetDir(kind, projectRoot, name), "SKILL.md")
	case CursorRule:
		return filepath.Join(projectRoot, CursorRulesDir, CursorRuleFilename(name))
	default:
		return ""
	}
}

// skillContentHash keys the skill-shim sha on every input that can change
// the rendered bytes: target kind, template version, name, description,
// triggers/paths (order-sensitive, rendered verbatim), and vault path
// for the Cursor fallback. Keeping the hash input-based (rather than
// over the rendered output) avoids the self-referential problem of
// embedding the sha into the hashed content.
func skillContentHash(kind TargetKind, item SkillItem) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00v=%d\x00name=%s\x00desc=%s\x00lifetime=%s",
		kind.String(), skillShimVersion, item.Name,
		item.Frontmatter.Description, item.Frontmatter.Lifetime)
	for _, t := range item.Frontmatter.Triggers {
		fmt.Fprintf(h, "\x00trig=%s", t)
	}
	for _, p := range item.Frontmatter.Paths {
		fmt.Fprintf(h, "\x00path=%s", p)
	}
	if kind == CursorRule {
		fmt.Fprintf(h, "\x00vault=%s", item.VaultPath)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:7]
}

// ExpectedSkillSha returns the sha a freshly rendered skill shim would
// carry for the given target and input. Callers comparing an on-disk
// shim's extracted sha to the current expected value use this.
func ExpectedSkillSha(kind TargetKind, item SkillItem) string {
	return skillContentHash(kind, item)
}

// RenderSkill returns the file body for a ClaudeSkill or CursorRule
// shim. Output uses LF line endings and is deterministic for a given
// (kind, item, template version) triple.
func RenderSkill(kind TargetKind, item SkillItem) string {
	switch kind {
	case ClaudeSkill:
		return renderClaudeSkill(item)
	case CursorRule:
		return renderCursorRule(item)
	default:
		return ""
	}
}

func renderClaudeSkill(item SkillItem) string {
	sha := skillContentHash(ClaudeSkill, item)
	openMarker := fmt.Sprintf(shimOpenFmt, skillShimVersion, sha)
	shimName := SkillDirName(item.Name)
	desc := sanitizeFrontmatter(item.Frontmatter.Description)
	if desc == "" {
		desc = "Vibe-palace skill persona — " + item.Name
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: ")
	sb.WriteString(shimName)
	sb.WriteString("\n")
	sb.WriteString("description: ")
	sb.WriteString(desc)
	sb.WriteString("\n")
	sb.WriteString("---\n\n")
	sb.WriteString(openMarker)
	sb.WriteString("\n")
	sb.WriteString("Call `vp_skill` with `name=\"")
	sb.WriteString(item.Name)
	sb.WriteString("\"` and adopt the returned persona as standing instruction\n")
	sb.WriteString("for the rest of this session. Multiple vps-* invocations stack\n")
	sb.WriteString("additively; `vps-clear` drops all.\n")
	sb.WriteString(shimCloseDelim)
	sb.WriteString("\n")
	return sb.String()
}

func renderCursorRule(item SkillItem) string {
	sha := skillContentHash(CursorRule, item)
	openMarker := fmt.Sprintf(shimOpenFmt, skillShimVersion, sha)
	desc := sanitizeFrontmatter(item.Frontmatter.Description)
	if desc == "" {
		desc = "Vibe-palace skill persona — " + item.Name
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("description: ")
	sb.WriteString(desc)
	sb.WriteString("\n")
	sb.WriteString("globs: ")
	sb.WriteString(renderGlobsYAMLFlow(item.Frontmatter.Paths))
	sb.WriteString("\n")
	sb.WriteString("alwaysApply: false\n")
	sb.WriteString("---\n\n")
	sb.WriteString(openMarker)
	sb.WriteString("\n")
	sb.WriteString("Call `vp_skill` with `name=\"")
	sb.WriteString(item.Name)
	sb.WriteString("\"` and adopt the returned persona as standing instruction\n")
	sb.WriteString("for the rest of this session. Multiple vps-* invocations stack\n")
	sb.WriteString("additively; `vps-clear` drops all.\n\n")
	sb.WriteString("If MCP tools are not available in this session, read\n")
	if item.VaultPath != "" {
		sb.WriteString("`")
		sb.WriteString(item.VaultPath)
		sb.WriteString("`\n")
	} else {
		sb.WriteString("`{vault}/Templates/skills/")
		sb.WriteString(item.Name)
		sb.WriteString("/SKILL.md`\n")
	}
	sb.WriteString("directly and adopt the persona manually.\n")
	sb.WriteString(shimCloseDelim)
	sb.WriteString("\n")
	return sb.String()
}

// renderGlobsYAMLFlow renders a []string as a YAML flow-sequence
// ("[a, b]"). Empty slice → "[]". Each entry is quoted with double
// quotes and backslash-escaped for safety against glob characters that
// YAML parsers sometimes choke on in bare scalars.
func renderGlobsYAMLFlow(paths []string) string {
	if len(paths) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i, p := range paths {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("\"")
		sb.WriteString(strings.ReplaceAll(strings.ReplaceAll(p, "\\", "\\\\"), "\"", "\\\""))
		sb.WriteString("\"")
	}
	sb.WriteString("]")
	return sb.String()
}
