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

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// CreateOptions drives a single archive operation. All fields except
// SessionID and Adapter are optional; the adapter fills in what it
// can from its own environment.
type CreateOptions struct {
	// Adapter identifies the source format. It must name a registered
	// adapter; RegisteredAdapterNames() is the live set — claude-code,
	// zed, and inline today. Required.
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

	// SourceContent is the transcript bytes supplied directly by the
	// caller, for adapters that synthesize their source from content
	// rather than resolving a file (the "inline" adapter). Ignored by
	// adapters that resolve an on-disk source.
	SourceContent []byte

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

	// Now is the capture-time instant. Injectable for tests; defaults
	// to time.Now() when zero. Calendar day for the archive stem is derived
	// via storage.Vault.CalendarDay; CapturedAt is the instant in UTC RFC3339.
	Now time.Time

	// Sign, when Enabled, triggers a detached signature over the
	// manifest after it is written. Failures here are returned to
	// the caller — an unsigned archive is usable but misses the
	// anchoring guarantee, so silently skipping would be surprising.
	Sign SignOptions

	// LockPosture selects how Create serializes on the MANIFEST PATH against a
	// concurrent writer of the same manifest (ADR-003; see lock.go).
	//
	// The ZERO VALUE is LockBlocking, and that is deliberate: every caller that
	// predates this locking is fail-stop and can afford to wait, so leaving the
	// field unset preserves their semantics exactly.
	//
	// 🔴 LockBlocking is a bare LOCK_EX with NO TIMEOUT. Set LockNonBlocking
	// when — and only when — the caller has no timeout and no cancellation of
	// its own, so that contention cannot hang it forever. Today that is
	// internal/hook: cmdHook is registered UNWRAPPED and cmd_hook.go passes
	// context.Background(), so a blocked acquire would wedge SessionEnd with no
	// error and no log. A future caller that copies the blocking default onto
	// such a path reintroduces exactly that hang.
	LockPosture LockPosture
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
		opts.Now = time.Now()
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

	day := storage.NewVault(opts.VaultRoot).CalendarDay(opts.Now)
	base := fmt.Sprintf("%s-%s", day, opts.SessionID)
	archivePath := filepath.Join(transcriptsDir, base+".jsonl.zst")
	manifestPath := filepath.Join(transcriptsDir, base+".manifest.json")

	// SERIALIZE THE READ-THEN-ACT WINDOW (ADR-003). Everything from the
	// ReadManifest below through WriteManifest is one read-modify-write of
	// manifestPath, and two writers racing it with a changed source did real
	// damage: both computed the SAME .bak name from the same prior hash, so the
	// second silently replaced the first's preservation — and a racer that read
	// AFTER the first's rename found no manifest at all, took no .bak arm, and
	// overwrote the record preserving nothing.
	//
	// The lock is keyed on the manifest path and taken HERE, not inside
	// WriteManifest: that primitive is shared with LinkSessionNote and with
	// vaultaudit's sequential-never-nested backfill sequence, and a
	// non-reentrant, timeout-free LOCK_EX inside it would convert a documented
	// ordering into a live lock-order constraint.
	//
	// Deliberately AFTER the MkdirAll above: directory creation is not part of
	// the window, and locking it would only widen the hold.
	//
	// Held through Sign as well as WriteManifest, so a detached signature is
	// never computed over a manifest another writer is mid-replacing.
	release, lerr := lockManifest(opts.VaultRoot, manifestPath, opts.LockPosture)
	if lerr != nil {
		return nil, lerr
	}
	defer release()

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
		//
		// Through the F2 sink: this is a RENAME of an existing vault file, not
		// a content write, so it belongs to the removal/rename family and not
		// to atomicfile. The Option E census originally filed archive.Create
		// under F1 on the strength of the MkdirAll two dozen lines up; the
		// MkdirAll is F3 and lands in a later phase, and this line is F2.
		//
		// SERIALIZED as of the per-manifest lock taken above (ADR-003). This
		// rename used to be unserialized against a concurrent writer of the same
		// manifest — recorded here rather than repaired, because a concurrency
		// change did not belong in a routing phase. It has since been ruled on:
		// Create holds the manifest's vaultlock across the whole
		// ReadManifest -> compare -> rename -> write window, so exactly one
		// writer can reach this arm per source change.
		//
		// The SINK still takes no lock, and that is unchanged and load-bearing
		// (see internal/vaultfs/raw.go). The exclusion lives in this caller.
		bakPath := fmt.Sprintf("%s.%s.bak", manifestPath, shortHash(existing.SourceSHA256))
		if err := vaultfs.RenameNoLock(manifestPath, bakPath); err != nil {
			return nil, fmt.Errorf("preserve prior manifest: %w", err)
		}
	}

	// Compress the source into place. Write to a tmp path then rename
	// so a crash can't leave a torn .jsonl.zst next to the manifest.
	compressedBytes, err := compressFile(opts.VaultRoot, sourcePath, archivePath)
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
		CapturedAt:          opts.Now.UTC().Format(time.RFC3339),
		CapturedByHostname:  hostname,
		VPVersion:           opts.VPVersion,
	}
	if err := WriteManifest(opts.VaultRoot, manifestPath, m); err != nil {
		return nil, err
	}

	if opts.Sign.Enabled() {
		if err := Sign(manifestPath, opts.Sign); err != nil {
			return nil, fmt.Errorf("sign manifest: %w", err)
		}
	}

	// No stamp call here any more. Both writes above — the .jsonl.zst through
	// atomicfile.WriteStream and the manifest through atomicfile.Write — stamp
	// structurally, and they resolve to the same Projects/<slug> stamp dir that
	// the one hand-written call used to cover. Stamping is now a property of
	// having written, not a line someone remembered to add after.
	//
	// One behaviour this improves rather than preserves: a Sign failure returns
	// above, and used to return BEFORE the stamp, leaving a written manifest
	// unstamped. It is stamped now, because the write stamped it.
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

// compressFile zstd-compresses src into dst and returns the compressed size.
//
// The temp-plus-rename it used to hand-roll now belongs to
// atomicfile.WriteStream, which also stamps the surface. The streaming shape is
// the whole reason that primitive exists: a transcript is the largest thing the
// vault holds and must not be buffered whole to be written.
//
// What is deliberately unchanged: the zstd encoder and its defaults, the
// io.Copy, and the absence of an fsync. The temp file's NAME did change — it is
// the primitive's ".vp-atomic-*" in the destination directory rather than
// dst+".tmp" — which is inherent in the primitive owning the rename, and
// nothing reads that name.
func compressFile(vaultRoot, src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	if err := atomicfile.WriteStream(vaultRoot, dst, func(w io.Writer) error {
		enc, err := zstd.NewWriter(w)
		if err != nil {
			return fmt.Errorf("init zstd: %w", err)
		}
		if _, err := io.Copy(enc, in); err != nil {
			enc.Close()
			return fmt.Errorf("compress: %w", err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("close zstd writer: %w", err)
		}
		return nil
	}); err != nil {
		return 0, err
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
