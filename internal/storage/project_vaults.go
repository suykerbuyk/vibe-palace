// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// projectVaultsKey is the global config table that binds a project slug to a
// vault on THIS host (ADR-012, resolution tier 2).
const projectVaultsKey = "project_vaults"

// readProjectVaults returns the host's [project_vaults] table from the global
// config, and that config's path.
//
// An ABSENT config file, or an unresolvable config directory, is "no
// bindings": the host binds nothing, and tier 3 reports its own error if it
// needs the file. Anything else that stops the table being read is a refusal
// wrapping ErrVaultBindingRejected, because an unreadable table might hold the
// binding that should have applied, and the tier below is the live vault:
//   - an Lstat error other than ENOENT (a dangling symlink, EACCES, EIO);
//   - a file that does not parse;
//   - a top-level key that differs from project_vaults only in case — the
//     table is decoded into a map, never a struct field, precisely because
//     BurntSushi matches struct fields case-insensitively (HostGitEnabled has
//     the long form);
//   - project_vaults that is not a table, a key that is not a valid slug, or a
//     value that is not a non-empty string.
func readProjectVaults() (map[string]string, string, error) {
	cfgPath, err := VaultConfigFilePath()
	if err != nil {
		return nil, "", nil
	}
	rejected := func(format string, a ...any) error {
		return fmt.Errorf("%w: [%s] in %s: %s", ErrVaultBindingRejected, projectVaultsKey, cfgPath, fmt.Sprintf(format, a...))
	}
	if _, err := os.Lstat(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, cfgPath, nil
		}
		return nil, cfgPath, rejected("cannot stat the config: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, cfgPath, rejected("cannot read the config: %v", err)
	}
	var top map[string]any
	if _, err := toml.Decode(string(data), &top); err != nil {
		return nil, cfgPath, rejected("the config does not parse: %v", err)
	}
	for key := range top {
		if key != projectVaultsKey && strings.EqualFold(key, projectVaultsKey) {
			return nil, cfgPath, rejected("key %q differs from %s only in case", key, projectVaultsKey)
		}
	}
	raw, ok := top[projectVaultsKey]
	if !ok {
		return nil, cfgPath, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, cfgPath, rejected("%s = %v is a %T, not a table", projectVaultsKey, raw, raw)
	}
	out := make(map[string]string, len(table))
	for key, v := range table {
		if err := slug.Validate(key); err != nil {
			return nil, cfgPath, rejected("key %q is not a project slug: %v", key, err)
		}
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, cfgPath, rejected("%s = %v is not a non-empty path string", key, v)
		}
		out[key] = s
	}
	return out, cfgPath, nil
}

// boundVaultRoot expands a tier-2 target and proves it is an existing vault: a
// directory holding the vault manifest (.vibe-palace/vault.toml).
//
// 🔴 A MISSING TARGET IS REFUSED, NOT RESOLVED. Nothing downstream checks that
// a resolved root exists, and the first reader to open a log under it creates
// <root>/palace/.local/ — after which a stale, near-empty directory reads as a
// plausible vault (the iter 188 class). The errors are formatted with %v, never
// %w, so no fs.ErrNotExist leaks out: callers read that as "no config at all".
func boundVaultRoot(cfgPath, name, target string) (string, error) {
	rejected := func(why string) error {
		return fmt.Errorf("%w: %s binds project %q to %s, which %s. Refusing to resolve a vault that is not there; "+
			"fix or remove the [%s] entry", ErrVaultBindingRejected, cfgPath, name, target, why, projectVaultsKey)
	}
	expanded, err := expandTilde(target)
	if err != nil {
		return "", rejected(fmt.Sprintf("cannot be expanded (%v)", err))
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", rejected(fmt.Sprintf("cannot be made absolute (%v)", err))
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", rejected(fmt.Sprintf("cannot be read (%v)", err))
	}
	if !st.IsDir() {
		return "", rejected("is not a directory")
	}
	mf, err := os.Stat(surface.VaultManifestPath(abs))
	if err != nil || !mf.Mode().IsRegular() {
		return "", rejected("holds no vault manifest (.vibe-palace/vault.toml)")
	}
	return abs, nil
}
