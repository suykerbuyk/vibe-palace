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
//  1. The nearest gitignored .vibe-palace/local.toml walking upward from cwd, if
//     it carries a non-empty top-level vault_path. Source "cwd-local:<file>".
//     The legacy .vibe-palace.toml top-level vault_path is still honored during a
//     deprecation window when the override file sets none (source "cwd:<file>"),
//     surfaced as a Resolution.Warnings migration nudge (ADR-012 Amendment
//     2026-10-25, Option (b) structural split). Both set and disagreeing refuses.
//  2. The global config's [project_vaults] entry for the identity marker's
//     [project].name. Source "binding:<configpath>#<slug>".
//  3. The global config's vault_path. Source "global:<configpath>".
//
// The walk stops at $HOME — a file at $HOME/.vibe-palace.toml is NOT
// considered when cwd lies within $HOME. If $HOME is not resolvable, the walk
// proceeds to the filesystem root.
//
// A tracked .vibe-palace.toml is identity only (ADR-012): a checkout shared
// across hosts names its project, and each host binds that project to a vault
// in its own global config. The tier-1 override lives in the gitignored
// .vibe-palace/local.toml and is scoped to throwaway/rehearsal trees.
//
// The identity marker is found and read by project.LocateMarker — the ONE walk
// (symlink-resolved, bounded at the resolved $HOME) and reader that project
// detection also uses, so the vault and the project label cannot come from
// different files or disagree on the (trimmed) name. The override file is found
// by project.LocateLocalOverride, which uses the SAME $HOME-bounded walk.
//
// Every refusal below wraps ErrVaultBindingRejected and never falls through
// to a lower tier, because the lowest tier is the LIVE vault:
//   - a found identity marker OR override that cannot be read or parsed, or whose
//     vault_path is not a string or is swallowed by a table;
//   - a git-TRACKED identity marker that carries vault_path (ADR-012 Option (b):
//     the committed marker is identity only), including the fail-closed case where
//     it sets vault_path and git cannot be asked whether it is tracked;
//   - the override and the legacy marker both setting vault_path to different
//     vaults;
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

	// U4 (ADR-012 Option (b)): a git-TRACKED identity marker must NEVER carry
	// vault_path — a committed host path is false on every other host sharing the
	// repo. A HARD refusal, fail-closed: when the marker sets vault_path and git
	// cannot be asked, refuse rather than risk honoring it. An UNTRACKED marker's
	// vault_path stays the deprecation-window tier-1 lever (resolveTier1). The git
	// call is scoped to a non-empty VaultPath (cost bound); a commented or empty
	// vault_path decodes to "" and never reaches git.
	if marker.VaultPath != "" {
		tracked, terr := markerIsTracked(marker.Path)
		if terr != nil {
			return Resolution{}, fmt.Errorf("%w: %s sets vault_path, and git could not be asked whether it is "+
				"tracked (%v). Refusing rather than risk honoring a committed host path; the host override belongs in %s",
				ErrVaultBindingRejected, marker.Path, terr, project.LocalOverrideName)
		}
		if tracked {
			return Resolution{}, fmt.Errorf("%w: %s is git-tracked and sets vault_path %q. A committed marker is "+
				"identity only (ADR-012 Option (b) structural split): the path is true on this host and false on every "+
				"other host sharing the repo. Remove vault_path from the tracked marker, then bind the project in this "+
				"host's [project_vaults], or put the override in the gitignored %s",
				ErrVaultBindingRejected, marker.Path, marker.VaultPath, project.LocalOverrideName)
		}
	}

	// Tier 1's vault_path now lives in the gitignored .vibe-palace/local.toml
	// (ADR-012 Amendment 2026-10-25, Option (b)). When an identity marker was
	// found, the override is CO-LOCATED with it (read only in the marker's own
	// directory) so the vault and the project label cannot come from different
	// trees (rule 4a); a throwaway tree with no marker keeps the $HOME-bounded
	// walk. A FOUND-but-unreadable or swallowed override is a refusal, same
	// discipline as the identity marker.
	local, err := readLocalOverride(cwdAbs, marker.Path)
	if err != nil {
		return Resolution{}, err
	}

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
			if err := staleProjectBinding(cfgPath, name, bound); err != nil {
				return Resolution{}, err
			}
		}
	}

	tier1Path, tier1File, tier1Source, tier1Warnings, terr := resolveTier1(local, marker)
	if terr != nil {
		return Resolution{}, terr
	}
	if tier1Path != "" {
		if bound != "" && !sameVaultRoot(tier1Path, bound) {
			return Resolution{}, fmt.Errorf("%w: %s sets vault_path %s, but %s binds project %q to %s on this host. "+
				"Refusing to guess which vault this checkout's history belongs in; remove one of the two (ADR-012: "+
				"a tracked .vibe-palace.toml never carries vault_path; the host override belongs in %s)",
				ErrVaultBindingRejected, tier1File, tier1Path, cfgPath, name, bound, project.LocalOverrideName)
		}
		return Resolution{Path: tier1Path, Source: tier1Source, Warnings: tier1Warnings}, nil
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

// readLocalOverride reads the gitignored tier-1 override .vibe-palace/local.toml,
// with the same refusal discipline as readCwdMarker: a FOUND file that cannot be
// read or parsed, or whose vault_path is swallowed by a table, is a refusal
// wrapping ErrVaultBindingRejected — never "no override" — because it could hide
// the host's real tier-1 lever, and the fallback is a lower (ultimately live)
// tier. No file at all is a zero Marker.
//
// When markerPath is non-empty (an identity marker was found), the override is
// read ONLY from the marker's own directory, so the vault and the project label
// come from the same tree (ADR-012 rule 4a). When markerPath is "" (a throwaway
// tree with no marker), the override keeps its own $HOME-bounded walk.
func readLocalOverride(cwdAbs, markerPath string) (project.Marker, error) {
	var m project.Marker
	var err error
	if markerPath != "" {
		m, err = project.ReadLocalOverrideInDir(filepath.Dir(markerPath))
	} else {
		m, err = project.LocateLocalOverride(cwdAbs)
	}
	if err != nil {
		where := m.Path
		if where == "" {
			where = project.LocalOverrideName + " (from " + cwdAbs + ")"
		}
		return project.Marker{}, fmt.Errorf("%w: read %s: %v. Refusing to resolve a vault from an override that "+
			"cannot be read: it may name the vault this host means, and the fallback is a lower (ultimately live) tier",
			ErrVaultBindingRejected, where, err)
	}
	if m.SwallowedTable != "" {
		return project.Marker{}, fmt.Errorf("parse %s: %w", m.Path, errSwallowedVaultPath(m.SwallowedTable))
	}
	return m, nil
}

// expandAbsVaultPath turns a raw vault_path (which may be ~-relative) into an
// absolute path.
//
// 🔴 BOTH failures wrap ErrVaultBindingRejected. A `~`-path whose UserHomeDir
// fails (expandTilde), or an Abs that fails, is a REFUSAL — never a bare error.
// resolveTier1 returns this straight up ResolveVaultBinding, and `vp hook` falls
// through every UNRECOGNISED error to OpenVaultGlobal (the live vault), so an
// unwrapped error here is a silent capture into real project history. It never
// wraps fs.ErrNotExist (which `vp check` / `vp skills show` read as "no config").
func expandAbsVaultPath(vaultPath string) (string, error) {
	expanded, err := expandTilde(vaultPath)
	if err != nil {
		return "", fmt.Errorf("%w: expand vault_path %q: %v", ErrVaultBindingRejected, vaultPath, err)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("%w: absolute vault_path %q: %v", ErrVaultBindingRejected, expanded, err)
	}
	return abs, nil
}

// resolveTier1 picks the effective tier-1 vault_path from the new gitignored
// override (local, .vibe-palace/local.toml) and the legacy identity marker
// (marker, .vibe-palace.toml), applying ADR-012 Option (b) precedence:
//
//   - the new override WINS when it sets vault_path;
//   - the legacy marker's vault_path is consulted ONLY when the override has
//     none, and is honored during a deprecation window, surfaced as a
//     Resolution.Warnings entry (the migration nudge `vp status` prints) — never
//     a new error type, which would fall through `vp hook` to the live vault;
//   - both set and naming DIFFERENT roots is a refusal (ErrVaultBindingRejected),
//     the same rule as tier-1-vs-tier-2 below, one step earlier.
//
// It returns the absolute path ("" when neither sets one), the source FILE that
// supplied it (for a disagreement message), the Resolution.Source string, and
// any warnings.
//
// 🔴 U2 DOES NOT ADD THE TRACKED-MARKER HARD REFUSAL. A tracked .vibe-palace.toml
// with vault_path is still honored here (advisory, as today) — turning it into a
// refusal is U4, which adds a fail-closed git-tracked check. U2 only changes
// WHERE tier-1 reads and the legacy precedence.
func resolveTier1(local, marker project.Marker) (path, srcFile, source string, warnings []string, err error) {
	newVP := local.VaultPath
	legacyVP := marker.VaultPath

	var absNew, absLegacy string
	if newVP != "" {
		if absNew, err = expandAbsVaultPath(newVP); err != nil {
			return "", "", "", nil, err
		}
	}
	if legacyVP != "" {
		if absLegacy, err = expandAbsVaultPath(legacyVP); err != nil {
			return "", "", "", nil, err
		}
	}

	switch {
	case newVP != "" && legacyVP != "":
		if !sameVaultRoot(absNew, absLegacy) {
			return "", "", "", nil, fmt.Errorf("%w: %s sets vault_path %s and %s sets vault_path %s; they name "+
				"different vaults. The gitignored %s is the host override (ADR-012 Option (b)); remove vault_path "+
				"from the legacy %s",
				ErrVaultBindingRejected, local.Path, absNew, marker.Path, absLegacy,
				project.LocalOverrideName, projectFileName)
		}
		// Same root: the override wins; the duplicate legacy key is redundant.
		return absNew, local.Path, "cwd-local:" + local.Path, []string{fmt.Sprintf(
			"%s and %s both set the same vault_path; the %s key is now redundant and can be removed "+
				"(ADR-012 Option (b): the host override lives in %s)",
			local.Path, marker.Path, projectFileName, project.LocalOverrideName)}, nil
	case newVP != "":
		return absNew, local.Path, "cwd-local:" + local.Path, nil, nil
	case legacyVP != "":
		return absLegacy, marker.Path, "cwd:" + marker.Path, []string{fmt.Sprintf(
			"%s sets vault_path (tier-1); this location is DEPRECATED (ADR-012 Option (b) structural split). "+
				"Move it to the gitignored %s beside the checkout so a committed identity marker never carries a "+
				"host path", marker.Path, project.LocalOverrideName)}, nil
	default:
		return "", "", "", nil, nil
	}
}

// CwdMarker returns the IDENTITY marker (.vibe-palace.toml) the resolver would
// read from cwd ("" when there is none) and ITS top-level vault_path, through the
// SAME walk and reader the resolver uses for identity. Its errors are the
// resolver's: typed refusals.
//
// 🔴 THIS IS THE IDENTITY MARKER ONLY — NOT "the file that binds the vault". Since
// ADR-012 Amendment 2026-10-25 (Option (b)), the tier-1 vault_path lever lives in
// .vibe-palace/local.toml (project.LocalOverrideName), which this function does
// NOT read. A caller asking "does the committed marker carry vault_path?" (vp
// check's tracked-marker row) is right to use this; a caller wanting the effective
// tier-1 path must consult the override too. (U5 TODO: vp init's CwdMarker-based
// --vault-path disagreement pre-check does not yet see the override.)
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

// markerIsTracked is project.IsTrackedMarker behind a seam, so a test can force
// the tracked verdict (and the fail-closed git-can't-answer case) without a git
// fixture. Tests MUST restore it (the SetMisboundRefuse restore pattern).
var markerIsTracked = project.IsTrackedMarker

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
