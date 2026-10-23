// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
)

// U5b of the mcp-vault-routing-per-project epic: the single routing authority.
// Both the MCP dispatch resolver (cmd/vp SetStackResolver) and the mutating gate
// (internal/mcp gateIfMutating) consult RouteForRequest, so the classify path is
// not forked. It reuses the U2 inputs exactly: the project/to_project M1
// convention and ClassifyProjectVault; from/to/slugs on the vault-lifecycle
// tools are NOT M1 names and stay invisible here (those tools carry no
// project/to_project, so they route to the startup vault and are protected by
// the per-write-site atomicfile.ForMisboundExempt, not by this classifier).

// CrossVaultRouteKind says why a request does not resolve to one servable vault.
type CrossVaultRouteKind int

const (
	// RouteBrokenOrUnbuilt: a slug is bound to a vault whose binding will not
	// resolve, or the routing snapshot is unbuilt. Fail-closed for a WRITE.
	RouteBrokenOrUnbuilt CrossVaultRouteKind = iota
	// RouteTwoRoots: the request names two slugs bound to DIFFERENT vaults (a
	// cross-vault move). One stack cannot serve it; routing to either side is a
	// wrong-vault write, so it is refused. A real two-vault move handler (read
	// source from one stack, write dest into the other, two commits) is deferred
	// work, not lost — this refusal preserves U2's loud failure.
	RouteTwoRoots
)

// CrossVaultRouteError is the typed, fail-closed result of RouteForRequest. It
// names the governing slug(s) and, for RouteTwoRoots, BOTH roots, so the gate
// message and the routing tests can distinguish the two-root case from a broken
// binding.
type CrossVaultRouteError struct {
	Kind CrossVaultRouteKind

	// RouteBrokenOrUnbuilt fields:
	Slug    string
	Verdict ProjectVaultVerdict
	Info    string // the broken reason, or empty for an unbuilt snapshot

	// RouteTwoRoots fields:
	SlugA, RootA string
	SlugB, RootB string
}

func (e *CrossVaultRouteError) Error() string {
	switch e.Kind {
	case RouteTwoRoots:
		return fmt.Sprintf("refusing a cross-vault move: project %q is bound to vault %s but %q is bound to vault %s on this host; "+
			"a move cannot span two vaults (one stack cannot serve both ends). Move within one vault, or fix the "+
			"[project_vaults] bindings so both sides share a vault", e.SlugA, e.RootA, e.SlugB, e.RootB)
	default: // RouteBrokenOrUnbuilt
		if e.Verdict == VaultBindingBroken {
			return fmt.Sprintf("project %q has a [project_vaults] binding that will not resolve (%s); "+
				"refusing this write (fail-closed). Fix the binding, then reload the MCP server", e.Slug, e.Info)
		}
		return fmt.Sprintf("this server could not build its [project_vaults] routing snapshot at startup; "+
			"refusing mutating writes to project %q (fail-closed) until the config is fixed and the host "+
			"reloads the MCP server", e.Slug)
	}
}

// ProjectSlugsFromParams extracts the project and to_project slugs — the M1
// convention — from a tool's params. project_path is a filesystem path, not a
// slug, and is out of scope; from/to/slugs are not M1 names and are ignored.
// Unparseable params (schema-validated upstream) yield no slugs.
func ProjectSlugsFromParams(params json.RawMessage) []string {
	var p struct {
		Project   string `json:"project"`
		ToProject string `json:"to_project"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	var out []string
	if p.Project != "" {
		out = append(out, p.Project)
	}
	if p.ToProject != "" {
		out = append(out, p.ToProject)
	}
	return out
}

// RouteForRequest names the ONE vault root that should serve this request, given
// its slugs classified against the startup snapshot.
//
//   - (startupRoot, nil): NO slug (whole-vault tool / vp_config_bind), or every
//     slug classifies VaultBoundHere / VaultUnbound.
//   - (R, nil): every defined slug resolves to one bound vault R.
//   - ("", *CrossVaultRouteError): the request does not resolve to one servable
//     vault — a broken/unbuilt binding (RouteBrokenOrUnbuilt) or two distinct
//     roots (RouteTwoRoots). A mutating caller is refused; a read falls back to
//     the startup vault (the caller treats the error as "serve startup").
//
// 🔴 ORDER IS LOAD-BEARING: a request with NO slug returns (startupRoot, nil)
// BEFORE the snapshot is consulted, so whole-vault tools (vp_vault_sync/commit/
// tidy) and vp_config_bind keep working even under an UNBUILT snapshot (the
// FLAG2 lock). Only AFTER a slug is present is the snapshot classified.
func RouteForRequest(params json.RawMessage, startupRoot string, snap ProjectVaultSnapshot) (string, error) {
	slugs := ProjectSlugsFromParams(params)
	if len(slugs) == 0 {
		return startupRoot, nil // FLAG2: no slug cannot be misrouted; never consult the snapshot
	}

	type resolved struct{ slug, root string }
	var defined []resolved
	for _, slug := range slugs {
		verdict, info, _ := ClassifyProjectVault(slug, startupRoot, snap)
		switch verdict {
		case VaultBoundHere, VaultUnbound:
			defined = append(defined, resolved{slug, startupRoot})
		case VaultBoundElsewhere:
			defined = append(defined, resolved{slug, info}) // info = the resolved bound root
		default: // VaultBindingBroken, VaultVerdictInvalid (unbuilt snapshot)
			return "", &CrossVaultRouteError{Kind: RouteBrokenOrUnbuilt, Slug: slug, Verdict: verdict, Info: info}
		}
	}

	first := defined[0]
	for _, d := range defined[1:] {
		if !sameVaultRoot(d.root, first.root) {
			return "", &CrossVaultRouteError{Kind: RouteTwoRoots, SlugA: first.slug, RootA: first.root, SlugB: d.slug, RootB: d.root}
		}
	}
	return first.root, nil
}
