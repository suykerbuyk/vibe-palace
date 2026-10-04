// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package index_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func payloadKeys(t *testing.T, payload []byte) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("payload is not a JSON object: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Two extractions of one fact that differ only in per-owner fields give the
// same record: the builders take the identity fields alone.
func TestKGBuildersIgnorePerOwnerInput(t *testing.T) {
	a := storage.Triple{Subject: "alice", Predicate: "works_on", Object: "atlas",
		Confidence: 0.6, ValidFrom: "2026-05-10", SourceSession: "s1", ExtractedAt: "2026-05-10T09:00:00Z"}
	b := storage.Triple{Subject: "alice", Predicate: "works_on", Object: "atlas",
		Confidence: 0.9, ValidFrom: "2026-05-03", SourceSession: "s2", ExtractedAt: "2026-05-03T09:00:00Z"}
	idA, pA := index.TripleRecord(a.Subject, a.Predicate, a.Object, a.ValidTo)
	idB, pB := index.TripleRecord(b.Subject, b.Predicate, b.Object, b.ValidTo)
	if idA != idB || string(pA) != string(pB) {
		t.Fatalf("per-owner fields reached the triple record:\n%s %s\n%s %s", idA, pA, idB, pB)
	}

	ea := storage.Entity{ID: "tool-go", Name: "Go", Type: "tool", CreatedAt: "2026-05-10T09:00:00Z"}
	eb := storage.Entity{ID: "tool-go", Name: "Go", Type: "tool", CreatedAt: "2026-05-03T09:00:00Z"}
	eidA, epA := index.EntityRecord(ea.ID, ea.Name, ea.Type)
	eidB, epB := index.EntityRecord(eb.ID, eb.Name, eb.Type)
	if eidA != eidB || string(epA) != string(epB) {
		t.Fatalf("per-owner fields reached the entity record:\n%s %s\n%s %s", eidA, epA, eidB, epB)
	}

	// The payloads hold exactly the owner-invariant fields, and no project.
	if got, want := payloadKeys(t, pA), []string{"object", "origin", "predicate", "subject"}; !slices.Equal(got, want) {
		t.Errorf("triple payload keys = %v, want %v", got, want)
	}
	_, pEnded := index.TripleRecord("alice", "works_on", "atlas", "2026-06-01")
	if got, want := payloadKeys(t, pEnded), []string{"object", "origin", "predicate", "subject", "valid_to"}; !slices.Equal(got, want) {
		t.Errorf("ended triple payload keys = %v, want %v", got, want)
	}
	if got, want := payloadKeys(t, epA), []string{"id", "name", "origin", "type"}; !slices.Equal(got, want) {
		t.Errorf("entity payload keys = %v, want %v", got, want)
	}
	var tp index.TriplePayload
	if err := json.Unmarshal(pA, &tp); err != nil || tp.Origin != index.OriginExtracted {
		t.Errorf("triple payload origin = %q (err %v), want %q", tp.Origin, err, index.OriginExtracted)
	}
}

// Ids are kind-prefixed, separate their fields, and change with every
// identity field.
func TestKGRecordIDs(t *testing.T) {
	id, _ := index.TripleRecord("a", "b", "c", "")
	eid, _ := index.EntityRecord("a", "b", "c")
	if !strings.HasPrefix(id, "t:") || !strings.HasPrefix(eid, "e:") {
		t.Fatalf("ids %q and %q lack their kind prefixes", id, eid)
	}
	if len(id) != 2+32 || len(eid) != 2+32 {
		t.Errorf("ids %q and %q are not prefix + 128-bit hex", id, eid)
	}
	distinct := map[string]bool{}
	for _, f := range [][4]string{
		// Pairs whose plain concatenations are equal: only the separator keeps
		// their ids apart.
		{"ab", "c", "d", ""},
		{"a", "bc", "d", ""},
		{"a", "b", "cd", ""},
		{"a", "b", "c", "d"},
		{"a", "b", "c", ""},
		{"a", "b", "c", "2026-06-01"},
	} {
		id, _ := index.TripleRecord(f[0], f[1], f[2], f[3])
		if distinct[id] {
			t.Errorf("fields %q collide with an earlier set", f)
		}
		distinct[id] = true
	}
	n1, _ := index.EntityRecord("tool-go", "Go", "tool")
	n2, _ := index.EntityRecord("tool-go", "go", "tool")
	n3, _ := index.EntityRecord("tool-go", "Go", "language")
	if n1 == n2 || n1 == n3 || n2 == n3 {
		t.Errorf("entity name and type must be identity: %s %s %s", n1, n2, n3)
	}
}
