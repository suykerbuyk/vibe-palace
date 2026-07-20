// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package skills

import (
	"errors"
	"strings"
	"testing"
)

func TestParseHappyPath(t *testing.T) {
	// The `triggers:` key is intentionally present: SkillFrontmatter no
	// longer carries a Triggers field, so it must parse away silently
	// (yaml.v3 ignores unknown keys) without breaking the rest of the
	// frontmatter. See TestParseIgnoresTriggersKey for the focused check.
	in := []byte(`---
name: startup-analyst
description: Does analysis
triggers:
  - pitch deck
  - business plan
paths:
  - "**/*.md"
lifetime: postural
---

# Body

Persona text.
`)
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Name != "startup-analyst" {
		t.Errorf("Name=%q", fm.Name)
	}
	if fm.Description != "Does analysis" {
		t.Errorf("Description=%q", fm.Description)
	}
	if len(fm.Paths) != 1 || fm.Paths[0] != "**/*.md" {
		t.Errorf("Paths=%v", fm.Paths)
	}
	if fm.Lifetime != "postural" {
		t.Errorf("Lifetime=%q", fm.Lifetime)
	}
	if !strings.HasPrefix(string(body), "\n# Body\n") {
		t.Errorf("body missing expected prefix, got %q", string(body[:min(len(body), 40)]))
	}
}

func TestParseMissingFields(t *testing.T) {
	in := []byte("---\nname: x\n---\nhello\n")
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Name != "x" {
		t.Errorf("Name=%q", fm.Name)
	}
	if fm.Description != "" {
		t.Errorf("Description=%q", fm.Description)
	}
	if fm.Lifetime != "postural" {
		t.Errorf("Lifetime default=%q want 'postural'", fm.Lifetime)
	}
	if string(body) != "hello\n" {
		t.Errorf("body=%q", body)
	}
}

// TestParseIgnoresTriggersKey pins the removal of the Triggers field:
// a SKILL.md that still carries a legacy `triggers:` key must parse
// without error, silently dropping the unknown key while every other
// field survives. (yaml.v3 is non-strict, so unknown keys are ignored.)
func TestParseIgnoresTriggersKey(t *testing.T) {
	in := []byte("---\nname: legacy\ndescription: d\ntriggers:\n  - a\n  - b\n---\nbody\n")
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse with legacy triggers key: %v", err)
	}
	if fm.Name != "legacy" || fm.Description != "d" {
		t.Errorf("triggers key contaminated other fields: %+v", fm)
	}
}

// TestParsePathsRoundTrip proves the surviving Paths field round-trips a
// multi-entry glob list verbatim and in order.
func TestParsePathsRoundTrip(t *testing.T) {
	in := []byte("---\nname: x\npaths:\n  - \"**/a*.md\"\n  - \"docs/**\"\n---\nbody\n")
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"**/a*.md", "docs/**"}
	if len(fm.Paths) != len(want) {
		t.Fatalf("Paths=%v want %v", fm.Paths, want)
	}
	for i := range want {
		if fm.Paths[i] != want[i] {
			t.Errorf("Paths[%d]=%q want %q", i, fm.Paths[i], want[i])
		}
	}
}

func TestParseDefaultLifetime(t *testing.T) {
	in := []byte("---\nname: x\nlifetime: \"\"\n---\nbody\n")
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Lifetime != "postural" {
		t.Errorf("Lifetime=%q want postural", fm.Lifetime)
	}
}

func TestParseTransactionalLifetime(t *testing.T) {
	in := []byte("---\nname: x\nlifetime: transactional\n---\nbody\n")
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Lifetime != "transactional" {
		t.Errorf("Lifetime=%q want transactional", fm.Lifetime)
	}
}

func TestParseMalformedYAML(t *testing.T) {
	in := []byte("---\nname: : : bad\n  - not valid\n---\nbody\n")
	_, _, err := Parse(in)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	if !strings.Contains(err.Error(), "parse frontmatter YAML") {
		t.Errorf("err=%q", err)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	in := []byte("# Just a markdown\n\nNo header.\n")
	fm, body, err := Parse(in)
	if !errors.Is(err, ErrNoFrontmatter) {
		t.Fatalf("err=%v want ErrNoFrontmatter", err)
	}
	if fm.Name != "" {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if string(body) != string(in) {
		t.Errorf("body should equal input on no-frontmatter")
	}
}

func TestParseUnterminatedFence(t *testing.T) {
	in := []byte("---\nname: x\nno closing fence here\n")
	_, _, err := Parse(in)
	if err == nil {
		t.Fatal("expected unterminated-fence error")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("err=%q", err)
	}
}

func TestParseCRLF(t *testing.T) {
	in := []byte("---\r\nname: x\r\n---\r\nbody\r\n")
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse CRLF: %v", err)
	}
	if fm.Name != "x" {
		t.Errorf("Name=%q", fm.Name)
	}
}

func TestParseEmptyBody(t *testing.T) {
	in := []byte("---\nname: x\n---")
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Name != "x" {
		t.Errorf("Name=%q", fm.Name)
	}
	if len(body) != 0 {
		t.Errorf("body should be empty, got %q", body)
	}
}

func TestParseFenceFirstLineOnly(t *testing.T) {
	// Leading blank line before fence should NOT be treated as frontmatter.
	in := []byte("\n---\nname: x\n---\nbody\n")
	_, _, err := Parse(in)
	if !errors.Is(err, ErrNoFrontmatter) {
		t.Errorf("expected ErrNoFrontmatter for non-first-line fence, got %v", err)
	}
}
