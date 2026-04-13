// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var initFlags = []cli.FlagDef{
	{Name: "--name", Short: "-n", Arg: "NAME", Help: "Project name (default: auto-detect)"},
	{Name: "--domain", Short: "-d", Arg: "DOMAIN", Help: "Domain (e.g. work, personal, opensource)"},
	{Name: "--tags", Short: "-t", Arg: "TAGS", Help: "Comma-separated tags"},
	{Name: "--vault-path", Short: "-V", Arg: "PATH", Help: "Vault directory (default: ~/vibe-palace-vault)"},
	{Name: "--no-git", Help: "Disable git version tracking for the vault"},
}

func cmdInit(info cli.BuildInfo) *cli.Command {
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

			var results []check.Result

			// --- Phase 1: Global init (config + vault directory) ---
			globalResults, globalCode := initGlobal(fv)
			results = append(results, globalResults...)
			if globalCode != cli.ExitOK {
				// Global init failed; agent wiring is meaningless without
				// a working install, so surface a single skip row and bail.
				results = append(results, check.Result{
					Name:    "Agent wiring",
					Status:  check.Skip,
					Summary: "skipped — global init failed",
				})
				printInitStatus(os.Stdout, info.Version, results)
				return globalCode
			}

			// --- Phase 2: Project init (if appropriate) ---
			projectDir, projectReady, projectResults, projectCode := initProject(fv)
			results = append(results, projectResults...)

			// --- Phase 2: Agent-file wiring ---
			results = append(results, initAgentWiring(projectDir, projectReady)...)

			// --- Phase 3: Slash-command shim emission ---
			results = append(results, initShimWiring(projectDir, projectReady)...)

			printInitStatus(os.Stdout, info.Version, results)
			return projectCode
		},
	}
}

// initGlobal creates the global config and vault directory if they don't
// exist, returning a result row for each logical step plus an exit code.
func initGlobal(fv *cli.FlagValues) ([]check.Result, int) {
	var results []check.Result

	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		results = append(results, check.Result{
			Name:    "Global config",
			Status:  check.Fail,
			Summary: err.Error(),
		})
		results = append(results, check.Result{Name: "Vault", Status: check.Skip, Summary: "skipped — global config error"})
		return results, cli.ExitSystem
	}

	if _, err := os.Stat(configPath); err == nil {
		results = append(results, check.Result{
			Name:    "Global config",
			Status:  check.Info,
			Summary: configPath + " (already exists, skipped)",
		})
		// Vault existence is not re-checked here — the existing global
		// config is authoritative. Surface a single [info] row.
		results = append(results, check.Result{Name: "Vault", Status: check.Info, Summary: "already configured"})
		return results, cli.ExitOK
	}

	vaultPath := fv.Get("--vault-path")
	if vaultPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			results = append(results, check.Result{
				Name:    "Global config",
				Status:  check.Fail,
				Summary: err.Error(),
			})
			return results, cli.ExitSystem
		}
		vaultPath = filepath.Join(home, "vibe-palace-vault")
	}

	gitEnabled := !fv.Bool("--no-git")

	writtenPath, err := storage.WriteGlobalConfig(vaultPath, gitEnabled)
	if err != nil {
		results = append(results, check.Result{Name: "Global config", Status: check.Fail, Summary: err.Error()})
		return results, cli.ExitSystem
	}
	results = append(results, check.Result{Name: "Global config", Status: check.Pass, Summary: writtenPath})

	vaultRow := check.Result{Name: "Vault", Status: check.Pass, Summary: vaultPath}
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: fmt.Sprintf("create vault: %v", err)})
		return results, cli.ExitSystem
	}

	if gitEnabled {
		switch {
		case !storage.GitAvailable():
			vaultRow.Details = append(vaultRow.Details, "git not found in PATH — vault version tracking disabled")
		case !storage.GitIsRepo(vaultPath):
			if err := storage.GitInit(vaultPath); err != nil {
				vaultRow.Details = append(vaultRow.Details, "git init failed: "+err.Error())
			} else {
				if err := storage.WriteVaultGitignore(vaultPath); err != nil {
					slog.Error("write vault .gitignore", "path", vaultPath, "err", err)
				}
				vaultRow.Details = append(vaultRow.Details, "git repository initialized")
			}
		}
	}
	results = append(results, vaultRow)
	return results, cli.ExitOK
}

