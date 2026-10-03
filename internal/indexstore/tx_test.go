// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// mustLock(t)(Lock(...)) returns the Tx or fails the test.
func mustLock(t *testing.T) func(*Tx, error) *Tx {
	t.Helper()
	return func(tx *Tx, err error) *Tx {
		t.Helper()
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		return tx
	}
}

// Both index locks live in palace/.local/locks/, nothing lock-related is
// created under index/<p>/ (which a discard deletes), and nothing goes under
// .vp-locks/. Deleting index/<p>/ while a helper holds the commit lock does
// not let anyone else in.
func TestLocksLiveInPalaceLocalLocks(t *testing.T) {
	v := newVault(t)
	rl := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	tx := mustLock(t)(Lock(context.Background(), v, "alpha", 0))

	locks, err := os.ReadDir(v.IndexLocksDir())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range locks {
		names = append(names, e.Name())
	}
	want := []string{"index-commit-alpha.lock", "index-run.holder", "index-run.lock"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("palace/.local/locks holds %v, want %v", names, want)
	}
	dir, _ := v.IndexDir("alpha")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("taking the locks created index/alpha/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Root, ".vp-locks")); !os.IsNotExist(err) {
		t.Fatalf("a .vp-locks directory was created: %v", err)
	}
	_ = tx.Release()
	_ = rl.Release()

	h := startHelper(t, v, "commit-hold")
	h.ready(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	within(t, "Lock with a zero timeout", func() {
		if tx, err := Lock(context.Background(), v, "alpha", 0); !errors.Is(err, vaultlock.ErrLockWaitTimeout) {
			if tx != nil {
				_ = tx.Release()
			}
			t.Errorf("Lock while a helper holds it, after index/alpha/ was deleted: err=%v, want ErrLockWaitTimeout", err)
		}
	})
	h.finish(t)
}

// The commit lock honours its timeout and its context, and neither path
// acquires it.
func TestCommitLockTimesOutAndHonoursItsContext(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "commit-hold")
	h.ready(t)

	var mu sync.Mutex
	acquired := 0
	recordLocks(t, func(ev lockEvent) {
		if ev.kind == evCommitAcquired {
			mu.Lock()
			acquired++
			mu.Unlock()
		}
	})
	within(t, "Lock with a zero timeout", func() {
		if _, err := Lock(context.Background(), v, "alpha", 0); !errors.Is(err, vaultlock.ErrLockWaitTimeout) {
			t.Errorf("err=%v, want ErrLockWaitTimeout", err)
		}
	})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, timeout := range []struct {
		name string
		d    time.Duration
	}{{"NoTimeout", NoTimeout}, {"zero", 0}} {
		within(t, "Lock with a cancelled context and "+timeout.name, func() {
			if _, err := Lock(cancelled, v, "alpha", timeout.d); !errors.Is(err, context.Canceled) {
				t.Errorf("err=%v, want context.Canceled", err)
			}
		})
	}
	mu.Lock()
	if acquired != 0 {
		t.Fatalf("a refused Lock acquired the commit lock %d times", acquired)
	}
	mu.Unlock()
	h.finish(t)
}

// The commit lock is a leaf. TestMain's checker watches every lock operation
// in this binary; here it is shown to catch a nested commit lock and a run
// lock tried under a commit lock, and a mixed-project commit taken one project
// at a time passes it. An AST check finds no Lock or TryRunLock call inside a
// *Tx method.
func TestCommitLockIsALeaf(t *testing.T) {
	v := newVault(t)
	for _, p := range []string{"alpha", "beta"} {
		tx := mustLock(t)(Lock(context.Background(), v, p, NoTimeout))
		tx.noteWrite(false)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// The checker itself: feed it a nesting and see it complain. A private
	// checker, so the binary-wide one stays clean.
	c := &leafChecker{held: map[uint64][]string{}}
	c.record(lockEvent{kind: evCommitWait, project: "alpha"})
	c.record(lockEvent{kind: evCommitAcquired, project: "alpha"})
	c.record(lockEvent{kind: evCommitWait, project: "beta"})
	c.record(lockEvent{kind: evRunTry})
	c.record(lockEvent{kind: evCommitReleased, project: "alpha"})
	c.record(lockEvent{kind: evCommitWait, project: "beta"})
	if got := len(c.violations()); got != 2 {
		t.Fatalf("leaf checker reported %d violations for one nested commit lock and one run try under it, want 2: %v", got, c.violations())
	}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Tx" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "Lock" || id.Name == "TryRunLock") {
					t.Errorf("%s: (*Tx).%s calls %s: the commit lock is a leaf", fset.Position(call.Pos()), fd.Name.Name, id.Name)
				}
				return true
			})
		}
	}
}

