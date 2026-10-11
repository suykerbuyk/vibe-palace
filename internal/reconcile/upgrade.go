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

// upgradedConfig computes the upgraded body of userText against target plus the
// number of canonical keys and optional/documented keys it adds. Returns
// ("", 0, 0, nil) when userText is already up to date (both tiers), which the
// caller treats as "write nothing".
//
// Two passes, canonical first: the canonical pass (storage.UpgradeConfig) adds
// missing schema keys as commented defaults; the optional pass
// (storage.PropagateOptionalBlocks) then propagates the commented doc-blocks the
// config lacks. Ordering matters for one case — a legacy config missing an entire
// active section gets that section (keys commented) from the canonical pass first,
// so the optional pass then finds its header and inserts a key like
// embedder.backend in place rather than appending a stray block at EOF.
//
// S3 invariant: the optional pass is invoked HERE, in reconcile, never wired into
// the storage primitives or check.CheckConfigStaleness.
func upgradedConfig(userText string, target upgradeTarget) (string, int, int, error) {
	canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
	if err != nil {
		return "", 0, 0, fmt.Errorf("parse schema: %w", err)
	}
	missing := storage.MissingKeys(canonical, storage.PresentKeys(userText))
	canonicalCount := 0
	for _, keys := range missing {
		canonicalCount += len(keys)
	}

	body := userText
	if canonicalCount > 0 {
		body = storage.UpgradeConfig(body, missing, storage.ParseTemplateBlocks(target.templateText))
	}

	body, optionalKeys := storage.PropagateOptionalBlocks(body, target.templateText, target.canonicalText)
	optionalCount := len(optionalKeys)

	if canonicalCount == 0 && optionalCount == 0 {
		return "", 0, 0, nil
	}
	return body, canonicalCount, optionalCount, nil
}

// upgradeSummary renders the two-category drift summary for a Plan Update,
// keeping the required (canonical) and optional/documented counts textually
// distinct — never one undifferentiated "N new settings" line.
func upgradeSummary(required, optional int) string {
	switch {
	case required > 0 && optional > 0:
		return fmt.Sprintf("%d required key(s) as commented defaults; %d optional setting(s) documented", required, optional)
	case optional > 0:
		return fmt.Sprintf("%d optional setting(s) — will document as inert comments", optional)
	default:
		return fmt.Sprintf("%d required key(s) — will add as commented defaults", required)
	}
}

// upgradeDetails tags each proposed key with its tier so the dry-run never reads
// as a single undifferentiated list.
func upgradeDetails(required, optional []string) []string {
	out := make([]string, 0, len(required)+len(optional))
	for _, k := range required {
		out = append(out, "required  "+k)
	}
	for _, k := range optional {
		out = append(out, "optional  "+k)
	}
	return out
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
	upgraded, canonicalCount, optionalCount, err := upgradedConfig(string(data), target)
	if err != nil {
		return 0, err
	}
	total := canonicalCount + optionalCount
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
