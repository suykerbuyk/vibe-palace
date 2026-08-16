// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package scopetoken owns the vault's template placeholder vocabulary and the
// write-side check that keeps an expanded body from being written back over the
// raw one.
//
// It is a leaf: the resolver that EXPANDS tokens and the writers that must
// REFUSE a body which lost them both iterate the same table, so a fifth token
// reaches the expander and the check in one edit. A second copy of the
// vocabulary — a regex, or four string literals in the writer — is the drift
// this package exists to make impossible.
//
// A regex is specifically wrong here: the skill and command corpus carries many
// other {{UPPER}} shapes ({{KNOWN_CONTEXT}}, {{SHA}}, {{PATH}}, {{FOCUS}}, …)
// which are NOT expanded by this package and must stay writable.
package scopetoken

import (
	"fmt"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// Scope carries the values a placeholder expands to. An empty field expands to
// the empty string — which is why token LOSS, not the presence of an expanded
// value, is the only sound way to detect a write-back: three of the four
// placeholders below leave no fingerprint at all when their scope is empty.
type Scope struct {
	Project string
	Wing    string
	Room    string
}

// token binds a placeholder to the value it expands to. Unexported so the table
// stays the single source; callers reach it only through Expand and Lost.
type token struct {
	placeholder string
	value       func(Scope) string
}

// tokens is THE table. Expand and Lost both range over it, so a placeholder
// added here reaches the expander and the write-side guard in one edit.
var tokens = []token{
	{"{{PROJECT}}", func(s Scope) string { return s.Project }},
	{"{{WING}}", func(s Scope) string { return s.Wing }},
	{"{{ROOM}}", func(s Scope) string { return s.Room }},
	{"{{DATE}}", func(Scope) string { return time.Now().Format("2006-01-02") }},
}

// Expand substitutes every placeholder in the table.
//
// It is a blind strings.ReplaceAll per token: it does NOT respect code fences or
// backtick spans, so a placeholder quoted as sample text is substituted like any
// other. That is long-standing behaviour, and it is precisely why writing an
// expanded body back destroys tokens the author meant to keep.
func Expand(content string, s Scope) string {
	for _, t := range tokens {
		content = strings.ReplaceAll(content, t.placeholder, t.value(s))
	}
	return content
}

// Loss records one placeholder that occurs fewer times in the incoming content
// than in the bytes already on disk.
type Loss struct {
	Placeholder string
	OnDisk      int
	Incoming    int
}

// Lost reports every placeholder whose occurrence count DROPS from existing to
// incoming, in table order. A placeholder absent from existing is never
// reported: the rule keys on what the file already holds, which is what keeps
// first writes, `vp init` scaffolds and template materialization out of scope.
//
// Counting, not expanded-value matching, is the whole point — see Scope.
func Lost(existing, incoming string) []Loss {
	var out []Loss
	for _, t := range tokens {
		before := strings.Count(existing, t.placeholder)
		if before == 0 {
			continue
		}
		after := strings.Count(incoming, t.placeholder)
		if after < before {
			out = append(out, Loss{Placeholder: t.placeholder, OnDisk: before, Incoming: after})
		}
	}
	return out
}

// CheckWriteBack refuses a whole-file write whose content drops placeholders the
// file on disk still carries — the signature of a body composed from a
// placeholder-EXPANDING reader (vp_bootstrap_context, vp_get_resume,
// vp_get_workflow, the inlined workflow digest) rather than from the raw bytes.
// Those readers serve expanded content alongside a digest computed over the RAW
// bytes, so such a body passes compare-and-set and bakes the expanded values
// onto disk with no diagnostic.
//
// This is a VERIFY, not a DERIVE (ADR-006): the server checks a body the caller
// produced and cannot see the caller's intent, so the refusal is loud and
// recoverable rather than a silent correction. There is deliberately NO opt-out
// parameter — a required field nothing can check is a field, not enforcement.
// A genuinely intended token removal goes through vp_vault_edit with an
// old_string containing the raw placeholder, which cannot be composed from an
// expanded read and is therefore its own proof of provenance.
//
// Known limits, stated rather than left to be discovered: a delete-then-recreate
// never sees the existing tokens, and a count-preserving rewrite (one
// placeholder dropped, another added) is invisible to a count. Closing either
// needs a first-write heuristic, which would false-positive on real scaffolds.
//
// The error is apperr.Caller: it is a caller-supplied body the file cannot
// accept, the same class as an old_string that does not match, and it must be
// counted as friction rather than as an internal fault.
func CheckWriteBack(relPath, existing, incoming string) error {
	lost := Lost(existing, incoming)
	if len(lost) == 0 {
		return nil
	}
	parts := make([]string, 0, len(lost))
	for _, l := range lost {
		parts = append(parts, fmt.Sprintf("%s (%d on disk, %d incoming)", l.Placeholder, l.OnDisk, l.Incoming))
	}
	return apperr.Caller(fmt.Errorf(
		"scopetoken: refusing write to %s: it drops template placeholders the file still carries: %s. "+
			"This is the signature of a body composed from a placeholder-EXPANDING reader "+
			"(vp_bootstrap_context, vp_get_resume, vp_get_workflow): those serve expanded text "+
			"while their sha256 covers the RAW bytes, so the body passes compare-and-set and bakes "+
			"the expanded values onto disk. Expansion is a blind replace that ignores code fences, "+
			"and {{PROJECT}}/{{WING}}/{{ROOM}} expand to NOTHING when unscoped, so the loss leaves "+
			"no trace in the text. Re-read the file with vp_vault_read, recompose against those raw "+
			"bytes, and resubmit with the sha it returned. To remove a placeholder deliberately, use "+
			"vp_vault_edit with an old_string containing the raw placeholder",
		relPath, strings.Join(parts, ", ")))
}
