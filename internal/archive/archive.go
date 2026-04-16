// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// CreateOptions drives a single archive operation. All fields except
// SessionID and Adapter are optional; the adapter fills in what it
// can from its own environment.
type CreateOptions struct {
	// Adapter identifies the source format. Only "claude-code" is
	// supported today; others are planned.
	Adapter string

	// SessionID uniquely identifies the session within the adapter's
	// namespace. Used as both the input lookup key and the output
	// filename component.
	SessionID string

	// SourcePath is the absolute path to the raw transcript (e.g.,
	// the Claude Code JSONL). If empty, the Claude Code adapter
	// resolves it via SourceCWD + SessionID.
	SourcePath string

	// SourceCWD is the working directory used to resolve SourcePath
	// when the adapter supports per-project path encoding (Claude
	// Code). Defaults to the current process cwd.
	SourceCWD string

	// VaultRoot is the absolute path to the vault root. Archive
	// output is written under
	// {VaultRoot}/Projects/{ProjectSlug}/transcripts/.
	VaultRoot string

	// ProjectSlug identifies the destination project directory.
	ProjectSlug string

	// VaultRelSessionNote, if set, is recorded in the manifest so
	// downstream tools can navigate from archive -> session note.
	VaultRelSessionNote string

	// VPVersion is the vp binary version string; recorded in the
	// manifest for schema forensics.
	VPVersion string

	// Now is the capture-time clock. Injectable for tests; defaults
	// to time.Now() in UTC when zero.
	Now time.Time

	// Sign, when Enabled, triggers a detached signature over the
	// manifest after it is written. Failures here are returned to
	// the caller — an unsigned archive is usable but misses the
	// anchoring guarantee, so silently skipping would be surprising.
	Sign SignOptions
}

// CreateResult reports what an archive operation produced or found.
type CreateResult struct {
	// ManifestPath is the absolute path to the written (or pre-existing) manifest.
	ManifestPath string

	// ArchivePath is the absolute path to the compressed transcript.
	ArchivePath string

	// Manifest is the manifest content that is now authoritative on disk.
	Manifest *Manifest

	// Skipped is true when an existing manifest matched the current
	// source hash and no work was performed.
	Skipped bool
}

// Create produces a <session>.jsonl.zst + <session>.manifest.json pair
// under the project's transcripts directory. Behavior follows ADR-001:
//
//   - Hashes the pre-compression JSONL bytes; that hash is the
//     evidentiary anchor embedded in the manifest.
//   - Idempotent on (session_id, adapter, source_sha256): if a manifest
//     at the target path already records the same source_sha256, the
//     function returns Skipped=true without rewriting anything.
//   - If the source hash has changed, the existing manifest is
//     preserved as <name>.manifest.json.<prev-hash>.bak before the
//     new pair is written. No prior ledger entry is destroyed.
func Create(opts CreateOptions) (*CreateResult, error) {
	if opts.SessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if opts.Adapter == "" {
		opts.Adapter = ClaudeCodeAdapterName
	}
	if opts.VaultRoot == "" {
		return nil, fmt.Errorf("vault root is required")
	}
	if opts.ProjectSlug == "" {
		return nil, fmt.Errorf("project slug is required")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}

	a, err := LookupAdapter(opts.Adapter)
	if err != nil {
		return nil, err
	}
	sourcePath, cleanupSource, err := a.ResolveSource(opts)
	if err != nil {
		return nil, err
	}
	defer cleanupSource()

	// Hash + size the source up-front so idempotency decisions work
	// whether or not a prior archive exists.
	srcSum, srcBytes, err := hashFile(sourcePath)
	if err != nil {
		return nil, err
	}

	transcriptsDir := filepath.Join(opts.VaultRoot, "Projects", opts.ProjectSlug, "transcripts")
	if err := os.MkdirAll(transcriptsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create transcripts dir: %w", err)
	}

	base := fmt.Sprintf("%s-%s", opts.Now.Format("2006-01-02"), opts.SessionID)
	archivePath := filepath.Join(transcriptsDir, base+".jsonl.zst")
	manifestPath := filepath.Join(transcriptsDir, base+".manifest.json")

	// Idempotency check: same (session_id, adapter, source hash)?
	if existing, err := ReadManifest(manifestPath); err == nil {
		if existing.Adapter == opts.Adapter &&
			existing.SessionID == opts.SessionID &&
			existing.SourceSHA256 == srcSum {
			return &CreateResult{
				ManifestPath: manifestPath,
				ArchivePath:  archivePath,
				Manifest:     existing,
				Skipped:      true,
			}, nil
		}
		// Source has changed. Preserve the prior manifest as a .bak
		// before overwriting. See ADR-001 (idempotency section).
		bakPath := fmt.Sprintf("%s.%s.bak", manifestPath, shortHash(existing.SourceSHA256))
		if err := os.Rename(manifestPath, bakPath); err != nil {
			return nil, fmt.Errorf("preserve prior manifest: %w", err)
		}
	}

	// Compress the source into place. Write to a tmp path then rename
	// so a crash can't leave a torn .jsonl.zst next to the manifest.
	compressedBytes, err := compressFile(sourcePath, archivePath)
	if err != nil {
		return nil, err
	}

	// Inspection failures are non-fatal — the source hash still pins
	// the exact bytes. Turn count and model are convenience fields.
	turnCount, model, _ := a.Inspect(sourcePath)

	hostname, _ := os.Hostname()

	m := &Manifest{
		SchemaVersion:       ManifestSchemaVersion,
		Adapter:             opts.Adapter,
		AdapterVersion:      a.Version(),
		SessionID:           opts.SessionID,
		Model:               model,
		TurnCount:           turnCount,
		SourceSHA256:        srcSum,
		SourceBytes:         srcBytes,
		CompressedBytes:     compressedBytes,
		GitHead:             gitHead(opts.SourceCWD),
		ProjectSlug:         opts.ProjectSlug,
		VaultRelSessionNote: opts.VaultRelSessionNote,
		CapturedAt:          opts.Now.Format(time.RFC3339),
		CapturedByHostname:  hostname,
		VPVersion:           opts.VPVersion,
	}
	if err := WriteManifest(manifestPath, m); err != nil {
		return nil, err
	}

	if opts.Sign.Enabled() {
		if err := Sign(manifestPath, opts.Sign); err != nil {
			return nil, fmt.Errorf("sign manifest: %w", err)
		}
	}

	return &CreateResult{
		ManifestPath: manifestPath,
		ArchivePath:  archivePath,
		Manifest:     m,
	}, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash source: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func compressFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("create archive tmp: %w", err)
	}

	enc, err := zstd.NewWriter(out)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("init zstd: %w", err)
	}
	if _, err := io.Copy(enc, in); err != nil {
		enc.Close()
		out.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("compress: %w", err)
	}
	if err := enc.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("close zstd writer: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("close archive tmp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("commit archive: %w", err)
	}

	st, err := os.Stat(dst)
	if err != nil {
		return 0, fmt.Errorf("stat archive: %w", err)
	}
	return st.Size(), nil
}

// gitHead returns the short HEAD SHA at cwd, or empty string on any
// failure (not in a repo, git missing, detached, etc.).
func gitHead(cwd string) string {
	if cwd == "" {
		c, err := os.Getwd()
		if err != nil {
			return ""
		}
		cwd = c
	}
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// shortHash returns the first 12 hex chars of a full SHA256 string,
// suitable for disambiguating .bak filenames without spraying full
// 64-char hashes across the filesystem.
func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
