// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// incomingBaselineLock is the one index lock the baseline add takes, the
// project's commit lock; a test seam counts it.
var incomingBaselineLock = indexstore.Lock

// incomingBaselineTimeout bounds the wait for one project's index commit lock
// during the baseline add; a test sets it to zero to inject a timeout.
var incomingBaselineTimeout = 10 * time.Second

// AddIncomingArchivesToBaseline adds the archives a copy or a merge brought in
// to THIS host's baseline set for each slug (ADR-014 decision 2, "Additions";
// task split-and-merge-exclude-derived-palace-paths, Scope 5).
//
// A host's baseline set is the historical backlog its automatic ingest skips.
// Copy and merge bring in whole project histories, and on a host that already
// has a ledger for the slug, those archives are not in its set: its automatic
// runs would start embedding the whole incoming history, a budget at a time.
// So the source_sha256 of every transcript archive under Projects/<slug>/
// transcripts/ goes into the set, under that project's index commit lock
// (indexstore.Lock, then Tx.AddToBaseline, then Commit; no run lock).
// AddToBaseline itself does nothing, and creates nothing, on a host with no
// ledger for the slug: a ledger created later records those archives anyway.
// Other hosts are not touched; they receive the archives by pull.
//
// It never fails the command: the add is host-local. Each slug it could not
// add to is returned as a warning naming the slug and `vp index rebuild`, and
// logged. Call it only after the command's own work is final (copy: after the
// exact publish; merge: after the apply's copy loop), so a rolled-back copy
// adds nothing. Between that and the add, an automatic run on this host may
// start ingesting the incoming archives within its budget; that window is
// accepted (ADR-014 places the add after the publish).
func AddIncomingArchivesToBaseline(ctx context.Context, vault *storage.Vault, slugs []string) []string {
	var warnings []string
	for _, s := range slugs {
		err := addIncomingToBaseline(ctx, vault, s)
		if err == nil {
			continue
		}
		w := fmt.Sprintf("project %s: the incoming archives were not added to this host's baseline set (%v); "+
			"they will be ingested automatically within the per-run budget unless `vp index rebuild %s` runs first",
			s, err, s)
		slog.Warn("baseline add for incoming archives failed", "project", s, "err", err)
		warnings = append(warnings, w)
	}
	return warnings
}

func addIncomingToBaseline(ctx context.Context, vault *storage.Vault, slug string) error {
	shas, err := incomingArchiveSHAs(vault, slug)
	if err != nil || len(shas) == 0 {
		return err
	}
	tx, err := incomingBaselineLock(ctx, vault, slug, incomingBaselineTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Release() }()
	if err := tx.AddToBaseline(shas); err != nil {
		return err
	}
	return tx.Commit()
}

// incomingArchiveSHAs reads the source_sha256 of every transcript archive
// manifest under Projects/<slug>/transcripts/ in the vault.
func incomingArchiveSHAs(vault *storage.Vault, slug string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(vault.Root, "Projects", slug, "transcripts", "*.manifest.json"))
	if err != nil {
		return nil, err
	}
	shas := make([]string, 0, len(paths))
	for _, p := range paths {
		m, err := archive.ReadManifest(p)
		if err != nil {
			return nil, err
		}
		if m.SourceSHA256 != "" {
			shas = append(shas, m.SourceSHA256)
		}
	}
	return shas, nil
}
