// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// renameAmbientSnapshot records every entry under root (kind, size, mode, mtime).
func renameAmbientSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snap[p] = fmt.Sprintf("%v %d %v %d", d.IsDir(), info.Size(), info.Mode(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

// A rename of the operator's configured vault is refused before it takes the
// root lock or writes the lifecycle marker. The shared empty-vaultPath arm also
// catches WriteRenamePending, but by then the marker is written, and a panic
// skips ApplyRename's error-only rollback and would leave it behind.
//
// The vault is a temp directory registered as the ambient root.
func TestApplyRenameRefusesAmbientVault(t *testing.T) {
	f := newRenFix(t)
	marker, err := lifecycleMarkerPath(f.V)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := NewVault(f.V).IndexRenamePendingPath("old")
	if err != nil {
		t.Fatal(err)
	}
	restore := surface.SetAmbientVaultRoot(f.V)
	t.Cleanup(restore)

	before := renameAmbientSnapshot(t, f.V)
	var rec any
	func() {
		defer func() { rec = recover() }()
		_, _ = ApplyRename(f.req())
	}()
	if rec == nil {
		t.Fatal("ApplyRename of the ambient vault returned instead of refusing")
	}
	if msg := fmt.Sprint(rec); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("refusal came from the wrong place: %v", rec)
	}
	if _, err := os.Lstat(marker); err == nil {
		t.Errorf("lifecycle marker %s was left behind", marker)
	}
	if _, err := os.Lstat(pending); err == nil {
		t.Errorf("rename-pending record %s was left behind", pending)
	}
	if _, err := os.Lstat(filepath.Join(f.V, ".vp-locks")); err == nil {
		t.Error(".vp-locks/ was created before the refusal")
	}
	if after := renameAmbientSnapshot(t, f.V); !reflect.DeepEqual(before, after) {
		t.Errorf("the refusal came after a filesystem effect: %v", snapDiff(before, after))
	}
	if out := gitRun(t, f.V, "status", "--porcelain"); out != "" {
		t.Errorf("the vault worktree is not clean after the refusal:\n%s", out)
	}

	// The root lock must be takeable afterwards. AcquireHeld has no timeout, so
	// the take runs in a goroutine and a stranded lock fails the test instead of
	// hanging it.
	restore()
	done := make(chan error, 1)
	go func() {
		h, err := vaultlock.AcquireHeld(f.V, f.V)
		if err == nil {
			err = h.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("root lock after the refusal: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the vault root lock is held after the refusal")
	}
}

// TestApplyRenameDryRunIsRefusedBeforeTheLockDir pins that a DRY run of
// ApplyRename against the ambient vault is refused, and refused BEFORE
// <vault>/.vp-locks exists.
//
// 🔴 THIS ROW USED TO SAY THE OPPOSITE ("a dry run is read-only, so the ambient
// vault is not refused for one"), and the premise was wrong at the lock.
// ApplyRename takes the vault commit lock (AcquireHeld, project_rename.go) before
// it knows the run is a dry one, and taking that lock creates
// <vault>/.vp-locks/ — a write to the vault, which task
// `append-entry-paths-touch-the-vault-before-the-tripwire-fires` forbids under
// test. So the dry run is now refused at the vaultlock seat, as the first
// statement of the lock acquisition and with nothing held. Production is
// unaffected: the guard is a no-op outside a go test binary.
//
// The ABSENCE of .vp-locks is the assertion that discriminates; "it panicked"
// alone would also pass if the seat sat after the MkdirAll. The fixture is
// checked first, so an absent directory cannot mean "the call never ran" (a
// control with a non-ambient root does create it).
func TestApplyRenameDryRunIsRefusedBeforeTheLockDir(t *testing.T) {
	// Control: against a non-ambient vault the dry run succeeds and DOES create
	// the lock directory, so the assertion below can discriminate.
	ctl := newRenFix(t)
	creq := ctl.req()
	creq.DryRun = true
	if _, err := ApplyRename(creq); err != nil {
		t.Fatalf("control dry run on a non-ambient vault: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(ctl.V, ".vp-locks")); err != nil {
		t.Fatalf("control: the dry run did not create .vp-locks (%v), so this row cannot discriminate", err)
	}

	f := newRenFix(t)
	restore := surface.SetAmbientVaultRoot(f.V)
	t.Cleanup(restore)
	req := f.req()
	req.DryRun = true
	if _, err := os.Lstat(filepath.Join(f.V, ".vp-locks")); err == nil {
		t.Fatal("fixture: .vp-locks already exists in the ambient vault before the call")
	}
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		_, _ = ApplyRename(req)
	}()
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("a dry run of the ambient vault was not refused, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Join(f.V, ".vp-locks")); err == nil {
		t.Error(".vp-locks was created in the ambient vault before the refusal")
	}
}

// snapDiff names the entries that differ between two snapshots, so a failure
// prints what changed rather than the whole tree.
func snapDiff(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if b, ok := before[k]; !ok {
			out = append(out, "created: "+k)
		} else if b != v {
			out = append(out, "changed: "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, "removed: "+k)
		}
	}
	sort.Strings(out)
	return out
}

// RemoveRenamePending deletes a file inside the vault's index tree, so under
// the tripwire it is refused and the record survives.
func TestRemoveRenamePendingRefusesAmbientVault(t *testing.T) {
	v := NewVault(t.TempDir())
	if err := v.WriteRenamePending("old", "new"); err != nil {
		t.Fatal(err)
	}
	pending, err := v.IndexRenamePendingPath("old")
	if err != nil {
		t.Fatal(err)
	}
	restore := surface.SetAmbientVaultRoot(v.Root)
	t.Cleanup(restore)

	var rec any
	func() {
		defer func() { rec = recover() }()
		_ = v.RemoveRenamePending("old")
	}()
	if rec == nil {
		t.Fatal("RemoveRenamePending of the ambient vault returned instead of refusing")
	}
	if msg := fmt.Sprint(rec); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("refusal came from the wrong place: %v", rec)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Errorf("the rename-pending record was removed despite the refusal: %v", err)
	}
}
