// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// stalenessVault builds a vault with n session notes spread over two projects and,
// optionally, an audit report stamped with a date and an anchor.
func stalenessVault(t *testing.T, notes int, report string) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	for i := range notes {
		project := fmt.Sprintf("proj-%d", i%2)
		dir := filepath.Join(root, "Projects", project, "sessions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("2026-07-%02d-abcdef12-%02d.md", (i%28)+1, i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if report != "" {
		dir := filepath.Join(root, "Audits")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// The filename date is deliberately fixed and the frontmatter varies: the
		// stamp is what must be read, not the name.
		if err := os.WriteFile(filepath.Join(dir, "2026-07-01-vault-audit.md"), []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return storage.NewVault(root)
}

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// SILENT WHEN FRESH. If this ever fails, the nag has become the fifth thing an
// agent learns to skim, and every other alert on this payload goes with it.
func TestCheckStaleness_SilentWhenFresh(t *testing.T) {
	v := stalenessVault(t, 60, "---\ntype: vault-audit\ndate: 2026-07-01\nsession_notes: 58\n---\n\n# Vault Audit\n")

	s := CheckStaleness(v, day("2026-07-03"))
	if s.Warn {
		t.Errorf("nagged on a fresh vault (churn 2, age 2 days): %q", s.Message)
	}
	if s.Message != "" {
		t.Errorf("a fresh vault must say NOTHING, got %q", s.Message)
	}
	if !s.ChurnKnown || s.Churn != 2 {
		t.Errorf("churn = %d (known=%v), want 2 (known)", s.Churn, s.ChurnKnown)
	}
}

func TestCheckStaleness_NagsOnChurn(t *testing.T) {
	v := stalenessVault(t, 60, "---\ntype: vault-audit\ndate: 2026-07-01\nsession_notes: 8\n---\n\n# Vault Audit\n")

	// Age is only 1 day — well inside the 7-day window — so ONLY churn can trip it.
	s := CheckStaleness(v, day("2026-07-02"))
	if !s.Warn {
		t.Fatalf("no nag at churn %d against a threshold of %d", s.Churn, StaleChurnThreshold)
	}
	if s.Churn != 52 {
		t.Errorf("churn = %d, want 52 (60 notes now, 8 at the audit)", s.Churn)
	}
	if !strings.Contains(s.Message, "52 session notes") {
		t.Errorf("message does not carry the number that justifies it: %q", s.Message)
	}
}

func TestCheckStaleness_NagsOnAge(t *testing.T) {
	// Churn is ZERO — a vault that is quiet but drifting. Age is the second axis
	// precisely to catch it (a sync from another machine, a hand-edit, a bad merge).
	v := stalenessVault(t, 10, "---\ntype: vault-audit\ndate: 2026-07-01\nsession_notes: 10\n---\n\n# Vault Audit\n")

	s := CheckStaleness(v, day("2026-07-09"))
	if !s.Warn {
		t.Fatalf("no nag %v days after the last audit (threshold %d)", s.AgeDays, StaleAgeDays)
	}
	if !strings.Contains(s.Message, "2026-07-01") {
		t.Errorf("message does not name the last audit: %q", s.Message)
	}
}

// 🔴 THE REGRESSION TEST FOR THE BUG THIS NAG SHIPPED WITH FOR ONE AFTERNOON.
//
// A report written before the session_notes anchor existed has NO anchor. The first
// cut read the missing field back as 0 and announced "634 session notes written
// since yesterday's audit" — on a vault holding 634 notes IN TOTAL. It was caught by
// driving the real server, not by any test, which is the whole thesis of this epic
// arriving inside the code written to enforce it.
//
// Absence is not zero. No anchor ⇒ no churn CLAIM — fall back to the age axis, which
// is anchored on the report's own date and cannot be faked by a field that isn't there.
func TestCheckStaleness_UnstampedReportMakesNoChurnClaim(t *testing.T) {
	// 634 notes, and a report with a date but NO session_notes stamp — exactly the
	// shape of every report written before this feature existed.
	v := stalenessVault(t, 634, "---\ntype: vault-audit\ndate: 2026-07-01\n---\n\n# Vault Audit\n")

	s := CheckStaleness(v, day("2026-07-02")) // one day old: fresh by age

	if s.ChurnKnown {
		t.Error("churn reported as KNOWN against a report that carries no anchor")
	}
	if s.Churn != 0 {
		t.Errorf("churn = %d against an un-anchored report — the tool invented a number it never measured", s.Churn)
	}
	if s.Warn {
		t.Errorf("nagged on an un-anchored but one-day-old report: %q\nthis is the 634-notes-since-yesterday bug", s.Message)
	}

	// And the age axis still works, because it never depended on the anchor.
	if old := CheckStaleness(v, day("2026-07-20")); !old.Warn {
		t.Error("age axis did not fire on a 19-day-old report — the un-anchored fallback lost its only remaining signal")
	}
}

// A vault nobody has ever audited needs no special case, and gets none: its anchor
// is zero because there is genuinely nothing there, so churn is simply the corpus.
func TestCheckStaleness_NeverAudited(t *testing.T) {
	busy := stalenessVault(t, 200, "")
	if s := CheckStaleness(busy, day("2026-07-14")); !s.Warn {
		t.Error("a 200-note vault that has never been audited did not nag")
	} else if !strings.Contains(s.Message, "NEVER been audited") {
		t.Errorf("message does not say what is actually wrong: %q", s.Message)
	}

	// ...and a FRESH vault stays silent. The rule falls out of the arithmetic
	// instead of being bolted on beside it.
	fresh := stalenessVault(t, 9, "")
	if s := CheckStaleness(fresh, day("2026-07-14")); s.Warn {
		t.Errorf("nagged a brand-new 9-note vault to run an audit: %q", s.Message)
	}
}

// The count is a GLOB of Projects/*/sessions/, never palace/ — the 206 lesson, which
// cost this project 5 projects and 73 notes of blindness. A churn signal that cannot
// see whole projects under-counts exactly the vaults that most need auditing.
func TestSessionNoteCount_ReadsProjectsNotPalace(t *testing.T) {
	root := t.TempDir()
	mkfile := func(parts ...string) {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile("Projects", "a", "sessions", "2026-07-01-aaaaaaaa-01.md")
	mkfile("Projects", "b", "sessions", "2026-07-01-bbbbbbbb-01.md")
	mkfile("palace", "c", "drawer.md")           // not a session note
	mkfile("Projects", "a", "tasks", "thing.md") // not a session note

	if n := SessionNoteCount(storage.NewVault(root)); n != 2 {
		t.Errorf("SessionNoteCount = %d, want 2", n)
	}
}

// The anchor must actually reach the report, or churn has nothing to subtract from
// and the nag silently degrades to age-only forever.
func TestRender_StampsTheChurnAnchor(t *testing.T) {
	r := Report{SessionNotes: 417}

	dated := r.Render("2026-07-14", "/vault")
	if !strings.Contains(dated, "session_notes: 417") {
		t.Errorf("a dated report does not stamp its churn anchor:\n%s", firstLines(dated, 6))
	}

	// An UNDATED (inline) render is not a record of anything. Stamping it would let a
	// nag reset its own clock every time somebody merely LOOKED at the audit.
	if inline := r.Render("", "/vault"); strings.Contains(inline, "session_notes:") {
		t.Errorf("an undated inline render stamped an anchor:\n%s", firstLines(inline, 6))
	}
}
