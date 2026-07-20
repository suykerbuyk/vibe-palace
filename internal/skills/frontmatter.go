// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package skills owns directory-form persona artifacts — SKILL.md +
// references/*.md. Phase 1 of the vps-skill-artifacts-cross-ide task
// introduces the YAML frontmatter parser that reads the metadata
// header of SKILL.md and returns the persona body with the frontmatter
// block stripped. Later phases add shim rendering and CLI surface on
// top of the same package.
package skills

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SkillFrontmatter captures the parsed YAML header of a SKILL.md.
// Name and Description are the primary persona identifiers consumed
// by Claude Code and Cursor shims. Paths is an optional list of file
// globs rendered into the Cursor rule's `globs:` line so the rule can
// path-activate; it is Cursor-only — the Claude and Grok shims are
// description-driven and ignore it. A skill that declares no Paths is
// valid: its Cursor rule ships `globs: []` and activates by
// description (Cursor's agent-requested mode). Lifetime defaults to
// "postural" when omitted — transactional skills bypass the
// stack-until-cleared lifetime contract taught by the managed block
// and are treated as single-shot invocations.
type SkillFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Paths       []string `yaml:"paths"`
	Lifetime    string   `yaml:"lifetime"`
}

// ErrNoFrontmatter is returned when a SKILL.md does not begin with a
// YAML frontmatter fence. Callers may decide whether to treat that as
// fatal (directory-form skills require a frontmatter) or to fall back
// to defaults.
var ErrNoFrontmatter = errors.New("skills: no YAML frontmatter")

// Parse extracts the YAML frontmatter block from a SKILL.md body and
// returns both the parsed struct and the body with the frontmatter
// block removed. The frontmatter fence must be a line consisting of
// exactly "---" as the very first line of the input; the closing
// fence is the next "---" line. Bytes after the closing fence are
// returned as body with their leading newline consumed but subsequent
// whitespace preserved. Lifetime defaults to "postural" when empty.
func Parse(raw []byte) (SkillFrontmatter, []byte, error) {
	var fm SkillFrontmatter

	// Normalise CRLF → LF for cross-platform robustness. We only do
	// this for fence detection; body bytes are returned as-is after
	// the closing fence.
	normalised := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))

	if !bytes.HasPrefix(normalised, []byte("---\n")) && !bytes.Equal(normalised, []byte("---")) {
		return fm, raw, ErrNoFrontmatter
	}

	// Find the closing fence: a "\n---\n" or "\n---" at EOF.
	rest := normalised[len("---\n"):]
	before, after0, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return fm, raw, fmt.Errorf("skills: unterminated frontmatter fence")
	}
	yamlBlock := before
	after := after0
	// Consume the rest of the closing-fence line: either "\n" or EOF.
	switch {
	case len(after) == 0:
		// frontmatter occupies entire file, body is empty
	case after[0] == '\n':
		after = after[1:]
	default:
		return fm, raw, fmt.Errorf("skills: closing fence must be on its own line")
	}

	if err := yaml.Unmarshal(yamlBlock, &fm); err != nil {
		return SkillFrontmatter{}, raw, fmt.Errorf("skills: parse frontmatter YAML: %w", err)
	}

	if fm.Lifetime == "" {
		fm.Lifetime = "postural"
	}
	return fm, after, nil
}
