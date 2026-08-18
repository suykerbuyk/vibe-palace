// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

var checkFlags = []cli.FlagDef{
	{Name: "--json", Help: "Output JSON"},
	{Name: "--check", Help: "Run only the named check(s) (comma-separated list; e.g. surface, resume-caps)", Arg: "NAME"},
}

func cmdCheck(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:     "check",
		Synopsis: "vp check [--json] [--check NAME[,NAME...]]",
		// The selectable-name list is JOINED FROM check.ProducerOrder, never
		// typed out: a hand-written copy is a second registry of check names,
		// and this one had already gone stale once by omitting checks the
		// registry accepted.
		Description: "Verify installation, config, vault, embedder, surface compatibility, project detection, and vault hygiene (vault filesystem, stray scaffolds, resume.md size/row caps, host-local plan refs, host-rooted absolute paths, inviolable-core size floor, resume pin-marker coverage). Reports pass/fail status for each component. With --check, runs only the named check(s) via selective execution — skipping the expensive embedder load and tool-registry build — for fast scripting / AI preflights. Selectable names: " + strings.Join(check.ProducerOrder, ", ") + ".",
		Flags:       checkFlags,
		Examples: []cli.Example{
			{Cmd: "vp check", Comment: "Run all installation checks"},
			{Cmd: "vp check --json", Comment: "Emit machine-readable results (exit_code 1 on any failure)"},
			{Cmd: "vp check --check surface --json", Comment: "Fast surface-only preflight (used by restart/wrap)"},
			{Cmd: "vp check --check resume-caps", Comment: "Warn on any project resume.md over its size/row caps"},
			{Cmd: "vp check --check resume-refs", Comment: "Warn on any resume.md committing a host-local ~/.claude/plans/… path"},
			{Cmd: "vp check --check vault-abs-paths", Comment: "Warn on any resume.md/workflow.md committing a host-rooted absolute path (/home/…, C:\\…) — true on one machine, false everywhere else"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(checkFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp check: %v\n", err)
				return cli.ExitUser
			}
			return runCheck(info, fv)
		},
	}
}

// runSelectedChecks resolves the vault from the process working directory —
// the CLI's contract, and the CLI's alone — and hands the root plus the raw
// filter to the shared selector registry in internal/check.
//
// The registry itself (check.Producers / check.RunSelected) used to live here,
// unexported in package main, where no MCP tool could reach it. It moved down
// so `vp check --check` and the vp_check MCP tool dispatch one map instead of
// two that drift. Only the cwd resolution stayed behind: check.RunSelected is
// pure, and the MCP tool passes the vault bound at registration instead.
//
// Unknown name → error (caller maps to ExitUser).
func runSelectedChecks(filter string) ([]check.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	// A REJECTED config file is reported; an ABSENT one still degrades to "".
	//
	// This path never runs CheckConfigAt — it dispatches the producer registry
	// directly — so nothing else in a `vp check --check …` run would mention a
	// rejected .vibe-palace.toml. Degrading it to "" makes every producer emit
	// "no vault configured", which reads as an unconfigured machine rather than
	// as a config pointing at the wrong vault: the silent-skip class this
	// command exists to catch.
	//
	// The absent case must NOT be promoted to an error. An un-set-up machine
	// has no global config, ResolveVaultPath fails, and `vp check --check
	// surface` is exactly the preflight that has to keep working there.
	vaultRoot := ""
	vp, _, perr := storage.ResolveVaultPath(cwd)
	switch {
	case perr == nil:
		vaultRoot = vp
	case errors.Is(perr, storage.ErrSwallowedVaultPath):
		return nil, perr
	}
	return check.RunSelected(vaultRoot, filter)
}

// isSurfaceOnly reports whether the filter, after splitting/trimming and
// dropping empties, is exactly the set {"surface"} — the case where the JSON
// binary block can skip the expensive registeredToolCount() build.
func isSurfaceOnly(filter string) bool {
	seen := map[string]struct{}{}
	for raw := range strings.SplitSeq(filter, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
	}
	if len(seen) != 1 {
		return false
	}
	_, ok := seen["surface"]
	return ok
}

