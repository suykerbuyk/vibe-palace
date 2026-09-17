// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"log/slog"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// warnSkippedSessions reports notes the session reader could not parse.
//
// storage.ListSessions returns skips rather than failing the whole listing, so
// a single malformed note no longer deletes a project's history. That is only
// half the fix: a skip nobody is told about is the same defect one layer down,
// with the outage replaced by a quietly short answer. Every derived series in
// this package computes over FEWER sessions than happened when a note is
// skipped, so the skip belongs in the operator's view of that series.
func warnSkippedSessions(what, project string, skipped []storage.SessionSkip) {
	for _, s := range skipped {
		slog.Warn("session note unreadable; excluded from "+what,
			"project", project, "note", s.Path, "reason", s.Reason)
	}
}
