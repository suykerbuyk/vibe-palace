// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// maxSkillLabelBytes is skillLabelPrefix (22 bytes) plus ExtractBrief's 60
// plus its 3-byte ellipsis.
const maxSkillLabelBytes = 85

// shimFrontmatter returns the YAML between a rendered shim's opening fence and
// the next closing fence.
func shimFrontmatter(t *testing.T, rendered string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(rendered, "---\n")
	if !ok {
		t.Fatalf("no opening fence:\n%s", rendered)
	}
	fm, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatalf("no closing fence:\n%s", rendered)
	}
	return fm
}

// TestPersonaShimFrontmatterIsALabel pins the one-line label on every persona
// target, for every shipped skill, and disable-model-invocation enforcement
// on the Claude target only. Each rendered frontmatter must parse as strict
// YAML (a bare scalar holding ": " does not, and a document that fails to
// parse leaves every key in it to the host's fallback); its description must
// be the short label, not the trigger text; and the Claude skill alone must
// carry disable-model-invocation: true.
func TestPersonaShimFrontmatterIsALabel(t *testing.T) {
	items := embeddedSkillItems(t)
	// Control: the shipped set, including the two skills whose labels hold
	// ": " and therefore need the quoting.
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Name] = true
	}
	for _, want := range []string{"chair", "code-digger", "epic-orchestrator", "pair-reviewer", "second-opinion", "startup-analyst"} {
		if !seen[want] {
			t.Fatalf("embedded skill %q not found, so the test does not cover the shipped set (found %v)", want, seen)
		}
	}

	for _, it := range items {
		for _, kind := range personaKinds {
			t.Run(kind.String()+"/"+it.Name, func(t *testing.T) {
				var fm map[string]any
				if err := yaml.Unmarshal([]byte(shimFrontmatter(t, RenderSkill(kind, it))), &fm); err != nil {
					t.Fatalf("frontmatter is not valid YAML: %v", err)
				}

				desc, _ := fm["description"].(string)
				if desc != skillLabel(it) {
					t.Errorf("description = %q, want the label %q", desc, skillLabel(it))
				}
				if !strings.HasPrefix(desc, skillLabelPrefix) {
					t.Errorf("description %q lacks the %q prefix", desc, skillLabelPrefix)
				}
				if len(desc) > maxSkillLabelBytes {
					t.Errorf("description is %d bytes, want at most %d: %q", len(desc), maxSkillLabelBytes, desc)
				}
				if !utf8.ValidString(desc) {
					t.Errorf("description is not valid UTF-8: %q", desc)
				}
				// Every shipped description names its trigger phrases after
				// the first sentence; none of that may reach the shim.
				if strings.Contains(it.Frontmatter.Description, "Trigger") && strings.Contains(desc, "Trigger") {
					t.Errorf("description carries trigger text: %q", desc)
				}

				dmi, present := fm["disable-model-invocation"]
				if kind == ClaudeSkill {
					if dmi != true {
						t.Errorf("disable-model-invocation = %v (present %v), want true", dmi, present)
					}
				} else if present {
					t.Errorf("disable-model-invocation is set on %s, which is not known to honour it", kind)
				}
			})
		}
	}
}

// TestSkillLabelFallsBackToTheName: a description that briefs to nothing — an
// empty one, or one whose only line is a heading, which ExtractBrief skips —
// labels the shim with the skill's name.
func TestSkillLabelFallsBackToTheName(t *testing.T) {
	for _, desc := range []string{"", "# heading only"} {
		it := SkillItem{Name: "pairing"}
		it.Frontmatter.Description = desc
		if got, want := skillLabel(it), skillLabelPrefix+"pairing"; got != want {
			t.Errorf("description %q: label = %q, want %q", desc, got, want)
		}
	}
}

// TestYAMLDoubleQuotedEscapes: the backslash and the quote are the only
// characters a single-line double-quoted YAML scalar must escape.
func TestYAMLDoubleQuotedEscapes(t *testing.T) {
	for _, s := range []string{`plain`, `a: b`, `say "hi"`, `back\slash`, `"\"`} {
		var got string
		if err := yaml.Unmarshal([]byte(yamlDoubleQuoted(s)), &got); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got != s {
			t.Errorf("round trip of %q = %q", s, got)
		}
	}
}

// TestSkillLabelOfAMultiByteDescriptionParses: a description whose first 60
// bytes end inside a multi-byte character must still yield a valid UTF-8
// label and frontmatter yaml.v3 accepts on every persona target — on the
// Claude skill the disable-model-invocation key rides on that parse.
func TestSkillLabelOfAMultiByteDescriptionParses(t *testing.T) {
	it := SkillItem{Name: "dashes"}
	it.Frontmatter.Description = "a" + strings.Repeat("—", 30)
	if label := skillLabel(it); !utf8.ValidString(label) {
		t.Errorf("label %q is not valid UTF-8", label)
	}
	for _, kind := range personaKinds {
		var fm map[string]any
		if err := yaml.Unmarshal([]byte(shimFrontmatter(t, RenderSkill(kind, it))), &fm); err != nil {
			t.Errorf("%s: frontmatter is not valid YAML: %v", kind, err)
		}
	}
}
