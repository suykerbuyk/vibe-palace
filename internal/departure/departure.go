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
// This is a LEAF package (it imports only internal/slug and internal/gitenv) so that
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

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
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
	// Deleted: the project was discarded from this vault (vp vault project
	// delete --discard) and continues nowhere. To is always empty: there is no
	// destination to name, and a label would read as one.
	//
	// A binary older than this kind reads it as unknown, so its Parse sets
	// Malformed — and Malformed still means departed (Record.Malformed): Read
	// and Find report found for any record file that exists, whatever its
	// kind. An older bind refuses it as unreadable; an older pull guard
	// refuses incoming work under it like any departure.
	Deleted Kind = "deleted"
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

	// Generation counts this slug's departures from this vault: 1 on the
	// first, +1 on each later one. Zero (absent) is a record written before
	// the field existed. The lifecycle commands key primary eligibility on it.
	Generation int `json:"generation,omitempty"`
	// CopyCommit is the destination's copy commit a moved-to-vault delete
	// verified. Informational only: the checks compare footprints, not shas.
	CopyCommit string `json:"copy_commit,omitempty"`
	// Footprint is the departing project's footprint digest, "v1:<hex>".
	Footprint string `json:"footprint,omitempty"`

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
	case Deleted:
		if r.To != "" {
			return fmt.Errorf("departure: a deleted project continues nowhere, so it names no destination (got %q)", r.To)
		}
	default:
		return fmt.Errorf("departure kind %q: want %q, %q or %q", r.Kind, Renamed, MovedToVault, Deleted)
	}
	if r.Generation < 0 {
		return fmt.Errorf("departure generation %d: want 1 or more (or absent)", r.Generation)
	}
	if r.CopyCommit != "" {
		if r.Kind != MovedToVault {
			return fmt.Errorf("departure copy_commit: only a %q record names a copy commit, not %q", MovedToVault, r.Kind)
		}
		if !isLowerHex(r.CopyCommit) || (len(r.CopyCommit) != 40 && len(r.CopyCommit) != 64) {
			return fmt.Errorf("departure copy_commit %q: want a full lowercase hex commit id", r.CopyCommit)
		}
	}
	if r.Footprint != "" {
		if d, ok := strings.CutPrefix(r.Footprint, "v1:"); !ok || len(d) != 64 || !isLowerHex(d) {
			return fmt.Errorf("departure footprint %q: want v1:<64 lowercase hex>", r.Footprint)
		}
	}
	return nil
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
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
func Read(vaultRoot, slug string) (Record, bool) {
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
	return Parse(slug, data), true
}

// Parse decodes the bytes of slug's record, from wherever they came: the
// working tree (Read) or a git object (`git show <ref>:<RelPath(slug)>`, which
// is how a pull reads a record that has not been merged yet). A record that
// does not parse, names another slug or has an unknown kind comes back with
// Malformed set — still a departure, per Record.Malformed.
func Parse(slug string, data []byte) Record {
	return parseKinds(slug, data, knownKind)
}

// knownKind reports whether this binary understands k.
func knownKind(k Kind) bool { return k == Renamed || k == MovedToVault || k == Deleted }

// parseKinds is Parse against a given set of known kinds, so a test can read a
// record the way a binary that predates a kind reads it.
func parseKinds(slug string, data []byte, known func(Kind) bool) Record {
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{Slug: slug, Malformed: err.Error()}
	}
	if rec.Slug != slug {
		// The file's name is the key; a body naming another slug is damage.
		return Record{Slug: slug, Malformed: fmt.Sprintf("record names slug %q", rec.Slug)}
	}
	if !known(rec.Kind) {
		rec.Malformed = fmt.Sprintf("unknown kind %q", rec.Kind)
	}
	return rec
}

// Find reports whether slug is DEPARTED: its departure record exists, of any
// kind, readable or not — whatever Projects/<slug>/ holds.
//
// 🔴 THE RECORD WINS OVER THE DIRECTORY (Chair ruling, U15). It used to be the
// other way round: a directory holding anything git would carry reopened the
// slug, so one stray file — an editor write, a stale host's capture, a `vp
// init` re-scaffold — made a moved project live again in the vault it left.
// Now the only way back is a git revert of the departure's commit (which
// removes the record in the same commit), or a future adopt. The existence
// test is departedpath.RecordExists, the one rule every write funnel refuses
// by; OnlyResidue remains for callers that REPORT what is left behind.
func Find(vaultRoot, slug string) (Record, bool) {
	if vaultRoot == "" || slugpkg.Validate(slug) != nil {
		return Record{}, false
	}
	if !departedpath.RecordExists(vaultRoot, slug) {
		return Record{}, false
	}
	if rec, found := Read(vaultRoot, slug); found {
		return rec, true
	}
	return Record{Slug: slug, Malformed: "the record exists but cannot be inspected"}, true
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

// List returns every DEPARTED slug's record (its record present, whatever its
// directory holds; see Find), sorted by slug. Entries that are not <valid-slug>.json are skipped:
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
