// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// TestVaultGCIsRegisteredUngated pins that `vault gc` is wired into the registry
// and is NOT marked MutatesVault: it runs no git and reaches no vault-write funnel
// sink, so it must stay declared ungated (the derived-gate check in
// internal/sourceaudit enforces the agreement; this pins the registration itself,
// which that check reads vacuously green if the command is absent).
func TestVaultGCIsRegisteredUngated(t *testing.T) {
	reg := cli.NewRegistry(cli.BuildInfo{})
	registerAll(reg, cli.BuildInfo{})

	cmd, ok := reg.Lookup("vault gc")
	if !ok {
		t.Fatal("`vault gc` is not registered")
	}
	if cmd.MutatesVault {
		t.Error("`vault gc` must NOT be marked MutatesVault: it runs no git and writes no vault content")
	}

	// The constructor itself must also be ungated (belt and braces against a future
	// mutates() wrap).
	if cmdVaultGC().MutatesVault {
		t.Error("cmdVaultGC() carries MutatesVault; it must be registered without mutates()")
	}
}
