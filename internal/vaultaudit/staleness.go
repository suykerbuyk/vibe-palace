// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The two axes the nag trips on, whichever comes first.
//
// CHURN, not age alone, is the real signal — and the distinction is the whole
// reason this is not a cron job. A vault that sat idle for three weeks needs no
// audit; a vault that took 60 iterations in four days badly does. Age is kept as
// the second axis to catch the vault that is quiet but drifting (a sync from
// another machine, a hand-edit, a botched merge).
const (
	// StaleChurnThreshold is how many session notes may be written across the
	// WHOLE vault since the last audit before it is considered stale. It is the
	// operator's "50 or so iterations", made well-defined for a vault-global tool.
	StaleChurnThreshold = 50
	// StaleAgeDays matches the stated weekly habit.
	StaleAgeDays = 7
)

// frontmatterHeadBytes bounds the read of an audit report to its frontmatter.
//
// THE AUDIT MAY BE SLOW. THE STALENESS CHECK MAY NOT. This runs inside
// vp_bootstrap_context — the hottest path in the system, which iteration 190 spent
// an entire session taking from ~0.4 s down — so it never scans a report and never
// opens a session note. Same discipline as vplog.Summarize's bounded tail read.
const frontmatterHeadBytes = 512

// Staleness is the audit-staleness verdict that rides in the bootstrap payload.
//
// SILENT WHEN FRESH. Warn is false and Message is empty on a healthy vault, and
// the bootstrap attaches nothing at all. This is not politeness — it is the
// standing NAG-FATIGUE constraint: vp_bootstrap_context now carries FOUR possible
// alerts (friction, vault staleness, log health, audit staleness), and four alerts
// that all fire on a healthy vault is how you train a reader to skim all four. The
// `partial` capture tier (196) and `vp check`'s "[info] resume-caps ... All checks
// passed" (205) are this project's two worked examples of the failure.
type Staleness struct {
	Warn      bool   `json:"warn"`
	Message   string `json:"message,omitempty"`
	LastAudit string `json:"last_audit,omitempty"` // "" ⇒ never audited

	// Churn is the number of session notes written across the vault since the last
	// audit — and it is MEANINGLESS unless ChurnKnown is true.
	//
	// ChurnKnown is false when the newest report predates the session_notes anchor
	// (or was written by a binary that did not stamp it). The two fields exist
	// separately, rather than a single int where 0 means both "no churn" and "no
	// idea", because collapsing them is precisely the bug this nag shipped with for
	// one afternoon: an absent anchor read back as zero and the tool reported the
	// entire corpus as churn. "I have no information" is not "nothing is wrong" —
	// and it is not "634", either.
	Churn      int  `json:"churn"`
	ChurnKnown bool `json:"churn_known"`

	AgeDays float64 `json:"age_days,omitempty"`
}

