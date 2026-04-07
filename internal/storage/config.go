// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

//go:embed config/defaults.toml
var defaultsToml string

// Config holds resolved configuration values.
type Config struct {
	VaultPath          string  `json:"vault_path"`
	HTTPPort           int     `json:"http_port"`
	LogLevel           string  `json:"log_level"`
	EmbedderModel      string  `json:"embedder_model"`
	EmbedderMaxSeqLen  int     `json:"embedder_max_seq_len"`
	EmbedderBatchSize  int     `json:"embedder_batch_size"`
	SearchDefaultLimit int     `json:"search_default_limit"`
	BoostWing          float64 `json:"boost_wing"`
	BoostHall          float64 `json:"boost_hall"`
	BoostRoom          float64 `json:"boost_room"`
}

// tomlConfig is the intermediate struct matching the TOML file structure.
type tomlConfig struct {
	VaultPath string `toml:"vault_path"`
	HTTPPort  int    `toml:"http_port"`
	LogLevel  string `toml:"log_level"`
	Embedder  struct {
		Model             string `toml:"model"`
		MaxSequenceLength int    `toml:"max_sequence_length"`
		BatchSize         int    `toml:"batch_size"`
	} `toml:"embedder"`
	Search struct {
		DefaultLimit       int     `toml:"default_limit"`
		StructuralBoostWing float64 `toml:"structural_boost_wing"`
		StructuralBoostHall float64 `toml:"structural_boost_hall"`
		StructuralBoostRoom float64 `toml:"structural_boost_room"`
	} `toml:"search"`
}

// flatten converts a tomlConfig into a Config.
func (tc *tomlConfig) flatten() Config {
	return Config{
		VaultPath:          tc.VaultPath,
		HTTPPort:           tc.HTTPPort,
		LogLevel:           tc.LogLevel,
		EmbedderModel:      tc.Embedder.Model,
		EmbedderMaxSeqLen:  tc.Embedder.MaxSequenceLength,
		EmbedderBatchSize:  tc.Embedder.BatchSize,
		SearchDefaultLimit: tc.Search.DefaultLimit,
		BoostWing:          tc.Search.StructuralBoostWing,
		BoostHall:          tc.Search.StructuralBoostHall,
		BoostRoom:          tc.Search.StructuralBoostRoom,
	}
}

// LoadConfig loads configuration with precedence: embedded < vault < project.
// Missing config files at any level are silently skipped.
func (v *Vault) LoadConfig(project string) (Config, error) {
	var tc tomlConfig

	// Layer 1: embedded defaults.
	if _, err := toml.Decode(defaultsToml, &tc); err != nil {
		return Config{}, fmt.Errorf("decode embedded defaults: %w", err)
	}

	// Layer 2: vault-level config (~/.config/vibe-palace/config.toml).
	vaultConfigPath, err := vaultConfigFilePath()
	if err == nil {
		if _, err := os.Stat(vaultConfigPath); err == nil {
			if _, err := toml.DecodeFile(vaultConfigPath, &tc); err != nil {
				return Config{}, fmt.Errorf("decode vault config %s: %w", vaultConfigPath, err)
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
			if _, err := toml.DecodeFile(projPath, &tc); err != nil {
				return Config{}, fmt.Errorf("decode project config %s: %w", projPath, err)
			}
		}
	}

	return tc.flatten(), nil
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
	vaultConfigPath, err := vaultConfigFilePath()
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

// vaultConfigFilePath returns the path to the vault-level config file.
func vaultConfigFilePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return configDir + "/vibe-palace/config.toml", nil
}
