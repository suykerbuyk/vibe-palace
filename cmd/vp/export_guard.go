// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// allowInsideVaultFlag is the per-invocation override shared by every command
// that writes to an operator-supplied destination. One name across all of them
// deliberately: the defect is a CLASS, and a per-command spelling would invite
// the same partial fix the `--to`-not-`--export` trap already caused once.
//
// It is a flag and never an environment variable. An env var set once stays set
// for every later invocation, which turns a deliberate one-off override into a
// silently disabled guard.
const allowInsideVaultFlag = "--allow-inside-vault"

// allowInsideVaultHelp is the flag's help text. It states the per-invocation
// scope, because a reader who believes the override is sticky will reach for an
// env var instead.
const allowInsideVaultHelp = "Permit an output destination inside the vault (this invocation only)"

// guardExportDestination refuses an output destination that resolves inside the
// vault, unless the operator passed the per-invocation override.
//
// It returns cli.ExitOK when the write may proceed. Otherwise it prints a
// curated remediation naming both the destination and the override flag, and
// returns the exit code the caller should return:
//
//   - cli.ExitUser when the destination resolves inside the vault — the operator
//     supplied a bad destination and can fix it by retyping it.
//   - cli.ExitSystem when the vault root itself could not be resolved — a config
//     or runtime fault (unmounted volume, bad vault_path), not a bad
//     destination. The predicate is fail-closed there, so this path refuses
//     rather than writing somewhere it could not verify.
func guardExportDestination(vaultRoot, dest string, allowInsideVault bool, out io.Writer) int {
	// The predicate ALWAYS runs, including when the override is set. The
	// override is permission to write inside a KNOWN vault — it is not
	// permission to skip verification. Checking the flag first would fail open
	// on an unresolvable vault root, which is the one case the predicate is
	// fail-closed about.
	err := vaultfs.RefuseDestinationInsideVault(vaultRoot, dest)
	if err == nil {
		return cli.ExitOK
	}

	if errors.Is(err, vaultfs.ErrDestinationInsideVault) {
		// The vault resolved and the destination is genuinely inside it. This
		// is the only finding the override applies to.
		if allowInsideVault {
			return cli.ExitOK
		}
		fmt.Fprintf(out, "Error: refusing to write %s: it is inside the vault.\n", dest)
		fmt.Fprintf(out, "  A write here would bypass the vault lock, the surface stamp and the containment checks,\n")
		fmt.Fprintf(out, "  and could overwrite a vault document. Choose a destination outside the vault,\n")
		fmt.Fprintf(out, "  or pass %s to override for this invocation only.\n", allowInsideVaultFlag)
		return cli.ExitUser
	}

	// The vault root could not be resolved, so whether the destination is
	// inside it is UNKNOWN. There is no override for that, and the message
	// deliberately does not offer the flag: pointing at it here would write the
	// same fail-open hole into the remediation.
	fmt.Fprintf(out, "Error: cannot verify destination %s: %v\n", dest, err)
	fmt.Fprintf(out, "  The vault root could not be resolved, so whether the destination is inside it is unknown.\n")
	fmt.Fprintf(out, "  Refusing rather than guessing. Check vault_path with `vp status`.\n")
	return cli.ExitSystem
}
