// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import "testing"

// TestMigrateProjectSlugIsRetired pins the U12 retirement of the one-shot
// `vp migrate project-slug` command (rename-docs-harness-deletion-and-
// registration): the supported path is `vp vault rename`. Both halves can
// fail — restore the reg.Register line in commands.go and the lookup passes;
// leave it listed in the migrate parent's Subcommands and the list check passes.
func TestMigrateProjectSlugIsRetired(t *testing.T) {
	reg, _, _ := testRegistry()
	if _, ok := reg.Lookup("migrate project-slug"); ok {
		t.Error(`retired command "migrate project-slug" is still registered; ` +
			`U12 replaced it with "vp vault rename"`)
	}
	parent, ok := reg.Lookup("migrate")
	if !ok {
		t.Fatal(`parent command "migrate" not registered`)
	}
	if contains(parent.Subcommands, "migrate project-slug") {
		t.Error(`retired "migrate project-slug" is still listed in the migrate Subcommands`)
	}
}
