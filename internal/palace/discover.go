// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DiscoverOptions controls the keyword discovery workflow.
type DiscoverOptions struct {
	Project    string
	MaxSamples int // default 50
	Keywords   map[string][]string
}

// DiscoverySample is a drawer selected for LLM keyword extraction.
type DiscoverySample struct {
	DrawerID    string `json:"drawer_id"`
	Wing        string `json:"wing"`
	CurrentRoom string `json:"current_room"`
	Content     string `json:"content"`
	SourceRef   string `json:"source_ref,omitempty"`
}

// LLMKeywordSuggestion is one keyword suggested by the LLM for a drawer.
type LLMKeywordSuggestion struct {
	Keyword string `json:"keyword"`
	Weight  string `json:"weight"` // "high", "medium", "low"
}

// LLMDiscoveryResponse is the parsed LLM response for one drawer.
type LLMDiscoveryResponse struct {
	DrawerID string                 `json:"drawer_id"`
	Room     string                 `json:"room"`
	Keywords []LLMKeywordSuggestion `json:"keywords"`
}

// KeywordProposal is a proposed new keyword with cross-validation scores.
type KeywordProposal struct {
	Room        string `json:"room"`
	Keyword     string `json:"keyword"`
	Weight      string `json:"weight"` // "high", "medium", "low"
	Captures    int    `json:"captures"`
	Regressions int    `json:"regressions"`
	Score       int    `json:"score"` // captures - (regressions * 3)
}

// DiscoverReport is the complete result of a discovery run.
type DiscoverReport struct {
	Project         string            `json:"project"`
	CandidatesTotal int               `json:"candidates_total"`
	LLMCalls        int               `json:"llm_calls"`
	Proposals       []KeywordProposal `json:"proposals"`
	Rejected        []KeywordProposal `json:"rejected,omitempty"`
	TokenEstimate   TokenEstimate     `json:"token_estimate,omitempty"`
}

const (
	discoverDefaultMaxSamples = 50
	discoverBatchSize         = 8
	discoverContentMaxLen     = 500
	minKeywordLen             = 2
	maxKeywordLen             = 60
	regressionPenalty         = 3
)

// drawerWithRoom pairs a Drawer with its room for cross-validation.
type drawerWithRoom struct {
	Drawer storage.Drawer
	Wing   string
	Room   string
}

// CollectDiscoveryCandidates walks all drawers in one pass, collecting
// "general" fallback drawers as primary candidates and audit mismatches
// as secondary. Returns candidates sorted by content length desc, then
// drawer ID for determinism, capped at maxSamples. Also returns all
// drawers with room context for cross-validation.
func CollectDiscoveryCandidates(
	vault *storage.Vault,
	classifier *RoomClassifier,
	opts DiscoverOptions,
) ([]DiscoverySample, []drawerWithRoom, error) {
	maxSamples := opts.MaxSamples
	if maxSamples <= 0 {
		maxSamples = discoverDefaultMaxSamples
	}

	wings, err := vault.ListWings(opts.Project)
	if err != nil {
		return nil, nil, fmt.Errorf("list wings: %w", err)
	}

	var candidates []DiscoverySample
	var allDrawers []drawerWithRoom
	seen := make(map[string]bool)

	for _, wing := range wings {
		rooms, err := vault.ListRooms(opts.Project, wing)
		if err != nil {
			slog.Warn("discover: list rooms failed", "wing", wing, "err", err)
			continue
		}

		for _, room := range rooms {
			drawers, err := vault.ListDrawers(opts.Project, wing, room)
			if err != nil {
				slog.Warn("discover: list drawers failed",
					"wing", wing, "room", room, "err", err)
				continue
			}

			for _, d := range drawers {
				allDrawers = append(allDrawers, drawerWithRoom{
					Drawer: d, Wing: wing, Room: room,
				})

				// Primary: general drawers.
				if room == "general" && !seen[d.ID] {
					candidates = append(candidates, DiscoverySample{
						DrawerID:    d.ID,
						Wing:        wing,
						CurrentRoom: room,
						Content:     d.Content,
						SourceRef:   d.SourceRef,
					})
					seen[d.ID] = true
					continue
				}

				// Secondary: mismatches (re-score and check).
				if room != "general" && !seen[d.ID] {
					res := classifier.ClassifyWithScores(
						d.Content, d.SourceRef, opts.Keywords)
					if res.Room != room {
						candidates = append(candidates, DiscoverySample{
							DrawerID:    d.ID,
							Wing:        wing,
							CurrentRoom: room,
							Content:     d.Content,
							SourceRef:   d.SourceRef,
						})
						seen[d.ID] = true
					}
				}
			}
		}
	}

	// Sort: longer content first (more signal), then by ID for determinism.
	sort.Slice(candidates, func(i, j int) bool {
		li, lj := len(candidates[i].Content), len(candidates[j].Content)
		if li != lj {
			return li > lj
		}
		return candidates[i].DrawerID < candidates[j].DrawerID
	})

	if len(candidates) > maxSamples {
		candidates = candidates[:maxSamples]
	}

	return candidates, allDrawers, nil
}

