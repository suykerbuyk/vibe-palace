// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalKeys(t *testing.T) {
	keys, err := CanonicalKeys()
	if err != nil {
		t.Fatalf("CanonicalKeys: %v", err)
	}

	// Spot-check expected keys from defaults.toml.
	for _, expected := range []string{
		"vault_path",
		"git_enabled",
		"http_port",
		"log_level",
		"embedder.model",
		"embedder.max_sequence_length",
		"embedder.batch_size",
		"search.default_limit",
		"search.structural_boost_wing",
		"chunker.max_chars",
		"chunker.overlap",
	} {
		if !keys[expected] {
			t.Errorf("missing canonical key: %s", expected)
		}
	}
}

func TestUserKeys(t *testing.T) {
	content := `vault_path = "/tmp/vault"
http_port = 8080

[embedder]
model = "test-model"
`
	var raw map[string]any
	keys := PresentKeys(content)

	_ = raw // PresentKeys works on raw text, not parsed TOML.

	if !keys["vault_path"] {
		t.Error("should detect vault_path")
	}
	if !keys["http_port"] {
		t.Error("should detect http_port")
	}
	if !keys["embedder.model"] {
		t.Error("should detect embedder.model")
	}
}

func TestPresentKeys_ScopeTracking(t *testing.T) {
	content := `vault_path = "/tmp"

[palace]
# [palace.llm]
# endpoint = "https://api.x.ai/v1"
# model = "grok-3-mini"

[search]
default_limit = 10
`
	keys := PresentKeys(content)

	// # endpoint under # [palace.llm] should resolve to palace.llm.endpoint.
	if !keys["palace.llm.endpoint"] {
		t.Error("should detect palace.llm.endpoint from commented section")
	}
	if !keys["palace.llm.model"] {
		t.Error("should detect palace.llm.model from commented section")
	}

	// Top-level key should not have a section prefix.
	if !keys["vault_path"] {
		t.Error("should detect top-level vault_path")
	}

	// search.default_limit should be correctly scoped.
	if !keys["search.default_limit"] {
		t.Error("should detect search.default_limit")
	}
}

func TestPresentKeys_ScopeReset(t *testing.T) {
	content := `# [palace.llm]
# endpoint = "test"

[search]
default_limit = 10
`
	keys := PresentKeys(content)

	// After uncommented [search], scope resets. default_limit is under search.
	if !keys["search.default_limit"] {
		t.Error("should detect search.default_limit after scope reset")
	}
	if !keys["palace.llm.endpoint"] {
		t.Error("should detect palace.llm.endpoint from commented section")
	}
	// endpoint should NOT appear at top level.
	if keys["endpoint"] {
		t.Error("endpoint should not appear at top level")
	}
}

func TestMissingKeys(t *testing.T) {
	canonical := map[string]bool{
		"vault_path":     true,
		"http_port":      true,
		"embedder.model": true,
		"search.limit":   true,
	}
	present := map[string]bool{
		"vault_path":     true,
		"embedder.model": true,
	}

	missing := MissingKeys(canonical, present)

	// Should have two groups: "" (top-level) and "search".
	if _, ok := missing[""]; !ok {
		t.Error("should have top-level missing keys")
	}
	if _, ok := missing["search"]; !ok {
		t.Error("should have search section missing keys")
	}

	// http_port should be in top-level group.
	found := false
	for _, k := range missing[""] {
		if k == "http_port" {
			found = true
		}
	}
	if !found {
		t.Error("http_port should be in top-level missing keys")
	}
}

func TestUpgradeNoOp(t *testing.T) {
	content := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[meta]
version_major = 1
version_minor = 0
kind = "global"

[embedder]
model = "sentence-transformers/all-MiniLM-L6-v2"
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
	canonical, _ := CanonicalKeys()
	present := PresentKeys(content)
	missing := MissingKeys(canonical, present)
	blocks := ParseTemplateBlocks(templateToml)

	result := UpgradeConfig(content, missing, blocks)
	if result != content {
		t.Errorf("full config should not be modified.\nGot:\n%s\nWant:\n%s", result, content)
	}
}

