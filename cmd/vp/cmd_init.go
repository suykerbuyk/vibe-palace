// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/onboard"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
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
		Description: "Initialize vibe-palace. The positional argument is the project directory (defaults to cwd); vault location is set via --vault-path. Creates global config and vault if needed, then initializes the project.",
		Flags:       initFlags,
		Examples: []cli.Example{
			{Cmd: "vp init", Comment: "Initialize global config (if needed) and project in current directory"},
			{Cmd: "vp init ~/code/myapp --name myapp --domain work"},
			{Cmd: "vp init --vault-path ~/my-vault", Comment: "Use a custom vault location"},
			{Cmd: "vp init ./myapp --vault-path ~/my-vault", Comment: "Custom project directory AND custom vault"},
			{Cmd: "vp init --no-git", Comment: "Initialize without git version tracking"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(initFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				return cli.ExitUser
			}

			// Validate any user-supplied positional *before* any filesystem
			// writes. A nonexistent positional is almost always a user
			// confusing the positional (project dir) with --vault-path,
			// and continuing would silently create the default vault
			// under $HOME.
			if code, msg := validatePositionalProjectPath(fv.Args()); code != cli.ExitOK {
				fmt.Fprintln(os.Stderr, msg)
				return code
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

			// --- Phases 2-6: project onboarding ---
			// One call, not five: internal/onboard owns the step table, the
			// order, the prerequisites and — the reason it exists — which
			// SURFACE each step writes, so a non-CLI caller can be denied a
			// step out loud instead of quietly doing less.
			req, preOmitted, projectResults, projectCode, proceed := initProject(fv)
			results = append(results, projectResults...)
			if !proceed {
				printInitStatus(os.Stdout, info.Version, results)
				return projectCode
			}

			res, err := onboard.Run(context.Background(), req, onboard.ScopeCLI, preOmitted...)
			if err != nil {
				// An accounting failure, never a step failure. It means a step
				// fell through every branch and produced no row at all, which
				// is the one outcome a status table cannot show.
				results = append(results, check.Result{
					Name: "Onboarding", Status: check.Fail, Summary: err.Error(),
				})
				printInitStatus(os.Stdout, info.Version, results)
				return cli.ExitSystem
			}
			results = append(results, onboard.Rows(res)...)
			if stepFailed(res, "cwd-project") {
				projectCode = cli.ExitSystem
			}

			printInitStatus(os.Stdout, info.Version, results)
			return projectCode
		},
	}
}

// validatePositionalProjectPath checks that a user-supplied positional
// argument to `vp init` points at an existing directory. It returns
// (ExitOK, "") when the arg is absent or the path exists. A nonexistent
// path returns ExitUser with a hint pointing the user at --vault-path —
// this is the single most common source of confusion (user types the
// vault path as the positional instead of via the flag). Other stat
// errors (EACCES, symlink loops) return ExitSystem with the raw error,
// no hint.
func validatePositionalProjectPath(args []string) (int, string) {
	if len(args) == 0 {
		return cli.ExitOK, ""
	}
	path := args[0]
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cli.ExitUser, fmt.Sprintf(
				"vp init: path %q does not exist — did you mean --vault-path? (the positional argument is the project directory; --vault-path sets the vault location)",
				path,
			)
		}
		return cli.ExitSystem, fmt.Sprintf("vp init: %v", err)
	}
	return cli.ExitOK, ""
}

