// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
)

// U1 of the mcp-vault-routing-per-project epic: a cwd-independent
// project-slug -> bound-vault resolver seam, resolved ONCE at startup into an
// in-memory snapshot. The MCP guard (U2), write-funnel backstop (U3) and
// eventual router (U4) consult the snapshot and NEVER live-re-read config.toml.
//
// This file delivers only the snapshot builder, ClassifyProjectVault, and the
// types. It wires into nothing: no bootstrap/contextFunc/handler change (U2),
// no write funnel (U3), no registry (U4), and it does not refactor
// staleProjectBinding.

// ProjectVaultVerdict is how a project slug relates to the server's startup
// vault, decided against the startup snapshot.
type ProjectVaultVerdict int

const (
	// VaultVerdictInvalid is the zero value and NEVER a decision. It is
	// returned only alongside a non-nil error: an empty startupRoot, or an
	// unbuilt snapshot (a build failure the server must fail closed on). It is
	// deliberately distinct from VaultUnbound so that a dropped error, or an
	// uninstalled snapshot, cannot read as "allow".
	VaultVerdictInvalid ProjectVaultVerdict = iota
	// VaultUnbound: the slug has no [project_vaults] entry on this host. The
	// consumer ALLOWS the write — this is the fail-safe's known coverage limit
	// (a tier-1 cwd-marker / origin-only project is invisible here, by design;
	// split projects carry a [project_vaults] entry, ADR-012 Decision 1).
	VaultUnbound
	// VaultBoundHere: the slug resolves to the server's own startup vault. Allow.
	VaultBoundHere
	// VaultBoundElsewhere: the slug resolves to a DIFFERENT vault than the one
	// this server bound at startup. The consumer REFUSES the write.
	VaultBoundElsewhere
	// VaultBindingBroken: the slug has an entry, but the binding will not
	// resolve (missing/not-a-vault target, relative/~ target, or a stale
	// [project_vaults] binding). A KNOWN-but-broken binding is distinct from
	// "absent"; the consumer REFUSES (fail-closed).
	VaultBindingBroken
)

// errUnbuiltSnapshot is returned by ClassifyProjectVault for every slug when it
// is handed a snapshot that did not build (its zero value, or one returned
// alongside a BuildProjectVaultSnapshot error). The caller (U2) must fail closed
// — refuse mutating writes — until a good snapshot builds, never install an
// allow-all empty snapshot.
var errUnbuiltSnapshot = errors.New("project-vault snapshot is unbuilt; refusing to classify (fail closed)")

// errEmptyStartupRoot is returned when ClassifyProjectVault is called with an
// empty startupRoot: a programming error at the call site, reported rather than
// silently treated as a decision.
var errEmptyStartupRoot = errors.New("ClassifyProjectVault: empty startupRoot")

// snapEntry is one resolved [project_vaults] binding in the snapshot.
//
// Invariant maintained by the builder: a non-broken entry ALWAYS has a
// non-empty root, and a broken entry ALWAYS has a non-empty reason and an empty
// root. A zero snapEntry (root=="" && !broken) therefore never occurs in a
// built snapshot, and ClassifyProjectVault treats it as a decision for NEITHER
// Here/Elsewhere NOR Broken.
type snapEntry struct {
	root   string // canonical-ish bound root (filepath.Clean'd by boundVaultRoot); "" iff broken
	broken bool   // true => known-but-unresolvable binding (state 3)
	reason string // why it is broken (= err.Error()); surfaced as classify info
}

// ProjectVaultSnapshot is the startup-time resolution of this host's
// [project_vaults] table. It is consulted in-memory with no config read.
//
// A snapshot is usable only when built==true. The zero value is UNBUILT and
// classifies every slug as VaultVerdictInvalid (fail closed), so an uninstalled
// or build-failed snapshot can never allow a write.
type ProjectVaultSnapshot struct {
	entries map[string]snapEntry
	built   bool
}

