// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The MCP sites that remove a gone project's host-local index store,
// palace/.local/index/<p>/: vp_vault_sync's pull and sync, the split purge and
// vp_vault_project_delete. Each runs after storage has released the vault lock;
// that ordering is pinned in package indexstore, not here.

// removalSeedStore commits one chunk to project's index store while the
// project exists, so the store, its .generation counter and its commit lock
// file are real.
func removalSeedStore(t *testing.T, v *storage.Vault, project string) {
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

func removalStoreExists(t *testing.T, v *storage.Vault, project string) bool {
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

func removalWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// removalRemoteVault is a git vault with a bare origin, palace/.local/ ignored
// as production ignores it, and the projects named seeded, committed and
// pushed. It returns the vault root and the bare remote.
func removalRemoteVault(t *testing.T, projects ...string) (root, bare string) {
	t.Helper()
	sandboxHostEnv(t)
	root = initVaultRepo(t)
	bare = t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	removalWrite(t, root, ".gitignore", ".vp-locks/\npalace/.local/\n")
	for _, p := range projects {
		removalWrite(t, root, "Projects/"+p+"/resume.md", "# "+p+"\n")
	}
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "seed projects")
	gitT(t, root, "push", "-u", "origin", "main")
	return root, bare
}

// removeOnRemote deletes Projects/<project>/ in a second clone and pushes, so
// the next pull is how this host learns the project left.
func removeOnRemote(t *testing.T, bare, project string) {
	t.Helper()
	other := t.TempDir()
	gitT(t, other, "clone", "-q", "-b", "main", bare, ".")
	gitT(t, other, "config", "user.email", "other@example.com")
	gitT(t, other, "config", "user.name", "Other")
	gitT(t, other, "rm", "-r", "-q", "Projects/"+project)
	gitT(t, other, "commit", "-m", "remove "+project)
	gitT(t, other, "push", "origin", "main")
}

// vp_vault_sync action=pull (gitPull) and action=sync (storage.SyncVault): the
// pull that removes Projects/gone/ is followed by the index sweep, which
// removes index/gone/ and keeps index/live/.
func TestVaultSyncToolPullRemovesGoneProjectsIndexStore(t *testing.T) {
	for _, action := range []string{"pull", "sync"} {
		t.Run(action, func(t *testing.T) {
			root, bare := removalRemoteVault(t, "live", "gone")
			v := storage.NewVault(root)
			removalSeedStore(t, v, "live")
			removalSeedStore(t, v, "gone")
			removeOnRemote(t, bare, "gone")
			if !removalStoreExists(t, v, "gone") {
				t.Fatal("fixture: index/gone/ missing before the pull")
			}

			params, _ := json.Marshal(vaultSyncParams{Action: action})
			if _, err := VaultSyncTool(v).Handler(context.Background(), params); err != nil {
				t.Fatalf("vp_vault_sync %s: %v", action, err)
			}
			if _, err := os.Stat(filepath.Join(root, "Projects", "gone")); err == nil {
				t.Fatal("fixture: the pull did not bring the removal of Projects/gone/")
			}
			if removalStoreExists(t, v, "gone") {
				t.Errorf("vp_vault_sync %s left index/gone/ after the pull removed the project", action)
			}
			if !removalStoreExists(t, v, "live") {
				t.Errorf("vp_vault_sync %s removed index/live/", action)
			}
		})
	}
}

// The split purge removes each purged project's index store after the purge
// commit, and keeps the store of the project that stays.
func TestSplitPurgeRemovesPurgedProjectsIndexStores(t *testing.T) {
	root := purgeVault(t)
	v := storage.NewVault(root)
	for _, s := range []string{"alpha", "orch", "keep"} {
		removalSeedStore(t, v, s)
	}
	p := purgeReady(t, root)
	for _, s := range []string{"alpha", "orch", "keep"} {
		if !removalStoreExists(t, v, s) {
			t.Fatalf("fixture: index/%s/ gone before the purge", s)
		}
	}
	before, err := indexstore.ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("purge: %v", err)
	}
	for _, s := range []string{"alpha", "orch"} {
		if removalStoreExists(t, v, s) {
			t.Errorf("index/%s/ survived the purge", s)
		}
	}
	if !removalStoreExists(t, v, "keep") {
		t.Error("the purge removed index/keep/, the store of a project that stays")
	}
	after, err := indexstore.ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if after.Epoch == before.Epoch {
		t.Errorf(".generation/alpha epoch unchanged (%x) by the removal", after.Epoch)
	}
}

