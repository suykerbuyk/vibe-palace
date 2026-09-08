// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// AuditOptions controls audit behavior.
type AuditOptions struct {
	Project  string
	Keywords map[string][]string // tier-1 custom keywords from config
}

// DrawerAudit holds the audit result for a single drawer.
type DrawerAudit struct {
	ID           string             `json:"id"`
	Wing         string             `json:"wing"`
	CurrentRoom  string             `json:"current_room"`
	BestRoom     string             `json:"best_room"`
	BestScore    float64            `json:"best_score"`
	CurrentScore float64            `json:"current_score"`
	Scores       map[string]float64 `json:"scores"`
	Borderline   bool               `json:"borderline"`
	Mismatch     bool               `json:"mismatch"`
	Tier         string             `json:"tier"`
}

// RoomDistribution holds per-room drawer counts.
type RoomDistribution struct {
	Room    string  `json:"room"`
	Count   int     `json:"count"`
	Percent float64 `json:"percent"`
}

// AuditReport is the complete audit result.
type AuditReport struct {
	Project        string              `json:"project"`
	TotalDrawers   int                 `json:"total_drawers"`
	Distributions  []RoomDistribution  `json:"distributions"`
	GeneralCount   int                 `json:"general_count"`
	GeneralPercent float64             `json:"general_percent"`
	Mismatches     []DrawerAudit       `json:"mismatches"`
	Borderlines    []DrawerAudit       `json:"borderlines"`
	Coverage       []RoomKeywordReport `json:"coverage"`
	MinScore       float64             `json:"min_score"`
}

// MoveCandidate holds a drawer that should be reclassified.
type MoveCandidate struct {
	DrawerID string  `json:"drawer_id"`
	Wing     string  `json:"wing"`
	FromRoom string  `json:"from_room"`
	ToRoom   string  `json:"to_room"`
	OldScore float64 `json:"old_score"`
	NewScore float64 `json:"new_score"`
}

// isDecisionDrawer reports whether a drawer holds one recorded decision rather
// than a slice of transcript, and therefore whether the room classifier has any
// business having an opinion about where it lives.
//
// A decision drawer's room is ASSERTED by its writer, not inferred: capture
// files every decision into the fixed "decisions" room, and that fixed room is
// what the palace query prunes to by default and what the append path scopes
// its dedup against. Nothing about the drawer's TEXT chose that room, so there
// is no classification the scorer could get right about it — a decision about
// the release pipeline reads like devops, but it is still a decision, and
// moving it into devops would be wrong.
//
// Worse than merely useless, the scorer cannot even NAME the room. "decisions"
// appears in neither defaultRoomKeywords nor filenameRules (it exists in this
// package only as HallDecisions, a hall, which is a different axis), so
// ClassifyWithScores can never return it. Every decision drawer would score as
// a permanent 100% mismatch, in every run, forever — noise that never clears
// because there is no keyword anyone could add to clear it. And an
// `vp audit rooms --apply` acting on that mismatch would call vault.MoveDrawer
// on each of them and empty the very room the query prunes to, breaking both
// the default query and the dedup scope in one pass.
//
// So decision drawers are excluded from re-scoring wherever it happens. Consult
// this predicate at every site that re-scores a drawer.
func isDecisionDrawer(d storage.Drawer) bool {
	return d.SourceType == storage.SourceTypeDecision
}

// RunAudit walks all wings/rooms/drawers and builds an AuditReport.
// Per-drawer errors are logged and skipped (non-fatal).
func RunAudit(vault *storage.Vault, classifier *RoomClassifier, opts AuditOptions) (*AuditReport, error) {
	wings, err := vault.ListWings(opts.Project)
	if err != nil {
		return nil, fmt.Errorf("list wings: %w", err)
	}

	report := &AuditReport{
		Project:  opts.Project,
		MinScore: classifier.minScore,
	}
	if report.MinScore <= 0 {
		report.MinScore = minRoomScore
	}

	roomCounts := make(map[string]int)
	var allContent []string

	for _, wing := range wings {
		rooms, err := vault.ListRooms(opts.Project, wing)
		if err != nil {
			slog.Warn("audit: list rooms failed", "wing", wing, "err", err)
			continue
		}

		for _, room := range rooms {
			drawers, err := vault.ListDrawers(opts.Project, wing, room)
			if err != nil {
				slog.Warn("audit: list drawers failed", "wing", wing, "room", room, "err", err)
				continue
			}

			for _, d := range drawers {
				// This skip belongs HERE, at the top of the loop, and not
				// further down next to the classify call. A decision drawer
				// must be invisible to the audit's ARITHMETIC, not merely
				// exempt from its moves:
				//
				//   - report.TotalDrawers is the DIVISOR for every room
				//     distribution percent below and for GeneralPercent. Count
				//     decision drawers into it and every reported percentage
				//     drifts downward as the decision corpus grows, silently,
				//     while the numerators stay honest.
				//   - roomCounts[room] would gain a "decisions" row that no
				//     classifier proposal can ever act on — a permanent line in
				//     the distribution report that nothing can change.
				//   - allContent feeds KeywordCoverage at the end of this
				//     function, which reports which keywords fire across the
				//     corpus. Decision prose would be scored as if it were
				//     classifier training material and skew that answer.
				//
				// Moving this below TotalDrawers++ still suppresses the move
				// proposal, so the mismatch test keeps passing while all three
				// of the above quietly break. Leave it at the top.
				if isDecisionDrawer(d) {
					continue
				}

				report.TotalDrawers++
				roomCounts[room]++
				allContent = append(allContent, d.Content)

				res := classifier.ClassifyWithScores(d.Content, d.SourceRef, opts.Keywords)
				da := DrawerAudit{
					ID:           d.ID,
					Wing:         wing,
					CurrentRoom:  room,
					BestRoom:     res.Room,
					BestScore:    res.Score,
					CurrentScore: res.Scores[room],
					Scores:       res.Scores,
					Borderline:   res.Borderline,
					Tier:         res.Tier,
				}

				if res.Room != room {
					da.Mismatch = true
					report.Mismatches = append(report.Mismatches, da)
				}
				if res.Borderline {
					report.Borderlines = append(report.Borderlines, da)
				}
			}
		}
	}

	// Room distribution.
	for room, count := range roomCounts {
		pct := 0.0
		if report.TotalDrawers > 0 {
			pct = float64(count) / float64(report.TotalDrawers) * 100
		}
		report.Distributions = append(report.Distributions, RoomDistribution{
			Room:    room,
			Count:   count,
			Percent: pct,
		})
	}
	sort.Slice(report.Distributions, func(i, j int) bool {
		return report.Distributions[i].Count > report.Distributions[j].Count
	})

	// General fallback rate.
	report.GeneralCount = roomCounts["general"]
	if report.TotalDrawers > 0 {
		report.GeneralPercent = float64(report.GeneralCount) / float64(report.TotalDrawers) * 100
	}

	// Keyword coverage across all content.
	joined := strings.Join(allContent, "\n\n")
	report.Coverage = classifier.KeywordCoverage(joined)

	return report, nil
}

// Candidates returns MoveCandidate entries from mismatched DrawerAudits.
func (r *AuditReport) Candidates() []MoveCandidate {
	candidates := make([]MoveCandidate, 0, len(r.Mismatches))
	for _, da := range r.Mismatches {
		candidates = append(candidates, MoveCandidate{
			DrawerID: da.ID,
			Wing:     da.Wing,
			FromRoom: da.CurrentRoom,
			ToRoom:   da.BestRoom,
			OldScore: da.CurrentScore,
			NewScore: da.BestScore,
		})
	}
	return candidates
}
