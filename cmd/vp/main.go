// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
	check.BuildVersion = version
	// Arm the pull/merge ingester trigger with the real detached launcher. A
	// library build and every test leaves it unset, so they spawn nothing and
	// never relaunch the test binary (ADR-014 decision 7).
	storage.SetIncomingIngestLauncher(detachlaunch.Launch)
	// Arm the cross-vault write guard (U3): the real binary refuses a content
	// write for a project bound to another vault. Unset in library builds and
	// tests (like the ingest launcher), so they never fail-close.
	storage.ArmProjectVaultGuard()
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
	installProjectVaultSnapshot(cmd)
	return surfaceGate(cmd)
}

// installProjectVaultSnapshot builds the host's [project_vaults] routing snapshot
// EAGERLY — before the command runs, before any write lock — and installs it in
// the storage holder the write guard (U3) and derived-delete warning classify
// against. The build runs git (staleProjectBinding), which is why it must happen
// here and never inside the locked write funnel.
//
// Uniform predicate: it runs for EVERY write-capable command — MutatesVault (which
// includes the lifecycle commands vault copy/clone/rename/project-delete) plus the
// known non-MutatesVault writers (`hook`, Codex's bounded SessionEnd hook, and `vault pull`/`vault sync`, which
// reach the derived-delete sink). The LIFECYCLE exemption is NOT a command here:
// it is per-write-site, via atomicfile.ForMisboundExempt at the relocating write
// (storage.CopyProjectTreeEntry), so it covers both the CLI and the MCP tools.
//
// On a build failure the (unbuilt) snapshot is installed anyway: the content guard
// then fail-closes writes (surfacing the broken config). Reads and non-write
// commands install nothing; the guard is also inert unless armed (main-only).
func installProjectVaultSnapshot(cmd *cli.Command) {
	switch {
	case cmd.MutatesVault, cmd.Name == "hook", cmd.Name == "codex hook session-end", cmd.Name == "vault pull", cmd.Name == "vault sync":
	default:
		return
	}
	snap, err := storage.BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		slog.Warn("vault: project-vault routing snapshot unbuilt; cross-vault writes fail closed",
			"op", "cli.preRun", "fault", "operational", "err", err)
	}
	storage.SetActiveProjectVaultSnapshot(snap)
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
	// The hook is exempt. surfaceGate resolves the CONFIGURED vault
	// (vaultRoot()), but the hook writes to the vault it resolves from the hook
	// payload's CWD — usually a different vault — so a warning here would name
	// the wrong one. The hook runs its own surface.EnforceWarnOnly against the
	// payload vault (see runHook), after it points logging there.
	if cmd.Name == "hook" {
		return cli.ExitOK
	}
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
