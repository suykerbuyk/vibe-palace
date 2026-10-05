// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ErrArchiveChanged: the archive's manifest on disk is no longer the one the
// caller listed. The hook rewrote a same-day archive (Create renames the old
// manifest to a .bak, then rewrites the pair) between the listing and the
// read. It is not a fault of the archive: the caller retries on its next run.
var ErrArchiveChanged = errors.New("archive: rewritten since it was listed")

// ErrArchiveCorrupt: the bytes on disk do not match the manifest that
// describes them, and that manifest is still the listed one.
var ErrArchiveCorrupt = errors.New("archive: bytes do not match the manifest")

// readArchiveFile is the one read of the compressed file; a seam so a test
// can count the opens.
var readArchiveFile = os.ReadFile

// VerifiedArchive is an archive's decompressed bytes and the key they are
// known by.
type VerifiedArchive struct {
	Bytes []byte
	// SHA is the manifest's source_sha256, which the bytes were checked
	// against; or, for a manifest with no source_sha256, the sha256 of the
	// bytes (Fallback).
	SHA      string
	Fallback bool
}

// ReadVerified reads a listed archive whole and checks it against its
// manifest (ADR-014 decision 7, "Reading an archive"):
//
//  1. the manifest is re-read; one that differs from e.Manifest (or is gone)
//     is ErrArchiveChanged;
//  2. the compressed file is read with one os.ReadFile, so it is closed before
//     anything else runs: the hook can rename a newer archive into place on
//     any OS, and no one version's bytes are read under another's hash;
//  3. its length is checked against compressed_bytes, it is decompressed in
//     memory, and the result is checked against source_bytes and
//     source_sha256.
//
// A mismatch in step 3 re-reads the manifest once more: a manifest that
// changed meanwhile is ErrArchiveChanged, one that did not is
// ErrArchiveCorrupt. A manifest with no source_sha256 is checked by length
// only, and the bytes are keyed by their own sha256.
func ReadVerified(e *Entry) (VerifiedArchive, error) {
	if e == nil || e.Manifest == nil {
		return VerifiedArchive{}, errors.New("archive: ReadVerified needs a listed entry with its manifest")
	}
	if err := sameManifest(e); err != nil {
		return VerifiedArchive{}, err
	}
	data, err := readArchiveFile(e.ArchivePath)
	if err != nil {
		// A file gone since the listing (renamed away mid-rewrite, the new
		// manifest not yet written) is a change, retried next run, as is any
		// read error while the manifest changed.
		if errors.Is(err, fs.ErrNotExist) || sameManifest(e) != nil {
			return VerifiedArchive{}, fmt.Errorf("%w: %s", ErrArchiveChanged, e.ArchivePath)
		}
		return VerifiedArchive{}, fmt.Errorf("archive: read %s: %w", e.ArchivePath, err)
	}
	m := e.Manifest
	mismatch := func(format string, args ...any) (VerifiedArchive, error) {
		if err := sameManifest(e); err != nil {
			return VerifiedArchive{}, err
		}
		return VerifiedArchive{}, fmt.Errorf("%w: %s: %s", ErrArchiveCorrupt, e.ArchivePath, fmt.Sprintf(format, args...))
	}
	if m.CompressedBytes > 0 && int64(len(data)) != m.CompressedBytes {
		return mismatch("compressed_bytes: manifest %d, file %d", m.CompressedBytes, len(data))
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return VerifiedArchive{}, fmt.Errorf("archive: init zstd: %w", err)
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(data, nil)
	if err != nil {
		return mismatch("decompress: %v", err)
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if m.SourceSHA256 == "" {
		return VerifiedArchive{Bytes: raw, SHA: got, Fallback: true}, nil
	}
	if m.SourceBytes > 0 && int64(len(raw)) != m.SourceBytes {
		return mismatch("source_bytes: manifest %d, decompressed %d", m.SourceBytes, len(raw))
	}
	if got != m.SourceSHA256 {
		return mismatch("source_sha256: manifest %s, decompressed %s", m.SourceSHA256, got)
	}
	return VerifiedArchive{Bytes: raw, SHA: got}, nil
}

// sameManifest re-reads e's manifest and reports ErrArchiveChanged when it is
// gone or differs from the listed one in what identifies the bytes.
func sameManifest(e *Entry) error {
	m, err := ReadManifest(e.ManifestPath)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrArchiveChanged, e.ManifestPath, err)
	}
	if m.SourceSHA256 != e.Manifest.SourceSHA256 || m.CompressedBytes != e.Manifest.CompressedBytes ||
		m.SourceBytes != e.Manifest.SourceBytes || m.CapturedAt != e.Manifest.CapturedAt {
		return fmt.Errorf("%w: %s", ErrArchiveChanged, e.ManifestPath)
	}
	return nil
}
