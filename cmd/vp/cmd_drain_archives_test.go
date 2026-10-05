// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// drainArchiveOf writes a Claude archive of session in project p and returns
// its manifest.
func drainArchiveOf(t *testing.T, vault, p, session string, captured time.Time) *archive.Manifest {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(vault, "Projects", p), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(vault, "Projects", p, "resume.md"), []byte("# "+p+"\n"), 0o644)
	body := `{"type":"user","timestamp":"2026-05-13T01:30:00Z","message":{"role":"user","content":"We designed the pending-archive ingester, its ledger and the chunk store in detail for session ` + session + `."}}` + "\n"
	src := filepath.Join(t.TempDir(), session+".jsonl")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := archive.Create(archive.CreateOptions{Adapter: archive.ClaudeCodeAdapterName, SessionID: session, SourcePath: src, VaultRoot: vault, ProjectSlug: p, Now: captured})
	if err != nil {
		t.Fatal(err)
	}
	return res.Manifest
}

func drainLedger(t *testing.T, vault, p string) *indexstore.Ledger {
	t.Helper()
	st, err := indexstore.ReadStore(storage.NewVault(vault), p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Ledger()
}

// TestDrainArchivesResolvesNothing: with no --project, a relative
// --vault-root, or an invalid slug, the command prints one line, exits 0,
// opens no archive and takes no lock, whatever the working directory.
func TestDrainArchivesResolvesNothing(t *testing.T) {
	vault := setupTestVaultEnv(t)
	drainArchiveOf(t, vault, "p", "S", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	t.Chdir(filepath.Join(vault, "Projects", "p"))
	for _, c := range [][3]string{{vault, "", ""}, {"", "p", ""}, {"relative/vault", "p", ""}, {vault, "../Bad", ""}} {
		var errOut bytes.Buffer
		if code := runDrainArchives(context.Background(), c[0], c[1], c[2], &errOut); code != cli.ExitOK {
			t.Fatalf("%v: exit %d", c, code)
		}
		if n := strings.Count(strings.TrimSpace(errOut.String()), "\n"); n != 0 || errOut.Len() == 0 {
			t.Fatalf("%v: stderr %q, want one line", c, errOut.String())
		}
		if _, err := os.Stat(filepath.Join(vault, "palace", ".local")); !os.IsNotExist(err) {
			t.Fatalf("%v: palace/.local was created (stat err %v)", c, err)
		}
	}
}

// TestDrainArchivesIngestsTheTriggersArchive: the trigger's archive, named by
// its source_sha256, is ingested on a host with no ledger, the embedder is
// built once, the run logs to the vault's vp.log, and it exits 0.
func TestDrainArchivesIngestsTheTriggersArchive(t *testing.T) {
	vault := setupTestVaultEnv(t)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	drainArchiveOf(t, vault, "p", "Old", time.Date(2026, 5, 10, 2, 0, 0, 0, time.UTC))
	m := drainArchiveOf(t, vault, "p", "S", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	var errOut bytes.Buffer
	if code := runDrainArchives(context.Background(), vault, "p", m.SourceSHA256, &errOut); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	l := drainLedger(t, vault, "p")
	if s, ok := l.Session("S"); !ok || s.State != indexstore.StateLive || s.StartDay != "2026-05-13" {
		t.Fatalf("session %+v", s)
	}
	if _, ok := l.Session("Old"); ok {
		t.Fatal("backlog was ingested")
	}
	if *constructed != 1 {
		t.Fatalf("embedder constructed %d times, want 1", *constructed)
	}
	if _, err := os.Stat(filepath.Join(vault, "palace", ".local", "vp.log")); err != nil {
		t.Fatalf("no vp.log in the vault: %v", err)
	}
}

// TestDrainArchivesIgnoresAPathAsFirst: --first given an archive path is
// warned about and ignored, so that archive becomes backlog like the rest.
func TestDrainArchivesIgnoresAPathAsFirst(t *testing.T) {
	vault := setupTestVaultEnv(t)
	stubVaultEmbedder(t, embedder.NewMock(384))
	m := drainArchiveOf(t, vault, "p", "S", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	var errOut bytes.Buffer
	runDrainArchives(context.Background(), vault, "p", "Projects/p/transcripts/2026-05-13-S.jsonl.zst", &errOut)
	if !drainLedger(t, vault, "p").InBaseline(m.SourceSHA256) {
		t.Fatal("a path given as --first kept the archive out of the baseline")
	}
	logData, _ := os.ReadFile(filepath.Join(vault, "palace", ".local", "vp.log"))
	if !strings.Contains(string(logData), "--first is not a source_sha256") {
		t.Fatalf("vp.log lacks the warning:\n%s", logData)
	}
}

// TestDrainArchivesLoadsNoModelWithNothingToEmbed: with nothing pending the
// lazy embedder is never built.
func TestDrainArchivesLoadsNoModelWithNothingToEmbed(t *testing.T) {
	vault := setupTestVaultEnv(t)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	drainArchiveOf(t, vault, "p", "Old", time.Date(2026, 5, 10, 2, 0, 0, 0, time.UTC))
	var errOut bytes.Buffer
	runDrainArchives(context.Background(), vault, "p", "", &errOut)
	if *constructed != 0 {
		t.Fatalf("embedder constructed %d times with nothing to embed", *constructed)
	}
}