// A project removed while Lock waits is detected under the lock: Lock returns
// ErrProjectGone, and neither index/<p>/ nor its counter is created.
func TestRemovedProjectIsNotRecreated(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "commit-hold", "VP_INDEXSTORE_DELETE=1")
	h.ready(t)
	genPath, _ := v.IndexGenerationPath("alpha")
	_ = os.Remove(genPath) // the helper's Lock created it; start from none

	// Tell the helper to delete the project only once this Lock has passed
	// every check it makes before waiting.
	commitWaitHook = func() { touch(t, h.file("go")) }
	defer func() { commitWaitHook = nil }()
	within(t, "Lock on a project removed while waiting", func() {
		tx, err := Lock(context.Background(), v, "alpha", NoTimeout)
		if !errors.Is(err, ErrProjectGone) {
			if tx != nil {
				_ = tx.Release()
			}
			t.Errorf("err=%v, want ErrProjectGone", err)
		}
	})
	h.wait(t)
	dir, _ := v.IndexDir("alpha")
	for _, p := range []string{dir, genPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists after Lock refused a removed project: %v", p, err)
		}
	}
}

// Generation tells each holder of in-memory state, by comparison with the
// value it remembers, whether another writer changed the store. A holder's own
// Commit leaves Generation at the counter it wrote, so it does not re-read its
// own write; and one holder's Lock does not consume the signal for another.
func TestGenerationTellsEachHolderWhatChanged(t *testing.T) {
	v := newVault(t)
	write := func() Gen {
		t.Helper()
		tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
		tx.noteWrite(false)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return tx.Generation()
	}
	look := func() Gen {
		t.Helper()
		tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
		defer tx.Release()
		return tx.Generation()
	}

	engine := write() // the engine's own write; it remembers what it left
	if g, _ := ReadGeneration(v, "alpha"); g != engine {
		t.Fatalf("Generation after Commit = %+v, the counter on disk is %+v", engine, g)
	}
	if g := look(); g != engine {
		t.Fatalf("the engine's next Lock sees %+v after its own write left %+v: it would re-read its own write", g, engine)
	}

	h := startHelper(t, v, "commit-write")
	h.wait(t)
	noteWriter := look() // another holder locks first ...
	if noteWriter == engine {
		t.Fatal("a holder did not see another process's write")
	}
	if g := look(); g == engine { // ... and the engine still sees the change
		t.Fatal("the engine's Lock no longer sees another process's write after a different holder locked first")
	}
}

// A finite timeout does not stop Lock from noticing a cancel during its wait:
// the context is cancelled only once Lock is known to be waiting on a held
// lock, and the timeout is far longer than the liveness bound.
func TestLockNoticesACancelDuringAFiniteWait(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "commit-hold")
	h.ready(t)
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	restore := vaultlock.SetWaitHook(func() { once.Do(cancel) })
	defer restore()
	within(t, "Lock cancelled during a finite wait", func() {
		if tx, err := Lock(ctx, v, "alpha", 10*liveness); !errors.Is(err, context.Canceled) {
			if tx != nil {
				_ = tx.Release()
			}
			t.Errorf("err=%v, want context.Canceled", err)
		}
	})
	h.finish(t)
}

// Every file this package writes or removes goes through write.go, so every
// write is fsynced with its directory and every rename and removal is retried
// on Windows: no non-test file calls a raw os write or removal.
func TestWritesGoThroughAtomicfile(t *testing.T) {
	banned := map[string]bool{"WriteFile": true, "Rename": true, "Remove": true, "RemoveAll": true, "Create": true, "CreateTemp": true, "OpenFile": true, "Truncate": true}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && banned[sel.Sel.Name] {
				t.Errorf("%s: os.%s; write and remove through writeFile and removeFile", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
