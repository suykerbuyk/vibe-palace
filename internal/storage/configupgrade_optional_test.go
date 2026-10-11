// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"sort"
	"strings"
	"testing"
)

// canonicalCompleteGlobal is a global config that carries every CANONICAL
// (active-in-defaults) key but NONE of the optional/documented commented blocks.
// It is the "complete under the old two-tier model" shape the optional tier must
// now propagate into.
const canonicalCompleteGlobal = `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[meta]
version_major = 1
version_minor = 2
kind = "global"

[embedder]
model = "x"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100
`

// want28 is the optional/documented tier re-derived by hand from
// defaults.toml/template.toml (Chair C1): 28 keys across 9 sections.
var want28 = []string{
	"embedder.backend",
	"enrichment.api_key_env",
	"enrichment.base_url",
	"enrichment.enabled",
	"enrichment.max_tokens",
	"enrichment.model",
	"enrichment.provider",
	"enrichment.timeout_seconds",
	"palace.llm.api_key_env",
	"palace.llm.endpoint",
	"palace.llm.max_tokens",
	"palace.llm.model",
	"palace.rooms.audio.keywords",
	"palace.scoring.min_score",
	"palace.scoring.rooms.ml.high",
	"palace.scoring.rooms.ml.low",
	"palace.scoring.rooms.ml.medium",
	"palace.scoring.rooms.testing.high",
	"palace.scoring.rooms.testing.low",
	"palace.scoring.rooms.testing.medium",
	"project_vaults.my-project",
	"summarization.api_key_env",
	"summarization.base_url",
	"summarization.enabled",
	"summarization.max_tokens",
	"summarization.model",
	"summarization.provider",
	"summarization.timeout_seconds",
}

// TestOptionalDocKeys pins the tier membership: the set equals the 28-key
// enumeration AND is disjoint from the canonical set. Load-bearing: it fails if a
// key is added/removed from the template without updating the enumeration, or if
// a canonical key ever leaks into the optional tier.
func TestOptionalDocKeys(t *testing.T) {
	got := OptionalDocKeys(templateToml, defaultsToml)

	var gotKeys []string
	for k := range got {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)

	if strings.Join(gotKeys, "\n") != strings.Join(want28, "\n") {
		t.Errorf("OptionalDocKeys mismatch\n got (%d): %v\nwant (%d): %v",
			len(gotKeys), gotKeys, len(want28), want28)
	}

	// Disjoint from canonical.
	canonical, err := CanonicalKeys()
	if err != nil {
		t.Fatalf("CanonicalKeys: %v", err)
	}
	for k := range got {
		if canonical[k] {
			t.Errorf("optional key %q is also canonical — tiers must be disjoint", k)
		}
	}
	// And backend specifically stays optional, not canonical (pairs with
	// TestEmbedderBackendIsNotCanonical).
	if !got["embedder.backend"] {
		t.Error("embedder.backend must be in the optional tier")
	}
}

// activeHeaderPresent reports whether text contains an ACTIVE "[section]" header
// (not the commented "# [section]" form).
func activeHeaderPresent(text, section string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "["+section+"]" {
			return true
		}
	}
	return false
}

