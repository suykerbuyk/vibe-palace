// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// projectFileName is the per-source-directory project config file.
// It mirrors the constant exported by the project package; kept private
// here to avoid an import cycle (project already imports storage).
const projectFileName = ".vibe-palace.toml"

// ResolveVaultPath determines the vault root for the given cwd.
//
// Walks upward from cwd looking for a .vibe-palace.toml carrying a
// non-empty top-level vault_path key. If found, that path wins
// (expanded and absolute) and source is "cwd:<file>".
//
// The walk stops at $HOME — a file at $HOME/.vibe-palace.toml is NOT
// considered when cwd lies within $HOME. If $HOME is not resolvable,
// the walk proceeds to the filesystem root.
//
// If no usable cwd override is found, falls back to the global config
// and source is "global:<configpath>". If a cwd file is found but its
// TOML is malformed, returns an error — no silent fallback.
//
// Callers must pass cwd explicitly; no env var, no implicit os.Getwd.
func ResolveVaultPath(cwd string) (path string, source string, err error) {
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", fmt.Errorf("resolve cwd: %w", err)
	}

	var boundary string
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		if abs, aerr := filepath.Abs(home); aerr == nil {
			boundary = abs
		}
	}

	if cwdFile := findProjectFileUpward(cwdAbs, boundary); cwdFile != "" {
		vp, perr := readCwdVaultPath(cwdFile)
		if perr != nil {
			return "", "", fmt.Errorf("parse %s: %w", cwdFile, perr)
		}
		if vp != "" {
			expanded, eerr := expandTilde(vp)
			if eerr != nil {
				return "", "", fmt.Errorf("expand vault_path: %w", eerr)
			}
			abs, aerr := filepath.Abs(expanded)
			if aerr != nil {
				return "", "", fmt.Errorf("absolute vault_path: %w", aerr)
			}
			return abs, "cwd:" + cwdFile, nil
		}
	}

	cfgPath, cerr := VaultConfigFilePath()
	if cerr != nil {
		return "", "", fmt.Errorf("resolve config dir: %w", cerr)
	}
	root, verr := VaultRoot(cfgPath)
	if verr != nil {
		return "", "", verr
	}
	return root, "global:" + cfgPath, nil
}

// findProjectFileUpward walks from dir toward the filesystem root
// looking for projectFileName. If homeBoundary is non-empty, the walk
// stops before inspecting homeBoundary itself — so a file at
// $HOME/.vibe-palace.toml is never matched when the walk originates
// below $HOME.
func findProjectFileUpward(dir, homeBoundary string) string {
	for {
		if homeBoundary != "" && dir == homeBoundary {
			return ""
		}
		candidate := filepath.Join(dir, projectFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// readCwdVaultPath decodes a .vibe-palace.toml and returns its
// top-level vault_path value (empty if unset). Unknown top-level keys
// and sections are tolerated.
func readCwdVaultPath(path string) (string, error) {
	var cfg struct {
		VaultPath string `toml:"vault_path"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return "", err
	}
	return cfg.VaultPath, nil
}
