// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
)

// ScaffoldNewVault creates a NEW vault at dest through the only scaffold that
// stamps the vault data format: the directory, `git init`, the canonical
// .gitignore and the .vibe-palace/vault.toml format stamp. Its callers are the
// split's destination (internal/tools) and `vp vault init`
// (lifecycle-commands-design-plan, unit U4).
//
// 🔴 THE FIRST ARGUMENT IS dest, NOT A WORKING DIRECTORY. VaultReconciler
// resolves its target as seed.VaultPath whenever the seed is set and that field
// is non-empty (vault.go:45-51), and falls through to
// storage.ResolveVaultPath(root) otherwise. `vp init` passes cwd because cwd is
// the project tree it is initialising from; an MCP handler passing cwd would
// resolve the SOURCE vault through .vibe-palace.toml or the global vault_path
// and reconcile the vault it is splitting FROM. Passing dest is the fail-closed
// argument, and VaultPath: dest plus WithCreate() is what makes it binding.
//
// 🔴 VaultReconciler.Apply RETURNS (Report, nil) ALWAYS. Its failures — mkdir,
// git init, the .gitignore write or top-up, and the data-format stamp — land in
// Report.Errors, and the stamp failure in particular still increments Created.
// So err is not the channel here, Report.Errors is, and ANY entry in it aborts
// before a single byte is copied. That is stricter than initGlobal in
// cmd/vp/cmd_init.go, which triages the same slice by error prefix and treats a
// failed stamp as non-fatal; a CLI that keeps going leaves a human looking at
// the terminal, and this does not.
//
// The Templates reconcile is not run here. Under Design B it writes nothing on
// a fresh destination (every embedded resource is served from the embedded
// floor), and the canonical .gitignore it used to reconcile is the Vault
// reconciler's Create above. A destination has no Templates/ until an
// operator writes an override.
func ScaffoldNewVault(ctx context.Context, dest string) error {
	vr := NewVault(dest, VaultSeed{
		VaultPath: dest,
		// Unconditionally true, with no GitAvailable probe. A missing git
		// binary surfaces as a GitInit error in Report.Errors and aborts, which
		// is the fail-closed outcome: a publishable split whose destination is
		// not a repository cannot be published, and storage.ListRemotes — which
		// verify calls — needs a repository to answer at all.
		GitEnabled: true,
	}.WithCreate())

	plan, err := vr.Plan(ctx)
	if err != nil {
		return fmt.Errorf("plan destination vault: %w", err)
	}
	// A seeded plan that cannot give dest its own repository (nested inside
	// another work tree, or an unverifiable .git) is ONLY that Skip, and applies
	// nothing — so it must abort here, never read as a scaffold that succeeded.
	for _, a := range plan.Actions {
		if a.Kind == ActionSkip {
			return fmt.Errorf("scaffold destination vault: %s", a.Summary)
		}
	}
	rep, err := vr.Apply(ctx, plan)
	if err != nil {
		return fmt.Errorf("scaffold destination vault: %w", err)
	}
	if len(rep.Errors) > 0 {
		return fmt.Errorf("scaffold destination vault: %w", errors.Join(rep.Errors...))
	}

	return nil
}
