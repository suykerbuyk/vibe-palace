// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Multi-process tests re-exec this test binary as a helper process. The
// helper is selected by helperModeEnv and synchronises with the parent only
// through files in helperDirEnv and its exit code, never through sleeps.
const (
	helperModeEnv  = "VP_INDEXSTORE_HELPER"
	helperVaultEnv = "VP_INDEXSTORE_VAULT"
	helperDirEnv   = "VP_INDEXSTORE_SYNC"
)

// liveness bounds every wait in these tests. It is a deadlock detector, not a
// performance assertion: every passing path returns without waiting on it.
const liveness = 60 * time.Second

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(runHelper(mode))
	}
	lockRecorder = leaf.record
	code := m.Run()
	if v := leaf.violations(); len(v) > 0 {
		fmt.Fprintln(os.Stderr, "index commit lock is not a leaf:")
		for _, s := range v {
			fmt.Fprintln(os.Stderr, "  "+s)
		}
		code = 1
	}
	os.Exit(code)
}

// ---- the leaf checker ----------------------------------------------------

// leafChecker sees every lock operation in the whole test binary and records
// a violation when a goroutine takes a second index commit lock, or tries the
// run lock, while it holds one (ADR-014 decision 7, "Lock order").
//
// Known limit: it is local to this package's tests. A later package that
// takes the index locks (the ingester, the rebuild driver, the search engine)
// cannot install it until it moves to an exported test-support package, which
// pending-archive-ingester-and-per-archive-commit-step, the first such
// consumer, should do.
type leafChecker struct {
	mu    sync.Mutex
	held  map[uint64][]string // goroutine -> commit locks it holds
	bad   []string
	extra func(lockEvent) // a test's own recorder, chained after the check
}

var leaf = &leafChecker{held: map[uint64][]string{}}

func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	f := bytes.Fields(buf[:n])
	id, _ := strconv.ParseUint(string(f[1]), 10, 64)
	return id
}

func (c *leafChecker) record(ev lockEvent) {
	g := goroutineID()
	c.mu.Lock()
	switch ev.kind {
	case evCommitWait:
		if len(c.held[g]) > 0 {
			c.bad = append(c.bad, fmt.Sprintf("goroutine %d waits for the commit lock of %q while holding %v", g, ev.project, c.held[g]))
		}
	case evCommitAcquired:
		c.held[g] = append(c.held[g], ev.project)
	case evCommitReleased:
		h := c.held[g]
		for i := len(h) - 1; i >= 0; i-- {
			if h[i] == ev.project {
				c.held[g] = append(h[:i], h[i+1:]...)
				break
			}
		}
	case evRunTry:
		if len(c.held[g]) > 0 {
			c.bad = append(c.bad, fmt.Sprintf("goroutine %d tries the run lock while holding the commit lock of %v", g, c.held[g]))
		}
	}
	extra := c.extra
	c.mu.Unlock()
	if extra != nil {
		extra(ev)
	}
}

func (c *leafChecker) violations() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bad...)
}

// recordLocks installs a per-test recorder for the duration of the test.
func recordLocks(t *testing.T, f func(lockEvent)) {
	t.Helper()
	leaf.mu.Lock()
	leaf.extra = f
	leaf.mu.Unlock()
	t.Cleanup(func() {
		leaf.mu.Lock()
		leaf.extra = nil
		leaf.mu.Unlock()
	})
}

// ---- fixtures --------------------------------------------------------------

// newVault returns a vault in a temp dir with projects alpha and beta.
func newVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, "Projects", p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return storage.NewVault(root)
}

func touch(t testing.TB, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitFile waits for path to exist, polling. It returns false at the liveness
// bound, which only a broken lock (or a crashed helper) reaches.
func waitFile(path string) bool {
	deadline := time.Now().Add(liveness)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func mustWaitFile(t *testing.T, path string) {
	t.Helper()
	if !waitFile(path) {
		t.Fatalf("timed out waiting for %s", filepath.Base(path))
	}
}

// within runs f and fails the test if it has not returned by the liveness
// bound: a lock that blocks where it must not fails here, with a message,
// instead of hanging until go test's package timeout.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(liveness):
		t.Fatalf("%s did not return", what)
	}
}

