// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// CurrentVersionMajor and CurrentVersionMinor are the schema version that
// this binary understands. A config file whose version_major exceeds
// CurrentVersionMajor is rejected. Minor bumps are additive-only and
// forward-compatible. See the [meta] block in defaults.toml.
const (
	CurrentVersionMajor = 1
	CurrentVersionMinor = 1
)

// MetaKind values identify which config-file schema a [meta] block belongs to.
const (
	MetaKindGlobal       = "global"
	MetaKindCwdProject   = "cwd-project"
	MetaKindVaultProject = "vault-project"
)

// missingMetaWarnOnce suppresses the "config has no [meta] block" warning
// to one emission per process, keyed by file path.
var missingMetaWarnOnce sync.Map // map[string]*sync.Once

//go:embed config/defaults.toml
var defaultsToml string

//go:embed config/template.toml
var templateToml string

// TemplateTomlContent returns the embedded template.toml content.
func TemplateTomlContent() string { return templateToml }

// DefaultsTomlContent returns the embedded defaults.toml content. Used
// by config-upgrade to derive canonical schema keys for the global
// config.
func DefaultsTomlContent() (string, error) { return defaultsToml, nil }

// Config holds resolved configuration values.
type Config struct {
	MetaVersionMajor       int                            `json:"meta_version_major"`
	MetaVersionMinor       int                            `json:"meta_version_minor"`
	MetaKind               string                         `json:"meta_kind"`
	VaultPath              string                         `json:"vault_path"`
	GitEnabled             bool                           `json:"git_enabled"`
	HTTPPort               int                            `json:"http_port"`
	LogLevel               string                         `json:"log_level"`
	EmbedderModel          string                         `json:"embedder_model"`
	EmbedderMaxSeqLen      int                            `json:"embedder_max_seq_len"`
	EmbedderBatchSize      int                            `json:"embedder_batch_size"`
	SearchDefaultLimit     int                            `json:"search_default_limit"`
	BoostWing              float64                        `json:"boost_wing"`
	BoostHall              float64                        `json:"boost_hall"`
	BoostRoom              float64                        `json:"boost_room"`
	ChunkMaxChars          int                            `json:"chunk_max_chars"`
	ChunkOverlap           int                            `json:"chunk_overlap"`
	PalaceRoomKeywords     map[string][]string            `json:"palace_room_keywords,omitempty"`
	PalaceScoringOverrides map[string]ScoringRoomOverride `json:"palace_scoring_overrides,omitempty"`
	PalaceMinScore         float64                        `json:"palace_min_score,omitempty"`
	PalaceLLM              LLMConfig                      `json:"palace_llm,omitempty"`
	Archive                ArchiveConfig                  `json:"archive,omitempty"`
	Enrichment             EnrichmentConfig               `json:"enrichment,omitempty"`
}

// EnrichmentConfig holds resolved settings for the session-enrichment LLM
// pass. APIKeyEnv is the name of the environment variable holding the API
// key, not the key itself. All fields are optional; Enabled is off by
// default so an absent [enrichment] block decodes to the zero value.
type EnrichmentConfig struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	APIKeyEnv      string `json:"api_key_env"`
	BaseURL        string `json:"base_url"`
	MaxTokens      int    `json:"max_tokens"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// ArchiveConfig holds transcript-archive settings (ADR-001 Phase 6).
// All fields are optional; signing is off by default.
type ArchiveConfig struct {
	SignMode       string `json:"sign_mode,omitempty"`       // "", "ssh", "gpg"
	SignKey        string `json:"sign_key,omitempty"`        // ssh key path or gpg key id
	SignNamespace  string `json:"sign_namespace,omitempty"`  // ssh-sig namespace
	AllowedSigners string `json:"allowed_signers,omitempty"` // path for ssh verify
	SignerIdentity string `json:"signer_identity,omitempty"` // principal for ssh verify
}

// LLMConfig holds TOML-level LLM endpoint settings.
// APIKeyEnv is the name of the environment variable holding the API key,
// not the key itself. Compare with llm.Config which holds the resolved key.
type LLMConfig struct {
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env"`
	MaxTokens int    `json:"max_tokens"`
}

// ScoringRoomOverride holds weighted keyword overrides for a single room.
type ScoringRoomOverride struct {
	High   []string `json:"high,omitempty"`
	Medium []string `json:"medium,omitempty"`
	Low    []string `json:"low,omitempty"`
}