func TestUpgradeNewKeys(t *testing.T) {
	// Config missing git_enabled.
	content := `vault_path = "/tmp"
http_port = 7423

[embedder]
model = "test"
`
	canonical := map[string]bool{
		"vault_path":     true,
		"git_enabled":    true,
		"http_port":      true,
		"embedder.model": true,
	}
	present := PresentKeys(content)
	missing := MissingKeys(canonical, present)
	blocks := ParseTemplateBlocks(templateToml)

	result := UpgradeConfig(content, missing, blocks)

	// Should contain git_enabled as a commented-out line.
	if !strings.Contains(result, "git_enabled") {
		t.Error("upgraded config should contain git_enabled")
	}
}

func TestUpgradeNewSection(t *testing.T) {
	content := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"
`
	// Pretend embedder.model is missing.
	missing := map[string][]string{
		"embedder": {"embedder.model"},
	}
	blocks := ParseTemplateBlocks(templateToml)

	result := UpgradeConfig(content, missing, blocks)

	if !strings.Contains(result, "[embedder]") {
		t.Error("should add [embedder] section")
	}
	if !strings.Contains(result, "model") {
		t.Error("should add model key")
	}
}

func TestUpgradeIdempotent(t *testing.T) {
	content := `vault_path = "/tmp"
http_port = 7423
`
	canonical := map[string]bool{
		"vault_path":  true,
		"git_enabled": true,
		"http_port":   true,
	}
	blocks := ParseTemplateBlocks(templateToml)

	// First upgrade.
	present1 := PresentKeys(content)
	missing1 := MissingKeys(canonical, present1)
	result1 := UpgradeConfig(content, missing1, blocks)

	// Second upgrade on the result.
	present2 := PresentKeys(result1)
	missing2 := MissingKeys(canonical, present2)
	result2 := UpgradeConfig(result1, missing2, blocks)

	if result1 != result2 {
		t.Errorf("upgrade should be idempotent.\nFirst:\n%s\nSecond:\n%s", result1, result2)
	}
}

func TestUpgradePreservesUserContent(t *testing.T) {
	content := `# My custom vault config
vault_path = "/home/user/my-vault"

# I use a different port
http_port = 9999
`
	canonical := map[string]bool{
		"vault_path":  true,
		"git_enabled": true,
		"http_port":   true,
	}
	blocks := ParseTemplateBlocks(templateToml)
	present := PresentKeys(content)
	missing := MissingKeys(canonical, present)

	result := UpgradeConfig(content, missing, blocks)

	// User's comments and values must be preserved.
	if !strings.Contains(result, "My custom vault config") {
		t.Error("should preserve user comments")
	}
	if !strings.Contains(result, `"/home/user/my-vault"`) {
		t.Error("should preserve user vault_path value")
	}
	if !strings.Contains(result, "9999") {
		t.Error("should preserve user http_port value")
	}
}

func TestUpgradeEmptyParentSection(t *testing.T) {
	// [palace] exists but has no direct keys — only subsections.
	content := `vault_path = "/tmp"

[palace]
# Room keywords are configurable per project.
`
	// Pretend palace.scoring.min_score is a canonical key that's missing.
	missing := map[string][]string{
		"palace.scoring": {"palace.scoring.min_score"},
	}
	blocks := ParseTemplateBlocks(templateToml)

	result := UpgradeConfig(content, missing, blocks)

	// Should add the scoring section without corrupting existing content.
	if !strings.Contains(result, "min_score") {
		t.Error("should add min_score")
	}
	// Original content should be preserved.
	if !strings.Contains(result, "Room keywords") {
		t.Error("should preserve existing palace comments")
	}
}

func TestDetectMissingKeys(t *testing.T) {
	// Write a config missing git_enabled.
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "vibe-palace")
	os.MkdirAll(configDir, 0o755)
	configPath := filepath.Join(configDir, "config.toml")

	content := `vault_path = "/tmp"
http_port = 7423
log_level = "info"

[embedder]
model = "sentence-transformers/all-MiniLM-L6-v2"
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
	os.WriteFile(configPath, []byte(content), 0o644)

	missing, err := DetectMissingKeys(configPath)
	if err != nil {
		t.Fatalf("DetectMissingKeys: %v", err)
	}
	if len(missing) == 0 {
		t.Fatal("should detect missing git_enabled")
	}

	found := false
	for _, k := range missing {
		if k == "git_enabled" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing keys should include git_enabled, got: %v", missing)
	}
}

