// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import "github.com/suykerbuyk/vibe-palace/internal/cli"

// cmdIndex is the parent of the host-local index maintenance commands. Like
// `vp drain`, it has no Run: a bare `vp index` renders its children's help.
func cmdIndex() *cli.Command {
	return &cli.Command{
		Name:        "index",
		Synopsis:    "vp index <command> [flags]",
		Description: "Maintenance of this host's local semantic search index (palace/.local/).",
	}
}
