// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package kgread answers knowledge-graph queries from the union of a project's
// tracked KG files (palace/<p>/kg/, read through internal/storage) and its
// host-local index store (palace/.local/index/<p>/kg/records.jsonl, read
// through internal/indexstore). It sits above both because indexstore imports
// storage, so storage cannot read the local store (ADR-014 decision 5; task
// authored-and-extracted-knowledge-graph-records).
//
// The rules it applies:
//   - Every entry point calls the vault data-format gate first.
//   - Only local records with a live owner are read (Store.KG(true)).
//   - A local record's date and session are derived at read time from its
//     earliest live owner, through Ledger.LiveOwner. The store keeps one
//     owner-invariant payload per record id, so nothing in a payload depends on
//     which owner wrote it first.
//   - Local triples that share subject, predicate and object are collapsed to
//     one (pickForRead), and invalidation chooses among them with
//     pickForInvalidate.
//   - A tracked record wins over local ones: a tracked triple hides every
//     local triple with its subject/predicate/object, and a tracked entity
//     line hides every local entity record with its id.
//   - A process caches each project's decoded local records with the store
//     change counter read BEFORE the snapshot, and reloads in full on any
//     change to it. The cache entry also holds what every read derives from
//     the records (the collapsed triples, the sorted entities), built once
//     per load, so a warm read does not redo them.
package kgread

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// localTriple is a decoded local triple record and its record id.
type localTriple struct {
	id  string
	t   storage.Triple
	day string // the earliest live owner's start day, YYYY-MM-DD
}

// localEntity is a decoded local entity record and its record id.
type localEntity struct {
	id string
	e  storage.Entity
}

// localKG is one project's decoded local KG, valid for one store counter.
// It is built complete under cacheMu, then published, and never written
// again: a reload replaces it, so a caller holding one reads a consistent
// load.
type localKG struct {
	gen         indexstore.Gen
	triples     []localTriple // every live record, as decoded (invalidation)
	readTriples []localTriple // triples collapsed for read (collapseFn)
	entities    []localEntity // sorted by entity id, then record id
	skips       []storage.RecordSkip
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*localKG{}

	// readKGFn is the one store read, a seam so a test can count reloads. It
	// is the KG-only snapshot: the ledger and the KG records, never the
	// chunks, which the KG view does not depend on.
	readKGFn = indexstore.ReadKG

	// collapseFn is the read collapse, run once per load (loadLocal) and
	// nowhere else; a seam so a test can count collapses.
	collapseFn = collapseForRead
)

// loadLocal returns the project's decoded local KG. It reads the store
// counter first, then the snapshot (the indexstore reader rule), and reuses a
// cached decode only while the counter is unchanged; any change to gen or
// epoch is a full reload.
func loadLocal(v *storage.Vault, project string) (*localKG, error) {
	g, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		return nil, fmt.Errorf("read index counter: %w", err)
	}
	key := v.Root + "\x00" + project
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := cache[key]; ok && c.gen == g {
		return c, nil
	}
	st, err := readKGFn(v, project)
	if err != nil {
		return nil, fmt.Errorf("read index store: %w", err)
	}
	kg, err := decodeStore(st, project)
	if err != nil {
		return nil, err
	}
	kg.gen = g
	kg.readTriples = collapseFn(kg.triples)
	slices.SortFunc(kg.entities, func(a, b localEntity) int {
		if c := strings.Compare(a.e.ID, b.e.ID); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	cache[key] = kg
	return kg, nil
}

// decodeStore decodes every local KG record that has a live owner, dating
// each from its earliest live owner.
func decodeStore(st *indexstore.KGSnapshot, project string) (*localKG, error) {
	led := st.Ledger()
	kg := &localKG{}
	for _, rec := range st.KG(true) {
		session, day, ok := earliestLiveOwner(led, rec.Owners)
		if !ok {
			continue
		}
		date := day + "T00:00:00Z"
		switch {
		case strings.HasPrefix(rec.ID, index.TripleIDPrefix):
			var p index.TriplePayload
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode local triple %s of %s: %w", rec.ID, project, err)
			}
			kg.triples = append(kg.triples, localTriple{id: rec.ID, day: day, t: storage.Triple{
				Subject: p.Subject, Predicate: p.Predicate, Object: p.Object, ValidTo: p.ValidTo,
				SourceSession: session, ExtractedAt: date, Origin: p.Origin,
			}})
		case strings.HasPrefix(rec.ID, index.EntityIDPrefix):
			var p index.EntityPayload
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				kg.skips = append(kg.skips, storage.RecordSkip{
					Path:   "palace/.local/index/" + project + "/kg#" + rec.ID,
					Reason: err.Error(),
				})
				continue
			}
			kg.entities = append(kg.entities, localEntity{id: rec.ID, e: storage.Entity{
				ID: p.ID, Name: p.Name, Type: p.Type, CreatedAt: date, Origin: p.Origin,
			}})
		}
	}
	return kg, nil
}

