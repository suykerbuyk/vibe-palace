// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// goneStore gives project a store (chunks, a ledger) and a counter, then removes
// the project from the vault, so its store is reapable.
func goneStore(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", project), 0o755); err != nil {
		t.Fatal(err)
	}
	tx := mustLock(t)(Lock(context.Background(), v, project, NoTimeout))
	if err := tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("chunk of "+project, project, "general")}, "2026-05-01")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dir, _ := v.IndexDir(project)
	touch(t, filepath.Join(dir, ledgerFile))
	if err := os.RemoveAll(filepath.Join(v.Root, "Projects", project)); err != nil {
		t.Fatal(err)
	}
}

// treeOf returns every file under dir with its bytes.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func tombstones(t *testing.T, v *storage.Vault) []string {
	t.Helper()
	ts, err := v.IndexTombstones()
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func lockFiles(t *testing.T, v *storage.Vault) []string {
	t.Helper()
	ents, _ := os.ReadDir(v.IndexLocksDir())
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// The lifecycle form takes the commit lock without Lock's refusal of a gone
// project, exposes exactly three methods, and waits on a held lock like Lock.
func TestLifecycleLockForm(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "gamma")
	if _, err := Lock(context.Background(), v, "gamma", 0); !errors.Is(err, ErrProjectGone) {
		t.Fatalf("Lock on a gone project: %v, want ErrProjectGone", err)
	}
	lt, err := LockLifecycle(context.Background(), v, "gamma", 0)
	if err != nil {
		t.Fatalf("LockLifecycle on a gone project: %v", err)
	}
	if err := lt.Release(); err != nil {
		t.Fatal(err)
	}

	var names []string
	typ := reflect.TypeOf(&LifecycleTx{})
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	if want := []string{"ChangeEpoch", "Release", "RemoveProject"}; !slices.Equal(names, want) {
		t.Fatalf("LifecycleTx methods = %v, want exactly %v: a lifecycle form must not write records", names, want)
	}

	path, _ := v.IndexCommitLockPath("gamma")
	release, err := vaultlock.AcquireFileWithTimeout(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := LockLifecycle(context.Background(), v, "gamma", 0); !errors.Is(err, vaultlock.ErrLockWaitTimeout) {
		t.Fatalf("LockLifecycle against a held commit lock: %v, want ErrLockWaitTimeout", err)
	}
}

// RemoveProject re-checks, under the lock, the rule the scan used: a project
// that came back, palace residue that came back, or a rename-pending record that
// appeared between the scan and the lock stops it, and nothing changes.
func TestRemoveProjectReChecksUnderTheLock(t *testing.T) {
	put := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, between := range map[string]func(t *testing.T, v *storage.Vault){
		"re-created": func(t *testing.T, v *storage.Vault) {
			put(t, filepath.Join(v.Root, "Projects", "gamma", "resume.md"), "x\n")
		},
		"palace": func(t *testing.T, v *storage.Vault) {
			put(t, filepath.Join(v.Root, "palace", "gamma", "drawers.jsonl"), "x\n")
		},
		"pending": func(t *testing.T, v *storage.Vault) {
			p, _ := v.IndexRenamePendingPath("gamma")
			put(t, p, "alpha\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := newVault(t)
			goneStore(t, v, "gamma")
			cands, err := v.IndexReapCandidates()
			if err != nil || !slices.Contains(cands, "gamma") {
				t.Fatalf("scan: %v, %v; want gamma a candidate", cands, err)
			}
			dir, _ := v.IndexDir("gamma")
			before := treeOf(t, dir)
			gen, _ := ReadGeneration(v, "gamma")

			between(t, v)
			lt, err := LockLifecycle(context.Background(), v, "gamma", 0)
			if err != nil {
				t.Fatal(err)
			}
			removed, err := lt.RemoveProject()
			_ = lt.Release()
			if removed || !(errors.Is(err, ErrProjectLive) || errors.Is(err, ErrRenamePending)) {
				t.Fatalf("RemoveProject = %v, %v; want kept with ErrProjectLive or ErrRenamePending", removed, err)
			}
			if name == "pending" && !errors.Is(err, ErrRenamePending) {
				t.Fatalf("pending record: %v, want ErrRenamePending", err)
			}
			if !reflect.DeepEqual(treeOf(t, dir), before) {
				t.Fatal("the store changed")
			}
			if g, _ := ReadGeneration(v, "gamma"); g != gen {
				t.Fatalf("counter %v -> %v; a kept store's epoch must not change", gen, g)
			}
			if len(tombstones(t, v)) != 0 {
				t.Fatal("a tombstone was made")
			}
		})
	}
}

// RemoveProject bumps the epoch, then renames the store to a tombstone in one
// rename. A crash before the rename leaves the whole store; after it, the whole
// tombstone. Neither leaves a ledger without its chunks, the next sweep finishes
// the job, and a slug re-created after the rename starts unbuilt.
func TestRemoveProjectCrashPoints(t *testing.T) {
	for _, step := range []string{"checked", "bumped", "renamed"} {
		t.Run(step, func(t *testing.T) {
			v := newVault(t)
			goneStore(t, v, "gamma")
			dir, _ := v.IndexDir("gamma")
			before := treeOf(t, dir)
			crash := errors.New("crash at " + step)
			removeProjectHook = func(s string) error {
				if s == step {
					return crash
				}
				return nil
			}
			t.Cleanup(func() { removeProjectHook = nil })

			lt, err := LockLifecycle(context.Background(), v, "gamma", 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = lt.RemoveProject()
			_ = lt.Release()
			if !errors.Is(err, crash) {
				t.Fatalf("RemoveProject: %v, want the injected crash", err)
			}
			switch step {
			case "checked", "bumped":
				if !reflect.DeepEqual(treeOf(t, dir), before) || len(tombstones(t, v)) != 0 {
					t.Fatal("a crash before the rename must leave the whole store and no tombstone")
				}
			case "renamed":
				ts := tombstones(t, v)
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) || len(ts) != 1 {
					t.Fatalf("after the rename: store stat %v, tombstones %v; want the store gone and one tombstone", err, ts)
				}
				if !reflect.DeepEqual(treeOf(t, ts[0]), before) {
					t.Fatal("the tombstone is not the whole store")
				}
			}
			// Never a ledger without its chunks, at any point.
			if tr := treeOf(t, dir); tr[ledgerFile] != "" || (len(tr) > 0 && tr[chunksFile] == "") {
				t.Fatalf("store after the crash holds %v", tr)
			}

			removeProjectHook = nil
			rep := ReapGoneProjects(context.Background(), v)
			if len(rep.Errors) > 0 {
				t.Fatal(rep.Errors)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) || len(tombstones(t, v)) != 0 {
				t.Fatalf("the next sweep did not finish: store %v, tombstones %v", err, tombstones(t, v))
			}
			if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "gamma"), 0o755); err != nil {
				t.Fatal(err)
			}
			if st := mustFingerprint(t, v, "gamma", testRecipe); st != FingerprintMissing {
				t.Fatalf("a re-created slug reads %v, want missing (unbuilt)", st)
			}
		})
	}
}

