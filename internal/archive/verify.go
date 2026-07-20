// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Entry is one archived transcript pair as seen on disk.
type Entry struct {
	ManifestPath string
	ArchivePath  string
	Manifest     *Manifest
}

// ListEntries enumerates all *.manifest.json files under the project's
// transcripts directory and pairs each with its archive file. Entries
// are returned sorted by CapturedAt (oldest first).
func ListEntries(vaultRoot, projectSlug string) ([]*Entry, error) {
	dir := filepath.Join(vaultRoot, "Projects", projectSlug, "transcripts")
	// filepath.Glob would swallow a directory read error -- EACCES, a vanished
	// mount -- and return nil,nil, reporting "I could not look" as "there was
	// nothing there". os.ReadDir surfaces that read error instead. A genuinely
	// absent transcripts directory (the common no-transcripts-yet project) is
	// NOT an error: it is an empty listing, matching Glob's old behaviour for
	// that case and keeping `vp archive list` non-erroring for every project
	// that has never archived a transcript.
	dirents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Entry{}, nil
		}
		return nil, fmt.Errorf("read transcripts dir: %w", err)
	}

	entries := make([]*Entry, 0, len(dirents))
	for _, de := range dirents {
		name := de.Name()
		if !strings.HasSuffix(name, ".manifest.json") {
			continue
		}
		mp := filepath.Join(dir, name)
		m, err := ReadManifest(mp)
		if err != nil {
			// Skip an unreadable individual manifest -- one bad file must not
			// poison the whole list. Directory-level blindness is fatal above;
			// a single unreadable manifest is tolerated here.
			continue
		}
		ap := strings.TrimSuffix(mp, ".manifest.json") + ".jsonl.zst"
		entries = append(entries, &Entry{ManifestPath: mp, ArchivePath: ap, Manifest: m})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Manifest.CapturedAt < entries[j].Manifest.CapturedAt
	})
	return entries, nil
}

// VerifyResult reports the outcome of a verification pass.
type VerifyResult struct {
	Entry           *Entry
	OK              bool
	RecomputedSHA   string
	CompressedBytes int64
	// SignatureChecked is true when a .sig was present and
	// VerifyOptions supplied enough to validate it. SignatureOK
	// reports the outcome of that check. If SignatureChecked is
	// false, signature status is unknown (missing or unverifiable).
	SignatureChecked bool
	SignatureOK      bool
	// Problems is empty on success. Each entry describes a discrete
	// mismatch or missing-file condition.
	Problems []string
}

// ErrArchiveMissing is returned by Verify when the .jsonl.zst file is
// absent alongside an otherwise-readable manifest.
var ErrArchiveMissing = errors.New("archive file missing")

// Verify decompresses the archive, recomputes SHA256 over the
// decompressed bytes, and compares against the manifest's
// source_sha256 (and compressed_bytes on-disk size). The manifest's
// hash must match bit-for-bit: ADR-001 chose pre-compression hashing
// precisely so re-compression with a different zstd level or frame
// format cannot break verification.
//
// VerifyWithOptions also validates the manifest signature when one is
// present and VerifyOptions supply enough to anchor it. The zero-value
// options yield hash-only verification.
func VerifyWithOptions(e *Entry, vo VerifyOptions) *VerifyResult {
	res := &VerifyResult{Entry: e}

	st, err := os.Stat(e.ArchivePath)
	if err != nil {
		res.Problems = append(res.Problems,
			fmt.Sprintf("archive file unreadable: %v", err))
		return res
	}
	res.CompressedBytes = st.Size()
	if e.Manifest.CompressedBytes > 0 && st.Size() != e.Manifest.CompressedBytes {
		res.Problems = append(res.Problems,
			fmt.Sprintf("compressed_bytes mismatch: manifest=%d on-disk=%d",
				e.Manifest.CompressedBytes, st.Size()))
	}

	sum, err := hashArchive(e.ArchivePath)
	if err != nil {
		res.Problems = append(res.Problems,
			fmt.Sprintf("decompress+hash: %v", err))
		return res
	}
	res.RecomputedSHA = sum
	if sum != e.Manifest.SourceSHA256 {
		res.Problems = append(res.Problems,
			fmt.Sprintf("source_sha256 mismatch: manifest=%s recomputed=%s",
				e.Manifest.SourceSHA256, sum))
	}

	// Signature: verify if present and we have the means. A missing
	// .sig is silently tolerated (signing is opt-in). A present .sig
	// that fails cryptographic verification is always a hard problem.
	sigPath := SignaturePath(e.ManifestPath)
	if _, sigErr := os.Stat(sigPath); sigErr == nil {
		if vo.AllowedSigners != "" && vo.Identity != "" {
			res.SignatureChecked = true
			if err := VerifySignature(e.ManifestPath, vo); err != nil {
				res.Problems = append(res.Problems,
					fmt.Sprintf("signature: %v", err))
			} else {
				res.SignatureOK = true
			}
		}
		// No AllowedSigners/Identity -> we have a sig but can't vet
		// it. Don't fail the archive for it; just report as unchecked.
	}

	res.OK = len(res.Problems) == 0
	return res
}

