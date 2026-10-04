// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The CLI sites that remove a gone project's host-local index store,
// palace/.local/index/<p>/: vp vault pull, vp vault sync and vp vault project
// delete. Each runs after storage has released the vault lock; that ordering is
// pinned in package indexstore, not here.

// cliSeedStore commits one chunk to project's index store while the project
// exists, so the store, its .generation counter and its commit lock file are
// real.
func cliSeedStore(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("Lock %s: %v", project, err)
	}
	tx.UseRecipe(index.ChunkRecipe{IndexerVersion: index.IndexerVersion})
	content := "a chunk of " + project
	chunk := indexstore.OwnedChunk{Chunk: indexstore.Chunk{ID: index.ChunkID(content), Content: content}, Ownership: indexstore.Ownership{Day: "2026-05-13"}}
	if err := tx.Append(indexstore.NoteOwner("notes/"+project+".md"), []indexstore.OwnedChunk{chunk}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func cliStoreExists(t *testing.T, v *storage.Vault, project string) bool {
	t.Helper()
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Lstat(dir)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatalf("lstat %s: %v", dir, err)
	}
	return false
}

// cliRemovalVault is the configured vault, wired to a bare origin, with
// palace/.local/ ignored as production ignores it and the projects named
// committed and pushed. It returns the vault root and the bare remote.
func cliRemovalVault(t *testing.T, projects ...string) (root, bare string) {
	t.Helper()
	root = setupVaultWithOrigin(t)
	bare = gitRun(t, root, "remote", "get-url", "origin")
	mkfile(t, root, ".gitignore", ".vp-locks/\npalace/.local/\n")
	for _, p := range projects {
		mkfile(t, root, "Projects/"+p+"/resume.md", "# "+p+"\n")
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-m", "seed projects")
	gitRun(t, root, "push", "origin", "main")
	return root, bare
}

// vp vault pull and vp vault sync: the pull that removes Projects/gone/ is
// followed by the index sweep, which removes index/gone/ and keeps index/live/.
func TestVaultPullAndSyncCLIRemoveGoneProjectsIndexStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() int
	}{
		{"pull", func() int { return cmdVaultPull().Run(nil) }},
		{"sync", func() int { return cmdVaultSync().Run(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bare := cliRemovalVault(t, "live", "gone")
			v := storage.NewVault(root)
			cliSeedStore(t, v, "live")
			cliSeedStore(t, v, "gone")

			other := t.TempDir()
			gitRun(t, other, "clone", "-q", "-b", "main", bare, ".")
			gitRun(t, other, "config", "user.email", "other@test.com")
			gitRun(t, other, "config", "user.name", "Other")
			gitRun(t, other, "rm", "-r", "-q", "Projects/gone")
			gitRun(t, other, "commit", "-m", "remove gone")
			gitRun(t, other, "push", "origin", "main")
			if !cliStoreExists(t, v, "gone") {
				t.Fatal("fixture: index/gone/ missing before the pull")
			}

			var code int
			stderr := captureStderr(t, func() { code = tc.run() })
			if code != cli.ExitOK {
				t.Fatalf("vp vault %s: exit %d\n%s", tc.name, code, stderr)
			}
			if _, err := os.Stat(filepath.Join(root, "Projects", "gone")); err == nil {
				t.Fatal("fixture: the pull did not bring the removal of Projects/gone/")
			}
			if cliStoreExists(t, v, "gone") {
				t.Errorf("vp vault %s left index/gone/ after the pull removed the project", tc.name)
			}
			if !cliStoreExists(t, v, "live") {
				t.Errorf("vp vault %s removed index/live/", tc.name)
			}
		})
	}
}

// vp vault project delete: the dry run lists the index store but does not hash
// it, so an append between dry run and real run leaves the digest valid; the
// real run then removes the store after the delete, changes its epoch, keeps
// the lock files and reports index_removal in --json.
func TestVaultProjectDeleteCLIRemovesIndexStore(t *testing.T) {
	root, bare := cliRemovalVault(t, "p", "q")
	v := storage.NewVault(root)
	cliSeedStore(t, v, "p")
	cliSeedStore(t, v, "q")

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--discard", "--vault", root, "--dry-run", "--json")
	if code != cli.ExitOK {
		t.Fatalf("dry run: exit %d\n%s", code, stderr)
	}
	var plan struct {
		IndexStores []string `json:"index_stores"`
		Digest      string   `json:"digest"`
	}
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("dry run JSON: %v\n%s", err, stdout)
	}
	if !slices.Equal(plan.IndexStores, []string{"palace/.local/index/p/"}) {
		t.Fatalf("index_stores = %v, want [palace/.local/index/p/]", plan.IndexStores)
	}

	// An ingester appends between dry run and real run: the store is not in the
	// digest.
	f, err := os.OpenFile(filepath.Join(v.IndexRootDir(), "p", "chunks.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"appended"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	genBefore, err := indexstore.ReadGeneration(v, "p")
	if err != nil {
		t.Fatal(err)
	}
	locks, err := os.ReadDir(v.IndexLocksDir())
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code = runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--discard", "--vault", root, "--expect", plan.Digest, "--json")
	if code != cli.ExitOK {
		t.Fatalf("real run: exit %d\n%s", code, stderr)
	}
	if got := gitRun(t, bare, "rev-parse", "main"); got != gitHead(t, root) {
		t.Fatalf("the delete was not published: remote %s, HEAD %s", got, gitHead(t, root))
	}
	var res struct {
		IndexRemoval []indexstore.IndexRemoval `json:"index_removal"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("real run JSON: %v\n%s", err, stdout)
	}
	want := []indexstore.IndexRemoval{{Slug: "p", Path: "palace/.local/index/p/", Outcome: indexstore.RemovalRemoved}}
	if !slices.Equal(res.IndexRemoval, want) {
		t.Errorf("index_removal = %+v, want %+v", res.IndexRemoval, want)
	}
	if cliStoreExists(t, v, "p") {
		t.Error("index/p/ survived the delete")
	}
	if !cliStoreExists(t, v, "q") {
		t.Error("the delete removed index/q/")
	}
	genAfter, err := indexstore.ReadGeneration(v, "p")
	if err != nil {
		t.Fatal(err)
	}
	if genAfter.Epoch == genBefore.Epoch {
		t.Errorf(".generation/p epoch unchanged (%x) by the removal", genAfter.Epoch)
	}
	for _, l := range locks {
		if _, err := os.Lstat(filepath.Join(v.IndexLocksDir(), l.Name())); err != nil {
			t.Errorf("lock file %s gone after the delete: %v", l.Name(), err)
		}
	}
}

// The human-readable delete prints one line per index store it removed (or
// kept), so an operator who reads the text output sees what happened.
func TestVaultProjectDeleteCLIPrintsIndexRemoval(t *testing.T) {
	root, _ := cliRemovalVault(t, "p", "q")
	v := storage.NewVault(root)
	cliSeedStore(t, v, "p")
	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--discard", "--vault", root, "--dry-run", "--json")
	if code != cli.ExitOK {
		t.Fatalf("dry run: exit %d\n%s", code, stderr)
	}
	var plan struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--discard", "--vault", root, "--expect", plan.Digest)
	if code != cli.ExitOK {
		t.Fatalf("real run: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "index store palace/.local/index/p/: removed") {
		t.Fatalf("the text output has no index_removal line:\n%s", stdout)
	}
}
