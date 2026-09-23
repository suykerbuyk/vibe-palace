// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package departure is the tracked record that a project slug LEFT a vault:
// renamed to another slug in the same vault, or moved to another vault. It is
// the task-move tombstone (storage.MoveProvenance.TombstoneSpec) lifted from
// task scope to project scope: a stale reference into the old name is told
// where the work went instead of finding a hole — or re-creating it.
//
// One file per slug, at Audits/departures/<slug>.json, committed with the
// departure it records:
//
//   - PER SLUG, NOT ONE APPEND-ONLY LOG. The vault has no merge driver (the
//     vp-surface driver was deleted; check.surface_merge_driver refuses one),
//     pull merges with plain `git merge`, and two hosts each appending a line
//     to one file conflict under both merge and rebase. Per-slug files only
//     conflict when two hosts record the SAME slug, which a human should see.
//   - UNDER Audits/, NOT Projects/. A non-slug entry under Projects/ fails
//     merge verify and is read as a project by the vibe-vault migrator;
//     Audits/ is vault-global, shares the one Audits/.surface stamp, and no
//     project enumerator walks it.
//
// This is a LEAF package (it imports only internal/slug) so that
// internal/project — which internal/storage imports, and so cannot import
// storage — can read it, and so the embed-cache sweep and the pull guard can
// read it with nothing but a vault root.
//
// Older binaries never read this file: they are unaffected by it (fail-open),
// and binaries at or after 031b7b7 still refuse a removed slug from git
// history, which remains the fallback. No data-format or surface bump.
package departure

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

// Format tags every record. A reader accepts unknown fields, so a later
// version can add some without a bump; it is here so a future change of
// meaning has something to key on.
const Format = "vp-departure/1"

// Kind is how the slug left.
type Kind string

const (
	// Renamed: the project continues under To, a slug in this same vault.
	Renamed Kind = "renamed"
	// MovedToVault: the project continues in another vault. To is an optional
	// free-text label for it (never a host path: this file syncs to every
	// host).
	MovedToVault Kind = "moved-to-vault"
)

// Dir is the vault-relative directory holding the records.
const Dir = "Audits/departures"

// Record is one departure, exactly as stored.
type Record struct {
	Format     string `json:"format"`
	Slug       string `json:"slug"`
	Kind       Kind   `json:"kind"`
	To         string `json:"to"`
	Date       string `json:"date"`
	BaseCommit string `json:"base_commit,omitempty"`

	// Malformed is set, never stored, when the file exists but does not parse
	// or names an unknown kind. It STILL means departed: the file exists only
	// because something departed, and a parse failure must not reopen the slug.
	Malformed string `json:"-"`
}

// RelPath is the record's vault-relative path, forward-slashed.
func RelPath(slug string) string { return Dir + "/" + slug + ".json" }

func absPath(vaultRoot, slug string) string {
	return filepath.Join(vaultRoot, filepath.FromSlash(RelPath(slug)))
}

// Validate checks a record a writer is about to store.
func (r Record) Validate() error {
	if err := slugpkg.Validate(r.Slug); err != nil {
		return fmt.Errorf("departure slug: %w", err)
	}
	switch r.Kind {
	case Renamed:
		if err := slugpkg.Validate(r.To); err != nil {
			return fmt.Errorf("departure renamed-to: %w", err)
		}
		if r.To == r.Slug {
			return fmt.Errorf("departure: %q cannot be renamed to itself", r.Slug)
		}
	case MovedToVault:
		if err := ValidateLabel(r.To); err != nil {
			return err
		}
	default:
		return fmt.Errorf("departure kind %q: want %q or %q", r.Kind, Renamed, MovedToVault)
	}
	return nil
}

// MaxLabelLen bounds a moved-to-vault label. A remote URL fits comfortably;
// the bound exists because the label is reported inside bootstrap's bounded
// instrument prefix, which has a byte budget.
const MaxLabelLen = 128

