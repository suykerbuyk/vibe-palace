// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"path/filepath"
)

// StaleBindingError reports that a long-lived process bound a vault root at
// startup that no longer matches what its launch directory resolves to.
//
// Re-binding in place is deliberately not the remedy: the registered tool
// closures, the context resolver and the search engine all captured the old
// root, so swapping it would leave them writing the old tree anyway. Refuse and
// have the operator reload. The full why reaches the operator through Error().
type StaleBindingError struct {
	// Bound is the vault root this process resolved at startup.
	Bound string
	// Resolved is what the same launch directory resolves to now.
	Resolved string
	// Source names the config that supplies Resolved ("cwd:<file>",
	// "binding:<configfile>#<slug>" or "global:<file>"), so the operator knows
	// which file to look at.
	Source string
	// Reason, when set, is a stale [project_vaults] binding: the bound vault
	// no longer holds the project (StaleProjectBindingError), even though the
	// root has not changed since startup.
	Reason string
}

func (e *StaleBindingError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("vault binding is stale: this server serves %s, and %s. Refusing writes. "+
			"Fix the binding, then reload the MCP server in your AI host", e.Bound, e.Reason)
	}
	return fmt.Sprintf(
		"vault binding is stale: this server bound %s at startup, but %s now resolves to %s. "+
			"A running MCP server resolves its vault ONCE, at startup — a mid-session config change "+
			"(a vault_path, or a [project_vaults] binding) "+
			"does not re-bind it, so continuing would write this project's history into the vault you "+
			"have already moved away from, while the CLI and every new `vp hook` write the other one. "+
			"Refusing. Fix: restart the MCP server by reloading it in your AI host — killing the server "+
			"alone leaves that session with no vp_* tools",
		e.Bound, e.Source, e.Resolved)
}

// CheckVaultBinding compares boundRoot against a fresh resolution from
// launchCwd — the directory boundRoot was originally resolved from.
//
// Returns:
//   - nil when they agree, or when the check is unarmed (either argument empty)
//   - *StaleBindingError when both resolve and the roots DIFFER — the
//     condition this guard exists for — or when the resolution refuses a
//     stale [project_vaults] binding (StaleProjectBindingError)
//   - the wrapped resolution error otherwise
//
// A resolution failure is NOT drift and callers must not treat it as such —
// with one exception, a stale [project_vaults] binding, reported above as a
// StaleBindingError because the binding still names a vault that no longer
// holds the project. An
// absent global config, an unreadable file, or a vault_path swallowed by a
// table all mean the new config governs NOTHING — it is refused everywhere it
// is read — so boundRoot remains the only vault in effect and refusing writes
// would strand a session over a file nobody is bound to. Report and continue.
func CheckVaultBinding(boundRoot, launchCwd string) error {
	if boundRoot == "" || launchCwd == "" {
		return nil
	}
	resolved, source, err := ResolveVaultPath(launchCwd)
	if err != nil {
		// A stale project binding IS drift, unlike every other resolution
		// failure: the binding still names a vault, and that vault no longer
		// holds the project, so writing on would plant a fresh copy of it there.
		var stale *StaleProjectBindingError
		if errors.As(err, &stale) {
			return &StaleBindingError{Bound: boundRoot, Resolved: stale.Target,
				Source: "binding:" + stale.CfgPath + "#" + stale.Slug, Reason: stale.Error()}
		}
		return err
	}
	if sameVaultRoot(boundRoot, resolved) {
		return nil
	}
	return &StaleBindingError{Bound: boundRoot, Resolved: resolved, Source: source}
}

// sameVaultRoot reports whether two vault roots name the same tree: Clean
// first, then EvalSymlinks only if that disagrees, because a symlink resolving
// to the same tree is not drift.
//
// The bias is deliberate and one-directional. This comparison gates WRITES, so
// a false positive (refusing a healthy session) is the expensive error and a
// false negative merely leaves the prior silent behavior in place. A failing
// EvalSymlinks — usually a root that does not exist yet — leaves the lexical
// verdict standing rather than upgrading it to drift on a guess.
func sameVaultRoot(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}
