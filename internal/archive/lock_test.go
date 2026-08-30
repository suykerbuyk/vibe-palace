// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// lockTestNow pins the archive stem so every writer in a test contends on ONE
// manifest path. A drifting clock would give each goroutine its own manifest and
// the test would pass with the lock deleted.
var lockTestNow = time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

// mustFinishWithin runs fn and fails LOUDLY if it does not return in time.
//
// This is not decoration. vaultlock's exclusive lock is a bare LOCK_EX with no
// LOCK_NB and no timeout, and it does not recurse — so a regression that
// double-acquires one manifest does not error, it hangs forever. Without a
// watchdog that failure mode wedges the whole package until the go test timeout
// kills it with a stack dump and no explanation.
func mustFinishWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not complete within %s — vaultlock is a non-reentrant, "+
			"timeout-free LOCK_EX, so a stray second acquire of the same manifest "+
			"hangs permanently rather than erroring", what, d)
	}
}

// seedForLock creates a vault with one archived session and returns the vault
// root, the source path, and the CreateResult. The source can then be rewritten
// to force the .bak arm.
func seedForLock(t *testing.T, sessionID string, body []byte) (vaultRoot, srcPath string, res *CreateResult) {
	t.Helper()
	tmp := t.TempDir()
	vaultRoot = filepath.Join(tmp, "vault")
	srcPath = filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(srcPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Create(CreateOptions{
		Adapter:     ClaudeCodeAdapterName,
		SessionID:   sessionID,
		SourcePath:  srcPath,
		VaultRoot:   vaultRoot,
		ProjectSlug: "demo",
		Now:         lockTestNow,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	return vaultRoot, srcPath, r
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// bigJSONL builds a source large enough that Create's compress step keeps the
// manifest lock held for a measurable interval. A racing writer needs a window
// to lose an update INTO; a 200-byte fixture closes it faster than the scheduler
// can interleave, which is how a concurrency test ends up passing either way.
func bigJSONL(lines int) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"permission-mode","permissionMode":"default","sessionId":"lock-test"}` + "\n")
	for i := 0; i < lines; i++ {
		b.WriteString(`{"type":"user","message":{"role":"user","content":"` +
			strings.Repeat("padding-", 24) + `"}}` + "\n")
	}
	return []byte(b.String())
}

// TestCreate_ConcurrentChangedSourcePreservesExactlyOneBak is the .bak-arm
// concurrency guard.
//
// The damage the lock exists to prevent: N writers archiving the SAME session
// with a CHANGED source all read the same prior manifest, all derive the SAME
// <name>.manifest.json.<prev-hash>.bak name from it, and then all try to rename
// onto it — the first wins, the rest fail with ENOENT on a source that no longer
// exists, and any racer that read AFTER the winner's rename finds no manifest at
// all, takes no .bak arm, and overwrites the record having preserved nothing.
//
// With the lock: the first writer preserves the prior manifest and writes the
// new one; every later writer reads the NEW manifest, matches on
// (adapter, session_id, source_sha256), and reports Skipped. Exactly one .bak,
// no errors, and a coherent final manifest.
//
// ROUNDS: the race is scheduler-dependent, and a single round can serialize by
// luck (one goroutine runs Create to completion before the others read, so they
// all dedup and the .bak arm is reached exactly once even unlocked). Rounds are
// independent vaults, so an unlocked build has to get lucky every time.
func TestCreate_ConcurrentChangedSourcePreservesExactlyOneBak(t *testing.T) {
	for round := 0; round < 4; round++ {
		t.Run("round", func(t *testing.T) {
			createBakRaceRound(t)
		})
	}
}

func createBakRaceRound(t *testing.T) {
	t.Helper()
	bodyA := bigJSONL(4000)
	bodyB := bigJSONL(4200)

	vaultRoot, srcPath, seed := seedForLock(t, "sess-bak-race", bodyA)
	manifestPath := seed.ManifestPath

	// The source changes. Every racer below archives THIS content.
	if err := os.WriteFile(srcPath, bodyB, 0o644); err != nil {
		t.Fatal(err)
	}

	const racers = 8
	errs := make([]error, racers)
	skipped := make([]bool, racers)

	mustFinishWithin(t, 60*time.Second, "concurrent Create", func() {
		var start sync.WaitGroup
		start.Add(1)
		var wg sync.WaitGroup
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				start.Wait()
				r, err := Create(CreateOptions{
					Adapter:     ClaudeCodeAdapterName,
					SessionID:   "sess-bak-race",
					SourcePath:  srcPath,
					VaultRoot:   vaultRoot,
					ProjectSlug: "demo",
					Now:         lockTestNow,
				})
				errs[i] = err
				if err == nil {
					skipped[i] = r.Skipped
				}
			}(i)
		}
		start.Done()
		wg.Wait()
	})

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: Create failed: %v", i, err)
		}
	}

	// Exactly one preservation survives, and it holds the PRIOR record.
	baks, err := filepath.Glob(manifestPath + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(baks) != 1 {
		t.Fatalf("want exactly 1 preserved .bak, got %d: %v", len(baks), baks)
	}
	bak, err := ReadManifest(baks[0])
	if err != nil {
		t.Fatalf("read preserved manifest: %v", err)
	}
	if got, want := bak.SourceSHA256, sha256Hex(bodyA); got != want {
		t.Errorf("preserved manifest source_sha256 = %q, want the PRIOR hash %q", got, want)
	}

	// The live manifest is coherent: it records the new source, and it is the
	// only manifest at that path.
	live, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("read live manifest: %v", err)
	}
	if got, want := live.SourceSHA256, sha256Hex(bodyB); got != want {
		t.Errorf("live manifest source_sha256 = %q, want the NEW hash %q", got, want)
	}
	if live.SessionID != "sess-bak-race" || live.Adapter != ClaudeCodeAdapterName {
		t.Errorf("live manifest identity torn: session_id=%q adapter=%q", live.SessionID, live.Adapter)
	}

	// Non-vacuity: at least one racer did real work and the rest deduped. If
	// every racer reported Skipped the .bak arm was never exercised.
	did := 0
	for _, s := range skipped {
		if !s {
			did++
		}
	}
	if did == 0 {
		t.Error("no racer performed the rewrite — the .bak arm was never reached")
	}
}