// BuildProjectVaultSnapshot resolves this host's [project_vaults] table ONCE,
// intended to be called at server startup (by U2), at the moment the config is
// readable by construction. It reuses the real resolver's machinery
// (readProjectVaults, boundVaultRoot, staleProjectBinding) so the snapshot
// agrees with ResolveVaultBinding.
//
// Build semantics (fail closed on a build failure):
//   - readProjectVaults error (unreadable / malformed table) -> returns an
//     unbuilt snapshot and the error. NEVER an allow-all empty snapshot.
//   - no bindings at all -> a BUILT, empty snapshot (every slug Unbound). A host
//     with no [project_vaults] table, or a tier-1-only host with no global
//     vault_path, legitimately binds nothing.
//   - bindings present but the global vault root does not resolve -> returns an
//     unbuilt snapshot and the error. The up-front resolve exists precisely to
//     DETECT this: staleProjectBinding swallows its own ResolveGlobalVaultPath
//     failure (project_vaults.go returns nil — "not stale" — on an empty or
//     errored global root), so without this check a broken global config would
//     silently resolve every entry as not-stale.
//   - a single entry that will not resolve (boundVaultRoot or staleProjectBinding
//     refuses) -> that slug is recorded broken; other entries resolve normally.
//
// The build runs AFTER the server's own startup vault resolution has already
// succeeded, so a whole-table malformed config is largely unreachable here (the
// server would have failed to start). This path's real job is robustness to a
// mid-startup config change.
//
// ctx is honoured for cancellation/timeout. Today the build does only local
// reads (config, vault manifests, departure records, bounded git), so ctx just
// guards those; it is in the signature for the forthcoming network-backed
// resolver (the vp client/server split, where the vault is a network resource),
// so that backend gains cancellation without a later signature churn. A
// cancelled or expired ctx returns an UNBUILT snapshot and the error — fail
// closed, consistent with every other build failure.
func BuildProjectVaultSnapshot(ctx context.Context) (ProjectVaultSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ProjectVaultSnapshot{}, fmt.Errorf("build project-vault snapshot: %w", err)
	}
	bindings, cfgPath, err := readProjectVaults()
	if err != nil {
		return ProjectVaultSnapshot{}, fmt.Errorf("build project-vault snapshot: %w", err)
	}
	if len(bindings) == 0 {
		// No bindings: a valid, built, allow-all snapshot. The global root is
		// irrelevant (staleProjectBinding never runs), so it is NOT resolved —
		// a tier-1-only host has no global vault_path and must stay allow-all.
		return ProjectVaultSnapshot{entries: map[string]snapEntry{}, built: true}, nil
	}
	// Resolve the global vault root ONCE, up front, to detect a failure that
	// staleProjectBinding would otherwise swallow. The root itself is not
	// threaded into staleProjectBinding (no refactor in U1); this call is the
	// detector.
	if _, _, err := ResolveGlobalVaultPath(); err != nil {
		return ProjectVaultSnapshot{}, fmt.Errorf("build project-vault snapshot: resolve global vault: %w", err)
	}

	entries := make(map[string]snapEntry, len(bindings))
	for slug, target := range bindings {
		// A cancel aborts a long multi-slug build (the path a network-backed
		// resolver will care about); fail closed with the same unbuilt+err.
		if err := ctx.Err(); err != nil {
			return ProjectVaultSnapshot{}, fmt.Errorf("build project-vault snapshot: %w", err)
		}
		root, berr := boundVaultRoot(cfgPath, slug, target)
		if berr != nil {
			entries[slug] = snapEntry{broken: true, reason: berr.Error()}
			continue
		}
		if serr := staleProjectBinding(cfgPath, slug, root); serr != nil {
			entries[slug] = snapEntry{broken: true, reason: serr.Error()}
			continue
		}
		entries[slug] = snapEntry{root: root}
	}
	return ProjectVaultSnapshot{entries: entries, built: true}, nil
}

// ClassifyProjectVault reports how slug relates to startupRoot, consulting snap
// ONLY — no config read, and never staleProjectBinding.
//
// Consumers switch on the VERDICT, then read info:
//   - VaultBoundHere / VaultBoundElsewhere -> info = the non-empty resolved root
//   - VaultBindingBroken                   -> info = the reason (non-empty)
//   - VaultUnbound                         -> info = ""
//   - VaultVerdictInvalid                  -> info = "", err != nil (unbuilt
//     snapshot, or empty startupRoot). NEVER VaultUnbound, so a dropped error or
//     an uninstalled snapshot cannot read as allow.
func ClassifyProjectVault(slug, startupRoot string, snap ProjectVaultSnapshot) (verdict ProjectVaultVerdict, info string, err error) {
	if !snap.built {
		return VaultVerdictInvalid, "", errUnbuiltSnapshot
	}
	if startupRoot == "" {
		return VaultVerdictInvalid, "", errEmptyStartupRoot
	}
	entry, ok := snap.entries[slug]
	if !ok {
		return VaultUnbound, "", nil
	}
	if entry.broken {
		return VaultBindingBroken, entry.reason, nil
	}
	if entry.root == "" {
		// A non-broken entry with no root cannot be produced by the builder; it
		// is a decision for neither Here/Elsewhere nor Broken. Treat as not a
		// decision rather than silently comparing an empty root.
		return VaultVerdictInvalid, "", errUnbuiltSnapshot
	}
	if sameVaultRoot(startupRoot, entry.root) {
		return VaultBoundHere, entry.root, nil
	}
	return VaultBoundElsewhere, entry.root, nil
}
