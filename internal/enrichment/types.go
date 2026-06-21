// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

// Result holds the LLM-generated enrichment for a session note.
type Result struct {
	Summary     string
	Decisions   []string
	OpenThreads []string
	Tag         string
}

// enrichmentJSON is the expected JSON structure from the LLM response.
type enrichmentJSON struct {
	Summary     string   `json:"summary"`
	Decisions   []string `json:"decisions"`
	OpenThreads []string `json:"open_threads"`
	Tag         string   `json:"tag"`
}
