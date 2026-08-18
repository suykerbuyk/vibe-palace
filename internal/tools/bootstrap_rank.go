// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

// headOfQueueN is how many head-of-queue rows and how many session index rows
// the bootstrap returns.
//
// 🔴 IT IS A SHAPE CONSTANT, NOT A BUDGET, AND THE DIFFERENCE IS THE WHOLE OF
// PRD §1.10. Nothing here measures, compares or compares against a payload
// size; this bounds how many ROWS an index carries, the same way memoryRecallCap
// does and the same way the old five-session recency trim did. A number that
// answers "how long is this list" is a shape; a number that answers "how many
// bytes may this payload be" is the apparatus Phase 2 deleted, and it must not
// grow back under a new name.
const headOfQueueN = 5

// RankingReport states how this payload's rows were ordered, and by what.
//
// 🔴 IT IS NEVER SILENT, WHICH MAKES IT THE ONE EXCEPTION TO "SILENT WHEN
// HEALTHY", AND THE EXCEPTION IS EARNED. Every other instrument reports a
// CONDITION — something is wrong, act on it — so an always-on one trains the
// reader to skim. This reports a property of the payload itself: which ranker
// ordered the rows below, and how many candidates it chose them from. An
// ordered list that does not say what ordered it is indistinguishable from
// recency order, and a reader cannot derive the difference from the rows. That
// is 286's lesson pointed at ordering instead of at size: the instrument that
// describes a reduction has to arrive with it.
//
// Ranker is "structural" (deterministic lexical) or "semantic" (search-index
// scores against an already-warm embedder+index). Slice 2 of first-principles
// Phase 3 added the semantic value behind this same field.
//
// Candidates and Returned describe the SESSION index: how many session notes the
// ranker scored, and how many rows it returned. They do not describe
// head_of_queue, which is the ranking's QUERY rather than its output — that
// trim is already visible as len(head_of_queue) against active_task_count.
//
// RankedAgainst names the slug the query was derived FROM. It is empty when the
// project has no eligible open task, and an empty value is the honest report
// that the ranker had no signal: every session then scores zero and the order
// falls back to recency, which is exactly what the rows show.
//
// FallbackReason is set when the semantic path was considered but not used
// (cold embedder, missing in-memory index, no session-typed hits, nil engine).
// Empty when structural was the intentional result, including empty-corpus
// projects that have nothing semantic to say.
//
// 🔴 IT IS NOT CALLED head_of_queue ON THE WIRE, AND THAT IS DELIBERATE. The
// payload already has a `head_of_queue` key — the row list — and two identical
// keys at different depths make "the first occurrence of head_of_queue" mean
// different things depending on which one marshals first. The wire-order tests
// use the bulk key as the instrument/index boundary and silently measured the
// wrong offset until this was renamed.
type RankingReport struct {
	Ranker          string `json:"ranker"`
	RankedAgainst   string `json:"ranked_against,omitempty"`
	Candidates      int    `json:"candidates"`
	Returned        int    `json:"returned"`
	FallbackReason  string `json:"fallback_reason,omitempty"`
}

// rankerStructural is the deterministic ranker: task-graph order for the queue,
// lexical overlap with the queue's own terms for the session index. It reads no
// embedder and no search index, so it behaves identically on a project with no
// indexed corpus at all — which is the majority of this vault's projects, and
// includes the one with the largest resume.
const rankerStructural = "structural"

// rankerSemantic scores sessions via search.Engine.SearchReady against an
// already-warm index. Bootstrap never calls ensureIndex or forces LazyEmbedder
// construction; if either is cold, the path falls back to structural.
const rankerSemantic = "semantic"

// Fallback reasons reported on RankingReport when semantic cannot run.
const (
	fallbackEngineNil       = "engine_nil"
	fallbackEmbedderNotReady = "embedder_not_ready"
	fallbackIndexNotReady   = "index_not_ready"
	fallbackNoSessionHits   = "no_session_hits"
)

