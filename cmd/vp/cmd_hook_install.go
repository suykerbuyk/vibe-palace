// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
)

func cmdHookInstall() *cli.Command {
	return &cli.Command{
		Name:        "hook install",
		Synopsis:    "vp hook install",
		Description: "Install vp hook entries in ~/.claude/settings.json for SessionEnd, Stop, and PreCompact events. Coexists with existing vv hook entries.",
		Run: func(args []string) int {
			// Check for legacy hooks before install.
			status, _ := hook.Status()

			changed, err := hook.Install()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp hook install: %v\n", err)
				return cli.ExitSystem
			}
			if !changed {
				fmt.Println("Hook already installed in ~/.claude/settings.json — no changes needed.")
				return cli.ExitOK
			}
			msg := "Hook installed in ~/.claude/settings.json"
			if status.LegacyPresent {
				msg += " (vv hook preserved — both will fire)"
			}
			fmt.Println(msg)
			return cli.ExitOK
		},
	}
}

func cmdHookUninstall() *cli.Command {
	return &cli.Command{
		Name:        "hook uninstall",
		Synopsis:    "vp hook uninstall",
		Description: "Remove vp hook entries from ~/.claude/settings.json.",
		Run: func(args []string) int {
			changed, err := hook.Uninstall()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp hook uninstall: %v\n", err)
				return cli.ExitSystem
			}
			if !changed {
				fmt.Println("No vp hook entries found in ~/.claude/settings.json — nothing to remove.")
				return cli.ExitOK
			}
			fmt.Println("Hook entries removed from ~/.claude/settings.json")
			return cli.ExitOK
		},
	}
}
