// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// KG record id prefixes. The store keeps triples and entities in one file,
// kg/records.jsonl, under one flat id space; the prefix keeps a triple id and
// an entity id from ever folding into one record.
const (
	TripleIDPrefix = "t:"
	EntityIDPrefix = "e:"
)

// OriginExtracted is the origin every host-local KG record carries: the store
// holds extracted records only, and authored ones stay tracked.
const OriginExtracted = "extracted"

// TriplePayload is the payload of a host-local KG triple record. It holds only
// the fields that are identical for every owner of the record's id: the
// identity fields and the constant origin. Fields that differ between owners
// (confidence, valid_from, source_session, extracted_at) are not kept; a
// reader derives the date and the session from the record's earliest live
// owner (task authored-and-extracted-knowledge-graph-records, operator ruling
// on C1).
type TriplePayload struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	ValidTo   string `json:"valid_to,omitempty"`
	Origin    string `json:"origin"`
}

// EntityPayload is the payload of a host-local KG entity record: the identity
// fields and the constant origin. Properties and any created_at are not kept.
type EntityPayload struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Origin string `json:"origin"`
}

// TripleRecord builds a host-local KG triple record. Its parameters are the
// identity fields and nothing else, so a per-owner field cannot reach the
// payload, and equal inputs give byte-identical payloads whichever owner
// writes them. That is what makes the store's first-payload-wins fold
// independent of ingest order.
//
// The id is "t:" plus the first 128 bits, in hex, of sha256 over the four
// fields joined by NUL. valid_to is identity: it changes what the fact says.
// Neither the id nor the payload carries a project slug; the project is the
// index directory.
func TripleRecord(subject, predicate, object, validTo string) (id string, payload []byte) {
	id = TripleIDPrefix + kgID(subject, predicate, object, validTo)
	payload = mustMarshal(TriplePayload{
		Subject: subject, Predicate: predicate, Object: object, ValidTo: validTo, Origin: OriginExtracted,
	})
	return id, payload
}

// EntityRecord builds a host-local KG entity record, by the same rule as
// TripleRecord. The id is "e:" plus the first 128 bits, in hex, of sha256 over
// the entity id, name and type joined by NUL: name and type are identity, so
// two exports that disagree on either keep two records rather than one whose
// payload depends on the first writer.
func EntityRecord(entityID, name, typ string) (id string, payload []byte) {
	id = EntityIDPrefix + kgID(entityID, name, typ)
	payload = mustMarshal(EntityPayload{ID: entityID, Name: name, Type: typ, Origin: OriginExtracted})
	return id, payload
}

func kgID(fields ...string) string {
	h := sha256.New()
	for i, f := range fields {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil)[:chunkIDBytes])
}

// mustMarshal marshals a payload struct of strings, which cannot fail.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("index: marshal KG payload: " + err.Error())
	}
	return b
}