// initProject creates a .vibe-palace.toml in the target directory if project
// signals are present. Returns the resolved project dir, whether the project
// is ready for downstream wiring (config present or just created), one or
// more status rows, and an exit code.
func initProject(fv *cli.FlagValues) (string, bool, []check.Result, int) {
	var results []check.Result

	dir := "."
	if positional := fv.Args(); len(positional) > 0 {
		dir = positional[0]
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		results = append(results, check.Result{Name: "Project config", Status: check.Fail, Summary: err.Error()})
		return dir, false, results, cli.ExitSystem
	}

	signal := project.DetectSignal(dir)
	if signal == project.SignalNone {
		results = append(results, check.Result{
			Name:    "Project config",
			Status:  check.Skip,
			Summary: "not a project directory (no .git, .vibe-palace.toml, or known manifest)",
		})
		return dir, false, results, cli.ExitOK
	}

	configPath := filepath.Join(dir, project.ConfigFileName)
	if _, err := os.Stat(configPath); err == nil {
		results = append(results, check.Result{
			Name:    "Project config",
			Status:  check.Info,
			Summary: configPath + " (already exists, skipped)",
		})
		// Config already exists — the project is ready for agent wiring.
		return dir, true, results, cli.ExitOK
	}

	name := fv.Get("--name")
	if name == "" {
		name, _ = project.DetectProject(dir)
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if err := storage.ValidateSlug(name); err != nil {
		results = append(results, check.Result{
			Name:    "Project config",
			Status:  check.Fail,
			Summary: fmt.Sprintf("invalid project name %q: %v", name, err),
		})
		return dir, false, results, cli.ExitUser
	}

	var vaultPathOverride string
	if vp := fv.Get("--vault-path"); vp != "" {
		expanded, err := expandAndAbsPath(vp)
		if err != nil {
			results = append(results, check.Result{
				Name:    "Project config",
				Status:  check.Fail,
				Summary: "resolve --vault-path: " + err.Error(),
			})
			return dir, false, results, cli.ExitUser
		}
		vaultPathOverride = expanded
	}

	var tagsList []string
	if t := fv.Get("--tags"); t != "" {
		for _, tag := range strings.Split(t, ",") {
			if trimmed := strings.TrimSpace(tag); trimmed != "" {
				tagsList = append(tagsList, trimmed)
			}
		}
	}

	if _, err := storage.WriteCwdProjectConfig(dir, name, fv.Get("--domain"), tagsList, vaultPathOverride); err != nil {
		results = append(results, check.Result{Name: "Project config", Status: check.Fail, Summary: err.Error()})
		return dir, false, results, cli.ExitSystem
	}

	row := check.Result{
		Name:    "Project config",
		Status:  check.Pass,
		Summary: fmt.Sprintf("%s (%s, %s detected)", configPath, name, signal),
	}

	if vault, err := storage.OpenVaultFromCwd(dir); err == nil {
		if tasksDir, err := vault.TasksDir(name); err == nil {
			if mkErr := os.MkdirAll(filepath.Join(tasksDir, "done"), 0o755); mkErr != nil {
				slog.Error("create tasks/done dir", "path", tasksDir, "err", mkErr)
			}
			if mkErr := os.MkdirAll(filepath.Join(tasksDir, "cancelled"), 0o755); mkErr != nil {
				slog.Error("create tasks/cancelled dir", "path", tasksDir, "err", mkErr)
			}
		}
		if _, _, wErr := vault.WriteVaultProjectConfig(name); wErr != nil {
			slog.Error("write vault-project config", "project", name, "err", wErr)
			row.Details = append(row.Details, "vault-project config write failed — see logs")
		}
	}

	results = append(results, row)
	return dir, true, results, cli.ExitOK
}

// initAgentWiring detects agent instruction files in projectRoot, writes the
// vibe-palace managed block into each, and returns one status row per
// (canonical) target plus one row per deliberate skip. When the project
// itself is not ready (no config was written and none pre-existed), a single
// skip row directs the user to set up the project first.
func initAgentWiring(projectRoot string, projectReady bool) []check.Result {
	if !projectReady {
		return []check.Result{{
			Name:    "Agent wiring",
			Status:  check.Skip,
			Summary: "skipped — no project config; run `vp init` inside a project directory",
		}}
	}

	targets, skips := agentfile.Detect(projectRoot)
	var rows []check.Result

	if len(targets) == 0 {
		rows = append(rows, check.Result{
			Name:    "Agent wiring",
			Status:  check.Skip,
			Summary: "no agent file found; create CLAUDE.md/AGENTS.md and re-run `vp init`",
		})
	}

	var driftFiles []string
	for _, t := range targets {
		display := t.DisplayName
		if len(t.Aliases) > 0 {
			display += " (→ " + strings.Join(t.Aliases, ", ") + ")"
		}
		// Detect legacy content (non-managed-block bytes) before we wire
		// so the Init summary can suggest `vp absorb` when migration is
		// warranted.
		if data, err := os.ReadFile(t.Path); err == nil && hasLegacyContent(data) {
			driftFiles = append(driftFiles, t.DisplayName)
		}
		res, err := agentfile.Wire(t)
		if err != nil {
			rows = append(rows, check.Result{
				Name:    "Agent wiring",
				Status:  check.Fail,
				Summary: display + ": " + err.Error(),
			})
			continue
		}
		switch res.Kind {
		case agentfile.Added:
			rows = append(rows, check.Result{
				Name:    "Agent wiring",
				Status:  check.Pass,
				Summary: display + " — block added",
			})
		case agentfile.Updated:
			summary := display + " — block updated"
			if res.PrevSha != "" {
				summary += " (was " + res.PrevSha + ")"
			}
			rows = append(rows, check.Result{
				Name:    "Agent wiring",
				Status:  check.Pass,
				Summary: summary,
			})
		case agentfile.Unchanged:
			rows = append(rows, check.Result{
				Name:    "Agent wiring",
				Status:  check.Info,
				Summary: display + " — block unchanged",
			})
		}
	}

	for _, s := range skips {
		rows = append(rows, check.Result{
			Name:    "Agent wiring",
			Status:  check.Skip,
			Summary: s.DisplayName + " — " + s.Reason,
		})
	}
	if len(driftFiles) > 0 {
		rows = append(rows, check.Result{
			Name:    "Agent wiring",
			Status:  check.Info,
			Summary: "legacy content detected in " + strings.Join(driftFiles, ", "),
			Details: []string{"run `vp absorb` to migrate existing content into the vault"},
		})
	}
	return rows
}

// initShimWiring emits one .claude/commands/vpc-<name>.md shim per
// vibe-palace command into projectRoot, surfacing the command set in
// Claude Code's `/` slash menu. Additive-by-default: stale shims (commands
// that no longer exist) are reported but not deleted — `vp commands
// upgrade` handles removal with explicit user consent.
func initShimWiring(projectRoot string, projectReady bool) []check.Result {
	if !projectReady {
		return nil // agent-wiring already surfaced the skip row.
	}

	vault, err := openProjectVault()
	if err != nil {
		return []check.Result{{
			Name:    "Slash-command shims",
			Status:  check.Skip,
			Summary: "skipped — open vault: " + err.Error(),
		}}
	}
	resolver := vpctx.NewResolver(vault.Root)

	summaries, err := commands.List(resolver, "command", "", "", "", 60)
	if err != nil {
		return []check.Result{{
			Name:    "Slash-command shims",
			Status:  check.Fail,
			Summary: "list commands: " + err.Error(),
		}}
	}
	if len(summaries) == 0 {
		return []check.Result{{
			Name:    "Slash-command shims",
			Status:  check.Skip,
			Summary: "no commands available to emit",
		}}
	}

	shimDir := filepath.Join(projectRoot, shims.ShimDir)
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		// Treat permission/IO errors as warn-and-continue per the plan
		// doc — `vp init` should not fail because the user deliberately
		// kept .claude/ read-only.
		return []check.Result{{
			Name:    "Slash-command shims",
			Status:  check.Skip,
			Summary: "skipped — create " + shims.ShimDir + ": " + err.Error(),
		}}
	}

	plan, err := shims.Plan(summaries, projectRoot)
	if err != nil {
		return []check.Result{{
			Name:    "Slash-command shims",
			Status:  check.Fail,
			Summary: "plan shims: " + err.Error(),
		}}
	}

	rep, err := shims.Apply(plan, shims.ApplyOptions{AllowStaleRemoval: false})
	if err != nil {
		return []check.Result{{
			Name:    "Slash-command shims",
			Status:  check.Fail,
			Summary: "apply shims: " + err.Error(),
		}}
	}

	summary := fmt.Sprintf(
		"added %d, updated %d, unchanged %d, stale %d, custom %d",
		rep.Added, rep.Updated, rep.Unchanged, rep.Stale, rep.Custom,
	)
	status := check.Info
	if rep.Added > 0 || rep.Updated > 0 {
		status = check.Pass
	}
	row := check.Result{
		Name:    "Slash-command shims",
		Status:  status,
		Summary: summary,
	}
	if rep.Stale > 0 {
		row.Details = append(row.Details,
			"stale shims left in place — run `vp commands upgrade` to review")
	}
	return []check.Result{row}
}