// BatchDiscoverySamples groups samples into deterministic batches.
func BatchDiscoverySamples(samples []DiscoverySample, batchSize int) [][]DiscoverySample {
	if batchSize <= 0 {
		batchSize = discoverBatchSize
	}

	sorted := make([]DiscoverySample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].DrawerID < sorted[j].DrawerID
	})

	var batches [][]DiscoverySample
	for i := 0; i < len(sorted); i += batchSize {
		end := i + batchSize
		if end > len(sorted) {
			end = len(sorted)
		}
		batches = append(batches, sorted[i:end])
	}
	return batches
}

// BuildDiscoveryPrompt constructs LLM messages for a keyword extraction batch.
func BuildDiscoveryPrompt(batch []DiscoverySample, roomDefs []RoomDefinition) []llm.Message {
	var sb strings.Builder
	sb.WriteString("You are a keyword analyst for a knowledge management system called \"vibe-palace\".\n")
	sb.WriteString("Content is classified into rooms based on keyword matching. ")
	sb.WriteString("Some content is stuck in \"general\" because no keywords match.\n\n")
	sb.WriteString("Your task: suggest 1-3 new keywords per piece of content that would help classify it into the correct room.\n\n")
	sb.WriteString("Available rooms and their current keywords:\n")
	for _, rd := range roomDefs {
		kws := rd.Keywords
		if len(kws) > 8 {
			kws = kws[:8]
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", rd.Name, strings.Join(kws, ", ")))
	}
	sb.WriteString("\nKeyword weight tiers:\n")
	sb.WriteString("- \"high\": domain-specific terms unlikely to appear outside this room (weight 1.0)\n")
	sb.WriteString("- \"medium\": moderately specific terms (weight 0.6)\n")
	sb.WriteString("- \"low\": common terms that need other keywords to classify (weight 0.3)\n\n")
	sb.WriteString("For each drawer, respond with a JSON array. Each element must have:\n")
	sb.WriteString("- \"drawer_id\": the ID provided\n")
	sb.WriteString("- \"room\": one of the room names listed above\n")
	sb.WriteString("- \"keywords\": array of {\"keyword\": \"...\", \"weight\": \"high\"|\"medium\"|\"low\"}\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Only suggest keywords that actually appear in the content\n")
	sb.WriteString("- Do not suggest keywords already listed above for that room\n")
	sb.WriteString("- Prefer multi-word phrases for high-weight keywords\n")
	sb.WriteString("- Respond ONLY with the JSON array. No markdown, no explanation.\n")

	var ub strings.Builder
	ub.WriteString("Suggest keywords for the following unclassified content:\n\n")
	for _, s := range batch {
		content := s.Content
		if len(content) > discoverContentMaxLen {
			content = content[:discoverContentMaxLen] + "..."
		}
		ub.WriteString(fmt.Sprintf("---\ndrawer_id: %s\ncontent: %s\n---\n\n", s.DrawerID, content))
	}

	return []llm.Message{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: ub.String()},
	}
}

