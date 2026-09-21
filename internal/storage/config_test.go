// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// TestMain runs this package hermetically: XDG_CONFIG_HOME points at the
// checked-in host-config fixture, never the developer's real host config.
func TestMain(m *testing.M) { os.Exit(testutil.RunHermetic(m)) }

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

// Per-project overrides of non-scoring settings were removed with the vault's
// Projects/<slug>/config.toml: neither that file nor the host-local
// per-project file can change log_level, http_port or [search] any more.
func TestLoadConfigProjectOverrideIsGone(t *testing.T) {
	const projConfig = `
log_level = "debug"
http_port = 9999

[search]
default_limit = 50
`
	v, hostPath := hostLocalEnv(t, "proj", "")
	want, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (no per-project file): %v", err)
	}

	check := func(t *testing.T, where string) {
		t.Helper()
		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.LogLevel != want.LogLevel || cfg.HTTPPort != want.HTTPPort || cfg.SearchDefaultLimit != want.SearchDefaultLimit {
			t.Errorf("%s changed LoadConfig: log_level=%q http_port=%d default_limit=%d, want %q %d %d",
				where, cfg.LogLevel, cfg.HTTPPort, cfg.SearchDefaultLimit,
				want.LogLevel, want.HTTPPort, want.SearchDefaultLimit)
		}
	}

	writeFileAt(t, filepath.Join(v.Root, "Projects", "proj", "config.toml"), projConfig)
	check(t, "the vault's Projects/proj/config.toml")

	writeFileAt(t, hostPath, projConfig)
	check(t, "the host-local per-project file")
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
	// The section is parsed from the host global config: the vault's
	// per-project config.toml is no longer read.
	v, _ := hostLocalEnv(t, "proj", `
[palace.rooms.audio]
keywords = ["wav", "mp3", "codec"]

[palace.rooms.graphics]
keywords = ["opengl", "vulkan", "shader"]
`)

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

