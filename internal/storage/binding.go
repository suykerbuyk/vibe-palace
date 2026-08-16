// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
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
	// Source names the config that supplies Resolved ("cwd:<file>" or
	// "global:<file>"), so the operator knows which file to look at.
	Source string
}

func (e *StaleBindingError) Error() string {
	return fmt.Sprintf(
		"vault binding is stale: this server bound %s at startup, but %s now resolves to %s. "+
			"A running MCP server resolves vault_path ONCE, at startup — a mid-session config change "+
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
//   - *StaleBindingError when both resolve and the roots DIFFER — the one
//     condition this guard exists for
//   - the wrapped resolution error otherwise
//
// A resolution failure is NOT drift and callers must not treat it as such. An
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