// tomlMeta captures the [meta] section that every managed config file carries.
type tomlMeta struct {
	VersionMajor int    `toml:"version_major"`
	VersionMinor int    `toml:"version_minor"`
	Kind         string `toml:"kind"`
}

// tomlConfig is the intermediate struct matching the TOML file structure.
type tomlConfig struct {
	Meta       tomlMeta `toml:"meta"`
	VaultPath  string   `toml:"vault_path"`
	GitEnabled bool     `toml:"git_enabled"`
	HTTPPort   int      `toml:"http_port"`
	LogLevel   string   `toml:"log_level"`
	Embedder   struct {
		Model             string `toml:"model"`
		MaxSequenceLength int    `toml:"max_sequence_length"`
		BatchSize         int    `toml:"batch_size"`
	} `toml:"embedder"`
	Search struct {
		DefaultLimit        int     `toml:"default_limit"`
		StructuralBoostWing float64 `toml:"structural_boost_wing"`
		StructuralBoostHall float64 `toml:"structural_boost_hall"`
		StructuralBoostRoom float64 `toml:"structural_boost_room"`
	} `toml:"search"`
	Chunker struct {
		MaxChars int `toml:"max_chars"`
		Overlap  int `toml:"overlap"`
	} `toml:"chunker"`
	Palace struct {
		Rooms   map[string]tomlRoomConfig `toml:"rooms"`
		Scoring tomlScoringConfig         `toml:"scoring"`
		LLM     tomlLLMConfig             `toml:"llm"`
	} `toml:"palace"`
	Archive    tomlArchiveConfig    `toml:"archive"`
	Enrichment tomlEnrichmentConfig `toml:"enrichment"`
}

