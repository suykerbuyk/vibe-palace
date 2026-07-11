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

// TuneOptions controls the tuning workflow.
type TuneOptions struct {
	Project    string
	MaxSamples int // default 50
	Keywords   map[string][]string
}

// TuneSample is a drawer selected for LLM evaluation.
// Content holds the FULL drawer content — truncation happens only in prompt construction.
type TuneSample struct {
	DrawerID    string `json:"drawer_id"`
	Wing        string `json:"wing"`
	CurrentRoom string `json:"current_room"`
	Content     string `json:"content"`
	SourceRef   string `json:"source_ref,omitempty"`
	Reason      string `json:"reason"` // "borderline", "mismatch", "general"
}

// LLMJudgment is the parsed LLM response for one sample.
type LLMJudgment struct {
	DrawerID   string  `json:"drawer_id"`
	Room       string  `json:"room"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

// WeightProposal is a single proposed weight change.
type WeightProposal struct {
	Room      string  `json:"room"`
	Keyword   string  `json:"keyword"`
	OldWeight float64 `json:"old_weight"`
	NewWeight float64 `json:"new_weight"`
	Rationale string  `json:"rationale"`
}

// TuneReport is the complete result of a tuning run.
type TuneReport struct {
	Project        string           `json:"project"`
	SamplesTotal   int              `json:"samples_total"`
	JudgmentsTotal int              `json:"judgments_total"`
	Agreements     int              `json:"agreements"`
	Disagreements  int              `json:"disagreements"`
	Proposals      []WeightProposal `json:"proposals"`
	UnmatchedFlags []UnmatchedFlag  `json:"unmatched_flags,omitempty"`
	TokenEstimate  TokenEstimate    `json:"token_estimate"`
}

// UnmatchedFlag flags content the LLM classified into a room but no existing
// keyword matched. Reserved for Task 12.4 keyword discovery.
type UnmatchedFlag struct {
	DrawerID    string `json:"drawer_id"`
	LLMRoom     string `json:"llm_room"`
	ContentSnip string `json:"content_snip"`
}

// TokenEstimate holds the cost estimation for --estimate mode.
type TokenEstimate struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	BatchCount       int `json:"batch_count"`
}

const (
	defaultMaxSamples   = 50
	defaultBatchSize    = 8
	promptContentMaxLen = 500
	proposalThreshold   = 3
)

// SelectSamples runs an internal audit and selects drawers for LLM evaluation.
// Priority: mismatches first, then borderlines, then general-room drawers.
// Deterministic: sorted by drawer ID, capped at maxSamples.
func SelectSamples(vault *storage.Vault, classifier *RoomClassifier, opts TuneOptions) ([]TuneSample, error) {
	maxSamples := opts.MaxSamples
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}

	auditOpts := AuditOptions{
		Project:  opts.Project,
		Keywords: opts.Keywords,
	}
	report, err := RunAudit(vault, classifier, auditOpts)
	if err != nil {
		return nil, fmt.Errorf("internal audit: %w", err)
	}

	// Collect all candidate drawers with reasons.
	type candidate struct {
		da     DrawerAudit
		reason string
	}
	var candidates []candidate

	// Priority 1: mismatches.
	for _, da := range report.Mismatches {
		candidates = append(candidates, candidate{da, "mismatch"})
	}
	// Priority 2: borderlines (not already in mismatches).
	mismatchIDs := make(map[string]bool, len(report.Mismatches))
	for _, da := range report.Mismatches {
		mismatchIDs[da.ID] = true
	}
	for _, da := range report.Borderlines {
		if !mismatchIDs[da.ID] {
			candidates = append(candidates, candidate{da, "borderline"})
		}
	}

	// Priority 3: general-room drawers from the full audit.
	// We need to re-walk to find general drawers not already captured.
	generalDrawers, err := collectGeneralDrawers(vault, classifier, opts)
	if err != nil {
		slog.Warn("tune: failed to collect general drawers", "err", err)
	} else {
		seen := make(map[string]bool, len(candidates))
		for _, c := range candidates {
			seen[c.da.ID] = true
		}
		for _, da := range generalDrawers {
			if !seen[da.ID] {
				candidates = append(candidates, candidate{da, "general"})
			}
		}
	}

	// Sort by drawer ID for determinism.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].da.ID < candidates[j].da.ID
	})

	// Cap and convert to TuneSamples.
	if len(candidates) > maxSamples {
		candidates = candidates[:maxSamples]
	}

	// Fetch full drawer content for each candidate.
	samples := make([]TuneSample, 0, len(candidates))
	for _, c := range candidates {
		drawers, err := vault.ListDrawers(opts.Project, c.da.Wing, c.da.CurrentRoom)
		if err != nil {
			slog.Warn("tune: list drawers failed", "wing", c.da.Wing, "room", c.da.CurrentRoom, "err", err)
			continue
		}
		for _, d := range drawers {
			if d.ID == c.da.ID {
				samples = append(samples, TuneSample{
					DrawerID:    d.ID,
					Wing:        c.da.Wing,
					CurrentRoom: c.da.CurrentRoom,
					Content:     d.Content,
					SourceRef:   d.SourceRef,
					Reason:      c.reason,
				})
				break
			}
		}
	}

	return samples, nil
}

// collectGeneralDrawers returns DrawerAudit entries for drawers currently in "general".
func collectGeneralDrawers(vault *storage.Vault, classifier *RoomClassifier, opts TuneOptions) ([]DrawerAudit, error) {
	wings, err := vault.ListWings(opts.Project)
	if err != nil {
		return nil, err
	}

	var results []DrawerAudit
	for _, wing := range wings {
		drawers, err := vault.ListDrawers(opts.Project, wing, "general")
		if err != nil {
			continue // general room may not exist
		}
		for _, d := range drawers {
			res := classifier.ClassifyWithScores(d.Content, d.SourceRef, opts.Keywords)
			results = append(results, DrawerAudit{
				ID:          d.ID,
				Wing:        wing,
				CurrentRoom: "general",
				BestRoom:    res.Room,
				BestScore:   res.Score,
				Scores:      res.Scores,
				Tier:        res.Tier,
			})
		}
	}
	return results, nil
}

// BatchSamples groups samples into deterministic batches.
// Samples are sorted by DrawerID before batching.
func BatchSamples(samples []TuneSample, batchSize int) [][]TuneSample {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	// Copy and sort for determinism.
	sorted := make([]TuneSample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].DrawerID < sorted[j].DrawerID
	})

	var batches [][]TuneSample
	for i := 0; i < len(sorted); i += batchSize {
		end := min(i+batchSize, len(sorted))
		batches = append(batches, sorted[i:end])
	}
	return batches
}

// BuildClassificationPrompt constructs the LLM messages for a batch.
// Content is truncated to 500 chars in the prompt only — TuneSample.Content is unchanged.
func BuildClassificationPrompt(batch []TuneSample, roomDefs []RoomDefinition) []llm.Message {
	// Build system message with room definitions.
	var sb strings.Builder
	sb.WriteString("You are a room classifier for a knowledge management system called \"vibe-palace\".\n")
	sb.WriteString("Each piece of content (called a \"drawer\") belongs to exactly one room based on its topic.\n\n")
	sb.WriteString("Available rooms and their defining keywords:\n")
	for _, rd := range roomDefs {
		// Show up to 8 representative keywords per room.
		kws := rd.Keywords
		if len(kws) > 8 {
			kws = kws[:8]
		}
		fmt.Fprintf(&sb, "- %s: %s\n", rd.Name, strings.Join(kws, ", "))
	}
	sb.WriteString("- general: content that doesn't clearly match any other room\n\n")
	sb.WriteString("For each drawer, respond with a JSON array. Each element must have:\n")
	sb.WriteString("- \"drawer_id\": the ID provided\n")
	sb.WriteString("- \"room\": one of the room names listed above, or \"general\"\n")
	sb.WriteString("- \"confidence\": a float from 0.0 to 1.0\n")
	sb.WriteString("- \"reasoning\": one sentence explaining your classification\n\n")
	sb.WriteString("Respond ONLY with the JSON array. No markdown, no explanation outside the array.\n")

	// Build user message with batch content.
	var ub strings.Builder
	ub.WriteString("Classify the following drawers:\n\n")
	for _, s := range batch {
		content := s.Content
		if len(content) > promptContentMaxLen {
			content = content[:promptContentMaxLen] + "..."
		}
		fmt.Fprintf(&ub, "---\ndrawer_id: %s\ncontent: %s\n---\n\n", s.DrawerID, content)
	}

	return []llm.Message{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: ub.String()},
	}
}

// ParseJudgments parses the LLM response JSON into judgments.
// Tolerates partial responses: valid judgments are returned, parse errors logged.
func ParseJudgments(response string, validRooms map[string]bool) ([]LLMJudgment, error) {
	// Strip markdown code fences if present.
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
		return nil, fmt.Errorf("parse judgments array: %w", err)
	}

	var judgments []LLMJudgment
	for _, r := range raw {
		var j LLMJudgment
		if err := json.Unmarshal(r, &j); err != nil {
			slog.Warn("tune: skipping malformed judgment", "err", err, "raw", string(r))
			continue
		}
		if j.DrawerID == "" || j.Room == "" {
			slog.Warn("tune: skipping judgment with missing fields", "raw", string(r))
			continue
		}
		// Validate room name.
		if !validRooms[j.Room] && j.Room != "general" {
			slog.Warn("tune: skipping unknown room", "room", j.Room, "drawer", j.DrawerID)
			continue
		}
		judgments = append(judgments, j)
	}
	return judgments, nil
}

// ComputeProposals applies heuristic rules to translate LLM judgments into
// weight change proposals.
//
// Design rationale: We use heuristic rules rather than optimization because:
// (a) weight space is discrete (0.3, 0.6, 1.0),
// (b) rules are auditable and predictable,
// (c) prevents overfitting to LLM biases,
// (d) users can review and reject individual proposals.
func ComputeProposals(judgments []LLMJudgment, samples []TuneSample, classifier *RoomClassifier) ([]WeightProposal, []UnmatchedFlag) {
	// Build lookups.
	sampleMap := make(map[string]TuneSample, len(samples))
	for _, s := range samples {
		sampleMap[s.DrawerID] = s
	}

	// Track promotion candidates: (room, keyword) -> count of general drawers promoted.
	type kwKey struct{ room, keyword string }
	promoCounts := make(map[kwKey]int)
	promoWeights := make(map[kwKey]float64)

	// Track demotion candidates: (room, keyword) -> count of disagreements.
	demoteCounts := make(map[kwKey]int)
	demoteWeights := make(map[kwKey]float64)

	var flags []UnmatchedFlag

	for _, j := range judgments {
		s, ok := sampleMap[j.DrawerID]
		if !ok {
			continue
		}

		// Agreement — skip.
		if j.Room == s.CurrentRoom {
			continue
		}

		// Rule 1: General drawer promoted to a specific room.
		if s.CurrentRoom == "general" && j.Room != "general" {
			matches := classifier.MatchingKeywords(s.Content, j.Room)
			if len(matches) == 0 {
				// Rule 3: No keywords fire — flag for discovery.
				snip := s.Content
				if len(snip) > 100 {
					snip = snip[:100]
				}
				flags = append(flags, UnmatchedFlag{
					DrawerID:    j.DrawerID,
					LLMRoom:     j.Room,
					ContentSnip: snip,
				})
			} else {
				for _, m := range matches {
					key := kwKey{j.Room, m.Raw}
					promoCounts[key]++
					promoWeights[key] = m.Weight
				}
			}
		}

		// Rule 2: LLM disagrees with non-general classification.
		if s.CurrentRoom != "general" && j.Room != s.CurrentRoom {
			matches := classifier.MatchingKeywords(s.Content, s.CurrentRoom)
			for _, m := range matches {
				key := kwKey{s.CurrentRoom, m.Raw}
				demoteCounts[key]++
				demoteWeights[key] = m.Weight
			}
		}
	}

	var proposals []WeightProposal

	// Generate promotion proposals.
	for key, count := range promoCounts {
		if count < proposalThreshold {
			continue
		}
		oldWeight := promoWeights[key]
		newWeight := promoteWeight(oldWeight)
		if newWeight == oldWeight {
			continue
		}
		proposals = append(proposals, WeightProposal{
			Room:      key.room,
			Keyword:   key.keyword,
			OldWeight: oldWeight,
			NewWeight: newWeight,
			Rationale: fmt.Sprintf("LLM classified %d general drawers with this keyword as %s", count, key.room),
		})
	}

	// Generate demotion proposals.
	for key, count := range demoteCounts {
		if count < proposalThreshold {
			continue
		}
		oldWeight := demoteWeights[key]
		newWeight := demoteWeight(oldWeight)
		if newWeight == oldWeight {
			proposals = append(proposals, WeightProposal{
				Room:      key.room,
				Keyword:   key.keyword,
				OldWeight: oldWeight,
				NewWeight: oldWeight,
				Rationale: fmt.Sprintf("LLM disagreed %d times; already at minimum weight — review recommended", count),
			})
			continue
		}
		proposals = append(proposals, WeightProposal{
			Room:      key.room,
			Keyword:   key.keyword,
			OldWeight: oldWeight,
			NewWeight: newWeight,
			Rationale: fmt.Sprintf("LLM disagreed with %s classification %d times", key.room, count),
		})
	}

	// Sort for deterministic output.
	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].Room != proposals[j].Room {
			return proposals[i].Room < proposals[j].Room
		}
		return proposals[i].Keyword < proposals[j].Keyword
	})

	return proposals, flags
}

// promoteWeight bumps a keyword weight up one tier.
func promoteWeight(w float64) float64 {
	switch {
	case w < 0.5:
		return 0.6
	case w < 0.9:
		return 1.0
	default:
		return w // already max
	}
}

// demoteWeight drops a keyword weight down one tier.
func demoteWeight(w float64) float64 {
	switch {
	case w > 0.9:
		return 0.6
	case w > 0.4:
		return 0.3
	default:
		return w // already min
	}
}

// FormatTOMLDiff generates a pasteable TOML fragment from proposals.
func FormatTOMLDiff(proposals []WeightProposal) string {
	if len(proposals) == 0 {
		return ""
	}

	// Group by room, skipping review-only proposals (old == new weight).
	type roomBuckets struct {
		high, medium, low []string
		comments          []string
	}
	rooms := make(map[string]*roomBuckets)
	var roomOrder []string

	for _, p := range proposals {
		if p.OldWeight == p.NewWeight {
			continue // review-only, no config change
		}
		b, ok := rooms[p.Room]
		if !ok {
			b = &roomBuckets{}
			rooms[p.Room] = b
			roomOrder = append(roomOrder, p.Room)
		}
		switch {
		case p.NewWeight >= 0.9:
			b.high = append(b.high, p.Keyword)
		case p.NewWeight >= 0.5:
			b.medium = append(b.medium, p.Keyword)
		default:
			b.low = append(b.low, p.Keyword)
		}
		b.comments = append(b.comments, fmt.Sprintf("# %q: %.1f -> %.1f (%s)", p.Keyword, p.OldWeight, p.NewWeight, p.Rationale))
	}

	sort.Strings(roomOrder)
	if len(roomOrder) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Proposed weight adjustments (generated by vp tune rooms)\n")
	sb.WriteString("# Review carefully before applying.\n\n")

	for _, room := range roomOrder {
		b := rooms[room]
		for _, c := range b.comments {
			sb.WriteString(c)
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "[palace.scoring.rooms.%s]\n", room)
		if len(b.high) > 0 {
			fmt.Fprintf(&sb, "high = %s\n", formatStringSlice(b.high))
		}
		if len(b.medium) > 0 {
			fmt.Fprintf(&sb, "medium = %s\n", formatStringSlice(b.medium))
		}
		if len(b.low) > 0 {
			fmt.Fprintf(&sb, "low = %s\n", formatStringSlice(b.low))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatStringSlice formats a string slice as a TOML array literal.
func formatStringSlice(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// EstimateTokenCost estimates prompt/completion tokens without calling the LLM.
func EstimateTokenCost(samples []TuneSample, batchSize int) TokenEstimate {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	batches := BatchSamples(samples, batchSize)
	var totalPrompt int
	for _, batch := range batches {
		msgs := BuildClassificationPrompt(batch, nil) // nil roomDefs — estimate only
		for _, m := range msgs {
			totalPrompt += llm.EstimateTokens(m.Content)
		}
	}

	// Estimate ~50 completion tokens per sample (room + confidence + reasoning).
	estCompletion := len(samples) * 50

	return TokenEstimate{
		PromptTokens:     totalPrompt,
		CompletionTokens: estCompletion,
		TotalTokens:      totalPrompt + estCompletion,
		BatchCount:       len(batches),
	}
}

// RunTune orchestrates the full tuning workflow.
// progress is called with status strings during execution (for CLI display).
func RunTune(
	ctx context.Context,
	vault *storage.Vault,
	classifier *RoomClassifier,
	client llm.Caller,
	opts TuneOptions,
	progress func(string),
) (*TuneReport, error) {
	if progress == nil {
		progress = func(string) {}
	}

	progress("Selecting samples...")
	samples, err := SelectSamples(vault, classifier, opts)
	if err != nil {
		return nil, fmt.Errorf("select samples: %w", err)
	}

	report := &TuneReport{
		Project:      opts.Project,
		SamplesTotal: len(samples),
	}
	if len(samples) == 0 {
		return report, nil
	}

	roomDefs := classifier.RoomDefinitions()
	validRooms := make(map[string]bool, len(roomDefs))
	for _, rd := range roomDefs {
		validRooms[rd.Name] = true
	}

	batches := BatchSamples(samples, defaultBatchSize)
	report.TokenEstimate = EstimateTokenCost(samples, defaultBatchSize)

	var allJudgments []LLMJudgment
	for i, batch := range batches {
		if err := ctx.Err(); err != nil {
			return report, err
		}

		progress(fmt.Sprintf("Processing batch %d/%d (%d samples)...", i+1, len(batches), len(batch)))

		msgs := BuildClassificationPrompt(batch, roomDefs)
		resp, err := client.ChatCompletion(ctx, msgs)
		if err != nil {
			slog.Error("tune: LLM call failed", "batch", i+1, "err", err)
			continue
		}

		judgments, err := ParseJudgments(resp.Choices[0].Message.Content, validRooms)
		if err != nil {
			slog.Error("tune: parse judgments failed", "batch", i+1, "err", err)
			continue
		}
		allJudgments = append(allJudgments, judgments...)
	}

	report.JudgmentsTotal = len(allJudgments)

	// Count agreements/disagreements.
	sampleMap := make(map[string]TuneSample, len(samples))
	for _, s := range samples {
		sampleMap[s.DrawerID] = s
	}
	for _, j := range allJudgments {
		if s, ok := sampleMap[j.DrawerID]; ok {
			if j.Room == s.CurrentRoom {
				report.Agreements++
			} else {
				report.Disagreements++
			}
		}
	}

	progress("Computing proposals...")
	report.Proposals, report.UnmatchedFlags = ComputeProposals(allJudgments, samples, classifier)

	return report, nil
}
