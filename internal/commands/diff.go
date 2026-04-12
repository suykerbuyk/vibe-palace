// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// RenderUnified returns a unified-diff string comparing old and new with the
// supplied labels. Three lines of context, LF line endings. Returns an empty
// string when the inputs are identical.
func RenderUnified(oldLabel, newLabel, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	d := difflib.UnifiedDiff{
		A:        splitLines(oldContent),
		B:        splitLines(newContent),
		FromFile: oldLabel,
		ToFile:   newLabel,
		Context:  3,
	}
	out, err := difflib.GetUnifiedDiffString(d)
	if err != nil {
		return ""
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	return lines
}
