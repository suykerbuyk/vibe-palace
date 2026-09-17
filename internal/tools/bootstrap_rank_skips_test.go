// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestRankingReportIsNeverSilent closes the hole that made a two-project outage
// read as normal.
//
// context_tools.go attaches the ranking report UNCONDITIONALLY, and its comment
// there says why: attaching it only on the success path would make "the ranker
// found nothing" and "the ranker never ran" the same bytes. That defence was
// real and it was incomplete — the report was attached, and then arrived with an
// EMPTY fallback_reason, which is the same ambiguity one field deeper. A healthy
// project with nothing semantic to say and a project whose entire session index
// had just been deleted by one malformed note produced identical payloads.
//
// Break: restore the bare `return nil, report` on the zero-length arm of
// rankSessionIndex.
func TestRankingReportIsNeverSilent(t *testing.T) {
	cases := []struct {
		name     string
		sessions []storage.SessionMeta
		skipped  []storage.RecordSkip
		want     string
	}{
		{
			name: "empty project",
			want: fallbackNoSessions,
		},
		{
			name:    "every note unreadable",
			skipped: []storage.RecordSkip{{Path: "Projects/p/sessions/bad.md", Reason: "yaml"}},
			want:    fallbackNotesUnreadable,
		},
		{
			name:     "notes present, no engine",
			sessions: []storage.SessionMeta{{ID: "a", Date: "2026-05-01"}},
			want:     fallbackEngineNil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, report := rankSessionIndex("p", tc.sessions, nil, 5, nil, tc.skipped)
			if report.FallbackReason == "" {
				t.Fatalf("the ranking report came back with NO reason. An instrument that goes silent "+
					"is indistinguishable from one that never ran, which is how a live outage read as "+
					"normal for weeks.\n  report: %+v", report)
			}
			if report.FallbackReason != tc.want {
				t.Errorf("fallback_reason = %q, want %q", report.FallbackReason, tc.want)
			}
		})
	}
}

// TestRankingReportCarriesSkippedNotes pins that a short index says it is short.
//
// The count of sessions alone cannot distinguish "this project has little
// history" from "this project's history is unreadable", and those need opposite
// responses from whoever reads the payload.
//
// Break: stop copying skips into report.SkippedNotes in rankSessionIndex.
func TestRankingReportCarriesSkippedNotes(t *testing.T) {
	skipped := []storage.RecordSkip{
		{Path: "Projects/p/sessions/2026-09-15-03.md", Reason: "yaml: line 20"},
	}
	_, report := rankSessionIndex("p", []storage.SessionMeta{{ID: "a"}}, nil, 5, nil, skipped)

	if !slices.Contains(report.SkippedNotes, "Projects/p/sessions/2026-09-15-03.md") {
		t.Errorf("the payload must name the notes the reader could not read; a short index that does "+
			"not say it is short is quietly wrong rather than merely partial.\n  got: %v",
			report.SkippedNotes)
	}
}

// TestNotesUnreadableOutranksNoSessions pins the precedence.
//
// "This project is new" and "this project's notes are broken" are different
// facts and the second must win when both are true, because only one of them
// needs somebody to go and fix something.
//
// Break: swap the order of the two assignments on the zero-length arm.
func TestNotesUnreadableOutranksNoSessions(t *testing.T) {
	_, report := rankSessionIndex("p", nil, nil, 5, nil,
		[]storage.RecordSkip{{Path: "x.md", Reason: "yaml"}})

	if report.FallbackReason != fallbackNotesUnreadable {
		t.Errorf("fallback_reason = %q, want %q — a project whose notes are UNREADABLE must not be "+
			"reported as merely having none, or the payload sends the reader to the wrong conclusion",
			report.FallbackReason, fallbackNotesUnreadable)
	}
}
