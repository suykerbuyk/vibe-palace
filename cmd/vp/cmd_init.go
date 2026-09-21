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
			req, projectResults, projectCode, proceed := initProject(fv)
			results = append(results, projectResults...)
			if !proceed {
				printInitStatus(os.Stdout, info.Version, results)
				return projectCode
			}

			res, err := onboard.Run(context.Background(), req, onboard.ScopeCLI)
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
			if exitWorthyFailure(res) != "" {
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
//
// It owns exactly those two things. It never reads, writes, prunes or reports
// on <vault>/Templates/: that tree is override-only (ADR-008) and belongs to
// `vp config sync`, which reconciles it, and to `vp commands reset` /
// `vp skills reset`, which remove a named override (keeping a backup) on the
// operator's request — the upgrade commands only report one. A first install
// onto a vault that already holds overrides therefore cannot fail on them, and
// the CLI matches the MCP vp_init tool, which never had a Templates pass.
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
	//
	// One exception: an explicit --vault-path naming a directory that does not
	// exist yet. That is the operator asking for a new vault, so it gets the
	// same vault the first-install path creates (initVault), not an error and
	// not a bare directory. Only the explicit flag triggers this. A CONFIGURED
	// vault_path that has gone missing may be a deleted or unmounted vault,
	// and init does not recreate an empty one in its place.
	if _, err := os.Stat(configPath); err == nil {
		results = append(results, check.Result{
			Name: "Global config", Status: check.Info,
			Summary: configPath + " (already exists, skipped)",
		})
		newVault, nerr := requestedNewVault(fv.Get("--vault-path"))
		if nerr != nil {
			results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: "resolve --vault-path: " + nerr.Error()})
			return results, cli.ExitUser
		}
		if newVault == "" {
			results = append(results, check.Result{Name: "Vault", Status: check.Info, Summary: "already configured"})
			return results, cli.ExitOK
		}
		cwd, err := os.Getwd()
		if err != nil {
			results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: err.Error()})
			return results, cli.ExitSystem
		}
		gitEnabled, gitNote, gerr := existingInstallGitChoice(newVault, !fv.Bool("--no-git"))
		if gerr != nil {
			results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: gerr.Error()})
			return results, cli.ExitSystem
		}
		vaultResults, code := initVault(cwd, newVault, gitEnabled)
		if gitNote != "" && len(vaultResults) > 0 {
			vaultResults[0].Details = append(vaultResults[0].Details, gitNote)
		}
		return append(results, vaultResults...), code
	}

	// Resolved exactly as initProject resolves it: expanded and absolute. A
	// relative --vault-path used to be recorded verbatim in the global config
	// (so it meant a different vault from every other cwd), and the Vault
	// reconciler's locked .gitignore top-up refuses a relative root outright —
	// vaultlock anchors its lock files at an absolute vault root.
	vaultPath := fv.Get("--vault-path")
	if vaultPath != "" {
		abs, aerr := expandAndAbsPath(vaultPath)
		if aerr != nil {
			results = append(results, check.Result{
				Name: "Global config", Status: check.Fail, Summary: "resolve --vault-path: " + aerr.Error(),
			})
			return results, cli.ExitUser
		}
		vaultPath = abs
	} else {
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
	vaultResults, code := initVault(cwd, vaultPath, gitEnabled)
	return append(results, vaultResults...), code
}

