// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// StartReader is an optional Adapter extension: an adapter that can read the
// instant a session started from its own transcript bytes. SessionStart scans
// the decompressed source and returns the time of the first record that
// carries one; ok is false when no record does. An adapter whose source holds
// no timestamps (Zed's synthesized lines) does not implement it, and its
// archives are dated from the manifest's captured_at.
type StartReader interface {
	SessionStart(source []byte) (start time.Time, ok bool, err error)
}

// Where a session day came from, recorded in the ledger as start_day_source.
const (
	DayFromTranscript = "transcript"
	DayFromCapturedAt = "captured_at"
)

// SessionDay is a session's start day: the UTC calendar day, YYYY-MM-DD, and
// where it came from. It is what the host-local index ledger records as a
// session's start_day and start_day_source (ADR-014 decision 4).
type SessionDay struct {
	Day    string
	Source string
}

// SessionDay returns the UTC day a session started, from immutable archive
// content only (ADR-014 decision 4; task importers-write-the-frozen-tracked-
// corpus, Scope 2):
//
//  1. the first timestamped record of the transcript, read by the manifest's
//     adapter when it is a StartReader;
//  2. otherwise the manifest's captured_at (RFC3339).
//
// The day is the UTC calendar day of that instant on every host, never the
// process-local day. It never reads the session note, vault_rel_session_note
// or the archive filename's day. An unknown adapter falls back with a
// warning. No timestamped record and an empty or unparseable captured_at is an
// error: the caller skips that archive.
//
// Callers: the pending-archive ingester and the rebuild driver, once per
// archive. Decision chunks never call it: they read the day the ledger
// recorded.
func (m *Manifest) SessionDay(source []byte) (SessionDay, error) {
	if a, err := LookupAdapter(m.Adapter); err != nil {
		slog.Warn("archive: unknown adapter; dating the session from captured_at", "adapter", m.Adapter, "session", m.SessionID)
	} else if sr, ok := a.(StartReader); ok {
		start, found, err := sr.SessionStart(source)
		if err != nil {
			return SessionDay{}, fmt.Errorf("read the start of session %s: %w", m.SessionID, err)
		}
		if found {
			return SessionDay{Day: start.UTC().Format("2006-01-02"), Source: DayFromTranscript}, nil
		}
	}
	t, err := time.Parse(time.RFC3339, m.CapturedAt)
	if err != nil {
		return SessionDay{}, fmt.Errorf("session %s has no timestamped record and its captured_at %q does not parse: %w", m.SessionID, m.CapturedAt, err)
	}
	return SessionDay{Day: t.UTC().Format("2006-01-02"), Source: DayFromCapturedAt}, nil
}

// claudeStart is the part of a Claude-shape JSONL record SessionStart reads.
type claudeStart struct {
	Timestamp string `json:"timestamp"`
}

// claudeSessionStart scans Claude-shape JSONL from the top and returns the
// first record's RFC3339 timestamp. Real transcripts open with records that
// carry none (custom-title, mode), so "first" means first timestamped. The
// scan stops, with no result, at the first non-empty line that is not a JSON
// object: a markdown source (a vibevault note) is never dated from a JSON
// snippet inside its body.
func claudeSessionStart(source []byte) (time.Time, bool, error) {
	sc := bufio.NewScanner(bytes.NewReader(source))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			return time.Time{}, false, nil
		}
		var rec claudeStart
		if err := json.Unmarshal(line, &rec); err != nil {
			return time.Time{}, false, nil
		}
		if rec.Timestamp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if err != nil {
			continue
		}
		return t, true, nil
	}
	if err := sc.Err(); err != nil {
		return time.Time{}, false, err
	}
	return time.Time{}, false, nil
}

// SessionStart implements StartReader over Claude Code's JSONL.
func (claudeAdapter) SessionStart(source []byte) (time.Time, bool, error) {
	return claudeSessionStart(source)
}