// A sweep removes a gone project's store under its lock: the directory goes, the
// counter stays with a new epoch, the lock files stay, and a live sibling keeps
// its store.
func TestReapGoneProjectsRemovesUnderTheLock(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "gamma")
	mustTx(t, v, func(tx *Tx) error {
		return tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("alive", "alpha", "general")}, "2026-05-01"))
	})
	alphaDir, _ := v.IndexDir("alpha")
	alphaBefore := treeOf(t, alphaDir)
	genBefore, _ := ReadGeneration(v, "gamma")
	locksBefore := lockFiles(t, v)

	var events []lockEvent
	recordLocks(t, func(ev lockEvent) { events = append(events, ev) })
	rep := ReapGoneProjects(context.Background(), v)
	if len(rep.Errors) > 0 || !slices.Equal(rep.Removed, []string{"gamma"}) || rep.Tombstones != 1 {
		t.Fatalf("report %+v", rep)
	}
	dir, _ := v.IndexDir("gamma")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gamma's store still there: %v", err)
	}
	if g, err := ReadGeneration(v, "gamma"); err != nil || g.Epoch == genBefore.Epoch || g.Epoch == 0 {
		t.Fatalf("gamma's counter %v -> %v, %v; want kept with a new epoch", genBefore, g, err)
	}
	if !slices.Equal(lockFiles(t, v), locksBefore) {
		t.Fatalf("lock files %v -> %v", locksBefore, lockFiles(t, v))
	}
	if !reflect.DeepEqual(treeOf(t, alphaDir), alphaBefore) {
		t.Fatal("a live project's store changed")
	}
	acquired := false
	for _, ev := range events {
		if ev.kind == evCommitAcquired && ev.project == "gamma" {
			acquired = true
		}
	}
	if !acquired {
		t.Fatal("gamma's store was removed without its commit lock")
	}
}

