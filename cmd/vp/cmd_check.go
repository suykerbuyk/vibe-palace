// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
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
		Name:        "check",
		Synopsis:    "vp check [--json] [--check NAME[,NAME...]]",
		Description: "Verify installation, config, vault, embedder, surface compatibility, project detection, and vault hygiene (stray scaffolds, resume.md size/row caps). Reports pass/fail status for each component. With --check, runs only the named check(s) via selective execution — skipping the expensive embedder load and tool-registry build — for fast scripting / AI preflights. Selectable names: surface, resume-caps.",
		Flags:       checkFlags,
		Examples: []cli.Example{
			{Cmd: "vp check", Comment: "Run all installation checks"},
			{Cmd: "vp check --json", Comment: "Emit machine-readable results (exit_code 1 on any failure)"},
			{Cmd: "vp check --check surface --json", Comment: "Fast surface-only preflight (used by restart/wrap)"},
			{Cmd: "vp check --check resume-caps", Comment: "Warn on any project resume.md over its size/row caps"},
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

// checkProducers maps a --check name to a light, dependency-light producer.
// Designed to grow one cheap check at a time. The string arg is the resolved
// vault path (cheap to compute; "" when unresolved — CheckSurface tolerates it).
var checkProducers = map[string]func(vaultRoot string) []check.Result{
	"surface": func(vaultRoot string) []check.Result { return []check.Result{check.CheckSurface(vaultRoot)} },
	"resume-caps": func(vaultRoot string) []check.Result {
		if vaultRoot == "" {
			return []check.Result{{Name: "Resume caps", Status: check.Skip, Summary: "no vault configured"}}
		}
		return []check.Result{check.CheckResumeCaps(storage.NewVault(vaultRoot))}
	},
}

// runSelectedChecks splits a comma-separated filter, validates each name
// against checkProducers, and collects results. Unknown name → error (caller
// maps to ExitUser). Resolves the vault path once (cheap).
func runSelectedChecks(filter string) ([]check.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	vaultRoot := ""
	if vp, _, perr := storage.ResolveVaultPath(cwd); perr == nil {
		vaultRoot = vp
	}
	var results []check.Result
	for raw := range strings.SplitSeq(filter, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		prod, ok := checkProducers[name]
		if !ok {
			return nil, fmt.Errorf("unknown check: %s", name)
		}
		results = append(results, prod(vaultRoot)...)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no checks selected")
	}
	return results, nil
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
				// Phase 3: surface TemplateTree drift alongside the other
				// reconcilers so vp check stays at parity with
				// vp config sync --dry-run.
				tt := reconcile.NewTemplateTree(vaultPath, "Templates", reconcile.TemplateTreeSeed{
					Mode: reconcile.TemplateModeMaterialize,
				})
				results = append(results, tt.Check(ctx)...)

				// Vault-wide: flag scaffold-only orphan projects (stray
				// `vp init` / un-isolated test residue like Projects/p).
				results = append(results, check.CheckStrayScaffolds(vault))

				// Vault-wide: flag resume.md files that have outgrown the
				// wrap-time caps. Advisory only — with the typed resume
				// editors retired, no write path can enforce a cap, so
				// detection is all that is on offer.
				results = append(results, check.CheckResumeCaps(vault))
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

	// --- Surface compatibility — last check, mirroring the runtime gate so
	// the binary-vs-vault verdict reads as the closing line of the report. ---
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
