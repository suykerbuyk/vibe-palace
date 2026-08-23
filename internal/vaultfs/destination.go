// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"fmt"
	"path/filepath"
)

// RefuseDestinationInsideVault reports ErrDestinationInsideVault when destPath
// resolves to a location inside vaultPath.
//
// This is the INVERSE of ResolveSafePath. ResolveSafePath takes a vault-relative
// path and proves it stays UNDER the vault; this takes an arbitrary host path an
// operator typed and proves it stays OUT. Export destinations (`vp audit rooms
// --export`, `vp discover rooms --export`, `vp tune rooms --export`, `vp archive
// extract --to`) are the callers: an export that lands on a vault document
// overwrites it with no lock, no surface stamp, no containment check and no
// compare-and-set, and the command still reports success.
//
// It returns an error only, never a resolved path. Handing callers the realpath
// would invite them to os.Create THAT instead of the path the operator typed,
// silently relocating a legitimate export whose destination is itself a symlink.
//
// FAIL-CLOSED: if the vault root cannot be resolved, the destination is refused
// rather than allowed. An unresolvable vault root is exactly the state in which
// we do not know whether destPath is inside it. This mirrors ResolveSafePath,
// which also errors when the root will not resolve. That refusal is a distinct
// error (not ErrDestinationInsideVault) so callers can exit ExitSystem for a
// broken configuration and ExitUser for a bad destination.
//
// Both sides are made absolute BEFORE symlink resolution. destPath is
// operator-typed and may be relative; EvalSymlinks preserves relativity, and a
// relative candidate never prefix-matches an absolute root, so comparing without
// filepath.Abs would fail OPEN — `--export Projects/x/resume.md` run from inside
// the vault would write straight through.
//
// The resolution ladder after that is ResolveSafePath's, for the same reasons:
// EvalSymlinks both sides; if the destination does not exist yet resolve its
// parent instead; if the parent does not exist either fall back to a lexical
// filepath.Clean. Comparison is pathIsUnder, which is separator-safe, so
// "/vault-backup" is correctly not under "/vault".
func RefuseDestinationInsideVault(vaultPath, destPath string) error {
	// An empty vault root must fail closed BEFORE Abs. filepath.Abs("")
	// succeeds and yields the process working directory, so an empty root would
	// silently compare the destination against the cwd — a different question
	// entirely, and one that answers "allowed" for anything outside the cwd.
	// This is not ErrDestinationInsideVault: it is an unusable configuration,
	// and callers route it to a system-fault exit rather than a user-fault one.
	if vaultPath == "" {
		return fmt.Errorf("vaultfs: cannot verify destination %q: vault root is empty", destPath)
	}

	absVault, err := filepath.Abs(vaultPath)
	if err != nil {
		return fmt.Errorf("vaultfs: resolve vault root %q: %w", vaultPath, err)
	}
	realVault, err := filepath.EvalSymlinks(absVault)
	if err != nil {
		return fmt.Errorf("vaultfs: resolve vault root %q: %w", vaultPath, err)
	}

	absDest, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("vaultfs: resolve destination %q: %w", destPath, err)
	}

	candidate, err := filepath.EvalSymlinks(absDest)
	if err != nil {
		// Destination does not exist yet — judge it by its parent, which is
		// where the file would actually be created.
		parent := filepath.Dir(absDest)
		realParent, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			// Parent does not exist either; fall back to the lexical path.
			candidate = filepath.Clean(absDest)
		} else {
			candidate = filepath.Join(realParent, filepath.Base(absDest))
		}
	}

	if pathIsUnder(candidate, realVault) {
		return fmt.Errorf("%w: %q resolves to %q, inside the vault at %q",
			ErrDestinationInsideVault, destPath, candidate, realVault)
	}
	return nil
}
