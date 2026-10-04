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
