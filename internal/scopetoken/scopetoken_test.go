// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package scopetoken

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// TestTableIsTheSingleSource is the property the package exists for, and it is
// in-package on purpose: it ranges the unexported table, so it covers every
// placeholder the expander knows about — including one added tomorrow — without
// an exported list for the source audit to flag as uninvoked.
//
// The completeness direction is the one that matters. An earlier version of this
// test lived in the integration package, could only assert "Lost returned at
// least one row", and PASSED while Lost reported just the first dropped
// placeholder and silently swallowed the other three. Asserting one-row-per-token
// is what closes that.
func TestTableIsTheSingleSource(t *testing.T) {
	scope := Scope{Project: "p", Wing: "w", Room: "r"}
	for _, tok := range tokens {
		body := "x " + tok.placeholder + " y"

		got := Expand(body, scope)
		if strings.Contains(got, tok.placeholder) {
			t.Errorf("Expand does not substitute %s, but it is in the table", tok.placeholder)
			continue
		}

		losses := Lost(body, got)
		if len(losses) != 1 || losses[0].Placeholder != tok.placeholder {
			t.Errorf("Expand erases %s but Lost reports %+v", tok.placeholder, losses)
		}
	}
}

// TestLostReportsEveryDroppedPlaceholder pins completeness across the WHOLE
// table at once, which is the shape a real write-back takes: one body losing
// every token in a single write, not one token at a time.
func TestLostReportsEveryDroppedPlaceholder(t *testing.T) {
	var b strings.Builder
	for _, tok := range tokens {
		b.WriteString("line ")
		b.WriteString(tok.placeholder)
		b.WriteString("\n")
	}
	body := b.String()

	losses := Lost(body, Expand(body, Scope{Project: "p", Wing: "w", Room: "r"}))
	if len(losses) != len(tokens) {
		t.Fatalf("a body losing every placeholder reported %d of %d losses: %+v", len(losses), len(tokens), losses)
	}
	for i, l := range losses {
		if l.Placeholder != tokens[i].placeholder {
			t.Errorf("loss %d is %s, want %s (table order)", i, l.Placeholder, tokens[i].placeholder)
		}
		if l.OnDisk != 1 || l.Incoming != 0 {
			t.Errorf("%s counts are (%d,%d), want (1,0)", l.Placeholder, l.OnDisk, l.Incoming)
		}
	}
}

// TestTokensCanExpandToNothing is the mutation-killer for the rejected design:
// keying the guard on "the expanded value appears in the incoming content"
// instead of on placeholder loss.
//
// With an empty scope, PROJECT, WING and ROOM all substitute the empty string.
// There is no expanded value to look for, so a value-matching rule reports the
// bake as absent on three of the four placeholders.
func TestTokensCanExpandToNothing(t *testing.T) {
	body := "a {{PROJECT}} b {{WING}} c {{ROOM}} d"
	got := Expand(body, Scope{})
	if got != "a  b  c  d" {
		t.Fatalf("empty scope no longer erases the tokens: %q", got)
	}
	if losses := Lost(body, got); len(losses) != 3 {
		t.Fatalf("counting must see 3 losses where value-matching sees none, got %d: %+v", len(losses), losses)
	}
}

// TestFirstWriteIsNotRefused pins the scope boundary: the rule keys on tokens
// present in the EXISTING bytes, which is what keeps `vp init` scaffolds, first
// writes and template materialization out of it.
func TestFirstWriteIsNotRefused(t *testing.T) {
	if err := CheckWriteBack("Projects/demo/resume.md", "", "no tokens here"); err != nil {
		t.Errorf("a first write (no existing bytes) was refused: %v", err)
	}
	if err := CheckWriteBack("Projects/demo/resume.md", "plain", "{{PROJECT}} added"); err != nil {
		t.Errorf("ADDING a token was refused: %v", err)
	}
}

// TestNonScopePlaceholdersAreNotOurs guards the false-positive class a regex
// would have created: the skill and command corpus carries many other {{UPPER}}
// shapes this package does not own, and removing them is a legitimate rewrite.
//
// Re-derive the corpus, never cite it:
//
//	grep -rhoE '\{\{[A-Z_]+\}\}' internal/templates/templates/ | sort | uniq -c
func TestNonScopePlaceholdersAreNotOurs(t *testing.T) {
	const body = "Focus: {{FOCUS}}\nPath: {{PATH}}\nSha: {{SHA}}\n"
	if got := Expand(body, Scope{Project: "p"}); got != body {
		t.Errorf("Expand touched a placeholder this package does not own: %q", got)
	}
	if err := CheckWriteBack("Projects/demo/note.md", body, "Focus: x\nPath: y\nSha: z\n"); err != nil {
		t.Errorf("removing non-scope placeholders was refused: %v", err)
	}
}

// TestRefusalIsCallerClassified pins the error class at the source. The MCP seam
// reclassifies only apperr.CallerError to fault="caller"; anything else defaults
// to fault="internal" and ambers vp_health. The integration tests assert the
// resulting log line on both writers; this asserts the class itself, so a
// regression is named here rather than diagnosed from a log.
func TestRefusalIsCallerClassified(t *testing.T) {
	err := CheckWriteBack("Projects/demo/resume.md", "{{PROJECT}} x", "demo x")
	if err == nil {
		t.Fatal("a dropped placeholder was not refused")
	}
	if !apperr.IsCaller(err) {
		t.Errorf("refusal is not caller-classified: %v", err)
	}
	if !strings.Contains(err.Error(), "(1 on disk, 0 incoming)") {
		t.Errorf("refusal does not carry the counts: %v", err)
	}
}
