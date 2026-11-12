// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Multi-process rows re-exec this test binary as a helper process, selected
// by helperModeEnv; it reports only through its exit code.
const (
	helperModeEnv    = "VP_SEARCH_HELPER"
	helperVaultEnv   = "VP_SEARCH_VAULT"
	helperProjectEnv = "VP_SEARCH_PROJECT"
	helperContentEnv = "VP_SEARCH_CONTENT"
	helperFPEnv      = "VP_SEARCH_FINGERPRINT"
	helperDirEnv     = "VP_SEARCH_DIR"
)

// runHelper runs one helper mode and returns its exit code.
func runHelper() int {
	switch os.Getenv(helperModeEnv) {
	case "crash-after-commit":
		if err := crashAfterCommit(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 3 // unreachable: crashAfterCommit exits
	case "discard-kill":
		if err := discardKill(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 3 // unreachable: discardKill blocks until killed
	}
	fmt.Fprintln(os.Stderr, "unknown helper mode")
	return 2
}

// crashAfterCommit commits one archive (its vector, chunk and ledger entry)
// and exits without Commit or Release: the process dies after its records are
// durable and before the store counter is bumped (Tx.Commit's known limit).
// The OS releases the commit lock.
func crashAfterCommit() error {
	v := storage.NewVault(os.Getenv(helperVaultEnv))
	project, content := os.Getenv(helperProjectEnv), os.Getenv(helperContentEnv)
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	ix, err := palace.ProjectIndexing(v, project)
	if err != nil {
		return err
	}
	tx.UseRecipe(ix.Recipe)
	if _, err := tx.EnsureLedger(nil); err != nil {
		return err
	}
	cache := NewEmbedCache(v)
	cache.fingerprint = os.Getenv(helperFPEnv)
	w, err := cache.Writer(tx)
	if err != nil {
		return err
	}
	vec, err := embedder.NewMock(384).Embed(context.Background(), content)
	if err != nil {
		return err
	}
	id := index.ChunkID(content)
	err = tx.CommitArchive(indexstore.ArchiveCommit{
		SessionID: "crashed-session", SHA: "crashed-sha", ArchivePath: "x",
		StartDay: "2026-05-13", StartDaySource: indexstore.DayFromTranscript,
		Chunks:  []indexstore.OwnedChunk{chunkOf(content, "session")},
		Vectors: map[string][]float32{id: vec},
	}, w)
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// runCrashHelper runs crashAfterCommit in a child process and requires a
// clean exit.
func runCrashHelper(t *testing.T, eng *Engine, v *storage.Vault, project, content string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"=crash-after-commit",
		helperVaultEnv+"="+v.Root,
		helperProjectEnv+"="+project,
		helperContentEnv+"="+content,
		helperFPEnv+"="+eng.cache.fingerprint,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, out)
	}
}

// discardKill runs a destructive Tx (Tx.Discard(DiscardChunks)) and blocks, for
// ever, at the "discard-chunks" step: after the ledger and chunks are removed
// and before the graph is, so a STALE hnsw.idx is on disk beside a store the
// destructive write has already changed. It signals "ready" in the directory
// named by helperDirEnv at that point. Neither Commit nor Release runs; the
// parent SIGKILLs it.
func discardKill() error {
	v := storage.NewVault(os.Getenv(helperVaultEnv))
	project := os.Getenv(helperProjectEnv)
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	ix, err := palace.ProjectIndexing(v, project)
	if err != nil {
		return err
	}
	tx.UseRecipe(ix.Recipe)
	done := make(chan struct{})
	indexstore.SetCommitStepHook(func(step string) error {
		if step == "discard-chunks" {
			if err := os.WriteFile(filepath.Join(os.Getenv(helperDirEnv), "ready"), nil, 0o644); err != nil {
				return err
			}
			<-done // never closed: block until killed
		}
		return nil
	})
	if err := tx.Discard(indexstore.DiscardChunks); err != nil {
		return err
	}
	return fmt.Errorf("the helper was never killed")
}

// killDiscardHelper forks discardKill against project and SIGKILLs it at the
// discard-chunks step.
func killDiscardHelper(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"=discard-kill",
		helperVaultEnv+"="+v.Root,
		helperProjectEnv+"="+project,
		helperDirEnv+"="+dir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the discard helper never reached the discard-chunks step")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}