// earliestLiveOwner picks, among the owners Ledger.LiveOwner reports live,
// the one with the earliest start day; ties go to the smaller owner kind, then
// the smaller sha or batch id. That is indexstore's ownerBefore order, so the
// result is the same whatever order the owners were ingested in.
func earliestLiveOwner(led *indexstore.Ledger, owners []indexstore.Owner) (session, day string, ok bool) {
	var best indexstore.Owner
	for _, o := range owners {
		s, d, live := led.LiveOwner(o)
		if !live {
			continue
		}
		if ok && !ownerBefore(d, o, day, best) {
			continue
		}
		best, session, day, ok = o, s, d, true
	}
	return session, day, ok
}

func ownerBefore(dayA string, a indexstore.Owner, dayB string, b indexstore.Owner) bool {
	if dayA != dayB {
		return dayA < dayB
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return ownerKey(a) < ownerKey(b)
}

func ownerKey(o indexstore.Owner) string {
	if o.Kind == indexstore.OwnerArchive {
		return o.SHA
	}
	return o.ID
}

// spo is a triple's identity in the tracked tree: one file per
// subject/predicate/object.
type spo struct{ s, p, o string }

func keyOf(t storage.Triple) spo { return spo{t.Subject, t.Predicate, t.Object} }

// pick is the one ordering both picks share: a record the preferred predicate
// holds for beats one it does not, and otherwise the smaller record id wins.
// It reads only record content and ids, never store order.
func pick(a, b localTriple, preferred func(localTriple) bool) localTriple {
	if pa, pb := preferred(a), preferred(b); pa != pb {
		if pa {
			return a
		}
		return b
	}
	if b.id < a.id {
		return b
	}
	return a
}

func hasEnd(lt localTriple) bool    { return lt.t.ValidTo != "" }
func isCurrent(lt localTriple) bool { return lt.t.ValidTo == "" }

// pickForRead is the readers' and the stats' choice among local records that
// share subject/predicate/object: the one with an explicit valid_to first,
// because an explicit end beats a later re-mention of the fact; else the
// smaller record id.
func pickForRead(a, b localTriple) localTriple { return pick(a, b, hasEnd) }

// pickForInvalidate is invalidation's choice: the one with an empty valid_to
// first, because invalidating must end a fact that is still current; else the
// smaller record id.
func pickForInvalidate(a, b localTriple) localTriple { return pick(a, b, isCurrent) }

// collapseForRead keeps one local record per subject/predicate/object, chosen
// by pickForRead, sorted by subject, predicate and object.
func collapseForRead(recs []localTriple) []localTriple {
	byKey := map[spo]localTriple{}
	for _, r := range recs {
		k := keyOf(r.t)
		if cur, seen := byKey[k]; seen {
			r = pickForRead(cur, r)
		}
		byKey[k] = r
	}
	out := make([]localTriple, 0, len(byKey))
	for _, r := range byKey {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b localTriple) int { return compareSPO(a.t, b.t) })
	return out
}

func compareSPO(a, b storage.Triple) int {
	if c := strings.Compare(a.Subject, b.Subject); c != 0 {
		return c
	}
	if c := strings.Compare(a.Predicate, b.Predicate); c != 0 {
		return c
	}
	return strings.Compare(a.Object, b.Object)
}

