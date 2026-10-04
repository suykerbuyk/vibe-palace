// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The two KG record origins (ADR-014 decision 5). An authored record is a
// fact a person stated (vp_kg_add, vp_kg_invalidate) and stays tracked under
// palace/<p>/kg/. An extracted record is derived from an archive or an import
// and lives in the host-local index store.
const (
	OriginAuthored  = "authored"
	OriginExtracted = "extracted"
)

// ClassifyTriple decides a tracked triple's origin. The rules apply in order
// and the first match wins:
//
//  1. an explicit origin is the answer;
//  2. no extracted_at: authored (vp_kg_add never set it);
//  3. extracted_at AND valid_to: authored. That is a pre-migration
//     invalidation edit, which a v8 InvalidateTriple made without dropping
//     extracted_at (X5). It holds for any extracted triple, a mempalace one
//     included;
//  4. otherwise extracted.
//
// It is pure: it reads no archive and no local store.
func ClassifyTriple(t Triple) string {
	switch {
	case t.Origin != "":
		return t.Origin
	case t.ExtractedAt == "":
		return OriginAuthored
	case t.ValidTo != "":
		return OriginAuthored
	}
	return OriginExtracted
}

// ClassifyEntityLine decides a tracked entity line's origin. An explicit
// origin wins. Otherwise the vp_kg_add shape (type "unknown", no created_at)
// is authored and every other line is extracted. A legacy mempalace line of
// that same shape also classifies as authored, which is benign: it stays
// tracked.
func ClassifyEntityLine(e Entity) string {
	switch {
	case e.Origin != "":
		return e.Origin
	case e.Type == "unknown" && e.CreatedAt == "":
		return OriginAuthored
	}
	return OriginExtracted
}

// AddAuthoredTriple is vp_kg_add's writer. It is create-once over every
// TRACKED record at the triple's subject/predicate/object path; the local
// store is never consulted, so a local extracted copy never blocks it.
//
//   - A tracked record ClassifyTriple calls authored: "already exists".
//   - A tracked record it calls extracted (pre-migration only): rewritten as
//     authored. The caller's fields win (confidence, valid_from); the old
//     record's source_session is kept and its extracted_at dropped.
//   - No tracked record: the file is created, as authored.
//
// The extractor's AddTriple is unchanged: it still reports "already exists"
// over any tracked record, which its dedup relies on.
func (v *Vault) AddAuthoredTriple(project string, t Triple) error {
	path, err := v.KGTriplePath(project, t.Subject, t.Predicate, t.Object)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure triples dir: %w", err)
	}
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock triple: %w", err)
	}
	defer release()

	out := t
	out.Origin = OriginAuthored
	out.ExtractedAt = ""
	existing, err := readTripleFile(path)
	switch {
	case err == nil:
		if ClassifyTriple(existing) == OriginAuthored {
			return fmt.Errorf("triple %s/%s/%s already exists", t.Subject, t.Predicate, t.Object)
		}
		out.SourceSession = existing.SourceSession
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return writeTripleFile(v.Root, path, out)
}

// InvalidateAuthoredTriple is vp_kg_invalidate's tracked write. It ends a fact
// by writing an authored record with valid_to = ended. The authored record
// never carries extracted_at, so ClassifyTriple keeps it authored even after a
// v8 binary strips its origin.
//
//   - A tracked triple at the path is rewritten in place: origin authored,
//     valid_to set, source_session kept, extracted_at dropped.
//   - Otherwise local, the caller's pick among the live host-local records of
//     that triple, is written as a new tracked authored file carrying its
//     source_session. The tracked file then hides every local record of that
//     triple.
//   - Neither: an error wrapping fs.ErrNotExist.
//
// The tracked-or-local decision is made here, under the path lock, so a
// concurrent writer cannot slip a tracked file in between.
func (v *Vault) InvalidateAuthoredTriple(project, subject, predicate, object, ended string, local *Triple) error {
	path, err := v.KGTriplePath(project, subject, predicate, object)
	if err != nil {
		return err
	}
	if local != nil && (local.Subject != subject || local.Predicate != predicate || local.Object != object) {
		return fmt.Errorf("invalidate triple: local record %s/%s/%s does not match %s/%s/%s",
			local.Subject, local.Predicate, local.Object, subject, predicate, object)
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure triples dir: %w", err)
	}
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock triple: %w", err)
	}
	defer release()

	t, err := readTripleFile(path)
	switch {
	case err == nil:
	case !errors.Is(err, fs.ErrNotExist):
		return err
	case local != nil:
		t = Triple{Subject: subject, Predicate: predicate, Object: object, SourceSession: local.SourceSession}
	default:
		return fmt.Errorf("triple %s/%s/%s: %w", subject, predicate, object, fs.ErrNotExist)
	}
	t.ValidTo = ended
	t.Origin = OriginAuthored
	t.ExtractedAt = ""
	return writeTripleFile(v.Root, path, t)
}

// writeTripleFile writes a triple in the indented form every tracked triple
// file uses. The caller holds the path lock.
func writeTripleFile(root, path string, t Triple) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal triple: %w", err)
	}
	if err := atomicfile.Write(root, path, data); err != nil {
		return fmt.Errorf("write triple: %w", err)
	}
	return nil
}