// TestLinkSessionNote_ConcurrentLinkersLeaveACoherentManifest proves concurrent
// read-modify-writes of one manifest do not tear it: the file stays valid JSON,
// the fields nobody touched are unchanged, and the surviving link is one that a
// linker actually asked for — never a splice of two.
func TestLinkSessionNote_ConcurrentLinkersLeaveACoherentManifest(t *testing.T) {
	body := bigJSONL(500)
	vaultRoot, _, seed := seedForLock(t, "sess-link-race", body)

	const linkers = 16
	want := make(map[string]bool, linkers)
	errs := make([]error, linkers)

	notes := make([]string, linkers)
	for i := range notes {
		notes[i] = "Projects/demo/sessions/2026-04-15-" + string(rune('a'+i)) + ".md"
		want[notes[i]] = true
	}

	mustFinishWithin(t, 60*time.Second, "concurrent LinkSessionNote", func() {
		var start sync.WaitGroup
		start.Add(1)
		var wg sync.WaitGroup
		for i := 0; i < linkers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				start.Wait()
				errs[i] = LinkSessionNote(vaultRoot, seed.ManifestPath, notes[i])
			}(i)
		}
		start.Done()
		wg.Wait()
	})

	for i, err := range errs {
		if err != nil {
			t.Errorf("linker %d: %v", i, err)
		}
	}

	m, err := ReadManifest(seed.ManifestPath)
	if err != nil {
		t.Fatalf("manifest is unreadable after concurrent linking: %v", err)
	}
	if !want[m.VaultRelSessionNote] {
		t.Errorf("vault_rel_session_note = %q, which no linker wrote", m.VaultRelSessionNote)
	}
	if m.SessionID != "sess-link-race" {
		t.Errorf("session_id = %q, want sess-link-race — an untouched field changed", m.SessionID)
	}
	if got, wantSum := m.SourceSHA256, sha256Hex(body); got != wantSum {
		t.Errorf("source_sha256 = %q, want %q — an untouched field changed", got, wantSum)
	}
}

