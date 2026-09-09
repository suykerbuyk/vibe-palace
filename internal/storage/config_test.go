// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	v := testVault(t)

	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.HTTPPort != 7423 {
		t.Errorf("HTTPPort = %d, want 7423", cfg.HTTPPort)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.EmbedderModel != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Errorf("EmbedderModel = %q, want %q", cfg.EmbedderModel, "sentence-transformers/all-MiniLM-L6-v2")
	}
	if cfg.EmbedderMaxSeqLen != 256 {
		t.Errorf("EmbedderMaxSeqLen = %d, want 256", cfg.EmbedderMaxSeqLen)
	}
	if cfg.EmbedderBatchSize != 32 {
		t.Errorf("EmbedderBatchSize = %d, want 32", cfg.EmbedderBatchSize)
	}
	if cfg.SearchDefaultLimit != 10 {
		t.Errorf("SearchDefaultLimit = %d, want 10", cfg.SearchDefaultLimit)
	}
	if cfg.BoostWing != 0.12 {
		t.Errorf("BoostWing = %f, want 0.12", cfg.BoostWing)
	}
	if cfg.BoostHall != 0.24 {
		t.Errorf("BoostHall = %f, want 0.24", cfg.BoostHall)
	}
	if cfg.BoostRoom != 0.34 {
		t.Errorf("BoostRoom = %f, want 0.34", cfg.BoostRoom)
	}
}

func TestLoadConfigProjectOverride(t *testing.T) {
	v := testVault(t)

	// Create project config that overrides some values.
	projDir := filepath.Join(v.Root, "Projects", "proj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	projConfig := `
log_level = "debug"
http_port = 9999

[search]
default_limit = 50
`
	if err := os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(projConfig), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Overridden values.
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.HTTPPort != 9999 {
		t.Errorf("HTTPPort = %d, want 9999", cfg.HTTPPort)
	}
	if cfg.SearchDefaultLimit != 50 {
		t.Errorf("SearchDefaultLimit = %d, want 50", cfg.SearchDefaultLimit)
	}

	// Non-overridden defaults preserved.
	if cfg.EmbedderModel != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Errorf("EmbedderModel = %q, want default", cfg.EmbedderModel)
	}
	if cfg.BoostWing != 0.12 {
		t.Errorf("BoostWing = %f, want default 0.12", cfg.BoostWing)
	}
}

func TestLoadConfigMissingProject(t *testing.T) {
	v := testVault(t)

	// No project config file — should just use defaults.
	cfg, err := v.LoadConfig("nonexistent")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HTTPPort != 7423 {
		t.Errorf("HTTPPort = %d, want default 7423", cfg.HTTPPort)
	}
}

func TestDefaultsTomlEmbedded(t *testing.T) {
	if defaultsToml == "" {
		t.Error("defaultsToml should not be empty")
	}
}

func TestConfigPalaceRooms(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[palace.rooms.audio]
keywords = ["wav", "mp3", "codec"]

[palace.rooms.graphics]
keywords = ["opengl", "vulkan", "shader"]
`), 0644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.PalaceRoomKeywords == nil {
		t.Fatal("PalaceRoomKeywords should not be nil")
	}
	if got := cfg.PalaceRoomKeywords["audio"]; len(got) != 3 || got[0] != "wav" {
		t.Errorf("audio keywords = %v, want [wav mp3 codec]", got)
	}
	if got := cfg.PalaceRoomKeywords["graphics"]; len(got) != 3 || got[0] != "opengl" {
		t.Errorf("graphics keywords = %v, want [opengl vulkan shader]", got)
	}
}

func TestConfigPalaceRoomsEmpty(t *testing.T) {
	v := testVault(t)

	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceRoomKeywords != nil {
		t.Errorf("PalaceRoomKeywords should be nil when no rooms configured, got %v", cfg.PalaceRoomKeywords)
	}
}

