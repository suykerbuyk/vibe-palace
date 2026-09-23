// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// T16. RecordDeparture writes the record where readers look, stamps the
// Audits tree, derives its own date and base commit, refuses a project that is
// still here or a record that is not a departure — and commits NOTHING: the
// record belongs in the caller's own departure commit.
func TestRecordDepartureWritesStampsAndRefusesALiveTree(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init", "-q", "-b", "main")
	gitRun(t, root, "config", "user.email", "t@t")
	gitRun(t, root, "config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(root, "Projects", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Projects", "old", "resume.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "seed")
	v := NewVault(root)

	if _, err := v.RecordDeparture("old", departure.Renamed, "new"); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("a departure must not be recorded while Projects/old/ exists, got %v", err)
	}
	gitRun(t, root, "mv", "Projects/old", "Projects/new")
	gitRun(t, root, "commit", "-q", "-m", "rename")
	head := strings.TrimSpace(gitRun(t, root, "rev-parse", "HEAD"))

	for _, bad := range []struct {
		slug, to string
		kind     departure.Kind
	}{
		{"old", "old", departure.Renamed},
		{"Old Slug", "new", departure.Renamed},
		{"old", "", "exploded"},
		{"old", "/home/me/other-vault", departure.MovedToVault},
	} {
		if _, err := v.RecordDeparture(bad.slug, bad.kind, bad.to); err == nil {
			t.Errorf("RecordDeparture(%q, %q, %q) must refuse", bad.slug, bad.kind, bad.to)
		}
	}

	rel, err := v.RecordDeparture("old", departure.Renamed, "new")
	if err != nil {
		t.Fatalf("RecordDeparture: %v", err)
	}
	if rel != "Audits/departures/old.json" {
		t.Errorf("rel = %q", rel)
	}
	rec, ok := departure.Find(root, "old")
	if !ok || rec.Kind != departure.Renamed || rec.To != "new" || rec.Format != departure.Format {
		t.Fatalf("record not readable where readers look: %+v %v", rec, ok)
	}
	if rec.BaseCommit != head {
		t.Errorf("base_commit = %q, want the vault HEAD %q", rec.BaseCommit, head)
	}
	if rec.Date != CalendarDay(time.Now()) {
		t.Errorf("date = %q, want the writer's calendar day", rec.Date)
	}
	if _, err := os.Stat(filepath.Join(root, "Audits", ".surface")); err != nil {
		t.Errorf("the write must stamp Audits/.surface: %v", err)
	}
	if got := strings.TrimSpace(gitRun(t, root, "rev-parse", "HEAD")); got != head {
		t.Errorf("RecordDeparture committed: HEAD moved %s -> %s", head, got)
	}
	if st := gitRun(t, root, "status", "--porcelain", "-uall"); !strings.Contains(st, "Audits/departures/old.json") {
		t.Errorf("the record must be left for the caller to commit, status:\n%s", st)
	}
}
