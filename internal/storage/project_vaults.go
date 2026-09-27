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

// ErrHostConfigUnreadable marks a global config that is THERE and cannot be
// read or parsed, found while looking for [project_vaults].
//
// It is deliberately NOT ErrVaultBindingRejected. That sentinel is a claim
// about a binding; this is a broken host config, which every command already
// reports as such — `vp check` must keep running its rows on it (preflight
// works on exactly the machine it diagnoses), and the hook's fallback to the
// global vault cannot capture anything, because opening the global vault reads
// the same unreadable file and fails. Resolution still fails closed on it: an
// unreadable file might hold the binding that should have applied.
var ErrHostConfigUnreadable = errors.New("host config unreadable")

// readProjectVaults returns the host's [project_vaults] table from the global
// config, and that config's path.
//
// "No bindings", with no error, when the config is ABSENT: an unresolvable
// config directory, an Lstat ENOENT, or a dangling symlink (a link to nothing
// is no file). The host binds nothing, and tier 3 then reports the missing
// config exactly as it did before [project_vaults] existed.
//
// ErrHostConfigUnreadable when the config is present and cannot be read or
// parsed (EACCES, EIO, a syntax error).
//
// ErrVaultBindingRejected when the config parses but the table is wrong — a
// refusal about the binding itself, which the hook must treat as "capture
// nothing":
//   - a top-level key that differs from project_vaults only in case — the
//     table is decoded into a map, never a struct field, precisely because
//     BurntSushi matches struct fields case-insensitively (HostGitEnabled has
//     the long form);
//   - project_vaults that is not a table, a key that is not a valid slug, or a
//     value that is not a non-empty string.
func readProjectVaults() (map[string]string, string, error) {
	bindings, cfgPath, _, err := readProjectVaultsBytes()
	return bindings, cfgPath, err
}

// readProjectVaultsBytes is readProjectVaults that also returns the bytes it
// parsed (nil when the config is absent), so a WRITER can splice exactly the
// bytes its checks were made against and compare-and-set on them.
func readProjectVaultsBytes() (map[string]string, string, []byte, error) {
	cfgPath, err := VaultConfigFilePath()
	if err != nil {
		return nil, "", nil, nil
	}
	unreadable := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrHostConfigUnreadable, cfgPath, fmt.Sprintf(format, a...))
	}
	if _, err := os.Lstat(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, cfgPath, nil, nil
		}
		return nil, cfgPath, nil, unreadable("cannot stat the config: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Lstat found the entry, the read found nothing: a dangling symlink.
			return nil, cfgPath, nil, nil
		}
		return nil, cfgPath, nil, unreadable("cannot read the config: %v", err)
	}
	bindings, err := parseProjectVaults(cfgPath, data)
	if err != nil {
		return nil, cfgPath, nil, err
	}
	return bindings, cfgPath, data, nil
}

// parseProjectVaults reads the [project_vaults] table out of a global
// config's bytes, with readProjectVaults' refusals (ErrHostConfigUnreadable
// for bytes that do not parse, ErrVaultBindingRejected for a malformed table).
func parseProjectVaults(cfgPath string, data []byte) (map[string]string, error) {
	rejected := func(format string, a ...any) error {
		return fmt.Errorf("%w: [%s] in %s: %s", ErrVaultBindingRejected, projectVaultsKey, cfgPath, fmt.Sprintf(format, a...))
	}
	var top map[string]any
	if _, err := toml.Decode(string(data), &top); err != nil {
		return nil, fmt.Errorf("%w: %s: the config does not parse: %v", ErrHostConfigUnreadable, cfgPath, err)
	}
	for key := range top {
		if key != projectVaultsKey && strings.EqualFold(key, projectVaultsKey) {
			return nil, rejected("key %q differs from %s only in case", key, projectVaultsKey)
		}
	}
	raw, ok := top[projectVaultsKey]
	if !ok {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, rejected("%s = %v is a %T, not a table", projectVaultsKey, raw, raw)
	}
	out := make(map[string]string, len(table))
	for key, v := range table {
		if err := slug.Validate(key); err != nil {
			return nil, rejected("key %q is not a project slug: %v", key, err)
		}
		s, ok := v.(string)
		if !ok {
			return nil, rejected("%s = %v is a %T, not a path string", key, v, v)
		}
		if strings.TrimSpace(s) == "" {
			return nil, rejected("%s is an empty string, not a vault path", key)
		}
		out[key] = s
	}
	return out, nil
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
	// A relative target would resolve against whatever directory the process
	// happens to run in, so one binding would name a different vault from
	// every checkout. Only an absolute path (after ~ expansion) is a binding.
	if !filepath.IsAbs(expanded) {
		return "", rejected("is not an absolute path (write it absolute, or starting with ~/)")
	}
	abs := filepath.Clean(expanded)
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