func TestConfigPalaceRoomsProjectOverridesVault(t *testing.T) {
	v := testVault(t)

	// Simulate vault-level config with palace rooms.
	// We can't easily write vault-level config in test (it uses UserConfigDir),
	// so we test that project-level rooms fully replace by verifying the TOML
	// layering: last decode wins for map fields.
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[palace.rooms.custom]
keywords = ["only-this"]
`), 0644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if len(cfg.PalaceRoomKeywords) != 1 {
		t.Errorf("expected exactly 1 room (project replaces vault), got %d: %v",
			len(cfg.PalaceRoomKeywords), cfg.PalaceRoomKeywords)
	}
	if _, ok := cfg.PalaceRoomKeywords["custom"]; !ok {
		t.Error("expected 'custom' room from project config")
	}
}

func TestConfigPalaceScoring(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[palace.scoring]
min_score = 0.4

[palace.scoring.rooms.testing]
high = ["integration test", "e2e test"]
medium = ["spec"]

[palace.scoring.rooms.ml]
high = ["neural network"]
medium = ["training"]
low = ["epoch"]
`), 0644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.PalaceMinScore != 0.4 {
		t.Errorf("PalaceMinScore = %f, want 0.4", cfg.PalaceMinScore)
	}
	if cfg.PalaceScoringOverrides == nil {
		t.Fatal("PalaceScoringOverrides should not be nil")
	}
	if got := cfg.PalaceScoringOverrides["testing"]; len(got.High) != 2 || got.High[0] != "integration test" {
		t.Errorf("testing overrides = %+v, want high=[integration test, e2e test]", got)
	}
	if got := cfg.PalaceScoringOverrides["ml"]; len(got.High) != 1 || got.High[0] != "neural network" {
		t.Errorf("ml overrides = %+v", got)
	}
	if got := cfg.PalaceScoringOverrides["ml"]; len(got.Low) != 1 || got.Low[0] != "epoch" {
		t.Errorf("ml low = %+v, want [epoch]", got)
	}
}

func TestConfigPalaceScoringEmpty(t *testing.T) {
	v := testVault(t)
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceScoringOverrides != nil {
		t.Errorf("PalaceScoringOverrides should be nil when not configured, got %v", cfg.PalaceScoringOverrides)
	}
	if cfg.PalaceMinScore != 0 {
		t.Errorf("PalaceMinScore should be 0 when not configured, got %f", cfg.PalaceMinScore)
	}
}

func TestConfigPalaceLLM(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[palace.llm]
endpoint = "https://api.x.ai/v1"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
max_tokens = 4096
`), 0644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.PalaceLLM.Endpoint != "https://api.x.ai/v1" {
		t.Errorf("Endpoint = %q, want %q", cfg.PalaceLLM.Endpoint, "https://api.x.ai/v1")
	}
	if cfg.PalaceLLM.Model != "grok-3-mini" {
		t.Errorf("Model = %q, want %q", cfg.PalaceLLM.Model, "grok-3-mini")
	}
	if cfg.PalaceLLM.APIKeyEnv != "XAI_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want %q", cfg.PalaceLLM.APIKeyEnv, "XAI_API_KEY")
	}
	if cfg.PalaceLLM.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", cfg.PalaceLLM.MaxTokens)
	}
}

func TestConfigPalaceLLMEmpty(t *testing.T) {
	// Isolate from any host global ~/.config/vibe-palace/config.toml (which
	// on developer machines may have [palace.llm] configured). Point XDG to
	// an empty temp dir so LoadConfig sees only embedded defaults.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	v := testVault(t)

	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceLLM.Endpoint != "" {
		t.Errorf("PalaceLLM.Endpoint should be empty, got %q", cfg.PalaceLLM.Endpoint)
	}
}

func TestConfigEnrichment(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[enrichment]
enabled = true
provider = "xai"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
base_url = "https://api.x.ai/v1"
max_tokens = 4096
timeout_seconds = 30
`), 0644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if !cfg.Enrichment.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if cfg.Enrichment.Provider != "xai" {
		t.Errorf("Provider = %q, want %q", cfg.Enrichment.Provider, "xai")
	}
	if cfg.Enrichment.Model != "grok-3-mini" {
		t.Errorf("Model = %q, want %q", cfg.Enrichment.Model, "grok-3-mini")
	}
	if cfg.Enrichment.APIKeyEnv != "XAI_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want %q", cfg.Enrichment.APIKeyEnv, "XAI_API_KEY")
	}
	if cfg.Enrichment.BaseURL != "https://api.x.ai/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.Enrichment.BaseURL, "https://api.x.ai/v1")
	}
	if cfg.Enrichment.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", cfg.Enrichment.MaxTokens)
	}
	if cfg.Enrichment.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", cfg.Enrichment.TimeoutSeconds)
	}
}

