// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// writeClaimFile drops a claim sentinel claimed-<id> with the given body (the
// note id) into claimDir. The body is deliberately separate from id so a test
// can prove SweepClaims keys on the FILENAME, never the body.
func writeClaimFile(t *testing.T, claimDir, id, body string) string {
	t.Helper()
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatalf("mkdir claim dir: %v", err)
	}
	path := filepath.Join(claimDir, claimPrefix+id)
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		t.Fatalf("write claim %s: %v", id, err)
	}
	return path
}

func claimIDs(claimDir string) []string {
	entries, _ := os.ReadDir(claimDir)
	var ids []string
	for _, e := range entries {
		if n := e.Name(); len(n) > len(claimPrefix) && n[:len(claimPrefix)] == claimPrefix {
			ids = append(ids, n[len(claimPrefix):])
		}
	}
	sort.Strings(ids)
	return ids
}

// TestSweepClaims_SingleHost_RemovesDeadKeepsLive is the core "Done when": with
// an injected live set, a dead-session claim is purged and a live one kept.
func TestSweepClaims_SingleHost_RemovesDeadKeepsLive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".vibe-palace")
	writeClaimFile(t, dir, "live-1", "note-live")
	writeClaimFile(t, dir, "dead-1", "note-dead")

	removed, err := SweepClaims(dir, map[string]bool{"live-1": true}, DefaultClaimTTL, ClaimModeSingleHost)
	if err != nil {
		t.Fatalf("SweepClaims: %v", err)
	}
	if want := []string{"dead-1"}; !equalStrings(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if got := claimIDs(dir); !equalStrings(got, []string{"live-1"}) {
		t.Errorf("surviving claims = %v, want [live-1]", got)
	}
}

// TestSweepClaims_MatchesFilenameIDNotBody pins the 🔴 rule: the id is the
// filename suffix, never the sentinel body. A claim whose BODY is a live note id
// but whose FILENAME id is dead must still be purged.
func TestSweepClaims_MatchesFilenameIDNotBody(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".vibe-palace")
	// Body is a live note id; filename id "dead-sess" is not in the live set.
	writeClaimFile(t, dir, "dead-sess", "live-note-id")

	removed, err := SweepClaims(dir, map[string]bool{"live-note-id": true}, DefaultClaimTTL, ClaimModeSingleHost)
	if err != nil {
		t.Fatalf("SweepClaims: %v", err)
	}
	if !equalStrings(removed, []string{"dead-sess"}) {
		t.Fatalf("removed = %v, want [dead-sess] (must key on filename, not body)", removed)
	}
	if got := claimIDs(dir); len(got) != 0 {
		t.Errorf("surviving claims = %v, want none", got)
	}
}

// TestSweepClaims_SharedCheckout_TTL keeps a recent dead claim and purges an
// aged one, with the clock pinned and mtimes injected via os.Chtimes.
func TestSweepClaims_SharedCheckout_TTL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".vibe-palace")
	recent := writeClaimFile(t, dir, "dead-recent", "note-a")
	aged := writeClaimFile(t, dir, "dead-aged", "note-b")

	fixed := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	restore := pinClaimNow(fixed)
	defer restore()

	ttl := 7 * 24 * time.Hour
	// recent: 1 day old (inside the window); aged: 8 days old (past it).
	chtime(t, recent, fixed.Add(-24*time.Hour))
	chtime(t, aged, fixed.Add(-8*24*time.Hour))

	removed, err := SweepClaims(dir, map[string]bool{}, ttl, ClaimModeSharedCheckout)
	if err != nil {
		t.Fatalf("SweepClaims: %v", err)
	}
	if !equalStrings(removed, []string{"dead-aged"}) {
		t.Errorf("removed = %v, want [dead-aged]", removed)
	}
	if got := claimIDs(dir); !equalStrings(got, []string{"dead-recent"}) {
		t.Errorf("surviving claims = %v, want [dead-recent]", got)
	}

	// Sanity: single-host mode ignores the TTL and purges both dead claims.
	writeClaimFile(t, dir, "dead-aged", "note-b") // restore the removed one
	removed, err = SweepClaims(dir, map[string]bool{}, ttl, ClaimModeSingleHost)
	if err != nil {
		t.Fatalf("SweepClaims single-host: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("single-host removed = %v, want both dead claims", removed)
	}
}

func TestSweepClaims_MissingDirIsNotAnError(t *testing.T) {
	removed, err := SweepClaims(filepath.Join(t.TempDir(), "nope"), nil, DefaultClaimTTL, ClaimModeSingleHost)
	if err != nil {
		t.Fatalf("SweepClaims on missing dir: %v", err)
	}
	if removed != nil {
		t.Errorf("removed = %v, want nil", removed)
	}
}