// initGlobal creates the global config and vault directory if they don't
// exist, returning a result row for each logical step plus an exit code.
// Delegates the underlying writes to the GlobalConfig and Vault reconcilers.
func initGlobal(fv *cli.FlagValues) ([]check.Result, int) {
	var results []check.Result

	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		results = append(results, check.Result{
			Name: "Global config", Status: check.Fail, Summary: err.Error(),
		})
		results = append(results, check.Result{Name: "Vault", Status: check.Skip, Summary: "skipped — global config error"})
		return results, cli.ExitSystem
	}

	// When the global config already exists, treat it as authoritative —
	// skip the vault re-check and emit two [info] rows. Matches prior init
	// behavior so users see the same "already configured" message.
	if _, err := os.Stat(configPath); err == nil {
		results = append(results, check.Result{
			Name: "Global config", Status: check.Info,
			Summary: configPath + " (already exists, skipped)",
		})
		results = append(results, check.Result{Name: "Vault", Status: check.Info, Summary: "already configured"})
		return results, cli.ExitOK
	}

	vaultPath := fv.Get("--vault-path")
	if vaultPath == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			results = append(results, check.Result{
				Name: "Global config", Status: check.Fail, Summary: herr.Error(),
			})
			return results, cli.ExitSystem
		}
		vaultPath = filepath.Join(home, "vibe-palace-vault")
	}
	gitEnabled := !fv.Bool("--no-git")

	cwd, err := os.Getwd()
	if err != nil {
		results = append(results, check.Result{Name: "Global config", Status: check.Fail, Summary: err.Error()})
		return results, cli.ExitSystem
	}

	ctx := context.Background()

	// --- GlobalConfig reconciler ---
	gc := reconcile.NewGlobalConfig(cwd, reconcile.GlobalSeed{
		VaultPath: vaultPath, GitEnabled: gitEnabled,
	}.WithCreate())
	gcPlan, err := gc.Plan(ctx)
	if err != nil {
		results = append(results, check.Result{Name: "Global config", Status: check.Fail, Summary: err.Error()})
		return results, cli.ExitSystem
	}
	if rep, err := gc.Apply(ctx, gcPlan); err != nil || len(rep.Errors) > 0 {
		msg := errOrFirst(err, rep.Errors)
		results = append(results, check.Result{Name: "Global config", Status: check.Fail, Summary: msg})
		return results, cli.ExitSystem
	}
	results = append(results, check.Result{Name: "Global config", Status: check.Pass, Summary: configPath})

	// --- Vault reconciler ---
	vaultRow := check.Result{Name: "Vault", Status: check.Pass, Summary: vaultPath}

	gitWanted := gitEnabled
	gitOK := !gitWanted || storage.GitAvailable()
	if gitWanted && !gitOK {
		vaultRow.Details = append(vaultRow.Details, "git not found in PATH — vault version tracking disabled")
	}
	v := reconcile.NewVault(cwd, reconcile.VaultSeed{
		VaultPath: vaultPath, GitEnabled: gitWanted && gitOK,
	}.WithCreate())
	vPlan, err := v.Plan(ctx)
	if err != nil {
		results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: err.Error()})
		return results, cli.ExitSystem
	}
	rep, err := v.Apply(ctx, vPlan)
	if err != nil {
		results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: fmt.Sprintf("create vault: %v", err)})
		return results, cli.ExitSystem
	}
	gitInitErr := ""
	for _, e := range rep.Errors {
		es := e.Error()
		switch {
		case strings.HasPrefix(es, "git init"):
			gitInitErr = es
		case strings.HasPrefix(es, "mkdir vault"):
			results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: es})
			return results, cli.ExitSystem
		default:
			// .gitignore failures are non-fatal — log only, mirroring the
			// pre-reconciler init path.
			slog.Error("vault reconciler error", "err", es)
		}
	}
	if gitWanted && gitOK {
		if gitInitErr != "" {
			vaultRow.Details = append(vaultRow.Details, "git init failed: "+gitInitErr)
		} else {
			for _, a := range vPlan.Actions {
				if a.Kind == reconcile.ActionCreate && filepath.Base(a.Target) == ".git" {
					vaultRow.Details = append(vaultRow.Details, "git repository initialized")
					break
				}
			}
		}
	}
	results = append(results, vaultRow)

	// --- TemplateTree (materialize) ---
	// Phase 3: after the vault directory exists, copy every embedded
	// template resource into <vault>/Templates/ and seed the lock file.
	// AutoAccept=true so the fresh-vault Create actions run unattended.
	tt := reconcile.NewTemplateTree(vaultPath, "Templates", reconcile.TemplateTreeSeed{
		Mode:       reconcile.TemplateModeMaterialize,
		AutoAccept: true,
	})
	ttPlan, err := tt.Plan(ctx)
	if err != nil {
		results = append(results, check.Result{
			Name: "Templates", Status: check.Fail, Summary: err.Error(),
		})
		return results, cli.ExitSystem
	}
	ttRep, err := tt.Apply(ctx, ttPlan)
	if err != nil {
		results = append(results, check.Result{
			Name: "Templates", Status: check.Fail, Summary: err.Error(),
		})
		return results, cli.ExitSystem
	}
	if len(ttRep.Errors) > 0 {
		// Log every error so a multi-failure Apply leaves a complete
		// forensic trail; the Fail row only surfaces the first.
		for _, e := range ttRep.Errors {
			slog.Error("templates materialize apply error", "err", e)
		}
		results = append(results, check.Result{
			Name: "Templates", Status: check.Fail, Summary: ttRep.Errors[0].Error(),
		})
		return results, cli.ExitSystem
	}
	results = append(results, check.Result{
		Name:    "Templates",
		Status:  check.Pass,
		Summary: fmt.Sprintf("%d templates materialized", ttRep.Created),
	})
	return results, cli.ExitOK
}

// errOrFirst returns err.Error() if err is non-nil, else the first error
// from errs. Used to surface a single message from a reconciler Apply
// result that may report failures via either return value.
func errOrFirst(err error, errs []error) string {
	if err != nil {
		return err.Error()
	}
	if len(errs) > 0 {
		return errs[0].Error()
	}
	return ""
}

