// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// sessionStemFP matches a host-qualified session filename stem
// "YYYY-MM-DD-<fp>-NN" and captures the 8-hex writer fingerprint. Legacy
// host-agnostic notes ("YYYY-MM-DD-NN") deliberately do not match: they carry no
// identity, and counting them under any host would invent an attribution.
var sessionStemFP = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-([0-9a-f]{8})-\d+\.md$`)

// writerIdentityWhy is the derivation, emitted only when the Info branch fires.
// It is the need-to-know path for a rule that used to be carried as prose in a
// project's workflow.md and enforced by nothing.
const writerIdentityWhy = "A writer fingerprint is sha256(hostname + vaultPath)[:8] " +
	"(internal/surface/version.go, WriterFingerprint) — it hashes the VAULT PATH as well as the host, so " +
	"MOVING A VAULT SILENTLY CHANGES THAT MACHINE'S IDENTITY and its future notes stop matching its past " +
	"ones. The raw hostname is never written to the vault, so nothing here can map a fingerprint back to a " +
	"machine name: this row is the only way to learn which identity is yours, and it is COMPUTED for the " +
	"vault actually bound right now rather than inferred from what the notes look like."

// CheckWriterIdentity reports the writer fingerprint this host writes under for
// the bound vault, and how the vault's existing sessions divide among identities.
//
// It exists so identity never has to be INFERRED. The fingerprint is a hash of
// hostname+vaultPath that appears in every session filename, and before this row
// no vp surface printed it: an agent asking "which of these notes are mine?" had
// to guess from dates and content, or hand-roll the sha256 in a shell. Guessing
// is what produced the iteration-277 misattribution.
//
// Pass carries the answer in its Summary — visible in `vp check` output without
// being an alert. Info fires only for the one genuinely notable state: this host
// has written nothing under its current identity while other identities exist,
// which is what a relocated vault (or a fresh clone) looks like from the inside.
func CheckWriterIdentity(vaultRoot string) Result {
	const name = "Writer identity"
	if vaultRoot == "" {
		return Result{Name: name, Status: Skip, Summary: "no vault configured"}
	}

	fp := surface.WriterFingerprint(vaultRoot)
	counts, err := countSessionFingerprints(vaultRoot)
	if err != nil {
		// The error text is DROPPED, not wrapped: an os error carries the absolute
		// path it failed on, and that is the one string this row must never emit.
		return Result{Name: name, Status: Skip, Summary: "cannot enumerate sessions under Projects/"}
	}

	mine := counts[fp]
	total := 0
	others := 0
	for f, n := range counts {
		total += n
		if f != fp {
			others++
		}
	}

	// The vault ROOT is deliberately absent from every line below. This row is
	// delivered at session start on the restart/wrap selector lists, so a
	// host-rooted absolute path here is one copy-paste from a synced resume.md —
	// which is the exact disease `vault-abs-paths` exists to catch, reintroduced
	// by the check meant to stop inference. 277's corollary: write the
	// CONSTRAINT, never the PATH. The fingerprint IS the constraint.
	var details []string
	for _, f := range sortedKeys(counts) {
		marker := ""
		if f == fp {
			marker = "  <- this host"
		}
		details = append(details, fmt.Sprintf("  %s: %d session(s)%s", f, counts[f], marker))
	}

	// Info only for the notable case. A healthy multi-host vault where this host
	// has written before is the normal state and must stay quiet: a row that
	// fires on every healthy vault teaches the reader to skim it.
	if mine == 0 && total > 0 {
		return Result{
			Name:    name,
			Status:  Info,
			Summary: fmt.Sprintf("this host (%s) has written no sessions; %d exist under %d other identity(ies)", fp, total, others),
			Details: append(details, "", writerIdentityWhy),
		}
	}

	return Result{
		Name:    name,
		Status:  Pass,
		Summary: fmt.Sprintf("this host writes as %s (%d of %d session(s))", fp, mine, total),
		Details: details,
	}
}

// countSessionFingerprints tallies session notes per writer fingerprint across
// every project in the vault. A missing Projects/ or sessions/ directory is an
// absence, not an error — a fresh vault has written nothing yet.
func countSessionFingerprints(vaultRoot string) (map[string]int, error) {
	counts := map[string]int{}
	projectsDir := filepath.Join(vaultRoot, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return counts, nil
		}
		return nil, err
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(projectsDir, p.Name(), "sessions"))
		if err != nil {
			continue // no sessions dir for this project: an absence, not a blindness
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if m := sessionStemFP.FindStringSubmatch(e.Name()); m != nil {
				counts[m[1]]++
			}
		}
	}
	return counts, nil
}

// sortedKeys returns the map's keys in a stable order so the detail rows do not
// reshuffle between runs.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