func TestConfigEnrichmentEmpty(t *testing.T) {
	// Isolate from any host global ~/.config/vibe-palace/config.toml (which
	// on developer machines often has [enrichment] enabled). Point XDG to
	// empty temp so LoadConfig sees only embedded defaults (enrichment off).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	v := testVault(t)

	// No [enrichment] block — Config.Enrichment must be the zero value.
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Enrichment != (EnrichmentConfig{}) {
		t.Errorf("Enrichment should be zero value when no block configured, got %+v", cfg.Enrichment)
	}
	if cfg.Enrichment.Enabled {
		t.Errorf("Enrichment.Enabled should be false by default, got true")
	}
}

func TestCurrentVersionMinor(t *testing.T) {
	if CurrentVersionMinor != 1 {
		t.Errorf("CurrentVersionMinor = %d, want 1", CurrentVersionMinor)
	}
}

func TestWriteScoringConfig_NewFile(t *testing.T) {
	v := testVault(t)

	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"integration test"}, Medium: []string{"spec"}},
		"ml":      {High: []string{"neural network"}, Low: []string{"epoch"}},
	}
	if err := v.WriteScoringConfig("proj", rooms, 0.5); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	// Reload and verify.
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0.5 {
		t.Errorf("PalaceMinScore = %f, want 0.5", cfg.PalaceMinScore)
	}
	if cfg.PalaceScoringOverrides == nil {
		t.Fatal("PalaceScoringOverrides should not be nil")
	}
	if got := cfg.PalaceScoringOverrides["testing"]; len(got.High) != 1 || got.High[0] != "integration test" {
		t.Errorf("testing.High = %v, want [integration test]", got.High)
	}
	if got := cfg.PalaceScoringOverrides["ml"]; len(got.Low) != 1 || got.Low[0] != "epoch" {
		t.Errorf("ml.Low = %v, want [epoch]", got.Low)
	}
}

func TestWriteScoringConfig_MergeExisting(t *testing.T) {
	v := testVault(t)

	// Write initial config.
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
log_level = "debug"

[palace.scoring]
min_score = 0.4

[palace.scoring.rooms.testing]
high = ["e2e test"]
`), 0644)

	// Merge new scoring.
	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"integration test"}},
		"ml":      {High: []string{"neural network"}},
	}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Existing testing.high should have both keywords.
	got := cfg.PalaceScoringOverrides["testing"]
	if len(got.High) != 2 {
		t.Fatalf("testing.High = %v, want 2 entries", got.High)
	}
	// New room should be added.
	if _, ok := cfg.PalaceScoringOverrides["ml"]; !ok {
		t.Error("expected ml room")
	}
	// min_score preserved (we passed 0, so it shouldn't change).
	if cfg.PalaceMinScore != 0.4 {
		t.Errorf("PalaceMinScore = %f, want 0.4 (preserved)", cfg.PalaceMinScore)
	}
}

func TestWriteScoringConfig_Idempotent(t *testing.T) {
	v := testVault(t)

	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"integration test"}},
	}
	if err := v.WriteScoringConfig("proj", rooms, 0.5); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Write same data again.
	if err := v.WriteScoringConfig("proj", rooms, 0.5); err != nil {
		t.Fatalf("second write: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got := cfg.PalaceScoringOverrides["testing"]
	if len(got.High) != 1 {
		t.Errorf("testing.High = %v, want exactly 1 entry (no duplicates)", got.High)
	}
}

func TestWriteScoringConfig_NoMinScoreOverride(t *testing.T) {
	v := testVault(t)

	// Write with minScore = 0 should not set min_score in config.
	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"test"}},
	}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0 {
		t.Errorf("PalaceMinScore = %f, want 0 (should not be set)", cfg.PalaceMinScore)
	}
}

func TestWriteScoringConfig_EmptyRooms(t *testing.T) {
	v := testVault(t)
	// Writing with empty rooms map should still create the file.
	if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{}, 0.5); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0.5 {
		t.Errorf("PalaceMinScore = %f, want 0.5", cfg.PalaceMinScore)
	}
}

func TestWriteScoringConfig_PreservesOtherConfig(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
log_level = "trace"
http_port = 8888

[embedder]
batch_size = 64
`), 0644)

	rooms := map[string]ScoringRoomOverride{
		"ml": {High: []string{"transformer"}},
	}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LogLevel != "trace" {
		t.Errorf("LogLevel = %q, want %q (preserved)", cfg.LogLevel, "trace")
	}
	if cfg.HTTPPort != 8888 {
		t.Errorf("HTTPPort = %d, want 8888 (preserved)", cfg.HTTPPort)
	}
	if cfg.EmbedderBatchSize != 64 {
		t.Errorf("EmbedderBatchSize = %d, want 64 (preserved)", cfg.EmbedderBatchSize)
	}
}

