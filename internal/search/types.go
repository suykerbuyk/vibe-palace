// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

// SearchResult represents a single search hit with metadata.
type SearchResult struct {
	DrawerID   string  `json:"drawer_id"`
	Content    string  `json:"content"`
	Project    string  `json:"project"`
	Wing       string  `json:"wing"`
	Room       string  `json:"room"`
	Hall       string  `json:"hall"`
	SourceType string  `json:"source_type"`
	SourceRef  string  `json:"source_ref"`
	Date       string  `json:"date"`
	Score      float64 `json:"score"`
}

// SearchFilters constrains a search query.
type SearchFilters struct {
	Project  string `json:"project,omitempty"`
	Wing     string `json:"wing,omitempty"`
	Room     string `json:"room,omitempty"`
	Hall     string `json:"hall,omitempty"`
	DateFrom string `json:"date_from,omitempty"`
	DateTo   string `json:"date_to,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
