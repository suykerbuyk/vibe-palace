// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
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
	// the vp_skill MCP tool, teaches the additive persona contract, and
	// carries the skillFallback `vp skills show` fallback, gated on vp_skill
	// being unloadable.
	ClaudeSkill
	// CursorRule emits one .mdc file per skill under .cursor/rules/. Body
	// calls vp_skill, with a `vp skills show` fallback gated on vp_skill being
	// unloadable, so a Cursor session without MCP still resolves the persona.
	// vp registers no MCP server with Cursor, so there the fallback is the
	// common path.
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

// skillShimVersion is the v= in every skill shim's marker, the version of the
// marker format and the ScanShim contract the skill planners gate on. It is
// NOT how a text change reaches existing shims: the skill-shim sha is taken
// over the rendered bytes (skillContentHash), so any edit to what a renderer
// writes re-keys every affected shim by itself. Bump this only if the marker
// format or the ScanShim contract changes. It is separate from shimVersion,
// so a command-shim bump never invalidates skill shims and vice versa.
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

// SkillItem is the Plan/Apply input for the ClaudeSkill, CursorRule and
// GrokSkill targets. Name drives the filename, the persona argument and the
// `vp skills show <name>` fallback; Frontmatter carries description/paths
// (parsed by internal/skills.Parse via context.ResolveSkillDir). The
// description reaches a persona shim only as the short label skillLabel
// derives from it, never as trigger text: a persona is meant to be adopted
// when the user invokes it by name (`/vps-<name>` or a typed `vps-<name>`).
// Only the Claude skill enforces that (skillLabel says how far the others
// go). Paths is Cursor-only — it renders into the Cursor rule's globs line,
// which Cursor auto-attaches on a file match; a path-less skill renders
// `globs: []`.
//
// No host path is part of an item: a shim never names a vault file, so the
// rendered bytes do not depend on where this host's vault lives.
type SkillItem struct {
	Name        string
	Frontmatter skills.SkillFrontmatter
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

// skillContentHash is the skill-shim sha: the first 7 hex characters of
// sha256 over "kind=<kind>\x00" followed by the file exactly as rendered with
// the marker's sha left blank ("<!-- vibe-palace:shim v=1 sha= -->").
//
// Taking the sha over the rendered bytes means every byte a renderer writes
// keys it — the frontmatter, the vp_skill delegation, the skillFallback text,
// the Grok hub body and the marker's v= — so an edit to any of them reaches
// every existing shim with no version bump and no rule to remember. Inputs a
// renderer does not write (a skill's lifetime, or Paths outside the Cursor
// rule) do not key it. Blanking the sha removes the self-reference, and
// rendering never calls this function, so there is no recursion.
//
// An independent check needs only a rendered file: blank its marker sha with
// markerRegexp, prefix "kind=<kind>\x00", hash, and compare.
func skillContentHash(kind TargetKind, item SkillItem) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00", kind.String())
	h.Write([]byte(renderSkillWithSha(kind, item, "")))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:7]
}

// ExpectedSkillSha returns the sha a freshly rendered skill shim would
// carry for the given target and input. Callers comparing an on-disk
// shim's extracted sha to the current expected value use this.
func ExpectedSkillSha(kind TargetKind, item SkillItem) string {
	return skillContentHash(kind, item)
}

// RenderSkill returns the file body for a ClaudeSkill, CursorRule or
// GrokSkill shim, carrying its skillContentHash in the marker. Output uses LF
// line endings and is deterministic for a given (kind, item) pair and binary.
func RenderSkill(kind TargetKind, item SkillItem) string {
	return renderSkillWithSha(kind, item, skillContentHash(kind, item))
}

// renderSkillWithSha renders a skill shim with the given sha in its marker.
// skillContentHash calls it with "" to get the bytes it hashes.
func renderSkillWithSha(kind TargetKind, item SkillItem, sha string) string {
	switch kind {
	case ClaudeSkill:
		return renderClaudeSkill(item, sha)
	case CursorRule:
		return renderCursorRule(item, sha)
	case GrokSkill:
		if item.Name == GrokHubName {
			return renderGrokHub(item, sha)
		}
		return renderGrokSkill(item, sha)
	default:
		return ""
	}
}

// skillFallback is the MCP-less fallback every persona shim (ClaudeSkill,
// CursorRule, persona GrokSkill) writes after its vp_skill delegation — the
// single source of that text.
//
// It names a command, never a file: under override-only Templates/ no file on
// the host holds a built-in skill body, and `vp skills show` serves the same
// resolver vp_skill does, defaulting the project from the working directory.
// Its trigger requires the agent to search its deferred or MCP tools for
// vp_skill first, mirroring the Grok hub's "load its schema first" guard, so a
// host that defers MCP tools (Claude Code, Grok) loads vp_skill instead of
// shelling out. The paste clause covers a host with no shell tool, an
// ask-only mode, or a workspace where vp is not installed.
func skillFallback(name string) string {
	return "If the `vp_skill` tool is not in your tool list and cannot be loaded\n" +
		"(search your deferred or MCP tools for `vp_skill` first), run\n" +
		"`vp skills show " + name + "` from the project directory and adopt the\n" +
		"printed persona manually. It lists the skill's references; print one\n" +
		"with `vp skills show " + name + " --section <ref>`. If you cannot run `vp`,\n" +
		"ask the user to run that command and paste its output.\n"
}

