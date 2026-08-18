// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestSemanticRankerOrdersBySessionHits pins Phase 3 slice 2: with a warm
// index and a session-typed drawer for the OLDEST (relevant) session only,
// recent_sessions leads with that session and ranking.ranker is semantic.
func TestSemanticRankerOrdersBySessionHits(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug:     "commit-log-archives-orphaned-commits",
		Title:    "The commit log archives orphaned commits",
		Content:  "body",
		Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}

	const relevant = "Orphaned commits in the archive"
	metas := []storage.SessionMeta{
		{Date: "2026-08-01", Title: relevant, Summary: "Investigated orphaned archive entries.", Tag: "investigation"},
		{Date: "2026-08-02", Title: "Unrelated colour tweak", Summary: "Palette.", Tag: "chore"},
		{Date: "2026-08-03", Title: "Another unrelated thing", Summary: "Rename.", Tag: "chore"},
	}
	var relevantID string
	for i, s := range metas {
		id, err := vault.WriteSession("test-proj", s, "body")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			relevantID = id
		}
	}
	if relevantID == "" {
		t.Fatal("relevant session id empty")
	}

	eng := search.NewEngine(embedder.NewMock(384), vault, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { _ = eng.Close() })
	d := storage.Drawer{
		Content:    "commit log archives orphaned commits investigation",
		Hall:       "facts",
		SourceType: "session",
		SourceRef:  relevantID,
		FiledAt:    "2026-08-01T00:00:00Z",
	}
	if err := vault.AppendDrawer("test-proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}
	stored, err := vault.ListDrawers("test-proj", "wing-a", "room-1")
	if err != nil || len(stored) == 0 {
		t.Fatalf("list drawers: %v n=%d", err, len(stored))
	}
	if err := eng.IndexDrawers(context.Background(), []search.DrawerInput{{
		Project: "test-proj", Wing: "wing-a", Room: "room-1", Drawer: stored[0],
	}}); err != nil {
		t.Fatal(err)
	}
	if !eng.HasIndex("test-proj") || !eng.EmbedderReady() {
		t.Fatal("engine should be ready after IndexDrawers with a mock embedder")
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault, eng), `{"project":"test-proj"}`)
	if br.Ranking == nil {
		t.Fatal("ranking absent")
	}
	if br.Ranking.Ranker != rankerSemantic {
		t.Fatalf("ranker = %q, want %q (fallback=%q)", br.Ranking.Ranker, rankerSemantic, br.Ranking.FallbackReason)
	}
	if br.Ranking.FallbackReason != "" {
		t.Errorf("fallback_reason = %q, want empty on semantic success", br.Ranking.FallbackReason)
	}
	if len(br.RecentSessions) == 0 || br.RecentSessions[0].Title != relevant {
		t.Fatalf("session index = %+v, want %q first", br.RecentSessions, relevant)
	}
}

func TestSemanticRankerFallsBackWhenIndexCold(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug: "some-task", Title: "Some task", Content: "body", Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-01", Title: "a session", Summary: "x", Tag: "chore",
	}, "body"); err != nil {
		t.Fatal(err)
	}
	eng := search.NewEngine(embedder.NewMock(384), vault, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { _ = eng.Close() })
	// No IndexDrawers → HasIndex false.
	br := bootstrapResult(t, BootstrapContextTool(resolver, vault, eng), `{"project":"test-proj"}`)
	if br.Ranking.Ranker != rankerStructural {
		t.Errorf("ranker = %q, want structural", br.Ranking.Ranker)
	}
	if br.Ranking.FallbackReason != fallbackIndexNotReady {
		t.Errorf("fallback_reason = %q, want %q", br.Ranking.FallbackReason, fallbackIndexNotReady)
	}
}

func TestSemanticRankerFallsBackWhenEmbedderCold(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug: "some-task", Title: "Some task", Content: "body", Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-01", Title: "a session", Summary: "x", Tag: "chore",
	}, "body"); err != nil {
		t.Fatal(err)
	}

	var constructions int
	lazy := embedder.NewLazy(func() (embedder.Embedder, error) {
		constructions++
		return embedder.NewMock(384), nil
	})
	eng := search.NewEngine(lazy, vault, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { _ = eng.Close() })
	if eng.EmbedderReady() {
		t.Fatal("lazy embedder should not be Ready before first Embed")
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault, eng), `{"project":"test-proj"}`)
	// EmbedderNotReady is checked before IndexNotReady, so a cold lazy embedder
	// reports that reason even when HasIndex is also false.
	if br.Ranking.Ranker != rankerStructural || br.Ranking.FallbackReason != fallbackEmbedderNotReady {
		t.Errorf("ranker=%q reason=%q, want structural/%s", br.Ranking.Ranker, br.Ranking.FallbackReason, fallbackEmbedderNotReady)
	}
	if constructions != 0 {
		t.Fatalf("bootstrap forced lazy construct %d times — investigation E violated", constructions)
	}
}

