// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// writeBackFixture carries every expandScoped placeholder, each exactly once, so
// a refusal message can be asserted to name all of them with real counts.
//
// {{WING}} and {{ROOM}} are here for a reason that is easy to lose: with no wing
// or room in scope they expand to the EMPTY STRING, and so does {{PROJECT}} when
// the project is empty. Three of the four placeholders therefore leave NO
// fingerprint in the expanded text. That is why the guard counts placeholder
// LOSS instead of looking for an expanded value — a value-matching rule is
// structurally blind on three quarters of the vocabulary, not merely weaker.
const writeBackFixture = `# {{PROJECT}} — Working Context

Last updated: {{DATE}}
Wing is {{WING}} and room is {{ROOM}}.

## State

A real line of prose.
`

// bakedBody is what an agent gets back from a placeholder-EXPANDING reader and
// would write straight back: every token substituted, the surrounding prose
// untouched.
//
// It is a frozen LITERAL, not scopetoken.Expand(writeBackFixture, …), on purpose:
// an acceptance test should not compute its input with the same code it is
// testing. The {{DATE}} value is therefore a snapshot and is never compared
// against a live expansion — do NOT tighten this into an equality check against
// Expand, which would make the test flake at midnight.
const bakedBody = `# demo — Working Context

Last updated: 2026-08-16
Wing is  and room is .

## State

A real line of prose.
`

// TestIntegration_VaultWriteRefusesTokenBake drives the ORIGINAL incident
// through the production MCP wiring: a whole-file vp_vault_write whose body came
// from an expanding reader, presenting the digest that reader advertised.
//
// The digest matches, because it is computed over the RAW bytes while the body
// served was expanded. Before this guard the compare-and-set therefore PASSED
// and the write reported clean success while destroying every live token.
//
// Reproduced live before it was written: `vp vault write --expected-sha256 <raw>`
// returned {"bytes":112,...,"replaced_sha256":"721e2e91…"} against a scratch
// vault and left four dead tokens on disk.
func TestIntegration_VaultWriteRefusesTokenBake(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	var read struct {
		Content string `json:"content"`
		Sha256  string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_vault_read", map[string]any{
		"path": path,
	})), &read); err != nil {
		t.Fatalf("parse vp_vault_read: %v", err)
	}

	// NON-VACUITY: the body under test must genuinely differ from what is on
	// disk in the tokens, or a passing test proves nothing.
	if !strings.Contains(read.Content, "{{PROJECT}}") {
		t.Fatalf("fixture lost its tokens before the test ran:\n%s", read.Content)
	}
	if strings.Contains(bakedBody, "{{") {
		t.Fatal("bakedBody still carries a placeholder; it is not a baked body")
	}

	text, isErr := h.callToolRaw(t, "vp_vault_write", map[string]any{
		"path":            path,
		"content":         bakedBody,
		"expected_sha256": read.Sha256,
	})
	if !isErr {
		t.Fatalf("vp_vault_write ACCEPTED a baked body under a matching digest: %s", text)
	}
	for _, want := range []string{"{{PROJECT}}", "{{DATE}}", "{{WING}}", "{{ROOM}}"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal does not name the lost placeholder %s: %s", want, text)
		}
	}
	if !strings.Contains(text, "vp_vault_read") {
		t.Errorf("refusal does not carry the remedy: %s", text)
	}
	// Clause 3: the counts are the measurement, not decoration — they are what
	// lets a reader tell "I dropped one of three" from "I dropped all of them"
	// without re-reading the file.
	if !strings.Contains(text, "(1 on disk, 0 incoming)") {
		t.Errorf("refusal does not report the per-placeholder counts: %s", text)
	}
	if !strings.Contains(text, "vp_vault_edit") {
		t.Errorf("refusal does not name the deliberate-removal route: %s", text)
	}

	assertOnDiskUnchanged(t, h, path, writeBackFixture)
}

