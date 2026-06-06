// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

var vaultDryRunFlag = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print git commands without executing"},
}

func cmdVault() *cli.Command {
	return &cli.Command{
		Name:        "vault",
		Synopsis:    "vp vault <command> [flags]",
		Description: "Manage the vault git repository. The vault stores all palace data in a git-tracked directory for versioning and multi-machine sync.",
		Subcommands: []string{
			"vault pull", "vault push", "vault sync", "vault commit",
			"vault read", "vault write", "vault edit", "vault delete",
			"vault move", "vault exists", "vault sha256",
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
			root, err := gitEnabledGuard()
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
			root, err := gitEnabledGuard()
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
			root, err := gitEnabledGuard()
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

var vaultCommitFlags = []cli.FlagDef{
	{Name: "--paths", Arg: "LIST", Help: "Comma-separated vault-relative paths to stage and commit (required)"},
	{Name: "--message", Arg: "MSG", Help: "Commit message (required)"},
	{Name: "--push", Help: "Push to all configured remotes after committing"},
}

func cmdVaultCommit() *cli.Command {
	return &cli.Command{
		Name:        "vault commit",
		Synopsis:    "vp vault commit --paths <p1,p2,...> --message <msg> [--push]",
		Description: "Stage and commit ONLY the named vault-relative paths (never git add -A), with a hostname-stamped message. Other dirty files are left untouched. Pass --push to also push to all configured remotes.",
		Flags:       vaultCommitFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault commit --paths Projects/foo/resume.md --message 'wrap foo'", Comment: "Commit one file locally"},
			{Cmd: "vp vault commit --paths Projects/foo/resume.md,Projects/foo/knowledge.md --message 'wrap' --push", Comment: "Commit two files and push"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultCommitFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault commit: %v\n", err)
				return cli.ExitUser
			}
			var paths []string
			for _, p := range strings.Split(fv.Get("--paths"), ",") {
				if p = strings.TrimSpace(p); p != "" {
					paths = append(paths, p)
				}
			}
			if len(paths) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault commit: --paths is required (comma-separated)")
				return cli.ExitUser
			}
			message := fv.Get("--message")
			if message == "" {
				fmt.Fprintln(os.Stderr, "vp vault commit: --message is required")
				return cli.ExitUser
			}
			root, err := gitEnabledGuard()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault commit: %v\n", err)
				return cli.ExitUser
			}
			push := fv.Bool("--push")
			res, err := storage.CommitAndPushPaths(root, message, paths, push)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault commit: %v\n", err)
				return cli.ExitSystem
			}
			if res.CommitSHA == "" {
				fmt.Fprintln(os.Stderr, "vp vault commit: nothing to commit for the given paths")
				return cli.ExitOK
			}
			fmt.Fprintf(os.Stderr, "Committed %s\n", res.CommitSHA)
			if push {
				for remote, rerr := range res.RemoteResults {
					if rerr != nil {
						fmt.Fprintf(os.Stderr, "  push %s: %v\n", remote, rerr)
					} else {
						fmt.Fprintf(os.Stderr, "  push %s: ok\n", remote)
					}
				}
			}
			return cli.ExitOK
		},
	}
}

func vaultRoot() (string, error) {
	v, err := storage.OpenVaultGlobal()
	if err != nil {
		return "", err
	}
	return v.Root, nil
}

