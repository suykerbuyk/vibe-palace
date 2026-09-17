// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests for the reader half: one malformed note must not delete a project's
// history, and the skip must not be silent.
//
// Two malformed notes out of 2360 once returned the ENTIRE session index of the
// two largest projects as empty, because ListSessions returned on the first
// parse failure and every caller either bailed on the error or dropped the
// results. `vp_bootstrap_context` reported zero sessions and the absence read
// as normal for weeks.

// writeSessionFixture drops n good notes plus the named bad ones into a
// project's sessions dir and returns the vault.
func writeSessionFixture(t *testing.T, good int, bad map[string]string) *Vault {
	t.Helper()
	root := t.TempDir()
	v := NewVault(root)
	dir := filepath.Join(root, "Projects", "proj", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= good; i++ {
		name := "2026-05-0" + string(rune('0'+i)) + "-01.md"
		body := "---\nsession_id: s" + string(rune('0'+i)) + "\nproject: proj\ndate: 2026-05-0" +
			string(rune('0'+i)) + "\niteration: 1\n---\n\n## Summary\n\nfine\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range bad {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// theLiveSpecimenShape is the exact frontmatter both failing notes carry: a
// block scalar whose indentation indicator promises column 8 and whose content
// sits at column 6.
const theLiveSpecimenShape = "---\nsession_id: bad\nproject: proj\ndate: 2026-05-09\n" +
	"decisions:\n    - |4-\n      <parameter name=\"files_changed\">x.go\n---\n\n## Summary\n\nbroken\n"

// TestListSessionsSkipsMalformedAndKeepsTheRest is the test that reproduces the
// outage, and it is the one that must be shown capable of failing.
//
// Break: restore `return nil, fmt.Errorf("parse session %s: %w", m, err)` in
// ListSessions. The listing comes back empty and this goes red — which is
// exactly what the live vault did.
func TestListSessionsSkipsMalformedAndKeepsTheRest(t *testing.T) {
	v := writeSessionFixture(t, 3, map[string]string{"2026-05-09-01.md": theLiveSpecimenShape})

	sessions, skipped, err := v.ListSessions("proj", "", "", 0)
	if err != nil {
		t.Fatalf("one malformed note failed the WHOLE listing: %v\n"+
			"That is the defect: a bad file must not delete a project's history", err)
	}
	if len(sessions) != 3 {
		t.Errorf("wanted the 3 readable notes back, got %d — a malformed note is taking healthy "+
			"notes down with it", len(sessions))
	}
	if len(skipped) != 1 {
		t.Fatalf("wanted exactly the 1 malformed note reported, got %d: %v", len(skipped), skipped)
	}
}

// TestListSessionsNamesWhatItSkipped pins the other half.
//
// A silently skipped file is the same disease as a fail-closed read, moved one
// layer down: the outage is replaced by a quietly short answer, which is harder
// to notice and harder to diagnose. The skip must name the file and say why.
//
// Break: change the skip branch to a bare `continue` without appending to
// `skipped`.
func TestListSessionsNamesWhatItSkipped(t *testing.T) {
	v := writeSessionFixture(t, 1, map[string]string{"2026-05-09-01.md": theLiveSpecimenShape})

	_, skipped, err := v.ListSessions("proj", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) == 0 {
		t.Fatal("a note was skipped and NOTHING said so. A skip nobody is told about is the outage " +
			"with the symptom removed and the cause intact")
	}
	s := skipped[0]
	if !strings.Contains(s.Path, "2026-05-09-01.md") {
		t.Errorf("the skip must name the file an operator has to go fix, got %q", s.Path)
	}
	// Vault-relative, because that is what is stable across hosts and what the
	// operator acts on.
	if filepath.IsAbs(s.Path) {
		t.Errorf("the skip path must be vault-relative, got an absolute host path: %q", s.Path)
	}
	if s.Reason == "" {
		t.Error("the skip must say WHY; a path with no reason sends the reader back to reproduce it")
	}
}

// TestListSessionsStillFailsOnAnUnreadableFile pins the asymmetry, which is
// deliberate and easy to erase by accident.
//
// A PARSE failure is a content problem scoped to one note, and every other note
// is still true. A READ failure is a HOST problem — a bad mount, a permission
// change, a disk fault — and says nothing about content, so continuing past it
// would report a partial index as though the vault were intact. Only the first
// becomes a skip.
//
// Break: move the os.ReadFile error into the skip list alongside the parse one.
func TestListSessionsStillFailsOnAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable, so this cannot be exercised")
	}
	v := writeSessionFixture(t, 1, nil)
	bad := filepath.Join(v.Root, "Projects", "proj", "sessions", "2026-05-09-01.md")
	if err := os.WriteFile(bad, []byte("---\nsession_id: x\n---\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	if _, _, err := v.ListSessions("proj", "", "", 0); err == nil {
		t.Fatal("an UNREADABLE file was skipped like a malformed one. A host-level read failure says " +
			"nothing about content, so continuing reports a partial index as though the vault were intact")
	}
}

// TestLiveVaultSessionIndexIsNotEmpty is the live-corpus reproduction, and it
// is the one that fails today on unpatched code.
//
// A bug that passes every fixture and dies on the real corpus is this project's
// signature failure, and this is that bug's own shape: the fixtures above prove
// the mechanism, this proves the mechanism is pointed at the thing that broke.
//
// 🔴 IT ASSERTS NO COUNT. "Exactly 2 notes are skipped" would go red the day the
// operator repairs them — a test that fails when someone FIXES the data. What is
// pinned is the property: the index is non-empty, and whatever is skipped is
// named.
//
// Break: restore the fail-closed return in ListSessions.
func TestLiveVaultSessionIndexIsNotEmpty(t *testing.T) {
	root := liveVaultRoot(t)
	v := NewVault(root)

	projects, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		t.Skipf("no Projects dir: %v", err)
	}

	checked := 0
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, "Projects", p.Name(), "sessions")
		onDisk, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		if len(onDisk) == 0 {
			continue
		}
		checked++

		sessions, skipped, lerr := v.ListSessions(p.Name(), "", "", 0)
		if lerr != nil {
			t.Errorf("%s: the session index failed outright: %v", p.Name(), lerr)
			continue
		}
		if len(sessions) == 0 {
			t.Errorf("%s: %d session note(s) on disk and the index came back EMPTY. This is the "+
				"outage: one malformed note deleting a project's whole history.\n  skipped: %v",
				p.Name(), len(onDisk), skipped)
		}
		for _, s := range skipped {
			if s.Path == "" || s.Reason == "" {
				t.Errorf("%s: a skip was reported without a path or a reason: %+v", p.Name(), s)
			}
			t.Logf("%s: skipped %s (%s)", p.Name(), s.Path, s.Reason)
		}
	}
	if checked == 0 {
		t.Fatal("no project with session notes was checked at all — the walk is not seeing the vault, " +
			"and a passing verdict here would be vacuous")
	}
}