// dated is a triple of the union together with the date Timeline orders it
// by: a tracked triple's valid_from, or, for a local triple, which carries no
// valid_from, its read-time derived day (Chair ruling on Timeline chronology).
type dated struct {
	t    storage.Triple
	when string
}

// datedUnion is tracked followed by every collapsed local triple that no
// tracked triple hides.
func datedUnion(tracked []storage.Triple, local []localTriple) []dated {
	hidden := make(map[spo]bool, len(tracked))
	out := make([]dated, 0, len(tracked)+len(local))
	for _, t := range tracked {
		hidden[keyOf(t)] = true
		out = append(out, dated{t: t, when: t.ValidFrom})
	}
	for _, lt := range local {
		if !hidden[keyOf(lt.t)] {
			out = append(out, dated{t: lt.t, when: lt.day})
		}
	}
	return out
}

// union is datedUnion without the dates.
func union(tracked []storage.Triple, local []localTriple) []storage.Triple {
	d := datedUnion(tracked, local)
	out := make([]storage.Triple, len(d))
	for i, x := range d {
		out[i] = x.t
	}
	return out
}

// matches reports whether a triple involves name in the given direction, by
// the rule storage.QueryEntity applies to tracked files (an exact match on the
// subject, the object, or either).
func matches(t storage.Triple, name, direction string) bool {
	switch direction {
	case "out", "":
		return t.Subject == name
	case "in":
		return t.Object == name
	}
	return t.Subject == name || t.Object == name
}

// validAt is storage's as_of rule: valid when valid_from <= asOf and valid_to
// is empty or later than asOf.
func validAt(t storage.Triple, asOf string) bool {
	if t.ValidFrom != "" && t.ValidFrom > asOf {
		return false
	}
	return t.ValidTo == "" || t.ValidTo > asOf
}

// QueryEntity returns the triples involving name, filtered by direction
// ("out", "in" or "both") and by validity at asOf, from the union.
func QueryEntity(v *storage.Vault, project, name, asOf, direction string) ([]storage.Triple, error) {
	if err := v.CheckFormatGate(); err != nil {
		return nil, err
	}
	return queryEntity(v, project, name, asOf, direction)
}

func queryEntity(v *storage.Vault, project, name, asOf, direction string) ([]storage.Triple, error) {
	all, err := involving(v, project, name, direction)
	if err != nil {
		return nil, err
	}
	var out []storage.Triple
	for _, d := range all {
		if asOf == "" || validAt(d.t, asOf) {
			out = append(out, d.t)
		}
	}
	return out, nil
}

// involving is the dated union of the triples involving name in direction.
func involving(v *storage.Vault, project, name, direction string) ([]dated, error) {
	// The tracked half is read with no as_of filter: a tracked triple that has
	// ended must still hide its local copies.
	tracked, err := v.QueryEntity(project, name, "", direction)
	if err != nil {
		return nil, err
	}
	local, err := loadLocal(v, project)
	if err != nil {
		return nil, err
	}
	var involved []localTriple
	for _, lt := range local.readTriples {
		if matches(lt.t, name, direction) {
			involved = append(involved, lt)
		}
	}
	return datedUnion(tracked, involved), nil
}

// Timeline returns every triple involving entity, from the union, stable-sorted
// by date, then extracted_at, subject, predicate and object. A tracked
// triple's date is its valid_from. A local triple carries no valid_from, so its
// date is its read-time derived day, the earliest live owner's start day (Chair
// ruling, 2026-10-03): without it every local triple would sort before every
// dated authored one, and a mixed timeline would not be chronological.
func Timeline(v *storage.Vault, project, entity string) ([]storage.Triple, error) {
	if err := v.CheckFormatGate(); err != nil {
		return nil, err
	}
	all, err := involving(v, project, entity, "both")
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(all, func(a, b dated) int {
		if c := strings.Compare(a.when, b.when); c != 0 {
			return c
		}
		if c := strings.Compare(a.t.ExtractedAt, b.t.ExtractedAt); c != 0 {
			return c
		}
		return compareSPO(a.t, b.t)
	})
	out := make([]storage.Triple, len(all))
	for i, d := range all {
		out[i] = d.t
	}
	return out, nil
}

