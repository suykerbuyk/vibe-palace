// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// AddEntity appends an entity to the entities JSONL file.
// Rejects duplicates by ID.
func (v *Vault) AddEntity(project string, e Entity) error {
	path, err := v.KGEntitiesFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure kg dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open entities file: %w", err)
	}
	defer f.Close()

	if err := flockFile(f); err != nil {
		return fmt.Errorf("lock entities file: %w", err)
	}
	defer funlockFile(f)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var existing Entity
		if err := json.Unmarshal(scanner.Bytes(), &existing); err != nil {
			continue
		}
		if existing.ID == e.ID {
			return fmt.Errorf("entity %q already exists", e.ID)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan entities file: %w", err)
	}

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal entity: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write entity: %w", err)
	}
	return nil
}

// GetEntity retrieves an entity by ID.
func (v *Vault) GetEntity(project, id string) (Entity, error) {
	entities, err := v.ListEntities(project)
	if err != nil {
		return Entity{}, err
	}
	for _, e := range entities {
		if e.ID == id {
			return e, nil
		}
	}
	return Entity{}, fmt.Errorf("entity %q not found", id)
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
	return os.WriteFile(path, data, 0644)
}

// GetTriple reads a triple by its subject, predicate, and object.
func (v *Vault) GetTriple(project, subject, predicate, object string) (Triple, error) {
	path, err := v.KGTriplePath(project, subject, predicate, object)
	if err != nil {
		return Triple{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Triple{}, fmt.Errorf("read triple: %w", err)
	}

	var t Triple
	if err := json.Unmarshal(data, &t); err != nil {
		return Triple{}, fmt.Errorf("parse triple: %w", err)
	}
	return t, nil
}

// QueryEntity returns triples involving the named entity, filtered by
// direction ("out", "in", or "both") and temporal validity (asOf date).
func (v *Vault) QueryEntity(project, name, asOf, direction string) ([]Triple, error) {
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

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read triple: %w", err)
	}

	var t Triple
	if err := json.Unmarshal(data, &t); err != nil {
		return fmt.Errorf("parse triple: %w", err)
	}

	t.ValidTo = ended

	out, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal triple: %w", err)
	}
	return os.WriteFile(path, out, 0644)
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
