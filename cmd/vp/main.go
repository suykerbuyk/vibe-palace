// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

func main() {
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

	modelDir := v.VaultLocalDir() + "/models"
	emb, err := embedder.NewONNX(
		cfg.EmbedderModel, modelDir,
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: embedder: %v\n", err)
		os.Exit(1)
	}

	eng := search.NewEngine(emb, v, cfg)
	defer eng.Close()

	// Rebuild indexes for known projects in the background.
	go func() {
		projects, _ := v.ListProjects()
		for _, p := range projects {
			_ = eng.Rebuild(context.Background(), p)
		}
	}()

	resolver := vpctx.NewResolver(v.Root)
	srv := mcpkg.NewServer(v)
	tools.RegisterAll(srv.Registry(), resolver, v, eng)

	if err := srv.Serve(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		os.Exit(1)
	}
}
