// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/hostsession"
)

const claimPrefix = "claimed-"

// ClaimPath returns the filesystem path for a session's claim sentinel.
// claimDir is the already-resolved directory (typically <CWD>/.vibe-palace).
func ClaimPath(claimDir, sessionID string) string {
	return filepath.Join(claimDir, claimPrefix+sessionID)
}

// IsClaimed reports whether a claim sentinel exists for the given session.
func IsClaimed(claimDir, sessionID string) bool {
	_, err := os.Stat(ClaimPath(claimDir, sessionID))
	return err == nil
}

// WriteClaim writes a claim sentinel for the given session. Creates the
// parent directory if needed. The noteID is written as the sentinel's
// content so operators can trace which vault note claimed the session.
func WriteClaim(claimDir, sessionID, noteID string) error {
	path := ClaimPath(claimDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create claim dir: %w", err)
	}
	return os.WriteFile(path, []byte(noteID+"\n"), 0o644)
}

// ClaimMode selects how SweepClaims decides a dead-session claim is safe to
// purge. The mode bit is OPERATOR-BINDING — nothing in source can detect an
// NFS / shared checkout, and purge-on-absence would delete a claim a DIFFERENT
// host wrote. The zero value is the conservative single-host default.
type ClaimMode int

const (
	// ClaimModeSingleHost purges a claim as soon as its session is not live on
	// THIS host. Correct only when the checkout is owned by one host — the
	// default (operator ruling).
	ClaimModeSingleHost ClaimMode = iota
	// ClaimModeSharedCheckout purges a dead-session claim only once it is also
	// older than the TTL, so a claim another host wrote and whose session this
	// host cannot see is given a grace window before removal.
	ClaimModeSharedCheckout
)

// String names the mode for logs.
func (m ClaimMode) String() string {
	switch m {
	case ClaimModeSingleHost:
		return "single-host"
	case ClaimModeSharedCheckout:
		return "shared-checkout"
	default:
		return "unknown(" + strconv.Itoa(int(m)) + ")"
	}
}

// DefaultClaimMode and DefaultClaimTTL are the hook invocation's defaults. The
// mode is single-host per the operator ruling; the TTL is a recommendation
// under the chosen mode (operator-tunable).
const (
	DefaultClaimMode = ClaimModeSingleHost
	DefaultClaimTTL  = 7 * 24 * time.Hour
)

// claimNow is the clock SweepClaims reads for the shared-checkout TTL check. A
// var so tests can pin "now" rather than sleep; production leaves it time.Now.
var claimNow = time.Now

// SweepClaims removes claim sentinels in claimDir whose session is no longer
// live, so a crashed or exited host does not leave a claimed-<id> sentinel that
// short-circuits a later real capture of a reused id. The claim id is the
// FILENAME suffix (claimed-<id>), never the sentinel body (which is the note
// id); matching the body would purge by the wrong key.
//
//   - single-host: purge claimed-<id> iff <id> is NOT in liveSet.
//   - shared-checkout: purge iff <id> is NOT in liveSet AND the claim's mtime is
//     older than ttl (a grace window for a claim another host may own).
//
// A wrong purge is non-corrupting: the hook's capture dedup keys on the same
// filename id (SessionKey: payload.SessionID), so at worst a re-capture rewrites
// the existing note via UpsertSessionByKey — it never mints or deletes one.
// removed holds the purged ids. A missing claimDir is not an error (no claims).
func SweepClaims(claimDir string, liveSet map[string]bool, ttl time.Duration, mode ClaimMode) (removed []string, err error) {
	entries, err := os.ReadDir(claimDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read claim dir: %w", err)
	}
	now := claimNow()
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, claimPrefix) {
			continue
		}
		id := strings.TrimPrefix(name, claimPrefix)
		if id == "" || liveSet[id] {
			continue // no id, or the session is live — keep it.
		}
		if mode == ClaimModeSharedCheckout {
			info, statErr := e.Info()
			if statErr != nil {
				// Cannot judge the mtime — skip rather than risk purging a claim
				// a live remote host owns.
				if firstErr == nil {
					firstErr = fmt.Errorf("stat claim %s: %w", name, statErr)
				}
				continue
			}
			if now.Sub(info.ModTime()) <= ttl {
				continue // dead but still inside the grace window.
			}
		}
		path := filepath.Join(claimDir, name)
		if rmErr := os.Remove(path); rmErr != nil {
			if os.IsNotExist(rmErr) {
				continue // vanished under us — nothing to do.
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("remove claim %s: %w", name, rmErr)
			}
			continue
		}
		removed = append(removed, id)
	}
	return removed, firstErr
}

// LiveSessionSet enumerates ~/.claude/sessions/<pid>.json (claudeHome/sessions)
// and returns the set of session ids whose host process is CONFIRMED live. It
// reuses the hostsession guard for every file — pid + /proc start time, which
// "NEVER guesses" — so a stale file left by a crashed host, or one whose pid was
// reused, never lands in the set. It never returns an error: a session that
// cannot be confirmed is simply absent (an honest unknown), which the sweep then
// treats as dead under the chosen mode.
func LiveSessionSet(claudeHome string, procStart hostsession.ProcStartFunc) map[string]bool {
	set := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(claudeHome, "sessions"))
	if err != nil {
		return set
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, convErr := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if convErr != nil {
			continue
		}
		if id := hostsession.ClaudeSessionID(pid, claudeHome, procStart); id != "" {
			set[id] = true
		}
	}
	return set
}

// claimSweepImpl resolves the live set and runs SweepClaims with the defaults. A
// var so the hook invocation and its test can substitute it. currentSessionID is
// the session running this very hook: it is live by definition and is added to
// the live set unconditionally, so the sweep never purges the current session's
// own claim — the claim whose whole job is to make the next event idempotent.
// That guard also covers non-Claude hosts (Zed, HTTP serve), where the
// hostsession resolver has no file and returns "": without it the sweep would
// delete the current claim and re-capture on every event.
//
// Only the CURRENT session is protected this way, so in single-host mode on a
// non-Claude host a PEER session sharing this checkout's .vibe-palace can have
// its claim purged; the worst that causes is a harmless note REWRITE on that
// peer's next capture (UpsertSessionByKey keys on the same filename id), never a
// second note nor a deleted one.
var claimSweepImpl = func(claimDir, currentSessionID string) (removed []string, err error) {
	home, err := archive.ClaudeHome()
	if err != nil {
		return nil, fmt.Errorf("resolve claude home: %w", err)
	}
	live := LiveSessionSet(home, hostsession.ReadProcStart)
	if currentSessionID != "" {
		live[currentSessionID] = true
	}
	return SweepClaims(claimDir, live, DefaultClaimTTL, DefaultClaimMode)
}

// sweepClaimsBestEffort runs the opportunistic claim sweep at session start and
// swallows EVERY failure — error or panic. The sweep is bounded, local-only
// maintenance; a hook that fails or hangs mid-capture is strictly worse than a
// skipped sweep, so this must never let a sweep problem reach the hook.
func sweepClaimsBestEffort(claimDir, currentSessionID string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("hook: claim sweep panicked (non-fatal)", "recover", r)
		}
	}()
	removed, err := claimSweepImpl(claimDir, currentSessionID)
	if err != nil {
		slog.Warn("hook: claim sweep failed (non-fatal)", "err", err)
		return
	}
	if len(removed) > 0 {
		slog.Info("hook: claim sweep removed stale claim sentinels", "count", len(removed))
	}
}
