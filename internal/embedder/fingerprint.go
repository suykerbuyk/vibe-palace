// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import "fmt"

// BehaviourVersion names the embedding regime: what vector this binary's
// embedder produces for a given input under a given model and max_seq_len.
//
// 🔴 BUMP IT IN THE SAME CHANGE THAT ALTERS EMBEDDING OUTPUT FOR THE SAME
// INPUT: a truncation rule or limit, a pooling or normalisation change, the
// defaultMaxSeqLen fallback, a tokenizer option. Cached vectors carry no
// embedder identity of their own (a drawer vector is keyed by
// md5(wing+content)), so without a bump every host keeps its old vectors next
// to the new ones for good — which is what 23bedcc's truncation did. A bump
// makes each host's cache re-embed every project once (the embed cache compares
// Fingerprint against a per-project sidecar).
//
// 1: truncation to maxSeqLen-2 runes, as introduced by 23bedcc.
// 2: token-level truncation replaces the 254-rune cut
// (embedder-truncates-by-characters-not-tokens).
const BehaviourVersion = 2

// Fingerprint identifies the embedding regime for a model and configured
// max_sequence_length (0 kept as 0: the fallback is covered by
// BehaviourVersion). Two embedders with equal fingerprints produce the same
// vector for the same input, so a cached vector written under one is valid
// under the other.
//
// It is the pure-Go regime — byte-for-byte today's string — and is pinned by
// TestFingerprintGoStringIsByteIdentical. Callers that know the backend use
// FingerprintBackend; this is FingerprintBackend(BackendGo, …).
func Fingerprint(model string, maxSeqLen int) string {
	return FingerprintBackend(BackendGo, model, maxSeqLen)
}

// FingerprintBackend is Fingerprint with the embedding backend folded in.
//
// 🔴 THE GO/DEFAULT REGIME KEEPS TODAY'S STRING BYTE-IDENTICAL (B2, ADR-014
// decision 3 "Phase 11"). fingerprint.go compiles into BOTH binaries and the
// embed cache marks a project stale on any fingerprint-string change, so
// appending a `backend=go` token on the default path would mismatch every
// existing production cache and force every host to re-embed once at the live
// pure-Go rate — the multi-day re-embed this whole task exists to avoid,
// triggered by a routine upgrade of the DEFAULT zero-CGO binary. So the go
// regime omits the field entirely and ONLY the ORT regime appends ` backend=ort`,
// giving ORT-produced vectors their own fingerprint namespace so they can never
// be accepted into a Go cache (or vice versa). On a default binary ResolveBackend
// never yields BackendORT, so a default host always produces the unchanged go
// string regardless of config.
func FingerprintBackend(backend, model string, maxSeqLen int) string {
	base := fmt.Sprintf("vp-embed behaviour=%d model=%s max_seq_len=%d", BehaviourVersion, model, maxSeqLen)
	if normalizeBackend(backend) == BackendORT {
		return base + " backend=ort"
	}
	return base
}
