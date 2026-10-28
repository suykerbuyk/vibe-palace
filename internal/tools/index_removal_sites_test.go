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

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The MCP sites that remove a gone project's host-local index store,
// palace/.local/index/<p>/: vp_vault_sync's pull and sync, and
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
	defer release() // idempotent; cleans up if the barrier below never fires

	// Wall-clock-free handshake (no time.AfterFunc): hand the lock over EXACTLY
	// when the delete's acquire finds it held and is about to wait — vaultlock's
	// SetWaitHook fires only on that transition (pollExclusive, after a failed try,
	// before the sleep), never on a try-once acquire that gives up at a zero
	// deadline without waiting. So a WAITING removal releases-then-acquires and
	// reports "removed"; a try-once removal never trips the hook, never gets the
	// lock, and reports the store kept — which is exactly the regression this test
	// pins. The guard runs once so only the first waiter hands the lock over.
	var once sync.Once
	restore := vaultlock.SetWaitHook(func() { once.Do(func() { _ = release() }) })
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

// firstRemoval decodes the single index_removal entry of an apply result.
func firstRemoval(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	removal := anySlice(res["index_removal"])
	if len(removal) != 1 {
		t.Fatalf("index_removal = %v, want one entry", res["index_removal"])
	}
	entry, _ := removal[0].(map[string]any)
	return entry
}

// Gap 3: the index-store removal that BOTH the CLI (`vp vault project delete`)
// and the MCP tool reach — storage.ApplyDelete, which takes and RELEASES the
// vault root lock, then indexstore.RemoveGoneProjects — must run the removal with
// the vault root lock NOT held. This drives the real MCP apply end to end (not a
// unit test of the indexstore helper) and asserts, at the instant the removal's
// index commit lock is acquired, that the vault root lock is free.
func TestVaultProjectDeleteToolRunsIndexRemovalWithoutTheVaultRootLock(t *testing.T) {
	root, _ := removalRemoteVault(t, "p", "q")
	v := storage.NewVault(root)
	removalSeedStore(t, v, "p")
	plan := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "plan", Slugs: []string{"p"}, Discard: true})
	digest, _ := plan["digest"].(string)

	var mu sync.Mutex
	var sawRemoval, rootFree bool
	restore := indexstore.ObserveCommitLocks(func(project string, ev indexstore.CommitLockEvent) {
		if project != "p" || ev != indexstore.CommitAcquired {
			return
		}
		// We are inside the removal, holding p's index commit lock. The vault root
		// lock must already be released by ApplyDelete: a non-blocking try proves it.
		rel, ok, err := vaultlock.TryAcquire(v.Root, v.Root)
		mu.Lock()
		sawRemoval = true
		rootFree = err == nil && ok
		mu.Unlock()
		if ok {
			_ = rel()
		}
	})
	defer restore()

	res := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "apply", Slugs: []string{"p"}, Discard: true, Expect: digest})
	if entry := firstRemoval(t, res); entry["outcome"] != "removed" {
		t.Fatalf("index_removal outcome = %v, want removed", entry["outcome"])
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawRemoval {
		t.Fatal("the removal's index commit lock was never acquired: the removal did not run")
	}
	if !rootFree {
		t.Fatal("the vault root lock was HELD while the index store was removed; ApplyDelete must release it before the removal")
	}
}

// Gap 4: a crash BETWEEN the vault-side delete and the host-local index removal
// (the process dies after storage.ApplyDelete commits and before
// indexstore.RemoveGoneProjects runs) leaves an orphaned index store. The next
// index sweep (indexstore.ReapGoneProjects) must converge: the orphan is removed
// and none is left behind.
func TestIndexStoreConvergesAfterACrashBetweenDeleteAndRemoval(t *testing.T) {
	root, _ := removalRemoteVault(t, "p", "q")
	v := storage.NewVault(root)
	removalSeedStore(t, v, "p")
	removalSeedStore(t, v, "q") // a live sibling whose store the sweep must keep
	plan := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "plan", Slugs: []string{"p"}, Discard: true})
	digest, _ := plan["digest"].(string)

	// ApplyDelete lands; the process crashes before RemoveGoneProjects — so the
	// index store is left behind as an orphan.
	if _, err := storage.ApplyDelete(v.Root, storage.DeleteRequest{Projects: []string{"p"}, Discard: true, Expect: digest}); err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "p")); err == nil {
		t.Fatal("ApplyDelete did not remove Projects/p/")
	}
	if !removalStoreExists(t, v, "p") {
		t.Fatal("precondition: the index store should still exist (the removal was skipped by the crash)")
	}

	// The next sweep converges.
	rep := indexstore.ReapGoneProjects(context.Background(), v)
	if len(rep.Errors) > 0 {
		t.Fatalf("sweep errors: %v", rep.Errors)
	}
	if !slices.Contains(rep.Removed, "p") {
		t.Fatalf("the sweep did not remove the orphaned store: %+v", rep)
	}
	if removalStoreExists(t, v, "p") {
		t.Fatal("an orphaned index store survived the sweep")
	}
	if !removalStoreExists(t, v, "q") {
		t.Fatal("the sweep removed a live project's store")
	}
}

