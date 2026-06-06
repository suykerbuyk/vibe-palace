// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"fmt"
	"os"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// upgradeTarget holds the canonical-schema and template text used to drive
// drift detection and fixing on a single config file.
type upgradeTarget struct {
	canonicalText string
	templateText  string
}

// detectMissingKeys reads configPath, derives missing keys against target's
// canonical schema, and returns the flattened list (section-qualified) of
// keys that Plan will propose to add. Returns (nil, nil) when up to date.
func detectMissingKeys(configPath string, target upgradeTarget) ([]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	present := storage.PresentKeys(string(data))
	missing := storage.MissingKeys(canonical, present)
	if len(missing) == 0 {
		return nil, nil
	}
	var flat []string
	for _, keys := range missing {
		flat = append(flat, keys...)
	}
	// MissingKeys returns a map keyed by section, so the per-call order is
	// non-deterministic. Sort the flattened list so Plan output is stable
	// across runs (otherwise users see different orderings on each
	// invocation, and tests can't fingerprint the drift surface).
	sort.Strings(flat)
	return flat, nil
}

// applyUpgrade reads configPath, computes missing keys, writes an upgraded
// file (with a .bak backup on success), and returns the number of keys
// added. Returns (0, nil) when the file is already up to date.
//
// vaultRoot is used only to stamp the MCP surface version when configPath is
// a vault write; pass "" for host-local configs (CWD project, global config)
// so stamping is skipped.
func applyUpgrade(vaultRoot, configPath string, target upgradeTarget) (int, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", configPath, err)
	}
	userText := string(data)
	canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
	if err != nil {
		return 0, fmt.Errorf("parse schema: %w", err)
	}
	present := storage.PresentKeys(userText)
	missing := storage.MissingKeys(canonical, present)
	total := 0
	for _, keys := range missing {
		total += len(keys)
	}
	if total == 0 {
		return 0, nil
	}
	templateBlocks := storage.ParseTemplateBlocks(target.templateText)
	upgraded := storage.UpgradeConfig(userText, missing, templateBlocks)

	backupPath := configPath + ".bak"
	if err := os.WriteFile(backupPath, data, 0o644); err != nil {
		return 0, fmt.Errorf("create backup: %w", err)
	}
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(upgraded), 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("rename: %w", err)
	}
	stampVaultWrite(vaultRoot, configPath)
	return total, nil
}