func TestWriteScoringConfig_InvalidProject(t *testing.T) {
	v := testVault(t)
	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"test"}},
	}
	err := v.WriteScoringConfig("INVALID SLUG!", rooms, 0)
	if err == nil {
		t.Error("expected error for invalid project slug")
	}
}

func TestWriteScoringConfig_CorruptExisting(t *testing.T) {
	v := testVault(t)
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("not valid toml [[["), 0644)

	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"test"}},
	}
	err := v.WriteScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Error("expected error for corrupt TOML")
	}
}

func TestConfigThreeLevelPrecedence(t *testing.T) {
	v := testVault(t)

	// Create project config.
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[embedder]
batch_size = 64
`), 0644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Project overrides embedder.batch_size.
	if cfg.EmbedderBatchSize != 64 {
		t.Errorf("EmbedderBatchSize = %d, want 64 (project override)", cfg.EmbedderBatchSize)
	}
	// Embedded default for embedder.model still present.
	if cfg.EmbedderModel != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Errorf("EmbedderModel = %q, want default", cfg.EmbedderModel)
	}
}

// --- WriteScoringConfig absent-field regression coverage ------------------
//
// The tests below exercise the bug this task fixes: WriteScoringConfig used
// to decode/re-encode the FULL tomlConfig struct, which turned every
// previously-absent (inherited) field into an explicit, present zero value.
// They isolate from the host's real global config the same way
// TestConfigEnrichmentEmpty does, then seed a distinctive vault-tier config
// so any accidental zeroing is unambiguous (a zeroed field could otherwise
// coincide with a real default and hide the bug).

// writeVaultTierConfig isolates XDG_CONFIG_HOME to a fresh temp dir and
// writes content as the vault-level config.toml there. Must be called
// before any LoadConfig/WriteScoringConfig call in the test.
func writeVaultTierConfig(t *testing.T, content string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := VaultConfigFilePath()
	if err != nil {
		t.Fatalf("VaultConfigFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const vaultTierFixture = `
git_enabled = true
http_port = 9001
vault_path = "/vault-tier-path"

[palace.llm]
endpoint = "https://vault-tier-llm.example"
model = "vault-model"
api_key_env = "VAULT_KEY"
max_tokens = 2048
`

func assertVaultTierInherited(t *testing.T, cfg Config) {
	t.Helper()
	if !cfg.GitEnabled {
		t.Errorf("GitEnabled = false, want true (vault tier)")
	}
	if cfg.HTTPPort != 9001 {
		t.Errorf("HTTPPort = %d, want 9001 (vault tier)", cfg.HTTPPort)
	}
	if cfg.VaultPath != "/vault-tier-path" {
		t.Errorf("VaultPath = %q, want /vault-tier-path (vault tier)", cfg.VaultPath)
	}
	if cfg.PalaceLLM.Endpoint != "https://vault-tier-llm.example" {
		t.Errorf("PalaceLLM.Endpoint = %q, want vault-tier value", cfg.PalaceLLM.Endpoint)
	}
	if cfg.PalaceLLM.Model != "vault-model" {
		t.Errorf("PalaceLLM.Model = %q, want vault-model", cfg.PalaceLLM.Model)
	}
	if cfg.PalaceLLM.APIKeyEnv != "VAULT_KEY" {
		t.Errorf("PalaceLLM.APIKeyEnv = %q, want VAULT_KEY", cfg.PalaceLLM.APIKeyEnv)
	}
	if cfg.PalaceLLM.MaxTokens != 2048 {
		t.Errorf("PalaceLLM.MaxTokens = %d, want 2048", cfg.PalaceLLM.MaxTokens)
	}
}

func assertProjectFileClean(t *testing.T, cfgPath string) {
	t.Helper()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	content := string(data)
	for _, forbidden := range []string{"git_enabled", "http_port", "vault_path", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("project config file unexpectedly contains %q after WriteScoringConfig:\n%s", forbidden, content)
		}
	}
}

// TestWriteScoringConfig_PreservesAbsentFields is the core regression test:
// a project config file whose non-scoring fields are ALL absent (the normal,
// documented vault_project_template.toml shape) must still inherit every one
// of those fields from the vault tier after a scoring-only write — including
// the [palace.llm] block whose zeroing broke `vp discover/tune rooms` in
// production.
func TestWriteScoringConfig_PreservesAbsentFields(t *testing.T) {
	writeVaultTierConfig(t, vaultTierFixture)
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.toml")
	// Mirrors vault_project_template.toml: only [meta], everything else absent.
	os.WriteFile(cfgPath, []byte("[meta]\nversion_major = 1\nversion_minor = 0\n"), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	assertVaultTierInherited(t, cfg)
	assertProjectFileClean(t, cfgPath)
}

// TestWriteScoringConfig_PreservesAbsentFields_Idempotent pairs the above
// with a second write against the now-once-merged file: a re-run must not
// reintroduce the corruption on the second pass either.
func TestWriteScoringConfig_PreservesAbsentFields_Idempotent(t *testing.T) {
	writeVaultTierConfig(t, vaultTierFixture)
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.toml")
	os.WriteFile(cfgPath, []byte("[meta]\nversion_major = 1\nversion_minor = 0\n"), 0644)

	if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{
		"ml": {High: []string{"transformer"}},
	}, 0); err != nil {
		t.Fatalf("first WriteScoringConfig: %v", err)
	}
	if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{
		"ml": {High: []string{"another-keyword"}},
	}, 0); err != nil {
		t.Fatalf("second WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	assertVaultTierInherited(t, cfg)
	assertProjectFileClean(t, cfgPath)

	high := cfg.PalaceScoringOverrides["ml"].High
	wantKeywords := map[string]bool{"transformer": false, "another-keyword": false}
	for _, kw := range high {
		if _, ok := wantKeywords[kw]; ok {
			wantKeywords[kw] = true
		}
	}
	for kw, found := range wantKeywords {
		if !found {
			t.Errorf("expected keyword %q in ml.High, got %v", kw, high)
		}
	}
	if len(high) != 2 {
		t.Errorf("ml.High = %v, want exactly 2 entries (no duplicates)", high)
	}
}

// TestWriteScoringConfig_PreservesMinScoreInheritance covers the in-scope
// tomlScoringConfig.MinScore instance of the same absent-vs-zero bug: an
// absent [palace.scoring] in the project file must inherit min_score from
// the vault tier, not clobber it with the Go float64 zero value.
func TestWriteScoringConfig_PreservesMinScoreInheritance(t *testing.T) {
	writeVaultTierConfig(t, "[palace.scoring]\nmin_score = 0.6\n")
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("[meta]\nversion_major = 1\n"), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	// 0 is the sentinel every real caller passes when it isn't overriding min_score.
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0.6 {
		t.Errorf("PalaceMinScore = %f, want 0.6 (inherited from vault tier)", cfg.PalaceMinScore)
	}
}

// TestWriteScoringConfig_ExplicitMinScoreZeroSurvives covers the flip side:
// a project file that explicitly sets min_score = 0 (a deliberate override,
// not an omission) must keep that explicit zero rather than have it read
// back as "absent" and silently re-inherit the vault tier's value.
func TestWriteScoringConfig_ExplicitMinScoreZeroSurvives(t *testing.T) {
	writeVaultTierConfig(t, "[palace.scoring]\nmin_score = 0.6\n")
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("[palace.scoring]\nmin_score = 0.0\n"), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0 {
		t.Errorf("PalaceMinScore = %f, want 0 (explicit project override preserved)", cfg.PalaceMinScore)
	}
}

// TestWriteScoringConfig_ExplicitZeroSurvives is the non-scoring counterpart:
// an explicit http_port = 0 in the project file is a real, deliberate
// override (not an omission) and must survive the map-based rewrite exactly
// as it would have survived the old struct-based one — the map approach
// can't overcorrect into treating an explicit zero as absent (the key is
// present in the decoded map either way), but this pins it as a regression
// guard.
func TestWriteScoringConfig_ExplicitZeroSurvives(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("http_port = 0\n"), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HTTPPort != 0 {
		t.Errorf("HTTPPort = %d, want 0 (explicit override preserved)", cfg.HTTPPort)
	}
}

// TestWriteScoringConfig_MalformedPalaceTable exercises the checked
// type-assertion guards in the navigation code: a scalar where a table is
// expected must produce a clear error, not a panic. The old struct-decode
// path would have rejected this at toml.DecodeFile time with its own
// type-mismatch error; the map path must be equally clear since
// map[string]any doesn't reject a mismatched *value* shape at decode time.
func TestWriteScoringConfig_MalformedPalaceTable(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("palace = \"not-a-table\"\n"), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	err := v.WriteScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Fatal("expected error for malformed [palace] table, got nil")
	}
	if !strings.Contains(err.Error(), "palace") {
		t.Errorf("error should mention 'palace': %v", err)
	}
}

// TestWriteScoringConfig_MergesExistingRoomKeywords covers scenario 5: an
// existing [palace.scoring.rooms] table with high/medium/low tiers already
// populated must survive a merge that adds new keywords to an EXISTING room
// and introduces a brand-NEW room, with no duplication in either.
func TestWriteScoringConfig_MergesExistingRoomKeywords(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[palace.scoring.rooms.devops]
high = ["kubernetes"]
medium = ["docker"]
low = ["deploy"]
`), 0644)

	rooms := map[string]ScoringRoomOverride{
		"devops": {High: []string{"kubernetes", "terraform"}, Medium: []string{"docker"}, Low: []string{"pipeline"}},
		"ml":     {High: []string{"transformer"}},
	}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	devops := cfg.PalaceScoringOverrides["devops"]
	if len(devops.High) != 2 {
		t.Errorf("devops.High = %v, want 2 entries (kubernetes, terraform)", devops.High)
	}
	if len(devops.Medium) != 1 {
		t.Errorf("devops.Medium = %v, want 1 entry (docker, no duplicate)", devops.Medium)
	}
	if len(devops.Low) != 2 {
		t.Errorf("devops.Low = %v, want 2 entries (deploy, pipeline)", devops.Low)
	}

	ml, ok := cfg.PalaceScoringOverrides["ml"]
	if !ok {
		t.Fatal("expected new 'ml' room to be added")
	}
	if len(ml.High) != 1 || ml.High[0] != "transformer" {
		t.Errorf("ml.High = %v, want [transformer]", ml.High)
	}
}

