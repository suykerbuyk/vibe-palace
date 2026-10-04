// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestRebuild_AnEditedSourceIsEmbeddedAgain covers all four id families a
// Rebuild derives itself: note bodies, note summaries, iteration summaries and
// raw iteration entries. Each is built, edited, and rebuilt: the second Rebuild
// must embed exactly the edited chunk, and a query for the new text must rank
// it first: the query is the row's exact chunk text, so only the row's own,
// current vector is at distance 0. With identity-only ids the edit hit the old vector (embedded=0),
// so search kept answering with the text the edit replaced.
func TestRebuild_AnEditedSourceIsEmbeddedAgain(t *testing.T) {
	const oldText = "OLD_TEXT_AARDVARK_BEFORE_THE_EDIT"
	const newText = "NEW_TEXT_PANGOLIN_AFTER_THE_EDIT"

	cases := []struct {
		name  string
		write func(t *testing.T, v *storage.Vault, text string)
		want  string                   // SourceType of the edited row
		query func(text string) string // the edited row's exact chunk text
	}{
		{"note body", func(t *testing.T, v *storage.Vault, text string) {
			writeSessionNote(t, v.Root, "edited", "2026-08-01-aaaa0000-01", "2026-08-01", "wrap", text)
		}, noteSourceType, func(s string) string { return s }},
		{"note summary", func(t *testing.T, v *storage.Vault, text string) {
			writeSessionNoteWithSummary(t, v.Root, "edited", "2026-08-01-aaaa0000-01", "2026-08-01", "wrap",
				"An unchanging body.", text)
		}, noteSummarySourceType, func(s string) string { return s }},
		{"iteration raw", func(t *testing.T, v *storage.Vault, text string) {
			writeIterationsMD(t, v.Root, "edited", strings.Join([]string{
				"## Iteration 1 — first", "", text, "", "---", "",
			}, "\n"))
		}, iterationRawSourceType, func(s string) string { return "## Iteration 1 — first\n\n" + s }},
		{"iteration summary", func(t *testing.T, v *storage.Vault, text string) {
			writeIterationsMD(t, v.Root, "edited", strings.Join([]string{
				"## Iteration 1 — first", "", "An unchanging body.", "", "---", "",
			}, "\n"))
			if err := v.WriteIterationSummary("edited", storage.IterationSummary{N: 1, MatchIndex: 0, Summary: text}); err != nil {
				t.Fatal(err)
			}
		}, iterationSourceType, func(s string) string { return s }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, v := testEngine(t)
			ctx := context.Background()
			tc.write(t, v, oldText)
			if _, err := eng.Rebuild(ctx, "edited"); err != nil {
				t.Fatal(err)
			}
			tc.write(t, v, newText)
			st, err := eng.Rebuild(ctx, "edited")
			if err != nil {
				t.Fatal(err)
			}
			if st.Embedded != 1 {
				t.Fatalf("second Rebuild embedded %d chunks, want exactly the edited one (stats %+v)", st.Embedded, st)
			}
			res, err := eng.Search(ctx, tc.query(newText), SearchFilters{Project: "edited", Limit: 5, IncludeRaw: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(res) == 0 || res[0].SourceType != tc.want || !strings.Contains(res[0].Content, newText) {
				t.Fatalf("the edited %s row must rank first with its new text; got %+v", tc.name, res)
			}
		})
	}
}
