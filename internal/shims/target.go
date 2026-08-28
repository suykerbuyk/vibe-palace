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
	// GrokSkill emits one directory per skill under .grok/skills/ —
	// .grok/skills/vps-<name>/SKILL.md — mirroring ClaudeSkill, for xAI's
	// Grok Build CLI (Claude-Code-compatible, reads .grok/skills/<n>/SKILL.md).
	// The reserved name "vpc" renders the single command hub at
	// .grok/skills/vpc/SKILL.md instead of a vps-* persona shim.
	GrokSkill
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
	case GrokSkill:
		return "grok-skill"
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
	// GrokSkillsDir is the project-relative parent dir for
	// .grok/skills/<dir>/SKILL.md (subdir-per-skill, like ClaudeSkill).
	GrokSkillsDir = ".grok/skills"
	// GrokHubName is the reserved skill name whose shim is the single
	// /vpc command hub at .grok/skills/vpc/SKILL.md (literal dir "vpc", no
	// vps- prefix), rather than a per-persona vps-* shim.
	GrokHubName = "vpc"
)

// SkillDirName returns the directory name (under .claude/skills/) that
// hosts the shim for a given skill — e.g. SkillDirName("pairing") →
// "vps-pairing".
func SkillDirName(name string) string { return SkillFilePrefix + name }

// CursorRuleFilename returns the .mdc filename for a given skill.
func CursorRuleFilename(name string) string { return SkillFilePrefix + name + ".mdc" }

// SkillItem is the Plan/Apply input for ClaudeSkill and CursorRule
// targets. Name drives the filename and persona argument; Frontmatter
// carries description/paths (parsed by internal/skills.Parse via
// context.ResolveSkillDir). Paths is Cursor-only — it renders into the
// Cursor rule's globs line; a path-less skill renders `globs: []` and
// still activates by description.
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
	f := TargetFile(kind, projectRoot, name)
	if f == "" {
		return ""
	}
	return filepath.Dir(f)
}

// TargetFile returns the absolute path of the shim file itself for a
// project-rooted layout. User-global plugin trees use skillFilePath /
// PlanCommandsAt with an absolute CommandsDir/SkillsDir instead.
func TargetFile(kind TargetKind, projectRoot, name string) string {
	switch kind {
	case ClaudeCommand:
		return filepath.Join(projectRoot, ShimDir, Filename(name))
	case ClaudeSkill:
		return skillFilePath(filepath.Join(projectRoot, ClaudeSkillsDir), ClaudeSkill, name)
	case CursorRule:
		return skillFilePath(filepath.Join(projectRoot, CursorRulesDir), CursorRule, name)
	case GrokSkill:
		return skillFilePath(filepath.Join(projectRoot, GrokSkillsDir), GrokSkill, name)
	default:
		return ""
	}
}