// headOfQueueRow is one task in the derived head of queue: what the project
// intends to do next, and the handle that fetches its body.
//
// It carries no dependency list on purpose. Rows here are UNBLOCKED by
// construction (deriveHeadOfQueue filters on it), so a blockers field could
// never be non-empty — and a field that cannot fire is indistinguishable from
// a field that is broken. The blocked work is still reachable: active_task_count
// counts it and vp_list_tasks lists it, grouped and dependency-ordered.
type headOfQueueRow struct {
	Slug     string `json:"slug"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
	Parent   string `json:"parent,omitempty"`
	URI      string `json:"uri"`
}

// deriveHeadOfQueue answers "what comes next" from the task graph rather than
// from recency, per PRD §1.9: the server can see the graph, so the server
// computes it.
//
// The ordering is: in-progress work first (a session that resumes something
// already started should see it at the top), then priority, then the graph's own
// topological position so a dependency precedes its dependent, then slug for a
// stable tie-break. Only unblocked, non-icebox, non-terminal tasks are eligible
// — an agent cannot start work whose dependency has not landed, and icebox is
// deliberately-unscheduled by definition.
//
// A task caught in a dependency cycle has no defensible topological position and
// taskgraph excludes it from Order; it stays eligible here and sorts after
// everything that does have one, rather than vanishing from the payload.
func deriveHeadOfQueue(vault *storage.Vault, project string, n int) []headOfQueueRow {
	g, err := taskgraph.BuildFromVault(vault, project)
	if err != nil {
		return nil
	}

	orderIdx := make(map[string]int, len(g.Order))
	for i, slug := range g.Order {
		orderIdx[slug] = i
	}

	eligible := make([]*taskgraph.Node, 0, len(g.Nodes))
	for _, node := range g.Nodes {
		if node.Meta.Done || node.Meta.Status == storage.StatusIcebox {
			continue
		}
		if len(node.Blockers) > 0 {
			continue
		}
		eligible = append(eligible, node)
	}

	// A node with no topological position sorts after every node that has one.
	pos := func(slug string) int {
		if i, ok := orderIdx[slug]; ok {
			return i
		}
		return len(g.Order)
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if ai, bi := inProgressRank(a.Meta.Status), inProgressRank(b.Meta.Status); ai != bi {
			return ai < bi
		}
		if ap, bp := taskgraph.PriorityRank(a.Meta.Priority), taskgraph.PriorityRank(b.Meta.Priority); ap != bp {
			return ap < bp
		}
		if pa, pb := pos(a.Meta.Slug), pos(b.Meta.Slug); pa != pb {
			return pa < pb
		}
		return a.Meta.Slug < b.Meta.Slug
	})

	if n < len(eligible) {
		eligible = eligible[:n]
	}
	rows := make([]headOfQueueRow, 0, len(eligible))
	for _, node := range eligible {
		rows = append(rows, headOfQueueRow{
			Slug:     node.Meta.Slug,
			Title:    node.Meta.Title,
			Status:   node.Meta.Status,
			Priority: node.Meta.Priority,
			Parent:   node.Meta.Parent,
			URI:      mcp.TaskURI(project, node.Meta.Slug),
		})
	}
	return rows
}

// inProgressRank sorts work already under way ahead of work not yet started.
func inProgressRank(status string) int {
	if status == storage.StatusInProgress {
		return 0
	}
	return 1
}

// rankSessionIndex chooses structural or semantic ordering for recent_sessions.
// Semantic runs only when the engine's embedder is already warm and the project
// index is already in memory — never ensureIndex, never LazyEmbedder construct.
func rankSessionIndex(project string, sessions []storage.SessionMeta, terms []string, n int, eng *search.Engine) (rows []sessionSummary, report RankingReport) {
	report = RankingReport{Ranker: rankerStructural, Candidates: len(sessions)}
	if len(sessions) == 0 {
		return nil, report
	}

	structuralRows, _ := rankSessions(project, sessions, terms, n)

	if eng == nil {
		report.FallbackReason = fallbackEngineNil
		report.Returned = len(structuralRows)
		return structuralRows, report
	}
	if !eng.EmbedderReady() {
		report.FallbackReason = fallbackEmbedderNotReady
		report.Returned = len(structuralRows)
		return structuralRows, report
	}
	if !eng.HasIndex(project) {
		report.FallbackReason = fallbackIndexNotReady
		report.Returned = len(structuralRows)
		return structuralRows, report
	}

	semRows, ok := rankSessionsSemantic(project, sessions, terms, n, eng)
	if !ok {
		report.FallbackReason = fallbackNoSessionHits
		report.Returned = len(structuralRows)
		return structuralRows, report
	}
	report.Ranker = rankerSemantic
	report.Returned = len(semRows)
	return semRows, report
}

// rankSessionsSemantic scores sessions from SearchReady hits whose source_type
// is "session" and whose source_ref matches a session note id. Returns ok=false
// when no session-typed hit maps, so the caller keeps structural order.
func rankSessionsSemantic(project string, sessions []storage.SessionMeta, terms []string, n int, eng *search.Engine) ([]sessionSummary, bool) {
	if len(terms) == 0 || eng == nil {
		return nil, false
	}
	byID := make(map[string]storage.SessionMeta, len(sessions))
	age := make(map[string]int, len(sessions))
	for i, s := range sessions {
		if s.ID == "" {
			continue
		}
		byID[s.ID] = s
		age[s.ID] = i
	}
	if len(byID) == 0 {
		return nil, false
	}

	query := strings.Join(terms, " ")
	hits, err := eng.SearchReady(context.Background(), query, search.SearchFilters{
		Project: project,
		Limit:   max(n*10, 20),
	})
	if err != nil || len(hits) == 0 {
		return nil, false
	}

	best := make(map[string]float64, len(byID))
	for _, h := range hits {
		if h.SourceType != "session" || h.SourceRef == "" {
			continue
		}
		if _, ok := byID[h.SourceRef]; !ok {
			continue
		}
		if h.Score > best[h.SourceRef] {
			best[h.SourceRef] = h.Score
		}
	}
	if len(best) == 0 {
		return nil, false
	}

	type scored struct {
		meta  storage.SessionMeta
		score float64
		age   int
	}
	ranked := make([]scored, 0, len(sessions))
	for _, s := range sessions {
		ranked = append(ranked, scored{meta: s, score: best[s.ID], age: age[s.ID]})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].age > ranked[j].age
	})
	if n < len(ranked) {
		ranked = ranked[:n]
	}
	rows := make([]sessionSummary, 0, len(ranked))
	for _, r := range ranked {
		row := sessionSummary{
			Date:      r.meta.Date,
			Iteration: r.meta.Iteration,
			Title:     r.meta.Title,
			Tag:       r.meta.Tag,
		}
		if r.meta.ID != "" {
			row.URI = mcp.SessionURI(project, r.meta.ID)
		}
		rows = append(rows, row)
	}
	return rows, true
}

// rankSessions orders the session index by relevance to the head of queue and
// returns the top n rows plus the number of candidates it scored.
//
// Relevance is the count of distinct query terms a session's own title, summary
// and tag carry. Ties — including the every-score-zero case a project with no
// open task produces — fall back to recency, most recent first, so the result is
// never worse than the recency order this replaced.
//
// 🔴 THE SUMMARY IS READ HERE AND DROPPED FROM THE ROW. That is PRD §1.9's "the
// server parses; the agent asks": scoring is deterministic text work that
// belongs server-side, and the body it scored stays behind the session URI
// rather than riding in the payload. The session summaries were 17,200 B of the
// live 68,977 B payload — the second-largest field, and whole bodies.
func rankSessions(project string, sessions []storage.SessionMeta, terms []string, n int) ([]sessionSummary, int) {
	candidates := len(sessions)
	if candidates == 0 {
		return nil, 0
	}

	type scored struct {
		meta  storage.SessionMeta
		score int
		age   int // index in the source order; sessions arrive oldest-first
	}
	ranked := make([]scored, 0, candidates)
	for i, s := range sessions {
		ranked = append(ranked, scored{meta: s, score: overlapScore(s, terms), age: i})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].age > ranked[j].age
	})

	if n < len(ranked) {
		ranked = ranked[:n]
	}
	rows := make([]sessionSummary, 0, len(ranked))
	for _, r := range ranked {
		row := sessionSummary{
			Date:      r.meta.Date,
			Iteration: r.meta.Iteration,
			Title:     r.meta.Title,
			Tag:       r.meta.Tag,
		}
		if r.meta.ID != "" {
			row.URI = mcp.SessionURI(project, r.meta.ID)
		}
		rows = append(rows, row)
	}
	return rows, candidates
}

// overlapScore counts how many distinct query terms a session carries.
func overlapScore(s storage.SessionMeta, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	hay := strings.ToLower(s.Title + " " + s.Summary + " " + s.Tag)
	score := 0
	for _, term := range terms {
		if strings.Contains(hay, term) {
			score++
		}
	}
	return score
}

// headOfQueueTerms derives the query the session index is ranked against: the
// distinct significant words of the head-of-queue rows' slugs and titles.
//
// Short words are dropped because they carry no discrimination — "the", "and",
// "for" match every session ever written, so including them would score every
// candidate equally and reduce the ranker to the recency order it replaced,
// while still reporting itself as a ranker. That is the failure this whole epic
// is named after, so the floor is deliberate rather than incidental.
func headOfQueueTerms(rows []headOfQueueRow) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, row := range rows {
		for _, word := range splitWords(row.Slug + " " + row.Title) {
			if len(word) < minTermRunes || termStopWords[word] {
				continue
			}
			if !seen[word] {
				seen[word] = true
				terms = append(terms, word)
			}
		}
	}
	slices.Sort(terms)
	return terms
}

// minTermRunes is the shortest word that may act as a query term.
//
// It is 3, not 4, and the difference was measured rather than chosen: at 4 the
// query for a task titled "The commit log has a bug" kept only "commit" and
// dropped BOTH "log" and "bug" — the two words that name the subject. A floor
// high enough to exclude "the" is also high enough to exclude the vocabulary
// this project actually files work under (log, bug, kg, uri, api, mcp), so the
// short common words are excluded by NAME below instead.
const minTermRunes = 3

// termStopWords are words that pass minTermRunes but match nearly every session
// note in this corpus, so they discriminate nothing and would flatten every
// score into a tie — handing the ordering back to the recency tie-break while
// the payload still reported itself as ranked.
var termStopWords = map[string]bool{
	"a": true, "all": true, "and": true, "any": true, "are": true, "but": true,
	"can": true, "does": true, "for": true, "from": true, "had": true,
	"has": true, "have": true, "into": true, "its": true, "not": true,
	"one": true, "only": true, "our": true, "out": true, "own": true,
	"per": true, "that": true, "the": true, "them": true, "then": true,
	"they": true, "this": true, "two": true, "use": true, "via": true,
	"was": true, "were": true, "what": true, "when": true, "with": true,
}

// splitWords lowercases and splits on every non-alphanumeric rune, which folds
// a slug's hyphens and a title's punctuation into one vocabulary.
func splitWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
