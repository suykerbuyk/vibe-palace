// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestHeadOfQueueIsGraphOrderNotListOrder is mutation 2's target: the queue must
// come from the task GRAPH, not from the directory listing.
//
// 🔴 THE FIXTURE IS BUILT SO THE TWO ORDERS DISAGREE, WHICH IS THE ONLY WAY THIS
// MEASURES ANYTHING. ListTasks returns tasks in filename order, so a fixture
// whose alphabetical order already matches the intended order passes with the
// derivation replaced by `vault.ListTasks` — the 307 trap, where a test cannot
// reach the logic it claims to guard. Here `a-blocked-task` sorts FIRST
// alphabetically and must not appear at all, and `z-in-progress` sorts LAST and
// must come first.
func TestHeadOfQueueIsGraphOrderNotListOrder(t *testing.T) {
	vault, resolver := testSetup(t)

	mustCreate := func(slug, title, priority string) {
		t.Helper()
		if err := vault.CreateTask("test-proj", storage.TaskSpec{
			Slug: slug, Title: title, Content: "body", Priority: priority,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Filename order: a-blocked-task, m-unblocked-high, n-icebox, z-in-progress.
	mustCreate("a-blocked-task", "Blocked on the gate", "critical")
	mustCreate("m-unblocked-high", "Unblocked and high", "high")
	mustCreate("n-icebox", "Known, not scheduled", "critical")
	mustCreate("z-in-progress", "Already under way", "low")
	mustCreate("the-gate", "The blocking dependency", "high")

	// a-blocked-task waits on the-gate, which is still open — so it is blocked
	// even though it is the highest-priority task and sorts first by filename.
	deps := []string{"the-gate"}
	if err := vault.SetTaskRelations("test-proj", "a-blocked-task", storage.TaskRelations{Depends: &deps}); err != nil {
		t.Fatal(err)
	}
	if err := vault.UpdateTaskStatus("test-proj", "z-in-progress", storage.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := vault.UpdateTaskStatus("test-proj", "n-icebox", storage.StatusIcebox); err != nil {
		t.Fatal(err)
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj"}`)

	got := make([]string, 0, len(br.HeadOfQueue))
	for _, row := range br.HeadOfQueue {
		got = append(got, row.Slug)
	}

	// In-progress first, then priority, then the graph's own order.
	want := []string{"z-in-progress", "m-unblocked-high", "the-gate"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("head of queue = %v, want %v\n"+
			"  in-progress must lead; a BLOCKED task must not appear at all however urgent; icebox is not intent.\n"+
			"  If this reads like the filename order (a-blocked-task first), the derivation is reading "+
			"ListTasks instead of the graph.", got, want)
	}

	// The blocked and iceboxed work still exists and is still counted — it is
	// withheld from the QUEUE, not hidden from the payload.
	if br.ActiveTaskCount != 4 {
		t.Errorf("active_task_count = %d, want 4 (icebox excluded, blocked included)", br.ActiveTaskCount)
	}
	for _, row := range br.HeadOfQueue {
		if row.Slug == "a-blocked-task" {
			t.Error("a blocked task reached the head of queue — an agent cannot start work whose dependency is open")
		}
		if row.Slug == "n-icebox" {
			t.Error("an iceboxed task reached the head of queue — icebox is deliberately unscheduled")
		}
	}
}

// TestHeadOfQueueRowsCarryTheirHandle pins that every row is fetchable. A row an
// agent cannot open is a name, not an index entry.
func TestHeadOfQueueRowsCarryTheirHandle(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug: "only-task", Title: "The only task", Content: "body", Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj"}`)
	if len(br.HeadOfQueue) != 1 {
		t.Fatalf("head_of_queue = %d rows, want 1", len(br.HeadOfQueue))
	}
	if got, want := br.HeadOfQueue[0].URI, "vibe-palace://task/test-proj/only-task"; got != want {
		t.Errorf("row uri = %q, want %q", got, want)
	}
	if br.Ranking == nil || br.Ranking.RankedAgainst != "only-task" {
		t.Errorf("ranking should name the slug it ranked against, got %+v", br.Ranking)
	}
}

// TestSessionIndexRanksByRelevanceNotRecency is mutation 3's target, and the
// POSITIVE CONTROL for the ranker.
//
// 🔴 THE FIXTURE MUST MAKE RELEVANCE ORDER DISAGREE WITH RECENCY ORDER. A
// ranker that scored nothing and simply returned the newest five would pass any
// fixture where the two agree — which is every naturally-ordered fixture, and is
// exactly how a dead ranker ships looking green (307: a rule with no offending
// instance never exercises its matching logic). So the relevant session is
// written FIRST, making it the OLDEST, and it must still come back at the top.
func TestSessionIndexRanksByRelevanceNotRecency(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug:     "commit-log-archives-orphaned-commits",
		Title:    "The commit log archives orphaned commits",
		Content:  "body",
		Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}

	// Oldest first, so recency order is the exact reverse of what is written.
	//
	// 🔴 THE ASSERTION KEYS ON TITLE, NOT ON ITERATION, AND THAT IS NOT A STYLE
	// CHOICE. WriteSession STAMPS the iteration number itself and stamps 1 for
	// every note in a fresh vault — measured, after mutation 3 failed to redden
	// this test. An assertion on a field that cannot vary cannot fail (301), so
	// the first version of this test passed with the scorer replaced by a
	// constant: the exact dead positive control its own comment warns about.
	const relevant = "Orphaned commits in the archive"
	sessions := []storage.SessionMeta{
		{Date: "2026-08-01", Title: relevant,
			Summary: "Investigated why the commit log archives orphaned entries.", Tag: "investigation"},
		{Date: "2026-08-02", Title: "Unrelated colour tweak",
			Summary: "Adjusted some palette values.", Tag: "chore"},
		{Date: "2026-08-03", Title: "Another unrelated thing",
			Summary: "Renamed a helper.", Tag: "chore"},
	}
	for _, s := range sessions {
		if _, err := vault.WriteSession("test-proj", s, "body"); err != nil {
			t.Fatal(err)
		}
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj"}`)
	if len(br.RecentSessions) != 3 {
		t.Fatalf("session index = %d rows, want 3", len(br.RecentSessions))
	}
	got := make([]string, 0, len(br.RecentSessions))
	for _, s := range br.RecentSessions {
		got = append(got, s.Title)
	}
	if got[0] != relevant {
		t.Errorf("session index leads with %q, want %q — that session is the OLDEST, and it is the one "+
			"that matches the head of queue. An index led by the newest row is recency order wearing a "+
			"ranker's name. Order: %v", got[0], relevant, got)
	}
	// The fixture's discriminating power is structural, not runtime: the relevant
	// session is written FIRST, so it is the oldest, so recency order puts it
	// LAST. Any ordering that leads with it had to have scored it.

	// The instrument must describe what actually happened.
	if br.Ranking == nil {
		t.Fatal("ranking is absent")
	}
	if br.Ranking.Ranker != rankerStructural {
		t.Errorf("ranking.ranker = %q, want %q", br.Ranking.Ranker, rankerStructural)
	}
	if br.Ranking.Candidates != 3 || br.Ranking.Returned != 3 {
		t.Errorf("ranking = %+v, want 3 candidates and 3 returned", *br.Ranking)
	}
}

// TestSessionIndexCarriesNoSummaryBody pins the other half of the session
// change: the row is an index entry, and the narrative stays behind the URI.
func TestSessionIndexCarriesNoSummaryBody(t *testing.T) {
	vault, resolver := testSetup(t)
	const narrative = "A long narrative that belongs in the session note and not in every bootstrap payload."
	if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-18", Title: "a session", Summary: narrative, Tag: "implementation",
	}, "body"); err != nil {
		t.Fatal(err)
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj"}`)
	if len(br.RecentSessions) != 1 {
		t.Fatalf("session index = %d rows, want 1", len(br.RecentSessions))
	}
	row := br.RecentSessions[0]
	// The iteration number is stamped by WriteSession, not by the caller, so the
	// assertion is that the row CARRIES one — not that it carries the value this
	// fixture asked for, which the writer is entitled to override.
	if row.Title != "a session" || row.Date != "2026-08-18" || row.Tag != "implementation" || row.Iteration == 0 {
		t.Errorf("row lost the metadata a reader chooses on: %+v", row)
	}
	if row.URI == "" {
		t.Error("row carries no uri — dropping the body without a handle deletes the session from the agent's reach")
	}
	if !strings.HasPrefix(row.URI, "vibe-palace://session/test-proj/") {
		t.Errorf("row uri = %q, want a session handle", row.URI)
	}
}

// TestHeadOfQueueTermsDropNoiseWords pins the floor that keeps the ranker from
// degenerating into recency order while still reporting itself as a ranker.
//
// A query of "the", "and", "for" matches every session ever written, so every
// candidate scores identically and the tie-break — recency — decides everything.
// That failure is invisible from outside: the payload looks ranked.
func TestHeadOfQueueTermsDropNoiseWords(t *testing.T) {
	terms := headOfQueueTerms([]headOfQueueRow{{
		Slug:  "the-commit-log-and-a-bug",
		Title: "The commit log has a bug",
	}})

	got := map[string]bool{}
	for _, term := range terms {
		got[term] = true
	}
	for _, want := range []string{"commit", "bug"} {
		if !got[want] {
			t.Errorf("term %q was dropped — the query lost the words that actually discriminate: %v", want, terms)
		}
	}
	for _, noise := range []string{"the", "and", "a", "has"} {
		if got[noise] {
			t.Errorf("noise word %q survived into the query — it matches every session, so it flattens "+
				"every score and hands the ordering back to recency: %v", noise, terms)
		}
	}
}