// initVault runs the Vault reconciler in init's create mode — mkdir, git init
// (when gitEnabled), the canonical .gitignore and the data-format stamp — and
// renders its one Vault row. It is the ONE vault creator `vp init` has: the
// first-install path calls it after writing the global config, and the
// existing-install path calls it for an explicit --vault-path that does not
// exist yet, so both produce the same fully formed vault.
func initVault(cwd, vaultPath string, gitEnabled bool) ([]check.Result, int) {
	var results []check.Result
	ctx := context.Background()

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
	out := classifyVaultStep(vPlan, rep)
	if out.mkdirErr != "" {
		results = append(results, check.Result{Name: "Vault", Status: check.Fail, Summary: out.mkdirErr})
		return results, cli.ExitSystem
	}
	if len(out.failures) == 0 {
		vaultRow.Details = append(vaultRow.Details, out.notes...)
	}
	if gitWanted && gitOK {
		if out.gitInitErr != "" {
			vaultRow.Details = append(vaultRow.Details, "git init failed: "+out.gitInitErr)
		} else {
			for _, a := range vPlan.Actions {
				if a.Kind == reconcile.ActionCreate && filepath.Base(a.Target) == ".git" {
					vaultRow.Details = append(vaultRow.Details, "git repository initialized")
					break
				}
			}
		}
	}
	if len(out.failures) > 0 {
		vaultRow.Status = check.Fail
		vaultRow.Details = append(vaultRow.Details, out.failures...)
		results = append(results, vaultRow)
		return results, cli.ExitSystem
	}
	results = append(results, vaultRow)
	return results, cli.ExitOK
}

// requestedNewVault returns the absolute --vault-path when the flag names a
// path that does not exist, and "" when the flag is empty or the path exists
// (in any form — what is there is onboarding's to judge, not a reason to
// create). Only ENOENT means "create"; any other stat error is returned rather
// than read as absent.
func requestedNewVault(flag string) (string, error) {
	if flag == "" {
		return "", nil
	}
	abs, err := expandAndAbsPath(flag)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return abs, nil
}

// existingInstallGitChoice decides whether a vault created on an EXISTING
// install gets a git repository. The first-install path records --no-git into
// the global config it writes, so its choice and the host policy agree by
// construction. Here the host config already exists and its git_enabled is the
// operator's policy for this machine: --no-git can only narrow it, never widen
// it. The policy is read through storage.RefuseIfGitDisabled, its one reader;
// git_enabled = false yields a vault without a repository and a note saying so,
// and an unreadable policy fails closed.
func existingInstallGitChoice(vaultPath string, wanted bool) (bool, string, error) {
	if !wanted {
		return false, "", nil
	}
	err := storage.RefuseIfGitDisabled(vaultPath, "initialise the new vault's git repository")
	switch {
	case err == nil:
		return true, "", nil
	case errors.Is(err, storage.ErrGitDisabled):
		return false, "git_enabled = false in the host config — vault created without a git repository", nil
	default:
		return false, "", err
	}
}

// vaultStepOutcome is initGlobal's reading of the Vault reconciler's Plan and
// Report.
type vaultStepOutcome struct {
	mkdirErr   string   // the vault directory itself could not be created
	gitInitErr string   // advisory: rendered as a Details line under [pass]
	failures   []string // the step failed: [FAIL] Vault and ExitSystem
	notes      []string // work that happened, for the row's Details
}

