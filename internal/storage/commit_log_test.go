// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// TestArchiveCommitBodies_AppendsAndAnchors is the core append-log contract:
// each commit's full body lands under a header, in order, and the anchor file
// is written to newAnchor.
func TestArchiveCommitBodies_AppendsAndAnchors(t *testing.T) {
	v := testVault(t)
	commits := []wrapstate.CommitInfo{
		{SHA: "aaa", Body: "feat: one\n\nfirst body"},
		{SHA: "bbb", Body: "fix: two\n\nsecond body\nwith a second line"},
	}
	n, err := v.ArchiveCommitBodies("proj", commits, "bbb")
	if err != nil {
		t.Fatalf("ArchiveCommitBodies: %v", err)
	}
	if n != 2 {
		t.Fatalf("appended = %d, want 2", n)
	}

	logPath, _ := v.CommitLogFile("proj")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read commit-log: %v", err)
	}
	log := string(data)
	for _, want := range []string{"## commit aaa", "first body", "## commit bbb", "with a second line"} {
		if !strings.Contains(log, want) {
			t.Errorf("commit-log missing %q:\n%s", want, log)
		}
	}
	// aaa's header must precede bbb's (oldest-first append order).
	if strings.Index(log, "## commit aaa") > strings.Index(log, "## commit bbb") {
		t.Errorf("commits appended out of order:\n%s", log)
	}

	anchor, err := v.ReadCommitLogAnchor("proj")
	if err != nil {
		t.Fatalf("ReadCommitLogAnchor: %v", err)
	}
	if anchor != "bbb" {
		t.Errorf("anchor = %q, want bbb", anchor)
	}
}

// TestArchiveCommitBodies_EmptyCommitsAdvancesAnchor — no commits appends
// nothing but still advances the anchor, so the next run's range is empty too.
func TestArchiveCommitBodies_EmptyCommitsAdvancesAnchor(t *testing.T) {
	v := testVault(t)
	n, err := v.ArchiveCommitBodies("proj", nil, "deadbeef")
	if err != nil {
		t.Fatalf("ArchiveCommitBodies: %v", err)
	}
	if n != 0 {
		t.Errorf("appended = %d, want 0", n)
	}
	// commit-log.md must NOT be created for a zero-commit run.
	logPath, _ := v.CommitLogFile("proj")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("commit-log.md created on a zero-commit run (stat err = %v)", err)
	}
	anchor, _ := v.ReadCommitLogAnchor("proj")
	if anchor != "deadbeef" {
		t.Errorf("anchor = %q, want deadbeef", anchor)
	}
}

// TestArchiveCommitBodies_RefusesEmptyAnchor — a blank newAnchor would clobber
// a good cursor with nothing; it is refused.
func TestArchiveCommitBodies_RefusesEmptyAnchor(t *testing.T) {
	v := testVault(t)
	if _, err := v.ArchiveCommitBodies("proj", nil, "  "); err == nil {
		t.Fatal("expected refusal for a blank anchor")
	}
}

// TestArchiveCommitBodies_EmptyBodyKeepsHeader — a commit whose message is only
// a subject still gets its SHA recorded, never silently dropped.
func TestArchiveCommitBodies_EmptyBodyKeepsHeader(t *testing.T) {
	v := testVault(t)
	if _, err := v.ArchiveCommitBodies("proj",
		[]wrapstate.CommitInfo{{SHA: "caf", Body: ""}}, "caf"); err != nil {
		t.Fatalf("ArchiveCommitBodies: %v", err)
	}
	logPath, _ := v.CommitLogFile("proj")
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "## commit caf") {
		t.Errorf("empty-body commit dropped its header:\n%s", data)
	}
}

// TestReadCommitLogAnchor_AbsentIsEmpty — a project that has never been
// archived reports "" (the first-run seed signal), not an error.
func TestReadCommitLogAnchor_AbsentIsEmpty(t *testing.T) {
	v := testVault(t)
	anchor, err := v.ReadCommitLogAnchor("fresh")
	if err != nil {
		t.Fatalf("ReadCommitLogAnchor: %v", err)
	}
	if anchor != "" {
		t.Errorf("absent anchor = %q, want empty", anchor)
	}
}