// SessionStart implements StartReader: inline sources are Claude-shape JSONL
// when they are a transcript, and a markdown body (a vibevault session) when
// they are not, which the scan's first-non-JSON-line stop leaves undated.
func (inlineAdapter) SessionStart(source []byte) (time.Time, bool, error) {
	return claudeSessionStart(source)
}

// codexRolloutHead is the part of a Codex rollout record codexSessionStart
// reads: the record type and, for session_meta, the payload's own timestamp.
//
// 🔴 The RECORD's top-level timestamp is when that record was APPENDED, not when
// the session began. Measured over 32 real rollouts: session_meta's own
// top-level stamp trails its payload stamp by up to 16.8h, and 3 of the 32 land
// on a LATER UTC DAY than the session actually started. Reading it would date
// those a day late — the exact defect this reader exists to remove — so it is
// deliberately not a field here.
type codexRolloutHead struct {
	Type    string `json:"type"`
	Payload struct {
		Timestamp string `json:"timestamp"`
	} `json:"payload"`
}

// codexSessionStart scans Codex rollout JSONL from the top and returns the
// payload timestamp of the first session_meta record. Every rollout in the live
// corpus opens with one, so the loop exits on line 1 in practice; it scans on
// rather than asserting the position, because a prepended record must not date
// the archive wrongly.
//
// 🔴 Every failure here is (zero, false, nil) — never an error — except a
// scanner failure. Manifest.SessionDay turns a StartReader error into a return
// (session_day.go, above), and IngestArchive turns THAT into a failed archive
// with no captured_at fallback. So a malformed rollout must degrade to the
// captured_at behaviour this reader replaces, not become un-ingestable. The
// scanner error is propagated only because claudeSessionStart already does; the
// longest line in the corpus is 188,804 bytes against the 64 MiB ceiling below.
func codexSessionStart(source []byte) (time.Time, bool, error) {
	sc := bufio.NewScanner(bytes.NewReader(source))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			return time.Time{}, false, nil
		}
		var rec codexRolloutHead
		if err := json.Unmarshal(line, &rec); err != nil {
			return time.Time{}, false, nil
		}
		if rec.Type != "session_meta" {
			continue
		}
		// The first session_meta is terminal either way: a rollout whose own
		// opening record carries no usable stamp is dated from captured_at, not
		// from some later record that means something else.
		if rec.Payload.Timestamp == "" {
			return time.Time{}, false, nil
		}
		t, err := time.Parse(time.RFC3339Nano, rec.Payload.Timestamp)
		if err != nil {
			return time.Time{}, false, nil
		}
		return t, true, nil
	}
	if err := sc.Err(); err != nil {
		return time.Time{}, false, err
	}
	return time.Time{}, false, nil
}

// SessionStart implements StartReader over Codex's rollout JSONL. The instant a
// Codex session began is session_meta's PAYLOAD timestamp; the record's own
// top-level timestamp is when the record was appended and can land on a later
// UTC day.
//
// 🔴 FIRST session_meta wins, and that is load-bearing. One rollout in the live
// corpus carries two, and they are a SUBAGENT and its parent: record 0 is the
// subagent's own (thread_source "subagent", parent_thread_id naming the
// parent), record 1 is the PARENT's, with a different id and an EARLIER stamp.
// A last-wins or earliest-wins rule would date that file by its parent's
// session.
//
// 🔴 THE TWO RECORDS DO NOT AGREE ABOUT WHICH FIELD IS WHOSE, and an earlier
// version of this comment had it backwards. Measured 2026-11-06 by spawning one
// subagent in a throwaway CODEX_HOME: the subagent's session_meta carries
// payload.id = its OWN thread and payload.session_id = the PARENT's. Nothing
// here is affected, because this reader takes only payload.timestamp — but a
// caller that joined on session_id would collapse a subagent into its parent.
// internal/check's codex-orphans row keys on payload.id for exactly that
// reason.
func (codexRawAdapter) SessionStart(source []byte) (time.Time, bool, error) {
	return codexSessionStart(source)
}