// helper is a running helper process.
type helper struct {
	cmd *exec.Cmd
	dir string
	out bytes.Buffer
}

// startHelper re-execs the test binary in helper mode against vault.
func startHelper(t *testing.T, vault *storage.Vault, mode string, env ...string) *helper {
	t.Helper()
	h := &helper{dir: t.TempDir()}
	h.cmd = exec.Command(os.Args[0], "-test.run=^$")
	h.cmd.Env = append(os.Environ(),
		helperModeEnv+"="+mode,
		helperVaultEnv+"="+vault.Root,
		helperDirEnv+"="+h.dir,
	)
	h.cmd.Env = append(h.cmd.Env, env...)
	h.cmd.Stdout = &h.out
	h.cmd.Stderr = &h.out
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if h.cmd.ProcessState == nil {
			_ = h.cmd.Process.Kill()
			_ = h.cmd.Wait()
		}
	})
	return h
}

func (h *helper) file(name string) string { return filepath.Join(h.dir, name) }

// ready waits for the helper to signal that it holds what it was asked to hold.
func (h *helper) ready(t *testing.T) {
	t.Helper()
	if !waitFile(h.file("ready")) {
		t.Fatalf("helper never became ready; output:\n%s", h.out.String())
	}
}

// finish tells the helper to go on, and waits for it to exit cleanly.
func (h *helper) finish(t *testing.T) {
	t.Helper()
	touch(t, h.file("go"))
	h.wait(t)
}

func (h *helper) wait(t *testing.T) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper failed: %v; output:\n%s", err, h.out.String())
		}
	case <-time.After(liveness):
		t.Fatalf("helper did not exit; output:\n%s", h.out.String())
	}
}

// ---- helper modes ----------------------------------------------------------

// noVectors is a VectorWriter that writes nothing, for helpers.
type noVectors struct{}

func (noVectors) Put(string, string, []float32) error { return nil }

