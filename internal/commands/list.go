// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package commands provides the shared list/upgrade surface used by both the
// `vp commands ...` CLI and the MCP command-discovery tools. It is a thin
// library on top of internal/context's Resolver, with no process/IO side
// effects beyond reading the embed.FS and the vault filesystem.
package commands

import (
	"fmt"
	"strings"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// Summary is the discovery-level view of a command or skill. Matches the
// shape previously defined in internal/tools so both MCP responses and CLI
// output stay in sync.
type Summary struct {
	Name   string `json:"name"`
	Alias  string `json:"alias,omitempty"`
	Source string `json:"source"`
	Brief  string `json:"brief,omitempty"`
	// Project is the slug a project-scoped command resolves under (Source one
	// of "project"/"wing"/"room"). Empty for global ("vault"/"embedded")
	// resources. Shims use it to emit a project="<slug>" param so the MCP
	// server resolves the project tier without cwd context.
	Project string `json:"project,omitempty"`
	// ArgHint is the command's argument-hint frontmatter value, when present.
	// Surfaced into the slash-command shim so domain commands keep their
	// specific hints. Commands only; empty for skills.
	ArgHint string `json:"arg_hint,omitempty"`
}

// Alias returns the "vpc-<name>" trigger token for a command.
func Alias(name string) string {
	return "vpc-" + name
}

// SkillAlias returns the "vps-<name>" trigger token for a skill.
func SkillAlias(name string) string {
	return "vps-" + name
}

// List returns Summaries for the given resource type ("command" or "skill"),
// resolved with 5-tier precedence. The Brief is extracted from the content
// at the winning tier and truncated to briefLen characters.
func List(resolver *vpctx.Resolver, resourceType, project, wing, room string, briefLen int) ([]Summary, error) {
	infos, err := resolver.ListResourcesScoped(resourceType, project, wing, room)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(infos))
	for _, ri := range infos {
		s := Summary{Name: ri.Name, Source: ri.Source}
		if resourceType == "command" {
			s.Alias = Alias(ri.Name)
		}
		if project != "" && isProjectScoped(ri.Source) {
			s.Project = project
		}
		content, _, err := resolver.ResolveScoped(
			fmt.Sprintf("%s:%s", resourceType, ri.Name), project, wing, room,
		)
		if err == nil {
			// Commands may carry argument-hint in a leading YAML fence.
			// ExtractBrief itself skips a well-formed fence, so the brief
			// never becomes "---" for skills or for frontmatter commands.
			if resourceType == "command" {
				fm, _ := parseFrontmatter(content)
				s.ArgHint = strings.TrimSpace(fm["argument-hint"])
			}
			s.Brief = ExtractBrief(content, briefLen)
		}
		out = append(out, s)
	}
	return out, nil
}

// isProjectScoped reports whether a resolver source tier is project-scoped
// (and therefore needs a project="<slug>" param to resolve), as opposed to the
// global "vault"/"embedded" tiers.
func isProjectScoped(source string) bool {
	switch source {
	case "project", "wing", "room":
		return true
	default:
		return false
	}
}

// parseFrontmatter splits an optional leading YAML frontmatter fence
// ("---\n…\n---\n") from the body. It returns a flat key→value map of the
// frontmatter's top-level "key: value" scalar lines and the remaining body.
// When no well-formed fence is present the map is empty and body is the
// original content unchanged. Only the minimal single-line scalar subset
// needed for shim metadata is parsed; nested YAML is ignored.
func parseFrontmatter(content string) (map[string]string, string) {
	fm := map[string]string{}
	if !strings.HasPrefix(content, "---\n") {
		return fm, content
	}
	lines := strings.Split(content, "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		// No closing fence — not frontmatter; leave content intact.
		return fm, content
	}
	for i := 1; i < end; i++ {
		line := strings.TrimRight(lines[i], "\r")
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fm[key] = val
	}
	body := strings.Join(lines[end+1:], "\n")
	return fm, strings.TrimLeft(body, "\n")
}

// ExtractBrief returns the first non-blank, non-heading line from content,
// truncated to maxLen characters. A well-formed leading YAML frontmatter
// block is skipped first — otherwise every SKILL.md (and every command that
// carries argument-hint) briefs as the opening fence, "---". An unclosed
// fence is left intact, matching parseFrontmatter. When truncation is
// needed the cut snaps to the last whitespace before maxLen (unless that
// would leave less than half of maxLen, in which case a mid-word cut is
// accepted) and an ellipsis is appended so the truncation is visible.
func ExtractBrief(content string, maxLen int) string {
	_, content = parseFrontmatter(content)
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > maxLen {
			cut := strings.LastIndex(line[:maxLen], " ")
			if cut <= maxLen/2 {
				cut = maxLen
			}
			return strings.TrimRight(line[:cut], " ") + "…"
		}
		return line
	}
	return "(no description)"
}
