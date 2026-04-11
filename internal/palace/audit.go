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
