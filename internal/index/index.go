// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package index holds the identities the host-local search index is keyed on
// (ADR-014): the indexer version that the chunk fingerprint hashes, and the
// chunk id.
//
// It is a leaf. It must never import a package that imports internal/capture
// or internal/search (capture imports search), because both of them, and the
// host-local store in internal/indexstore, depend on it.
package index

import (
	"crypto/sha256"
	"encoding/hex"
)

// IndexerVersion is the version of the code that turns an archive into chunks
// and KG records. A change to the chunker, the classifier's use or the
// extractor that changes what is stored bumps it; the chunk fingerprint hashes
// it, so a bump makes every host's store stale (ADR-014 decision 3).
const IndexerVersion = 1

// chunkIDBytes is the width of a chunk id: 128 bits. ADR-014 ("Chunk ids are
// wide content hashes") requires at least 128 so that ids do not collide at the
// corpus sizes the epic measured, where the 32-bit storage.DrawerID does.
const chunkIDBytes = 16

// ChunkID returns the id of a chunk in the host-local store: the first 128
// bits of sha256(content), hex-encoded (32 characters).
//
// It hashes the content ALONE. Wing and room are classification metadata, so
// neither a reclassification nor a project rename changes an id, and equal ids
// mean equal content. Legacy tracked drawers keep their storage.DrawerID; the
// two are never compared.
func ChunkID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:chunkIDBytes])
}
