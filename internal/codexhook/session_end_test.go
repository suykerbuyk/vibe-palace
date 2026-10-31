// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

func TestSessionEndSmallTranscriptArchivesAndLinksReceiptWithinThreeSeconds(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	source := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := []byte("{\"type\":\"response_item\",\"payload\":{\"message\":\"kept verbatim\"}}\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	started := time.Now()
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_small", TranscriptPath: source}, v, "demo", &out)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 3*time.Second {
		t.Fatalf("small fixture took %s", time.Since(started))
	}
	if !strings.Contains(out.String(), "archived:") {
		t.Fatalf("success output missing archive path: %q", out.String())
	}
	entries, err := archive.ListEntries(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	sum := sha256.Sum256(content)
	if entries[0].Manifest.SourceSHA256 != hex.EncodeToString(sum[:]) || entries[0].Manifest.TurnCount != 0 {
		t.Fatalf("manifest = %+v", entries[0].Manifest)
	}
	if entries[0].Manifest.VaultRelSessionNote != "" {
		t.Fatalf("stub receipt back-linked itself: %q", entries[0].Manifest.VaultRelSessionNote)
	}
	metaRows, _, err := v.ListSessions("demo", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var meta storage.SessionMeta
	for _, row := range metaRows {
		if row.ArchiveSessionID == "thr_small" {
			meta = row
			break
		}
	}
	if meta.Archive == "" {
		t.Fatalf("receipt has no forward archive link: %+v", meta)
	}
	if meta.Host != storage.HostCodex || meta.HostSource != storage.HostSourceCodexHookEvent || meta.ArchiveSessionIDSource != storage.ArchiveIDSourceCodexSessionEnd {
		t.Fatalf("receipt provenance = %+v", meta)
	}
}

func TestSessionEndOversizeTranscriptFailsWithoutReceipt(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	source := filepath.Join(t.TempDir(), "large.jsonl")
	content := make([]byte, archive.DefaultMaxArchiveBytes+1)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	started := time.Now()
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_large", TranscriptPath: source}, v, "demo", &out)
	if err == nil || time.Since(started) >= 3*time.Second {
		t.Fatalf("large source result err=%v elapsed=%s", err, time.Since(started))
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("failure emitted success output: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("large failure committed manifest/receipt: %+v", entries)
	}
}

func TestSessionEndEmptyTranscriptReportsFailureWithoutReceipt(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	source := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_empty", TranscriptPath: source}, storage.NewVault(root), "demo", &out)
	if err == nil || !strings.Contains(err.Error(), "usable non-empty regular file") {
		t.Fatalf("empty transcript error = %v", err)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("empty transcript emitted success output: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("empty source created archive: %+v", entries)
	}
}

func TestSessionEndMissingTranscriptReportsFailureWithoutReceipt(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	var out bytes.Buffer
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_missing", TranscriptPath: filepath.Join(t.TempDir(), "missing")}, storage.NewVault(root), "demo", &out)
	if err == nil || !strings.Contains(err.Error(), "transcript unavailable") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("failure emitted success output: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("missing source created archive: %+v", entries)
	}
}

func TestSessionEndReportsArchiveSuccessReceiptFailure(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	dir, err := v.SessionDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.EnsureVaultDir(dir); err != nil {
		t.Fatal(err)
	}
	release, err := vaultlock.Acquire(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_fail", TranscriptPath: path}, v, "demo", &out)
	if err == nil || !strings.Contains(err.Error(), "archive created; note/link incomplete") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("failure emitted archived line: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 1 {
		t.Fatalf("archive should remain available for retry: %+v", entries)
	}
}