// skillContentHash keys the skill-shim sha on every input that can change
// the rendered bytes: target kind, template version, name, description,
// paths (order-sensitive, rendered verbatim into the Cursor globs line),
// and vault path for the Cursor fallback. Keeping the hash input-based
// (rather than over the rendered output) avoids the self-referential
// problem of embedding the sha into the hashed content.
func skillContentHash(kind TargetKind, item SkillItem) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00v=%d\x00name=%s\x00desc=%s\x00lifetime=%s",
		kind.String(), skillShimVersion, item.Name,
		item.Frontmatter.Description, item.Frontmatter.Lifetime)
	for _, p := range item.Frontmatter.Paths {
		fmt.Fprintf(h, "\x00path=%s", p)
	}
	if kind == CursorRule || kind == GrokSkill {
		// Both CursorRule and GrokSkill render a vault-path fallback whose
		// text is part of the file bytes, so the vault path keys the hash.
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
	case GrokSkill:
		if item.Name == GrokHubName {
			return renderGrokHub(item)
		}
		return renderGrokSkill(item)
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

// grokHubDescription is the single-line description carried in the /vpc
// hub frontmatter and hashed into the hub shim's sha. Kept as one constant
// so the renderer and PlanGrokHub agree on the exact bytes.
const grokHubDescription = "Vibe-palace command hub. Naked /vpc lists all commands via vp_cmd {}; /vpc <cmd> <args> dispatches via vp_cmd name=<cmd>. Use for restart, wrap, review-plan, execute-plan, cancel-plan and other vibe-palace operations."

// grokHubBody is the fixed instructional body of the /vpc command hub,
// wrapped between the managed markers by renderGrokHub. It collapses every
// Claude .claude/commands/vpc-*.md shim into one argument-taking Grok skill
// and encodes the vp_get_task / no-grep task-reading discipline.
const grokHubBody = `# Vibe-Palace Command Hub (/vpc)

A single ` + "`/vpc`" + ` slash command that mirrors the Claude ` + "`.claude/commands/vpc-*.md`" + `
shims. It lists vibe-palace commands and dispatches them through the ` + "`vp_cmd`" + `
MCP tool. Grok skills take arguments, so this one skill replaces every
per-command shim.

## Tool

Use the qualified MCP tool name ` + "`vp_cmd`" + `. If it is not already available in
this session, load its schema first (search your tool list for ` + "`vp_cmd`" + `), then
call it. Never guess parameter names — use only the schema the tool exposes.

## Naked ` + "`/vpc`" + ` — list commands

Call ` + "`vp_cmd`" + ` with empty input ` + "`{}`" + `. Do NOT pass ` + "`project`" + ` — let ` + "`vp_cmd`" + `
resolve it from the working directory / ` + "`.vibe-palace.toml`" + `. Present the returned
commands (name, source, brief) to the user and offer to run one.

## ` + "`/vpc <cmd> <args>`" + ` — dispatch a command

Parse the first word of the argument as the command name (e.g. ` + "`review-plan`" + `)
and treat the rest as its arguments. Call ` + "`vp_cmd`" + ` with ` + "`name=\"<cmd>\"`" + `. Do
NOT pass ` + "`project`" + ` — ` + "`vp_cmd`" + ` resolves it, exactly as the Claude ` + "`vpc-*`" + `
shims do, which keeps this portable across projects. Then **follow the returned
instructions verbatim** — do not summarize; execute every step as written.

## Reading tasks (review-plan, cancel-plan, execute-plan)

For the task-reading commands ` + "`review-plan`" + `, ` + "`cancel-plan`" + `, and
` + "`execute-plan`" + ` the argument is a task name (e.g. ` + "`/vpc review-plan <task-name>`" + `).
BEFORE acting, call ` + "`vp_get_task`" + ` with the resolved ` + "`project`" + ` and ` + "`task`" + ` to
read the task. For a large task body, call ` + "`vp_get_task`" + ` with
` + "`include_content=false`" + ` — it drops the big inline body and returns a
` + "`content_uri`" + ` plus a short ` + "`excerpt`" + ` — then page the full body with
` + "`vp_read_resource(uri, offset, limit)`" + `, advancing ` + "`offset`" + ` by the
returned ` + "`offset+length`" + ` until ` + "`eof`" + `. Do NOT assume your client
surfaces ` + "`resources/read`" + ` to the model; ` + "`vp_read_resource`" + ` is a tool you
can always call. Task files live ONLY in the vault and are reachable solely
through the MCP task tools and these ` + "`vibe-palace://`" + ` resource URIs.
NEVER grep or scan the filesystem for task files, never fall back to stale
resume prose, and never write a task to a repo-relative
` + "`tasks/`" + ` path. If a task tool is not loaded, load its schema first, then call
it.

## Session start

When restoring context (e.g. ` + "`/vpc restart`" + `), call ` + "`vp_bootstrap_context`" + `.
What comes back is an INDEX: instruments, handles, head of queue, a session
index. ` + "`resume`" + ` and ` + "`workflow`" + ` are NOT fields of it and never arrive by
waiting — FETCH each one with ` + "`vp_read_resource`" + ` from ` + "`resume_uri`" + ` /
` + "`workflow_uri`" + `, paging by the returned ` + "`offset+length`" + ` until ` + "`eof`" + `, and
CAS-verify any later write against ` + "`resume_sha256`" + `.

Your HOST can still cut the index itself. ` + "`complete: true`" + ` is the payload's
last field and carries no ` + "`omitempty`" + `, so it arrives on every whole result and
on no cut one. If ` + "`complete`" + ` is missing, or your host printed a truncation
banner, the HOST cut the result: call ` + "`vp_bootstrap_context`" + ` again rather than
acting on the handles you can still see.

## After execution

Confirm what was done. For a review or plan, ask whether to proceed with
implementation. Update task status in the vault via the MCP task tools when
appropriate.`

// GrokHubItem is the canonical SkillItem describing the /vpc command hub.
// Both PlanGrokHub and the init/upgrade wiring use it so the hub's expected
// sha and rendered bytes never drift between plan and apply.
func GrokHubItem() SkillItem {
	return SkillItem{
		Name:        GrokHubName,
		Frontmatter: skills.SkillFrontmatter{Description: grokHubDescription},
	}
}

// renderGrokSkill renders a per-persona Grok skill shim (vps-<name>). It
// mirrors renderCursorRule structurally — vp_skill delegation plus a
// vault-path MCP-unavailable fallback — but emits Grok frontmatter
// (name/description/metadata.short-description) and omits the Claude-only
// user-invocable / argument-hint keys.
func renderGrokSkill(item SkillItem) string {
	sha := skillContentHash(GrokSkill, item)
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
	sb.WriteString("metadata:\n")
	sb.WriteString("  short-description: \"Vibe-palace skill: ")
	sb.WriteString(item.Name)
	sb.WriteString("\"\n")
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

// renderGrokHub renders the single /vpc command hub shim. The body is the
// fixed grokHubBody constant wrapped in the managed marker pair so ScanShim
// detects it and version/drift handling works exactly like the other shims.
func renderGrokHub(item SkillItem) string {
	sha := skillContentHash(GrokSkill, item)
	openMarker := fmt.Sprintf(shimOpenFmt, skillShimVersion, sha)
	desc := sanitizeFrontmatter(item.Frontmatter.Description)
	if desc == "" {
		desc = grokHubDescription
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: ")
	sb.WriteString(GrokHubName)
	sb.WriteString("\n")
	sb.WriteString("description: ")
	sb.WriteString(desc)
	sb.WriteString("\n")
	sb.WriteString("metadata:\n")
	sb.WriteString("  short-description: \"Vibe-palace command hub (/vpc)\"\n")
	sb.WriteString("---\n\n")
	sb.WriteString(openMarker)
	sb.WriteString("\n")
	sb.WriteString(grokHubBody)
	sb.WriteString("\n")
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