// ParseDiscoveryResponse parses the LLM response JSON into discovery responses.
// Tolerates partial responses: valid entries are returned, parse errors logged.
func ParseDiscoveryResponse(response string, validRooms map[string]bool) ([]LLMDiscoveryResponse, error) {
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```") {
		lines := strings.SplitN(response, "\n", 2)
		if len(lines) > 1 {
			response = lines[1]
		}
		if idx := strings.LastIndex(response, "```"); idx > 0 {
			response = response[:idx]
		}
		response = strings.TrimSpace(response)
	}

	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse discovery response array: %w", err)
	}

	var results []LLMDiscoveryResponse
	for _, r := range raw {
		var resp LLMDiscoveryResponse
		if err := json.Unmarshal(r, &resp); err != nil {
			slog.Warn("discover: skipping malformed response", "err", err, "raw", string(r))
			continue
		}
		if resp.DrawerID == "" || resp.Room == "" {
			slog.Warn("discover: skipping response with missing fields", "raw", string(r))
			continue
		}
		if !validRooms[resp.Room] && resp.Room != "general" {
			slog.Warn("discover: skipping unknown room", "room", resp.Room, "drawer", resp.DrawerID)
			continue
		}

		// Validate keywords.
		var valid []LLMKeywordSuggestion
		for _, kw := range resp.Keywords {
			cleaned := strings.TrimSpace(strings.ToLower(kw.Keyword))
			if len(cleaned) < minKeywordLen {
				slog.Warn("discover: skipping short keyword",
					"keyword", kw.Keyword, "drawer", resp.DrawerID)
				continue
			}
			if len(cleaned) > maxKeywordLen {
				slog.Warn("discover: skipping long keyword",
					"keyword", kw.Keyword, "drawer", resp.DrawerID)
				continue
			}
			weight := normalizeWeight(kw.Weight)
			valid = append(valid, LLMKeywordSuggestion{
				Keyword: cleaned,
				Weight:  weight,
			})
		}
		resp.Keywords = valid

		if len(resp.Keywords) > 0 {
			results = append(results, resp)
		}
	}
	return results, nil
}

