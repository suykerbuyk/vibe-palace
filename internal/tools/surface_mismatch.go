// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// SurfaceMismatch is the bootstrap payload's report that the VAULT IS AHEAD OF
// THIS BINARY — the structured half of the condition alert. It is attached only
// when the mismatch fires; see the field comment on BootstrapResult.
//
// It carries the REMEDY, not just the diagnosis, and that distinction is the
// entire reason this type exists. "binary v2 < vault v3" is accurate,
// well-formed and useless: a stranded host cannot pull its way out (pulling
// raises the vault floor further) and the MCP server it is talking to is the
// thing that is out of date, so its only route back is text that names the
// command to run.
type SurfaceMismatch struct {
	// BinarySurface is what this binary supports; VaultSurface is the MAX stamp
	// found in the vault. Both, because "you are behind" is not actionable
	// without "behind what" — an operator triaging several writer identities
	// needs to tell a one-version lag from a five.
	BinarySurface int `json:"binary_surface"`
	VaultSurface  int `json:"vault_surface"`

	// StampDir is the directory of the WORST (highest) stamp — the one that
	// raised the floor. It is frequently NOT the project this session is about:
	// CheckCompatible takes the max across every stamp target in the vault, so
	// a sibling project upgraded first is what a host lands on.
	StampDir string `json:"stamp_dir"`

	// Remediation is surface.(*IncompatibleError).Remediation() verbatim — the
	// single source of that prose. Never re-typed here: two independently
	// maintained copies of these lines is the defect this whole change removed.
	Remediation []string `json:"remediation"`

	// Message is the one-line-per-line human form that rides in the alert slot
	// of post_bootstrap_instructions, for a reader that skims the structured
	// fields. Derived from the fields above, never authored separately.
	Message string `json:"message"`
}

// surfaceMismatchMessage renders the alert line for a surface mismatch.
//
// 🔴 IT IS ONE FLOWED LINE, NOT THE ERROR'S MULTI-LINE RENDERING. The alerts
// are joined with a single space into post_bootstrap_instructions, which is one
// prose field; embedding the producer's newlines and four-space margins there
// would put ASCII layout into the middle of a sentence stream that no host
// renders as a block. The remediation LINES are what carry the layout, and they
// ride in the structured field above for a reader that wants them.
//
// The text still comes from Remediation() — the lines are flowed, not rewritten
// — so this cannot drift from the producer the way internal/check/surface.go's
// hand-written copy did.
//
// 🔴 THE DIAGNOSIS HALF IS AS LOAD-BEARING AS THE REMEDY, AND IT IS THE HALF THE
// REMEDY CANNOT RESCUE. Render the two versions in the wrong roles and this says
// the vault is behind the binary; an operator who believes that updates the
// VAULT, which is the exact wrong action and which no amount of correct
// remediation text below it undoes. TestSurfaceMismatchMessageIsExact pins the
// rendered string byte for byte, and TestSurfaceMismatchMessageNamesEachVersionInItsRole
// fails with a readable message when the two are transposed.
func surfaceMismatchMessage(ie *surface.IncompatibleError) string {
	return fmt.Sprintf(
		"🔴 SURFACE MISMATCH: this vp binary supports MCP surface v%d but the vault is at v%d (%s). "+
			"Every mutating vp tool will be REFUSED until this is fixed, and pulling the vault will not "+
			"fix it — the fix is a new binary. %s",
		ie.BinarySurface, ie.VaultSurface, ie.StampDir,
		flowRemediation(ie.Remediation()),
	)
}

// flowRemediation reflows the remediation block into one prose line.
//
// Two things have to happen, and the first one is a MEANING bug rather than a
// cosmetic one.
//
// 🔴 THE LINES ARE THE TWO BRANCHES OF A FORK, AND RUNNING THEM TOGETHER INVERTS
// THE ADVICE. Joined with a bare space the block reads
//
//	action: … git pull && make install if you cannot upgrade right now …
//
// which parses as a CONDITION on `make install` — do this only if you cannot
// upgrade — when `make install` IS the upgrade. The reader most likely to be
// hurt is the one who most needs it: a stranded operator skimming one long
// sentence. So a line gets a real separator unless the line before it ENDS IN A
// COLON, which is exactly the case where it does introduce what follows (the
// framing line and its override command are one clause, and a dash between them
// would be the opposite error).
//
// Second, the block's INTERIOR alignment padding is column layout, not prose.
// strings.TrimSpace does not touch it — "action:    cd …" keeps four spaces
// mid-sentence — so every run of whitespace is collapsed with strings.Fields.
//
// A blank line is deliberately NOT filtered here. Silently swallowing one would
// let a maintainer who separates the two branches with a blank line lose it with
// no signal; the invariant belongs at the producer, and
// TestRemediationIsErrorsOnlyProseSource already fails on a blank remediation
// line. Enforce it there, do not paper over it here.
func flowRemediation(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		flowed := strings.Join(strings.Fields(line), " ")
		if i > 0 {
			if strings.HasSuffix(strings.TrimSpace(lines[i-1]), ":") {
				b.WriteString(" ")
			} else {
				b.WriteString(" — ")
			}
		}
		b.WriteString(flowed)
	}
	return b.String()
}