// skillLabelPrefix opens every persona shim's description, in the shape of
// the command shim's "Vibe-palace command — <brief>" (Render).
const skillLabelPrefix = "Vibe-palace skill — "

// skillLabel is a persona shim's description: skillLabelPrefix plus the
// skill's description briefed exactly as a command's content is
// (commands.ExtractBrief, the 60 bytes commands.List uses for shim briefs).
// It is a label for a menu, never the skill's trigger text. On Claude Code
// the shim also sets disable-model-invocation, so the model cannot adopt
// the persona itself. On Cursor and Grok the label is the only safeguard:
// it makes a match against the conversation unlikely but does not rule it
// out, and a Cursor rule's globs (from paths) still auto-attach. A
// description that briefs to nothing usable falls back to the skill name.
func skillLabel(item SkillItem) string {
	brief := commands.ExtractBrief(sanitizeFrontmatter(item.Frontmatter.Description), 60)
	if brief == "(no description)" {
		brief = item.Name
	}
	return skillLabelPrefix + brief
}

// yamlDoubleQuoted renders s as a YAML double-quoted scalar. A bare scalar
// cannot hold ": " (chair's and pair-reviewer's labels do), and a document
// that fails to parse puts every key beside it at the mercy of the host's
// fallback — disable-model-invocation included. Quoting keeps the label's
// text as written, as the source SKILL.md files do. s is single-line
// (sanitizeFrontmatter), so only the backslash and the quote need escaping.
func yamlDoubleQuoted(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// renderClaudeSkill also writes disable-model-invocation: true. Claude Code
// then keeps the description out of the model's context and refuses a
// model-initiated Skill call, while the user's /vps-<name> still works: on
// Claude Code the persona is only ever adopted deliberately. A typed
// `vps-<name>` never used the Skill tool — the agent-file block routes it to
// vp_skill over MCP.
func renderClaudeSkill(item SkillItem, sha string) string {
	openMarker := fmt.Sprintf(shimOpenFmt, skillShimVersion, sha)
	shimName := SkillDirName(item.Name)

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: ")
	sb.WriteString(shimName)
	sb.WriteString("\n")
	sb.WriteString("description: ")
	sb.WriteString(yamlDoubleQuoted(skillLabel(item)))
	sb.WriteString("\n")
	sb.WriteString("disable-model-invocation: true\n")
	sb.WriteString("---\n\n")
	sb.WriteString(openMarker)
	sb.WriteString("\n")
	sb.WriteString("Call `vp_skill` with `name=\"")
	sb.WriteString(item.Name)
	sb.WriteString("\"` and adopt the returned persona as standing instruction\n")
	sb.WriteString("for the rest of this session. Multiple vps-* invocations stack\n")
	sb.WriteString("additively; `vps-clear` drops all.\n\n")
	sb.WriteString(skillFallback(item.Name))
	sb.WriteString(shimCloseDelim)
	sb.WriteString("\n")
	return sb.String()
}

func renderCursorRule(item SkillItem, sha string) string {
	openMarker := fmt.Sprintf(shimOpenFmt, skillShimVersion, sha)

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("description: ")
	sb.WriteString(yamlDoubleQuoted(skillLabel(item)))
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
	sb.WriteString(skillFallback(item.Name))
	sb.WriteString(shimCloseDelim)
	sb.WriteString("\n")
	return sb.String()
}

// grokHubDescription is the single-line description carried in the /vpc
// hub frontmatter. Kept as one constant so the renderer and PlanGrokHub agree
// on the exact bytes.
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
// mirrors renderCursorRule structurally — vp_skill delegation plus the
// skillFallback `vp skills show` fallback — but emits Grok frontmatter
// (name/description/metadata.short-description) and omits the Claude-only
// disable-model-invocation key, which Grok is not known to honour.
func renderGrokSkill(item SkillItem, sha string) string {
	openMarker := fmt.Sprintf(shimOpenFmt, skillShimVersion, sha)
	shimName := SkillDirName(item.Name)

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: ")
	sb.WriteString(shimName)
	sb.WriteString("\n")
	sb.WriteString("description: ")
	sb.WriteString(yamlDoubleQuoted(skillLabel(item)))
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
	sb.WriteString(skillFallback(item.Name))
	sb.WriteString(shimCloseDelim)
	sb.WriteString("\n")
	return sb.String()
}

// renderGrokHub renders the single /vpc command hub shim. The body is the
// fixed grokHubBody constant wrapped in the managed marker pair so ScanShim
// detects it and version/drift handling works exactly like the other shims.
// The hub carries no skillFallback: there is no `vp commands show` for it to
// name.
func renderGrokHub(item SkillItem, sha string) string {
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
// ("[a, b]"). Empty slice → "[]". Each entry is a yamlDoubleQuoted scalar,
// for safety against glob characters that YAML parsers sometimes choke on
// in bare scalars.
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
		sb.WriteString(yamlDoubleQuoted(p))
	}
	sb.WriteString("]")
	return sb.String()
}
