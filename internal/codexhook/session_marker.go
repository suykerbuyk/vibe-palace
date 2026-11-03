// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// markerDirName is the per-checkout sentinel directory this package shares with
// internal/hook's claim sentinels. Spelled inline there too (hook.go:307); the
// project has no single constant for it.
const markerDirName = ".vibe-palace"

// sessionMarkerPrefix names a Vibe-Palace Codex session marker. The session id
// is the filename SUFFIX, so one file exists per live session and a second one
// is DETECTABLE rather than silently overwriting the first.
const sessionMarkerPrefix = "codex-session-"

// markerTTL bounds how long a marker may be believed.
//
// It exists because a session that never emits SessionEnd — killed, crashed,
// SIGHUP'd — leaves its marker behind, and nothing else will reap it:
// hook.SweepClaims filters on claimPrefix ("claimed-") alone (claim.go:114), so
// a codex-session-* file is invisible to it. Without the TTL one dead session
// would make its checkout permanently ambiguous.
//
// 🔴 The TTL is applied by the READER, which treats an expired marker as ABSENT
// and never unlinks it. Deletion stays with the hook, which is the only party
// holding an authoritative statement that a session ended. "Absent" means
// excluded from the candidate set entirely, so one live marker beside one
// expired marker is ONE candidate and derivation still succeeds.
const markerTTL = 24 * time.Hour

// markerNow is the clock the marker writer and reader share. A var so tests can
// pin "now" rather than sleep; production leaves it time.Now. Same shape and
// same reason as hook.claimNow (internal/hook/claim.go:83).
var markerNow = time.Now

// sessionMarker is the on-disk document.
//
// WrittenAt is a time.Time, not a preformatted string: encoding/json renders it
// through Time.MarshalJSON, which emits RFC 3339 WITH SUB-SECOND PRECISION, and
// parses it back symmetrically. That is deliberately not the time.RFC3339 layout
// constant, which is second-resolution and would make two markers written inside
// one second indistinguishable. No hand-rolled layout string anywhere.
type sessionMarker struct {
	SessionID string    `json:"session_id"`
	Model     string    `json:"model"`
	WrittenAt time.Time `json:"written_at"`
}

// SessionMarkerPath returns the marker path for one session in one checkout.
//
// 🔴 It CLEANS but does not CONSTRAIN — filepath.Join resolves ".." rather than
// refusing it. It is the single definition of where a marker lives, and it is
// reached in production only THROUGH safeSessionMarkerPath, which refuses an id
// that would leave the marker directory. Nothing that writes or removes may
// call this directly.
func SessionMarkerPath(cwd, sessionID string) string {
	return filepath.Join(cwd, markerDirName, sessionMarkerPrefix+sessionID)
}

// errUnsafeSessionID refuses a session id that would place the marker anywhere
// but directly inside the checkout's marker directory.
var errUnsafeSessionID = errors.New("codex session id is not a single safe path component")

// safeSessionMarkerPath resolves the marker path for sessionID and refuses any
// id that escapes the marker directory.
//
// 🔴 WHY BOTH A CHARACTER RULE AND A CONTAINMENT CHECK. The id arrives on the
// hook event and the code otherwise treats it as trusted, which hands
// RemoveSessionMarker an arbitrary-file delete primitive and WriteSessionMarker
// an arbitrary-file overwrite. Measured against this exact construction:
//
//	"../../../../victim"  -> /home/johns/code/victim                ESCAPES
//	"../../etc/cron.d/x"  -> <cwd>/.vibe-palace/etc/cron.d/x        ESCAPES
//	"a/b"                 -> <cwd>/.vibe-palace/codex-session-a/b   ESCAPES
//	"/etc/passwd"         -> <cwd>/.vibe-palace/codex-session-/etc/passwd
//
// 🔴 filepath.IsLocal ALONE IS NOT SUFFICIENT, and that is not a guess:
// IsLocal("a/b") is TRUE, and the third line above still leaves the marker
// directory. The separator rule is what catches it. IsLocal is kept anyway for
// what it adds — absolute paths, ".." elements, and the platform-reserved names
// it knows about — and the containment assertion is the actual guarantee,
// because it is stated over the RESOLVED path rather than over the characters
// someone thought to enumerate.
//
// The id is a filename SUFFIX, so "." and ".." are in fact harmless here
// (they render as "codex-session-." and "codex-session-.."). They are refused
// regardless: no real Codex session id looks like that, and a rule that admits
// a dot segment invites the next reader to relax it.
func safeSessionMarkerPath(cwd, sessionID string) (string, error) {
	if strings.ContainsRune(sessionID, '/') || strings.ContainsRune(sessionID, os.PathSeparator) ||
		sessionID == "." || sessionID == ".." || !filepath.IsLocal(sessionID) {
		return "", fmt.Errorf("%w: %q", errUnsafeSessionID, sessionID)
	}
	path := SessionMarkerPath(cwd, sessionID)
	if filepath.Dir(path) != filepath.Clean(filepath.Join(cwd, markerDirName)) {
		return "", fmt.Errorf("%w: %q resolves outside the marker directory", errUnsafeSessionID, sessionID)
	}
	return path, nil
}

