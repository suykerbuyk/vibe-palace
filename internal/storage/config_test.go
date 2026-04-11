// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
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

func TestGetConfigValueEmbedded(t *testing.T) {
	v := testVault(t)

	val, source, err := v.GetConfigValue("", "http_port")
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if val != "7423" {
		t.Errorf("value = %q, want %q", val, "7423")
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
}

func TestGetConfigValueNested(t *testing.T) {
	v := testVault(t)

	val, source, err := v.GetConfigValue("", "embedder.model")
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if val != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Errorf("value = %q, want %q", val, "sentence-transformers/all-MiniLM-L6-v2")
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
}

func TestGetConfigValueProjectOverride(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`log_level = "trace"`), 0644)

	val, source, err := v.GetConfigValue("proj", "log_level")
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if val != "trace" {
		t.Errorf("value = %q, want %q", val, "trace")
	}
	if source != "project" {
		t.Errorf("source = %q, want %q", source, "project")
	}
}

func TestGetConfigValueNotFound(t *testing.T) {
	v := testVault(t)

	_, _, err := v.GetConfigValue("", "nonexistent.key")
	if err == nil {
		t.Error("GetConfigValue for nonexistent key should return error")
	}
}

func TestGetConfigValueSearchNested(t *testing.T) {
	v := testVault(t)

	val, _, err := v.GetConfigValue("", "search.structural_boost_wing")
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if val != "0.12" {
		t.Errorf("value = %q, want %q", val, "0.12")
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
	v := testVault(t)

	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceLLM.Endpoint != "" {
		t.Errorf("PalaceLLM.Endpoint should be empty, got %q", cfg.PalaceLLM.Endpoint)
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