// hashArchive decompresses an archive and returns the SHA256 of the
// decompressed byte stream.
func hashArchive(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("init zstd reader: %w", err)
	}
	defer dec.Close()
	h := sha256.New()
	if _, err := io.Copy(h, dec); err != nil {
		return "", fmt.Errorf("stream: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Extract decompresses an archive to w. The decompressed stream is the
// original transcript bytes and matches source_sha256 on a successful
// Verify.
func Extract(archivePath string, w io.Writer) (int64, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return 0, fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("init zstd reader: %w", err)
	}
	defer dec.Close()
	n, err := io.Copy(w, dec)
	if err != nil {
		return n, fmt.Errorf("stream: %w", err)
	}
	return n, nil
}

// ResolveEntry finds an Entry by path or session ID within the given
// project. A path is recognized if the argument exists on disk or ends
// with a known archive suffix; otherwise the argument is treated as a
// session ID and matched against manifests under the project.
// If multiple archives share the session ID (re-archives across days),
// the most recent CapturedAt wins.
func ResolveEntry(vaultRoot, projectSlug, arg string) (*Entry, error) {
	if arg == "" {
		return nil, fmt.Errorf("path or session id required")
	}

	// Path form: argument looks file-like and resolves to a manifest or archive.
	if strings.ContainsRune(arg, filepath.Separator) ||
		strings.HasSuffix(arg, ".manifest.json") ||
		strings.HasSuffix(arg, ".jsonl.zst") {
		mp, ap := companionPaths(arg)
		if _, err := os.Stat(mp); err != nil {
			return nil, fmt.Errorf("manifest not found: %s", mp)
		}
		m, err := ReadManifest(mp)
		if err != nil {
			return nil, err
		}
		return &Entry{ManifestPath: mp, ArchivePath: ap, Manifest: m}, nil
	}

	// Session-ID form: scan the project's transcripts.
	entries, err := ListEntries(vaultRoot, projectSlug)
	if err != nil {
		return nil, err
	}
	var matched []*Entry
	for _, e := range entries {
		if e.Manifest.SessionID == arg {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("no archive found for session id %q in project %q", arg, projectSlug)
	}
	// ListEntries is ascending; pick the newest.
	return matched[len(matched)-1], nil
}

// companionPaths derives the (manifest, archive) pair from either half
// of the pair, passed as an absolute or relative path.
func companionPaths(p string) (manifest, archive string) {
	switch {
	case strings.HasSuffix(p, ".manifest.json"):
		base := strings.TrimSuffix(p, ".manifest.json")
		return p, base + ".jsonl.zst"
	case strings.HasSuffix(p, ".jsonl.zst"):
		base := strings.TrimSuffix(p, ".jsonl.zst")
		return base + ".manifest.json", p
	default:
		// Bare base path; add both suffixes.
		return p + ".manifest.json", p + ".jsonl.zst"
	}
}