// TestLinkSessionNote_ConcurrentWithCreateNeverRollsBackTheRecord is the
// lost-update guard that actually needs the lock.
//
// A linker's window opens at its READ. Unserialized, a linker can read the
// manifest, have a concurrent Create rename that manifest to .bak and write a
// new one for the changed source, and then write its own stale body back — which
// RESURRECTS the old source_sha256 over an archive whose bytes on disk are now
// the new ones. The manifest is not torn; it is worse than torn, it is coherent
// and wrong.
//
// With the lock, every linker read is either fully before or fully after
// Create's write, so the final source_sha256 is the NEW one in both orders.
func TestLinkSessionNote_ConcurrentWithCreateNeverRollsBackTheRecord(t *testing.T) {
	bodyA := bigJSONL(6000)
	bodyB := bigJSONL(6200)

	vaultRoot, srcPath, seed := seedForLock(t, "sess-rollback", bodyA)
	manifestPath := seed.ManifestPath

	if err := os.WriteFile(srcPath, bodyB, 0o644); err != nil {
		t.Fatal(err)
	}

	var createErr error
	var linkErrs []error
	var linkOps int
	var mu sync.Mutex

	// The linkers run UNTIL Create finishes, not for a fixed count. A fixed count
	// is what makes this kind of test pass either way: Create hashes and
	// compresses megabytes before its WriteManifest, so a burst of quick link ops
	// is long over by the time the write it has to straddle actually happens. The
	// iteration cap only bounds a pathological run; the watchdog bounds the test.
	createDone := make(chan struct{})

	mustFinishWithin(t, 120*time.Second, "Create racing LinkSessionNote", func() {
		var start sync.WaitGroup
		start.Add(1)
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(createDone)
			start.Wait()
			_, createErr = Create(CreateOptions{
				Adapter:     ClaudeCodeAdapterName,
				SessionID:   "sess-rollback",
				SourcePath:  srcPath,
				VaultRoot:   vaultRoot,
				ProjectSlug: "demo",
				Now:         lockTestNow,
			})
		}()

		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				start.Wait()
				for i := 0; i < 3000; i++ {
					select {
					case <-createDone:
						return
					default:
					}
					note := fmt.Sprintf("Projects/demo/sessions/n-%d-%d.md", g, i)
					err := LinkSessionNote(vaultRoot, manifestPath, note)
					mu.Lock()
					linkOps++
					if err != nil {
						linkErrs = append(linkErrs, err)
					}
					mu.Unlock()
				}
			}(g)
		}
		start.Done()
		wg.Wait()
	})

	if createErr != nil {
		t.Fatalf("Create: %v", createErr)
	}
	for _, err := range linkErrs {
		t.Errorf("LinkSessionNote: %v", err)
	}
	if linkOps < 8 {
		t.Fatalf("only %d link ops ran — the race never overlapped Create, so this "+
			"assertion proves nothing", linkOps)
	}

	m, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if got, want := m.SourceSHA256, sha256Hex(bodyB); got != want {
		t.Errorf("source_sha256 = %q, want the NEW hash %q — a linker wrote a stale "+
			"body back over Create's record, rolling the archive's identity backwards "+
			"while the .jsonl.zst on disk holds the new bytes", got, want)
	}
}

