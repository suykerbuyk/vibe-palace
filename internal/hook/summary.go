// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

// autoSummaryHonest is the crash-net placeholder written by Stop/SessionEnd
// auto-capture. It must not embed git subjects: a git log attributes prior
// work to this session (task auto-capture-notes-…).
const autoSummaryHonest = "Auto-captured session (no summary yet)"

// AutoSummary returns an honest crash-net placeholder for hook auto-capture.
// cwd is retained so call sites stay stable; it is not read.
func AutoSummary(cwd string) string {
	_ = cwd
	return autoSummaryHonest
}
