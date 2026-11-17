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
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// This file is the only place in the package that touches the filesystem for
// writing (TestWritesGoThroughThisFile). Everything written is the host-local
// derived index under palace/.local/, which git ignores (ADR-014 decision 2).
//
// # The under-test vault tripwire
//
// All of it lives INSIDE the vault, and none of it carries a vault root, so the
// vault-root arm of the tripwire (surface.GuardTestVaultWrite) never saw it. Each
// primitive here therefore refuses, as its FIRST statement, a path inside the
// operator's configured vault under `go test` (surface.GuardTestPathWrite), and
// every function that creates a directory or a lock file calls mkdirAll or
// guardAmbientIndex before its first effect. TestWritesGoThroughThisFile pins
// that no other file mutates the filesystem or takes a lock without that seat.
// A no-op in a production binary.

// guardAmbientIndex refuses, under `go test`, a vault whose index tree or lock
// directory lies inside the operator's configured vault. It is the FIRST
// statement of every function that takes an index lock, ahead of its MkdirAll
// and its flock: a panic after the flock is taken skips the manual release and
// would strand it, so the refusal must come while nothing is held.
//
// It judges BOTH directories because they are siblings under palace/.local, not
// parent and child: a lock acquirer creates its file in the locks directory, and
// that directory can resolve (a symlink) somewhere the index root does not.
func guardAmbientIndex(vault *storage.Vault) {
	surface.GuardTestPathWrite(vault.IndexRootDir())
	surface.GuardTestPathWrite(vault.IndexLocksDir())
}

// mkdirAll is os.MkdirAll for a directory in the index tree, behind the
// tripwire. It is the only MkdirAll in the package.
func mkdirAll(dir string) error {
	surface.GuardTestPathWrite(dir)
	return os.MkdirAll(dir, 0o755)
}

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
// record.
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
	surface.GuardTestPathWrite(path)
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
	surface.GuardTestPathWrite(path)
	dir := filepath.Dir(path)
	if err := mkdirAll(dir); err != nil {
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

// renameDir renames a whole directory in one rename(2), retrying the transient
// Windows sharing failures, then fsyncs the parent so the rename survives a
// crash. A lifecycle removal moves index/<p>/ to a tombstone this way, so a
// crash leaves the whole live store or the whole tombstone, never part of each.
func renameDir(from, to string) error {
	surface.GuardTestPathWrite(from)
	surface.GuardTestPathWrite(to)
	if err := atomicfile.RenameWithRetry(from, to); err != nil {
		return fmt.Errorf("indexstore: rename %s: %w", from, err)
	}
	if err := atomicfile.SyncDir(filepath.Dir(to)); err != nil {
		return fmt.Errorf("indexstore: fsync %s: %w", filepath.Dir(to), err)
	}
	return nil
}

// removeTree removes a tombstoned directory and everything under it, deepest
// first, one entry at a time through removeFile. It never follows a symlink: a
// link is removed, not what it points to. Only tombstones are removed this way;
// no project maps to one, so no lock guards it, and a removal a crash cut short
// is finished by the next pass.
func removeTree(dir string) error {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("indexstore: walk %s: %w", dir, err)
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := removeIfExists(paths[i]); err != nil {
			return fmt.Errorf("indexstore: remove %s: %w", paths[i], err)
		}
	}
	return nil
}
