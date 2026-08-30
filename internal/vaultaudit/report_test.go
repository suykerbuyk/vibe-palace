// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"strings"
	"testing"
)

// 🔴 TestRender_UndatedReportDoesNotFakeADate pins a defect found on the tool's FIRST
// real MCP invocation: Render emitted the date field even when handed no date, so the
// report opened with `date:` followed by nothing and a title ending in a bare em-dash —
// a document ASSERTING it has a date and then not having one.
//
// What is pinned here is the HOLLOW-FIELD rule, and only that. The reason Render used to
// be handed an empty date — that the server should not invent one the caller knows — was
// WITHDRAWN by the operator on 2026-08-19: the writer owns the calendar day and clients
// do not send one (see storage.Vault.CalendarDay). No shipping caller passes "" today.
// The rule survives the reversal because it is about rendering, not about authority: a
// function handed no date must say so rather than fake the field.
//
// A hollow field is worse than an absent one, because it looks like data — a reader
// trusts it, and a parser reads an empty string as a value. Shipping one from THIS
// package, whose whole subject is instruments that claim more than they know, would have
// been the joke writing itself.
func TestRender_UndatedReportDoesNotFakeADate(t *testing.T) {
	r := Report{Dimensions: []DimensionResult{
		{Name: "d", Status: StatusPass, Evidence: "echo hi"},
	}}

	out := r.Render("", "/some/vault")

	if strings.Contains(out, "date:") {
		t.Errorf("an undated report emitted a date FIELD — absent must mean absent, not empty:\n%s",
			firstLines(out, 6))
	}
	if strings.Contains(out, "— \n") || strings.Contains(out, "Vault Audit — \n") {
		t.Errorf("the title trails a bare em-dash with nothing after it:\n%s", firstLines(out, 6))
	}
	if !strings.Contains(out, "undated") {
		t.Errorf("an undated report must SAY it is undated, not merely omit the date:\n%s",
			firstLines(out, 6))
	}
	// And it must still tell the reader how to produce a dated, committed one — that is
	// the artifact `git log -p Audits/` diffs.
	if !strings.Contains(out, "--write") {
		t.Error("an undated report should point at the flag that stamps and commits one")
	}
}

// TestRender_DatedReportCarriesTheDate — the other half. The date is what names the file
// and what the frontmatter asserts, and the two must agree.
func TestRender_DatedReportCarriesTheDate(t *testing.T) {
	r := Report{Dimensions: []DimensionResult{
		{Name: "d", Status: StatusPass, Evidence: "echo hi"},
	}}

	out := r.Render("2026-07-14", "/some/vault")

	if !strings.Contains(out, "date: 2026-07-14") {
		t.Errorf("frontmatter lost the date:\n%s", firstLines(out, 6))
	}
	if !strings.Contains(out, "# Vault Audit — 2026-07-14") {
		t.Errorf("title lost the date:\n%s", firstLines(out, 6))
	}
	if strings.Contains(out, "undated") {
		t.Error("a dated report must not describe itself as undated")
	}
	// The filename and the frontmatter must agree, or the report says one thing and is
	// filed as another.
	if got := ReportRelPath("2026-07-14"); !strings.Contains(got, "2026-07-14") {
		t.Errorf("ReportRelPath(%q) = %q — the file name and the stamped date must agree",
			"2026-07-14", got)
	}
}

// TestRender_AbsolutePathIsNeverWrittenIntoTheReport: the vault syncs to every machine
// and lives somewhere different on each, so an absolute path is a fact about the host
// that wrote it and a lie everywhere else (188).
func TestRender_AbsolutePathIsNeverWrittenIntoTheReport(t *testing.T) {
	r := Report{Dimensions: []DimensionResult{{Name: "d", Status: StatusPass, Evidence: "x"}}}

	out := r.Render("2026-07-14", "/home/someone/obsidian/vibe-palace-vault")

	if strings.Contains(out, "/home/someone") {
		t.Errorf("the report leaked a host-specific absolute path:\n%s", firstLines(out, 8))
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
