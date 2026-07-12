// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
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

	initLoggingFor(v, cfg)

	// The embedder is lazy: loading the ONNX model can take tens of seconds and
	// downloads ~90MB on a cold cache. Doing that here would stall the MCP
	// initialize handshake past the host's timeout, killing the session before
	// a single tool is registered. The model loads on first use instead — which
	// means a load failure now surfaces at first search, not at startup.
	modelDir := v.VaultLocalDir() + "/models"
	emb := embedder.NewLazy(func() (embedder.Embedder, error) {
		e, err := embedder.NewONNX(
			cfg.EmbedderModel, modelDir,
			cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
		)
		if err != nil {
			return nil, fmt.Errorf("embedder: %w", err)
		}
		return e, nil
	})

	// The engine builds each project's index lazily, on the first search that
	// touches it. Rebuilding all projects up front cost minutes of CPU on every
	// MCP spawn — most of it for projects the session never searched.
	eng := search.NewEngine(emb, v, cfg)

	resolver := vpctx.NewResolver(v.Root)
	srv := mcpkg.NewServer(v)
	// stdio serves local Claude/Zed — bootstrap slim default stays false.
	tools.RegisterAll(srv.Registry(), resolver, v, eng, tools.WithConfig(cfg))
	tools.RegisterResources(srv, resolver, v)

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

// initLoggingFor points process logging at v's log file. It is the single
// definition of where vp.log lives and at what level; every entry point routes
// through it — the CLI pre-run (main.go), bootstrap above, and the hook's
// re-point once it has parsed its payload — so no two of them can disagree
// about the destination.
func initLoggingFor(v *storage.Vault, cfg storage.Config) {
	// Best-effort: Init falls back to a discard handler and reports the error,
	// but a command must never fail because logging could not be set up.
	_ = vplog.Init(v.VaultLocalDir()+"/vp.log", parseLogLevel(cfg.LogLevel))
}

// initLoggingForVault is initLoggingFor for callers that hold a vault but have
// not loaded its config. An unreadable config is not a reason to run without a
// log: parseLogLevel("") yields the same Info default LoadConfig would.
func initLoggingForVault(v *storage.Vault) {
	cfg, err := v.LoadConfig("")
	if err != nil {
		cfg = storage.Config{}
	}
	initLoggingFor(v, cfg)
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