// A sweep tries a busy lock once and moves on; the next sweep reaps it.
func TestReapGoneProjectsSkipsABusySlug(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "gamma")
	path, _ := v.IndexCommitLockPath("gamma")
	release, err := vaultlock.AcquireFileWithTimeout(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	within(t, "a sweep against a held lock", func() {
		rep := ReapGoneProjects(context.Background(), v)
		if rep.Kept["gamma"] != RemovalKeptBusy || len(rep.Removed) != 0 {
			t.Errorf("report %+v, want gamma kept busy", rep)
		}
	})
	dir, _ := v.IndexDir("gamma")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a busy store was touched: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if rep := ReapGoneProjects(context.Background(), v); !slices.Equal(rep.Removed, []string{"gamma"}) {
		t.Fatalf("after release: %+v", rep)
	}
}

// The rename-pending keep holds only on the host that has the record, for as
// long as it has it, whatever happens to the target; a stale record is reported
// on every pass, even when the old store is already gone.
func TestRenamePendingKeepOnTheRenamingHostOnly(t *testing.T) {
	setup := func(t *testing.T, withRecord bool) (*storage.Vault, string) {
		v := newVault(t)
		goneStore(t, v, "old")
		if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "new"), 0o755); err != nil {
			t.Fatal(err)
		}
		p, _ := v.IndexRenamePendingPath("old")
		if withRecord {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("new\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return v, p
	}
	oldDir := func(v *storage.Vault) string { d, _ := v.IndexDir("old"); return d }
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	t.Run("a: the renaming host keeps it", func(t *testing.T) {
		v, _ := setup(t, true)
		rep := ReapGoneProjects(context.Background(), v)
		if !exists(oldDir(v)) {
			t.Fatal("the renaming host reaped the store its rename has still to move")
		}
		if rep.StaleRenamePending["old"] != "new" {
			t.Fatalf("stale record not reported: %+v", rep)
		}
	})
	t.Run("b: another host reaps it", func(t *testing.T) {
		v, _ := setup(t, false)
		ReapGoneProjects(context.Background(), v)
		if exists(oldDir(v)) {
			t.Fatal("a host with no record kept the store")
		}
	})
	t.Run("c: kept when the target is deleted", func(t *testing.T) {
		v, _ := setup(t, true)
		if err := os.RemoveAll(filepath.Join(v.Root, "Projects", "new")); err != nil {
			t.Fatal(err)
		}
		ReapGoneProjects(context.Background(), v)
		if !exists(oldDir(v)) {
			t.Fatal("the target's state ended the keep")
		}
	})
	t.Run("d: kept when another writer built the target, reaped once the record goes", func(t *testing.T) {
		v, p := setup(t, true)
		mustTxOn(t, v, "new", func(tx *Tx) error {
			return tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("new chunk", "new", "general")}, "2026-05-01"))
		})
		ReapGoneProjects(context.Background(), v)
		if !exists(oldDir(v)) {
			t.Fatal("index/new/ appearing ended the keep")
		}
		if err := os.Remove(p); err != nil { // what vp index rebuild new does
			t.Fatal(err)
		}
		ReapGoneProjects(context.Background(), v)
		if exists(oldDir(v)) {
			t.Fatal("with the record gone the store was kept")
		}
	})
	t.Run("e: a record whose store is gone is still reported", func(t *testing.T) {
		v, _ := setup(t, true)
		if err := os.RemoveAll(oldDir(v)); err != nil {
			t.Fatal(err)
		}
		if rep := ReapGoneProjects(context.Background(), v); rep.StaleRenamePending["old"] != "new" {
			t.Fatalf("report %+v, want the stale record reported", rep)
		}
	})
	t.Run("a running rename is not reported", func(t *testing.T) {
		v, _ := setup(t, true)
		rl := mustTryRunLock(t)(TryRunLock(v, KindLifecycle, "old"))
		defer rl.Release()
		if rep := ReapGoneProjects(context.Background(), v); len(rep.StaleRenamePending) != 0 {
			t.Fatalf("a running rename's record was reported stale: %+v", rep)
		}
	})
}