// normalizeWeight maps LLM weight strings to canonical values.
func normalizeWeight(w string) string {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

// weightToFloat converts a weight tier string to its numeric value.
func weightToFloat(w string) float64 {
	switch w {
	case "high":
		return 1.0
	case "low":
		return 0.3
	default:
		return 0.6
	}
}

// CrossValidateProposals scores proposed keywords against all drawers.
// For each keyword, counts captures (general drawers that match) and
// regressions (correctly-classified drawers that match for a different room).
// Uses buildKeyword().matches() for word-boundary-aware matching.
//
// Complexity: O(proposals × drawers × content_length).
// Each drawer's content is lowercased once per validation pass.
func CrossValidateProposals(
	proposals []rawProposal,
	allDrawers []drawerWithRoom,
	classifier *RoomClassifier,
) (accepted []KeywordProposal, rejected []KeywordProposal) {
	// Pre-lowercase all drawer content once.
	type cachedDrawer struct {
		Lower string
		Wing  string
		Room  string
	}
	cached := make([]cachedDrawer, len(allDrawers))
	for i, dwr := range allDrawers {
		cached[i] = cachedDrawer{
			Lower: strings.ToLower(dwr.Drawer.Content),
			Wing:  dwr.Wing,
			Room:  dwr.Room,
		}
	}

	for _, p := range proposals {
		kw := buildKeyword(strings.ToLower(p.keyword))
		captures, regressions := 0, 0

		for _, cd := range cached {
			if !kw.matches(cd.Lower) {
				continue
			}
			if cd.Room == "general" {
				captures++
			} else if cd.Room != p.room {
				regressions++
			}
			// If cd.Room == p.room, the drawer is already correctly
			// classified — the keyword is redundant but not harmful.
		}

		score := captures - (regressions * regressionPenalty)
		kp := KeywordProposal{
			Room:        p.room,
			Keyword:     p.keyword,
			Weight:      p.weight,
			Captures:    captures,
			Regressions: regressions,
			Score:       score,
		}

		if score > 0 {
			accepted = append(accepted, kp)
		} else {
			rejected = append(rejected, kp)
		}
	}

	// Sort accepted by score descending, then room+keyword for determinism.
	sort.Slice(accepted, func(i, j int) bool {
		if accepted[i].Score != accepted[j].Score {
			return accepted[i].Score > accepted[j].Score
		}
		if accepted[i].Room != accepted[j].Room {
			return accepted[i].Room < accepted[j].Room
		}
		return accepted[i].Keyword < accepted[j].Keyword
	})

	sort.Slice(rejected, func(i, j int) bool {
		if rejected[i].Room != rejected[j].Room {
			return rejected[i].Room < rejected[j].Room
		}
		return rejected[i].Keyword < rejected[j].Keyword
	})

	return accepted, rejected
}

// rawProposal is an intermediate type for pre-merge proposals.
type rawProposal struct {
	room    string
	keyword string
	weight  string
}

// MergeProposals deduplicates keyword proposals across batches and
// excludes keywords already in the classifier's scoring table.
func MergeProposals(
	responses []LLMDiscoveryResponse,
	classifier *RoomClassifier,
) []rawProposal {
	type key struct{ room, keyword string }
	seen := make(map[key]string) // key → best weight

	for _, resp := range responses {
		for _, kw := range resp.Keywords {
			k := key{resp.Room, kw.Keyword}
			existing, ok := seen[k]
			if !ok {
				seen[k] = kw.Weight
			} else if weightToFloat(kw.Weight) > weightToFloat(existing) {
				seen[k] = kw.Weight
			}
		}
	}

	// Exclude keywords already in the scoring table.
	var proposals []rawProposal
	for k, weight := range seen {
		if classifierHasKeyword(classifier, k.room, k.keyword) {
			continue
		}
		proposals = append(proposals, rawProposal{
			room:    k.room,
			keyword: k.keyword,
			weight:  weight,
		})
	}

	// Sort for determinism.
	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].room != proposals[j].room {
			return proposals[i].room < proposals[j].room
		}
		return proposals[i].keyword < proposals[j].keyword
	})

	return proposals
}

// classifierHasKeyword checks if a keyword already exists in the classifier
// for a given room.
func classifierHasKeyword(rc *RoomClassifier, room, kw string) bool {
	entries := rc.entries
	if len(entries) == 0 {
		entries = defaultRoomKeywords
	}
	lower := strings.ToLower(kw)
	for _, entry := range entries {
		if entry.room != room {
			continue
		}
		for _, existing := range entry.keywords {
			if existing.raw == lower {
				return true
			}
		}
		return false
	}
	return false
}

