// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// nonAlphanumericHook matches runs of characters that are not lowercase
// alphanumeric, used for fallback slug generation.
var nonAlphanumericHook = regexp.MustCompile(`[^a-z0-9]+`)

// fallbackSlug derives a lowercase-hyphen slug from the basename of dir.
func fallbackSlug(dir string) string {
	base := filepath.Base(dir)
	s := strings.ToLower(base)
	s = nonAlphanumericHook.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "unknown"
	}
	return s
}

func cmdHook(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:           "hook",
		Synopsis:       "vp hook",
		Description:    "Claude Code hook handler. Reads hook JSON from stdin and archives the session transcript. Also supports install/uninstall subcommands.",
		Subcommands:    []string{"hook install", "hook uninstall"},
		BareInvocation: true,
		Run: func(args []string) int {
			return runHook(info)
		},
	}
}

// runHook is the core hook handler invoked when stdin has data.
func runHook(info cli.BuildInfo) int {
	// Detect whether stdin is a terminal.
	fi, err := os.Stdin.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp hook: stat stdin: %v\n", err)
		return cli.ExitSystem
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		// Interactive terminal — print usage.
		fmt.Fprintln(os.Stderr, "Usage: vp hook\n\nReads Claude Code hook JSON from stdin.\nSubcommands: hook install, hook uninstall")
		return cli.ExitOK
	}

	// Read hook payload from stdin.
	var payload hook.Payload
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "vp hook: invalid JSON on stdin: %v\n", err)
		return cli.ExitUser
	}

	// Detect project slug (fallback to directory basename).
	proj, err := project.DetectProject(payload.CWD)
	if err != nil || proj == "" {
		proj = fallbackSlug(payload.CWD)
	}

	// Open vault from the hook's CWD.
	//
	// The global fallback is for an ABSENT cwd override. It must NEVER run for a
	// REJECTED one: this is the capture path that caused iteration 210, and the
	// global vault is the LIVE vault. An operator who wrote a vault_path meant
	// their work to go somewhere else; falling back here writes the throwaway
	// session into real project history — the precise defect readCwdVaultPath
	// now refuses, re-entered through the back door. Refuse and capture nothing:
	// a lost capture is recoverable, a polluted history is not.
	vault, err := storage.OpenVaultFromCwd(payload.CWD)
	if err != nil {
		if errors.Is(err, storage.ErrSwallowedVaultPath) {
			// Alarm through the two sanctioned channels — the log and the result
			// body — never the exit code, per the ruling below.
			//
			// Reaching the log takes an explicit step here. Pre-run initLogging()
			// resolves through openProjectVault, which fails on THIS error, so no
			// handler is attached and slog would degrade to stderr: the alarm is
			// missing precisely when it fires. Attaching the global vault's log is
			// not the fallback this branch refuses — it writes one diagnostic line,
			// never session history, and vp_health reads that file.
			if gv, gerr := storage.OpenVaultGlobal(); gerr == nil {
				initLoggingForVault(gv)
			}
			slog.Error("hook: refusing to capture into the live vault",
				"cwd", payload.CWD, "event", payload.HookEventName, "err", err)
			res := &hook.Result{Event: payload.HookEventName, Error: err.Error()}
			if encErr := json.NewEncoder(os.Stdout).Encode(res); encErr != nil {
				slog.Error("hook: encode result failed", "err", encErr, "event", payload.HookEventName)
			}
			return cli.ExitOK
		}
		// Fallback: try the global vault.
		vault, err = storage.OpenVaultGlobal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp hook: cannot open vault: %v\n", err)
			return cli.ExitSystem
		}
	}

	// Re-point logging at the vault this hook is actually writing to. The CLI
	// pre-run already initialized a logger, but it resolved the vault from this
	// process's cwd — and the hook's vault comes from the CWD in the payload we
	// only just parsed off stdin. Those differ whenever the hook runs from
	// somewhere other than the project it is capturing, which is the normal
	// case. Without this, the hook's warnings land in a different vault's log.
	initLoggingForVault(vault)

	opts := hook.RunOptions{
		VaultRoot:   vault.Root,
		ProjectSlug: proj,
		VPVersion:   info.Version,
	}

	// A hook failure is reported through the LOG and the result body — never
	// through the exit code. cli.ExitSystem is 2, and 2 is Claude Code's reserved
	// BLOCKING-error code: the hook is installed on SessionEnd, Stop and
	// PreCompact, and Stop fires once per assistant turn. Exiting 2 there blocks
	// the turn and feeds stderr back into the model, so a deterministically-failing
	// hook (bad config, missing dir) would blocking-error on the first turn of
	// every session, forever, in a loop. At SessionEnd the same exit blocks
	// nothing at all — it is a silent shrug. So the exit code is obnoxious at one
	// end and useless at the other, and it is not the alarm. slog is: it reaches
	// vp.log, which vp_health reads.
	result, err := hook.Run(context.Background(), payload, opts)
	if err != nil {
		slog.Error("hook: run failed", "err", err, "event", payload.HookEventName, "cwd", payload.CWD)
		result = &hook.Result{Event: payload.HookEventName, Error: err.Error()}
	}

	// Write result JSON to stdout.
	if encErr := json.NewEncoder(os.Stdout).Encode(result); encErr != nil {
		slog.Error("hook: encode result failed", "err", encErr, "event", payload.HookEventName)
	}
	return cli.ExitOK
}
