// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var initFlags = []cli.FlagDef{
	{Name: "--name", Short: "-n", Arg: "NAME", Help: "Project name (default: auto-detect)"},
	{Name: "--domain", Short: "-d", Arg: "DOMAIN", Help: "Domain (e.g. work, personal, opensource)"},
	{Name: "--tags", Short: "-t", Arg: "TAGS", Help: "Comma-separated tags"},
	{Name: "--vault-path", Short: "-V", Arg: "PATH", Help: "Vault directory (default: ~/vibe-palace-vault)"},
	{Name: "--no-git", Help: "Disable git version tracking for the vault"},
}

func cmdInit() *cli.Command {
	return &cli.Command{
		Name:        "init",
		Synopsis:    "vp init [path] [flags]",
		Description: "Initialize vibe-palace. Creates global config and vault if needed, then initializes a project in the current or given directory.",
		Flags:       initFlags,
		Examples: []cli.Example{
			{Cmd: "vp init", Comment: "Initialize global config (if needed) and project in current directory"},
			{Cmd: "vp init ~/code/myapp --name myapp --domain work"},
			{Cmd: "vp init --vault-path ~/my-vault", Comment: "Use a custom vault location"},
			{Cmd: "vp init --no-git", Comment: "Initialize without git version tracking"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(initFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				return cli.ExitUser
			}

			// --- Phase 1: Global init (config + vault directory) ---
			if code := initGlobal(fv); code != cli.ExitOK {
				return code
			}

			// --- Phase 2: Project init (if appropriate) ---
			return initProject(fv)
		},
	}
}

// initGlobal creates the global config and vault directory if they don't exist.
func initGlobal(fv *cli.FlagValues) int {
	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
		return cli.ExitSystem
	}

	if _, err := os.Stat(configPath); err == nil {
		fmt.Fprintf(os.Stderr, "Global config exists at %s, skipping.\n", configPath)
		return cli.ExitOK
	}

	// Determine vault path.
	vaultPath := fv.Get("--vault-path")
	if vaultPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
			return cli.ExitSystem
		}
		vaultPath = filepath.Join(home, "vibe-palace-vault")
	}

	gitEnabled := !fv.Bool("--no-git")

	// Write global config.
	writtenPath, err := storage.WriteGlobalConfig(vaultPath, gitEnabled)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
		return cli.ExitSystem
	}
	fmt.Fprintf(os.Stderr, "Created config at %s\n", writtenPath)

	// Create vault directory.
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "vp init: create vault: %v\n", err)
		return cli.ExitSystem
	}
	fmt.Fprintf(os.Stderr, "Created vault at %s\n", vaultPath)

	// Initialize git repo in vault if git is enabled.
	if gitEnabled {
		if !storage.GitAvailable() {
			fmt.Fprintln(os.Stderr, "Warning: git not found in PATH. Install git for vault version tracking.")
		} else if !storage.GitIsRepo(vaultPath) {
			if err := storage.GitInit(vaultPath); err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				// Non-fatal — vault works without git.
			} else {
				if err := storage.WriteVaultGitignore(vaultPath); err != nil {
					slog.Error("write vault .gitignore", "path", vaultPath, "err", err)
				}
				fmt.Fprintln(os.Stderr, "Initialized git repository in vault.")
			}
		}
	}

	return cli.ExitOK
}

// initProject creates a .vibe-palace.toml in the target directory if project
// signals are present. Skips if cwd is $HOME or no project signals detected.
func initProject(fv *cli.FlagValues) int {
	dir := "."
	if positional := fv.Args(); len(positional) > 0 {
		dir = positional[0]
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
		return cli.ExitSystem
	}

	// Skip project init if we're in $HOME or no project signals.
	if !hasProjectSignals(dir) {
		fmt.Fprintln(os.Stderr, "No project detected. Run 'vp init' inside a project directory to set up a project.")
		return cli.ExitOK
	}

	// Check for existing project config.
	configPath := filepath.Join(dir, project.ConfigFileName)
	if _, err := os.Stat(configPath); err == nil {
		fmt.Fprintf(os.Stderr, "Project already initialized: %s\n", configPath)
		return cli.ExitOK
	}

	// Determine project name.
	name := fv.Get("--name")
	if name == "" {
		name, _ = project.DetectProject(dir)
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if err := storage.ValidateSlug(name); err != nil {
		fmt.Fprintf(os.Stderr, "vp init: invalid project name %q: %v\n", name, err)
		return cli.ExitUser
	}

	// Write .vibe-palace.toml.
	content := fmt.Sprintf("[project]\nname = %q\n", name)
	if d := fv.Get("--domain"); d != "" {
		content += fmt.Sprintf("domain = %q\n", d)
	}
	if t := fv.Get("--tags"); t != "" {
		content += fmt.Sprintf("tags = [%q]\n", t)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
		return cli.ExitSystem
	}
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
		return cli.ExitSystem
	}

	// Create vault task directories if vault is available.
	vault, err := storage.OpenVault("")
	if err == nil {
		tasksDir, err := vault.TasksDir(name)
		if err == nil {
			if mkErr := os.MkdirAll(filepath.Join(tasksDir, "done"), 0o755); mkErr != nil {
				slog.Error("create tasks/done dir", "path", tasksDir, "err", mkErr)
			}
			if mkErr := os.MkdirAll(filepath.Join(tasksDir, "cancelled"), 0o755); mkErr != nil {
				slog.Error("create tasks/cancelled dir", "path", tasksDir, "err", mkErr)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "Initialized project %q in %s\n", name, dir)
	return cli.ExitOK
}

// hasProjectSignals returns true if the directory looks like a project:
// has a .git directory or is not the user's home directory.
func hasProjectSignals(dir string) bool {
	home, err := os.UserHomeDir()
	if err == nil && dir == home {
		return false
	}

	// Check for .git directory.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return true
	}

	// Check for existing .vibe-palace.toml anywhere in tree.
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, project.ConfigFileName)); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}

	// Not home, but no git or config — still a valid project dir.
	// The user explicitly navigated here and ran init.
	return true
}
