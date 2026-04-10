// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

var version = "0.1.0-dev"

const usage = `vp — vibe-palace MCP server

Usage:
  vp                          Start the MCP server (stdio JSON-RPC)
  vp check                    Verify installation, config, and model
  vp migrate vibevault        Import VibeVault sessions into palace
  vp migrate mempalace        Import MemPalace export into palace
  vp version                  Print version

First run of 'vp check' downloads the embedding model (~90MB).
See https://github.com/suykerbuyk/vibe-palace for documentation.
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "--help", "-h":
			fmt.Print(usage)
			return
		case "check":
			os.Exit(runCheck())
		case "migrate":
			os.Exit(runMigrate())
		case "version":
			fmt.Printf("vp %s\n", version)
			return
		default:
			fmt.Fprintf(os.Stderr, "vp: unknown command %q\nRun 'vp help' for usage.\n", os.Args[1])
			os.Exit(1)
		}
		return
	}
	runServe()
}

func runCheck() int {
	var results []check.Result

	configPath, vaultPath, r := check.CheckConfig()
	results = append(results, r)

	if r.Status == check.Fail {
		results = append(results,
			check.Result{Name: "Vault", Status: check.Skip},
			check.Result{Name: "Settings", Status: check.Skip},
			check.Result{Name: "Embedder", Status: check.Skip},
		)
		results = append(results, check.CheckProject())
		return check.Print(os.Stdout, version, results)
	}

	r = check.CheckVault(vaultPath)
	results = append(results, r)
	if r.Status == check.Fail {
		results = append(results,
			check.Result{Name: "Settings", Status: check.Skip},
			check.Result{Name: "Embedder", Status: check.Skip},
		)
		results = append(results, check.CheckProject())
		return check.Print(os.Stdout, version, results)
	}

	v := storage.NewVault(vaultPath)

	cfg, r := check.CheckSettings(v)
	results = append(results, r)
	if r.Status == check.Fail {
		results = append(results, check.Result{Name: "Embedder", Status: check.Skip})
		results = append(results, check.CheckProject())
		return check.Print(os.Stdout, version, results)
	}

	check.ProgressLine(os.Stderr, "Embedder", "loading model (first run downloads ~90MB)...")
	results = append(results, check.CheckEmbedder(cfg, v, configPath))
	results = append(results, check.CheckProject())

	return check.Print(os.Stdout, version, results)
}

func runServe() {
	v, err := storage.OpenVault("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		os.Exit(1)
	}

	cfg, err := v.LoadConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: load config: %v\n", err)
		os.Exit(1)
	}

	vplog.Init(v.VaultLocalDir()+"/vp.log", parseLogLevel(cfg.LogLevel))
	defer vplog.Close()

	modelDir := v.VaultLocalDir() + "/models"
	emb, err := embedder.NewONNX(
		cfg.EmbedderModel, modelDir,
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: embedder: %v\n", err)
		os.Exit(1)
	}
	defer emb.Close()

	eng := search.NewEngine(emb, v, cfg)
	defer eng.Close()

	// Rebuild indexes for known projects in the background.
	go func() {
		projects, err := v.ListProjects()
		if err != nil {
			slog.Warn("background rebuild: list projects failed", "err", err)
			return
		}
		for _, p := range projects {
			if err := eng.Rebuild(context.Background(), p); err != nil {
				slog.Warn("background rebuild failed", "project", p, "err", err)
			}
		}
	}()

	resolver := vpctx.NewResolver(v.Root)
	srv := mcpkg.NewServer(v)
	tools.RegisterAll(srv.Registry(), resolver, v, eng, cfg)

	if err := srv.Serve(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		os.Exit(1)
	}
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
