// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
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

func runCheck(version string) int {
	var results []check.Result

	configPath, vaultPath, r := check.CheckConfig()
	results = append(results, r)

	if r.Status == check.Fail {
		results = append(results,
			check.Result{Name: "Vault", Status: check.Skip},
			check.Result{Name: "Settings", Status: check.Skip},
			check.Result{Name: "Embedder", Status: check.Skip},
			check.Result{Name: "Git", Status: check.Skip},
			check.Result{Name: "Config Staleness", Status: check.Skip},
		)
		results = append(results, check.CheckProject())
		n := check.Print(os.Stdout, version, results)
		if n > 0 {
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	r = check.CheckVault(vaultPath)
	results = append(results, r)
	if r.Status == check.Fail {
		results = append(results,
			check.Result{Name: "Settings", Status: check.Skip},
			check.Result{Name: "Embedder", Status: check.Skip},
			check.Result{Name: "Git", Status: check.Skip},
			check.Result{Name: "Config Staleness", Status: check.Skip},
		)
		results = append(results, check.CheckProject())
		n := check.Print(os.Stdout, version, results)
		if n > 0 {
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	v := storage.NewVault(vaultPath)

	cfg, r := check.CheckSettings(v)
	results = append(results, r)
	if r.Status == check.Fail {
		results = append(results,
			check.Result{Name: "Embedder", Status: check.Skip},
			check.Result{Name: "Git", Status: check.Skip},
			check.Result{Name: "Config Staleness", Status: check.Skip},
		)
		results = append(results, check.CheckProject())
		n := check.Print(os.Stdout, version, results)
		if n > 0 {
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	check.ProgressLine(os.Stderr, "Embedder", "loading model (first run downloads ~90MB)...")
	results = append(results, check.CheckEmbedder(cfg, v, configPath))
	results = append(results, check.CheckGit(vaultPath, cfg.GitEnabled))
	results = append(results, check.CheckConfigStaleness(configPath))
	results = append(results, check.CheckProject())
	if cwd, err := os.Getwd(); err == nil {
		results = append(results, check.CheckAgentDrift(cwd))
	}

	n := check.Print(os.Stdout, version, results)
	if n > 0 {
		return cli.ExitUser
	}
	return cli.ExitOK
}
