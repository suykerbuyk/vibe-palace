// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// DrawerQuery is an ALREADY-RESOLVED filter set: every field here is the final
// value to apply, and a zero field means "do not filter on this".
//
// This helper applies NO DEFAULTS OF ITS OWN. The caller's resolver owns
// defaulting (which room an unqualified query lands in), the limit clamp, and
// the decision to prune the walk to a single room. That split is deliberate: if
// defaults also lived down here they would be decided in two places, and the
// two would drift the moment one side changed — and a drifted default is not a
// loud failure, it is a query that quietly scans the wrong subtree and returns
// an empty result the caller cannot tell from an honestly-empty palace. One
// owner, one place to read the policy.
//
// The scan is DETERMINISTIC and EMBEDDER-FREE: exact-match and substring
// predicates over what is already on disk, no ranking, no model. storage sits
// below search, embedder, palace and capture, so none of them may be imported
// here (capture would be an outright import cycle).
type DrawerQuery struct {
	Project     string   // required
	Wing        string   // exact; empty scans every wing present
	Room        string   // exact; empty scans every room in the wing
	Hall        string   // exact; empty matches any hall
	SourceTypes []string // exact-match set; empty matches any source type
	Q           string   // case-insensitive substring over Drawer.Content; empty matches any
	DateFrom    string   // YYYY-MM-DD, inclusive
	DateTo      string   // YYYY-MM-DD, inclusive
	Limit       int      // <= 0 means no limit; the caller clamps
}

// DrawerHit is a drawer plus the path segments that are NOT fields on it.
// Drawer carries no Wing or Room (see storage/drawers.go:22-32) because both
// reach storage as arguments rather than as record content, so a caller
// rendering results from a multi-room walk has no way to say where a hit came
// from unless the walk hands the segments back alongside the record.
type DrawerHit struct {
	Drawer
	Wing string
	Room string
}

// ScanDrawers walks the palace store for one project and returns the drawers
// matching q, newest first.
//
// The walk is ListWings → ListRooms → ListDrawers, pruned by whichever of
// q.Wing and q.Room are set. It does not parse JSONL itself: readDrawerFile
// (drawers.go:405) stays the ONLY drawer parser, and it is reached through
// ListDrawers. A second reader here would be a second place to get the
// tolerate-a-torn-line rule right.
//
// An honestly-empty palace is NOT an error. ListWings (drawers.go:319),
// ListRooms (drawers.go:346) and readDrawerFile (drawers.go:405) each return
// (nil, nil) when their directory or file is absent — they all check
// os.IsNotExist — so a project with no palace store at all, or a wing with no
// such room, yields an empty result and a nil error rather than a failure the
// caller would have to special-case.
func (v *Vault) ScanDrawers(q DrawerQuery) ([]DrawerHit, error) {
	if q.Project == "" {
		return nil, errors.New("project is required")
	}

	// A pinned wing is used directly: ListWings would only enumerate the
	// directory to hand back a name we already have, and it would turn a wing
	// that does not exist yet into "scan nothing" instead of "scan that wing
	// and find nothing", which are the same answer by a different route.
	wings := []string{q.Wing}
	if q.Wing == "" {
		var err error
		wings, err = v.ListWings(q.Project)
		if err != nil {
			return nil, fmt.Errorf("list wings: %w", err)
		}
	}

	// Lowercase the needle ONCE. Doing it inside the drawer loop would fold the
	// same short string on every record in a room that can hold tens of
	// thousands (drawers.go:70-86 measures 20,056 in one real room).
	needle := strings.ToLower(q.Q)

	// The source-type set is likewise built once rather than re-scanned per
	// drawer. Empty stays nil, and a nil set means "match any".
	var sourceTypes map[string]struct{}
	if len(q.SourceTypes) > 0 {
		sourceTypes = make(map[string]struct{}, len(q.SourceTypes))
		for _, st := range q.SourceTypes {
			sourceTypes[st] = struct{}{}
		}
	}

	var hits []DrawerHit
	for _, wing := range wings {
		rooms := []string{q.Room}
		if q.Room == "" {
			var err error
			rooms, err = v.ListRooms(q.Project, wing)
			if err != nil {
				return nil, fmt.Errorf("list rooms in %s: %w", wing, err)
			}
		}

		for _, room := range rooms {
			drawers, err := v.ListDrawers(q.Project, wing, room)
			if err != nil {
				return nil, fmt.Errorf("list drawers in %s/%s: %w", wing, room, err)
			}
			for _, d := range drawers {
				if q.Hall != "" && d.Hall != q.Hall {
					continue
				}
				if sourceTypes != nil {
					if _, ok := sourceTypes[d.SourceType]; !ok {
						continue
					}
				}
				if needle != "" && !strings.Contains(strings.ToLower(d.Content), needle) {
					continue
				}
				if !matchesDateRange(d.FiledAt, q.DateFrom, q.DateTo) {
					continue
				}
				hits = append(hits, DrawerHit{Drawer: d, Wing: wing, Room: room})
			}
		}
	}

	// Sort BEFORE the limit, or the limit keeps an arbitrary N (whatever the
	// directory walk happened to reach first) rather than the newest N.
	// FiledAt is RFC3339, so a lexical compare is a chronological one; ID
	// ascending breaks the tie so two drawers filed in the same second come
	// back in a stable order across runs and across filesystems.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].FiledAt != hits[j].FiledAt {
			return hits[i].FiledAt > hits[j].FiledAt
		}
		return hits[i].ID < hits[j].ID
	})

	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

// matchesDateRange applies the inclusive [from, to] day window to a drawer's
// FiledAt stamp. Both bounds are skipped when empty.
//
// 🔴 The stamp is truncated to its 10-byte YYYY-MM-DD prefix BEFORE the
// compare. Drawer.FiledAt is a full RFC3339 timestamp (drawers.go:29), so
// comparing it directly against a date-only bound drops every drawer filed ON
// the closing day: "2026-09-08T14:23:00Z" > "2026-09-08" lexically, so a
// DateTo of "2026-09-08" would exclude everything filed after midnight that
// day. This is a real bug class in this repo, not a hypothetical — search's
// makeDrawerMeta (search/engine.go:702-705) truncates for exactly the same
// reason. The length guard keeps a short or empty stamp from panicking; such a
// record simply fails a bounded query rather than taking the scan down.
func matchesDateRange(filedAt, from, to string) bool {
	if from == "" && to == "" {
		return true
	}
	date := filedAt
	if len(date) >= 10 {
		date = date[:10]
	}
	if from != "" && date < from {
		return false
	}
	if to != "" && date > to {
		return false
	}
	return true
}