type tomlEnrichmentConfig struct {
	Enabled        bool   `toml:"enabled"`
	Provider       string `toml:"provider"`
	Model          string `toml:"model"`
	APIKeyEnv      string `toml:"api_key_env"`
	BaseURL        string `toml:"base_url"`
	MaxTokens      int    `toml:"max_tokens"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

type tomlArchiveConfig struct {
	SignMode       string `toml:"sign_mode"`
	SignKey        string `toml:"sign_key"`
	SignNamespace  string `toml:"sign_namespace"`
	AllowedSigners string `toml:"allowed_signers"`
	SignerIdentity string `toml:"signer_identity"`
}

type tomlRoomConfig struct {
	Keywords []string `toml:"keywords"`
}

type tomlScoringConfig struct {
	MinScore float64                    `toml:"min_score"`
	Rooms    map[string]tomlRoomScoring `toml:"rooms"`
}

type tomlRoomScoring struct {
	High   []string `toml:"high"`
	Medium []string `toml:"medium"`
	Low    []string `toml:"low"`
}

type tomlLLMConfig struct {
	Endpoint  string `toml:"endpoint"`
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
	MaxTokens int    `toml:"max_tokens"`
}

// flatten converts a tomlConfig into a Config.
func (tc *tomlConfig) flatten() Config {
	return Config{
		MetaVersionMajor:       tc.Meta.VersionMajor,
		MetaVersionMinor:       tc.Meta.VersionMinor,
		MetaKind:               tc.Meta.Kind,
		VaultPath:              tc.VaultPath,
		GitEnabled:             tc.GitEnabled,
		HTTPPort:               tc.HTTPPort,
		LogLevel:               tc.LogLevel,
		EmbedderModel:          tc.Embedder.Model,
		EmbedderMaxSeqLen:      tc.Embedder.MaxSequenceLength,
		EmbedderBatchSize:      tc.Embedder.BatchSize,
		SearchDefaultLimit:     tc.Search.DefaultLimit,
		BoostWing:              tc.Search.StructuralBoostWing,
		BoostHall:              tc.Search.StructuralBoostHall,
		BoostRoom:              tc.Search.StructuralBoostRoom,
		ChunkMaxChars:          tc.Chunker.MaxChars,
		ChunkOverlap:           tc.Chunker.Overlap,
		PalaceRoomKeywords:     flattenPalaceRooms(tc.Palace.Rooms),
		PalaceScoringOverrides: flattenScoringRooms(tc.Palace.Scoring.Rooms),
		PalaceMinScore:         tc.Palace.Scoring.MinScore,
		PalaceLLM: LLMConfig{
			Endpoint:  tc.Palace.LLM.Endpoint,
			Model:     tc.Palace.LLM.Model,
			APIKeyEnv: tc.Palace.LLM.APIKeyEnv,
			MaxTokens: tc.Palace.LLM.MaxTokens,
		},
		Archive: ArchiveConfig{
			SignMode:       tc.Archive.SignMode,
			SignKey:        tc.Archive.SignKey,
			SignNamespace:  tc.Archive.SignNamespace,
			AllowedSigners: tc.Archive.AllowedSigners,
			SignerIdentity: tc.Archive.SignerIdentity,
		},
		Enrichment: EnrichmentConfig{
			Enabled:        tc.Enrichment.Enabled,
			Provider:       tc.Enrichment.Provider,
			Model:          tc.Enrichment.Model,
			APIKeyEnv:      tc.Enrichment.APIKeyEnv,
			BaseURL:        tc.Enrichment.BaseURL,
			MaxTokens:      tc.Enrichment.MaxTokens,
			TimeoutSeconds: tc.Enrichment.TimeoutSeconds,
		},
	}
}

// flattenPalaceRooms converts the TOML palace rooms config to a plain map.
// Returns nil if no rooms are configured.
func flattenPalaceRooms(rooms map[string]tomlRoomConfig) map[string][]string {
	if len(rooms) == 0 {
		return nil
	}
	m := make(map[string][]string, len(rooms))
	for room, cfg := range rooms {
		m[room] = cfg.Keywords
	}
	return m
}

// flattenScoringRooms converts TOML scoring room overrides to the Config format.
func flattenScoringRooms(rooms map[string]tomlRoomScoring) map[string]ScoringRoomOverride {
	if len(rooms) == 0 {
		return nil
	}
	m := make(map[string]ScoringRoomOverride, len(rooms))
	for room, cfg := range rooms {
		m[room] = ScoringRoomOverride{
			High:   cfg.High,
			Medium: cfg.Medium,
			Low:    cfg.Low,
		}
	}
	return m
}

// LoadConfig loads configuration with precedence: embedded < vault < project.
// Missing config files at any level are silently skipped.
//
// After each on-disk layer decode, the resulting [meta] block is validated:
//   - version_major > CurrentVersionMajor is a hard error (binary is too old).
//   - version_major == 0 (missing [meta]) triggers one slog.Warn per path,
//     recommending `vp config upgrade`.
//
// Decodes use per-layer tomlConfig instances so layer-2's version check sees
// layer-2's values, not the embedded defaults.
func (v *Vault) LoadConfig(project string) (Config, error) {
	var tc tomlConfig

	// Layer 1: embedded defaults (never missing, never version-mismatched
	// by construction — the build pins its own defaults).
	if _, err := toml.Decode(defaultsToml, &tc); err != nil {
		return Config{}, fmt.Errorf("decode embedded defaults: %w", err)
	}

	// Layer 2: vault-level config (~/.config/vibe-palace/config.toml).
	// Meta is file-local, not inherited: zero it before each on-disk decode
	// so Config.Meta* reflects the actual on-disk file (or zero if absent).
	vaultConfigPath, err := VaultConfigFilePath()
	if err == nil {
		if _, err := os.Stat(vaultConfigPath); err == nil {
			tc.Meta = tomlMeta{}
			if _, err := toml.DecodeFile(vaultConfigPath, &tc); err != nil {
				return Config{}, fmt.Errorf("decode vault config %s: %w", vaultConfigPath, err)
			}
			if err := checkConfigVersion(vaultConfigPath, tc.Meta); err != nil {
				return Config{}, err
			}
		}
	}

	// Layer 3: project-level config.
	if project != "" {
		projPath, err := v.ProjectConfigFile(project)
		if err != nil {
			return Config{}, err
		}
		if _, err := os.Stat(projPath); err == nil {
			tc.Meta = tomlMeta{}
			if _, err := toml.DecodeFile(projPath, &tc); err != nil {
				return Config{}, fmt.Errorf("decode project config %s: %w", projPath, err)
			}
			if err := checkConfigVersion(projPath, tc.Meta); err != nil {
				return Config{}, err
			}
		}
	}

	return tc.flatten(), nil
}

// checkConfigVersion rejects configs from a future major version and warns
// once per path when [meta] is missing (version_major == 0).
func checkConfigVersion(path string, m tomlMeta) error {
	if m.VersionMajor > CurrentVersionMajor {
		return fmt.Errorf(
			"config at %s is version %d.%d, this vp supports up to %d.%d — upgrade vp or downgrade the config",
			path, m.VersionMajor, m.VersionMinor, CurrentVersionMajor, CurrentVersionMinor,
		)
	}
	if m.VersionMajor == 0 {
		oncePtr, _ := missingMetaWarnOnce.LoadOrStore(path, &sync.Once{})
		oncePtr.(*sync.Once).Do(func() {
			slog.Warn("config has no [meta] block; run 'vp config upgrade' to add schema version markers", "path", path)
		})
	}
	return nil
}

// GetConfigValue returns the value for a given key and which config level
// provided it ("project", "vault", or "embedded"). Keys use dot notation
// for nested values (e.g., "embedder.model", "search.default_limit").
func (v *Vault) GetConfigValue(project, key string) (string, string, error) {
	// Load each level as raw maps.
	levels := []struct {
		name string
		data string
	}{
		{"embedded", defaultsToml},
	}

	// Vault level.
	vaultConfigPath, err := VaultConfigFilePath()
	if err == nil {
		if data, err := os.ReadFile(vaultConfigPath); err == nil {
			levels = append(levels, struct {
				name string
				data string
			}{"vault", string(data)})
		}
	}

	// Project level.
	if project != "" {
		projPath, pathErr := v.ProjectConfigFile(project)
		if pathErr == nil {
			if data, err := os.ReadFile(projPath); err == nil {
				levels = append(levels, struct {
					name string
					data string
				}{"project", string(data)})
			}
		}
	}

	// Check from highest precedence to lowest.
	for i := len(levels) - 1; i >= 0; i-- {
		var raw map[string]interface{}
		if _, err := toml.Decode(levels[i].data, &raw); err != nil {
			continue
		}
		if val, ok := lookupKey(raw, key); ok {
			return fmt.Sprintf("%v", val), levels[i].name, nil
		}
	}

	return "", "", fmt.Errorf("key %q not found in any config level", key)
}

// lookupKey resolves a dot-notation key in a nested map.
func lookupKey(m map[string]interface{}, key string) (interface{}, bool) {
	parts := splitDot(key)
	current := m
	for i, part := range parts {
		val, ok := current[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return val, true
		}
		sub, ok := val.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current = sub
	}
	return nil, false
}

// splitDot splits a string on '.' characters.
func splitDot(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// VaultConfigFilePath returns the path to the vault-level config file.
func VaultConfigFilePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return configDir + "/vibe-palace/config.toml", nil
}

// WriteScoringConfig merges scoring overrides into the project config file.
// Creates the file and parent directories if they don't exist. Uses atomic
// temp-file + os.Rename. Idempotent: skips keywords already present at the
// same weight tier.
func (v *Vault) WriteScoringConfig(project string, rooms map[string]ScoringRoomOverride, minScore float64) error {
	cfgPath, err := v.ProjectConfigFile(project)
	if err != nil {
		return fmt.Errorf("project config path: %w", err)
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// This is a read→merge→write of the same file (RMW): hold the per-path
	// lock across the whole sequence so concurrent merges never lose updates.
	release, err := vaultlock.Acquire(v.Root, cfgPath)
	if err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	defer release()

	// Load existing config if present.
	var tc tomlConfig
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		if _, err := toml.DecodeFile(cfgPath, &tc); err != nil {
			return fmt.Errorf("decode existing config %s: %w", cfgPath, err)
		}
	}

	// Merge scoring overrides.
	if tc.Palace.Scoring.Rooms == nil {
		tc.Palace.Scoring.Rooms = make(map[string]tomlRoomScoring)
	}
	for room, ov := range rooms {
		existing := tc.Palace.Scoring.Rooms[room]
		existing.High = mergeKeywordTier(existing.High, ov.High)
		existing.Medium = mergeKeywordTier(existing.Medium, ov.Medium)
		existing.Low = mergeKeywordTier(existing.Low, ov.Low)
		tc.Palace.Scoring.Rooms[room] = existing
	}
	if minScore > 0 {
		tc.Palace.Scoring.MinScore = minScore
	}

	// Atomic write via the shared primitive (temp + rename + surface stamp).
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := atomicfile.Write(v.Root, cfgPath, buf.Bytes()); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	slog.Info("wrote scoring config", "path", cfgPath, "rooms", len(rooms))
	return nil
}

// mergeKeywordTier adds new keywords to an existing tier, skipping duplicates.
func mergeKeywordTier(existing, additions []string) []string {
	if len(additions) == 0 {
		return existing
	}
	seen := make(map[string]bool, len(existing))
	for _, kw := range existing {
		seen[kw] = true
	}
	for _, kw := range additions {
		if !seen[kw] {
			existing = append(existing, kw)
			seen[kw] = true
		}
	}
	return existing
}