// Gap 5: a slug deleted and then RE-CREATED must start with a clean host-local
// index — no stale chunks from the old project surface, because the removal took
// the store and bumped the generation so the fingerprint reads missing.
func TestReCreatedSlugStartsWithACleanIndex(t *testing.T) {
	root, _ := removalRemoteVault(t, "p", "q")
	v := storage.NewVault(root)
	removalSeedStore(t, v, "p")

	// Prove the store really held a chunk before the delete.
	if before, err := indexstore.ReadStore(v, "p"); err != nil || len(before.Chunks(false)) == 0 {
		t.Fatalf("precondition: p's store should hold a chunk (chunks=%d err=%v)", func() int {
			if before == nil {
				return -1
			}
			return len(before.Chunks(false))
		}(), err)
	}

	plan := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "plan", Slugs: []string{"p"}, Discard: true})
	digest, _ := plan["digest"].(string)
	res := deleteToolCall(t, v, vaultProjectDeleteParams{Action: "apply", Slugs: []string{"p"}, Discard: true, Expect: digest})
	if entry := firstRemoval(t, res); entry["outcome"] != "removed" {
		t.Fatalf("index_removal outcome = %v, want removed", entry["outcome"])
	}
	if removalStoreExists(t, v, "p") {
		t.Fatal("the delete did not remove p's index store")
	}

	// Re-create the SAME slug.
	removalWrite(t, root, "Projects/p/resume.md", "# p\n")

	// The host-local index starts clean: fingerprint missing (unbuilt) and no
	// stale chunks readable.
	recipe := index.ChunkRecipe{IndexerVersion: index.IndexerVersion}
	if st, err := indexstore.ReadFingerprint(v, "p", recipe); err != nil || st != indexstore.FingerprintMissing {
		t.Fatalf("re-created slug fingerprint = %v (err %v), want missing (unbuilt)", st, err)
	}
	after, err := indexstore.ReadStore(v, "p")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(after.Chunks(false)); n != 0 {
		t.Fatalf("re-created slug surfaced %d stale chunk(s); want a clean index", n)
	}
}

// goneStoreMoved seeds an index store for slug (Projects/<slug> present so Lock
// admits it), then removes the Projects/ side and records the slug as departed to
// another vault, optionally leaving palace residue. With residue the slug is a
// store the sweep must KEEP; bare (only palace/<slug>/.local/) it is reapable.
func goneStoreMoved(t *testing.T, v *storage.Vault, slug string, residue bool) {
	t.Helper()
	removalWrite(t, v.Root, "Projects/"+slug+"/resume.md", "# "+slug+"\n")
	removalSeedStore(t, v, slug)
	if err := os.RemoveAll(filepath.Join(v.Root, "Projects", slug)); err != nil {
		t.Fatal(err)
	}
	rec, err := (departure.Record{Slug: slug, Kind: departure.MovedToVault, To: "git@example.test:x/other.git", Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	removalWrite(t, v.Root, departure.RelPath(slug), string(rec))
	if residue {
		removalWrite(t, v.Root, "palace/"+slug+"/kg/entities.jsonl", "{}\n")
	} else {
		removalWrite(t, v.Root, "palace/"+slug+"/.local/x", "x\n")
	}
}

// Gap 6: the moved-to-vault and residue cases were tested only at the predicate
// level (storage.Vault.IndexReapable). This runs them END TO END through the
// removal site (indexstore.ReapGoneProjects): a moved-to-vault slug whose palace
// side holds only .local/ is reaped, while one with palace residue (a regular
// non-derived file) is kept — the store stays for the vault it moved to.
func TestReapSweepHonoursMovedToVaultAndResidueEndToEnd(t *testing.T) {
	root, _ := removalRemoteVault(t, "live")
	v := storage.NewVault(root)
	// .gitignore must carry the lines the presence rule reads so a derived file
	// does not count as residue and palace/.local/ is ignored.
	removalWrite(t, root, ".gitignore", ".vp-locks/\npalace/.local/\n*.bak\n")

	goneStoreMoved(t, v, "movedbare", false) // reapable: palace holds only .local/
	goneStoreMoved(t, v, "movedres", true)   // kept: palace residue is a store

	// Predicate controls, so a mis-seeded fixture cannot make the e2e vacuous.
	if ok, _, err := v.IndexReapable("movedbare"); err != nil || !ok {
		t.Fatalf("fixture: movedbare should be reapable (%v, %v)", ok, err)
	}
	if ok, _, err := v.IndexReapable("movedres"); err != nil || ok {
		t.Fatalf("fixture: movedres should be kept (%v, %v)", ok, err)
	}

	rep := indexstore.ReapGoneProjects(context.Background(), v)
	if len(rep.Errors) > 0 {
		t.Fatalf("sweep errors: %v", rep.Errors)
	}
	if !slices.Contains(rep.Removed, "movedbare") {
		t.Fatalf("the moved-to-vault bare slug was not reaped end to end: %+v", rep)
	}
	if removalStoreExists(t, v, "movedbare") {
		t.Fatal("movedbare's store survived the sweep")
	}
	if slices.Contains(rep.Removed, "movedres") || !removalStoreExists(t, v, "movedres") {
		t.Fatalf("movedres's store was reaped despite palace residue: %+v", rep)
	}
}
