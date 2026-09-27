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

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// projectFileName is the per-source-directory project config file. It
// mirrors project.ConfigFileName, kept as a private copy here; storage does
// import internal/project (for (*Vault).DetectedProject), and project imports
// only internal/slug, so there is no cycle either way.
const projectFileName = ".vibe-palace.toml"

// Resolution is one vault resolution: the root, the source that bound it, and
// any advisory warnings a caller that prints the binding (vp status) reports.
type Resolution struct {
	Path     string
	Source   string
	Warnings []string
}

// ResolveVaultPath determines the vault root for the given cwd. It is
// ResolveVaultBinding without the warnings; see that function for the rules.
//
// Callers must pass cwd explicitly; no env var, no implicit os.Getwd.
func ResolveVaultPath(cwd string) (path string, source string, err error) {
	r, err := ResolveVaultBinding(cwd)
	if err != nil {
		return "", "", err
	}
	return r.Path, r.Source, nil
}

// ResolveVaultBinding determines the vault root for the given cwd, in three
// tiers (ADR-012):
//
//  1. The nearest .vibe-palace.toml walking upward from cwd, if it carries a
//     non-empty top-level vault_path. Source "cwd:<file>".
//  2. The global config's [project_vaults] entry for that SAME file's
//     [project].name. Source "binding:<configpath>#<slug>".
//  3. The global config's vault_path. Source "global:<configpath>".
//
// The walk stops at $HOME — a file at $HOME/.vibe-palace.toml is NOT
// considered when cwd lies within $HOME. If $HOME is not resolvable, the walk
// proceeds to the filesystem root.
//
// A tracked .vibe-palace.toml is identity only (ADR-012): a checkout shared
// across hosts names its project, and each host binds that project to a vault
// in its own global config. Tier 1 remains for untracked trees.
//
// Every refusal below wraps ErrVaultBindingRejected and never falls through
// to a lower tier, because the lowest tier is the LIVE vault. (A cwd file that
// fails to parse at all is an error too, but an untyped one — unchanged here.)
//   - a cwd file whose vault_path is swallowed by a table;
//   - an unreadable or malformed [project_vaults] table;
//   - tiers 1 and 2 naming different vaults (both sources are named);
//   - a tier-2 target that is not an existing vault;
//   - a marker naming no project while the git-origin slug is bound.
//
// Callers must pass cwd explicitly; no env var, no implicit os.Getwd.
func ResolveVaultBinding(cwd string) (Resolution, error) {
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve cwd: %w", err)
	}

	var marker cwdMarker
	cwdFile := findCwdMarkerFile(cwdAbs)
	if cwdFile != "" {
		m, perr := readCwdMarker(cwdFile)
		if perr != nil {
			return Resolution{}, fmt.Errorf("parse %s: %w", cwdFile, perr)
		}
		marker = m
	}

	bindings, cfgPath, err := readProjectVaults()
	if err != nil {
		return Resolution{}, err
	}

	name := strings.TrimSpace(marker.ProjectName)
	if slug.Validate(name) != nil {
		name = ""
	}

	// Tier 2's target is validated before tier 1 is honoured: a broken binding
	// is broken whichever tier would have won, and R2 below needs its root.
	var bound string
	if name != "" {
		if target, ok := bindings[name]; ok {
			bound, err = boundVaultRoot(cfgPath, name, target)
			if err != nil {
				return Resolution{}, err
			}
		}
	}

	if vp := marker.VaultPath; vp != "" {
		expanded, eerr := expandTilde(vp)
		if eerr != nil {
			return Resolution{}, fmt.Errorf("expand vault_path: %w", eerr)
		}
		abs, aerr := filepath.Abs(expanded)
		if aerr != nil {
			return Resolution{}, fmt.Errorf("absolute vault_path: %w", aerr)
		}
		if bound != "" && !sameVaultRoot(abs, bound) {
			return Resolution{}, fmt.Errorf("%w: %s sets vault_path %s, but %s binds project %q to %s on this host. "+
				"Refusing to guess which vault this checkout's history belongs in; remove one of the two (ADR-012: "+
				"a tracked .vibe-palace.toml never carries vault_path)",
				ErrVaultBindingRejected, cwdFile, abs, cfgPath, name, bound)
		}
		return Resolution{Path: abs, Source: "cwd:" + cwdFile}, nil
	}

	if bound != "" {
		return Resolution{Path: bound, Source: "binding:" + cfgPath + "#" + name}, nil
	}

	// The git-origin slug is consulted ONLY when this host binds something, so
	// a host with no bindings never runs git to resolve a vault.
	var warnings []string
	if len(bindings) > 0 {
		if gs := gitRemoteSlug(cwdAbs); gs != "" && gs != name {
			if target, ok := bindings[gs]; ok {
				if name == "" {
					where := "no .vibe-palace.toml"
					if cwdFile != "" {
						where = cwdFile + " names no project"
					}
					return Resolution{}, fmt.Errorf("%w: this checkout's git origin names project %q, which %s binds to %s "+
						"on this host, but there is %s. Refusing to fall back to the global vault; "+
						"set [project].name in the checkout's .vibe-palace.toml",
						ErrVaultBindingRejected, gs, cfgPath, target, where)
				}
				warnings = append(warnings, fmt.Sprintf(
					"%s names project %q, which has no [project_vaults] entry, while its git origin names %q, which does; "+
						"the marker is the project's identity, so %q's binding does not apply here", cwdFile, name, gs, gs))
			}
		}
	}

	path, source, err := ResolveGlobalVaultPath()
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Path: path, Source: source, Warnings: warnings}, nil
}

