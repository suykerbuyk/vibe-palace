// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// Entity represents a person, project, concept, or tool in the knowledge graph.
type Entity struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

// Triple represents a temporal fact in the knowledge graph.
type Triple struct {
	Subject       string  `json:"subject"`
	Predicate     string  `json:"predicate"`
	Object        string  `json:"object"`
	ValidFrom     string  `json:"valid_from,omitempty"`
	ValidTo       string  `json:"valid_to,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	SourceSession string  `json:"source_session,omitempty"`
	ExtractedAt   string  `json:"extracted_at,omitempty"`
}

// KGStats summarizes knowledge graph contents.
type KGStats struct {
	EntityCount    int      `json:"entity_count"`
	TripleCount    int      `json:"triple_count"`
	CurrentFacts   int      `json:"current_facts"`
	ExpiredFacts   int      `json:"expired_facts"`
	PredicateTypes []string `json:"predicate_types"`
}

// maxEntityLine caps a single entities JSONL record for the dedup scan.
// bufio.Scanner's 64 KB default is well above a normal entity — a name, a
// type, and a small properties map — but an entity carrying a large property
// value, or a torn append that concatenates two records into one oversized
// line, exceeds it. The default turns that into bufio.ErrTooLong, and because
// the scan runs on the WRITE path, one such line fails EVERY subsequent
// AddEntity for that project rather than just being skipped; ListEntities
// breaks on the same line. The ceiling stays finite so a corrupt file cannot
// drive an unbounded allocation.
//
// It is deliberately a SEPARATE constant from maxDrawerLine (drawers.go),
// which exists for the same reason on the drawer file: the two files hold
// independent record shapes, and sharing one constant would couple a future
// change to drawer size to the entity reader and back.
const maxEntityLine = 1 << 20

// AddEntity appends an entity to the entities JSONL file, rejecting duplicates
// by ID.
//
// It is the n=1 wrapper over AddEntities, and it exists for the callers whose
// contract is the ERROR rather than the count: the MCP kg_add tool ensures its
// subject and object entities exist and treats "already exists" as success,
// and the mempalace importer reports it the same way. AddEntities deliberately
// does not error on a duplicate, because its caller is a bulk ingest for which
// a duplicate is the normal case, not a failure.
//
// 🔴 The "already exists" substring is a load-bearing contract; callers match
// on it with strings.Contains. Do not reword it.
func (v *Vault) AddEntity(project string, e Entity) error {
	n, err := v.AddEntities(project, []Entity{e})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("entity %q already exists", e.ID)
	}
	return nil
}

// AddEntities appends every entity in es that is not already filed for the
// project, and returns how many it actually appended. Duplicates — against
// what is on disk OR against an earlier entry in es — are skipped silently.
//
// # Why this is the batch shape and not a loop over AddEntity
//
// The per-entity entry point costs O(entities already filed): it reads the
// whole entities JSONL and unmarshals every line to check for a duplicate ID,
// then used to copy that whole buffer plus one line back out through
// atomicfile.Write. Callers invoke it in a loop — the capture indexer once per
// extracted entity, the mempalace importer once per exported entity — so
// writing N entities cost O(N²) bytes in both directions. This entry point
// pays the scan ONCE per (project, batch) and appends the new lines in ONE
// write, which removes the quadratic term without a sidecar index file, a
// persistent ID cache, or any new state to keep coherent. The `seen` set below
// lives for the duration of one call and is discarded with it.
//
// # The write is an append, not a whole-file replace
//
// It routes to appendUnderLock (family F4) rather than atomicfile.Write. The
// lock is acquired HERE and held across the read→dedup→append sequence, which
// is exactly the contract F4 documents: it does not acquire, and a second
// acquire on the same path would block forever. Do not move the acquire into
// the primitive, and do not call it from a caller that is not already holding.
func (v *Vault) AddEntities(project string, es []Entity) (int, error) {
	if len(es) == 0 {
		return 0, nil
	}

	path, err := v.KGEntitiesFile(project)
	if err != nil {
		return 0, err
	}
	// appendUnderLock opens with O_CREATE but does not create parent
	// directories the way atomicfile.Write does, so the kg directory is still
	// this caller's job.
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return 0, fmt.Errorf("ensure kg dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return 0, fmt.Errorf("lock entities file: %w", err)
	}
	defer release()

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("read entities file: %w", err)
	}

	// One scan of the file, not one per entity. A line that does not parse is
	// skipped rather than failing the append: it contributes no ID to dedup
	// against, which is the same thing the per-line `continue` did before.
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(existing))
	scanner.Buffer(make([]byte, 0, 64*1024), maxEntityLine)
	for scanner.Scan() {
		var cur Entity
		if err := json.Unmarshal(scanner.Bytes(), &cur); err != nil {
			continue
		}
		seen[cur.ID] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan entities file: %w", err)
	}

	var buf bytes.Buffer
	appended := 0
	for _, e := range es {
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		line, err := json.Marshal(e)
		if err != nil {
			return 0, fmt.Errorf("marshal entity: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		appended++
	}

	// Every entity in the batch was already filed. Write nothing at all: this
	// is what makes a re-run of an already-ingested transcript cost one read
	// rather than one read plus a whole-file rewrite.
	if appended == 0 {
		return 0, nil
	}

	// 🔴 Heal a missing final newline before appending onto it. Every writer
	// here terminates its lines, so a file that does not end in '\n' ends in a
	// TORN record — the failure mode an append primitive has and a whole-file
	// replace does not. Appending straight onto it would concatenate the torn
	// bytes with the first new record and lose BOTH.
	//
	// This matters MORE here than it does for drawers: readDrawerFile SKIPS a
	// malformed line, so an unhealed torn write there costs one row, whereas
	// ListEntities returns an error on the first line that does not parse — so
	// the same damage makes the ENTIRE knowledge graph unreadable, not one
	// record. Separating them costs one byte.
	out := buf.Bytes()
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		out = append([]byte{'\n'}, out...)
	}

	if err := v.appendUnderLock(path, out); err != nil {
		return 0, fmt.Errorf("write entity: %w", err)
	}
	return appended, nil
}

// ListEntities returns all entities for a project.
func (v *Vault) ListEntities(project string) ([]Entity, error) {
	path, err := v.KGEntitiesFile(project)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open entities file: %w", err)
	}
	defer f.Close()

	var entities []Entity
	scanner := bufio.NewScanner(f)
	// Same ceiling as the dedup scan in AddEntities: the reader and the writer
	// must agree on what a readable line is, or a record one of them accepts is
	// an unrecoverable error to the other.
	scanner.Buffer(make([]byte, 0, 64*1024), maxEntityLine)
	for scanner.Scan() {
		var e Entity
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("parse entity line: %w", err)
		}
		entities = append(entities, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan entities file: %w", err)
	}
	return entities, nil
}

// AddTriple writes a triple as an individual JSON file.
// Rejects duplicates by file-path collision (subject/predicate/object).
// Returns an error containing "already exists" when the triple file already
// exists, matching the dedup-signal shape of AppendDrawer and AddEntity so
// callers can use a uniform strings.Contains predicate to detect skips.
// To mutate an existing triple (e.g. set ValidTo), use InvalidateTriple.
func (v *Vault) AddTriple(project string, t Triple) error {
	path, err := v.KGTriplePath(project, t.Subject, t.Predicate, t.Object)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure triples dir: %w", err)
	}

	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal triple: %w", err)
	}

	// Hold the per-path lock so the create-once stat→write is atomic against
	// other vp writers: atomicfile.Write replaces rather than failing on an
	// existing file, so existence is enforced here rather than by O_EXCL.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock triple: %w", err)
	}
	defer release()

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("triple %s/%s/%s already exists", t.Subject, t.Predicate, t.Object)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat triple file: %w", err)
	}

	if err := atomicfile.Write(v.Root, path, data); err != nil {
		return fmt.Errorf("write triple: %w", err)
	}
	return nil
}

// QueryEntity returns triples involving the named entity, filtered by
// direction ("out", "in", or "both") and temporal validity (asOf date).
func (v *Vault) QueryEntity(project, name, asOf, direction string) ([]Triple, error) {
	if err := v.checkFormatGate(); err != nil {
		return nil, err
	}
	triplesDir, err := v.KGTriplesDir(project)
	if err != nil {
		return nil, err
	}

	encoded := encodeTripleComponent(name)
	var patterns []string

	switch direction {
	case "out", "":
		patterns = append(patterns, filepath.Join(triplesDir, encoded+"--*--*.json"))
	case "in":
		patterns = append(patterns, filepath.Join(triplesDir, "*--*--"+encoded+".json"))
	case "both":
		patterns = append(patterns, filepath.Join(triplesDir, encoded+"--*--*.json"))
		patterns = append(patterns, filepath.Join(triplesDir, "*--*--"+encoded+".json"))
	default:
		return nil, fmt.Errorf("invalid direction %q: must be out, in, or both", direction)
	}

	seen := make(map[string]bool)
	var result []Triple
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob triples: %w", err)
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			t, err := readTripleFile(m)
			if err != nil {
				return nil, err
			}
			if asOf != "" && !tripleValidAt(t, asOf) {
				continue
			}
			result = append(result, t)
		}
	}
	return result, nil
}

// InvalidateTriple sets the ValidTo field on an existing triple.
func (v *Vault) InvalidateTriple(project, subject, predicate, object, ended string) error {
	path, err := v.KGTriplePath(project, subject, predicate, object)
	if err != nil {
		return err
	}

	// Hold the per-path lock across the read→rewrite so concurrent
	// invalidations of the same triple never corrupt the file or lose a write.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock triple: %w", err)
	}
	defer release()

	t, err := readTripleFile(path)
	if err != nil {
		return err
	}

	t.ValidTo = ended

	out, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal triple: %w", err)
	}
	return atomicfile.Write(v.Root, path, out)
}

// Timeline returns all triples involving the named entity, sorted by ValidFrom.
func (v *Vault) Timeline(project, entity string) ([]Triple, error) {
	triples, err := v.QueryEntity(project, entity, "", "both")
	if err != nil {
		return nil, err
	}
	sort.Slice(triples, func(i, j int) bool {
		return triples[i].ValidFrom < triples[j].ValidFrom
	})
	return triples, nil
}

// KGStats returns summary statistics for the knowledge graph.
func (v *Vault) KGStats(project string) (KGStats, error) {
	if err := v.checkFormatGate(); err != nil {
		return KGStats{}, err
	}
	entities, err := v.ListEntities(project)
	if err != nil {
		return KGStats{}, err
	}

	triplesDir, err := v.KGTriplesDir(project)
	if err != nil {
		return KGStats{}, err
	}

	matches, err := filepath.Glob(filepath.Join(triplesDir, "*.json"))
	if err != nil {
		return KGStats{}, fmt.Errorf("glob triples: %w", err)
	}

	var stats KGStats
	stats.EntityCount = len(entities)
	stats.TripleCount = len(matches)

	predicates := make(map[string]bool)
	for _, m := range matches {
		t, err := readTripleFile(m)
		if err != nil {
			return KGStats{}, err
		}
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
	sort.Strings(stats.PredicateTypes)
	return stats, nil
}

// readTripleFile reads and parses a single triple JSON file.
func readTripleFile(path string) (Triple, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Triple{}, fmt.Errorf("read triple %s: %w", path, err)
	}
	var t Triple
	if err := json.Unmarshal(data, &t); err != nil {
		return Triple{}, fmt.Errorf("parse triple %s: %w", path, err)
	}
	return t, nil
}

// tripleValidAt reports whether a triple is valid at the given date string.
// A triple is valid if valid_from <= asOf and (valid_to is empty or valid_to > asOf).
func tripleValidAt(t Triple, asOf string) bool {
	if t.ValidFrom != "" && t.ValidFrom > asOf {
		return false
	}
	if t.ValidTo != "" && strings.Compare(t.ValidTo, asOf) <= 0 {
		return false
	}
	return true
}