// TestWriteScoringConfig_MixedExplicitAndAbsent covers scenario 3: a project
// file with a MIX of explicit non-default overrides and absent fields — the
// explicit ones must survive unchanged, the absent ones must stay
// absent/inherited.
func TestWriteScoringConfig_MixedExplicitAndAbsent(t *testing.T) {
	writeVaultTierConfig(t, vaultTierFixture)
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.toml")
	os.WriteFile(cfgPath, []byte(`
log_level = "trace"
http_port = 8888

[embedder]
batch_size = 64
`), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Explicit overrides survive.
	if cfg.LogLevel != "trace" {
		t.Errorf("LogLevel = %q, want trace (explicit override)", cfg.LogLevel)
	}
	if cfg.HTTPPort != 8888 {
		t.Errorf("HTTPPort = %d, want 8888 (explicit override)", cfg.HTTPPort)
	}
	if cfg.EmbedderBatchSize != 64 {
		t.Errorf("EmbedderBatchSize = %d, want 64 (explicit override)", cfg.EmbedderBatchSize)
	}
	// Absent fields inherit from the vault tier, not the project's own zero value.
	if cfg.VaultPath != "/vault-tier-path" {
		t.Errorf("VaultPath = %q, want vault-tier value (absent in project)", cfg.VaultPath)
	}
	if !cfg.GitEnabled {
		t.Errorf("GitEnabled = false, want true (absent, vault tier)")
	}
	if cfg.PalaceLLM.Endpoint != "https://vault-tier-llm.example" {
		t.Errorf("PalaceLLM.Endpoint = %q, want vault-tier value (absent in project)", cfg.PalaceLLM.Endpoint)
	}

	// The project file itself must not have gained these keys.
	data, _ := os.ReadFile(cfgPath)
	content := string(data)
	for _, forbidden := range []string{"vault_path =", "git_enabled =", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("project config unexpectedly gained %q:\n%s", forbidden, content)
		}
	}
}

