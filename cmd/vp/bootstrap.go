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
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
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
	eng      *search.Engine
	resolver *vpctx.Resolver
	srv      *mcpkg.Server
	// launchCwd is the directory vault was RESOLVED FROM. Carried on the stack
	// so a second server built from the same vault (`vp mcp serve`) arms its
	// stale-binding gate against the same directory rather than a fresh Getwd
	// that only happens to match.
	launchCwd string
	// snap is the startup snapshot of the host's [project_vaults] routing table,
	// built once here. Carried on the stack so the second server `vp mcp serve`
	// builds installs the SAME snapshot (via SetProjectVaultSnapshot) rather than
	// a fresh, unbuilt one that would fail-close every project-keyed write.
	snap storage.ProjectVaultSnapshot
	// reg is the vault-stack registry (U4). It owns the startup stack (vault,
	// engine, resolver, shared-model handle) and, in time, every routed stack.
	// close() closes it, which closes each stack's engine — the single decrement
	// of its shared-model handle. eng/resolver/vault above are the STARTUP
	// stack's, kept as direct fields for the existing stdio/serve wiring.
	reg *vaultstack.Registry
}

// vaultStackBuilder is the injected BuildFunc the registry calls to construct one
// vault's stack. It opens the vault at an already-resolved root, loads its
// config, Acquires a refcounted handle into the ONE shared model cache (so N
// engines share one resident model), and builds the engine with that SAME handle
// as its embedder — the engine is the handle's sole owner, so Engine.Close is the
// single decrement. The engine options match the pre-U4 bootstrap exactly.
func vaultStackBuilder(models *embedder.SharedModelCache) vaultstack.BuildFunc {
	return func(ctx context.Context, root string) (*vaultstack.VaultStack, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v := storage.NewVault(root)
		cfg, err := v.LoadConfig("")
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}

		// The model is lazy inside the shared cache: Acquire bumps the refcount
		// and returns immediately; the ~90MB-cold / tens-of-seconds ONNX load
		// happens on first use, off the MCP initialize handshake, and is paid
		// ONCE per key across every vault that shares it.
		//
		// newVaultEmbedder is captured as the construct closure; it uses
		// RequestedBackend (fails loud on an "ort" ask of a default binary),
		// while sharedModelKey uses the ResolveBackend-masked value so the cache
		// key matches each engine's embed-cache fingerprint.
		handle, err := models.Acquire(ctx, sharedModelKey(cfg), func() (embedder.Embedder, error) {
			e, cerr := newVaultEmbedder(v, cfg)
			if cerr != nil {
				return nil, fmt.Errorf("embedder: %w", cerr)
			}
			return e, nil
		})
		if err != nil {
			return nil, fmt.Errorf("acquire shared model: %w", err)
		}

		// WithBackgroundConverter / WithRebuildLauncher: identical to the pre-U4
		// long-lived serving engine (the brute→HNSW converter and the detached
		// `vp index rebuild` seam live only on this engine, never a CLI/one-shot
		// one). The engine holds `handle` as its embedder; VaultStack.Close ->
		// Engine.Close -> handle.Close is the single refcount decrement.
		eng := search.NewEngine(handle, v, cfg,
			search.WithBackgroundConverter(),
			search.WithRebuildLauncher(tools.NewRebuildLauncher(v, detachlaunch.Launch)))

		resolver := vpctx.NewResolver(v.Root)
		return vaultstack.NewVaultStack(root, v, cfg, eng, resolver, handle), nil
	}
}

// bootstrap opens the vault and initializes the full MCP server stack. At N=1
// (every launch today) it binds and serves exactly one vault, as before; the
// registry + shared-model cache it now builds are the foundation routing (U5)
// stands on, not a behavior change to the startup path.
func bootstrap() (*serverStack, error) {
	// One Getwd, used BOTH to resolve the vault and to arm the stale-binding
	// gate, so the two can never disagree about which directory this binding
	// came from.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	// Resolve the startup vault from cwd (honoring any .vibe-palace.toml
	// override) to learn its root and to initialise logging BEFORE the engine is
	// built, exactly as before. The registry re-opens the vault by that root.
	startupVault, err := OpenProjectVaultAt(cwd)
	if err != nil {
		return nil, fmt.Errorf("open vault: %w", err)
	}
	startupCfg, err := startupVault.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	initLoggingFor(startupVault, startupCfg)

	// One shared model cache for the whole process; the registry builds stacks
	// lazily and shares the model across them.
	models := embedder.NewSharedModelCache()
	reg := vaultstack.NewRegistry(vaultStackBuilder(models), models)

	// context.Background(): bootstrap takes no signal ctx; on `vp mcp serve` no
	// request ctx exists until after this runs.
	ctx := context.Background()
	stack, err := reg.StackFor(ctx, startupVault.Root)
	if err != nil {
		return nil, fmt.Errorf("build startup stack: %w", err)
	}
	v := stack.Vault
	cfg := stack.Cfg

	srv := mcpkg.NewServer(v)
	srv.WatchVaultBinding(cwd)

	// Build the [project_vaults] routing snapshot ONCE, here, where the config
	// is readable by construction (the vault just resolved through it). An
	// unbuilt snapshot (a mid-startup config change) is installed as-is and the
	// mutating gate fails project-keyed writes closed; reads still work.
	snap, serr := storage.BuildProjectVaultSnapshot(ctx)
	if serr != nil {
		slog.Warn("mcp: project-vault routing snapshot unbuilt; project-keyed mutating writes will fail closed",
			"op", "mcp.snapshot", "fault", "operational", "err", serr)
	}
	srv.SetProjectVaultSnapshot(snap)
	// Dual install (U3): the same snapshot also backs the storage-layer write
	// guard and the derived-delete warning, so the MCP funnel classifies against
	// the SAME startup snapshot as the dispatch gate (no mid-session drift).
	storage.SetActiveProjectVaultSnapshot(snap)

	// stdio serves local Claude/Zed — bootstrap slim default stays false.
	// RegisterAll keeps today's signature and the STARTUP stack's vault + engine;
	// no vaultstack.Registry crosses into internal/mcp (that is U5).
	tools.RegisterAll(srv.Registry(), stack.Resolver, v, stack.Engine, tools.WithConfig(cfg))
	tools.RegisterResources(srv, stack.Resolver, v)

	return &serverStack{
		vault:     v,
		cfg:       cfg,
		eng:       stack.Engine,
		resolver:  stack.Resolver,
		srv:       srv,
		launchCwd: cwd,
		snap:      snap,
		reg:       reg,
	}, nil
}

// close shuts down the server stack. Registry.Close closes every stack's engine,
// and each Engine.Close is the single decrement of its shared-model handle (the
// shared model Destroys at the last handle) — so there is NO separate embedder
// close here; a second one would double-decrement.
func (s *serverStack) close() {
	if s.reg != nil {
		s.reg.Close()
	}
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