func runHelper(mode string) int {
	vault := storage.NewVault(os.Getenv(helperVaultEnv))
	dir := os.Getenv(helperDirEnv)
	sig := func(name string) error { return os.WriteFile(filepath.Join(dir, name), nil, 0o644) }
	await := func(name string) bool { return waitFile(filepath.Join(dir, name)) }
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "helper %s: "+format+"\n", append([]any{mode}, a...)...)
		return 2
	}

	switch mode {
	case "run-hold":
		// Take the run lock, optionally report progress, signal, and hold
		// until told to go (or until killed).
		rl, ok, err := TryRunLock(vault, KindIngest, "alpha")
		if err != nil || !ok {
			return fail("TryRunLock: ok=%v err=%v", ok, err)
		}
		if os.Getenv("VP_INDEXSTORE_PROGRESS") != "" {
			if err := rl.SetProgress(3, 10); err != nil {
				return fail("SetProgress: %v", err)
			}
		}
		if err := sig("ready"); err != nil {
			return fail("%v", err)
		}
		if !await("go") {
			return fail("never told to go")
		}
		if err := rl.Release(); err != nil {
			return fail("Release: %v", err)
		}
		return 0

	case "commit-hold":
		// Take alpha's commit lock and hold it until told to go. With
		// VP_INDEXSTORE_DELETE set, delete the project from the vault before
		// releasing.
		tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
		if err != nil {
			return fail("Lock: %v", err)
		}
		if err := sig("ready"); err != nil {
			return fail("%v", err)
		}
		if !await("go") {
			return fail("never told to go")
		}
		if os.Getenv("VP_INDEXSTORE_DELETE") != "" {
			for _, tree := range []string{"Projects", "palace"} {
				if err := os.RemoveAll(filepath.Join(vault.Root, tree, "alpha")); err != nil {
					return fail("delete project: %v", err)
				}
			}
		}
		if err := tx.Release(); err != nil {
			return fail("Release: %v", err)
		}
		return 0

	case "append-same", "append-distinct":
		// Append 1,000 chunks to alpha in 100 commits of 10, after the go
		// signal, so two helpers interleave. append-same uses the same
		// contents in every helper; append-distinct prefixes them.
		prefix := ""
		if mode == "append-distinct" {
			prefix = os.Getenv("VP_INDEXSTORE_PREFIX")
		}
		if err := sig("ready"); err != nil {
			return fail("%v", err)
		}
		if !await("go") {
			return fail("never told to go")
		}
		for b := 0; b < 100; b++ {
			tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
			if err != nil {
				return fail("Lock: %v", err)
			}
			var recs []OwnedChunk
			for i := 0; i < 10; i++ {
				recs = append(recs, ownedChunk(fmt.Sprintf("%schunk %d", prefix, b*10+i), "w", "r"))
			}
			if err := tx.Append(NoteOwner("notes/n.md"), withDay(recs, "2026-05-01")); err != nil {
				_ = tx.Release()
				return fail("Append: %v", err)
			}
			if err := tx.Commit(); err != nil {
				return fail("Commit: %v", err)
			}
		}
		return 0

	case "append-one":
		// One Append of one chunk to alpha.
		tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
		if err != nil {
			return fail("Lock: %v", err)
		}
		recs := withDay([]OwnedChunk{ownedChunk(os.Getenv("VP_INDEXSTORE_CONTENT"), "alpha", "d")}, "2026-05-13")
		if err := tx.Append(NoteOwner("n"), recs); err != nil {
			_ = tx.Release()
			return fail("Append: %v", err)
		}
		if err := tx.Commit(); err != nil {
			return fail("Commit: %v", err)
		}
		return 0

	case "commit-A-W":
		// Commit archive A of session S with chunks X, Y and W after the go
		// signal, and record the outcome in "result": ok, or superseded.
		if err := sig("ready"); err != nil {
			return fail("%v", err)
		}
		if !await("go") {
			return fail("never told to go")
		}
		tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
		if err != nil {
			return fail("Lock: %v", err)
		}
		result := "ok"
		err = tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y", "W"), noVectors{})
		switch {
		case errors.Is(err, ErrSuperseded):
			result = "superseded"
		case err != nil:
			_ = tx.Release()
			return fail("CommitArchive: %v", err)
		}
		if err := tx.Commit(); err != nil {
			return fail("Commit: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "result"), []byte(result), 0o644); err != nil {
			return fail("%v", err)
		}
		if err := sig("done"); err != nil {
			return fail("%v", err)
		}
		return 0

	case "discard-crash":
		// Start a chunk discard of alpha and die after it removed the ledger
		// and the chunks: exit without releasing anything, as a killed
		// process would. The OS drops the commit lock.
		tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
		if err != nil {
			return fail("Lock: %v", err)
		}
		commitStep = func(step string) error {
			if step == "discard-chunks" {
				os.Exit(0)
			}
			return nil
		}
		_ = tx.Discard(DiscardChunks)
		return fail("the discard did not reach discard-chunks")

	case "ingest-loop":
		// Ingest 100 archives of two chunks each into alpha, one commit step
		// each, after the go signal; signal "done" at the end.
		if err := sig("ready"); err != nil {
			return fail("%v", err)
		}
		if !await("go") {
			return fail("never told to go")
		}
		for i := 0; i < 100; i++ {
			tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
			if err != nil {
				return fail("Lock: %v", err)
			}
			if _, err := tx.EnsureLedger(nil); err != nil {
				return fail("EnsureLedger: %v", err)
			}
			c := commitOf(fmt.Sprintf("S%d", i), fmt.Sprintf("sha%d", i), "2026-05-13",
				fmt.Sprintf("chunk %d a", i), fmt.Sprintf("chunk %d b", i))
			if err := tx.CommitArchive(c, fileVW{vault}); err != nil {
				_ = tx.Release()
				return fail("CommitArchive: %v", err)
			}
			if err := tx.Commit(); err != nil {
				return fail("Commit: %v", err)
			}
		}
		if err := sig("done"); err != nil {
			return fail("%v", err)
		}
		return 0

	case "commit-write":
		// One Tx on alpha that wrote something (an append).
		tx, err := Lock(context.Background(), vault, "alpha", NoTimeout)
		if err != nil {
			return fail("Lock: %v", err)
		}
		tx.noteWrite(false)
		if err := tx.Commit(); err != nil {
			return fail("Commit: %v", err)
		}
		return 0
	}
	return fail("unknown mode")
}
