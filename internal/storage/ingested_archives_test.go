// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"strings"
	"testing"
)

func TestRecordIngestedArchiveRoundTrip(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	if err := v.RecordIngestedArchive(project, IngestedArchive{
		SourceSHA256: "aaa111", SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("RecordIngestedArchive: %v", err)
	}

	got, err := v.IngestedArchives(project)
	if err != nil {
		t.Fatalf("IngestedArchives: %v", err)
	}
	if _, ok := got["aaa111"]; !ok {
		t.Fatalf("ledger = %v, want it to contain aaa111", got)
	}
	if len(got) != 1 {
		t.Fatalf("ledger holds %d hashes, want 1", len(got))
	}
}

// TestIngestedArchivesMissingLedgerIsEmptySet: every project predates this
// file, and "no ledger" must mean "ingest everything", never an error that
// fails the sweep.
func TestIngestedArchivesMissingLedgerIsEmptySet(t *testing.T) {
	v := testVault(t)
	got, err := v.IngestedArchives("proj")
	if err != nil {
		t.Fatalf("IngestedArchives on a project with no ledger: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ledger = %v, want empty", got)
	}
}

// TestRecordIngestedArchiveRefusesEmptyHash pins the rule that keeps the ledger
// from growing while skipping nothing: a row keyed on "" can never match.
func TestRecordIngestedArchiveRefusesEmptyHash(t *testing.T) {
	v := testVault(t)
	err := v.RecordIngestedArchive("proj", IngestedArchive{SessionID: "sess-1"})
	if err == nil {
		t.Fatal("recording an empty source_sha256 must be refused")
	}
	if !strings.Contains(err.Error(), "empty source_sha256") {
		t.Fatalf("error = %q, want it to name the empty hash", err)
	}
	path, _ := v.IngestedArchivesFile("proj")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("refused record still created %s", path)
	}
}

// TestRecordIngestedArchiveIsIdempotent: re-recording the same hash must not
// grow the file, or a project that reingests once per crash accumulates a row
// per run forever.
func TestRecordIngestedArchiveIsIdempotent(t *testing.T) {
	v := testVault(t)
	const project = "proj"
	rec := IngestedArchive{SourceSHA256: "bbb222", SessionID: "sess-2"}

	if err := v.RecordIngestedArchive(project, rec); err != nil {
		t.Fatalf("first record: %v", err)
	}
	path, _ := v.IngestedArchivesFile(project)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.RecordIngestedArchive(project, rec); err != nil {
		t.Fatalf("second record: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("re-recording rewrote the ledger:\n%q\n%q", before, after)
	}
}

// TestRecordIngestedArchiveDistinctHashesSameSession is the reason the key is
// the content hash: a re-archived session carries the SAME session_id with new
// bytes, and it must be ingestable again.
func TestRecordIngestedArchiveDistinctHashesSameSession(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	for _, sha := range []string{"ccc333", "ddd444"} {
		if err := v.RecordIngestedArchive(project, IngestedArchive{
			SourceSHA256: sha, SessionID: "same-session",
		}); err != nil {
			t.Fatalf("record %s: %v", sha, err)
		}
	}
	got, err := v.IngestedArchives(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ledger holds %d hashes, want 2 — a re-archive of one session is a new archive", len(got))
	}
}

// TestIngestedArchivesSkipsTornAndEmptyRows: this file is appended to through
// F4, so a crash can leave a torn last line. One bad row must not invalidate
// the ledger and send the next run back to a full reingest.
func TestIngestedArchivesSkipsTornAndEmptyRows(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	if err := v.RecordIngestedArchive(project, IngestedArchive{SourceSHA256: "eee555"}); err != nil {
		t.Fatal(err)
	}
	path, _ := v.IngestedArchivesFile(project)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// An empty-hash row, then a torn one with no closing brace.
	if _, err := f.WriteString(`{"source_sha256":""}` + "\n" + `{"source_sha256":"fff6`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := v.IngestedArchives(project)
	if err != nil {
		t.Fatalf("IngestedArchives over a torn ledger: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ledger = %v, want just the one good hash", got)
	}
	if _, ok := got["eee555"]; !ok {
		t.Fatalf("ledger = %v, want it to contain eee555", got)
	}

	// And a later record must not be swallowed by the torn line.
	if err := v.RecordIngestedArchive(project, IngestedArchive{SourceSHA256: "999888"}); err != nil {
		t.Fatalf("record over a torn ledger: %v", err)
	}
	got, err = v.IngestedArchives(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["999888"]; !ok {
		t.Fatalf("ledger = %v, want the new hash to have survived the torn line", got)
	}
}

func TestIngestedArchivesFileInvalidSlug(t *testing.T) {
	v := testVault(t)
	if _, err := v.IngestedArchivesFile("BAD PROJECT"); err == nil {
		t.Error("IngestedArchivesFile with an invalid slug should return an error")
	}
	if _, err := v.IngestedArchives("BAD PROJECT"); err == nil {
		t.Error("IngestedArchives with an invalid slug should return an error")
	}
	if err := v.RecordIngestedArchive("BAD PROJECT", IngestedArchive{SourceSHA256: "x"}); err == nil {
		t.Error("RecordIngestedArchive with an invalid slug should return an error")
	}
}
