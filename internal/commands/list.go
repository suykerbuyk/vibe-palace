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
}

// Alias returns the "vpc-<name>" trigger token for a command.
func Alias(name string) string {
	return "vpc-" + name
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
		content, _, err := resolver.ResolveScoped(
			fmt.Sprintf("%s:%s", resourceType, ri.Name), project, wing, room,
		)
		if err == nil {
			s.Brief = ExtractBrief(content, briefLen)
		}
		out = append(out, s)
	}
	return out, nil
}

// ExtractBrief returns the first non-blank, non-heading line from content,
// truncated to maxLen characters. When truncation is needed the cut snaps to
// the last whitespace before maxLen (unless that would leave less than half
// of maxLen, in which case a mid-word cut is accepted) and an ellipsis is
// appended so the truncation is visible.
func ExtractBrief(content string, maxLen int) string {
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
