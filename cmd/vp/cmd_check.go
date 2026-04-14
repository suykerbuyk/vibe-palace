// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdCheck(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:        "check",
		Synopsis:    "vp check",
		Description: "Verify installation, config, vault, embedder, and project detection. Reports pass/fail status for each component.",
		Examples: []cli.Example{
			{Cmd: "vp check", Comment: "Run all installation checks"},
		},
		Run: func(args []string) int {
			return runCheck(info.Version)
		},
	}
}

// runCheck delegates the five reconciled artifacts (global config, vault
// directory, vault settings, cwd-project, vault-project) to their
// reconcilers' Check() methods so vp check and vp config sync see the same
// world. Embedder and agent-drift checks stay inline — neither has a
// reconciler (Embedder is intentionally excluded; agent drift belongs to
// vp commands upgrade).
func runCheck(version string) int {
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

	n := check.Print(os.Stdout, version, results)
	if n > 0 {
		return cli.ExitUser
	}
	return cli.ExitOK
}
