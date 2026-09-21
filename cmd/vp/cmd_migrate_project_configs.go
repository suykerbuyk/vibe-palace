// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// ONE-SHOT. `vp migrate project-configs` retires the per-project vault configs,
// Projects/<slug>/config.toml, which vp no longer reads. Delete this file with
// internal/storage/project_config_retirement.go, both tests, its registration
// and wantMutating row, and the planProjectConfigRetirement entry in
// internal/sourceaudit's plannerFuncs.
//
// # Plan-first, one planner
//
// The bare command is the report and deletes nothing; --apply prints the same
// report and then executes it. Both call planProjectConfigRetirement, which
// reads and writes nothing else, so an override committed between a dry run and
// an apply is in the apply's transcript: the apply re-plans, it never replays.
//
// # What the transcript is for
//
// Every file's palace.scoring is rendered by the host-local writer's own
// renderer, so an operator who wants a project's scoring on a host pastes the
// block into that host's <config dir>/vibe-palace/projects/<slug>.toml. Every
// other section a file carries has no per-project tier any more and is named as
// dropped.
//
// # Only tracked files are retired
//
// The census line — `untracked survivors: <names|none> (N on disk, M tracked)`
// — prints on both the dry run and the apply, before anything that can refuse.
// --apply refuses while any config is untracked: an untracked file has no copy
// in HEAD, so a failed run could not restore it, and writing it back directly
// would sidestep vaultfs's refusal of that very path. The operator commits it
// or removes it with `vp vault delete`, and re-runs.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

var migrateProjectConfigsFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and retire from (default: the configured vault_path)"},
	{Name: "--apply", Help: "DELETE the tracked per-project configs and commit the removal. Without this the command only reports."},
}

func cmdMigrateProjectConfigs() *cli.Command {
	return &cli.Command{
		Name:     "migrate project-configs",
		Synopsis: "vp migrate project-configs [--vault PATH] [--apply]",
		Description: "The one-time retirement of the per-project vault configs (Projects/<slug>/config.toml), " +
			"which vp no longer reads.\n\n" +
			"The bare command reports and deletes nothing: for every file, the palace.scoring block to carry " +
			"into that host's <config dir>/vibe-palace/projects/<slug>.toml, and the sections dropped with no " +
			"per-project tier; then the census of untracked files. --apply prints the same report, then " +
			"deletes the files and commits the removal as one commit. It never pushes.\n\n" +
			"--apply refuses unless: no config is untracked; the vault is the top level of its own git " +
			"repository; git is enabled and a commit identity is set; no merge, cherry-pick, revert or rebase " +
			"is in progress; Projects/ is clean; HEAD is on a named branch; and a surface stamp at this " +
			"binary's version is committed in HEAD (no remotes) or at every remote's tip, each tip an ancestor " +
			"of HEAD. A failure after the first deletion restores every file from HEAD, with one exception: " +
			"if the commit lands but git leaves some paths in it, that commit holds PART of the retirement, the " +
			"rest is restored, the command exits 2 naming both, and re-running --apply finishes it.",
		Flags: migrateProjectConfigsFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate project-configs", Comment: "Report what would be retired; deletes nothing"},
			{Cmd: "vp migrate project-configs --apply", Comment: "Retire the tracked configs in one commit"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateProjectConfigsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate project-configs: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate project-configs: %v\n", err)
				return cli.ExitUser
			}
			// preRun's surfaceGate checked only the CONFIGURED vault; this is
			// the root this run will actually write, however it was resolved.
			if code := enforceSurfaceOnRoot(root); code != cli.ExitOK {
				return code
			}
			return runProjectConfigRetirement(root, fv.Bool("--apply"), os.Stdout, os.Stderr)
		},
	}
}

// retiringProjectConfig is one Projects/<slug>/config.toml as the planner found it.
type retiringProjectConfig struct {
	Rel     string
	Slug    string
	Sha256  string   // of the bytes read; the delete refuses if they changed
	Scoring string   // renderScoringSections output, "" when there is none
	Dropped []string // sections with no per-project tier
	Problem string   // why this file cannot be retired as found, "" when it can
}

// projectConfigRetirementPlan is everything the report prints and the apply acts on.
type projectConfigRetirementPlan struct {
	Configs   []retiringProjectConfig
	Untracked []string // vault-relative, among Configs
}