func TestDetectMissingKeys_UpToDate(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "vibe-palace")
	os.MkdirAll(configDir, 0o755)
	configPath := filepath.Join(configDir, "config.toml")

	content := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[meta]
version_major = 1
version_minor = 0
kind = "global"

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
	os.WriteFile(configPath, []byte(content), 0o644)

	missing, err := DetectMissingKeys(configPath)
	if err != nil {
		t.Fatalf("DetectMissingKeys: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("config should be up to date, but missing: %v", missing)
	}
}

func TestDetectMissingKeys_FileNotFound(t *testing.T) {
	_, err := DetectMissingKeys("/nonexistent/path/config.toml")
	if err == nil {
		t.Error("should return error for missing file")
	}
}

func TestEnsureParentSections(t *testing.T) {
	sectionMap := map[string]*SectionRange{
		"": {Name: "", StartLine: 0, EndLine: 5},
	}
	lines := []string{
		"vault_path = \"/tmp\"",
		"http_port = 7423",
	}

	// palace.scoring.rooms.ml needs parents: palace, palace.scoring, palace.scoring.rooms
	result := ensureParentSections("palace.scoring.rooms.ml", sectionMap, lines)

	if !strings.Contains(result, "[palace]") {
		t.Error("should create [palace] parent section")
	}
	if !strings.Contains(result, "[palace.scoring]") {
		t.Error("should create [palace.scoring] parent section")
	}
	if !strings.Contains(result, "[palace.scoring.rooms]") {
		t.Error("should create [palace.scoring.rooms] parent section")
	}
}

func TestEnsureParentSections_ExistingParent(t *testing.T) {
	sectionMap := map[string]*SectionRange{
		"":       {Name: "", StartLine: 0, EndLine: 5},
		"palace": {Name: "palace", StartLine: 5, EndLine: 10},
	}
	lines := []string{
		"vault_path = \"/tmp\"",
		"",
		"[palace]",
		"# comments",
	}

	// palace.scoring needs parent palace (exists), so only scoring should be added
	result := ensureParentSections("palace.scoring", sectionMap, lines)

	if strings.Contains(result, "[palace]") {
		t.Error("should NOT create [palace] — it already exists in sectionMap")
	}
}

func TestUpgradeSectionsInDifferentOrder(t *testing.T) {
	// User has sections in non-canonical order.
	content := `[chunker]
max_chars = 800
overlap = 100

[search]
default_limit = 10

vault_path = "/tmp"
`
	// Missing git_enabled (top-level).
	missing := map[string][]string{
		"": {"git_enabled"},
	}
	blocks := ParseTemplateBlocks(templateToml)

	result := UpgradeConfig(content, missing, blocks)
	if !strings.Contains(result, "git_enabled") {
		t.Error("should add git_enabled even with non-canonical section order")
	}
}

func TestParseSectionHeader(t *testing.T) {
	tests := []struct {
		line      string
		commented bool
		want      string
		ok        bool
	}{
		{"[embedder]", false, "embedder", true},
		{"[palace.scoring.rooms]", false, "palace.scoring.rooms", true},
		{"# [palace.llm]", true, "palace.llm", true},
		{"not a section", false, "", false},
		{"[[array]]", false, "", false},
		{"# just a comment", true, "", false},
	}

	for _, tt := range tests {
		got, ok := parseSectionHeader(tt.line, tt.commented)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseSectionHeader(%q, %v) = (%q, %v), want (%q, %v)",
				tt.line, tt.commented, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseKeyAssignment(t *testing.T) {
	tests := []struct {
		line      string
		commented bool
		want      string
		ok        bool
	}{
		{"model = \"test\"", false, "model", true},
		{"max_chars = 800", false, "max_chars", true},
		{"# endpoint = \"test\"", true, "endpoint", true},
		{"not a key", false, "", false},
		{"# just a comment", true, "", false},
	}

	for _, tt := range tests {
		got, ok := parseKeyAssignment(tt.line, tt.commented)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseKeyAssignment(%q, %v) = (%q, %v), want (%q, %v)",
				tt.line, tt.commented, got, ok, tt.want, tt.ok)
		}
	}
}
