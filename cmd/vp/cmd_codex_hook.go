// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/codexhook"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func cmdCodex() *cli.Command {
	return &cli.Command{Name: "codex", Synopsis: "vp codex <command>", Description: "Codex host integration commands."}
}

func cmdCodexHook() *cli.Command {
	return &cli.Command{Name: "codex hook", Synopsis: "vp codex hook <event>", Description: "Codex hook handlers."}
}

func cmdCodexHookSessionStart() *cli.Command {
	return &cli.Command{
		Name:        "codex hook session-start",
		Synopsis:    "vp codex hook session-start",
		Description: "Emit the explicit-project bootstrap instruction for a Codex SessionStart hook.",
		Run: func(args []string) int {
			if len(args) != 0 {
				fmt.Fprintln(os.Stderr, "vp codex hook session-start: does not accept arguments")
				return cli.ExitUser
			}
			return runCodexSessionStart(os.Stdin, os.Stdout, os.Stderr)
		},
	}
}

func cmdCodexHookSessionEnd(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name: "codex hook session-end", Synopsis: "vp codex hook session-end",
		Description: "Archive the transcript Codex supplies at SessionEnd and write a typed receipt.",
		Run: func(args []string) int {
			if len(args) != 0 {
				fmt.Fprintln(os.Stderr, "vp codex hook session-end: does not accept arguments")
				return cli.ExitUser
			}
			return runCodexSessionEnd(info, os.Stdin, os.Stdout, os.Stderr)
		},
	}
}

// runCodexSessionEnd is the Codex SessionEnd handler, shaped like runHook: the
// handler resolves the vault from the EVENT's cwd, and this function supplies
// the two things that have to happen against that vault before any work —
// logging and the surface check — plus the per-process values the handler
// cannot resolve for itself.
//
// 🔴 THE EXIT CODE IS DELIBERATELY UNCHANGED. A failure still returns
// cli.ExitSystem (2). The Claude ruling in cmd_hook.go — never signal a hook
// failure through the exit code — rests on two premises that hold for Claude and
// are UNVERIFIED for Codex: that 2 is this host's reserved blocking code, and
// that the hook also runs on Stop (once per assistant turn), which is what turns
// a deterministic failure into a per-turn loop. vp installs no Stop hook for
// Codex. Settling it needs a Codex hook-protocol reference or a deliberate
// failing-hook experiment, and changing a host-boundary signal on an unverified
// premise is not something to do as a side effect. What DID change is that the
// failure now reaches vp.log, which is the evidence that question needs.
//
// A GATE SKIP is not a failure: HandleSessionEnd returns nil for it and this
// returns cli.ExitOK, so a stale checkout never reads as a hook crash.
func runCodexSessionEnd(info cli.BuildInfo, in io.Reader, out, errw io.Writer) int {
	opts := codexhook.Options{
		VPVersion: info.Version,
		Launch:    detachlaunch.Launch,
		// Both steps the Claude hook performs once its real vault is known:
		// re-point logging off the process-cwd vault that preRun attached, and
		// run the surface check against the vault being written rather than the
		// configured one. surfaceGate exempts this command for exactly that
		// reason (see cmd/vp/main.go).
		OnVaultResolved: func(v *storage.Vault) {
			initLoggingForVault(v)
			surface.EnforceWarnOnly(v.Root)
		},
		// A refused binding has no vault, so the diagnostic has nowhere to go.
		// Attach the GLOBAL vault's log — and ONLY its log.
		//
		// 🔴 THIS IS NOT THE GLOBAL-VAULT FALLBACK. cmd_hook.go has one
		// (storage.OpenVaultGlobal as a capture target); this path has none and
		// must not grow one, because the global vault is the LIVE vault and the
		// refusal being reported is precisely the guard against writing
		// throwaway work into real project history. The handler still returns
		// the error and captures nothing. One line, then out.
		OnLiveVaultRefusal: func() {
			if gv, err := storage.OpenVaultGlobal(); err == nil {
				initLoggingForVault(gv)
			}
		},
	}
	if err := codexhook.HandleSessionEnd(in, out, opts); err != nil {
		fmt.Fprintf(errw, "vp codex hook session-end: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}

func runCodexSessionStart(in io.Reader, out, errw io.Writer) int {
	if err := codexhook.HandleSessionStart(in, out); err != nil {
		fmt.Fprintf(errw, "vp codex hook session-start: %v\n", err)
		// Codex hook output is advisory. A failure to produce context must not
		// block the session; AGENTS.md remains the instruction fallback.
	}
	return cli.ExitOK
}
