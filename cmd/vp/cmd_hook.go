// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	vault, err := storage.OpenVaultFromCwd(payload.CWD)
	if err != nil {
		// Fallback: try the global vault.
		vault, err = storage.OpenVaultGlobal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp hook: cannot open vault: %v\n", err)
			return cli.ExitSystem
		}
	}

	opts := hook.RunOptions{
		VaultRoot:   vault.Root,
		ProjectSlug: proj,
		VPVersion:   info.Version,
	}

	result, err := hook.Run(context.Background(), payload, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp hook: %v\n", err)
		return cli.ExitSystem
	}

	// Write result JSON to stdout.
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "vp hook: encode result: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}