// [palace.rooms] can no longer be overridden per project: the rooms the host
// global config sets are what LoadConfig resolves, whatever the vault's
// Projects/<slug>/config.toml or the host-local per-project file say.
func TestConfigPalaceRoomsProjectOverrideIsGone(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", `
[palace.rooms.audio]
keywords = ["wav"]
`)
	const projRooms = `
[palace.rooms.custom]
keywords = ["only-this"]
`
	writeFileAt(t, filepath.Join(v.Root, "Projects", "proj", "config.toml"), projRooms)
	writeFileAt(t, hostPath, projRooms)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, ok := cfg.PalaceRoomKeywords["custom"]; ok {
		t.Errorf("a per-project [palace.rooms.custom] still applies: %v", cfg.PalaceRoomKeywords)
	}
	if got := cfg.PalaceRoomKeywords["audio"]; len(got) != 1 || got[0] != "wav" {
		t.Errorf("audio keywords = %v, want [wav] from the host global config", got)
	}
}

func TestConfigPalaceScoring(t *testing.T) {
	// The section is parsed from the host global config: the vault's
	// per-project config.toml is no longer read.
	v, _ := hostLocalEnv(t, "proj", `
[palace.scoring]
min_score = 0.4

[palace.scoring.rooms.testing]
high = ["integration test", "e2e test"]
medium = ["spec"]

[palace.scoring.rooms.ml]
high = ["neural network"]
medium = ["training"]
low = ["epoch"]
`)

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
	// The section is parsed from the host global config: the vault's
	// per-project config.toml is no longer read.
	v, _ := hostLocalEnv(t, "proj", `
[palace.llm]
endpoint = "https://api.x.ai/v1"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
max_tokens = 4096
`)

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
	isolateHostConfig(t)

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
	// The section is parsed from the host global config: the vault's
	// per-project config.toml is no longer read.
	v, _ := hostLocalEnv(t, "proj", `
[enrichment]
enabled = true
provider = "xai"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
base_url = "https://api.x.ai/v1"
max_tokens = 4096
timeout_seconds = 30
`)

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
	isolateHostConfig(t)

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

func TestConfigSummarization(t *testing.T) {
	// The section is parsed from the host global config: the vault's
	// per-project config.toml is no longer read.
	v, _ := hostLocalEnv(t, "proj", `
[summarization]
enabled = true
provider = "xai"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
base_url = "https://api.x.ai/v1"
max_tokens = 4096
timeout_seconds = 30
`)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if !cfg.Summarization.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if cfg.Summarization.Provider != "xai" {
		t.Errorf("Provider = %q, want %q", cfg.Summarization.Provider, "xai")
	}
	if cfg.Summarization.Model != "grok-3-mini" {
		t.Errorf("Model = %q, want %q", cfg.Summarization.Model, "grok-3-mini")
	}
	if cfg.Summarization.APIKeyEnv != "XAI_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want %q", cfg.Summarization.APIKeyEnv, "XAI_API_KEY")
	}
	if cfg.Summarization.BaseURL != "https://api.x.ai/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.Summarization.BaseURL, "https://api.x.ai/v1")
	}
	if cfg.Summarization.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", cfg.Summarization.MaxTokens)
	}
	if cfg.Summarization.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", cfg.Summarization.TimeoutSeconds)
	}
}

func TestConfigSummarizationEmpty(t *testing.T) {
	// Isolate from any host global ~/.config/vibe-palace/config.toml (which
	// on developer machines might have [summarization] enabled). Point XDG to
	// empty temp so LoadConfig sees only embedded defaults (summarization off).
	isolateHostConfig(t)

	v := testVault(t)

	// No [summarization] block — Config.Summarization must be the zero value.
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Summarization != (SummarizationConfig{}) {
		t.Errorf("Summarization should be zero value when no block configured, got %+v", cfg.Summarization)
	}
	if cfg.Summarization.Enabled {
		t.Errorf("Summarization.Enabled should be false by default, got true")
	}
}

func TestCurrentVersionMinor(t *testing.T) {
	if CurrentVersionMinor != 1 {
		t.Errorf("CurrentVersionMinor = %d, want 1", CurrentVersionMinor)
	}
}

// --- WriteHostScoringConfig ---------------------------------------------------
//
// These drive the production scoring writer, which writes the host-local
// per-project file (<XDG>/vibe-palace/projects/<slug>.toml). Every test
// isolates XDG through hostLocalEnv, so none of them writes into the package's
// read-only fixture or the real host config.

// writeHostScoring calls the production writer for "proj" and fails the test on
// error.
func writeHostScoring(t *testing.T, v *Vault, rooms map[string]ScoringRoomOverride, minScore float64) {
	t.Helper()
	if _, _, err := v.WriteHostScoringConfig("proj", rooms, minScore); err != nil {
		t.Fatalf("WriteHostScoringConfig: %v", err)
	}
}

// readText returns a file's contents as a string.
func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestWriteHostScoringConfig_NewFile(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")

	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"integration test"}, Medium: []string{"spec"}},
		"ml":      {High: []string{"neural network"}, Low: []string{"epoch"}},
	}
	writeHostScoring(t, v, rooms, 0.5)

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

func TestWriteHostScoringConfig_MergeExisting(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")

	// Initial host-local file, carrying one non-scoring line the writer must
	// leave in place.
	writeFileAt(t, hostPath, `
log_level = "debug"

[palace.scoring]
min_score = 0.4

[palace.scoring.rooms.testing]
high = ["e2e test"]
`)

	// Merge new scoring.
	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"integration test"}},
		"ml":      {High: []string{"neural network"}},
	}
	writeHostScoring(t, v, rooms, 0)

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
	if text := readText(t, hostPath); !strings.Contains(text, `log_level = "debug"`) {
		t.Errorf("the merge dropped a non-scoring line:\n%s", text)
	}
}

func TestWriteHostScoringConfig_Idempotent(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")

	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"integration test"}},
	}
	writeHostScoring(t, v, rooms, 0.5)
	// Write same data again.
	writeHostScoring(t, v, rooms, 0.5)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got := cfg.PalaceScoringOverrides["testing"]
	if len(got.High) != 1 {
		t.Errorf("testing.High = %v, want exactly 1 entry (no duplicates)", got.High)
	}
}

func TestWriteHostScoringConfig_NoMinScoreOverride(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")

	// Write with minScore = 0 should not set min_score in config.
	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"test"}},
	}
	writeHostScoring(t, v, rooms, 0)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0 {
		t.Errorf("PalaceMinScore = %f, want 0 (should not be set)", cfg.PalaceMinScore)
	}
	if text := readText(t, hostPath); strings.Contains(text, "min_score") {
		t.Errorf("a minScore of 0 wrote a min_score key:\n%s", text)
	}
}

func TestWriteHostScoringConfig_EmptyRooms(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")
	// Writing with empty rooms map should still create the file.
	writeHostScoring(t, v, map[string]ScoringRoomOverride{}, 0.5)
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0.5 {
		t.Errorf("PalaceMinScore = %f, want 0.5", cfg.PalaceMinScore)
	}
}

// Non-scoring keys in the host-local file are not read, but the writer owns
// only the scoring subtree, so their TEXT must survive a write.
func TestWriteHostScoringConfig_PreservesOtherConfig(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")

	writeFileAt(t, hostPath, `
log_level = "trace"
http_port = 8888

[embedder]
batch_size = 64
`)

	rooms := map[string]ScoringRoomOverride{
		"ml": {High: []string{"transformer"}},
	}
	writeHostScoring(t, v, rooms, 0)

	text := readText(t, hostPath)
	for _, want := range []string{`log_level = "trace"`, "http_port = 8888", "[embedder]", "batch_size = 64"} {
		if !strings.Contains(text, want) {
			t.Errorf("the write dropped %q:\n%s", want, text)
		}
	}
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PalaceScoringOverrides["ml"].High; len(got) != 1 || got[0] != "transformer" {
		t.Errorf("ml.High = %v, want [transformer]", got)
	}
}

func TestWriteHostScoringConfig_InvalidProject(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")
	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"test"}},
	}
	if _, _, err := v.WriteHostScoringConfig("INVALID SLUG!", rooms, 0); err == nil {
		t.Error("expected error for invalid project slug")
	}
}

func TestWriteHostScoringConfig_CorruptExisting(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, hostPath, "not valid toml [[[")

	rooms := map[string]ScoringRoomOverride{
		"testing": {High: []string{"test"}},
	}
	if _, _, err := v.WriteHostScoringConfig("proj", rooms, 0); err == nil {
		t.Error("expected error for corrupt TOML")
	}
}

// Scoring resolves defaults < host global config < host-local per-project
// file, and non-scoring settings still come from the layers below.
func TestConfigThreeLevelPrecedence(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")

	defaults, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (defaults): %v", err)
	}

	globalPath, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, globalPath, `
[embedder]
batch_size = 64

[palace.scoring]
min_score = 0.3

[palace.scoring.rooms.general]
high = ["global-high"]

[palace.scoring.rooms.other]
high = ["global-other"]
`)
	global, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (global): %v", err)
	}
	if global.PalaceMinScore != 0.3 || global.PalaceMinScore == defaults.PalaceMinScore {
		t.Errorf("min_score = %v over defaults %v, want 0.3 from the host global config",
			global.PalaceMinScore, defaults.PalaceMinScore)
	}
	if got := global.PalaceScoringOverrides["general"].High; len(got) != 1 || got[0] != "global-high" {
		t.Errorf("general.high = %v, want [global-high]", got)
	}

	writeFileAt(t, hostPath, `
[palace.scoring]
min_score = 0.7

[palace.scoring.rooms.general]
high = ["host-high"]
`)
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (host-local): %v", err)
	}
	if cfg.PalaceMinScore != 0.7 {
		t.Errorf("min_score = %v, want 0.7 from the host-local file", cfg.PalaceMinScore)
	}
	if got := cfg.PalaceScoringOverrides["general"].High; len(got) != 1 || got[0] != "host-high" {
		t.Errorf("general.high = %v, want [host-high]", got)
	}
	if got := cfg.PalaceScoringOverrides["other"].High; len(got) != 1 || got[0] != "global-other" {
		t.Errorf("other.high = %v, want [global-other]: a room the host-local file does not name comes from below", got)
	}
	// Non-scoring layering is unchanged: global over embedded defaults.
	if cfg.EmbedderBatchSize != 64 {
		t.Errorf("EmbedderBatchSize = %d, want 64 (host global config)", cfg.EmbedderBatchSize)
	}
	if cfg.EmbedderModel != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Errorf("EmbedderModel = %q, want default", cfg.EmbedderModel)
	}
}

// --- absent-field regression coverage ---------------------------------------
//
// The scoring writer once decoded/re-encoded the FULL tomlConfig struct, which
// turned every previously-absent (inherited) field into an explicit, present
// zero value. These seed a distinctive host GLOBAL config (the "tier" below the
// per-project file) so any accidental zeroing is unambiguous (a zeroed field
// could otherwise coincide with a real default and hide the bug), and check
// both what LoadConfig resolves and what the per-project FILE contains.

// writeGlobalTierConfig isolates XDG_CONFIG_HOME to a fresh temp dir and writes
// content as the host global config there. It returns the vault and the
// host-local per-project path for "proj". Must be called before any
// LoadConfig/WriteHostScoringConfig call in the test.
func writeGlobalTierConfig(t *testing.T, content string) (*Vault, string) {
	t.Helper()
	return hostLocalEnv(t, "proj", content)
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
	if cfg.HTTPPort != 9001 {
		t.Errorf("HTTPPort = %d, want 9001 (global tier)", cfg.HTTPPort)
	}
	if cfg.VaultPath != "/vault-tier-path" {
		t.Errorf("VaultPath = %q, want /vault-tier-path (global tier)", cfg.VaultPath)
	}
	if cfg.PalaceLLM.Endpoint != "https://vault-tier-llm.example" {
		t.Errorf("PalaceLLM.Endpoint = %q, want global-tier value", cfg.PalaceLLM.Endpoint)
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
	content := readText(t, cfgPath)
	for _, forbidden := range []string{"git_enabled", "http_port", "vault_path", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("project config file unexpectedly contains %q after WriteHostScoringConfig:\n%s", forbidden, content)
		}
	}
}

// TestWriteHostScoringConfig_PreservesAbsentFields is the core regression
// test: a per-project file whose non-scoring fields are ALL absent must not
// gain any of them from a scoring-only write, and everything still resolves
// from the global tier — including the [palace.llm] block whose zeroing broke
// `vp discover/tune rooms` in production.
func TestWriteHostScoringConfig_PreservesAbsentFields(t *testing.T) {
	v, cfgPath := writeGlobalTierConfig(t, vaultTierFixture)
	// Only [meta], everything else absent.
	writeFileAt(t, cfgPath, "[meta]\nversion_major = 1\nversion_minor = 0\n")

	writeHostScoring(t, v, map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}, 0)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	assertVaultTierInherited(t, cfg)
	assertProjectFileClean(t, cfgPath)
}

// TestWriteHostScoringConfig_PreservesAbsentFields_Idempotent pairs the above
// with a second write against the now-once-merged file: a re-run must not
// reintroduce the corruption on the second pass either.
func TestWriteHostScoringConfig_PreservesAbsentFields_Idempotent(t *testing.T) {
	v, cfgPath := writeGlobalTierConfig(t, vaultTierFixture)
	writeFileAt(t, cfgPath, "[meta]\nversion_major = 1\nversion_minor = 0\n")

	writeHostScoring(t, v, map[string]ScoringRoomOverride{
		"ml": {High: []string{"transformer"}},
	}, 0)
	writeHostScoring(t, v, map[string]ScoringRoomOverride{
		"ml": {High: []string{"another-keyword"}},
	}, 0)

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

// TestWriteHostScoringConfig_PreservesMinScoreInheritance covers the
// min_score instance of the same absent-vs-zero bug: an absent
// [palace.scoring] min_score in the per-project file must inherit min_score
// from the global tier, not clobber it with the Go float64 zero value.
func TestWriteHostScoringConfig_PreservesMinScoreInheritance(t *testing.T) {
	v, cfgPath := writeGlobalTierConfig(t, "[palace.scoring]\nmin_score = 0.6\n")
	writeFileAt(t, cfgPath, "[meta]\nversion_major = 1\n")

	// 0 is the sentinel every real caller passes when it isn't overriding min_score.
	writeHostScoring(t, v, map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}, 0)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0.6 {
		t.Errorf("PalaceMinScore = %f, want 0.6 (inherited from global tier)", cfg.PalaceMinScore)
	}
}

// TestWriteHostScoringConfig_ExplicitMinScoreZeroSurvives covers the flip
// side: a per-project file that explicitly sets min_score = 0 (a deliberate
// override, not an omission) must keep that explicit zero rather than have it
// read back as "absent" and silently re-inherit the global tier's value.
func TestWriteHostScoringConfig_ExplicitMinScoreZeroSurvives(t *testing.T) {
	v, cfgPath := writeGlobalTierConfig(t, "[palace.scoring]\nmin_score = 0.6\n")
	writeFileAt(t, cfgPath, "[palace.scoring]\nmin_score = 0.0\n")

	writeHostScoring(t, v, map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}, 0)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PalaceMinScore != 0 {
		t.Errorf("PalaceMinScore = %f, want 0 (explicit project override preserved)", cfg.PalaceMinScore)
	}
}

// TestWriteHostScoringConfig_ExplicitZeroSurvives is the non-scoring
// counterpart: an explicit http_port = 0 in the per-project file is not read
// any more (the host-local file carries scoring only), but it is operator text
// the writer does not own, so it must survive the write verbatim.
func TestWriteHostScoringConfig_ExplicitZeroSurvives(t *testing.T) {
	v, cfgPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, cfgPath, "http_port = 0\n")

	writeHostScoring(t, v, map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}, 0)

	if text := readText(t, cfgPath); !strings.Contains(text, "http_port = 0") {
		t.Errorf("the explicit http_port = 0 did not survive the write:\n%s", text)
	}
}

// TestWriteHostScoringConfig_MalformedPalaceTable: a scalar where a table is
// expected must produce a clear error, not a panic, and leave the file alone.
func TestWriteHostScoringConfig_MalformedPalaceTable(t *testing.T) {
	v, cfgPath := hostLocalEnv(t, "proj", "")
	original := "palace = \"not-a-table\"\n"
	writeFileAt(t, cfgPath, original)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	_, _, err := v.WriteHostScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Fatal("expected error for malformed [palace] table, got nil")
	}
	if !strings.Contains(err.Error(), "palace") {
		t.Errorf("error should mention 'palace': %v", err)
	}
	if text := readText(t, cfgPath); text != original {
		t.Errorf("a failed write changed the file:\n%s", text)
	}
	assertCoreRefuses(t, cfgPath, original, "palace", rooms)
}

// TestWriteHostScoringConfig_MergesExistingRoomKeywords: an existing
// [palace.scoring.rooms] table with high/medium/low tiers already populated
// must survive a merge that adds new keywords to an EXISTING room and
// introduces a brand-NEW room, with no duplication in either.
func TestWriteHostScoringConfig_MergesExistingRoomKeywords(t *testing.T) {
	v, cfgPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, cfgPath, `
[palace.scoring.rooms.devops]
high = ["kubernetes"]
medium = ["docker"]
low = ["deploy"]
`)

	rooms := map[string]ScoringRoomOverride{
		"devops": {High: []string{"kubernetes", "terraform"}, Medium: []string{"docker"}, Low: []string{"pipeline"}},
		"ml":     {High: []string{"transformer"}},
	}
	writeHostScoring(t, v, rooms, 0)

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

// TestWriteHostScoringConfig_MixedExplicitAndAbsent: a per-project file with a
// MIX of explicit non-scoring lines and absent fields. The explicit lines are
// not read (the host-local file carries scoring only) but must survive in the
// file's text; the absent ones must stay absent and resolve from the global
// tier.
func TestWriteHostScoringConfig_MixedExplicitAndAbsent(t *testing.T) {
	v, cfgPath := writeGlobalTierConfig(t, vaultTierFixture)
	writeFileAt(t, cfgPath, `
log_level = "trace"
http_port = 8888

[embedder]
batch_size = 64
`)

	writeHostScoring(t, v, map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}, 0)

	content := readText(t, cfgPath)
	// Explicit lines survive in the file.
	for _, want := range []string{`log_level = "trace"`, "http_port = 8888", "batch_size = 64"} {
		if !strings.Contains(content, want) {
			t.Errorf("the write dropped %q:\n%s", want, content)
		}
	}
	// The project file itself must not have gained these keys.
	for _, forbidden := range []string{"vault_path =", "git_enabled =", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("project config unexpectedly gained %q:\n%s", forbidden, content)
		}
	}

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Every non-scoring value resolves from the global tier.
	assertVaultTierInherited(t, cfg)
}

// TestWriteHostScoringConfig_FreshCreateOnlyHasScoringKeys: with no existing
// file, the write produces the host-local header — a comment block and a
// [meta] with kind = "host-project" — plus ONLY the [palace.scoring...] keys
// this call actually set: no git_enabled/http_port/vault_path/[palace.llm].
func TestWriteHostScoringConfig_FreshCreateOnlyHasScoringKeys(t *testing.T) {
	v, cfgPath := hostLocalEnv(t, "proj", "")

	writeHostScoring(t, v, map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}, 0.5)

	content := readText(t, cfgPath)
	if !strings.Contains(content, "[meta]") || !strings.Contains(content, `kind = "host-project"`) {
		t.Errorf("fresh-create should seed [meta] with kind = \"host-project\", got:\n%s", content)
	}
	if !strings.HasPrefix(content, renderConfigMetaHeader(MetaKindHostProject)) {
		t.Errorf("fresh-create does not start with the host-local header:\n%s", content)
	}
	for _, forbidden := range []string{"git_enabled", "http_port", "vault_path", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("fresh-create unexpectedly contains %q:\n%s", forbidden, content)
		}
	}
	if !strings.Contains(content, "[palace.scoring") {
		t.Errorf("fresh-create should contain [palace.scoring...], got:\n%s", content)
	}

	// Reload still succeeds and inherits everything else from the tiers below.
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

// TestWriteHostScoringConfig_NoOpDoesNotTouchFile: a call with an empty rooms
// map AND minScore at the "no override" sentinel (<= 0) has nothing to write,
// and must not create a file (or gain empty [palace]/[palace.scoring]/
// [palace.scoring.rooms] headers in an existing one) that wasn't already there.
func TestWriteHostScoringConfig_NoOpDoesNotTouchFile(t *testing.T) {
	t.Run("no config file stays absent", func(t *testing.T) {
		v, cfgPath := hostLocalEnv(t, "proj", "")
		writeHostScoring(t, v, map[string]ScoringRoomOverride{}, 0)
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected no config file to be created, stat err = %v", statErr)
		}
	})

	t.Run("negative minScore is also a no-op", func(t *testing.T) {
		v, cfgPath := hostLocalEnv(t, "proj", "")
		writeHostScoring(t, v, map[string]ScoringRoomOverride{}, -1)
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected no config file to be created, stat err = %v", statErr)
		}
	})

	t.Run("existing config file is byte-identical after a no-op call", func(t *testing.T) {
		v, cfgPath := hostLocalEnv(t, "proj", "")
		before := []byte("log_level = \"trace\"\nhttp_port = 8888\n")
		writeFileAt(t, cfgPath, string(before))

		writeHostScoring(t, v, map[string]ScoringRoomOverride{}, 0)

		after, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read project config: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("no-op call mutated the file:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

// assertCoreRefuses calls the splice core, writeScoringConfigAt, directly on
// cfgPath — bypassing WriteHostScoringConfig's hostRoomsAlreadyWritten decode,
// which would otherwise reject the file first — so the core's own
// malformed-table guards stay pinned. It requires an error mentioning want and
// the file unchanged.
func assertCoreRefuses(t *testing.T, cfgPath, original, want string, rooms map[string]ScoringRoomOverride) {
	t.Helper()
	err := writeScoringConfigAt(cfgPath, filepath.Dir(filepath.Dir(cfgPath)), "", MetaKindHostProject, rooms, 0)
	if err == nil {
		t.Fatalf("writeScoringConfigAt accepted a malformed file:\n%s", original)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("writeScoringConfigAt error should mention %q: %v", want, err)
	}
	if got := readText(t, cfgPath); got != original {
		t.Errorf("a failed core write changed the file:\n%s", got)
	}
}

// assertMalformedTierRefused seeds original into the host-local file, runs a
// write, and requires an error naming the "high" tier with the file unchanged.
func assertMalformedTierRefused(t *testing.T, original, what string) {
	t.Helper()
	v, cfgPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, cfgPath, original)

	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}
	_, _, err := v.WriteHostScoringConfig("proj", rooms, 0)
	if err == nil {
		t.Fatalf("expected error for %s, got nil", what)
	}
	if !strings.Contains(err.Error(), "high") {
		t.Errorf("error should mention 'high': %v", err)
	}
	if got := readText(t, cfgPath); got != original {
		t.Errorf("original %s should survive a failed write, got:\n%s", what, got)
	}
	assertCoreRefuses(t, cfgPath, original, "high", rooms)
}

// TestWriteHostScoringConfig_MalformedTierScalar: a tier value that decodes to
// a scalar instead of an array (`high = "oops-not-an-array"`) must produce a
// clear error instead of silently overwriting whatever was there.
func TestWriteHostScoringConfig_MalformedTierScalar(t *testing.T) {
	assertMalformedTierRefused(t, "[palace.scoring.rooms.ml]\nhigh = \"oops-not-an-array\"\n", "non-array tier value")
}

// TestWriteHostScoringConfig_MalformedTierSubTable: `[palace.scoring.rooms.ml.high]`
// written as a table instead of an array must error, not silently destroy the
// sub-table's contents.
func TestWriteHostScoringConfig_MalformedTierSubTable(t *testing.T) {
	assertMalformedTierRefused(t, "[palace.scoring.rooms.ml.high]\nweight = 5\n", "sub-table tier value")
}

// TestWriteHostScoringConfig_MixedTypeTierArray (`high = ["neural network", 42,
// true]`) pins the chosen behavior — a clear error — so a non-string element
// can never be silently dropped from the tier.
func TestWriteHostScoringConfig_MixedTypeTierArray(t *testing.T) {
	assertMalformedTierRefused(t, "[palace.scoring.rooms.ml]\nhigh = [\"neural network\", 42, true]\n", "mixed-type tier array")
}

// TestWriteHostScoringConfig_PreservesNonScoringFloatField: a generic float
// field OUTSIDE the scoring section — [search] structural_boost_wing — through
// the absent / explicit-zero / explicit-real-value permutations, since a float
// zero value is exactly the shape the original bug silently produced. The
// host-local file does not apply it, so the explicit cases pin the FILE text
// and that the global tier's value still resolves.
func TestWriteHostScoringConfig_PreservesNonScoringFloatField(t *testing.T) {
	rooms := map[string]ScoringRoomOverride{"ml": {High: []string{"transformer"}}}

	t.Run("absent inherits from global tier", func(t *testing.T) {
		v, cfgPath := writeGlobalTierConfig(t, "[search]\nstructural_boost_wing = 0.42\n")
		writeFileAt(t, cfgPath, "[meta]\nversion_major = 1\n")

		writeHostScoring(t, v, rooms, 0)

		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.BoostWing != 0.42 {
			t.Errorf("BoostWing = %f, want 0.42 (inherited from global tier)", cfg.BoostWing)
		}
		if text := readText(t, cfgPath); strings.Contains(text, "structural_boost_wing") {
			t.Errorf("the write added structural_boost_wing to the per-project file:\n%s", text)
		}
	})

	for _, tc := range []struct{ name, line string }{
		{"explicit zero survives in the file", "structural_boost_wing = 0.0"},
		{"explicit real value survives in the file", "structural_boost_wing = 0.77"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, cfgPath := writeGlobalTierConfig(t, "[search]\nstructural_boost_wing = 0.42\n")
			writeFileAt(t, cfgPath, "[search]\n"+tc.line+"\n")

			writeHostScoring(t, v, rooms, 0)

			if text := readText(t, cfgPath); !strings.Contains(text, tc.line) {
				t.Errorf("the write dropped %q:\n%s", tc.line, text)
			}
			cfg, err := v.LoadConfig("proj")
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.BoostWing != 0.42 {
				t.Errorf("BoostWing = %f, want 0.42: the host-local file does not apply [search]", cfg.BoostWing)
			}
		})
	}
}
