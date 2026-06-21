// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

import (
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// embeddedTemplatePath is the root-relative path (no "templates/" prefix)
// of the enrichment system-prompt template inside the embedded FS.
const embeddedTemplatePath = "enrichment.md"

// LoadSystemPrompt resolves the session-synthesis system prompt with the
// precedence: vault override > embedded template file > in-package const.
//
// The content is loaded RAW — no scoped-token expansion (no {{DATE}} /
// {{PROJECT}} substitution). A system prompt must stay deterministic so it
// does not leak nondeterminism into recorded fixtures or bust fixture tests.
//
// Precedence:
//   - If vaultPath != "", read <vaultPath>/Templates/enrichment.md directly
//     and, on success, return its verbatim content.
//   - On any miss/error, fall back to the embedded enrichment.md template.
//   - If even the embedded read fails, return defaultSystemPrompt so the
//     function never returns "".
func LoadSystemPrompt(vaultPath string) string {
	if vaultPath != "" {
		p := filepath.Join(vaultPath, "Templates", "enrichment.md")
		if data, err := os.ReadFile(p); err == nil {
			return string(data)
		}
	}

	fsys := templates.FS()
	if data, err := fsys.ReadFile(filepath.ToSlash(filepath.Join("templates", embeddedTemplatePath))); err == nil {
		return string(data)
	}

	return defaultSystemPrompt
}
