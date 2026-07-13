// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vplog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vp.log")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// TestSummarizeReadsABoundedTailNotTheWholeLog is the performance constraint made
// executable, and it is not a micro-optimization.
//
// Summarize runs on the vp_bootstrap_context path — the hottest call in the system,
// which iteration 190 spent a whole session taking from ~0.4 s to 0.012 s — and the
// log is capped at 8 MiB. Scanning it end to end on every session start would hand
// that win straight back.
//
// So: bury a warning far below the tail window under megabytes of noise, and assert
// Summarize does NOT see it, and SAYS it did not (Truncated). A summary that silently
// covers only part of the log while looking authoritative is the same lie in a new place.
func TestSummarizeReadsABoundedTailNotTheWholeLog(t *testing.T) {
	// One ancient warning, then enough filler to push it well past TailBytes.
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"ancient: buried far above the tail"}`, nowStamp()),
	}
	filler := strings.Repeat("x", 512)
	for len(lines)*560 < TailBytes*3 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"noise %s"}`, nowStamp(), filler))
	}
	lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"recent: inside the tail"}`, nowStamp()))

	s := Summarize(writeLog(t, lines), 24, 20)

	if !s.Truncated {
		t.Fatal("the log is far larger than TailBytes but Truncated is false — " +
			"a partial view is being presented as a complete one")
	}
	joined := ""
	for _, e := range s.RecentWarns {
		joined += e.Msg + "\n"
	}
	if !strings.Contains(joined, "recent: inside the tail") {
		t.Errorf("the tail's own warning was missed:\n%s", joined)
	}
	if strings.Contains(joined, "ancient: buried far above the tail") {
		t.Error("Summarize read past the tail window — it is scanning the whole log, " +
			"which is what the bounded read exists to prevent")
	}
}

// TestSummarizeDropsThePartialFirstLine guards the seam the bounded read creates: a
// mid-file seek almost certainly lands INSIDE a line, and that fragment is not a
// record. Parsing it would at best be ignored and at worst mis-parse.
func TestSummarizeDropsThePartialFirstLine(t *testing.T) {
	var lines []string
	bytes := 0
	for bytes < TailBytes*2 {
		l := fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry"}`, nowStamp())
		lines = append(lines, l)
		bytes += len(l) + 1 // +1 for the newline writeLog adds
	}
	s := Summarize(writeLog(t, lines), 24, 1000)

	if !s.Truncated {
		t.Fatal("test is not exercising a truncated read")
	}
	// Every surviving entry must be a fully-parsed record, not a fragment.
	for _, e := range s.RecentWarns {
		if e.Msg != "cat: entry" || e.Level != "WARN" {
			t.Fatalf("a partial line was parsed as a record: %+v", e)
		}
	}
}

// TestSummarizeUnknownNotHealthyOnMissingLog: the headline invariant, at the level
// that actually implements it.
func TestSummarizeUnknownNotHealthyOnMissingLog(t *testing.T) {
	s := Summarize(filepath.Join(t.TempDir(), "does-not-exist.log"), 24, 20)

	if s.Status == StatusHealthy {
		t.Fatal("a log that does not exist was reported HEALTHY")
	}
	if s.Status != StatusUnknown {
		t.Errorf("status = %q, want %q", s.Status, StatusUnknown)
	}
	if s.Healthy() {
		t.Error("Healthy() is true for an unknown status")
	}
	if s.ScanError == "" {
		t.Error("status is unknown but nothing says why")
	}
}

// TestSummarizeStatusOverAllEntriesNotTheDisplayCap — an ERROR outside the display
// cap must still set the status. Deriving status from the capped list is how the tool
// reported "warnings" while holding an ERROR it had itself counted.
func TestSummarizeStatusOverAllEntriesNotTheDisplayCap(t *testing.T) {
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"boom: it broke"}`, nowStamp()),
	}
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, nowStamp(), i))
	}

	s := Summarize(writeLog(t, lines), 24, 2)

	if len(s.RecentWarns) != 2 {
		t.Fatalf("display cap not applied: %d entries", len(s.RecentWarns))
	}
	for _, e := range s.RecentWarns {
		if e.Level == "ERROR" {
			t.Fatal("test is not exercising the defect: the ERROR is inside the display cap")
		}
	}
	if s.Status != StatusErrors {
		t.Errorf("status = %q, want %q — the ERROR was counted but did not set the status", s.Status, StatusErrors)
	}
}

// TestSummarizeRecentWarnsIsNewestN — the field is called recent_warns.
func TestSummarizeRecentWarnsIsNewestN(t *testing.T) {
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, nowStamp(), i))
	}
	s := Summarize(writeLog(t, lines), 24, 3)

	want := []string{"cat: entry 7", "cat: entry 8", "cat: entry 9"}
	if len(s.RecentWarns) != len(want) {
		t.Fatalf("got %d entries, want %d", len(s.RecentWarns), len(want))
	}
	for i, w := range want {
		if s.RecentWarns[i].Msg != w {
			t.Errorf("recent_warns[%d] = %q, want %q — this is the OLDEST-N bug", i, s.RecentWarns[i].Msg, w)
		}
	}
}
