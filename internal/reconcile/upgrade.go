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
// vaultRoot is the discriminator between the two write paths, and they are
// deliberately NOT the same:
//
//   - vaultRoot != "" — configPath is a vault file. The write goes through
//     storage.LockedUpdate, which holds the vaultlock across read → upgrade →
//     atomicfile.Write, and which also stamps the MCP surface version. No .bak
//     and no hand-rolled .tmp: atomicfile.Write owns the temp and the rename.
//   - vaultRoot == "" — configPath is a host-local config (CWD project, global
//     config) that lives OUTSIDE the vault. It keeps the raw
//     backup + temp + rename it has always had. Host-local files are not vault
//     files: they get no vaultlock, no atomicfile permission/fsync semantics,
//     and they keep their .bak, which is the only pre-image they have.
func applyUpgrade(vaultRoot, configPath string, target upgradeTarget) (int, error) {
	if vaultRoot != "" {
		return applyUpgradeVault(vaultRoot, configPath, target)
	}
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

// applyUpgradeVault upgrades a config that lives inside the vault.
//
// The whole read → upgrade → write runs inside one storage.LockedUpdate, i.e.
// inside one vaultlock.Acquire critical section, because the lost-update window
// opens at the READ: two concurrent reconcilers that each read the pre-upgrade
// bytes will each compute an upgrade from that stale snapshot, and the second
// rename silently discards the first (ADR-003). LockedUpdate reaches
// atomicfile.Write directly under that held lock rather than re-entering a
// primitive that locks again, which would self-deadlock forever.
//
// No .bak is written here, and that is deliberate — see
// storage.CanonicalGitignorePatterns: *.bak is gitignored in the vault, so a
// vault .bak is never committed and never synced. It is host-local litter next to a file
// whose real recoverable pre-image is the committed config.toml itself.
// No .tmp either: atomicfile.Write owns the temp file and the rename, and a
// second fixed-name sidecar is exactly what two concurrent writers collide on.
func applyUpgradeVault(vaultRoot, configPath string, target upgradeTarget) (int, error) {
	total := 0
	err := storage.LockedUpdate(vaultRoot, configPath, func(current []byte) ([]byte, error) {
		upgraded, n, err := upgradedConfig(string(current), target)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			// nil ⇒ LockedUpdate writes nothing and leaves the file alone.
			return nil, nil
		}
		total = n
		return []byte(upgraded), nil
	})
	if err != nil {
		return 0, err
	}
	// No stampVaultWrite here: atomicfile.Write already stamps when vaultRoot is
	// non-empty. Stamping twice was harmless (surface.StampForPath memoizes per
	// stamp dir) — dropping it is tidiness, not a bug fix.
	return total, nil
}

// applyUpgradeHostLocal upgrades a config that lives OUTSIDE the vault (the CWD
// project config, the global config).
//
// This is the pre-existing path, unchanged byte for byte, and that is a ruling
// rather than an oversight: host-local configs are not vault files, so they take
// no vaultlock, must not inherit atomicfile's permission/fsync semantics, and
// must keep their .bak — unlike a vault file there is no committed copy standing
// behind them, so the backup is the only pre-image they have.
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
	// Kept, and kept as a no-op: ResolveStampDir("", ...) yields no stamp dir, so
	// StampForPath returns early. Retaining the call keeps the two branches
	// symmetrical at a glance and keeps the "every write site stamps" reading
	// true for anyone auditing this file.
	stampVaultWrite("", configPath)
	return total, nil
}
