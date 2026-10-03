// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

// This file is the only place in the package that touches the filesystem for
// writing (TestWritesGoThroughThisFile). Everything written is the host-local
// derived index under palace/.local/, which git ignores (ADR-014 decision 2).

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
	return writeFileFn(path, data)
}

// writeFileFn is writeFile's body, a seam so a test can fail a whole-file
// write and check that the previous file survives.
var writeFileFn = func(path string, data []byte) error {
	return atomicfile.Write("", path, data, atomicfile.WithFsync(), atomicfile.WithDirFsync())
}

// removeFile removes path, retrying the transient Windows sharing failures a
// concurrent lock-free reader causes.
func removeFile(path string) error {
	return atomicfile.RemoveWithRetry(path)
}

// removeIfExists is removeFile with a missing file not an error.
func removeIfExists(path string) error {
	if err := removeFile(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// fsyncFile is the fsync of an appended file, a seam so a test can see that
// every append is synced.
var fsyncFile = func(f *os.File) error { return f.Sync() }

// appendFile appends data to path and fsyncs it before returning. When the
// append created the file, the directory is fsynced too, so the new file
// survives a crash. The directory is created if needed.
//
// An append is not atomic: a crash can leave a torn final line. Readers skip
// it, and the next append under the commit lock cuts it off first
// (truncateTornTail), so a torn line is never mistaken for a record.
func appendFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("indexstore: create %s: %w", dir, err)
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("indexstore: open %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("indexstore: append to %s: %w", path, err)
	}
	if err := fsyncFile(f); err != nil {
		f.Close()
		return fmt.Errorf("indexstore: fsync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("indexstore: close %s: %w", path, err)
	}
	if created {
		if err := atomicfile.SyncDir(dir); err != nil {
			return fmt.Errorf("indexstore: fsync %s: %w", dir, err)
		}
	}
	return nil
}
