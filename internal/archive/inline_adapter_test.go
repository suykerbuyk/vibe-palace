// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleInlineJSONL = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}
{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi there"}]}}
`

func TestInlineAdapter_ResolveSourceWritesExactBytes(t *testing.T) {
	content := []byte(sampleInlineJSONL)
	path, cleanup, err := inlineAdapter{}.ResolveSource(CreateOptions{
		SourceContent: content,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("temp bytes differ from SourceContent:\ngot  %q\nwant %q", got, content)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove temp file %s (err=%v)", path, err)
	}
}

func TestInlineAdapter_EmptyContentFailsClear(t *testing.T) {
	_, _, err := inlineAdapter{}.ResolveSource(CreateOptions{})
	if err == nil || !strings.Contains(err.Error(), "SourceContent") {
		t.Errorf("err = %v, want hint naming SourceContent", err)
	}

	_, err = Create(CreateOptions{
		Adapter:     InlineAdapterName,
		SessionID:   "empty",
		VaultRoot:   t.TempDir(),
		ProjectSlug: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "SourceContent") {
		t.Errorf("Create err = %v, want hint naming SourceContent", err)
	}
}

func TestInlineAdapter_CreateProducesArchivePair(t *testing.T) {
	content := []byte(sampleInlineJSONL)
	vault := t.TempDir()

	res, err := Create(CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "session-inline",
		SourceContent: content,
		VaultRoot:     vault,
		ProjectSlug:   "test",
		Now:           time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		VPVersion:     "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Skipped {
		t.Errorf("first Create should not be skipped")
	}
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Errorf("archive missing: %v", err)
	}
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Errorf("manifest missing: %v", err)
	}
	if !strings.HasSuffix(res.ArchivePath, ".jsonl.zst") {
		t.Errorf("archive path = %s, want .jsonl.zst suffix", res.ArchivePath)
	}
	if !strings.HasSuffix(res.ManifestPath, ".manifest.json") {
		t.Errorf("manifest path = %s, want .manifest.json suffix", res.ManifestPath)
	}
	if res.Manifest.Adapter != InlineAdapterName {
		t.Errorf("adapter = %q, want inline", res.Manifest.Adapter)
	}
	if got, want := res.Manifest.SourceSHA256, sha256OfBytes(content); got != want {
		t.Errorf("source_sha256 = %s, want %s", got, want)
	}
	// The archive must decompress back to the supplied bytes.
	lines := readArchiveLines(t, res.ArchivePath)
	if len(lines) != 2 {
		t.Fatalf("archive has %d lines, want 2", len(lines))
	}
	requireField(t, lines[0], "type", "user")
	requireField(t, lines[1], "type", "assistant")
}

func TestInlineAdapter_Idempotent(t *testing.T) {
	vault := t.TempDir()
	opts := CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "idem-inline",
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
		t.Errorf("first call should not be skipped")
	}
	second, err := Create(opts)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if !second.Skipped {
		t.Errorf("second call should be skipped (idempotent)")
	}
	if second.Manifest.SourceSHA256 != first.Manifest.SourceSHA256 {
		t.Errorf("hash drifted between calls")
	}
}

func TestInlineAdapter_ChangedContentPreservesPriorManifest(t *testing.T) {
	vault := t.TempDir()
	opts := CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "changed-inline",
		SourceContent: []byte(sampleInlineJSONL),
		VaultRoot:     vault,
		ProjectSlug:   "p",
		Now:           time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
	}
	first, err := Create(opts)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	opts.SourceContent = []byte(sampleInlineJSONL + `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"more"}]}}` + "\n")
	second, err := Create(opts)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if second.Skipped {
		t.Errorf("changed content should not be skipped")
	}
	if second.Manifest.SourceSHA256 == first.Manifest.SourceSHA256 {
		t.Errorf("hash should change with content")
	}

	// The prior manifest must survive as a .bak next to the new pair.
	bakPath := first.ManifestPath + "." + shortHash(first.Manifest.SourceSHA256) + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		t.Errorf("prior manifest not preserved at %s: %v", bakPath, err)
	}
	m, err := ReadManifest(second.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.SourceSHA256 != second.Manifest.SourceSHA256 {
		t.Errorf("new manifest not authoritative: got %s want %s", m.SourceSHA256, second.Manifest.SourceSHA256)
	}
}

func TestInlineAdapter_TempFileCleanedUp(t *testing.T) {
	tmpdir := os.TempDir()
	before := listInlineTmpFiles(t, tmpdir)

	_, err := Create(CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "clean-inline",
		SourceContent: []byte(sampleInlineJSONL),
		VaultRoot:     t.TempDir(),
		ProjectSlug:   "p",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	after := listInlineTmpFiles(t, tmpdir)
	if len(after) > len(before) {
		t.Errorf("temp files leaked: before=%d after=%d", len(before), len(after))
	}
}

func listInlineTmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vp-archive-inline-") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
