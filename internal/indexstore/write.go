// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import "github.com/suykerbuyk/vibe-palace/internal/atomicfile"

// writeFile replaces path with data durably: temp file, fsync, rename (retried
// on the transient Windows sharing failures a concurrent lock-free reader
// causes), then fsync of the directory. It goes through internal/atomicfile,
// the one whole-file write primitive. vaultRoot is "" on purpose: everything
// this package writes is the host-local derived index under palace/.local/,
// which git ignores (ADR-014 decision 2), so no .surface stamp is written.
//
// Known limit: a directory atomicfile creates on the way (palace/.local/locks/,
// index/.generation/) is not itself fsynced into its parent, and a temp file a
// crash leaves behind (.vp-atomic-*) is never swept. Neither loses a committed
// record; the sweep of index/ belongs to
// index-fingerprints-project-lifecycle-and-migration-marker.
func writeFile(path string, data []byte) error {
	return atomicfile.Write("", path, data, atomicfile.WithFsync(), atomicfile.WithDirFsync())
}

// removeFile removes path, retrying the transient Windows sharing failures a
// concurrent lock-free reader causes.
func removeFile(path string) error {
	return atomicfile.RemoveWithRetry(path)
}
