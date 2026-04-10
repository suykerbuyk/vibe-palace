// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var vaultDryRunFlag = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print git commands without executing"},
}

func cmdVault() *cli.Command {
	return &cli.Command{
		Name:        "vault",
		Synopsis:    "vp vault <command> [flags]",
		Description: "Manage the vault git repository. The vault stores all palace data in a git-tracked directory for versioning and multi-machine sync.",
		Subcommands: []string{"vault pull", "vault push", "vault sync"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp vault <command> [flags]\n\nRun 'vp vault --help' for details.")
			return cli.ExitOK
		},
	}
}

func cmdVaultPull() *cli.Command {
	return &cli.Command{
		Name:        "vault pull",
		Synopsis:    "vp vault pull [--dry-run]",
		Description: "Pull from all configured vault remotes.",
		Flags:       vaultDryRunFlag,
		Examples: []cli.Example{
			{Cmd: "vp vault pull", Comment: "Pull from all remotes"},
			{Cmd: "vp vault pull --dry-run", Comment: "Preview pull commands without executing"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultDryRunFlag, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault pull: %v\n", err)
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault pull: %v\n", err)
				return cli.ExitUser
			}
			remotes, err := gitRemotes(root)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault pull: %v\n", err)
				return cli.ExitSystem
			}
			return pullAll(root, remotes, fv.Bool("--dry-run"))
		},
	}
}

func cmdVaultPush() *cli.Command {
	return &cli.Command{
		Name:        "vault push",
		Synopsis:    "vp vault push [--dry-run]",
		Description: "Push to all configured vault remotes. Requires clean vault state.",
		Flags:       vaultDryRunFlag,
		Examples: []cli.Example{
			{Cmd: "vp vault push", Comment: "Push to all remotes"},
			{Cmd: "vp vault push --dry-run", Comment: "Preview push commands without executing"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultDryRunFlag, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault push: %v\n", err)
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault push: %v\n", err)
				return cli.ExitUser
			}
			remotes, err := gitRemotes(root)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault push: %v\n", err)
				return cli.ExitSystem
			}
			return pushAll(root, remotes, fv.Bool("--dry-run"))
		},
	}
}

func cmdVaultSync() *cli.Command {
	return &cli.Command{
		Name:        "vault sync",
		Synopsis:    "vp vault sync [--dry-run]",
		Description: "Pull then push all configured vault remotes.",
		Flags:       vaultDryRunFlag,
		Examples: []cli.Example{
			{Cmd: "vp vault sync", Comment: "Pull then push all remotes"},
			{Cmd: "vp vault sync --dry-run", Comment: "Preview sync commands without executing"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultDryRunFlag, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault sync: %v\n", err)
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault sync: %v\n", err)
				return cli.ExitUser
			}
			remotes, err := gitRemotes(root)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault sync: %v\n", err)
				return cli.ExitSystem
			}
			dryRun := fv.Bool("--dry-run")
			if code := pullAll(root, remotes, dryRun); code != cli.ExitOK {
				return code
			}
			return pushAll(root, remotes, dryRun)
		},
	}
}

func vaultRoot() (string, error) {
	v, err := storage.OpenVault("")
	if err != nil {
		return "", err
	}
	return v.Root, nil
}

func gitRemotes(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "remote")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git remote: %w", err)
	}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			remotes = append(remotes, line)
		}
	}
	if len(remotes) == 0 {
		return nil, fmt.Errorf("no git remotes configured in %s", root)
	}
	return remotes, nil
}

func pullAll(root string, remotes []string, dryRun bool) int {
	for _, remote := range remotes {
		if dryRun {
			fmt.Fprintf(os.Stderr, "would run: git -C %s pull %s main\n", root, remote)
			continue
		}
		fmt.Fprintf(os.Stderr, "Pulling from %s...\n", remote)
		cmd := exec.Command("git", "-C", root, "pull", remote, "main")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "vp vault pull: %s: %v\n", remote, err)
			// Continue trying other remotes.
		}
	}
	return cli.ExitOK
}

func pushAll(root string, remotes []string, dryRun bool) int {
	// Check for clean state.
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp vault push: git status: %v\n", err)
		return cli.ExitSystem
	}
	if len(bytes.TrimSpace(out)) > 0 {
		fmt.Fprintf(os.Stderr, "vp vault push: vault has uncommitted changes:\n%s\nCommit or stash changes before pushing.\n", out)
		return cli.ExitUser
	}

	for _, remote := range remotes {
		if dryRun {
			fmt.Fprintf(os.Stderr, "would run: git -C %s push %s main\n", root, remote)
			continue
		}
		fmt.Fprintf(os.Stderr, "Pushing to %s...\n", remote)
		cmd := exec.Command("git", "-C", root, "push", remote, "main")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "vp vault push: %s: %v\n", remote, err)
		}
	}
	return cli.ExitOK
}