// FormatDiscoveryTOML generates a pasteable TOML fragment from proposals.
func FormatDiscoveryTOML(proposals []KeywordProposal) string {
	if len(proposals) == 0 {
		return ""
	}

	type roomBuckets struct {
		high, medium, low []string
		comments          []string
	}
	rooms := make(map[string]*roomBuckets)
	var roomOrder []string

	for _, p := range proposals {
		b, ok := rooms[p.Room]
		if !ok {
			b = &roomBuckets{}
			rooms[p.Room] = b
			roomOrder = append(roomOrder, p.Room)
		}
		b.comments = append(b.comments,
			fmt.Sprintf("# %q (%s): +%d captures, %d regressions, score %d",
				p.Keyword, p.Weight, p.Captures, p.Regressions, p.Score))
		switch p.Weight {
		case "high":
			b.high = append(b.high, p.Keyword)
		case "low":
			b.low = append(b.low, p.Keyword)
		default:
			b.medium = append(b.medium, p.Keyword)
		}
	}

	sort.Strings(roomOrder)

	var sb strings.Builder
	sb.WriteString("# Proposed new keywords (generated by vp discover rooms)\n")
	sb.WriteString("# Review carefully before applying.\n\n")

	for _, room := range roomOrder {
		b := rooms[room]
		for _, c := range b.comments {
			sb.WriteString(c)
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("[palace.scoring.rooms.%s]\n", room))
		if len(b.high) > 0 {
			sb.WriteString(fmt.Sprintf("high = %s\n", formatStringSlice(b.high)))
		}
		if len(b.medium) > 0 {
			sb.WriteString(fmt.Sprintf("medium = %s\n", formatStringSlice(b.medium)))
		}
		if len(b.low) > 0 {
			sb.WriteString(fmt.Sprintf("low = %s\n", formatStringSlice(b.low)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// EstimateDiscoveryCost estimates prompt/completion tokens without calling the LLM.
func EstimateDiscoveryCost(samples []DiscoverySample, batchSize int) TokenEstimate {
	if batchSize <= 0 {
		batchSize = discoverBatchSize
	}

	batches := BatchDiscoverySamples(samples, batchSize)
	var totalPrompt int
	for _, batch := range batches {
		msgs := BuildDiscoveryPrompt(batch, nil)
		for _, m := range msgs {
			totalPrompt += llm.EstimateTokens(m.Content)
		}
	}

	// Estimate ~80 completion tokens per sample (room + 1-3 keywords with weights).
	estCompletion := len(samples) * 80

	return TokenEstimate{
		PromptTokens:     totalPrompt,
		CompletionTokens: estCompletion,
		TotalTokens:      totalPrompt + estCompletion,
		BatchCount:       len(batches),
	}
}

// RunDiscover orchestrates the full keyword discovery workflow.
func RunDiscover(
	ctx context.Context,
	vault *storage.Vault,
	classifier *RoomClassifier,
	client llm.Caller,
	opts DiscoverOptions,
	progress func(string),
) (*DiscoverReport, error) {
	if progress == nil {
		progress = func(string) {}
	}

	progress("Collecting candidates...")
	candidates, allDrawers, err := CollectDiscoveryCandidates(vault, classifier, opts)
	if err != nil {
		return nil, fmt.Errorf("collect candidates: %w", err)
	}

	report := &DiscoverReport{
		Project:         opts.Project,
		CandidatesTotal: len(candidates),
	}
	if len(candidates) == 0 {
		return report, nil
	}

	roomDefs := classifier.RoomDefinitions()
	validRooms := make(map[string]bool, len(roomDefs))
	for _, rd := range roomDefs {
		validRooms[rd.Name] = true
	}

	batches := BatchDiscoverySamples(candidates, discoverBatchSize)
	report.TokenEstimate = EstimateDiscoveryCost(candidates, discoverBatchSize)

	var allResponses []LLMDiscoveryResponse
	for i, batch := range batches {
		if err := ctx.Err(); err != nil {
			return report, err
		}

		progress(fmt.Sprintf("Processing batch %d/%d (%d samples)...", i+1, len(batches), len(batch)))

		msgs := BuildDiscoveryPrompt(batch, roomDefs)
		resp, err := client.ChatCompletion(ctx, msgs)
		if err != nil {
			slog.Error("discover: LLM call failed", "batch", i+1, "err", err)
			continue
		}
		report.LLMCalls++

		parsed, err := ParseDiscoveryResponse(resp.Choices[0].Message.Content, validRooms)
		if err != nil {
			slog.Error("discover: parse response failed", "batch", i+1, "err", err)
			continue
		}
		allResponses = append(allResponses, parsed...)
	}

	progress("Merging proposals...")
	merged := MergeProposals(allResponses, classifier)

	progress("Cross-validating proposals...")
	report.Proposals, report.Rejected = CrossValidateProposals(merged, allDrawers, classifier)

	return report, nil
}
