// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// TestUpgradeInjectsMetaActive verifies that `vp config upgrade` on a legacy
// config missing [meta] injects the section with uncommented version fields
// (so subsequent LoadConfig sees the correct schema version), while keeping
// kind commented as documentation.
func TestUpgradeInjectsMetaActive(t *testing.T) {
	legacy := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[embedder]
model = "test"
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
	canonical, err := CanonicalKeys()
	if err != nil {
		t.Fatal(err)
	}
	present := PresentKeys(legacy)
	missing := MissingKeys(canonical, present)
	if len(missing["meta"]) == 0 {
		t.Fatalf("meta keys should be missing, got: %v", missing)
	}
	blocks := ParseTemplateBlocks(templateToml)
	upgraded := UpgradeConfig(legacy, missing, blocks)

	// Must contain the new [meta] section with active version fields.
	if !strings.Contains(upgraded, "[meta]") {
		t.Fatalf("upgraded config missing [meta] section:\n%s", upgraded)
	}
	// Active assignment (no leading '#') for version_major / version_minor.
	if !containsActiveLine(upgraded, "version_major = 1") {
		t.Errorf("version_major should be active (uncommented) after upgrade:\n%s", upgraded)
	}
	if !containsActiveLine(upgraded, "version_minor = 0") {
		t.Errorf("version_minor should be active (uncommented) after upgrade:\n%s", upgraded)
	}
	// kind stays commented (documentation, not an enforced value).
	if containsActiveLine(upgraded, "kind = ") {
		t.Errorf("kind should remain commented:\n%s", upgraded)
	}

	// Idempotency: re-running upgrade must not duplicate [meta].
	present2 := PresentKeys(upgraded)
	missing2 := MissingKeys(canonical, present2)
	upgraded2 := UpgradeConfig(upgraded, missing2, blocks)
	if strings.Count(upgraded2, "[meta]") != 1 {
		t.Errorf("re-running upgrade duplicated [meta]:\n%s", upgraded2)
	}
	if strings.Count(upgraded2, "version_major") != 1 {
		t.Errorf("re-running upgrade duplicated version_major:\n%s", upgraded2)
	}
}

// containsActiveLine reports whether text contains a line whose trimmed form
// starts with the prefix (i.e., not commented out).
func containsActiveLine(text, prefix string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}