// WriteSessionMarker records the authoritative session id and model that Codex
// supplied on SessionStart, where the MCP server can later read them.
//
// This is the whole mechanism by which a Codex MCP note learns its host session
// id. The server cannot derive it: vp mcp runs as a child of the SHARED
// app-server daemon, so os.Getppid() names the daemon rather than a session and
// collides across concurrent sessions; the process environment carries no
// session id; and vp's MCP library parses no session id from the initialize
// handshake (mark3labs/mcp-go InitializeParams carries only protocolVersion,
// capabilities and clientInfo, with no _meta passthrough).
//
// The caller supplies cwd from the hook event, never from an agent argument.
//
// An ABSENT id (empty) returns nil — there is nothing to record, and that is a
// normal shape. A MALFORMED id returns an ERROR, because it means something
// upstream is wrong and silence would hide it. That costs nothing here:
// HandleSessionStart logs the error and still emits the bootstrap injection,
// so a refusal can never turn a marker problem into a capture problem.
func WriteSessionMarker(cwd, sessionID, model string) error {
	if strings.TrimSpace(cwd) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	// Resolved and refused BEFORE anything is marshalled or any directory is
	// created: MkdirAll below would otherwise happily build whatever
	// intermediate path a traversing id named.
	path, err := safeSessionMarkerPath(cwd, sessionID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sessionMarker{SessionID: sessionID, Model: model, WrittenAt: markerNow()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// RemoveSessionMarker deletes the marker for ONE session id.
//
// 🔴 It removes exactly the named file and never globs. A SessionEnd event for
// id X is Codex stating that X has ended — authoritative and race-free for X,
// and silent about every other id. Deleting a sibling on any other basis
// (timestamp order, "newest wins") would let a later session disarm a LIVE
// earlier one, after which that session's note is stamped with the wrong id —
// a stamp that is sticky (capture.mergeCaptureMeta carries it forward) and
// unrecoverable (storage.BackfillArchiveLink refuses to overwrite it). An
// unjoined note is a gap; a cross-linked note is corruption.
//
// Both no-ops are load-bearing. An empty id must not resolve to the directory
// itself, and a missing file must not be an error: the caller is a defer on
// every SessionEnd return path, including those where no marker was ever
// written, and an error there would turn the existing no-marker gate test into
// a failure.
//
// A MALFORMED id is the third case and is distinct from both: it returns an
// error, matching WriteSessionMarker. Both SessionEnd defers log and discard
// that error, so surfacing it costs no capture and no exit code.
func RemoveSessionMarker(cwd, sessionID string) error {
	if strings.TrimSpace(cwd) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	// Refused before os.Remove sees it. Without this the id — which arrives on
	// the hook event — is an arbitrary-file delete primitive.
	path, err := safeSessionMarkerPath(cwd, sessionID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadSessionMarker resolves the one live Codex session id and model recorded
// for checkout dir, or "", "" when it cannot be CONFIRMED.
//
// It NEVER guesses. Every failure — no marker, unreadable, malformed, expired,
// a filename that disagrees with its own body, or MORE THAN ONE live candidate
// — returns empty, meaning "cannot be confirmed". That mirrors
// hostsession.ClaudeSessionID's contract and exists for the same reason: the
// archive linker matches on the stamped id alone, so a wrong stamp mis-links
// another session's transcript while an empty one leaves the note honestly
// unlinked.
//
// 🔴 Two live markers means two concurrent Codex sessions in one checkout, and
// nothing on disk says which one is asking. That is ambiguous and returns empty
// for BOTH. Do not add a tie-break; see RemoveSessionMarker.
//
// Unlike the writer and the remover, this needs no path guard, and that was
// confirmed rather than assumed:
//   - filepath.Glob's "*" does NOT cross a separator — measured: with files at
//     .vibe-palace/codex-session-good and .vibe-palace/etc/cron.d/x, the glob
//     below returns only the former. A marker planted in a subdirectory is
//     unreachable from here.
//   - filepath.Base strips any directory component before the suffix is taken.
//   - the filename/body agreement check then forces the returned id to equal
//     that Base-derived suffix, so the id this function hands back is itself a
//     single path component. Anything that later feeds it to
//     RemoveSessionMarker is therefore safe by construction.
func ReadSessionMarker(dir string) (sessionID, model string) {
	if strings.TrimSpace(dir) == "" {
		return "", ""
	}
	matches, err := filepath.Glob(filepath.Join(dir, markerDirName, sessionMarkerPrefix+"*"))
	if err != nil {
		return "", ""
	}
	now := markerNow()
	var foundID, foundModel string
	found := 0
	for _, path := range matches {
		// The filename suffix and the body must agree about whose marker this
		// is. Without this check a renamed file is a second source of truth.
		suffix := strings.TrimPrefix(filepath.Base(path), sessionMarkerPrefix)
		if suffix == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc sessionMarker
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		if doc.SessionID == "" || doc.SessionID != suffix {
			continue
		}
		// 🔴 A FUTURE-dated stamp is LIVE, not expired. Sub yields a negative
		// duration under clock skew, and a negative duration is not > markerTTL.
		// Never replace this with an absolute or Abs() comparison: that reads a
		// skewed-future marker as expired and discards the newest one first.
		if now.Sub(doc.WrittenAt) > markerTTL {
			continue
		}
		found++
		if found > 1 {
			return "", ""
		}
		foundID, foundModel = doc.SessionID, doc.Model
	}
	if found != 1 {
		return "", ""
	}
	return foundID, foundModel
}
