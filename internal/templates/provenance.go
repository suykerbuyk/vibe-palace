// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"
	"sync"
)

// Provenance is what a vault Templates/ copy of a built-in is, judged by the
// binary alone — no host-local state, no lock, no history walk:
//
//   - ProvenanceCurrent: its bytes are the embedded copy this binary serves;
//   - ProvenanceEarlier: its bytes are a version of the same built-in that a
//     vp binary could have written into a vault (a row of shipped.txt);
//   - ProvenanceOperator: anything else — an operator's override.
//
// ProvenanceOperator is the zero value on purpose: a branch that forgets to
// classify keeps the file. vp removes a Templates/ file only when it can prove
// the bytes are vp's, and never changes operator bytes on its own initiative.
type Provenance int

const (
	// ProvenanceOperator: bytes vp cannot prove it shipped. Kept.
	ProvenanceOperator Provenance = iota
	// ProvenanceCurrent: the embedded copy this binary serves, line endings
	// aside.
	ProvenanceCurrent
	// ProvenanceEarlier: an earlier shipped version of the same built-in,
	// line endings aside.
	ProvenanceEarlier
)

// String is the word the prune rows, Details and commit lines use.
func (p Provenance) String() string {
	switch p {
	case ProvenanceCurrent:
		return "current"
	case ProvenanceEarlier:
		return "earlier"
	default:
		return "operator"
	}
}

// ProvenanceKey is the key a copy is classified by: the hex sha256 of content
// after one pass of CRLF -> LF. A Windows checkout with core.autocrlf=true
// holds a vp mirror with CRLF line endings; pruning a line-ending variant of
// vp's bytes loses nothing, and without the normalisation such a host would
// keep every stale mirror forever. The pass is single on purpose — "\r\r\n"
// becomes "\r\n", not "\n" — and a lone "\r" is kept. The embedded bytes and
// every shipped.txt row are keyed the same way; no shipped blob holds a CR, so
// each row equals a plain sha256sum.
func ProvenanceKey(content []byte) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(string(content), "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}

// ClassifyVaultCopy says what content, found at the vault Templates/ path of
// the built-in relPath ("commands/wrap.md", no "Templates/" prefix), is. It is
// relpath-scoped: wrap.md's bytes at Templates/commands/restart.md are operator
// content. An empty file and a relPath the binary does not embed are operator
// content too.
func ClassifyVaultCopy(relPath string, content []byte) Provenance {
	k := ProvenanceKey(content)
	if cur, ok := EmbeddedSHA(relPath); ok && k == cur {
		return ProvenanceCurrent
	}
	if ShippedVersion(relPath, k) {
		return ProvenanceEarlier
	}
	return ProvenanceOperator
}

// shippedManifest is internal/templates/shipped.txt: every version of every
// built-in that a vp binary could ever have written into a vault, FROZEN at
// the last-writer boundary. See the file's own header.
//
//go:embed shipped.txt
var shippedManifest string

// ShippedVersion reports whether key (a ProvenanceKey) is a shipped version of
// the built-in relPath. It is a function variable — the EmbeddedSHA
// convention — so a test can substitute it and restore it; it exposes a
// membership answer, never the parsed map, so no caller can mutate the cache.
var ShippedVersion func(relPath, key string) bool = realShippedVersion

// shippedRows parses shipped.txt once.
var shippedRows = sync.OnceValue(func() map[string]map[string]bool {
	return parseShipped(shippedManifest)
})

func realShippedVersion(relPath, key string) bool {
	return shippedRows()[relPath][key]
}

// parseShipped reads sha256sum-format rows ("<64 hex>  <relpath>"). "#" lines
// and blank lines are skipped, a trailing "\r" is stripped from every line (a
// CRLF checkout of the file must not turn every key into a miss), and a
// malformed line is skipped rather than panicking: a row that cannot be read
// can only make a copy classify as operator content — kept.
// TestShippedManifestWellFormed makes the skip unreachable for the real file.
func parseShipped(s string) map[string]map[string]bool {
	rows := map[string]map[string]bool{}
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rel, ok := strings.Cut(line, "  ")
		if !ok || len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" || rel == "" {
			continue
		}
		if rows[rel] == nil {
			rows[rel] = map[string]bool{}
		}
		rows[rel][key] = true
	}
	return rows
}
