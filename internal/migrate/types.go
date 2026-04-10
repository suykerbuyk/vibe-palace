// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

// ProgressType identifies the kind of progress event.
type ProgressType int

const (
	ProgressProjectStart ProgressType = iota
	ProgressSessionDone
	ProgressSessionSkip
	ProgressProjectDone
	ProgressError
)

// ProgressEvent is emitted during migration to report progress.
type ProgressEvent struct {
	Type      ProgressType
	Project   string
	SessionID string
	Message   string
	Current   int
	Total     int
}

// ProgressFunc is a callback for receiving progress events.
type ProgressFunc func(ProgressEvent)

// ImportResult summarizes the outcome of a migration run.
type ImportResult struct {
	ProjectsScanned  int
	SessionsImported int
	SessionsSkipped  int
	DrawersCreated   int
	EntitiesCreated  int
	TriplesCreated   int
	Errors           []ImportError
}

// ImportError records a single failure during migration.
type ImportError struct {
	Project   string
	SessionID string
	File      string
	Err       error
}

// ImportOptions configures a migration run.
type ImportOptions struct {
	DryRun   bool
	Progress ProgressFunc
}
