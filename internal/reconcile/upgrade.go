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
// file, and returns the number of keys added. Returns (0, nil) when the file
// is already up to date.
//
// Every config it reaches is host-local (the CWD project config, the global
// config) and lives OUTSIDE the vault. The vault-side branch went with the
// per-project vault config it existed for (task
// move-per-project-config-out-of-the-shared-vault).
func applyUpgrade(configPath string, target upgradeTarget) (int, error) {
	return applyUpgradeHostLocal(configPath, target)
}

// upgradedConfig computes the upgraded body of userText against target and the
// number of keys it adds. Returns ("", 0, nil) when userText is already up to
// date, which both branches treat as "write nothing".
func upgradedConfig(userText string, target upgradeTarget) (string, int, error) {
	canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
	if err != nil {
		return "", 0, fmt.Errorf("parse schema: %w", err)
	}
	present := storage.PresentKeys(userText)
	missing := storage.MissingKeys(canonical, present)
	total := 0
	for _, keys := range missing {
		total += len(keys)
	}
	if total == 0 {
		return "", 0, nil
	}
	templateBlocks := storage.ParseTemplateBlocks(target.templateText)
	return storage.UpgradeConfig(userText, missing, templateBlocks), total, nil
}

// applyUpgradeHostLocal upgrades a config that lives OUTSIDE the vault (the CWD
// project config, the global config).
//
// Host-local configs are not vault files, so they take no vaultlock, must not
// inherit atomicfile's permission/fsync semantics, and must keep their .bak —
// unlike a vault file there is no committed copy standing behind them, so the
// backup is the only pre-image they have.
func applyUpgradeHostLocal(configPath string, target upgradeTarget) (int, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", configPath, err)
	}
	upgraded, total, err := upgradedConfig(string(data), target)
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}

	if _, err := storage.WriteHostLocalWithBackup(configPath, data, []byte(upgraded)); err != nil {
		return 0, err
	}
	// Kept, and kept as a no-op: ResolveStampDir("", ...) yields no stamp dir, so
	// StampForPath returns early. Retaining the call keeps the "every write site
	// stamps" reading true for anyone auditing this file.
	stampVaultWrite("", configPath)
	return total, nil
}