// mustTxOn is mustTx for another project.
func mustTxOn(t *testing.T, v *storage.Vault, project string, f func(tx *Tx) error) {
	t.Helper()
	tx := mustLock(t)(Lock(context.Background(), v, project, NoTimeout))
	err := f(tx)
	if cerr := tx.Release(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}

// RemoveGoneProject, the delete and split callers' entry point, reports each
// outcome, and is called with no vault root lock held.
func TestRemoveGoneProjectOutcomes(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "gamma")
	if out, err := RemoveGoneProject(context.Background(), v, "alpha", time.Second); err != nil || out != RemovalKeptLive {
		t.Fatalf("live project: %v, %v", out, err)
	}
	if out, err := RemoveGoneProject(context.Background(), v, "nosuch", time.Second); err != nil || out != RemovalAbsent {
		t.Fatalf("no store: %v, %v", out, err)
	}
	removeProjectHook = func(step string) error {
		if step == "checked" {
			release, ok, err := vaultlock.TryAcquire(v.Root, v.Root)
			if err != nil || !ok {
				t.Errorf("the vault root lock is held while the index commit lock is: ok=%v err=%v", ok, err)
				return nil
			}
			_ = release()
		}
		return nil
	}
	t.Cleanup(func() { removeProjectHook = nil })
	if out, err := RemoveGoneProject(context.Background(), v, "gamma", time.Second); err != nil || out != RemovalRemoved {
		t.Fatalf("gone project: %v, %v", out, err)
	}
	got := RemoveGoneProjects(context.Background(), v, []string{"gamma", "alpha"}, time.Second)
	if len(got) != 2 || got[0].Outcome != RemovalAbsent || got[1].Outcome != RemovalKeptLive || !strings.HasSuffix(got[0].Path, "/gamma/") {
		t.Fatalf("RemoveGoneProjects = %+v", got)
	}
}

// A damaged rename-pending record (a target that is not a slug) is not proof
// that no rename is pending: the store is KEPT, and the sweep reports the error.
func TestDamagedRenamePendingRecordKeepsTheStore(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "old")
	p, _ := v.IndexRenamePendingPath("old")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("Not A Slug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := ReapGoneProjects(context.Background(), v)
	dir, _ := v.IndexDir("old")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a store with a damaged rename-pending record was reaped: %v", err)
	}
	if len(rep.Errors) == 0 {
		t.Fatal("the damaged record was not reported")
	}
}

// The tombstone's name carries the epoch the removal set, so removing a
// re-created slug again never collides with an uncollected tombstone.
func TestTombstoneNameCarriesTheEpoch(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "gamma")
	removeProjectHook = func(s string) error {
		if s == "renamed" {
			return errors.New("stop before the sweep collects it")
		}
		return nil
	}
	t.Cleanup(func() { removeProjectHook = nil })
	lt, err := LockLifecycle(context.Background(), v, "gamma", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lt.RemoveProject()
	_ = lt.Release()
	g, err := ReadGeneration(v, "gamma")
	if err != nil {
		t.Fatal(err)
	}
	ts := tombstones(t, v)
	want := ".tomb-gamma-" + strconv.FormatUint(g.Epoch, 16)
	if len(ts) != 1 || filepath.Base(ts[0]) != want {
		t.Fatalf("tombstones %v, want one named %s", ts, want)
	}
}

// A crashed rename leaves its holder record behind with no run lock held. That
// stale record must not hide the stale rename-pending warning forever.
func TestCrashedRenameHolderDoesNotHideTheWarning(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "old")
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := v.IndexRenamePendingPath("old")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(v.IndexLocksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	holder := `{"pid":424242,"kind":"lifecycle","project":"old","start_time":"2026-10-03T00:00:00Z"}`
	if err := os.WriteFile(v.IndexRunHolderPath(), []byte(holder), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep := ReapGoneProjects(context.Background(), v); rep.StaleRenamePending["old"] != "new" {
		t.Fatalf("report %+v: a crashed run's holder record hid the stale record", rep)
	}
}

// With no store present, RemoveProject changes nothing: no epoch change on an
// existing counter, and no counter created for a project that never had one.
func TestRemoveProjectWithNoStoreChangesNothing(t *testing.T) {
	v := newVault(t)
	goneStore(t, v, "gamma")
	dir, _ := v.IndexDir("gamma")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	before, _ := ReadGeneration(v, "gamma")
	if out, err := RemoveGoneProject(context.Background(), v, "gamma", time.Second); err != nil || out != RemovalAbsent {
		t.Fatalf("no store: %v, %v; want absent", out, err)
	}
	if after, _ := ReadGeneration(v, "gamma"); after != before {
		t.Fatalf("counter %v -> %v with no store to remove", before, after)
	}
	if out, err := RemoveGoneProject(context.Background(), v, "never", time.Second); err != nil || out != RemovalAbsent {
		t.Fatalf("never-built project: %v, %v", out, err)
	}
	gp, _ := v.IndexGenerationPath("never")
	if _, err := os.Stat(gp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a counter was created for a project that never had a store: %v", err)
	}
}