// TestPropagateOptionalBlocks covers the three group-(b) states plus the
// all-present no-op, and the no-nag + idempotency invariants.
func TestPropagateOptionalBlocks(t *testing.T) {
	// --- (c) fully-absent sections + (a) active-header key (backend) ---
	t.Run("fully absent", func(t *testing.T) {
		out, propagated := PropagateOptionalBlocks(canonicalCompleteGlobal, templateToml, defaultsToml)

		if len(propagated) != len(want28) {
			t.Errorf("propagated %d keys, want %d: %v", len(propagated), len(want28), propagated)
		}

		// backend is inserted IN PLACE under the existing active [embedder],
		// between [embedder] and [search] — not appended at EOF.
		iEmbedder := strings.Index(out, "[embedder]")
		iBackend := strings.Index(out, "# backend =")
		iSearch := strings.Index(out, "[search]")
		if iBackend < 0 || !(iEmbedder < iBackend && iBackend < iSearch) {
			t.Errorf("backend not inserted under the active [embedder] section (iEmbedder=%d iBackend=%d iSearch=%d)",
				iEmbedder, iBackend, iSearch)
		}

		// backend carries its doc prose, not a bare line.
		if !strings.Contains(out, "# Optional, off by default") {
			t.Error("backend's documentation prose was not propagated")
		}

		// Commented doc-blocks present.
		for _, want := range []string{
			"# [palace.llm]", "# endpoint =", "# [enrichment]", "# [summarization]",
			"# [project_vaults]", "# my-project =", "# [palace.scoring]", "# min_score =",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("expected propagated block to contain %q", want)
			}
		}

		// NO active header synthesized for any commented section.
		for _, sec := range []string{
			"palace.llm", "enrichment", "summarization", "project_vaults",
			"palace.scoring", "palace.rooms.audio", "palace.scoring.rooms.testing",
		} {
			if activeHeaderPresent(out, sec) {
				t.Errorf("an ACTIVE [%s] header was synthesized — must stay commented", sec)
			}
		}

		// project_vaults is appended LAST (after summarization).
		if strings.Index(out, "# [project_vaults]") < strings.Index(out, "# [summarization]") {
			t.Error("project_vaults block must be appended last (after summarization)")
		}

		// No nag: the canonical staleness view sees nothing missing.
		assertNoCanonicalDrift(t, out)

		// Idempotent.
		out2, prop2 := PropagateOptionalBlocks(out, templateToml, defaultsToml)
		if out2 != out {
			t.Errorf("second pass changed the config:\n%s", out2)
		}
		if prop2 != nil {
			t.Errorf("second pass propagated keys again: %v", prop2)
		}
	})

	// --- (b) partial commented section: the B1 regression ---
	t.Run("partial commented section", func(t *testing.T) {
		seed := canonicalCompleteGlobal + `
# [palace.llm]
# endpoint = "https://api.x.ai/v1"
`
		out, _ := PropagateOptionalBlocks(seed, templateToml, defaultsToml)

		// Exactly one palace.llm header (no duplicate, no active variant).
		if n := strings.Count(out, "[palace.llm]"); n != 1 {
			t.Errorf("want exactly 1 [palace.llm] header, got %d:\n%s", n, out)
		}
		if activeHeaderPresent(out, "palace.llm") {
			t.Error("palace.llm must remain a COMMENTED header")
		}

		// Each palace.llm key appears exactly once WITHIN the palace.llm block.
		// (model/api_key_env/max_tokens also occur in the enrichment and
		// summarization blocks, so the count must be section-scoped.)
		llmBlock := out[strings.Index(out, "# [palace.llm]"):]
		if i := strings.Index(llmBlock, "\n\n"); i >= 0 {
			llmBlock = llmBlock[:i]
		}
		for _, k := range []string{"# endpoint =", "# model =", "# api_key_env =", "# max_tokens ="} {
			if n := strings.Count(llmBlock, k); n != 1 {
				t.Errorf("palace.llm key line %q appears %d times in its block, want 1:\n%s", k, n, llmBlock)
			}
		}

		// The user's existing endpoint value is preserved (not re-templated).
		if !strings.Contains(out, `# endpoint = "https://api.x.ai/v1"`) {
			t.Error("user's existing endpoint value was not preserved")
		}

		assertNoCanonicalDrift(t, out)

		// Idempotent on a second pass.
		out2, _ := PropagateOptionalBlocks(out, templateToml, defaultsToml)
		if out2 != out {
			t.Errorf("second pass changed a partial-section config:\n%s", out2)
		}
	})

	// --- all present: template-verbatim is a no-op ---
	t.Run("all present", func(t *testing.T) {
		full := GenerateConfigTOML("/tmp", true)
		out, propagated := PropagateOptionalBlocks(full, templateToml, defaultsToml)
		if out != full {
			t.Error("template-verbatim config must be left unchanged")
		}
		if propagated != nil {
			t.Errorf("template-verbatim config propagated keys: %v", propagated)
		}
	})
}

// assertNoCanonicalDrift fails if the canonical staleness view (CanonicalKeys)
// finds any key missing from text — i.e. the optional pass never raised the nag.
func assertNoCanonicalDrift(t *testing.T, text string) {
	t.Helper()
	canonical, err := CanonicalKeys()
	if err != nil {
		t.Fatalf("CanonicalKeys: %v", err)
	}
	if miss := MissingKeys(canonical, PresentKeys(text)); len(miss) != 0 {
		t.Errorf("optional propagation raised canonical drift (nag): %v", miss)
	}
}