// TestWriteScoringConfig_FreshCreateOnlyHasScoringKeys extends the
// fresh-create case: with no existing config.toml, the map starts empty
// (not a zero-value tomlConfig), so the write produces a file containing
// ONLY the [palace.scoring...] keys this call actually set — no [meta], no
// zeroed git_enabled/http_port/vault_path/[palace.llm]. This is a documented
// behavior change from the pre-fix struct writer (which emitted a spurious
// zeroed [meta] and every other field); confirm it's harmless by reloading.
func TestWriteScoringConfig_FreshCreateOnlyHasScoringKeys(t *testing.T) {
	// Sandbox against the real machine's global config: this test asserts
	// the embedded default HTTPPort (7423) survives untouched, which only
	// holds if there's no real ~/.config/vibe-palace/config.toml on the
	// developer's machine shadowing it.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := testVault(t)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	if err := v.WriteScoringConfig("proj", rooms, 0.5); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	cfgPath, err := v.ProjectConfigFile("proj")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "[meta]") {
		t.Errorf("fresh-create should not emit [meta], got:\n%s", content)
	}
	for _, forbidden := range []string{"git_enabled", "http_port", "vault_path", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("fresh-create unexpectedly contains %q:\n%s", forbidden, content)
		}
	}
	if !strings.Contains(content, "[palace.scoring") {
		t.Errorf("fresh-create should contain [palace.scoring...], got:\n%s", content)
	}

	// Reload still succeeds and inherits everything else from embedded/vault tiers.
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig after fresh create: %v", err)
	}
	if cfg.PalaceMinScore != 0.5 {
		t.Errorf("PalaceMinScore = %f, want 0.5", cfg.PalaceMinScore)
	}
	if cfg.HTTPPort != 7423 {
		t.Errorf("HTTPPort = %d, want embedded default 7423", cfg.HTTPPort)
	}
}