// gitEnabledGuard wraps vaultRoot and checks that git_enabled is true.
// Returns the vault root path or an error if git is disabled.
func gitEnabledGuard() (string, error) {
	root, err := vaultRoot()
	if err != nil {
		return "", err
	}
	v := storage.NewVault(root)
	cfg, err := v.LoadConfig("")
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	if !cfg.GitEnabled {
		return "", fmt.Errorf("git is disabled (git_enabled = false in config)")
	}
	return root, nil
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

// ---------------------------------------------------------------------------
// Vault file CRUD subcommands.
//
// These share the internal/vaultfs backend with the vp_vault_* MCP tools — one
// implementation, two front-ends. The vault root is resolved via vaultRoot()
// (global config) for consistency with the sibling `vp vault` git subcommands.
// ---------------------------------------------------------------------------

// printVaultJSON writes res as indented JSON to stdout.
func printVaultJSON(res any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(os.Stderr, "vp vault: encode result: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}

var vaultReadFlags = []cli.FlagDef{
	{Name: "--max-bytes", Arg: "N", Help: "Read cap in bytes (default 1 MiB, max 10 MiB)"},
	{Name: "--json", Help: "Output JSON (content, bytes, sha256, mtime)"},
}

func cmdVaultRead() *cli.Command {
	return &cli.Command{
		Name:        "vault read",
		Synopsis:    "vp vault read <path> [--max-bytes N] [--json]",
		Description: "Read a vault-relative file. Prints content; --json adds size, sha256, mtime.",
		Flags:       vaultReadFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault read Projects/foo/agentctx/resume.md", Comment: "Print file content"},
			{Cmd: "vp vault read notes.md --json", Comment: "Print full metadata as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultReadFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault read: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault read: path argument required")
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault read: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Read(root, pos[0], int64(fv.Int("--max-bytes")))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault read: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			fmt.Print(res.Content)
			return cli.ExitOK
		},
	}
}

var vaultWriteFlags = []cli.FlagDef{
	{Name: "--content", Arg: "STR", Help: "File content (if omitted, read from stdin)"},
	{Name: "--expected-sha256", Arg: "SHA", Help: "Compare-and-set guard: current SHA-256 must match"},
	{Name: "--json", Help: "Output JSON result (bytes, sha256, replaced_sha256)"},
}

func cmdVaultWrite() *cli.Command {
	return &cli.Command{
		Name:        "vault write",
		Synopsis:    "vp vault write <path> [--content STR | <stdin>] [--expected-sha256 SHA] [--json]",
		Description: "Atomically write a vault-relative file. Content comes from --content or stdin. Refuses '.git' paths.",
		Flags:       vaultWriteFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault write notes.md --content 'hello'", Comment: "Write inline content"},
			{Cmd: "echo hi | vp vault write notes.md", Comment: "Write content from stdin"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultWriteFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault write: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault write: path argument required")
				return cli.ExitUser
			}
			content := fv.Get("--content")
			if !fv.IsSet("--content") {
				data, rerr := io.ReadAll(os.Stdin)
				if rerr != nil {
					fmt.Fprintf(os.Stderr, "vp vault write: read stdin: %v\n", rerr)
					return cli.ExitSystem
				}
				content = string(data)
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault write: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Write(root, pos[0], content, fv.Get("--expected-sha256"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault write: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			fmt.Printf("wrote %s (%d bytes, sha256 %s)\n", pos[0], res.Bytes, res.Sha256)
			return cli.ExitOK
		},
	}
}

var vaultEditFlags = []cli.FlagDef{
	{Name: "--old", Arg: "STR", Help: "String to replace (required)"},
	{Name: "--new", Arg: "STR", Help: "Replacement string (may be empty)"},
	{Name: "--replace-all", Help: "Replace all occurrences (default: reject ambiguous multi-match)"},
	{Name: "--expected-sha256", Arg: "SHA", Help: "Compare-and-set guard"},
	{Name: "--json", Help: "Output JSON result (bytes, sha256, replacements)"},
}

func cmdVaultEdit() *cli.Command {
	return &cli.Command{
		Name:        "vault edit",
		Synopsis:    "vp vault edit <path> --old STR --new STR [--replace-all] [--expected-sha256 SHA] [--json]",
		Description: "Replace old_string with new_string in a vault-relative file. Multi-occurrence requires --replace-all.",
		Flags:       vaultEditFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault edit notes.md --old foo --new bar", Comment: "Replace a single occurrence"},
			{Cmd: "vp vault edit notes.md --old foo --new bar --replace-all", Comment: "Replace all occurrences"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultEditFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault edit: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault edit: path argument required")
				return cli.ExitUser
			}
			if !fv.IsSet("--old") {
				fmt.Fprintln(os.Stderr, "vp vault edit: --old is required")
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault edit: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Edit(root, pos[0], fv.Get("--old"), fv.Get("--new"), fv.Bool("--replace-all"), fv.Get("--expected-sha256"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault edit: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			fmt.Printf("edited %s (%d replacement(s), %d bytes, sha256 %s)\n", pos[0], res.Replacements, res.Bytes, res.Sha256)
			return cli.ExitOK
		},
	}
}

var vaultDeleteFlags = []cli.FlagDef{
	{Name: "--expected-sha256", Arg: "SHA", Help: "Compare-and-set guard"},
	{Name: "--json", Help: "Output JSON result (removed)"},
}

func cmdVaultDelete() *cli.Command {
	return &cli.Command{
		Name:        "vault delete",
		Synopsis:    "vp vault delete <path> [--expected-sha256 SHA] [--json]",
		Description: "Delete a vault-relative file (file-only). Refuses directories and '.git' paths.",
		Flags:       vaultDeleteFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault delete stale.md", Comment: "Delete a file"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultDeleteFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault delete: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault delete: path argument required")
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault delete: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Delete(root, pos[0], fv.Get("--expected-sha256"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault delete: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			fmt.Printf("removed %s\n", pos[0])
			return cli.ExitOK
		},
	}
}

var vaultMoveFlags = []cli.FlagDef{
	{Name: "--json", Help: "Output JSON result (moved)"},
}

func cmdVaultMove() *cli.Command {
	return &cli.Command{
		Name:        "vault move",
		Synopsis:    "vp vault move <from> <to> [--json]",
		Description: "Rename a vault-relative file. Refuses to overwrite an existing destination and '.git' paths.",
		Flags:       vaultMoveFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault move old.md new.md", Comment: "Rename a file"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultMoveFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault move: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) < 2 {
				fmt.Fprintln(os.Stderr, "vp vault move: <from> and <to> arguments required")
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault move: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Move(root, pos[0], pos[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault move: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			fmt.Printf("moved %s -> %s\n", pos[0], pos[1])
			return cli.ExitOK
		},
	}
}

var vaultExistsFlags = []cli.FlagDef{
	{Name: "--json", Help: "Output JSON result (exists, type)"},
}

func cmdVaultExists() *cli.Command {
	return &cli.Command{
		Name:        "vault exists",
		Synopsis:    "vp vault exists <path> [--json]",
		Description: "Check whether a vault-relative path exists. Prints exists/type.",
		Flags:       vaultExistsFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault exists notes.md", Comment: "Report existence and type"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultExistsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault exists: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault exists: path argument required")
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault exists: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Exists(root, pos[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault exists: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			if res.Exists {
				fmt.Printf("exists: %s\n", res.Type)
			} else {
				fmt.Println("does not exist")
			}
			return cli.ExitOK
		},
	}
}

var vaultSha256Flags = []cli.FlagDef{
	{Name: "--json", Help: "Output JSON result (sha256, bytes, mtime)"},
}

func cmdVaultSha256() *cli.Command {
	return &cli.Command{
		Name:        "vault sha256",
		Synopsis:    "vp vault sha256 <path> [--json]",
		Description: "Compute the SHA-256 of a vault-relative file. Prints the hex digest.",
		Flags:       vaultSha256Flags,
		Examples: []cli.Example{
			{Cmd: "vp vault sha256 notes.md", Comment: "Print the file's SHA-256"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultSha256Flags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault sha256: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp vault sha256: path argument required")
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault sha256: %v\n", err)
				return cli.ExitUser
			}
			res, err := vaultfs.Sha256(root, pos[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault sha256: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				return printVaultJSON(res)
			}
			fmt.Println(res.Sha256)
			return cli.ExitOK
		},
	}
}