// TestLiveSessionSet_DrivesHostsessionGuard exercises the REAL hostsession guard
// through a fixture ~/.claude/sessions tree and an injected ProcStartFunc — not
// an injected live set — so the pid-reuse path ships tested. Then it feeds the
// resulting live set to SweepClaims end-to-end.
func TestLiveSessionSet_DrivesHostsessionGuard(t *testing.T) {
	home := t.TempDir()
	sessionsDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeSessionFile(t, sessionsDir, 1000, "live-a", "111")    // confirmed live
	writeSessionFile(t, sessionsDir, 2000, "reused-b", "222")  // pid reused: live start differs
	writeSessionFile(t, sessionsDir, 3000, "unknown-c", "333") // /proc read fails
	// Body pid (4001) disagrees with the filename pid (4000): the guard rejects
	// it on f.PID != parentPID even though procStart would otherwise accept it.
	writeSessionFileWithPID(t, sessionsDir, 4000, 4001, "mismatch-d", "444")
	// A non-pid filename and a non-json file must be ignored, not crash.
	if err := os.WriteFile(filepath.Join(sessionsDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	procStart := func(pid int) (string, error) {
		switch pid {
		case 1000:
			return "111", nil // matches the file: live
		case 2000:
			return "999", nil // pid reuse: differs from the file's "222"
		case 3000:
			return "", fmt.Errorf("no such process")
		case 4000:
			return "444", nil // matches the file's procStart; only the body pid is wrong
		}
		return "", fmt.Errorf("unexpected pid %d", pid)
	}

	live := LiveSessionSet(home, procStart)
	if !equalStringSet(live, map[string]bool{"live-a": true}) {
		t.Fatalf("LiveSessionSet = %v, want only {live-a} (pid-reuse, dead and body/filename-pid-mismatch excluded)", live)
	}

	// End-to-end: claims for every session; only the confirmed-live one survives.
	claimDir := filepath.Join(t.TempDir(), ".vibe-palace")
	writeClaimFile(t, claimDir, "live-a", "note-a")
	writeClaimFile(t, claimDir, "reused-b", "note-b")
	writeClaimFile(t, claimDir, "unknown-c", "note-c")
	writeClaimFile(t, claimDir, "mismatch-d", "note-d")

	removed, err := SweepClaims(claimDir, live, DefaultClaimTTL, ClaimModeSingleHost)
	if err != nil {
		t.Fatalf("SweepClaims: %v", err)
	}
	if !equalStrings(removed, []string{"mismatch-d", "reused-b", "unknown-c"}) {
		t.Errorf("removed = %v, want [mismatch-d reused-b unknown-c]", removed)
	}
	if got := claimIDs(claimDir); !equalStrings(got, []string{"live-a"}) {
		t.Errorf("surviving claims = %v, want [live-a]", got)
	}
}

// TestSweepClaimsBestEffort_NeverFailsTheHook pins that the invocation path
// swallows every sweep failure — both an error and a panic — so it can never
// fail the hook. A failure escaping this function would propagate into Run.
func TestSweepClaimsBestEffort_NeverFailsTheHook(t *testing.T) {
	orig := claimSweepImpl
	defer func() { claimSweepImpl = orig }()

	claimSweepImpl = func(string, string) ([]string, error) {
		return nil, fmt.Errorf("sweep boom")
	}
	sweepClaimsBestEffort("/does/not/matter", "cur-sess") // must return normally

	claimSweepImpl = func(string, string) ([]string, error) {
		panic("sweep kaboom")
	}
	sweepClaimsBestEffort("/does/not/matter", "cur-sess") // must recover, not propagate

	// Reaching here without a panic escaping is the assertion.
}

// --- helpers ---

func pinClaimNow(fixed time.Time) func() {
	orig := claimNow
	claimNow = func() time.Time { return fixed }
	return func() { claimNow = orig }
}

func chtime(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func writeSessionFile(t *testing.T, sessionsDir string, pid int, sessionID, procStart string) {
	t.Helper()
	writeSessionFileWithPID(t, sessionsDir, pid, pid, sessionID, procStart)
}

// writeSessionFileWithPID writes sessions/<filePID>.json whose BODY names
// bodyPID, so a test can make them disagree and exercise the guard's
// f.PID != parentPID rejection.
func writeSessionFileWithPID(t *testing.T, sessionsDir string, filePID, bodyPID int, sessionID, procStart string) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%q}`, bodyPID, sessionID, procStart)
	path := filepath.Join(sessionsDir, fmt.Sprintf("%d.json", filePID))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write session file %s: %v", path, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func equalStringSet(got, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