// hasLegacyContent reports whether data contains any non-whitespace bytes
// outside the managed vibe-palace block. Used by init to suggest `vp
// absorb`.
func hasLegacyContent(data []byte) bool {
	start, end := agentfile.FindBlock(data)
	var outside []byte
	if start < 0 {
		outside = data
	} else {
		outside = append([]byte{}, data[:start]...)
		if end <= len(data) {
			outside = append(outside, data[end:]...)
		}
	}
	for _, b := range outside {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
	}
	return false
}

// printInitStatus renders the end-of-run status table. Mirrors check.Print's
// vocabulary but carries the init-specific header and summary line.
func printInitStatus(w *os.File, version string, results []check.Result) {
	fmt.Fprintf(w, "\nvp init — vibe-palace %s\n\n", version)
	check.PrintRows(w, results)

	var pass, fail, skip, info int
	for _, r := range results {
		switch r.Status {
		case check.Pass:
			pass++
		case check.Fail:
			fail++
		case check.Skip:
			skip++
		case check.Info:
			info++
		}
	}
	fmt.Fprintln(w)
	parts := []string{}
	ok := pass + info
	if ok > 0 {
		parts = append(parts, fmt.Sprintf("%d ok", ok))
	}
	if skip > 0 {
		parts = append(parts, fmt.Sprintf("%d skip", skip))
	}
	if fail > 0 {
		parts = append(parts, fmt.Sprintf("%d FAIL", fail))
	}
	fmt.Fprintf(w, "Summary: %s. Re-run `vp init` anytime — it is idempotent.\n", strings.Join(parts, ", "))
}

// expandAndAbsPath expands a leading tilde and resolves to an absolute path.
func expandAndAbsPath(p string) (string, error) {
	if len(p) > 0 && p[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			p = home
		} else if len(p) > 1 && p[1] == '/' {
			p = filepath.Join(home, p[2:])
		}
	}
	return filepath.Abs(p)
}
