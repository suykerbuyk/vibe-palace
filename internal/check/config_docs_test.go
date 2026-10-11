// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// canonicalCompleteNoOptional carries every canonical key but no optional/
// documented commented blocks — the config that the Config Docs row should flag.
const canonicalCompleteNoOptional = `vault_path = "/tmp"
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

// TestCheckConfigDocs pins the separate optional-docs row: Info when optional
// blocks are absent, Skip when the config documents them all — and, crucially,
// CheckConfigStaleness's Status is UNCHANGED across both (the nag never fires for
// the optional tier).
func TestCheckConfigDocs(t *testing.T) {
	dir := t.TempDir()

	// (1) canonical-complete but optional-absent.
	missing := filepath.Join(dir, "missing.toml")
	if err := os.WriteFile(missing, []byte(canonicalCompleteNoOptional), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := CheckConfigDocs(missing)
	if docs.Status != Info {
		t.Errorf("Config Docs status = %v, want Info (optional blocks absent): %s", docs.Status, docs.Summary)
	}
	if docs.Name != "Config Docs" {
		t.Errorf("row name = %q, want %q", docs.Name, "Config Docs")
	}
	// Staleness must stay Pass — the optional tier never enters its count.
	if st := CheckConfigStaleness(missing); st.Status != Pass {
		t.Errorf("staleness status = %v, want Pass (optional keys must not nag): %s", st.Status, st.Summary)
	}

	// (2) template-verbatim: optional blocks all present.
	complete := filepath.Join(dir, "complete.toml")
	if err := os.WriteFile(complete, []byte(storage.GenerateConfigTOML("/tmp", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	docs = CheckConfigDocs(complete)
	if docs.Status != Skip {
		t.Errorf("Config Docs status = %v, want Skip (all optional documented): %s", docs.Status, docs.Summary)
	}
	if st := CheckConfigStaleness(complete); st.Status != Pass {
		t.Errorf("staleness status on complete config = %v, want Pass", st.Status)
	}
}