// TestWriteScoringConfig_NoOpDoesNotTouchFile covers Fix 2: a call with an
// empty rooms map AND minScore at the "no override" sentinel (<= 0) has
// nothing to write, and must not create a file (or gain empty
// [palace]/[palace.scoring]/[palace.scoring.rooms] headers in an existing
// one) that wasn't already there.
func TestWriteScoringConfig_NoOpDoesNotTouchFile(t *testing.T) {
	t.Run("no config file stays absent", func(t *testing.T) {
		v := testVault(t)

		if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{}, 0); err != nil {
			t.Fatalf("WriteScoringConfig: %v", err)
		}

		cfgPath, err := v.ProjectConfigFile("proj")
		if err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected no config file to be created, stat err = %v", statErr)
		}
	})

	t.Run("negative minScore is also a no-op", func(t *testing.T) {
		v := testVault(t)

		if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{}, -1); err != nil {
			t.Fatalf("WriteScoringConfig: %v", err)
		}

		cfgPath, err := v.ProjectConfigFile("proj")
		if err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected no config file to be created, stat err = %v", statErr)
		}
	})

	t.Run("existing config file is byte-identical after a no-op call", func(t *testing.T) {
		v := testVault(t)

		projDir := filepath.Join(v.Root, "Projects", "proj")
		if err := os.MkdirAll(projDir, 0755); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(projDir, "config.toml")
		before := []byte("log_level = \"trace\"\nhttp_port = 8888\n")
		if err := os.WriteFile(cfgPath, before, 0644); err != nil {
			t.Fatal(err)
		}

		if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{}, 0); err != nil {
			t.Fatalf("WriteScoringConfig: %v", err)
		}

		after, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read project config: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("no-op call mutated the file:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

// TestWriteScoringConfig_MalformedTierScalar covers Fix 1: a tier value that
// decodes to a scalar instead of an array (`high = "oops-not-an-array"`)
// must produce a clear error instead of silently overwriting whatever was
// there.
func TestWriteScoringConfig_MalformedTierScalar(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.toml")
	original := "[palace.scoring.rooms.ml]\nhigh = \"oops-not-an-array\"\n"
	os.WriteFile(cfgPath, []byte(original), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	err := v.WriteScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Fatal("expected error for non-array tier value, got nil")
	}
	if !strings.Contains(err.Error(), "high") {
		t.Errorf("error should mention 'high': %v", err)
	}

	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("read project config: %v", readErr)
	}
	if string(data) != original {
		t.Errorf("original malformed value should survive a failed write, got:\n%s", data)
	}
}

// TestWriteScoringConfig_MalformedTierSubTable covers the sub-table variant
// of Fix 1: `[palace.scoring.rooms.ml.high]` written as a table instead of
// an array must error, not silently destroy the sub-table's contents.
func TestWriteScoringConfig_MalformedTierSubTable(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.toml")
	original := "[palace.scoring.rooms.ml.high]\nweight = 5\n"
	os.WriteFile(cfgPath, []byte(original), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	err := v.WriteScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Fatal("expected error for sub-table tier value, got nil")
	}
	if !strings.Contains(err.Error(), "high") {
		t.Errorf("error should mention 'high': %v", err)
	}

	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("read project config: %v", readErr)
	}
	if string(data) != original {
		t.Errorf("original sub-table should survive a failed write, got:\n%s", data)
	}
}

