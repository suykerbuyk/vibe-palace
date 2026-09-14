// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// IterationSummary is one iteration's vault-committed, LLM-generated search
// summary — the cached artifact IterationSummaryFile locates on disk.
//
// MatchIndex records which file-order match of N this summary was generated
// from (wrapstate.ParseEntries's entries are 0-indexed among all entries
// sharing N — see wrapstate.EntriesByN/LastEntryByN). More than one
// iterations.md entry can share the same N (that's exactly why
// wrapstate.LastEntryByN/EntryByNMatch exist); this field lets a reader
// detect when a NEWER entry sharing N has been appended since this summary
// was generated (the newly-computed "current last match index" for N would
// no longer equal this stored value), so a superseded summary can be
// detected and treated as stale rather than silently misattributed to the
// wrong entry.
type IterationSummary struct {
	N           int      `json:"n"`
	MatchIndex  int      `json:"match_index"`
	Summary     string   `json:"summary"`
	Decisions   []string `json:"decisions,omitempty"`
	Unblocks    string   `json:"unblocks,omitempty"`
	Model       string   `json:"model"`
	GeneratedAt string   `json:"generated_at"` // RFC3339
}

// WriteIterationSummary writes (creating or OVERWRITING) one iteration's
// vault-committed search summary. Unlike AddTriple (which refuses to
// overwrite an existing triple), this allows overwrite deliberately: an
// operator-triggered re-summarization (a "--force" re-run, or picking up a
// newly-resolved matchIndex after a superseded entry is detected) must be
// able to replace an existing cache file, not be refused by one.
func (v *Vault) WriteIterationSummary(project string, s IterationSummary) error {
	path, err := v.IterationSummaryFile(project, s.N)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure iteration-summaries dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal iteration summary: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock iteration summary: %w", err)
	}
	defer release()

	if err := atomicfile.Write(v.Root, path, data); err != nil {
		return fmt.Errorf("write iteration summary: %w", err)
	}
	return nil
}

// ReadIterationSummary reads one iteration's cached search summary, if one
// exists. Returns (IterationSummary{}, false, nil) if no cache file exists
// yet — this is the normal, expected state for an entry not yet summarized,
// not an error.
func (v *Vault) ReadIterationSummary(project string, n int) (IterationSummary, bool, error) {
	path, err := v.IterationSummaryFile(project, n)
	if err != nil {
		return IterationSummary{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return IterationSummary{}, false, nil
		}
		return IterationSummary{}, false, fmt.Errorf("read iteration summary: %w", err)
	}
	var s IterationSummary
	if err := json.Unmarshal(data, &s); err != nil {
		return IterationSummary{}, false, fmt.Errorf("unmarshal iteration summary: %w", err)
	}
	return s, true, nil
}