// TestIntegration_VaultWriteRefusesBakeWithoutCAS pins the EASIER bake.
//
// vaultfs.Write treats an empty expected_sha256 as "no compare-and-set" and
// overwrites. A guard that only ran when a digest was supplied would leave this
// path — the one that needs no digest at all — wide open, so the matching-digest
// case above is necessary and NOT sufficient.
func TestIntegration_VaultWriteRefusesBakeWithoutCAS(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	text, isErr := h.callToolRaw(t, "vp_vault_write", map[string]any{
		"path":    path,
		"content": bakedBody,
	})
	if !isErr {
		t.Fatalf("vp_vault_write ACCEPTED a baked body with no compare-and-set at all: %s", text)
	}
	assertOnDiskUnchanged(t, h, path, writeBackFixture)
}

// TestIntegration_UpdateResumeRefusesTokenBake covers the SECOND whole-file
// writer. storage.WriteResume does not call vaultfs.Write — it runs its own
// compare-and-set and then atomicfile.Write — so it is a separate call site that
// a test of the first would not exercise.
//
// This path was reproduced independently before the test was written, driven
// through `vp mcp` over stdio: vp_update_resume returned
// {"project":"scratchproj","status":"updated"} and destroyed all four tokens.
// The test was not copied from the vaultfs one with the name changed.
func TestIntegration_UpdateResumeRefusesTokenBake(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	var read struct {
		Sha256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_vault_read", map[string]any{
		"path": path,
	})), &read); err != nil {
		t.Fatalf("parse vp_vault_read: %v", err)
	}

	logs := captureLogs(t)
	text, isErr := h.callToolRaw(t, "vp_update_resume", map[string]any{
		"project":         "demo",
		"content":         bakedBody,
		"expected_sha256": read.Sha256,
	})
	if !isErr {
		t.Fatalf("vp_update_resume ACCEPTED a baked body under a matching digest: %s", text)
	}

	// This path wraps the refusal with %w on its way out of WriteResume. Assert
	// the diagnostic survives that wrap rather than only that SOMETHING failed:
	// a regression that rewrapped with %v would lose the caller class, and one
	// that dropped the counts would still leave a bare isError test green.
	if !strings.Contains(text, "(1 on disk, 0 incoming)") {
		t.Errorf("the counts did not survive the wrap out of WriteResume: %s", text)
	}
	if !strings.Contains(text, "vp_vault_read") {
		t.Errorf("the remedy did not survive the wrap out of WriteResume: %s", text)
	}
	if out := logs.String(); !strings.Contains(out, `"fault":"caller"`) {
		t.Errorf("vp_update_resume's refusal was not caller-classified:\n%s", out)
	}

	assertOnDiskUnchanged(t, h, path, writeBackFixture)
}

// TestIntegration_TokenBakeRefusalIsCallerFault pins the error CLASS, not just
// the refusal.
//
// A caller-supplied body the file cannot accept is the same class as an
// old_string that does not match. mcp.makeHandler defaults an unclassified error
// to fault="internal", which ambers vp_health and buries a working guard in the
// warn counts the amber-wash exists to keep clean. Only apperr.CallerError is
// reclassified to fault="caller".
func TestIntegration_TokenBakeRefusalIsCallerFault(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	logs := captureLogs(t)
	if _, isErr := h.callToolRaw(t, "vp_vault_write", map[string]any{
		"path":    path,
		"content": bakedBody,
	}); !isErr {
		t.Fatal("expected the write to be refused")
	}

	out := logs.String()
	if !strings.Contains(out, `"fault":"caller"`) {
		t.Errorf("token-loss refusal was not classified as caller friction:\n%s", out)
	}
	if strings.Contains(out, `"fault":"internal"`) {
		t.Errorf("token-loss refusal logged as an INTERNAL fault; it ambers vp_health:\n%s", out)
	}
}

// TestIntegration_EditStillRemovesTokenDeliberately is the pin that keeps the
// escape hatch open.
//
// There is deliberately no opt-out parameter on the guard — ADR-006 bans a
// required field nothing can check. The only way to remove a placeholder on
// purpose is vp_vault_edit with an old_string containing the RAW placeholder,
// which cannot be composed from an expanded read and is therefore its own proof
// of provenance.
//
// If a later conversion "completes" the guard by adding it to Edit as well, that
// remedy is bricked and this test is what says so.
func TestIntegration_EditStillRemovesTokenDeliberately(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	text, isErr := h.callToolRaw(t, "vp_vault_edit", map[string]any{
		"path":       path,
		"old_string": "{{PROJECT}}",
		"new_string": "demo",
	})
	if isErr {
		t.Fatalf("vp_vault_edit refused a deliberate removal anchored on the RAW placeholder; the escape hatch is bricked: %s", text)
	}

	onDisk := readResumeFile(t, h)
	if strings.Contains(onDisk, "{{PROJECT}}") {
		t.Error("the deliberate removal did not land")
	}
	if !strings.Contains(onDisk, "{{DATE}}") {
		t.Error("Edit removed a token it was not anchored on")
	}
}