// TestWriteScoringConfig_MixedTypeTierArray covers the mixed-type array
// case of Fix 1 (`high = ["neural network", 42, true]`): this pins the
// chosen behavior — a clear error — so a non-string element can never again
// be silently dropped from the tier.
func TestWriteScoringConfig_MixedTypeTierArray(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.toml")
	original := "[palace.scoring.rooms.ml]\nhigh = [\"neural network\", 42, true]\n"
	os.WriteFile(cfgPath, []byte(original), 0644)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	err := v.WriteScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Fatal("expected error for mixed-type tier array, got nil")
	}
	if !strings.Contains(err.Error(), "high") {
		t.Errorf("error should mention 'high': %v", err)
	}

	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("read project config: %v", readErr)
	}
	if string(data) != original {
		t.Errorf("original mixed-type array should survive a failed write, got:\n%s", data)
	}
}

// TestWriteScoringConfig_PreservesNonScoringFloatField covers Fix 4: a
// generic float config field OUTSIDE the scoring section — [search]
// structural_boost_wing — through the same absent/explicit-zero/
// explicit-real-value permutations already covered for min_score, since a
// float zero value is exactly the shape the original bug silently produced.
func TestWriteScoringConfig_PreservesNonScoringFloatField(t *testing.T) {
	t.Run("absent inherits from vault tier", func(t *testing.T) {
		writeVaultTierConfig(t, "[search]\nstructural_boost_wing = 0.42\n")
		v := testVault(t)

		projDir := filepath.Join(v.Root, "Projects", "proj")
		os.MkdirAll(projDir, 0755)
		os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("[meta]\nversion_major = 1\n"), 0644)

		rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
		if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
			t.Fatalf("WriteScoringConfig: %v", err)
		}

		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.BoostWing != 0.42 {
			t.Errorf("BoostWing = %f, want 0.42 (inherited from vault tier)", cfg.BoostWing)
		}
	})

	t.Run("explicit zero survives", func(t *testing.T) {
		writeVaultTierConfig(t, "[search]\nstructural_boost_wing = 0.42\n")
		v := testVault(t)

		projDir := filepath.Join(v.Root, "Projects", "proj")
		os.MkdirAll(projDir, 0755)
		os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("[search]\nstructural_boost_wing = 0.0\n"), 0644)

		rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
		if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
			t.Fatalf("WriteScoringConfig: %v", err)
		}

		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.BoostWing != 0 {
			t.Errorf("BoostWing = %f, want 0 (explicit project override preserved)", cfg.BoostWing)
		}
	})

	t.Run("explicit real value survives", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		v := testVault(t)

		projDir := filepath.Join(v.Root, "Projects", "proj")
		os.MkdirAll(projDir, 0755)
		os.WriteFile(filepath.Join(projDir, "config.toml"), []byte("[search]\nstructural_boost_wing = 0.77\n"), 0644)

		rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
		if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
			t.Fatalf("WriteScoringConfig: %v", err)
		}

		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.BoostWing != 0.77 {
			t.Errorf("BoostWing = %f, want 0.77 (explicit project override preserved)", cfg.BoostWing)
		}
	})
}
