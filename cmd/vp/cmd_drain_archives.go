// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var drainArchivesFlags = []cli.FlagDef{
	{Name: "--vault-root", Arg: "PATH", Help: "Absolute path to the vault root (required; never inferred)"},
	{Name: "--project", Arg: "SLUG", Help: "The project the trigger resolved (required; never inferred)"},
	{Name: "--first", Arg: "SHA256", Help: "source_sha256 of the archive the trigger just created (optional)"},
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func cmdDrainArchives() *cli.Command {
	return &cli.Command{
		Name:     "drain archives",
		Synopsis: "vp drain archives --vault-root PATH --project SLUG [--first SHA256]",
		Description: "Ingest the pending transcript archives of a vault into this host's " +
			"local index (palace/.local/), the triggering project first: one bounded, " +
			"idempotent pass, then exit. It is launched detached by the session hook, " +
			"vp_capture_session, the pull paths and `vp mcp` startup; nothing else runs " +
			"the automatic pass. --vault-root and --project are required and never " +
			"inferred from the working directory. --first names the archive the trigger " +
			"just created, by its source_sha256. It always exits 0: what happened goes " +
			"to palace/.local/vp.log. It writes nothing tracked.",
		Flags: drainArchivesFlags,
		Examples: []cli.Example{
			{Cmd: "vp drain archives --vault-root /path/to/vault --project myproj", Comment: "Ingest pending archives, myproj first"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(drainArchivesFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp drain archives: %v\n", err)
				return cli.ExitOK
			}
			return runDrainArchives(context.Background(), fv.Get("--vault-root"), fv.Get("--project"), fv.Get("--first"), os.Stderr)
		},
	}
}

// runDrainArchives is the testable body of `vp drain archives` (Scope 5). It
// resolves nothing: an empty or relative vault root, or a missing or invalid
// project, is one line on errOut and exit 0, with no archive opened and no
// lock taken. Otherwise it logs to the vault's palace/.local/vp.log (plan
// revision D12: a detached run's working directory is not the vault), and
// runs ingest.Run with an engine over a lazy embedder, so the model loads
// only on the first cache miss. It exits 0 whatever happened.
func runDrainArchives(ctx context.Context, vaultRoot, project, first string, errOut io.Writer) int {
	if vaultRoot == "" || project == "" {
		fmt.Fprintln(errOut, "vp drain archives: --vault-root and --project are required; nothing ingested")
		return cli.ExitOK
	}
	if !filepath.IsAbs(vaultRoot) {
		fmt.Fprintf(errOut, "vp drain archives: --vault-root must be absolute, got %q; nothing ingested\n", vaultRoot)
		return cli.ExitOK
	}
	if err := slug.Validate(project); err != nil {
		fmt.Fprintf(errOut, "vp drain archives: --project: %v; nothing ingested\n", err)
		return cli.ExitOK
	}
	v := storage.NewVault(vaultRoot)
	initLoggingForVault(v)
	if first != "" && !sha256Pattern.MatchString(first) {
		slog.Warn("vp drain archives: --first is not a source_sha256; ignored", "first", first, "project", project)
		first = ""
	}
	cfg, err := v.LoadConfig(project)
	if err != nil {
		slog.Warn("vp drain archives: cannot load the config; nothing ingested", "project", project, "error", err)
		return cli.ExitOK
	}
	emb := embedder.NewLazy(func() (embedder.Embedder, error) { return newVaultEmbedder(v, cfg) })
	eng := search.NewEngine(emb, v, cfg)
	defer eng.Close()
	res, err := ingest.Run(ctx, ingest.Deps{
		Vault: v, Engine: eng, Embedder: emb, GraphHealer: ingest.NewEngineHealer(eng),
		// ADR-016 U4 B2/B3: the drain is the guaranteed consumer of a pending
		// rebuild-corpus want. These seams keep the process-spawn and liveness
		// machinery out of internal/ingest (which imports neither cmd/vp's
		// detachlaunch nor syscall).
		RebuildSpawner: spawnRebuildFor(v),
		HolderAlive:    holderAlive,
	}, ingest.RunOptions{
		VaultRoot: vaultRoot, Project: project, First: first,
		// ADR-016 decision 6: the production drain opts in to the inter-run
		// cooldown; the zero default keeps it out of the fixture tests.
		Cooldown: indexstore.DefaultRunCooldown,
		// The disk/inode watchdog as the during-run check, so a background
		// run cannot fill the filesystem that holds palace/.local/ either.
		Checkpoint: ingest.ReserveCheckpoint(nil, v.VaultLocalDir(), ingest.Reserve{}),
	})
	if err != nil {
		slog.Warn("vp drain archives: the run stopped", "project", project, "error", err)
		return cli.ExitOK
	}
	if res.LockHeld {
		slog.Info("vp drain archives: another index run holds the lock; it serves this trigger", "project", project)
		return cli.ExitOK
	}
	slog.Info("vp drain archives: done", "project", project, "committed", res.Committed, "failed", res.Failed,
		"changed", res.Changed, "repaired", res.Repaired, "stopped", res.Stopped, "passes", res.Passes)
	return cli.ExitOK
}

// spawnRebuildFor returns the ingest.Deps.RebuildSpawner for this vault: it
// spawns a detached `vp index rebuild <project>` WITHOUT taking the run lock
// (ADR-016 decision 6, U4 B2). The drain calls it to serve a pending
// rebuild-corpus want — the only path that consumes one — once the cooldown is
// inactive and no live holder is building; the spawned child's own TryRunLock
// is the authority. A launch failure is logged and dropped: no lock was taken,
// so nothing is lost, and the durable want is served by a later trigger.
func spawnRebuildFor(v *storage.Vault) func(project string) {
	return func(project string) {
		args := []string{"index", "rebuild", project, "--vault-root", v.Root}
		logPath := filepath.Join(v.VaultLocalDir(), "rebuild.log")
		if _, err := detachlaunch.Launch("", args, logPath); err != nil {
			slog.Warn("vp drain archives: could not spawn the rebuild for a pending corpus want",
				"project", project, "error", err)
		}
	}
}

// holderAlive is the ingest.Deps.HolderAlive liveness check (ADR-016 U4 B3). It
// mirrors tools.pidAlive — signal 0, with a process owned by another user
// (EPERM) counted as alive — and lives here, in cmd/vp, so internal/ingest
// imports neither internal/tools nor syscall. The holder record is advisory and
// the spawned rebuild's own try-lock is authoritative, so a conservative answer
// only ever costs an extra harmless spawn.
func holderAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
