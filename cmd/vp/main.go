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
	// dirty is stamped by the Makefile from $(VERSION); see cli.BuildInfo.Dirty.
	// It defaults to EMPTY, not "false": an un-stamped build does not know
	// whether its source was committed, and must not claim it was.
	dirty = ""
)

func main() {
	info := cli.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate, Dirty: dirty}
	reg := cli.NewRegistry(info)
	reg.SetPreRun(preRun)
	registerAll(reg, info)
	os.Exit(reg.Dispatch(os.Args[1:]))
}

// preRun is the CLI dispatch pre-run hook (see cli.Registry.SetPreRun). Every
// leaf invocation funnels through it, which makes it the one place that can
// give a command a logger before it runs.
//
// It has to: vplog.Init used to be reachable only from bootstrap(), and only
// `vp mcp` and `vp mcp serve` call bootstrap. So ~40 of the ~42 entry points —
// `vp hook` among them — ran with Go's default slog handler and wrote their
// warnings to stderr, where nothing reads them. `vp hook` is the only path that
// archives a transcript or derives a model, so its 11 slog calls were the ones
// that mattered most and the ones most reliably lost.
func preRun(cmd *cli.Command) int {
	initLogging()
	return surfaceGate(cmd)
}

// initLogging attaches logging to the vault this command will act on. Silent
// and best-effort — a command must never fail because logging could not be set
// up, and a cwd outside any vault legitimately has nowhere to log.
//
// Resolution goes through openProjectVault (not surfaceGate's vaultRoot, which
// uses OpenVaultGlobal) so a cwd-local .vibe-palace.toml vault_path override is
// honored. Otherwise a project that redirects its vault would log to the global
// one — the right lines in the wrong file.
func initLogging() {
	v, err := openProjectVault()
	if err != nil {
		return
	}
	initLoggingForVault(v)
}

// surfaceGate is the MCP surface gate on the CLI side, invoked from preRun.
// Vault-
// mutating commands (marked via mutates() in registerAll) fail-stop when the
// vault's surface version exceeds this binary's, honoring VP_SURFACE_GATE=warn;
// every other command is warn-only so a stale binary never blocks a read or a
// capture. A vault path that cannot even be resolved degrades to a no-op here;
// a resolved-but-unreachable root does NOT — CheckCompatible reports it, so a
// mutating command fail-stops rather than writing into the void.
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