// runCheck renders the diagnostic results either as the human-readable table
// (with a trailing failure count → exit code) or, with --json, as the stable
// JSONReport (exit_code carried on the report).
func runCheck(info cli.BuildInfo, fv *cli.FlagValues) int {
	asJSON := fv.Bool("--json")
	filter := fv.Get("--check")

	var results []check.Result
	surfaceOnly := false
	if filter != "" {
		var err error
		results, err = runSelectedChecks(filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp check: %v\n", err)
			return cli.ExitUser
		}
		surfaceOnly = isSurfaceOnly(filter)
	} else {
		results = gatherCheckResults()
	}

	if asJSON {
		var bi check.JSONBinaryInfo
		if surfaceOnly {
			bi = check.JSONBinaryInfo{
				Surface: surface.MCPSurfaceVersion, // free constant — NOT 0
				Commit:  info.Commit,               // already in hand
				Tools:   0,                         // skip registeredToolCount(): the only expensive field
			}
		} else {
			bi = binaryInfo(info)
		}
		report := check.ToJSON(results, bi)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report)
		if report.ExitCode != 0 {
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	n := check.Print(os.Stdout, info.Version, results)
	if n > 0 {
		return cli.ExitUser
	}
	return cli.ExitOK
}

// gatherCheckResults runs every diagnostic check and returns the ordered
// results, shared by both the human and --json renderings of `vp check`.
//
// It delegates the five reconciled artifacts (global config, vault directory,
// vault settings, cwd-project, vault-project) to their reconcilers' Check()
// methods so vp check and vp config sync see the same world. Embedder,
// agent-drift, and surface checks stay inline — none has a reconciler
// (Embedder is intentionally excluded; agent drift belongs to vp commands
// upgrade; surface mirrors the runtime gate).
func gatherCheckResults() []check.Result {
	var results []check.Result
	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	// --- GlobalConfig (+ staleness) ---
	gc := reconcile.NewGlobalConfig(cwd, reconcile.GlobalSeed{})
	gcRows := gc.Check(ctx)
	results = append(results, gcRows...)
	globalOK := len(gcRows) > 0 && gcRows[0].Status != check.Fail

	// --- Vault (+ git) ---
	var vault *storage.Vault
	vaultOK := false
	if !globalOK {
		results = append(results,
			check.Result{Name: "Vault", Status: check.Skip},
			check.Result{Name: "Settings", Status: check.Skip},
			check.Result{Name: "Vault project", Status: check.Skip},
			check.Result{Name: "Embedder", Status: check.Skip},
		)
	} else {
		vr := reconcile.NewVault(cwd, reconcile.VaultSeed{})
		vRows := vr.Check(ctx)
		results = append(results, vRows...)
		vaultOK = len(vRows) > 0 && vRows[0].Status != check.Fail
		if vaultOK {
			if vaultPath, _, perr := storage.ResolveVaultPath(cwd); perr == nil && vaultPath != "" {
				vault = storage.NewVault(vaultPath)
				// Templates/ drift, as ONE aggregate row.
				//
				// This used to append the TemplateTree reconciler's
				// per-resource rows — one per embedded template, ~38 of
				// them, all reading "served from embedded floor" on a
				// healthy vault. That shape is also why drift was
				// CLI-only: the registry serves a row an agent reads,
				// and 38 rows is not one.
				//
				// check.CheckTemplateDrift wraps the SAME classification
				// (the reconciler now delegates to it too) and names the
				// drifting resources in Details, so nothing actionable is
				// lost — only the pass-row noise. Per-resource planning
				// detail still lives in `vp config sync --dry-run`, which
				// is where an operator goes to act on drift.
				results = append(results, check.CheckTemplateDrift(vaultPath))

				// Vault-wide: flag a vault sitting on a filesystem that rejects
				// ":" in filenames (NTFS/exFAT), reported plainly here rather
				// than surfacing as a cryptic write failure later.
				results = append(results, check.CheckVaultFilesystem(vaultPath))

				// Vault-wide: report the writer fingerprint this host writes
				// under, so which-notes-are-mine is COMPUTED rather than
				// inferred. Quiet (Pass) unless this host has written nothing
				// while other identities exist.
				results = append(results, check.CheckWriterIdentity(vaultPath))

				// Vault-wide: flag scaffold-only orphan projects (stray
				// `vp init` / un-isolated test residue like Projects/p).
				results = append(results, check.CheckStrayScaffolds(vault))

				// Vault-wide: fail a .gitattributes naming the deleted
				// vp-surface merge driver. git emits NO diagnostic for an
				// attribute naming an undefined driver — it silently text
				// merges — so this is the one vault-hygiene row that is a
				// hard Fail rather than an advisory Info.
				results = append(results, check.CheckSurfaceMergeDriver(vault))

				// Vault-wide: flag resume.md files that have outgrown the
				// wrap-time caps. Advisory only — with the typed resume
				// editors retired, no write path can enforce a cap, so
				// detection is all that is on offer.
				results = append(results, check.CheckResumeCaps(vault))

				// Vault-wide: flag resume.md files that commit a host-local
				// plan reference (~/.claude/plans/… or an absolute
				// /…/.claude/plans/… path) — dead weight in a shared,
				// committed artifact. Advisory only, never Fail.
				results = append(results, check.CheckResumeRefs(vault))

				// Vault-wide: flag host-rooted absolute paths committed into
				// the ADR-009 core (resume.md + workflow.md). The vault syncs
				// to every machine, so such a path is a fact about the ONE host
				// that wrote it — and a stale one that still exists as an empty
				// directory reads as plausible instead of failing loudly, which
				// is the iter 188 bug this check was commissioned for. Advisory
				// only, never Fail: rewriting the pointer is a human judgement
				// about what the document meant to say.
				results = append(results, check.CheckVaultAbsPaths(vault))

				// Vault-wide: flag iterations.md headings the writer would not
				// have emitted — a framed entry that is not "## Iteration N —",
				// a numbered heading that is not canonical, and a title that
				// repeats its own "Iteration N —" prefix. The first class is
				// the load-bearing one: a framed orphan is invisible to the
				// numbered parser, so ParseEntries glues its whole narrative
				// onto the previous entry and vp_get_iteration answers for that
				// N with two iterations' text and reports success. Advisory
				// only, never Fail: assigning an iteration number to an orphan
				// is a human judgement the migration refuses to make itself.
				results = append(results, check.CheckIterationHeadings(vault))
			}
		}
	}

	// --- VaultSettings + Embedder + VaultProject ---
	if globalOK {
		if !vaultOK || vault == nil {
			results = append(results,
				check.Result{Name: "Settings", Status: check.Skip},
				check.Result{Name: "Vault project", Status: check.Skip},
				check.Result{Name: "Embedder", Status: check.Skip},
			)
		} else {
			// Settings — call CheckSettings directly so we can reuse cfg
			// for CheckEmbedder. The VaultSettings reconciler wraps the
			// same primitive, so the row is identical.
			cfg, sRow := check.CheckSettings(vault)
			results = append(results, sRow)

			// Vault project — derive slug from cwd; reconciler tolerates
			// empty slug by emitting Skip.
			slug, _ := project.DetectProject(cwd)
			vp := reconcile.NewVaultProject(vault, slug)
			results = append(results, vp.Check(ctx)...)

			// Phase 4: per-project scaffold check. Only the current
			// project's scaffold state is surfaced here — config sync
			// enumerates every project under Projects/ in its default
			// scope, but vp check stays project-local for parity with
			// the rest of its rows.
			if slug != "" {
				scaffold := reconcile.NewTemplateTree(vault.Root, "Projects/"+slug,
					reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeScaffold})
				results = append(results, scaffold.Check(ctx)...)
			}

			if sRow.Status == check.Fail {
				results = append(results, check.Result{Name: "Embedder", Status: check.Skip})
			} else {
				configPath, _ := storage.VaultConfigFilePath()
				check.ProgressLine(os.Stderr, "Embedder", "loading model (first run downloads ~90MB)...")
				results = append(results, check.CheckEmbedder(cfg, vault, configPath))
			}
		}
	}

	// --- CwdProject ---
	if !globalOK {
		results = append(results, check.CheckProject())
	} else {
		cp := reconcile.NewCwdProject(cwd, reconcile.CwdProjectSeed{})
		results = append(results, cp.Check(ctx)...)
	}

	// --- Agent drift (not reconciled — owned by vp commands upgrade) ---
	results = append(results, check.CheckAgentDrift(cwd))

	// --- Project-root .gitignore (advisory — reconciled by vp init /
	// vp commands upgrade, surfaced here when canonical entries are missing). ---
	results = append(results, check.CheckProjectGitignore(cwd))

	// --- MCP host registration (advisory — one row per detected host:
	// Claude/Grok/Zed, reporting whether vibe-palace is registered). ---
	results = append(results, check.CheckMCPHosts()...)

	// --- User-global slash surfaces (advisory — vp mcp install emit). ---
	results = append(results, check.CheckHostSurfaces()...)

	// --- Stale MCP server image (advisory — make install is not enough). ---
	results = append(results, check.CheckStaleMCP())

	// --- Surface compatibility — last check, mirroring the runtime gate so
	// the binary-vs-vault verdict reads as the closing line of the report. ---
	//
	// A REJECTED config does not degrade to "": CheckSurface("") reports "no
	// vault configured", and that is the unconfigured-machine reading this
	// conversion exists to stop — printed here as the report's CLOSING LINE,
	// where it would be the last thing an operator sees after Config already
	// Failed for the real reason.
	if _, _, perr := storage.ResolveVaultPath(cwd); errors.Is(perr, storage.ErrSwallowedVaultPath) {
		results = append(results, check.Result{
			Name: "Surface", Status: check.Skip,
			Summary: "not evaluated — vault path rejected (see Config above)",
		})
		return results
	}
	surfaceVault := ""
	if vaultPath, _, perr := storage.ResolveVaultPath(cwd); perr == nil {
		surfaceVault = vaultPath
	}
	results = append(results, check.CheckSurface(surfaceVault))

	return results
}

// binaryInfo assembles the JSON binary-metadata block: this binary's MCP
// surface version, the count of registered MCP tools, and the build commit.
func binaryInfo(info cli.BuildInfo) check.JSONBinaryInfo {
	return check.JSONBinaryInfo{
		Surface: surface.MCPSurfaceVersion,
		Tools:   registeredToolCount(),
		Commit:  info.Commit,
	}
}

// registeredToolCount counts the MCP tools this binary exposes. It registers
// the full tool set against a throwaway registry — a nil embedder is harmless
// because tool constructors only stash the engine, never call it at
// registration — so the count is deterministic and needs no real vault,
// embedder, or model download.
func registeredToolCount() int {
	v := storage.NewVault("")
	srv := mcpkg.NewServer(v)
	eng := search.NewEngine(nil, v, storage.Config{})
	tools.RegisterAll(srv.Registry(), vpctx.NewResolver(""), v, eng)
	return len(srv.Registry().List())
}