// ValidateLabel checks a moved-to-vault destination label. Empty is allowed
// (the destination is simply not recorded). A host path is refused: the record
// syncs to every host, and one host's absolute path means nothing on another —
// write the constraint, never the path. It is printable ASCII without the
// characters JSON escapes (< > & " \), so its reported size is its length.
func ValidateLabel(label string) error {
	if label == "" {
		return nil
	}
	if len(label) > MaxLabelLen {
		return fmt.Errorf("departure destination label is %d bytes, over the %d-byte limit", len(label), MaxLabelLen)
	}
	for _, r := range label {
		if r < 0x20 || r > 0x7e || strings.ContainsRune(`<>&"\`, r) {
			return fmt.Errorf("departure destination label %q: printable ASCII only, without < > & \" or \\", label)
		}
	}
	if filepath.IsAbs(label) || strings.HasPrefix(label, "/") || strings.HasPrefix(label, `\`) ||
		label == "~" || strings.HasPrefix(label, "~/") || strings.HasPrefix(label, `~\`) {
		return fmt.Errorf("departure destination label %q is a host path; name the vault (for example its remote URL) instead", label)
	}
	return nil
}

// Encode renders a validated record, one trailing newline.
func (r Record) Encode() ([]byte, error) {
	r.Format = Format
	if err := r.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Read returns slug's record and whether one exists. It does NOT look at
// Projects/<slug>/: use Find for "is this slug departed". A file that exists
// but cannot be read or parsed is returned with Malformed set and found=true.
func Read(vaultRoot, slug string) (rec Record, found bool) {
	if vaultRoot == "" || slugpkg.Validate(slug) != nil {
		return Record{}, false
	}
	data, err := os.ReadFile(absPath(vaultRoot, slug))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Record{}, false
		}
		return Record{Slug: slug, Malformed: err.Error()}, true
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{Slug: slug, Malformed: err.Error()}, true
	}
	if rec.Slug != slug {
		// The file's name is the key; a body naming another slug is damage.
		return Record{Slug: slug, Malformed: fmt.Sprintf("record names slug %q", rec.Slug)}, true
	}
	if rec.Kind != Renamed && rec.Kind != MovedToVault {
		rec.Malformed = fmt.Sprintf("unknown kind %q", rec.Kind)
	}
	return rec, true
}

// Find reports whether slug is DEPARTED: its record exists AND
// Projects/<slug>/ is absent. The directory wins — a deliberate `vp init`
// re-scaffold reopens the slug without anyone deleting the record, and a later
// departure simply overwrites it.
func Find(vaultRoot, slug string) (Record, bool) {
	if vaultRoot == "" || slugpkg.Validate(slug) != nil {
		return Record{}, false
	}
	if _, err := os.Lstat(filepath.Join(vaultRoot, "Projects", slug)); !errors.Is(err, fs.ErrNotExist) {
		// Present, or not inspectable: either way the slug is not departed.
		return Record{}, false
	}
	return Read(vaultRoot, slug)
}

// maxHops bounds Resolve's walk.
const maxHops = 8

// Resolve follows a chain of renames from slug's own departure to where the
// project lives now: a renamed to b, b renamed to c, resolves to c. It returns
// the chain of records walked, first to last; the last one says where to go.
// It stops at a record whose destination is not itself departed, at a
// moved-to-vault or malformed record, at a cycle, or after maxHops — and in
// every stop case the last record walked is the best answer there is.
// ok is false when slug itself is not departed.
func Resolve(vaultRoot, slug string) (chain []Record, ok bool) {
	rec, found := Find(vaultRoot, slug)
	if !found {
		return nil, false
	}
	chain = append(chain, rec)
	seen := map[string]bool{slug: true}
	for len(chain) < maxHops {
		last := chain[len(chain)-1]
		if last.Malformed != "" || last.Kind != Renamed || seen[last.To] {
			break
		}
		next, found := Find(vaultRoot, last.To)
		if !found {
			break
		}
		seen[last.To] = true
		chain = append(chain, next)
	}
	return chain, true
}

// List returns every DEPARTED slug's record (record present, Projects/<slug>/
// absent), sorted by slug. Entries that are not <valid-slug>.json are skipped:
// one stray file cannot hide the others.
func List(vaultRoot string) []Record {
	if vaultRoot == "" {
		return nil
	}
	ents, err := os.ReadDir(filepath.Join(vaultRoot, filepath.FromSlash(Dir)))
	if err != nil {
		return nil
	}
	var out []Record
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || slugpkg.Validate(name) != nil {
			continue
		}
		if rec, departed := Find(vaultRoot, name); departed {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}