// initProject resolves the project directory, applies the two gates that
// decide whether onboarding may run there at all, resolves identity, and
// builds the onboard.Request the step table is driven with.
//
// It performs NO writes of its own any more: cwd-project, vault-project and
// project-scaffold are steps in internal/onboard, alongside the five wiring
// steps that used to be phases 2-6 below.
//
// Returns the request, any Omissions the CALLER has already accounted for, the
// rows produced before onboarding starts, an exit code, and whether onboarding
// should run at all.
func initProject(fv *cli.FlagValues) (onboard.Request, []onboard.Omission, []check.Result, int, bool) {
	var results []check.Result

	dir := "."
	if positional := fv.Args(); len(positional) > 0 {
		dir = positional[0]
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		results = append(results, check.Result{Name: "Project config", Status: check.Fail, Summary: err.Error()})
		return onboard.Request{}, nil, results, cli.ExitSystem, false
	}

	// The one vault resolver every step shares. It walks up from the PROJECT
	// directory rather than the process cwd — see the comment on the shim step
	// in internal/onboard/steps.go.
	openVault := func() (*storage.Vault, error) { return OpenProjectVaultAt(dir) }

	signal := project.DetectSignal(dir)
	if signal == project.SignalNone {
		results = append(results, check.Result{
			Name:    "Project config",
			Status:  check.Skip,
			Summary: "not a project directory (no .git, .vibe-palace.toml, or known manifest)",
		})
		return onboard.Request{}, nil, results, cli.ExitOK, false
	}

	configPath := filepath.Join(dir, project.ConfigFileName)
	if _, err := os.Stat(configPath); err == nil {
		// Config already exists — the project is ready for agent wiring.
		// Don't re-touch vault-project artifacts here; that's `vp config
		// sync`'s job and the existing init contract treats this branch as
		// a no-op.
		//
		// Expressed as Omissions rather than as a bare early return so the
		// three steps stay ACCOUNTED FOR: onboard.Run refuses a run in which
		// a step is neither an outcome nor an omission, and the operator gets
		// told where to go instead of silently getting a shorter table.
		name, _ := project.DetectProject(dir)
		req := onboard.Request{OpenVault: openVault, Slug: name, ProjectDir: dir}
		return req, alreadyConfigured(dir, configPath), results, cli.ExitOK, true
	}

	name := fv.Get("--name")
	if name == "" {
		name, _ = project.DetectProject(dir)
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if err := slug.Validate(name); err != nil {
		results = append(results, check.Result{
			Name:    "Project config",
			Status:  check.Fail,
			Summary: fmt.Sprintf("invalid project name %q: %v", name, err),
		})
		return onboard.Request{}, nil, results, cli.ExitUser, false
	}

	var vaultPathOverride string
	if vp := fv.Get("--vault-path"); vp != "" {
		// expandAndAbsPath stays CLI-side: --vault-path is a flag, and four
		// other cmd/vp callers use it.
		expanded, err := expandAndAbsPath(vp)
		if err != nil {
			results = append(results, check.Result{
				Name:    "Project config",
				Status:  check.Fail,
				Summary: "resolve --vault-path: " + err.Error(),
			})
			return onboard.Request{}, nil, results, cli.ExitUser, false
		}
		vaultPathOverride = expanded
	}

	var tagsList []string
	if t := fv.Get("--tags"); t != "" {
		for tag := range strings.SplitSeq(t, ",") {
			if trimmed := strings.TrimSpace(tag); trimmed != "" {
				tagsList = append(tagsList, trimmed)
			}
		}
	}

	return onboard.Request{
		OpenVault:         openVault,
		Slug:              name,
		ProjectDir:        dir,
		Domain:            fv.Get("--domain"),
		Tags:              tagsList,
		VaultPathOverride: vaultPathOverride,
	}, nil, results, cli.ExitOK, true
}

// alreadyConfigured is the marker gate's vehicle: the three project-config
// steps that `vp init` has never re-run once <dir>/.vibe-palace.toml exists.
//
// Every Remedy names the verbatim command, the host it has to run on, and the
// artifact in question — an Omission whose remedy is "run vp init" tells an
// operator on another machine nothing at all.
func alreadyConfigured(dir, configPath string) []onboard.Omission {
	cmd := "run `vp config sync --tier project --cwd " + dir + "`"
	host := " on the machine that owns " + dir
	const reason = "skipped — the project is already configured; `vp init` treats a re-init as a no-op here"
	return []onboard.Omission{
		{
			Step:   "cwd-project",
			Reason: configPath + " (already exists, skipped)",
			Remedy: cmd + host + " — it reconciles the existing " + configPath + " against the current schema instead of rewriting it.",
		},
		{
			Step:   "vault-project",
			Reason: reason,
			Remedy: cmd + host + " — it writes Projects/<slug>/config.toml in the vault.",
		},
		{
			Step:   "project-scaffold",
			Reason: reason,
			Remedy: cmd + host + " — it creates Projects/<slug>/{commands,skills}/ in the vault.",
		},
	}
}

// stepFailed reports whether the named onboarding step produced a Fail row.
// `vp init`'s exit code has always been the cwd-project write's exit code, and
// moving the write into a step must not change that.
func stepFailed(res onboard.Result, step string) bool {
	for _, oc := range res.Outcomes {
		if oc.Step == step && oc.Status == check.Fail {
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