// classifyVaultStep triages the Vault step by error IDENTITY, never by message
// text. Tolerating a failure is the exception and has to be named: a
// reconcile.VaultApplyError for the git repository (a Details line under
// [pass], as it has always been) or for the data-format stamp (logged — a
// stamp hiccup must not abort vault init). Everything else fails the step: a
// .gitignore that could not be created or topped up, and any error this
// function does not recognise, however it is worded. The old triage matched
// message prefixes, so rewording "top up .gitignore" in another package would
// have silently turned a failed vault write into a log line under [pass].
//
// A .gitignore failure is a FAILED step, not hygiene: the canonical lines keep
// host-local .bak/.new sidecars, .vp-locks/ and palace/.local/ out of the vault
// repo, and every other host inherits whatever this one commits (operator
// ruling, 2581f4b: a failed vault-side step exits non-zero). It used to exit 2
// anyway, one step later, when the Templates pass re-ran the same write — that
// pass is gone, so the Vault step has to say it.
//
// A Skip in init's seeded plan is a failure too: in that mode the reconciler
// plans a Skip only for something it cannot act on — an unreadable .gitignore,
// or a vault path that is not a directory.
func classifyVaultStep(p reconcile.Plan, rep reconcile.Report) vaultStepOutcome {
	var out vaultStepOutcome
	for _, a := range p.Actions {
		if a.Kind == reconcile.ActionSkip {
			out.failures = append(out.failures, a.Summary)
		}
	}
	for _, e := range rep.Errors {
		var ve *reconcile.VaultApplyError
		if !errors.As(e, &ve) {
			out.failures = append(out.failures, e.Error())
			continue
		}
		switch ve.Artifact {
		case reconcile.VaultArtifactGit:
			out.gitInitErr = e.Error()
		case reconcile.VaultArtifactFormatStamp:
			slog.Error("vault reconciler error", "err", e)
		case reconcile.VaultArtifactDir:
			if out.mkdirErr == "" {
				out.mkdirErr = e.Error()
			}
		default:
			out.failures = append(out.failures, e.Error())
		}
	}
	out.notes = rep.Notes
	return out
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

// initProject resolves the project directory, applies the ONE gate that decides
// whether onboarding may run there at all, resolves identity, and builds the
// onboard.Request the step table is driven with.
//
// It performs NO writes of its own: cwd-project and project-scaffold are
// steps in internal/onboard, alongside the five wiring steps that used to be
// phases 2-6 below.
//
// # There used to be a SECOND gate here, and deleting it is the point
//
// `vp init` returned early the moment <dir>/.vibe-palace.toml existed, on the
// premise that a project carrying a marker is a project that has been
// onboarded. That premise was false for every project the MCP vp_init tool
// touched: it wrote a two-line marker and two task directories and nothing
// else, and the marker it wrote then told `vp init` there was nothing left to
// do. A CLI run against such a project was a permanent no-op, so the vault's
// commands/skills scaffold could never appear.
//
// The DetectSignal gate below is a different gate and stays. It answers "is
// this a project directory at all", which is a question about the directory,
// not a claim about work already done.
//
// Returns the request, the rows produced before onboarding starts, an exit
// code, and whether onboarding should run at all.
func initProject(fv *cli.FlagValues) (onboard.Request, []check.Result, int, bool) {
	var results []check.Result

	dir := "."
	if positional := fv.Args(); len(positional) > 0 {
		dir = positional[0]
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		results = append(results, check.Result{Name: "Project config", Status: check.Fail, Summary: err.Error()})
		return onboard.Request{}, results, cli.ExitSystem, false
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
		return onboard.Request{}, results, cli.ExitOK, false
	}

	name := fv.Get("--name")
	if name == "" {
		name, _ = project.DetectProject(dir)
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if err := slug.ValidateCreatable(name); err != nil {
		results = append(results, check.Result{
			Name:    "Project config",
			Status:  check.Fail,
			Summary: fmt.Sprintf("invalid project name %q: %v", name, err),
		})
		return onboard.Request{}, results, cli.ExitUser, false
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
			return onboard.Request{}, results, cli.ExitUser, false
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
	}, results, cli.ExitOK, true
}

// exitWorthyFailure names the first failed step that must make `vp init` exit
// non-zero, or "" when the run is clean enough to report success.
//
// Two classes qualify, and the boundary is deliberate (operator ruling,
// 2026-09-10):
//
//   - cwd-project, because the project marker is what `vp init` fundamentally
//     exists to write, and this has always been the exit code's meaning.
//   - any SideVault step, because a failed project-scaffold means the vault
//     was NOT written — and before this change that failure
//     rendered as a Details line under a [pass] row, so nothing surfaced it at
//     all. Promoting it to a [FAIL] row while still exiting 0 would leave the
//     table saying FAIL, the summary counting a FAIL, and `vp init && ...`
//     seeing success.
//
// Working-tree and host-global steps stay ADVISORY. A missing ~/.claude or an
// unwritable AGENTS.md is a row the operator should read, not a reason to break
// a build that only needed the vault side to land.
func exitWorthyFailure(res onboard.Result) string {
	side := make(map[string]onboard.Side, len(onboard.Steps()))
	for _, st := range onboard.Steps() {
		side[st.Name] = st.Side
	}
	for _, oc := range res.Outcomes {
		if oc.Status != check.Fail {
			continue
		}
		if oc.Step == "cwd-project" || side[oc.Step] == onboard.SideVault {
			return oc.Step
		}
	}
	return ""
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