// TestCreate_NonBlockingPostureRefusesOnHeldLock pins the hook's posture: with
// the manifest lock already held, LockNonBlocking REFUSES with a recognizable
// error instead of waiting. The watchdog is the point — under LockBlocking this
// call never returns.
func TestCreate_NonBlockingPostureRefusesOnHeldLock(t *testing.T) {
	body := bigJSONL(200)
	vaultRoot, srcPath, seed := seedForLock(t, "sess-try", body)

	release, err := vaultlock.Acquire(vaultRoot, seed.ManifestPath)
	if err != nil {
		t.Fatalf("hold manifest lock: %v", err)
	}
	defer release()

	// Change the source so a lock-blind Create would have real work to do.
	if err := os.WriteFile(srcPath, bigJSONL(220), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotErr error
	mustFinishWithin(t, 20*time.Second, "Create with LockNonBlocking against a held lock", func() {
		_, gotErr = Create(CreateOptions{
			Adapter:     ClaudeCodeAdapterName,
			SessionID:   "sess-try",
			SourcePath:  srcPath,
			VaultRoot:   vaultRoot,
			ProjectSlug: "demo",
			Now:         lockTestNow,
			LockPosture: LockNonBlocking,
		})
	})

	if !errors.Is(gotErr, ErrManifestLocked) {
		t.Fatalf("Create error = %v, want one wrapping ErrManifestLocked", gotErr)
	}

	// Refusal is not corruption: nothing was preserved and nothing rewritten.
	baks, err := filepath.Glob(seed.ManifestPath + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(baks) != 0 {
		t.Errorf("a refused Create left %d .bak file(s): %v", len(baks), baks)
	}
	m, err := ReadManifest(seed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.SourceSHA256, sha256Hex(body); got != want {
		t.Errorf("a refused Create rewrote the manifest: source_sha256 = %q, want %q", got, want)
	}
}

// TestTryLinkSessionNote_RefusesOnHeldLock is the same proof for the link path:
// the hook's non-blocking sibling returns ErrManifestLocked rather than wedging.
func TestTryLinkSessionNote_RefusesOnHeldLock(t *testing.T) {
	vaultRoot, _, seed := seedForLock(t, "sess-try-link", bigJSONL(200))

	release, err := vaultlock.Acquire(vaultRoot, seed.ManifestPath)
	if err != nil {
		t.Fatalf("hold manifest lock: %v", err)
	}
	defer release()

	var gotErr error
	mustFinishWithin(t, 20*time.Second, "TryLinkSessionNote against a held lock", func() {
		gotErr = TryLinkSessionNote(vaultRoot, seed.ManifestPath,
			"Projects/demo/sessions/2026-04-15-01.md")
	})
	if !errors.Is(gotErr, ErrManifestLocked) {
		t.Fatalf("TryLinkSessionNote error = %v, want one wrapping ErrManifestLocked", gotErr)
	}

	m, err := ReadManifest(seed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote != "" {
		t.Errorf("a refused link still wrote: vault_rel_session_note = %q", m.VaultRelSessionNote)
	}
}

// TestLinkSessionNote_BlockingFormActuallyBlocks tests the OTHER direction of
// the posture split. Without it, a change that quietly turned every site
// non-blocking would still pass the refusal tests above — and the CLI/MCP path
// would start dropping writes it is supposed to wait for.
func TestLinkSessionNote_BlockingFormActuallyBlocks(t *testing.T) {
	vaultRoot, _, seed := seedForLock(t, "sess-blocking", bigJSONL(200))

	release, err := vaultlock.Acquire(vaultRoot, seed.ManifestPath)
	if err != nil {
		t.Fatalf("hold manifest lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- LinkSessionNote(vaultRoot, seed.ManifestPath,
			"Projects/demo/sessions/2026-04-15-01.md")
	}()

	// It must still be waiting: not returned, and above all not returned with an
	// error. A non-blocking regression shows up here as an early completion.
	select {
	case err := <-done:
		_ = release()
		t.Fatalf("blocking LinkSessionNote returned early (err=%v) while the manifest "+
			"lock was held — the CLI/MCP posture must WAIT, not refuse", err)
	case <-time.After(250 * time.Millisecond):
	}

	if rerr := release(); rerr != nil {
		t.Fatalf("release: %v", rerr)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LinkSessionNote after release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("blocking LinkSessionNote never completed after the lock was released")
	}

	m, err := ReadManifest(seed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote != "Projects/demo/sessions/2026-04-15-01.md" {
		t.Errorf("vault_rel_session_note = %q, want the waiting writer's value", m.VaultRelSessionNote)
	}
}

// TestCreate_ThenLinkSessionNote_NoSelfDeadlock pins non-reentrancy.
//
// Both sites lock the SAME manifest path, and flock does not recurse: if either
// one ever acquired twice — say by someone moving the lock down into
// WriteManifest, which both call — the second acquire would block forever with
// no error and no log. Sequential Create-then-Link in one process is the
// cheapest shape that catches it, and the watchdog is what makes the catch
// legible instead of a suite-wide hang.
func TestCreate_ThenLinkSessionNote_NoSelfDeadlock(t *testing.T) {
	// Seeded on the test goroutine: seedForLock calls t.Fatal, which is illegal
	// from the watchdog's goroutine.
	vaultRoot, srcPath, seed := seedForLock(t, "sess-reentry", bigJSONL(200))

	mustFinishWithin(t, 30*time.Second, "sequential Create then LinkSessionNote", func() {
		if err := LinkSessionNote(vaultRoot, seed.ManifestPath,
			"Projects/demo/sessions/2026-04-15-01.md"); err != nil {
			t.Errorf("LinkSessionNote after Create: %v", err)
			return
		}

		// And again through the .bak arm, which adds the rename to the window.
		if err := os.WriteFile(srcPath, bigJSONL(220), 0o644); err != nil {
			t.Errorf("rewrite source: %v", err)
			return
		}
		if _, err := Create(CreateOptions{
			Adapter:     ClaudeCodeAdapterName,
			SessionID:   "sess-reentry",
			SourcePath:  srcPath,
			VaultRoot:   vaultRoot,
			ProjectSlug: "demo",
			Now:         lockTestNow,
		}); err != nil {
			t.Errorf("second Create: %v", err)
			return
		}
		if err := LinkSessionNote(vaultRoot, seed.ManifestPath,
			"Projects/demo/sessions/2026-04-15-02.md"); err != nil {
			t.Errorf("LinkSessionNote after the .bak rewrite: %v", err)
		}
	})
}