// CheckStaleness decides whether the vault audit has gone stale.
//
// It is essentially free, by construction:
//
//   - CHURN is a filepath.Glob of session-note FILENAMES — ZERO file reads. (The
//     precedent is NextIteration, which globs basenames and parses NN out of them
//     without ever opening a file.)
//   - The ONLY read is the newest report's frontmatter, capped at frontmatterHeadBytes.
//
// A vault that has NEVER been audited needs no special case, and deliberately gets
// none: its last-audit count is zero, so churn is simply the whole corpus. A fresh
// vault with nine notes stays silent; a vault with four hundred nags. The rule
// falls out of the arithmetic instead of being bolted on beside it.
//
// now is passed in rather than read from the clock so the caller and the test agree
// on what "today" means — the same reason Render takes its date.
func CheckStaleness(vault *storage.Vault, now time.Time) Staleness {
	current := SessionNoteCount(vault)

	lastDate, lastCount, anchored, found := newestReportStamp(vault)
	if !found {
		s := Staleness{Churn: current, ChurnKnown: true}
		if current >= StaleChurnThreshold {
			s.Warn = true
			s.Message = fmt.Sprintf("⚠ this vault has NEVER been audited and holds %d session notes — run `vp audit vault` (or `vp_audit_vault`).", current)
		}
		return s
	}

	s := Staleness{LastAudit: lastDate, ChurnKnown: anchored}
	if t, err := time.Parse("2006-01-02", lastDate); err == nil {
		s.AgeDays = now.Sub(t).Hours() / 24
	}

	// 🔴 AN UNSTAMPED REPORT MEANS CHURN IS UNKNOWN. IT DOES NOT MEAN CHURN IS THE
	// WHOLE CORPUS.
	//
	// This was a live bug in the first cut of this function, and it was caught the
	// only way anything gets caught in this project — by driving the real server and
	// reading what came back. Reports written before the session_notes anchor existed
	// carry no anchor; the missing field read back as 0; and the nag announced "634
	// session notes written since yesterday's audit" on a vault holding 634 notes IN
	// TOTAL. A number that confidently describes something the instrument never
	// measured is the exact defect this epic is named after — and it got into the
	// code written to fix it, which is why invariant 2 is a rule and not a slogan.
	//
	// So: no anchor ⇒ NO CHURN CLAIM. Fall back to the age axis, which is anchored on
	// the report's own date and cannot be faked by an absent field. The next
	// `vp audit vault --write` stamps the anchor and churn starts working — the
	// degradation is temporary, self-healing, and honest while it lasts.
	if anchored {
		// A NEGATIVE churn means notes were DELETED since the last audit. That is not
		// freshness — the corpus moved under the audit's feet, and the report now
		// describes a vault that no longer exists — so its magnitude is what counts.
		if churn := current - lastCount; churn >= 0 {
			s.Churn = churn
		} else {
			s.Churn = -churn
		}
	}

	switch {
	case s.ChurnKnown && s.Churn >= StaleChurnThreshold:
		s.Warn = true
		s.Message = fmt.Sprintf("⚠ vault audit is STALE: %d session notes written across the vault since the %s audit (threshold %d) — run `vp audit vault`.",
			s.Churn, lastDate, StaleChurnThreshold)
	case s.AgeDays >= StaleAgeDays:
		s.Warn = true
		s.Message = fmt.Sprintf("⚠ vault audit is STALE: last run %s, %.0f days ago (threshold %d) — run `vp audit vault`.",
			lastDate, s.AgeDays, StaleAgeDays)
	}
	return s
}

// SessionNoteCount counts session notes across the WHOLE vault by globbing
// filenames. It opens nothing.
//
// It reads Projects/*/sessions/, NOT palace/ — the 206 lesson, which cost this
// project 5 projects and 73 notes of blindness: session notes live under Projects/
// and the palace-only enumerator could not see them. A churn signal that misses
// whole projects would under-count exactly the vaults that need auditing most.
func SessionNoteCount(vault *storage.Vault) int {
	notes, err := filepath.Glob(filepath.Join(vault.Root, "Projects", "*", "sessions", "*.md"))
	if err != nil {
		return 0
	}
	return len(notes)
}

// newestReportStamp returns the date and stamped session-note count of the most
// recent audit report.
//
// Reports are named Audits/<YYYY-MM-DD>-vault-audit.md, so an ISO date sorts
// lexically and the newest is simply the last one.
//
// found is false when no report has ever been written — NOT an error, and not a
// warning by itself. anchored is false when the report carries no session_notes
// stamp, which is a DIFFERENT thing from a stamp of zero: see the comment in
// CheckStaleness, where conflating them shipped a nag that reported a vault's
// entire history as churn.
func newestReportStamp(vault *storage.Vault) (date string, sessionNotes int, anchored, found bool) {
	reports, err := filepath.Glob(filepath.Join(vault.Root, "Audits", "*-vault-audit.md"))
	if err != nil || len(reports) == 0 {
		return "", 0, false, false
	}
	sort.Strings(reports)
	newest := reports[len(reports)-1]

	f, err := os.Open(newest)
	if err != nil {
		return "", 0, false, false
	}
	defer f.Close()

	head := make([]byte, frontmatterHeadBytes)
	n, err := f.Read(head)
	if n == 0 && err != nil {
		return "", 0, false, false
	}

	for _, line := range strings.Split(string(head[:n]), "\n") {
		switch key, val, _ := strings.Cut(line, ":"); strings.TrimSpace(key) {
		case "date":
			date = strings.TrimSpace(val)
		case "session_notes":
			if v, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				sessionNotes, anchored = v, true
			}
		}
	}
	if date == "" {
		// An undated report cannot anchor an age, and its filename is the only other
		// witness. Fall back to it rather than reporting a vault as never-audited.
		date = strings.TrimSuffix(filepath.Base(newest), "-vault-audit.md")
	}
	return date, sessionNotes, anchored, true
}