// ListTriples returns every triple of the project, from the union.
func ListTriples(v *storage.Vault, project string) ([]storage.Triple, error) {
	if err := v.CheckFormatGate(); err != nil {
		return nil, err
	}
	return listTriples(v, project)
}

func listTriples(v *storage.Vault, project string) ([]storage.Triple, error) {
	tracked, err := v.ListTriples(project)
	if err != nil {
		return nil, err
	}
	local, err := loadLocal(v, project)
	if err != nil {
		return nil, err
	}
	return union(tracked, local.readTriples), nil
}

// ListEntities returns every entity of the project, from the union, one row
// per record: a local entity id can appear with two names or types. A tracked
// line hides every local record with its id. The skips name tracked lines and
// local records the reader could not decode.
func ListEntities(v *storage.Vault, project string) ([]storage.Entity, []storage.RecordSkip, error) {
	if err := v.CheckFormatGate(); err != nil {
		return nil, nil, err
	}
	return listEntities(v, project)
}

func listEntities(v *storage.Vault, project string) ([]storage.Entity, []storage.RecordSkip, error) {
	tracked, skips, err := v.ListEntities(project)
	if err != nil {
		return nil, nil, err
	}
	local, err := loadLocal(v, project)
	if err != nil {
		return nil, nil, err
	}
	hidden := make(map[string]bool, len(tracked))
	for _, e := range tracked {
		hidden[e.ID] = true
	}
	out := tracked
	for _, le := range local.entities {
		if !hidden[le.e.ID] {
			out = append(out, le.e)
		}
	}
	return out, append(skips, local.skips...), nil
}

// KGStats summarises the union. TripleCount, CurrentFacts and ExpiredFacts
// count the collapsed set, so a subject/predicate/object is counted once.
// EntityCount is the number of distinct entity ids.
func KGStats(v *storage.Vault, project string) (storage.KGStats, error) {
	if err := v.CheckFormatGate(); err != nil {
		return storage.KGStats{}, err
	}
	entities, skips, err := listEntities(v, project)
	if err != nil {
		return storage.KGStats{}, err
	}
	triples, err := listTriples(v, project)
	if err != nil {
		return storage.KGStats{}, err
	}
	var stats storage.KGStats
	ids := map[string]bool{}
	for _, e := range entities {
		ids[e.ID] = true
	}
	stats.EntityCount = len(ids)
	for _, sk := range skips {
		stats.SkippedRecords = append(stats.SkippedRecords, sk.Path)
	}
	stats.TripleCount = len(triples)
	predicates := map[string]bool{}
	for _, t := range triples {
		predicates[t.Predicate] = true
		if t.ValidTo == "" {
			stats.CurrentFacts++
		} else {
			stats.ExpiredFacts++
		}
	}
	for p := range predicates {
		stats.PredicateTypes = append(stats.PredicateTypes, p)
	}
	slices.Sort(stats.PredicateTypes)
	return stats, nil
}

// InvalidateTriple ends a fact as of ended. If a tracked triple exists at the
// subject/predicate/object path, storage rewrites it as authored. Otherwise the
// live local record chosen by pickForInvalidate becomes a tracked authored
// file, which then hides every local record of that triple. With neither, the
// error wraps fs.ErrNotExist.
func InvalidateTriple(v *storage.Vault, project, subject, predicate, object, ended string) error {
	if err := v.CheckFormatGate(); err != nil {
		return err
	}
	local, err := loadLocal(v, project)
	if err != nil {
		return err
	}
	want := spo{subject, predicate, object}
	var chosen *localTriple
	for _, lt := range local.triples {
		if keyOf(lt.t) != want {
			continue
		}
		if chosen == nil {
			c := lt
			chosen = &c
			continue
		}
		c := pickForInvalidate(*chosen, lt)
		chosen = &c
	}
	var rec *storage.Triple
	if chosen != nil {
		rec = &chosen.t
	}
	return v.InvalidateAuthoredTriple(project, subject, predicate, object, ended, rec)
}
