// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	return ResolveGlobalVaultPath()
}

// ResolveGlobalVaultPath returns the vault root from the global config only
// (no cwd walk). Use this for machine-wide artifacts such as user-global host
// surfaces so install does not depend on which directory the operator ran from.
func ResolveGlobalVaultPath() (path string, source string, err error) {
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
//
// It REFUSES a vault_path that TOML parsed as a sub-key of a table rather
// than a top-level key — see errSwallowedVaultPath. That is a hard error,
// joining the malformed-TOML path this file already documents: no silent
// fallback.
func readCwdVaultPath(path string) (string, error) {
	var cfg struct {
		VaultPath string `toml:"vault_path"`
	}
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return "", err
	}
	if cfg.VaultPath == "" {
		if owner := swallowedVaultPathTable(md); owner != "" {
			return "", errSwallowedVaultPath(owner)
		}
	}
	return cfg.VaultPath, nil
}

// swallowedVaultPathTable reports the table that captured a vault_path key,
// or "" when none did.
//
// vault_path is a TOP-LEVEL key, but TOML scopes every key that follows a
// [table] header INTO that table. So the natural writing order
//
//	[project]
//	name = "throwaway"
//	vault_path = "/tmp/throwaway-vault"
//
// binds project.vault_path, leaves the top-level key unset, and the decode
// succeeds with err == nil. Without this detection the caller falls through to
// the GLOBAL config — the live vault — and writes there. Iteration 210 caught
// that only after a throwaway run had captured into the real vault.
//
// MetaData.Keys reports every key the document actually defined, so the
// swallowed case is directly observable; the decode alone cannot see it.
func swallowedVaultPathTable(md toml.MetaData) string {
	for _, key := range md.Keys() {
		if len(key) < 2 || key[len(key)-1] != "vault_path" {
			continue
		}
		return strings.Join(key[:len(key)-1], ".")
	}
	return ""
}

// ErrSwallowedVaultPath marks a .vibe-palace.toml that was FOUND and PARSED
// but REJECTED because its vault_path landed under a table.
//
// Callers need this distinguishable from "no vault configured". Both arrive as
// an error from ResolveVaultPath, but they mean opposite things: an absent
// global config is the normal state of an un-set-up machine and diagnostics
// must still run on it, while a rejected config file is a live misconfiguration
// pointing at the wrong vault. Collapsing the two makes `vp check` refuse to
// run on exactly the machine it exists to diagnose.
var ErrSwallowedVaultPath = errors.New("vault_path swallowed by a table")

// errSwallowedVaultPath builds the refusal.
//
// The message carries the load-bearing facts rather than a token, because this
// error IS the rule's delivery channel — it replaced the workflow.md paragraph
// that used to ship in every bootstrap payload and was enforced by nothing.
// A reader who never saw that paragraph must be able to act on this text alone.
func errSwallowedVaultPath(owner string) error {
	return fmt.Errorf("%w: it is defined under table [%s], not at the top level, "+
		"so it does NOT override the vault — TOML scopes every key after a [table] header "+
		"into that table. Refusing to fall back to the global config, which is the LIVE vault: "+
		"that fallback is silent, and it captures throwaway work into real project history "+
		"(iteration 210). Fix: move vault_path ABOVE every table in the file, then confirm "+
		"with `vp check`, which prints the resolved vault_path and its source",
		ErrSwallowedVaultPath, owner)
}
