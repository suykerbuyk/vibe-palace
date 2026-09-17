// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"log/slog"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// warnSkippedSessions reports notes the session reader could not parse, for the
// one tool whose wire contract has nowhere to put them.
//
// Every other reader in this package carries skips in its PAYLOAD
// (RankingReport.SkippedNotes, ProjectContext.SkippedNotes,
// EffectivenessResult.SkippedNotes) because a caller that cannot see the skip
// cannot tell a short answer from a short history. vp_search_sessions returns a
// bare array with no envelope, so it logs instead — a stated limitation, not a
// quiet omission.
func warnSkippedSessions(what, project string, skipped []storage.SessionSkip) {
	for _, s := range skipped {
		slog.Warn("session note unreadable; excluded from "+what,
			"project", project, "note", s.Path, "reason", s.Reason)
	}
}
