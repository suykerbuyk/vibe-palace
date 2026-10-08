// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// incompressibleBytes returns n bytes of crypto/rand data. zstd cannot shrink
// random bytes, so the compressed archive is ~n — big enough to trip a tiny
// MaxArchiveBytes ceiling without a multi-MiB fixture.
func incompressibleBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

// transcriptsDirEntries lists the names under Projects/<slug>/transcripts, or
// an empty slice when the directory does not exist.
func transcriptsDirEntries(t *testing.T, vault, slug string) []string {
	t.Helper()
	dir := filepath.Join(vault, "Projects", slug, "transcripts")
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read transcripts dir: %v", err)
	}
	out := make([]string, 0, len(des))
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

// TestCreate_OverCeilingRefusesAndWritesNothing is the trip test: an
// over-ceiling archive must refuse with ErrArchiveTooLarge and leave NOTHING in
// the vault — no .jsonl.zst, no .manifest.json, and no leaked atomicfile temp.
func TestCreate_OverCeilingRefusesAndWritesNothing(t *testing.T) {
	vault := t.TempDir()
	testutil.InitProject(t, vault, "p")

	_, err := Create(CreateOptions{
		Adapter:         InlineAdapterName,
		SessionID:       "oversize",
		SourceContent:   incompressibleBytes(t, 4096),
		VaultRoot:       vault,
		ProjectSlug:     "p",
		Now:             time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		MaxArchiveBytes: 1024,
	})
	if err == nil {
		t.Fatalf("Create over the ceiling returned nil error, want ErrArchiveTooLarge")
	}
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("errors.Is(err, ErrArchiveTooLarge) = false; err = %v", err)
	}

	// Nothing may be materialized in the transcripts directory: no committed
	// blob, no manifest, and no leaked .vp-atomic-* temp (the atomicfile
	// primitive removes its temp on a fill error).
	for _, name := range transcriptsDirEntries(t, vault, "p") {
		switch {
		case strings.HasSuffix(name, ".jsonl.zst"):
			t.Errorf("refused archive left a blob: %s", name)
		case strings.HasSuffix(name, ".manifest.json"):
			t.Errorf("refused archive left a manifest: %s", name)
		case strings.HasPrefix(name, ".vp-atomic-"):
			t.Errorf("refused archive leaked a temp: %s", name)
		default:
			t.Errorf("refused archive left an unexpected file: %s", name)
		}
	}
}

// TestCreate_OverCeilingRefusesMidStream pins the PRODUCTION trip path.
//
// The sibling ~4 KiB trip test buffers entirely under zstd's ~128 KiB default
// block, so its cap fires only at the final enc.Close() flush
// ("close zstd writer: ..."). A real >18 MiB transcript is different: the cap
// trips MID-STREAM, inside the io.Copy/ReadFrom that feeds the encoder
// ("compress: ..."). This test exercises that other branch, which the
// Close-branch test cannot reach.
//
// 512 KiB of incompressible bytes is chosen deliberately: it is several times
// zstd's ~128 KiB default block, so multiple full blocks are flushed to the
// capWriter DURING io.Copy and the 256 KiB ceiling is crossed there — well
// before EOF and the final Close.
func TestCreate_OverCeilingRefusesMidStream(t *testing.T) {
	vault := t.TempDir()
	testutil.InitProject(t, vault, "p")

	_, err := Create(CreateOptions{
		Adapter:         InlineAdapterName,
		SessionID:       "oversize-midstream",
		SourceContent:   incompressibleBytes(t, 512*1024),
		VaultRoot:       vault,
		ProjectSlug:     "p",
		Now:             time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		MaxArchiveBytes: 256 * 1024,
	})
	if err == nil {
		t.Fatalf("Create over the ceiling returned nil error, want ErrArchiveTooLarge")
	}
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("errors.Is(err, ErrArchiveTooLarge) = false; err = %v", err)
	}

	// 🔴 PIN THE BRANCH. compressFile wraps the io.Copy error as "compress: %w"
	// and the final-flush error as "close zstd writer: %w". Asserting the wrap is
	// "compress:" and NOT "close zstd writer:" proves THIS test rides the
	// io.Copy/ReadFrom branch — the one a real >18 MiB transcript hits. The
	// coupling to the wrap prefix is intentional: if a future zstd block-size
	// change made 512 KiB buffer whole, this would silently become a duplicate of
	// the Close-branch test; instead the prefix assertion flips it to a LOUD
	// failure that says "re-pick the size, this no longer covers io.Copy".
	msg := err.Error()
	if !strings.Contains(msg, "compress:") {
		t.Errorf("want io.Copy-branch wrap prefix %q, got %q", "compress:", msg)
	}
	if strings.Contains(msg, "close zstd writer:") {
		t.Errorf("error rode the Close branch, not io.Copy (re-pick the input size): %q", msg)
	}

	// Same as the Close-branch test: nothing materialized — no blob, no manifest,
	// no leaked .vp-atomic-* temp (glob the transcripts dir).
	for _, name := range transcriptsDirEntries(t, vault, "p") {
		switch {
		case strings.HasSuffix(name, ".jsonl.zst"):
			t.Errorf("refused archive left a blob: %s", name)
		case strings.HasSuffix(name, ".manifest.json"):
			t.Errorf("refused archive left a manifest: %s", name)
		case strings.HasPrefix(name, ".vp-atomic-"):
			t.Errorf("refused archive leaked a temp: %s", name)
		default:
			t.Errorf("refused archive left an unexpected file: %s", name)
		}
	}
}

