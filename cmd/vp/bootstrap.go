// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// openProjectVault opens the vault honoring any cwd-local vault_path
// override from .vibe-palace.toml, walking up from os.Getwd().
func openProjectVault() (*storage.Vault, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	return OpenProjectVaultAt(cwd)
}

// OpenProjectVaultAt is the explicit-root variant of openProjectVault. It
// opens the vault honoring any cwd-local vault_path override found by
// walking up from the given root.
func OpenProjectVaultAt(root string) (*storage.Vault, error) {
	return storage.OpenVaultFromCwd(root)
}

// serverStack holds the full MCP server stack. Callers must defer close().
type serverStack struct {
	vault    *storage.Vault
	cfg      storage.Config
	emb      embedder.Embedder
	eng      *search.Engine
	resolver *vpctx.Resolver
	srv      *mcpkg.Server
}

// bootstrap opens the vault and initializes the full MCP server stack.
func bootstrap() (*serverStack, error) {
	v, err := openProjectVault()
	if err != nil {
		return nil, fmt.Errorf("open vault: %w", err)
	}

	cfg, err := v.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	vplog.Init(v.VaultLocalDir()+"/vp.log", parseLogLevel(cfg.LogLevel))

	modelDir := v.VaultLocalDir() + "/models"
	emb, err := embedder.NewONNX(
		cfg.EmbedderModel, modelDir,
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		vplog.Close()
		return nil, fmt.Errorf("embedder: %w", err)
	}

	eng := search.NewEngine(emb, v, cfg)

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

	return &serverStack{
		vault:    v,
		cfg:      cfg,
		emb:      emb,
		eng:      eng,
		resolver: resolver,
		srv:      srv,
	}, nil
}

// close shuts down the server stack in reverse order.
func (s *serverStack) close() {
	s.eng.Close()
	s.emb.Close()
	vplog.Close()
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