// vp_vault_project_delete: plan lists the index store but does not hash it, so
// an append between plan and apply leaves the digest valid; apply then removes
// the store after the delete, changes its epoch, keeps the lock files and
// reports index_removal.
func TestVaultProjectDeleteToolRemovesIndexStore(t *testing.T) {
	root, bare := removalRemoteVault(t, "p", "q")
	v := storage.NewVault(root)
	removalSeedStore(t, v, "p")
	removalSeedStore(t, v, "q")
	tool := VaultProjectDeleteTool(v)
	call := func(params vaultProjectDeleteParams) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(params)
		res, err := tool.Handler(context.Background(), raw)
		if err != nil {
			t.Fatalf("vp_vault_project_delete %s: %v", params.Action, err)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	plan := call(vaultProjectDeleteParams{Action: "plan", Slugs: []string{"p"}, Discard: true})
	var stores []string
	for _, s := range anySlice(plan["index_stores"]) {
		stores = append(stores, s.(string))
	}
	if !slices.Equal(stores, []string{"palace/.local/index/p/"}) {
		t.Fatalf("plan index_stores = %v, want [palace/.local/index/p/]", plan["index_stores"])
	}
	digest, _ := plan["digest"].(string)
	if digest == "" {
		t.Fatalf("plan has no digest: %v", plan)
	}

	// An ingester appends between plan and apply: the store is not in the digest.
	chunks := filepath.Join(v.IndexRootDir(), "p", "chunks.jsonl")
	f, err := os.OpenFile(chunks, os.O_APPEND|os.O_WRONLY, 0)
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

	res := call(vaultProjectDeleteParams{Action: "apply", Slugs: []string{"p"}, Discard: true, Expect: digest})
	if gitT(t, bare, "rev-parse", "main") != gitT(t, root, "rev-parse", "HEAD") {
		t.Fatal("the delete was not published")
	}
	if removalStoreExists(t, v, "p") {
		t.Error("index/p/ survived the delete")
	}
	if !removalStoreExists(t, v, "q") {
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
	removal := anySlice(res["index_removal"])
	if len(removal) != 1 {
		t.Fatalf("index_removal = %v, want one entry", res["index_removal"])
	}
	entry, _ := removal[0].(map[string]any)
	if entry["slug"] != "p" || entry["path"] != "palace/.local/index/p/" || entry["outcome"] != "removed" {
		t.Errorf("index_removal[0] = %v, want slug p, path palace/.local/index/p/, outcome removed", entry)
	}
	if e, _ := entry["error"].(string); strings.TrimSpace(e) != "" {
		t.Errorf("index_removal[0].error = %q", e)
	}
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// deleteToolCall runs vp_vault_project_delete and decodes its JSON result.
func deleteToolCall(t *testing.T, v *storage.Vault, params vaultProjectDeleteParams) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := VaultProjectDeleteTool(v).Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("vp_vault_project_delete %s: %v", params.Action, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The dry run lists only the stores that exist: a deleted project with no
// host-local store is not named.
func TestVaultProjectDeletePlanListsOnlyExistingStores(t *testing.T) {
	root, _ := removalRemoteVault(t, "p", "q", "r")
	v := storage.NewVault(root)
	removalSeedStore(t, v, "p")
	plan := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "plan", Slugs: []string{"p", "q"}, Discard: true})
	var stores []string
	for _, s := range anySlice(plan["index_stores"]) {
		stores = append(stores, s.(string))
	}
	if !slices.Equal(stores, []string{"palace/.local/index/p/"}) {
		t.Fatalf("index_stores = %v, want only the existing palace/.local/index/p/", stores)
	}
}

// The MCP delete WAITS for a busy commit lock (indexstore.LifecycleRemovalTimeout)
// rather than trying once: an ingester holding p's lock when the delete lands
// releases it a moment later, and the store is still removed. The lock is
// released shortly after the delete starts waiting for it; a try-once removal
// has already given up by then and reports the store kept.
func TestVaultProjectDeleteToolWaitsForABusyLock(t *testing.T) {
	root, _ := removalRemoteVault(t, "p", "q")
	v := storage.NewVault(root)
	removalSeedStore(t, v, "p")
	plan := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "plan", Slugs: []string{"p"}, Discard: true})
	digest, _ := plan["digest"].(string)

	lockPath, err := v.IndexCommitLockPath("p")
	if err != nil {
		t.Fatal(err)
	}
	release, err := vaultlock.AcquireFileWithTimeout(context.Background(), lockPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	restore := indexstore.ObserveCommitLocks(func(project string, ev indexstore.CommitLockEvent) {
		if project == "p" && ev == indexstore.CommitWait {
			once.Do(func() { time.AfterFunc(200*time.Millisecond, func() { _ = release() }) })
		}
	})
	defer restore()

	res := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "apply", Slugs: []string{"p"}, Discard: true, Expect: digest})
	removal := anySlice(res["index_removal"])
	if len(removal) != 1 {
		t.Fatalf("index_removal = %v", res["index_removal"])
	}
	if entry, _ := removal[0].(map[string]any); entry["outcome"] != "removed" {
		t.Fatalf("index_removal[0] = %v, want removed after waiting for the lock", entry)
	}
	if removalStoreExists(t, v, "p") {
		t.Fatal("index/p/ survived")
	}
}