// TestCreate_OverCeilingReArchiveKeepsPriorManifestLive is the reorder
// assertion: a valid archive exists, then an over-ceiling RE-archive of the same
// session (changed source hash) must refuse WITHOUT having .bak'd the prior
// manifest — because compressFile runs ahead of the .bak step. A regression that
// renames the prior manifest first would strand its blob and hide it from
// ListEntries.
func TestCreate_OverCeilingReArchiveKeepsPriorManifestLive(t *testing.T) {
	vault := t.TempDir()
	testutil.InitProject(t, vault, "p")

	// A valid prior archive+manifest for the session (default ceiling; the
	// sample compresses to a few hundred bytes).
	opts := CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "rearchive",
		SourceContent: []byte(sampleInlineJSONL),
		VaultRoot:     vault,
		ProjectSlug:   "p",
		Now:           time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
	}
	first, err := Create(opts)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if first.Skipped {
		t.Fatalf("first Create should not be skipped")
	}

	// Capture the prior blob bytes so we can prove the refused overwrite leaves
	// them byte-identical.
	priorBlob, err := os.ReadFile(first.ArchivePath)
	if err != nil {
		t.Fatalf("read prior blob: %v", err)
	}

	// Re-archive the SAME session (same day -> same manifest path) with a
	// different, incompressible source over a tiny ceiling. The source hash
	// changes (so it is not Skipped), and it trips the cap.
	bad := opts
	bad.SourceContent = incompressibleBytes(t, 4096)
	bad.MaxArchiveBytes = 1024
	if _, err := Create(bad); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("re-archive over ceiling: errors.Is(err, ErrArchiveTooLarge) = false; err = %v", err)
	}

	// The prior manifest must STILL be live at its original path...
	if _, err := os.Stat(first.ManifestPath); err != nil {
		t.Errorf("prior manifest not live after refused re-archive: %v", err)
	}
	// ...and it must NOT have been moved to a .bak.
	bakPath := first.ManifestPath + "." + shortHash(first.Manifest.SourceSHA256) + ".bak"
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Errorf("prior manifest was .bak'd by a refused re-archive: Stat(%s) err = %v", bakPath, err)
	}
	// ...and readers must still see exactly the prior entry.
	entries, err := ListEntries(vault, "p")
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListEntries returned %d entries, want 1", len(entries))
	}
	if got := entries[0].Manifest.SourceSHA256; got != first.Manifest.SourceSHA256 {
		t.Errorf("live entry hash = %s, want prior %s", got, first.Manifest.SourceSHA256)
	}
	// ...and the prior BLOB bytes must be byte-identical — the refused overwrite
	// must not have touched the live .jsonl.zst (atomicfile never renamed its
	// temp in).
	afterBlob, err := os.ReadFile(first.ArchivePath)
	if err != nil {
		t.Errorf("prior blob not readable after refused re-archive: %v", err)
	} else if !bytes.Equal(afterBlob, priorBlob) {
		t.Errorf("prior blob bytes changed after refused re-archive: before %d bytes, after %d bytes",
			len(priorBlob), len(afterBlob))
	}
}
