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
	if cfg.EmbedderModel != "all-MiniLM-L6-v2" {
		t.Errorf("EmbedderModel = %q, want %q", cfg.EmbedderModel, "all-MiniLM-L6-v2")
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
	projDir := filepath.Join(v.Root, "Projects", "proj", "agentctx")
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
	if cfg.EmbedderModel != "all-MiniLM-L6-v2" {
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
	if val != "all-MiniLM-L6-v2" {
		t.Errorf("value = %q, want %q", val, "all-MiniLM-L6-v2")
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
}

func TestGetConfigValueProjectOverride(t *testing.T) {
	v := testVault(t)

	projDir := filepath.Join(v.Root, "Projects", "proj", "agentctx")
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

func TestConfigThreeLevelPrecedence(t *testing.T) {
	v := testVault(t)

	// Create project config.
	projDir := filepath.Join(v.Root, "Projects", "proj", "agentctx")
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
	if cfg.EmbedderModel != "all-MiniLM-L6-v2" {
		t.Errorf("EmbedderModel = %q, want default", cfg.EmbedderModel)
	}
}
