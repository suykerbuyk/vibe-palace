// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"log/slog"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// LaunchArchiveIngester spawns the DETACHED pending-archive drain for the
// archive a host hook has just created, and returns immediately. The hook never
// embeds the ingest: it only spawns and returns (ADR-014 decision 7).
//
// 🔴 firstSHA is a source_sha256, NEVER a path. `vp drain archives` validates
// --first and, when it is not a hash, logs "--first is not a source_sha256;
// ignored" and drains UNORDERED (cmd/vp/cmd_drain_archives.go). That is a
// silent downgrade — the command still succeeds — so passing a manifest path
// here produces no error anywhere and only shows up as a drain that did not
// prioritise the archive that triggered it. The hash makes a ledger this
// trigger creates leave its own archive out of the baseline set.
//
// A nil launch spawns nothing, which is the library/test default: a hook
// library test must never re-exec the test binary. See
// detachlaunch.ErrRefusedTestBinary for why that is a fork bomb and not merely
// untidy.
//
// Non-fatal in every branch. The archive and its receipt are already durable by
// the time this runs; a failure to START the indexer must not turn a successful
// capture into a failed hook, so the only report is one Warn.
func LaunchArchiveIngester(launch detachlaunch.LaunchFunc, vaultRoot, projectSlug, firstSHA string) {
	if launch == nil {
		return
	}
	args := []string{"drain", "archives", "--vault-root", vaultRoot, "--project", projectSlug, "--first", firstSHA}
	logPath := filepath.Join(storage.NewVault(vaultRoot).VaultLocalDir(), "ingester.log")
	if _, err := launch("", args, logPath); err != nil {
		slog.Warn("hook: could not launch the archive ingester (non-fatal)",
			"project", projectSlug, "error", err)
	}
}
