// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

var (
	version   = "0.1.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	info := cli.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}
	reg := cli.NewRegistry(info)
	reg.SetPreRun(surfaceGate)
	registerAll(reg, info)
	os.Exit(reg.Dispatch(os.Args[1:]))
}

// surfaceGate is the CLI dispatch pre-run hook (see cli.Registry.SetPreRun) and
// the single choke-point for the MCP surface gate on the CLI side. Vault-
// mutating commands (marked via mutates() in registerAll) fail-stop when the
// vault's surface version exceeds this binary's, honoring VP_SURFACE_GATE=warn;
// every other command is warn-only so a stale binary never blocks a read or a
// capture. An unresolvable vault degrades to a no-op (and surface.CheckCompatible
// itself no-ops on an empty/unreachable path).
func surfaceGate(cmd *cli.Command) int {
	root, err := vaultRoot()
	if err != nil {
		return cli.ExitOK
	}
	if cmd.MutatesVault {
		if gerr := surface.EnforceFailStop(root); gerr != nil {
			fmt.Fprintln(os.Stderr, gerr.Error())
			return cli.ExitSystem
		}
		return cli.ExitOK
	}
	surface.EnforceWarnOnly(root)
	return cli.ExitOK
}
