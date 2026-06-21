// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// embeddedEnrichment reads the embedded enrichment.md template bytes for
// use as the expected value in drift/fallback assertions.
func embeddedEnrichment(t *testing.T) string {
	t.Helper()
	data, err := templates.FS().ReadFile("templates/enrichment.md")
	if err != nil {
		t.Fatalf("read embedded enrichment.md: %v", err)
	}
	return string(data)
}

// TestEnrichmentTemplate_DriftGuard ensures the embedded enrichment.md
// template stays byte-identical to the in-package defaultSystemPrompt const.
// They are two copies of the same prompt; this catches future divergence.
func TestEnrichmentTemplate_DriftGuard(t *testing.T) {
	if got := embeddedEnrichment(t); got != defaultSystemPrompt {
		t.Errorf("embedded enrichment.md differs from defaultSystemPrompt\n"+
			"len(embedded)=%d len(const)=%d", len(got), len(defaultSystemPrompt))
	}
}

// TestLoadSystemPrompt_EmptyVault returns the embedded/const content when
// no vault path is supplied.
func TestLoadSystemPrompt_EmptyVault(t *testing.T) {
	want := embeddedEnrichment(t)
	got := LoadSystemPrompt("")
	if got != want {
		t.Errorf("LoadSystemPrompt(\"\") = %q..., want embedded content", got[:min(40, len(got))])
	}
	if got != defaultSystemPrompt {
		t.Errorf("LoadSystemPrompt(\"\") must equal defaultSystemPrompt")
	}
	if got == "" {
		t.Error("LoadSystemPrompt must never return empty string")
	}
}

// TestLoadSystemPrompt_VaultOverride proves a vault Templates/enrichment.md
// file wins over the embedded template.
func TestLoadSystemPrompt_VaultOverride(t *testing.T) {
	const sentinel = "SENTINEL vault override prompt — do not mangle {braces}"
	dir := t.TempDir()
	tdir := filepath.Join(dir, "Templates")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatalf("mkdir Templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "enrichment.md"), []byte(sentinel), 0o644); err != nil {
		t.Fatalf("write vault template: %v", err)
	}

	if got := LoadSystemPrompt(dir); got != sentinel {
		t.Errorf("LoadSystemPrompt(vault) = %q, want sentinel", got)
	}
}

// TestLoadSystemPrompt_MissingVaultFile falls back to the embedded content
// when the vault has no enrichment.md.
func TestLoadSystemPrompt_MissingVaultFile(t *testing.T) {
	dir := t.TempDir() // no Templates/enrichment.md inside
	want := embeddedEnrichment(t)
	if got := LoadSystemPrompt(dir); got != want {
		t.Errorf("LoadSystemPrompt(emptyVault) did not fall back to embedded content")
	}
}

// TestLoadSystemPrompt_PromptIntegrity [H1] verifies the raw load preserves
// the literal JSON schema braces and field names — proving no token
// expansion or escaping mangled the prompt.
func TestLoadSystemPrompt_PromptIntegrity(t *testing.T) {
	got := LoadSystemPrompt("")
	for _, want := range []string{`"summary"`, "{", "}", `"tag"`, "implementation, debugging, refactor, exploration, review, docs, planning"} {
		if !strings.Contains(got, want) {
			t.Errorf("loaded prompt missing expected literal %q", want)
		}
	}
}