func TestSemanticRankerFallsBackWhenEngineNil(t *testing.T) {
	vault, resolver := testSetup(t)
	if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-01", Title: "a session", Summary: "x", Tag: "chore",
	}, "body"); err != nil {
		t.Fatal(err)
	}
	br := bootstrapResult(t, BootstrapContextTool(resolver, vault, nil), `{"project":"test-proj"}`)
	if br.Ranking.Ranker != rankerStructural || br.Ranking.FallbackReason != fallbackEngineNil {
		t.Errorf("ranker=%q reason=%q, want structural/%s", br.Ranking.Ranker, br.Ranking.FallbackReason, fallbackEngineNil)
	}
}

// TestSemanticRankerIgnoresIterationOnlyHits is the mlnx-sw-os / sessions-only
// fork: a warm index whose ONLY hits are source_type=iteration must not flip
// the ranker to semantic, and recent_sessions must remain session note titles.
//
// The iteration drawer deliberately reuses a session id as SourceRef — the
// shape that would falsely score a session if the source_type=="session"
// filter were dropped (mutation target below).
func TestSemanticRankerIgnoresIterationOnlyHits(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug:     "commit-log-archives-orphaned-commits",
		Title:    "The commit log archives orphaned commits",
		Content:  "body",
		Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}

	const sessionTitle = "Operator wrap notes from Tuesday"
	id, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-01", Title: sessionTitle, Summary: "plain session narrative", Tag: "implementation",
	}, "body")
	if err != nil {
		t.Fatal(err)
	}
	// Newest session so structural/recency alone would still lead with a session title.
	if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-03", Title: "Later chore session", Summary: "unrelated", Tag: "chore",
	}, "body"); err != nil {
		t.Fatal(err)
	}

	eng := search.NewEngine(embedder.NewMock(384), vault, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { _ = eng.Close() })

	const iterTitle = "## Iteration 99 — Iteration chunk about commit log archives"
	d := storage.Drawer{
		Content:    iterTitle + "\n\ncommit log archives orphaned commits narrative body",
		Hall:       "narrative",
		SourceType: "iteration",
		// Same id a session drawer would carry — without the type filter this
		// would score that session and flip ranker to semantic.
		SourceRef: id,
		FiledAt:   "2026-08-01T00:00:00Z",
	}
	if err := vault.AppendDrawer("test-proj", "history", "iterations", d); err != nil {
		t.Fatal(err)
	}
	stored, err := vault.ListDrawers("test-proj", "history", "iterations")
	if err != nil || len(stored) == 0 {
		t.Fatalf("list drawers: %v n=%d", err, len(stored))
	}
	if err := eng.IndexDrawers(context.Background(), []search.DrawerInput{{
		Project: "test-proj", Wing: "history", Room: "iterations", Drawer: stored[0],
	}}); err != nil {
		t.Fatal(err)
	}
	if !eng.HasIndex("test-proj") {
		t.Fatal("warm index required")
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault, eng), `{"project":"test-proj"}`)
	if br.Ranking == nil {
		t.Fatal("ranking absent")
	}
	if br.Ranking.Ranker != rankerStructural {
		t.Fatalf("ranker = %q, want structural (iteration-only corpus must not go semantic)", br.Ranking.Ranker)
	}
	if br.Ranking.FallbackReason != fallbackNoSessionHits {
		t.Fatalf("fallback_reason = %q, want %q", br.Ranking.FallbackReason, fallbackNoSessionHits)
	}
	if len(br.RecentSessions) == 0 {
		t.Fatal("recent_sessions empty")
	}
	for _, row := range br.RecentSessions {
		if strings.Contains(row.Title, "Iteration chunk") || strings.HasPrefix(row.Title, "## Iteration") {
			t.Fatalf("recent_sessions carried an iteration title %q — sessions-only violated", row.Title)
		}
	}
	// Structural/recency: newest session first.
	if br.RecentSessions[0].Title != "Later chore session" {
		t.Errorf("recent_sessions[0] = %q, want the newest session note (structural order)", br.RecentSessions[0].Title)
	}
}
