// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package summarize

import "context"

// DispatchSummarizer routes a SummaryItem to the sub-Summarizer registered
// for its Kind, returning ErrUnsupportedKind for any Kind with no registered
// handler. This is the ONLY Summarizer cmd/vp/cmd_drain.go ever constructs in
// production: a KindIteration handler is registered into it in this task; a
// future session-note summarizer registers a KindSessionNote handler into
// the SAME instance once it lands, rather than building separate wiring or a
// second Summarizer type.
type DispatchSummarizer struct {
	Iteration   Summarizer // handles KindIteration; nil = unsupported
	SessionNote Summarizer // handles KindSessionNote; nil = unsupported
}

func (d *DispatchSummarizer) Summarize(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
	switch item.Kind {
	case KindIteration:
		if d.Iteration != nil {
			return d.Iteration.Summarize(ctx, item)
		}
	case KindSessionNote:
		if d.SessionNote != nil {
			return d.SessionNote.Summarize(ctx, item)
		}
	}
	return nil, ErrUnsupportedKind
}
