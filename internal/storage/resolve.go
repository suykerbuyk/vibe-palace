// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/project"
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
// The marker is found and read by project.LocateMarker — the ONE walk
// (symlink-resolved, bounded at the resolved $HOME) and reader that project
// detection also uses, so the vault and the project label cannot come from
// different files or disagree on the (trimmed) name.
//
// Every refusal below wraps ErrVaultBindingRejected and never falls through
// to a lower tier, because the lowest tier is the LIVE vault:
//   - a found marker that cannot be read or parsed, or whose vault_path is not
//     a string or is swallowed by a table;
//   - a malformed [project_vaults] table (a case-variant key, a non-slug key,
//     a non-string or empty value);
//   - tiers 1 and 2 naming different vaults (both sources are named);
//   - a tier-2 target that is not an absolute path to an existing vault;
//   - a marker naming no project while the git-origin slug is bound, or while
//     git cannot be asked (missing, failed, timed out) on a host that binds.
//
// A global config that is present but unreadable fails with
// ErrHostConfigUnreadable instead: it is a broken host config, not a binding
// refusal. An absent (or dangling-symlink) config means "no bindings".
//
// Callers must pass cwd explicitly; no env var, no implicit os.Getwd.
func ResolveVaultBinding(cwd string) (Resolution, error) {
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve cwd: %w", err)
	}

	marker, err := readCwdMarker(cwdAbs)
	if err != nil {
		return Resolution{}, err
	}
	cwdFile := marker.Path

	bindings, cfgPath, err := readProjectVaults()
	if err != nil {
		return Resolution{}, err
	}

	name := marker.Name
	if marker.NameErr != nil {
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
		gs, gerr := gitRemoteSlug(cwdAbs)
		switch {
		case gerr != nil && name == "":
			// The answer decides between a refusal and the global (live)
			// vault, and git could not give it: refuse rather than guess.
			return Resolution{}, fmt.Errorf("%w: this checkout names no project (%s), this host binds projects in %s, "+
				"and its git origin could not be read to rule them out (%v). Refusing to fall back to the global vault; "+
				"set [project].name in the checkout's .vibe-palace.toml",
				ErrVaultBindingRejected, markerWhere(cwdFile), cfgPath, gerr)
		case gerr != nil:
			// The marker names the project, so the git slug could only have
			// produced the advisory below. Say it was not checked.
			warnings = append(warnings, fmt.Sprintf(
				"%s names project %q, which has no [project_vaults] entry; its git origin could not be read to check "+
					"whether a bound project is meant (%v)", cwdFile, name, gerr))
		case gs != "" && gs != name:
			if target, ok := bindings[gs]; ok {
				if name == "" {
					return Resolution{}, fmt.Errorf("%w: this checkout's git origin names project %q, which %s binds to %s "+
						"on this host, but there is %s. Refusing to fall back to the global vault; "+
						"set [project].name in the checkout's .vibe-palace.toml",
						ErrVaultBindingRejected, gs, cfgPath, target, markerWhere(cwdFile))
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

// markerWhere phrases "which marker names no project" for a refusal.
func markerWhere(cwdFile string) string {
	if cwdFile == "" {
		return "no .vibe-palace.toml"
	}
	return cwdFile + " names no project"
}

// readCwdMarker is the resolver's marker read: project.LocateMarker — the ONE
// walk and reader detection also uses (ADR-012) — with its failures turned
// into refusals.
//
// 🔴 A FOUND MARKER THAT CANNOT BE READ IS A REFUSAL, NOT "NO MARKER". Once a
// committed marker plus a host binding decides the vault, a marker that fails
// to read or parse (a syntax error in one commit, `vault_path = 5`, EACCES)
// hides the binding it would have keyed. Every such failure wraps
// ErrVaultBindingRejected — the one error `vp hook` treats as "capture
// nothing" instead of falling back to the global (live) vault — and never
// fs.ErrNotExist, which callers read as "no config at all".
func readCwdMarker(cwdAbs string) (project.Marker, error) {
	m, err := project.LocateMarker(cwdAbs)
	if err != nil {
		if m.Path == "" {
			return project.Marker{}, fmt.Errorf("%w: find .vibe-palace.toml from %s: %v", ErrVaultBindingRejected, cwdAbs, err)
		}
		return project.Marker{}, fmt.Errorf("%w: parse %s: %v. Refusing to resolve a vault from a marker that "+
			"cannot be read: it may name a project this host binds elsewhere, and the fallback is the global (live) vault",
			ErrVaultBindingRejected, m.Path, err)
	}
	if m.SwallowedTable != "" {
		return project.Marker{}, fmt.Errorf("parse %s: %w", m.Path, errSwallowedVaultPath(m.SwallowedTable))
	}
	return m, nil
}

// CwdMarker returns the .vibe-palace.toml the resolver would read from cwd
// ("" when there is none) and its top-level vault_path, through the SAME walk
// and reader, so a report about "this checkout's marker" cannot name a
// different file than the one that binds the vault. Its errors are the
// resolver's: typed refusals.
func CwdMarker(cwd string) (path, vaultPath string, err error) {
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", fmt.Errorf("resolve cwd: %w", err)
	}
	m, err := readCwdMarker(cwdAbs)
	if err != nil {
		return "", "", err
	}
	return m.Path, m.VaultPath, nil
}

// gitRemoteSlug is project.GitRemoteSlugChecked behind a seam, so a test can
// prove the resolver never runs git on a host that binds nothing, and can make
// the lookup fail.
var gitRemoteSlug = project.GitRemoteSlugChecked

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