// TestIntegration_EditRejectsAnExpandedAnchor records the property that makes
// the escape hatch safe: an old_string copied from an expanding reader cannot
// match where a token lives, so Edit cannot bake one even without a guard.
//
// This is the half of the retired workflow.md paragraph that was ALREADY
// enforced. The test is what lets that sentence be deleted rather than kept as a
// receipt.
func TestIntegration_EditRejectsAnExpandedAnchor(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	text, isErr := h.callToolRaw(t, "vp_vault_edit", map[string]any{
		"path":       path,
		"old_string": "# demo — Working Context",
		"new_string": "# demo — Rewritten",
	})
	if !isErr {
		t.Fatalf("an EXPANDED anchor matched a file whose bytes carry the raw token: %s", text)
	}
	if !strings.Contains(text, "old_string not found") {
		t.Errorf("expected the anchor miss, got: %s", text)
	}
}

// TestIntegration_WholeFileWriteKeepingTokensAccepted is the non-regression
// floor: the guard must not turn every whole-file write into a refusal. A body
// that preserves the placeholders lands normally.
func TestIntegration_WholeFileWriteKeepingTokensAccepted(t *testing.T) {
	const path = "Projects/demo/resume.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume("demo", writeBackFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}

	edited := strings.Replace(writeBackFixture, "A real line of prose.", "An edited line of prose.", 1)
	if edited == writeBackFixture {
		t.Fatal("the edit did not apply; the test would be vacuous")
	}

	if text, isErr := h.callToolRaw(t, "vp_vault_write", map[string]any{
		"path":    path,
		"content": edited,
	}); isErr {
		t.Fatalf("a whole-file write that PRESERVED every placeholder was refused: %s", text)
	}
	if got := readResumeFile(t, h); got != edited {
		t.Errorf("edit did not land:\n%s", got)
	}
}

// TestIntegration_NonScopeTokensStayWritable is the false-positive pin for the
// vocabulary decision.
//
// The skill and command corpus carries many other {{UPPER}} shapes —
// {{KNOWN_CONTEXT}}, {{SHA}}, {{PATH}}, {{FOCUS}}, … — which expandScoped does
// NOT substitute. A regex-based rule would refuse legitimate whole-file writes
// of those files. The guard iterates the expander's own table instead, so these
// are freely writable AND freely removable.
//
// Re-derive the corpus, never cite it:
//
//	grep -rhoE '\{\{[A-Z_]+\}\}' internal/templates/templates/ | sort | uniq -c
func TestIntegration_NonScopeTokensStayWritable(t *testing.T) {
	const path = "Projects/demo/notes/skill-template.md"

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	const withTokens = "Focus: {{FOCUS}}\nPath: {{PATH}}\nSha: {{SHA}}\n"
	if text, isErr := h.callToolRaw(t, "vp_vault_write", map[string]any{
		"path": path, "content": withTokens,
	}); isErr {
		t.Fatalf("writing non-scope tokens was refused: %s", text)
	}

	// Removing them is a legitimate whole-file rewrite, not a bake.
	if text, isErr := h.callToolRaw(t, "vp_vault_write", map[string]any{
		"path": path, "content": "Focus: resolved\nPath: /x\nSha: abc\n",
	}); isErr {
		t.Fatalf("REMOVING non-scope tokens was refused; the guard is over-broad: %s", text)
	}
}

func readResumeFile(t *testing.T, h *testHarness) string {
	t.Helper()
	p, err := h.Vault.ResumeFile("demo")
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read resume: %v", err)
	}
	return string(data)
}

func assertOnDiskUnchanged(t *testing.T, h *testHarness, path, want string) {
	t.Helper()
	got := readResumeFile(t, h)
	if got != want {
		t.Errorf("the refused write still reached disk (%s):\n%s", path, got)
	}
}
