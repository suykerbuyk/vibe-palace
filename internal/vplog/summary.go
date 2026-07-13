// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vplog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// TailBytes bounds how much of the log a summary reads: the LAST TailBytes only,
// never the whole file.
//
// This is a hard constraint, not a tuning knob. Summarize runs on the
// vp_bootstrap_context path — the hottest call in the system, which iteration 190
// spent a whole session taking from ~0.4 s to 0.012 s — and the log is now capped
// at 8 MiB (MaxSize). Reading it end to end on every session start would hand that
// win straight back. A tail is also the RIGHT read: a health report is about what
// just went wrong, and warnings age out of the window anyway.
const TailBytes = 256 << 10

// Status values. UnknownStatus is the one that did not exist before, and it is the
// whole point of this file: a tool that cannot READ the log must not report that
// the system is HEALTHY.
const (
	StatusHealthy  = "healthy"
	StatusWarnings = "warnings"
	StatusErrors   = "errors"
	StatusUnknown  = "unknown"
)

// Entry is one WARN/ERROR line from the log.
type Entry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// Summary is the health of the log: what has gone wrong recently, and how badly.
type Summary struct {
	// Status is derived from EVERY in-window entry, never from the (capped)
	// RecentWarns list. Deriving it from the display list is how the tool used to
	// report "warnings" while holding an ERROR it had tallied but not shown —
	// contradicting itself inside its own payload.
	Status string `json:"status"`

	// RecentWarns is a true NEWEST-N tail. The old implementation appended while
	// `len(RecentWarns) < limit` as it scanned FORWARD from the start of an
	// append-only file, which keeps the OLDEST N entries in a field named recent_.
	RecentWarns []Entry `json:"recent_warns,omitempty"`

	WarnCounts map[string]int `json:"warn_counts,omitempty"`
	LogPath    string         `json:"log_path"`
	LogSize    int64          `json:"log_size_bytes"`

	// Truncated reports that the log is larger than TailBytes, so the counts cover
	// the tail and not the whole file. Without it, a partial view is
	// indistinguishable from a complete one and the numbers read as authoritative.
	Truncated bool `json:"truncated,omitempty"`

	// ScanError explains a Status of "unknown": the log could not be stat'd, opened
	// or read. It used to be silently swallowed — an unopenable log returned
	// "healthy" and did not even set this field.
	ScanError string `json:"scan_error,omitempty"`
}

// Healthy reports whether nothing needs the operator's attention. An "unknown" is
// NOT healthy: it means the instrument is blind, which is the failure this whole
// task exists to stop reporting as success.
func (s Summary) Healthy() bool { return s.Status == StatusHealthy }

// Summarize reads the last TailBytes of the log at logPath and reports what went
// wrong in the last `hours`, keeping at most `limit` entries for display.
//
// It never returns an error: a health probe that fails is itself a health signal,
// so a failure becomes Status "unknown" with ScanError set. What it must never do
// is report "healthy" for a log it could not read.
func Summarize(logPath string, hours, limit int) Summary {
	s := Summary{
		Status:     StatusHealthy,
		WarnCounts: map[string]int{},
		LogPath:    logPath,
	}

	info, err := os.Stat(logPath)
	if err != nil {
		// A MISSING LOG IS NOT HEALTH. It is the normal state on a fresh host and
		// the permanent state for any process that never initialized the logger —
		// which is precisely the condition this task was filed to make visible.
		// Reporting "healthy" here is the tool asserting a fact it does not have.
		s.Status = StatusUnknown
		s.ScanError = fmt.Sprintf("cannot stat log: %v", err)
		return s
	}
	s.LogSize = info.Size()

	f, err := os.Open(logPath)
	if err != nil {
		s.Status = StatusUnknown
		s.ScanError = fmt.Sprintf("cannot open log: %v", err)
		return s
	}
	defer f.Close()

	// The bounded read: seek to the tail rather than scanning forward.
	start := int64(0)
	if s.LogSize > TailBytes {
		start = s.LogSize - TailBytes
		s.Truncated = true
	}
	buf := make([]byte, s.LogSize-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		s.Status = StatusUnknown
		s.ScanError = fmt.Sprintf("cannot read log tail: %v", err)
		return s
	}

	lines := strings.Split(string(buf), "\n")
	// A mid-file seek almost certainly lands inside a line; that first fragment is
	// not a record and must not be parsed as one.
	if s.Truncated && len(lines) > 0 {
		lines = lines[1:]
	}

	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)

	// Collect EVERY in-window entry. Status is computed over all of them; the
	// display list is sliced off the end afterwards. Those are two different
	// questions, and conflating them was defect 3.
	var inWindow []Entry
	for _, line := range lines {
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue // tolerate malformed lines
		}
		level, _ := raw["level"].(string)
		if level != "WARN" && level != "ERROR" {
			continue
		}
		ts, _ := raw["time"].(string)
		if ts != "" && ts < cutoff {
			continue
		}
		msg, _ := raw["msg"].(string)
		inWindow = append(inWindow, Entry{Time: ts, Level: level, Msg: msg})
		s.WarnCounts[categorize(msg)]++
	}

	for _, e := range inWindow {
		if e.Level == "ERROR" {
			s.Status = StatusErrors
			break
		}
		s.Status = StatusWarnings
	}

	// Newest-N, taken from the END.
	if limit > 0 && len(inWindow) > limit {
		inWindow = inWindow[len(inWindow)-limit:]
	}
	s.RecentWarns = inWindow

	return s
}

// categorize extracts a category prefix from a log message.
func categorize(msg string) string {
	if idx := strings.Index(msg, ":"); idx > 0 && idx < 40 {
		return strings.TrimSpace(msg[:idx])
	}
	if len(msg) > 40 {
		return msg[:40]
	}
	return msg
}