// planProjectConfigRetirement reads the vault and returns the plan. It writes
// nothing — the source audit's plannerNoWrite rule names it — and its one git
// call is storage.UntrackedAmong, which only reads the index.
func planProjectConfigRetirement(root string) (*projectConfigRetirementPlan, error) {
	plan := &projectConfigRetirementPlan{}
	entries, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return plan, nil
		}
		return nil, fmt.Errorf("read Projects/: %w", err)
	}
	var rels []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := "Projects/" + e.Name() + "/config.toml"
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", rel, err)
		}
		c := retiringProjectConfig{Rel: rel, Slug: e.Name()}
		if !info.Mode().IsRegular() {
			c.Problem = "not a regular file"
		} else {
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", rel, err)
			}
			sum := sha256.Sum256(data)
			c.Sha256 = hex.EncodeToString(sum[:])
			c.Scoring, c.Dropped, err = storage.RenderRetiredScoring(data)
			if err != nil {
				c.Problem = err.Error()
			}
		}
		plan.Configs = append(plan.Configs, c)
		rels = append(rels, rel)
	}
	sort.Slice(plan.Configs, func(i, j int) bool { return plan.Configs[i].Rel < plan.Configs[j].Rel })
	sort.Strings(rels)
	plan.Untracked, err = storage.UntrackedAmong(root, rels)
	if err != nil {
		return nil, fmt.Errorf("untracked census: %w", err)
	}
	return plan, nil
}

// censusLine is the untracked census, derived — never a constant.
func (p *projectConfigRetirementPlan) censusLine() string {
	names := "none"
	if len(p.Untracked) > 0 {
		names = strings.Join(p.Untracked, ", ")
	}
	return fmt.Sprintf("untracked survivors: %s (%d on disk, %d tracked)",
		names, len(p.Configs), len(p.Configs)-len(p.Untracked))
}

func (p *projectConfigRetirementPlan) print(w io.Writer, root string) {
	printVaultRoot(w, root)
	for _, c := range p.Configs {
		fmt.Fprintf(w, "\n%s\n", c.Rel)
		if c.Problem != "" {
			fmt.Fprintf(w, "  CANNOT RETIRE: %s\n", c.Problem)
			continue
		}
		if c.Scoring == "" {
			fmt.Fprintln(w, "  palace.scoring: none")
		} else {
			dest := "<config dir>/vibe-palace/projects/" + c.Slug + ".toml"
			if hp, err := storage.HostProjectConfigPath(c.Slug); err == nil {
				dest = hp
			}
			fmt.Fprintf(w, "  palace.scoring — carry into %s on each host that wants it:\n", dest)
			for _, line := range strings.Split(strings.TrimRight(c.Scoring, "\n"), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
		if len(c.Dropped) > 0 {
			fmt.Fprintf(w, "  dropped, no per-project tier: %s\n", strings.Join(c.Dropped, ", "))
		}
	}
	fmt.Fprintf(w, "\n%s\n", p.censusLine())
}

// retireAfterDelete is a test seam for storage.RetireProjectConfigs; nil in
// production.
var retireAfterDelete func(n int) error

// runProjectConfigRetirement plans, prints, and with apply executes. The
// census line is printed before every refusal the apply can make.
func runProjectConfigRetirement(root string, apply bool, stdout, stderr io.Writer) int {
	const name = "vp migrate project-configs"
	plan, err := planProjectConfigRetirement(root)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	plan.print(stdout, root)
	if !apply {
		fmt.Fprintln(stdout, "dry run: nothing was deleted; --apply retires the tracked configs")
		return cli.ExitOK
	}
	if len(plan.Configs) == 0 {
		fmt.Fprintln(stdout, "nothing to retire")
		return cli.ExitOK
	}
	if len(plan.Untracked) > 0 {
		fmt.Fprintf(stderr, "%s: refusing: %d untracked config(s) have no copy in HEAD to restore from. "+
			"Commit each one, or remove it, then re-run:\n", name, len(plan.Untracked))
		for _, rel := range plan.Untracked {
			fmt.Fprintf(stderr, "  vp vault delete %s\n", rel)
		}
		return cli.ExitUser
	}
	var retiring []storage.RetiringConfig
	for _, c := range plan.Configs {
		if c.Problem != "" {
			fmt.Fprintf(stderr, "%s: refusing: %s cannot be retired as found (%s); fix or remove it, then re-run\n",
				name, c.Rel, c.Problem)
			return cli.ExitUser
		}
		retiring = append(retiring, storage.RetiringConfig{Rel: c.Rel, Sha256: c.Sha256})
	}
	if err := refuseUnlessOwnGitRepo(root); err != nil {
		fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
		return cli.ExitUser
	}
	if err := storage.CheckProjectConfigRetirement(root, surface.MCPSurfaceVersion); err != nil {
		fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
		return cli.ExitUser
	}
	rels := make([]string, len(retiring))
	for i, c := range retiring {
		rels[i] = c.Rel
	}
	msg := fmt.Sprintf("Retire %d per-project vault config(s)\n\n"+
		"vp migrate project-configs removed the Projects/<slug>/config.toml files vp no\n"+
		"longer reads. Scoring moved to each host's <config dir>/vibe-palace/projects/<slug>.toml.\n\n%s",
		len(rels), strings.Join(rels, "\n"))
	sha, err := storage.RetireProjectConfigs(root, msg, retiring, retireAfterDelete)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	for _, rel := range rels {
		fmt.Fprintf(stdout, "deleted %s\n", rel)
	}
	fmt.Fprintf(stdout, "retired %d config(s) in commit %s (not pushed; the next vp vault sync publishes it)\n", len(rels), sha)
	return cli.ExitOK
}