// findCwdMarkerFile is the resolver's walk: the nearest .vibe-palace.toml at or
// above cwdAbs, bounded at $HOME, or "".
func findCwdMarkerFile(cwdAbs string) string {
	var boundary string
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		if abs, aerr := filepath.Abs(home); aerr == nil {
			boundary = abs
		}
	}
	return findProjectFileUpward(cwdAbs, boundary)
}

// CwdMarker returns the .vibe-palace.toml the resolver would read from cwd
// ("" when there is none) and its top-level vault_path, found by the SAME walk
// and decode, so a report about "this checkout's marker" cannot name a
// different file than the one that binds the vault.
func CwdMarker(cwd string) (path, vaultPath string, err error) {
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", fmt.Errorf("resolve cwd: %w", err)
	}
	path = findCwdMarkerFile(cwdAbs)
	if path == "" {
		return "", "", nil
	}
	m, err := readCwdMarker(path)
	if err != nil {
		return path, "", fmt.Errorf("parse %s: %w", path, err)
	}
	return path, m.VaultPath, nil
}

// gitRemoteSlug is project.GitRemoteSlug behind a seam, so a test can prove
// the resolver never runs git on a host that binds nothing.
var gitRemoteSlug = project.GitRemoteSlug

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

// cwdMarker is what the resolver reads from a .vibe-palace.toml: the
// top-level vault_path (tier 1) and [project].name (the key for tier 2).
// Unknown top-level keys and sections are tolerated.
type cwdMarker struct {
	VaultPath   string
	ProjectName string
}

// readCwdMarker reads a .vibe-palace.toml ONCE and returns both keys the
// resolver needs.
//
// vault_path is decoded exactly as before this file learned about names: a
// wrong-typed value is an error. The name is read TOLERANTLY from a generic
// decode of the same bytes — a missing, non-table [project] or non-string name
// is "no usable name", never an error — so no marker that resolved before
// ADR-012 fails to resolve now merely for what its [project] table holds.
//
// It REFUSES a vault_path that TOML parsed as a sub-key of a table rather
// than a top-level key — see errSwallowedVaultPath. That is a hard error,
// joining the malformed-TOML path this file already documents: no silent
// fallback.
func readCwdMarker(path string) (cwdMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cwdMarker{}, err
	}
	var cfg struct {
		VaultPath string `toml:"vault_path"`
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return cwdMarker{}, err
	}
	if cfg.VaultPath == "" {
		if owner := swallowedVaultPathTable(md); owner != "" {
			return cwdMarker{}, errSwallowedVaultPath(owner)
		}
	}
	m := cwdMarker{VaultPath: cfg.VaultPath}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err == nil {
		if p, ok := raw["project"].(map[string]any); ok {
			m.ProjectName, _ = p["name"].(string)
		}
	}
	return m, nil
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

// ErrVaultBindingRejected marks every vault resolution that FOUND a binding
// and REFUSED it: a swallowed vault_path (which also matches
// ErrSwallowedVaultPath), a malformed [project_vaults] table, tiers that
// disagree, a bound vault that does not exist, a checkout that names no
// project while its git-origin slug is bound.
//
// 🔴 ONE SENTINEL, BECAUSE CALLERS FALL BACK ON EVERYTHING ELSE. `vp hook`
// captures into the GLOBAL vault on any resolution error it does not
// recognise (cmd/vp/cmd_hook.go), which is right for an absent config and is
// iteration 210 for a rejected one. A refusal that does not wrap this sentinel
// is a silent capture into the live vault. It never wraps fs.ErrNotExist
// either: CheckConfigAt and `vp skills show` read that as "no config at all".
var ErrVaultBindingRejected = errors.New("vault binding rejected")

// errSwallowedVaultPath builds the refusal.
//
// The message carries the load-bearing facts rather than a token, because this
// error IS the rule's delivery channel — it replaced the workflow.md paragraph
// that used to ship in every bootstrap payload and was enforced by nothing.
// A reader who never saw that paragraph must be able to act on this text alone.
func errSwallowedVaultPath(owner string) error {
	return fmt.Errorf("%w: %w: it is defined under table [%s], not at the top level, "+
		"so it does NOT override the vault — TOML scopes every key after a [table] header "+
		"into that table. Refusing to fall back to the global config, which is the LIVE vault: "+
		"that fallback is silent, and it captures throwaway work into real project history "+
		"(iteration 210). Fix: move vault_path ABOVE every table in the file, then confirm "+
		"with `vp check`, which prints the resolved vault_path and its source",
		ErrVaultBindingRejected, ErrSwallowedVaultPath, owner)
}
